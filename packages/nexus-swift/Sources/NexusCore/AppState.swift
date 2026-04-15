import Foundation
import Combine
import os

/// Root app state — owns the daemon client and drives all views.
/// Always connects to the real daemon. If the daemon isn't running,
/// connectionState reflects .disconnected and an error message is set.
@MainActor
public final class AppState: ObservableObject {
    private static let logger = Logger(subsystem: "com.nexus.NexusApp", category: "AppState")

    // MARK: - PTY state (tracked for XCUITest via sidebar accessibility markers)

    public enum PTYState {
        case idle    // workspace stopped / no workspace selected
        case active  // PTY session open
        case error   // PTY failed
    }

    @Published public var ptyState: PTYState = .idle
    // Set by DaemonPTYTerminalView to re-focus the terminal NSView when the
    // sidebar terminal_view button is clicked in XCUITest.
    public var refocusTerminalAction: (() -> Void)?

    public func refocusTerminal() { refocusTerminalAction?() }

    /// Live terminal title from shell escape sequences (e.g. `\033]0;…\007`).
    /// Nil when no PTY is active or the shell has not set a title.
    @Published public var terminalTitle: String?

    /// Live working directory reported by the shell (OSC 7 / `hostCurrentDirectoryUpdate`).
    /// Nil when not reported.
    @Published public var terminalDirectory: String?

    // MARK: - Published state
    @Published public var repos: [Repo] = []
    @Published public var projects: [Project] = []
    @Published public var selectedWorkspaceID: String?
    @Published public var connectionState: ConnectionState = .disconnected
    @Published public var daemonStatus: DaemonStatus = .unknown
    @Published public var showNewWorkspace = false
    @Published public var newSandboxProjectID: String?
    @Published public var sidebarVisible = true
    @Published public var showInspector = true
    @Published public var error: String?

    // MARK: - Client
    public private(set) var client: any DaemonClient

    private var refreshTask: Task<Void, Never>?
    private var isRestarting = false
    @Published public var isBusy = false

    private var injectedDaemonURL: String? {
        let value = ProcessInfo.processInfo.environment["NEXUS_DAEMON_URL"]?
            .trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
        return value.isEmpty ? nil : value
    }

    public init() {
        self.client = WebSocketDaemonClient(daemonURL: WebSocketDaemonClient.discoverURL())
        connectionState = .starting
        Task { await self.ensureDaemonAndLoad() }
        startRefreshLoop()
    }

    // Designated for dependency injection in tests only
    public init(client: any DaemonClient) {
        self.client = client
        Task { await self.load() }
    }

    private func applyLoadedWorkspaces(_ workspaces: [Workspace], relations: [RelationsGroup], projects: [Project]) {
        self.projects = projects
        let projectRepos = Repo.fromProjects(projects, workspaces: workspaces)
        repos = projectRepos.isEmpty ? Repo.fromRelations(relations, workspaces: workspaces) : projectRepos
        connectionState = .connected
        error = nil

        if selectedWorkspaceID == nil {
            selectedWorkspaceID = repos.first?.workspaces.first?.id
        }
    }

    // MARK: - Load

