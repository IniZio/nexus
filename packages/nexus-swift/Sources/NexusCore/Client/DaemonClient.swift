import Foundation

// MARK: - Protocol

/// Abstraction over the Nexus daemon. Set NEXUS_DAEMON_URL to point at the daemon;
/// defaults to ws://localhost:63987.
public protocol DaemonClient: Sendable {

    // ── Projects ─────────────────────────────────────────────────────
    func listProjects() async throws -> [Project]
    func createProject(repo: String) async throws -> Project

    // ── Discovery ────────────────────────────────────────────────────
    func listWorkspaces() async throws -> [Workspace]
    func listRelations() async throws -> [RelationsGroup]

    // ── Lifecycle ────────────────────────────────────────────────────
    func createWorkspace(spec: WorkspaceCreateSpec) async throws -> Workspace
    func createSandbox(request: SandboxCreateRequest) async throws -> Workspace
    func startWorkspace(id: String) async throws
    func stopWorkspace(id: String) async throws
    func removeWorkspace(id: String) async throws

    // ── Ports ────────────────────────────────────────────────────────
    func markWorkspaceReady(id: String) async throws
    func listPorts(workspaceId: String) async throws -> [ForwardedPort]
    func addPort(workspaceId: String, port: Int) async throws
    func removePort(workspaceId: String, port: Int) async throws
    func startTunnels(workspaceId: String) async throws -> TunnelStatus
    func stopTunnels(workspaceId: String) async throws -> TunnelStatus
    func tunnelStatus(workspaceId: String) async throws -> TunnelStatus

    // ── Exec ─────────────────────────────────────────────────────────
    /// Runs a command in the workspace's root directory and returns buffered output.
    func exec(workspaceId: String, command: String, args: [String]) async throws -> ExecOutput

    // ── Workspace info ────────────────────────────────────────────────
    /// Returns rich workspace metadata including spotlight ports.
    func workspaceInfo(id: String) async throws -> WorkspaceInfo

    // ── Daemon settings ────────────────────────────────────────────────
    func getDaemonSandboxResourceSettings() async throws -> SandboxResourceSettings
    func updateDaemonSandboxResourceSettings(_ settings: SandboxResourceSettings) async throws -> SandboxResourceSettings
}

public struct SandboxCreateRequest: Sendable {
    public let projectId: String
    public let targetBranch: String
    public let sourceBranch: String?
    public let sourceWorkspaceId: String?
    public let fresh: Bool
    public let workspaceName: String
    public let agentProfile: String
    public let backend: String
    public let configBundle: String?

    public init(
        projectId: String,
        targetBranch: String = "main",
        sourceBranch: String? = nil,
        sourceWorkspaceId: String? = nil,
        fresh: Bool = false,
        workspaceName: String,
        agentProfile: String = "default",
        backend: String = "",
        configBundle: String? = nil
    ) {
        self.projectId = projectId
        self.targetBranch = targetBranch
        self.sourceBranch = sourceBranch
        self.sourceWorkspaceId = sourceWorkspaceId
        self.fresh = fresh
        self.workspaceName = workspaceName
        self.agentProfile = agentProfile
        self.backend = backend
        self.configBundle = configBundle
    }
}

public struct TunnelStatus: Sendable {
    public let active: Bool
    public let activeWorkspaceId: String
}

public struct SandboxResourceSettings: Sendable, Equatable {
    public let defaultMemoryMiB: Int
    public let defaultVCPUs: Int
    public let maxMemoryMiB: Int
    public let maxVCPUs: Int

    public init(defaultMemoryMiB: Int, defaultVCPUs: Int, maxMemoryMiB: Int, maxVCPUs: Int) {
        self.defaultMemoryMiB = defaultMemoryMiB
        self.defaultVCPUs = defaultVCPUs
        self.maxMemoryMiB = maxMemoryMiB
        self.maxVCPUs = maxVCPUs
    }
}
