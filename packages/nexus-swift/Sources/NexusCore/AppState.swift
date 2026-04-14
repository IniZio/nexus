import Foundation
import Combine

/// Root app state — owns the daemon client and drives all views.
/// Always connects to the real daemon. If the daemon isn't running,
/// connectionState reflects .disconnected and an error message is set.
@MainActor
public final class AppState: ObservableObject {

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
    @Published public var selectedWorkspaceID: String?
    @Published public var connectionState: ConnectionState = .disconnected
    @Published public var daemonStatus: DaemonStatus = .unknown
    @Published public var showNewWorkspace = false
    @Published public var sidebarVisible = true
    @Published public var showInspector = true
    @Published public var error: String?

    // MARK: - Client
    public private(set) var client: any DaemonClient

    private var refreshTask: Task<Void, Never>?
    private var isRestarting = false
    @Published public var isBusy = false

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

    private func applyLoadedWorkspaces(_ workspaces: [Workspace], relations: [RelationsGroup]) {
        repos = Repo.fromRelations(relations, workspaces: workspaces)
        connectionState = .connected
        error = nil

        if selectedWorkspaceID == nil {
            selectedWorkspaceID = repos.first?.workspaces.first?.id
        }
    }

    // MARK: - Load

    public func load() async {
        connectionState = .connecting
        do {
            async let wsFetch        = client.listWorkspaces()
            async let relationsFetch = client.listRelations()
            var workspaces  = try await wsFetch
            let relations   = try await relationsFetch

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

            applyLoadedWorkspaces(workspaces, relations: relations)
        } catch {
            connectionState = .disconnected
            if self.error == nil {
                self.error = "Cannot reach daemon: \(error.localizedDescription)"
            }
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
        connectionState = .connecting

        // Step 1: Check daemon version compatibility (unauthenticated HTTP).
        if let wsClient = client as? WebSocketDaemonClient {
            if let info = await wsClient.fetchDaemonInfo() {
                if info.isCompatible {
                    daemonStatus = .running(info: info)
                } else {
                    daemonStatus = .outdated(running: info)
                    connectionState = .disconnected
                    error = "Daemon protocol v\(info.protocolVersion) is older than required v\(DaemonInfo.requiredProtocol). Use the daemon panel to update."
                    return
                }
            } else {
                daemonStatus = .offline
            }
        }

        // Step 2: Fast path — try to connect using current credentials.
        do {
            try await attemptLoad()
            return
        } catch {}

        // Step 3: If a daemon URL was injected (XCUITest or external daemon), never
        // kill or restart — just retry briefly then report failure.
        if ProcessInfo.processInfo.environment["NEXUS_DAEMON_URL"] != nil {
            for delay in [0.5, 1.0, 2.0] as [Double] {
                try? await Task.sleep(for: .seconds(delay))
                do { try await attemptLoad(); return } catch {}
            }
            connectionState = .disconnected
            error = "Cannot reach daemon at injected URL"
            return
        }

        // Step 4: No injected URL — we own the daemon lifecycle.
        // If fast-path auth fails, force a restart so daemon and keychain token
        // are guaranteed to match.
        connectionState = .starting
        await Task.detached { DaemonLauncher.killRunning() }.value
        try? await Task.sleep(for: .seconds(0.4))
        do {
            try await DaemonLauncher.ensureRunning()
        } catch {
            connectionState = .disconnected
            self.error = error.localizedDescription
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

    /// Kills the running daemon, starts a fresh one from the resolved binary,
    /// and reconnects. Called by the daemon management UI.
    public func restartDaemon() async {
        guard !isRestarting else { return }
        isRestarting = true
        isBusy = true
        defer { isRestarting = false; isBusy = false }
        await Task.detached { DaemonLauncher.killRunning() }.value
        try? await Task.sleep(for: .seconds(0.5))
        do {
            try await DaemonLauncher.ensureRunning()
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
        }
    }

    /// Kills the running daemon without restarting.
    public func stopDaemon() async {
        await Task.detached { DaemonLauncher.killRunning() }.value
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

        applyLoadedWorkspaces(workspaces, relations: relations)
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
