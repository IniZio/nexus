import Foundation

/// Real client: JSON-RPC 2.0 over WebSocket, targeting the Nexus daemon.
///
/// Token resolution order:
///   1. NEXUS_DAEMON_TOKEN env var (for dev / CI)
///   2. $XDG_DATA_HOME/nexus/token  (daemon writes this on first start)
///   3. ~/.config/nexus/run/token   (legacy path)
///
/// Port discovery: reads `daemon-PORT.pid` from the Nexus run dir to find the
/// actual port the daemon is listening on.  Falls back to 63987 (n-e-x-u-s on a telephone keypad).
///
/// Auth: sends the token in the `Authorization: Bearer TOKEN` HTTP header on
/// the WebSocket upgrade request.
public final class WebSocketDaemonClient: DaemonClient, @unchecked Sendable {

    private let daemonURL: URL
    private var task: URLSessionWebSocketTask?
    private var pending: [String: CheckedContinuation<Any, Error>] = [:]
    private var requestCounter = 0
    private let lock = NSLock()

    public init(daemonURL: URL) {
        self.daemonURL = daemonURL
    }

    // MARK: - Daemon URL discovery

    /// Discovers the daemon's WebSocket URL by scanning the run directory for
    /// `daemon-PORT.pid` files.  Falls back to `ws://localhost:63987`.
    public static func discoverURL() -> URL {
        if let env = ProcessInfo.processInfo.environment["NEXUS_DAEMON_URL"], !env.isEmpty,
           let url = URL(string: env) { return url }

        let home = FileManager.default.homeDirectoryForCurrentUser.path
        let configHome = ProcessInfo.processInfo.environment["XDG_CONFIG_HOME"]
                      ?? "\(home)/.config"
        let runDir = "\(configHome)/nexus/run"

        if let entries = try? FileManager.default.contentsOfDirectory(atPath: runDir) {
            for entry in entries where entry.hasPrefix("daemon-") && entry.hasSuffix(".pid") {
                // entry = "daemon-63987.pid" → port = 63987
                let inner = String(entry.dropFirst("daemon-".count).dropLast(".pid".count))
                if let port = Int(inner) {
                    return URL(string: "ws://localhost:\(port)")!
                }
            }
        }
        return URL(string: "ws://localhost:63987")!
    }

    // MARK: - Auth token

    public static func readToken() -> String {
        if let env = ProcessInfo.processInfo.environment["NEXUS_DAEMON_TOKEN"], !env.isEmpty {
            return env
        }
        let home = FileManager.default.homeDirectoryForCurrentUser.path
        let xdgData = ProcessInfo.processInfo.environment["XDG_DATA_HOME"] ?? "\(home)/.local/share"
        for path in ["\(xdgData)/nexus/token", "\(home)/.config/nexus/run/token"] {
            if let raw = try? String(contentsOfFile: path, encoding: .utf8) {
                let tok = raw.trimmingCharacters(in: .whitespacesAndNewlines)
                if !tok.isEmpty { return tok }
            }
        }
        return ""
    }

    // MARK: - Connect / disconnect

    private func connect() async throws {
        guard task?.state != .running else { return }
        let token = Self.readToken()
        var request = URLRequest(url: daemonURL)
        if !token.isEmpty {
            request.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")
        }
        let session = URLSession(configuration: .default)
        task = session.webSocketTask(with: request)
        task?.resume()
        receiveLoop()
    }

    public func disconnect() {
        task?.cancel(with: .goingAway, reason: nil)
        task = nil
        lock.withLock {
            let all = pending
            pending.removeAll()
            for cont in all.values {
                cont.resume(throwing: CancellationError())
            }
        }
    }

    // MARK: - Receive loop

    private func receiveLoop() {
        task?.receive { [weak self] result in
            guard let self else { return }
            switch result {
            case .success(let msg):
                if case .string(let text) = msg { self.handle(text) }
                self.receiveLoop()
            case .failure(let err):
                self.failAll(err)
            }
        }
    }

    private func handle(_ text: String) {
        guard let data = text.data(using: .utf8),
              let json = try? JSONSerialization.jsonObject(with: data) as? [String: Any] else { return }

        // Server push notification (no "id" field, has "method")
        if json["id"] == nil, let method = json["method"] as? String {
            let params = json["params"] as? [String: Any] ?? [:]
            handleNotification(method: method, params: params)
            return
        }

        // JSON-RPC response (has "id")
        guard let id = json["id"] as? String else { return }
        lock.withLock {
            guard let cont = pending.removeValue(forKey: id) else { return }
            if let errObj = json["error"] as? [String: Any],
               let msg    = errObj["message"] as? String {
                cont.resume(throwing: RPCError(message: msg))
            } else {
                cont.resume(returning: json["result"] as Any? ?? NSNull())
            }
        }
    }

    // MARK: - Notification dispatch (pty.data / pty.exit)

    private let ptyLock = NSLock()
    private var ptyDataHandlers: [String: @Sendable (String) -> Void] = [:]
    private var ptyExitHandlers: [String: @Sendable (Int) -> Void] = [:]
    // Buffers early pty.data that arrives before subscribePTY() is called (race window after pty.open)
    private var ptyDataBuffer: [String: [String]] = [:]

