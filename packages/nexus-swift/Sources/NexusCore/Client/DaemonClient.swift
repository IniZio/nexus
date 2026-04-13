import Foundation

// MARK: - Protocol

/// Abstraction over the Nexus daemon. Set NEXUS_DAEMON_URL to point at the daemon;
/// defaults to ws://localhost:63987.
public protocol DaemonClient: Sendable {

    // ── Discovery ────────────────────────────────────────────────────
    func listWorkspaces() async throws -> [Workspace]
    func listRelations() async throws -> [RelationsGroup]

    // ── Lifecycle ────────────────────────────────────────────────────
    func createWorkspace(spec: WorkspaceCreateSpec) async throws -> Workspace
    func startWorkspace(id: String) async throws
    func stopWorkspace(id: String) async throws
    func pauseWorkspace(id: String) async throws
    func resumeWorkspace(id: String) async throws
    func removeWorkspace(id: String) async throws

    // ── Ports ────────────────────────────────────────────────────────
    func listPorts(workspaceId: String) async throws -> [ForwardedPort]

    // ── Exec ─────────────────────────────────────────────────────────
    /// Runs a command in the workspace's root directory and returns buffered output.
    func exec(workspaceId: String, command: String, args: [String]) async throws -> ExecOutput

    // ── Workspace info ────────────────────────────────────────────────
    /// Returns rich workspace metadata including spotlight ports.
    func workspaceInfo(id: String) async throws -> WorkspaceInfo
}