    public func load() async {
        connectionState = .connecting
        Self.logger.debug("load() started")
        do {
            async let wsFetch        = client.listWorkspaces()
            async let relationsFetch = client.listRelations()
            var workspaces  = try await wsFetch
            let relations   = try await relationsFetch
            let projects    = try await client.listProjects()

            // Fetch ports concurrently for all active workspaces
            var activeTunnelWorkspaceID = ""
            await withTaskGroup(of: (String, [ForwardedPort], String).self) { group in
                for ws in workspaces where ws.state.isActive {
                    group.addTask { [c = self.client] in
                        // `workspace.ready` triggers daemon-side compose port auto-apply.
                        try? await c.markWorkspaceReady(id: ws.id)
                        let ports = (try? await c.listPorts(workspaceId: ws.id)) ?? []
                        let status = (try? await c.tunnelStatus(workspaceId: ws.id))
                        return (ws.id, ports, status?.activeWorkspaceId ?? "")
                    }
                }
                for await (id, ports, activeID) in group {
                    if let idx = workspaces.firstIndex(where: { $0.id == id }) {
                        workspaces[idx].ports = ports
                    }
                    if !activeID.isEmpty { activeTunnelWorkspaceID = activeID }
                }
            }
            for i in workspaces.indices {
                workspaces[i].hasActiveTunnels = (workspaces[i].id == activeTunnelWorkspaceID)
            }

            applyLoadedWorkspaces(workspaces, relations: relations, projects: projects)
            Self.logger.debug("load() succeeded with \(workspaces.count, privacy: .public) workspaces and \(projects.count, privacy: .public) projects")
        } catch {
            if injectedDaemonURL == nil, isMethodNotFound(error) {
                // Hard cutover: old local daemon APIs are not supported; restart into latest binary.
                Self.logger.error("load() hit method-not-found on local daemon; restarting managed daemon")
                await restartDaemon()
                return
            }
            connectionState = .disconnected
            daemonStatus = .offline
            if injectedDaemonURL != nil {
                setInjectedDaemonUnavailableError(reason: error.localizedDescription)
            } else if self.error == nil {
                self.error = "Cannot reach daemon: \(error.localizedDescription)"
            }
            Self.logger.error("load() failed: \(error.localizedDescription, privacy: .public)")
        }
    }

    // MARK: - Background refresh (4 s polling)

    private func startRefreshLoop() {
        refreshTask?.cancel()
        refreshTask = Task { [weak self] in
            while !Task.isCancelled {
                try? await Task.sleep(for: .seconds(4))
                guard !Task.isCancelled, let self else { break }
                if self.connectionState != .starting,
                   case .outdated = self.daemonStatus {} else {
                    await self.load()
                }
            }
        }
    }

    // MARK: - Daemon auto-start

    /// On first launch: fast-path if daemon is up and auth works; otherwise
    /// restart to a daemon token we control (env/keychain), then reconnect.
    func ensureDaemonAndLoad() async {
        let targetDaemonPort = DaemonLauncher.preferredPort()
        connectionState = .connecting
        Self.logger.info("ensureDaemonAndLoad() started; preferred port \(targetDaemonPort, privacy: .public)")

        // Step 1: Check daemon version compatibility (unauthenticated HTTP).
        if let wsClient = client as? WebSocketDaemonClient {
            if let info = await wsClient.fetchDaemonInfo() {
                if info.isCompatible {
                    daemonStatus = .running(info: info)
                } else {
                    daemonStatus = .outdated(running: info)
                    if injectedDaemonURL != nil {
                        connectionState = .disconnected
                        error = "Daemon protocol v\(info.protocolVersion) is older than required v\(DaemonInfo.requiredProtocol)."
                        return
                    }
                    // Local daemon is outdated; continue and force managed restart to latest.
                }
            } else {
                daemonStatus = .offline
            }
        }

        // Step 2: Fast path — try to connect using current credentials.
        do {
            try await attemptLoad()
            Self.logger.info("Fast-path daemon load succeeded")
            return
        } catch {}

        // Step 3: If a daemon URL was injected (XCUITest or external daemon), never
        // kill or restart — just retry briefly then report failure.
        if injectedDaemonURL != nil {
            var lastError: Error?
            for delay in [0.5, 1.0, 2.0] as [Double] {
                try? await Task.sleep(for: .seconds(delay))
                do {
                    try await attemptLoad()
                    return
                } catch {
                    lastError = error
                }
            }
            connectionState = .disconnected
            daemonStatus = .offline
            setInjectedDaemonUnavailableError(reason: lastError?.localizedDescription)
            Self.logger.error("Injected daemon unreachable: \(lastError?.localizedDescription ?? "unknown", privacy: .public)")
            return
        }

        // Step 4: No injected URL — we own the daemon lifecycle.
        // If fast-path auth fails, force a restart so daemon and keychain token
        // are guaranteed to match.
        connectionState = .starting
        await Task.detached { DaemonLauncher.killRunning(port: targetDaemonPort) }.value
        try? await Task.sleep(for: .seconds(0.4))
        do {
            try await DaemonLauncher.ensureRunning(port: targetDaemonPort)
            Self.logger.info("Managed daemon started on port \(targetDaemonPort, privacy: .public)")
        } catch {
            connectionState = .disconnected
            self.error = error.localizedDescription
            Self.logger.error("Failed to start managed daemon: \(error.localizedDescription, privacy: .public)")
            return
        }
        let newURL = WebSocketDaemonClient.discoverURL()
        client = WebSocketDaemonClient(daemonURL: newURL)
        if let wsClient = client as? WebSocketDaemonClient,
           let info = await wsClient.fetchDaemonInfo() {
            daemonStatus = .running(info: info)
        } else {
            daemonStatus = .unknown
        }
        await load()
    }