    private func handleNotification(method: String, params: [String: Any]) {
        switch method {
        case "pty.data":
            guard let sid  = params["sessionId"] as? String,
                  let data = params["data"]      as? String else { return }
            let h: (@Sendable (String) -> Void)? = ptyLock.withLock {
                if let handler = ptyDataHandlers[sid] { return handler }
                ptyDataBuffer[sid, default: []].append(data)
                return nil
            }
            h?(data)
        case "pty.exit":
            guard let sid = params["sessionId"] as? String else { return }
            let code = params["exitCode"] as? Int ?? -1
            let h = ptyLock.withLock { ptyExitHandlers[sid] }
            h?(code)
        default:
            break
        }
    }

    private func failAll(_ error: Error) {
        let all = lock.withLock { () -> [String: CheckedContinuation<Any, Error>] in
            let a = pending; pending.removeAll(); return a
        }
        task = nil
        all.values.forEach { $0.resume(throwing: error) }
    }

    // MARK: - Low-level RPC

    func call(_ method: String, params: [String: Any] = [:]) async throws -> Any {
        try await connect()
        var id = ""
        lock.withLock {
            requestCounter += 1
            id = "req-\(requestCounter)"
        }
        let payload: [String: Any] = ["jsonrpc": "2.0", "id": id, "method": method, "params": params]
        let text = String(data: try JSONSerialization.data(withJSONObject: payload), encoding: .utf8)!
        return try await withCheckedThrowingContinuation { cont in
            lock.withLock { pending[id] = cont }
            task?.send(.string(text)) { _ in }
        }
    }

    // MARK: - DaemonClient

    public func listWorkspaces() async throws -> [Workspace] {
        let result = try await call("workspace.list")
        guard let dict = result as? [String: Any], let arr = dict["workspaces"] else { return [] }
        return try JSONDecoder().decode([Workspace].self,
                                       from: JSONSerialization.data(withJSONObject: arr))
    }

    public func listRelations() async throws -> [RelationsGroup] {
        let result = try await call("workspace.relations.list")
        guard let dict = result as? [String: Any], let arr = dict["relations"] else { return [] }
        return try JSONDecoder().decode([RelationsGroup].self,
                                       from: JSONSerialization.data(withJSONObject: arr))
    }

    public func createWorkspace(spec: WorkspaceCreateSpec) async throws -> Workspace {
        let specData = try JSONEncoder().encode(spec)
        guard let specObj = try JSONSerialization.jsonObject(with: specData) as? [String: Any] else {
            throw RPCError(message: "failed to encode spec")
        }
        let result = try await call("workspace.create", params: ["spec": specObj])
        guard let dict = result as? [String: Any], let ws = dict["workspace"] else {
            throw RPCError(message: "unexpected response from workspace.create")
        }
        return try JSONDecoder().decode(Workspace.self,
                                       from: JSONSerialization.data(withJSONObject: ws))
    }

    public func startWorkspace(id: String) async throws {
        _ = try await call("workspace.start", params: ["id": id])
    }

    public func stopWorkspace(id: String) async throws {
        _ = try await call("workspace.stop", params: ["id": id])
    }

    public func pauseWorkspace(id: String) async throws {
        _ = try await call("workspace.pause", params: ["id": id])
    }

    public func resumeWorkspace(id: String) async throws {
        _ = try await call("workspace.resume", params: ["id": id])
    }

    public func removeWorkspace(id: String) async throws {
        _ = try await call("workspace.remove", params: ["id": id])
    }

    public func listPorts(workspaceId: String) async throws -> [ForwardedPort] {
        let result = try await call("spotlight.list", params: ["workspaceId": workspaceId])
        guard let dict  = result as? [String: Any],
              let items = dict["items"] as? [[String: Any]] else { return [] }
        return items.compactMap { item -> ForwardedPort? in
            guard let port = item["guestPort"] as? Int ?? item["port"] as? Int else { return nil }
            return ForwardedPort(id: port)
        }
    }

    public func exec(workspaceId: String, command: String, args: [String]) async throws -> ExecOutput {
        try await exec(workspaceId: workspaceId, command: command, args: args, workDir: nil)
    }

    public func exec(workspaceId: String, command: String, args: [String], workDir: String?) async throws -> ExecOutput {
        var options: [String: Any] = [:]
        if let wd = workDir, !wd.isEmpty { options["work_dir"] = wd }
        let result = try await call("exec", params: [
            "workspaceId": workspaceId,
            "command": command,
            "args": args,
            "options": options,
        ])
        guard let dict = result as? [String: Any] else {
            throw RPCError(message: "unexpected exec response")
        }
        return ExecOutput(
            stdout: dict["stdout"] as? String ?? "",
            stderr: dict["stderr"] as? String ?? "",
            exitCode: dict["exit_code"] as? Int ?? 0
        )
    }

