import Foundation

// Debug extension for WebSocketDaemonClient to track PTY operations
extension WebSocketDaemonClient {
    func openPTYWithDebug(workspaceId: String, name: String, cols: Int, rows: Int) async throws -> String {
        print("[DEBUG] openPTY called with workspaceId=\(workspaceId), name=\(name)")
        let result = try await openPTY(workspaceId: workspaceId, name: name, cols: cols, rows: rows)
        print("[DEBUG] openPTY returned sessionId=\(result)")
        return result
    }
}