    private func setInjectedDaemonUnavailableError(reason: String?) {
        let urlDisplay = injectedDaemonURL ?? "(unknown)"
        let trimmedReason = reason?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
        let reasonText = trimmedReason.isEmpty ? "" : " Reason: \(trimmedReason)."
        error = "Injected daemon URL is unreachable: \(urlDisplay). Check NEXUS_DAEMON_URL, NEXUS_DAEMON_TOKEN, and ensure the daemon is running.\(reasonText)"
    }

    /// Kills the running daemon, starts a fresh one from the resolved binary,
    /// and reconnects. Called by the daemon management UI.
    public func restartDaemon() async {
        guard !isRestarting else { return }
        isRestarting = true
        isBusy = true
        defer { isRestarting = false; isBusy = false }
        Self.logger.info("restartDaemon() requested")
        let targetDaemonPort = DaemonLauncher.preferredPort()
        await Task.detached { DaemonLauncher.killRunning(port: targetDaemonPort) }.value
        try? await Task.sleep(for: .seconds(0.5))
        do {
            try await DaemonLauncher.ensureRunning(port: targetDaemonPort)
            let newURL = WebSocketDaemonClient.discoverURL()
            client = WebSocketDaemonClient(daemonURL: newURL)
            if let wsClient = client as? WebSocketDaemonClient,
               let info = await wsClient.fetchDaemonInfo() {
                daemonStatus = .running(info: info)
            } else {
                daemonStatus = .unknown
            }
            await load()
        } catch {
            connectionState = .disconnected
            daemonStatus = .offline
            self.error = error.localizedDescription
            Self.logger.error("restartDaemon() failed: \(error.localizedDescription, privacy: .public)")
        }
    }

    /// Kills the running daemon without restarting.
    public func stopDaemon() async {
        let targetDaemonPort = DaemonLauncher.preferredPort()
        await Task.detached { DaemonLauncher.killRunning(port: targetDaemonPort) }.value
        connectionState = .disconnected
        repos = []
        daemonStatus = .offline
    }

    /// Throws on failure; used by ensureDaemonAndLoad's fast-path probe.
    private func attemptLoad() async throws {
        async let wsFetch        = client.listWorkspaces()
        async let relationsFetch = client.listRelations()
        var workspaces  = try await wsFetch
        let relations   = try await relationsFetch
        let projects    = try await client.listProjects()

        var activeTunnelWorkspaceID = ""
        await withTaskGroup(of: (String, [ForwardedPort], String).self) { group in
            for ws in workspaces where ws.state.isActive {
                group.addTask { [c = self.client] in
                    try? await c.markWorkspaceReady(id: ws.id)
                    let ports = (try? await c.listPorts(workspaceId: ws.id)) ?? []
                    let status = (try? await c.tunnelStatus(workspaceId: ws.id))
                    return (ws.id, ports, status?.activeWorkspaceId ?? "")
                }
            }
            for await (id, ports, activeID) in group {
                if let idx = workspaces.firstIndex(where: { $0.id == id }) {
                    workspaces[idx].ports = ports
                }
                if !activeID.isEmpty { activeTunnelWorkspaceID = activeID }
            }
        }
        for i in workspaces.indices {
            workspaces[i].hasActiveTunnels = (workspaces[i].id == activeTunnelWorkspaceID)
        }

        applyLoadedWorkspaces(workspaces, relations: relations, projects: projects)
    }

    private func isMethodNotFound(_ error: Error) -> Bool {
        error.localizedDescription.lowercased().contains("method not found")
    }

    // MARK: - Workspace actions

    public func createWorkspace(spec: WorkspaceCreateSpec) async {
        do {
            let ws = try await client.createWorkspace(spec: spec)
            await load()
            selectedWorkspaceID = ws.id
        } catch {
            self.error = error.localizedDescription
        }
    }