    public func workspaceInfo(id: String) async throws -> WorkspaceInfo {
        let result = try await call("workspace.info", params: ["workspaceId": id])
        guard let dict = result as? [String: Any] else {
            throw RPCError(message: "unexpected workspace.info response")
        }
        let wsPath = dict["workspace_path"] as? String ?? ""
        let wsId   = dict["workspace_id"]   as? String ?? id
        var ports: [ForwardedPort] = []
        if let items = dict["spotlight"] as? [[String: Any]] {
            ports = items.compactMap { item in
                guard let p = item["guestPort"] as? Int ?? item["port"] as? Int else { return nil }
                return ForwardedPort(id: p)
            }
        }
        return WorkspaceInfo(workspaceId: wsId, workspacePath: wsPath, ports: ports)
    }

    // MARK: - PTY

    /// Opens a PTY session in the workspace.  Returns the session ID.
    public func openPTY(workspaceId: String, cols: Int, rows: Int) async throws -> String {
        let result = try await call("pty.open", params: [
            "workspaceId": workspaceId,
            "cols": cols,
            "rows": rows,
        ])
        guard let dict = result as? [String: Any],
              let sid  = dict["sessionId"] as? String else {
            throw RPCError(message: "unexpected pty.open response")
        }
        return sid
    }

    public func writePTY(sessionId: String, data: String) async throws {
        _ = try await call("pty.write", params: ["sessionId": sessionId, "data": data])
    }

    public func resizePTY(sessionId: String, cols: Int, rows: Int) async throws {
        _ = try await call("pty.resize", params: ["sessionId": sessionId, "cols": cols, "rows": rows])
    }

    public func closePTY(sessionId: String) async throws {
        _ = try await call("pty.close", params: ["sessionId": sessionId])
    }

    /// Register callbacks for output and exit events on a PTY session.
    /// Drains any early-buffered pty.data that arrived before this call.
    public func subscribePTY(
        sessionId: String,
        onData: @escaping @Sendable (String) -> Void,
        onExit: @escaping @Sendable (Int) -> Void
    ) {
        let buffered: [String] = ptyLock.withLock {
            ptyDataHandlers[sessionId] = onData
            ptyExitHandlers[sessionId] = onExit
            return ptyDataBuffer.removeValue(forKey: sessionId) ?? []
        }
        for chunk in buffered { onData(chunk) }
    }

    public func unsubscribePTY(sessionId: String) {
        ptyLock.withLock {
            ptyDataHandlers.removeValue(forKey: sessionId)
            ptyExitHandlers.removeValue(forKey: sessionId)
            ptyDataBuffer.removeValue(forKey: sessionId)
        }
    }
}

// MARK: - Error type

public struct RPCError: Error, LocalizedError {
    public let message: String
    public var errorDescription: String? { message }
    public init(message: String) { self.message = message }
}

// MARK: - Exec output

public struct ExecOutput: Sendable {
    public let stdout: String
    public let stderr: String
    public let exitCode: Int
    public var output: String { stdout.isEmpty ? stderr : stdout }
    public var failed: Bool   { exitCode != 0 }
}

// MARK: - Workspace info (from workspace.info RPC)

public struct WorkspaceInfo: Sendable {
    public let workspaceId: String
    public let workspacePath: String
    public let ports: [ForwardedPort]

    public init(workspaceId: String, workspacePath: String, ports: [ForwardedPort]) {
        self.workspaceId   = workspaceId
        self.workspacePath = workspacePath
        self.ports         = ports
    }
}

// MARK: - Version info (unauthenticated HTTP endpoint)

/// Version and protocol information returned by the daemon's `/version` endpoint.
public struct DaemonInfo: Decodable, Equatable {
    public let name: String
    public let version: String
    public let commit: String
    public let builtAt: String
    public let protocolVersion: Int

    /// Protocol version this build of the Swift app requires.
    /// Must match `ProtocolVersion` in `packages/nexus/pkg/buildinfo/buildinfo.go`.
    public static let requiredProtocol = 2

    /// Dev builds (go run / go build without ldflags) always report "0.0.0-dev".
    /// Treat them as always compatible so local development is never blocked.
    public var isCompatible: Bool {
        version == "0.0.0-dev" || protocolVersion >= Self.requiredProtocol
    }

    enum CodingKeys: String, CodingKey {
        case name, version, commit, builtAt
        case protocolVersion = "protocol"
    }
}

extension WebSocketDaemonClient {
    /// Fetches `/version` over plain HTTP (no auth).
    /// Returns `nil` if the daemon is unreachable or the response can't be decoded.
    public func fetchDaemonInfo() async -> DaemonInfo? {
        guard let host = daemonURL.host,
              let port = daemonURL.port else { return nil }
        let scheme = daemonURL.scheme == "wss" ? "https" : "http"
        guard let url = URL(string: "\(scheme)://\(host):\(port)/version") else { return nil }
        var req = URLRequest(url: url)
        req.timeoutInterval = 2
        guard let (data, _) = try? await URLSession.shared.data(for: req) else { return nil }
        return try? JSONDecoder().decode(DaemonInfo.self, from: data)
    }
}