    public func createSandbox(request: SandboxCreateRequest) async {
        do {
            let ws = try await client.createSandbox(request: request)
            await load()
            selectedWorkspaceID = ws.id
        } catch {
            self.error = error.localizedDescription
        }
    }

    public func ensureProjectRootSandbox(projectID: String) async -> Workspace? {
        if let existing = projectRootSandbox(projectID: projectID) {
            return existing
        }
        if projects.isEmpty || !projects.contains(where: { $0.id == projectID }) {
            await load()
            if let existing = projectRootSandbox(projectID: projectID) {
                return existing
            }
        }
        guard let project = projects.first(where: { $0.id == projectID }) else {
            self.error = "Project not found: \(projectID)"
            return nil
        }
        do {
            _ = try await client.createSandbox(request: SandboxCreateRequest(
                projectId: projectID,
                targetBranch: "main",
                sourceBranch: nil,
                sourceWorkspaceId: nil,
                fresh: true,
                workspaceName: project.name
            ))
            await load()
            if let root = projectRootSandbox(projectID: projectID) {
                return root
            }
            self.error = "Project root sandbox creation did not appear in list"
            return nil
        } catch {
            await load()
            if let root = projectRootSandbox(projectID: projectID) {
                return root
            }
            self.error = error.localizedDescription
            return nil
        }
    }

    public func createProject(repo: String) async -> Project? {
        do {
            let project = try await client.createProject(repo: repo)
            await load()
            guard let rootSandbox = await ensureProjectRootSandbox(projectID: project.id) else {
                return nil
            }
            selectedWorkspaceID = rootSandbox.id
            return project
        } catch {
            self.error = error.localizedDescription
            return nil
        }
    }

    public func start(_ workspace: Workspace) async {
        await perform { try await self.client.startWorkspace(id: workspace.id) }
    }

    public func stop(_ workspace: Workspace) async {
        await perform { try await self.client.stopWorkspace(id: workspace.id) }
    }

    public func remove(_ workspace: Workspace) async {
        if selectedWorkspaceID == workspace.id { selectedWorkspaceID = nil }
        await perform { try await self.client.removeWorkspace(id: workspace.id) }
    }

    public func addPort(_ port: Int, workspace: Workspace) async {
        await perform {
            try await self.client.addPort(workspaceId: workspace.id, port: port)
        }
    }

    public func removePort(_ port: Int, workspace: Workspace) async {
        await perform {
            try await self.client.removePort(workspaceId: workspace.id, port: port)
        }
    }

    public func activateTunnels(_ workspace: Workspace) async {
        do {
            let status = try await client.activateTunnels(workspaceId: workspace.id)
            if !status.active && status.activeWorkspaceId != workspace.id && !status.activeWorkspaceId.isEmpty {
                self.error = "Tunnels are active in another workspace (\(status.activeWorkspaceId)). Deactivate there first."
            }
            await load()
        } catch {
            self.error = error.localizedDescription
        }
    }

    public func deactivateTunnels(_ workspace: Workspace) async {
        await perform {
            _ = try await self.client.deactivateTunnels(workspaceId: workspace.id)
        }
    }

    private func perform(_ op: @escaping () async throws -> Void) async {
        do {
            try await op()
            await load()
        } catch {
            self.error = error.localizedDescription
        }
    }

    // MARK: - Computed helpers

    public var selectedWorkspace: Workspace? {
        repos.flatMap(\.workspaces).first { $0.id == selectedWorkspaceID }
    }

    public var allWorkspaces: [Workspace] {
        repos.flatMap(\.workspaces)
    }

    private func projectRootSandbox(projectID: String) -> Workspace? {
        guard let repo = repos.first(where: { $0.id == projectID }) else { return nil }
        return repo.workspaces.first(where: { ($0.parentWorkspaceId ?? "").isEmpty })
    }
}

public enum ConnectionState: Equatable {
    case starting, disconnected, connecting, connected
}

/// The compatibility status of the running daemon.
public enum DaemonStatus: Equatable {
    case unknown
    case running(info: DaemonInfo)
    case outdated(running: DaemonInfo)  // protocolVersion < requiredProtocol
    case offline
}
