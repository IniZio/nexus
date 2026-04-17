import Foundation

/// Real client: JSON-RPC 2.0 over WebSocket, targeting the Nexus daemon.
///
/// Token resolution order:
///   1. NEXUS_DAEMON_TOKEN env var (for dev / CI)
///   2. macOS Keychain generic password (service configurable via
///      NEXUS_DAEMON_TOKEN_KEYCHAIN_SERVICE)
///
/// Port discovery: reads `daemon-PORT.pid` from the Nexus run dir to find the
/// actual port the daemon is listening on.  Falls back to 63987 (n-e-x-u-s on a telephone keypad).
///
/// Auth: sends the token in the `Authorization: Bearer TOKEN` HTTP header on
/// the WebSocket upgrade request.
public final class WebSocketDaemonClient: DaemonClient, @unchecked Sendable {

    private let daemonURL: URL
    /// Single session for all WebSocket connections — `URLSession()` per `connect()` leaked memory when
    /// refresh/load stacked during handshake (see AppState refresh loop).
    private let webSocketSession: URLSession = {
        let cfg = URLSessionConfiguration.default
        cfg.timeoutIntervalForRequest = 90
        return URLSession(configuration: cfg)
    }()
    private var task: URLSessionWebSocketTask?
    private var pending: [String: CheckedContinuation<Any, Error>] = [:]
    private var requestCounter = 0
    private let lock = NSLock()
    /// Serializes `connect()` / `task` mutation. Parallel `call()` (e.g. `async let` list RPCs) must not
    /// create overlapping WebSocket handshakes — that leaked tasks, stacked `receiveLoop`, and grew RAM.
    private let connectionLock = NSLock()

    public init(daemonURL: URL) {
        self.daemonURL = daemonURL
    }

    deinit {
        disconnect()
        // XCTest and rapid client churn otherwise retain URLSession delegate queues and buffers.
        webSocketSession.invalidateAndCancel()
    }

    // MARK: - Daemon URL discovery

    /// Resolves the WebSocket URL for the daemon.
    ///
    /// Uses `NEXUS_DAEMON_URL` when set. Otherwise picks a port **without synchronous HTTP**:
    /// 1) Port already owned by the current process-isolation worktree (`daemon-*.owner`), if any.
    /// 2) Else the **newest** `daemon-*.pid` by mtime (typical “last started daemon”).
    /// 3) Else `DaemonLauncher.preferredPort()` (launcher will bind next).
    public static func discoverURL() -> URL {
        if let env = ProcessInfo.processInfo.environment["NEXUS_DAEMON_URL"], !env.isEmpty,
           let url = URL(string: env) {
            return url
        }
        let port = resolveConnectionPortDiskOnly()
        return loopbackWebSocketURL(port: port)
    }

    /// Picks localhost port using run-dir metadata only (no `/healthz` on the main thread).
    private static func resolveConnectionPortDiskOnly() -> Int {
        let env = ProcessInfo.processInfo.environment
        // Explicit port must win before owner/mtime heuristics; otherwise the newest `daemon-*.pid`
        // on disk overrides scheme/test env (e.g. NEXUS_DAEMON_PORT=19998).
        if let raw = env["NEXUS_DAEMON_PORT"]?.trimmingCharacters(in: .whitespacesAndNewlines), !raw.isEmpty,
           let p = Int(raw), (1..<65536).contains(p) {
            return p
        }
        let preferred = DaemonLauncher.preferredPort()
        let runDir = DaemonLauncher.resolveRunDir()

        if let owned = DaemonLauncher.existingPortForCurrentProcessWorktree(),
           FileManager.default.fileExists(atPath: "\(runDir)/daemon-\(owned).pid") {
            return owned
        }
        guard let entries = try? FileManager.default.contentsOfDirectory(atPath: runDir) else {
            return preferred
        }
        let fm = FileManager.default
        var bestPort: Int?
        var bestMtime = Date.distantPast
        for entry in entries where entry.hasPrefix("daemon-") && entry.hasSuffix(".pid") {
            let inner = String(entry.dropFirst("daemon-".count).dropLast(".pid".count))
            guard let port = Int(inner) else { continue }
            let path = "\(runDir)/\(entry)"
            let mtime = (try? fm.attributesOfItem(atPath: path)[.modificationDate] as? Date) ?? .distantPast
            if mtime >= bestMtime {
                bestMtime = mtime
                bestPort = port
            }
        }
        if let bestPort { return bestPort }
        return preferred
    }

    private static func loopbackWebSocketURL(port: Int) -> URL {
        var components = URLComponents()
        components.scheme = "ws"
        components.host = "localhost"
        components.port = max(1, min(port, 65535))
        return components.url ?? URL(fileURLWithPath: "/")
    }

    // MARK: - Auth token

    public static func readToken() -> String {
        if let env = ProcessInfo.processInfo.environment["NEXUS_DAEMON_TOKEN"], !env.isEmpty {
            return env
        }
        let configuredService = ProcessInfo.processInfo.environment["NEXUS_DAEMON_TOKEN_KEYCHAIN_SERVICE"]?
            .trimmingCharacters(in: .whitespacesAndNewlines)
        let services = [
            configuredService,
            "nexus-daemon-token",
            "nexus/token",
            "nexus-daemon",
            "nexus",
        ]
        for maybeService in services {
            guard let service = maybeService, !service.isEmpty else { continue }
            if let token = readMacKeychainPassword(service: service), !token.isEmpty {
                return token
            }
        }
        return ""
    }

    private static func readMacKeychainPassword(service: String) -> String? {
        let task = Process()
        task.executableURL = URL(fileURLWithPath: "/usr/bin/security")
        task.arguments = ["find-generic-password", "-s", service, "-w"]

        let out = Pipe()
        task.standardOutput = out
        task.standardError = Pipe()

        do {
            try task.run()
        } catch {
            return nil
        }
        task.waitUntilExit()
        guard task.terminationStatus == 0 else { return nil }

        let data = out.fileHandleForReading.readDataToEndOfFile()
        guard let raw = String(data: data, encoding: .utf8) else { return nil }
        let token = raw.trimmingCharacters(in: .whitespacesAndNewlines)
        return token.isEmpty ? nil : token
    }

    // MARK: - Connect / disconnect

    private func connect() async throws {
        connectionLock.lock()
        StartupTrace.checkpoint("ws.connect.enter", daemonURL.absoluteString)
        // Guard on task != nil, NOT task?.state == .running.
        // A freshly-resumed task is in state .suspended (connecting) — checking only .running
        // caused every concurrent call to create a new URLSessionWebSocketTask before the first
        // one finished its TCP handshake, leaking tasks+buffers and growing RAM unboundedly.
        if task != nil {
            connectionLock.unlock()
            StartupTrace.checkpoint("ws.connect.noop", "task already exists (state=\(task?.state.rawValue ?? -1))")
            return
        }
        let token = Self.readToken()
        var request = URLRequest(url: daemonURL)
        if !token.isEmpty {
            request.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")
        }
        task = webSocketSession.webSocketTask(with: request)
        task?.resume()
        StartupTrace.checkpoint("ws.connect.resumed")
        connectionLock.unlock()
        receiveLoop()
    }

    public func disconnect() {
        // Same lock order as `failAll` (`lock` then `connectionLock`) to avoid deadlocks.
        lock.withLock {
            let all = pending
            pending.removeAll()
            for cont in all.values {
                cont.resume(throwing: CancellationError())
            }
        }
        connectionLock.lock()
        task?.cancel(with: .goingAway, reason: nil)
        task = nil
        connectionLock.unlock()
    }

    // MARK: - Receive loop

    private func receiveLoop() {
        connectionLock.lock()
        let ws = task
        connectionLock.unlock()
        guard let ws else { return }
        ws.receive { [weak self] result in
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
        connectionLock.lock()
        task?.cancel(with: .goingAway, reason: nil)
        task = nil
        connectionLock.unlock()
        all.values.forEach { $0.resume(throwing: error) }
    }

    // MARK: - Low-level RPC

    /// Default ceiling for a single JSON-RPC round trip (connect + request + response).
    private static let defaultRPCSeconds: UInt64 = 45

    func call(_ method: String, params: [String: Any] = [:]) async throws -> Any {
        // Do NOT call disconnect() here on RPC errors — timeouts and transient failures must not tear
        // down the shared WebSocket for all concurrent calls. Socket-level teardown is handled by
        // failAll() in receiveLoop's .failure branch, which is the only place it is appropriate.
        return try await withTimeoutRPC(seconds: Self.defaultRPCSeconds) {
            try await self.performRPC(method: method, params: params)
        }
    }

    private func performRPC(method: String, params: [String: Any]) async throws -> Any {
        StartupTrace.rpc(method: method)
        try await connect()
        var id = ""
        lock.withLock {
            requestCounter += 1
            id = "req-\(requestCounter)"
        }
        let payload: [String: Any] = ["jsonrpc": "2.0", "id": id, "method": method, "params": params]
        let text = String(data: try JSONSerialization.data(withJSONObject: payload), encoding: .utf8)!
        // withTaskCancellationHandler ensures that if the enclosing task is cancelled (e.g. by
        // withTimeoutRPC's group.cancelAll()), the pending continuation is removed from the dict
        // immediately. Without this, the cancelled task's continuation stays in pending[] and is
        // double-resumed when disconnect()/failAll() later iterates the dict — undefined behaviour.
        return try await withTaskCancellationHandler {
            try await withCheckedThrowingContinuation { cont in
                lock.withLock { pending[id] = cont }
                connectionLock.lock()
                let sock = task
                connectionLock.unlock()
                guard let sock else {
                    _ = lock.withLock { pending.removeValue(forKey: id) }
                    cont.resume(throwing: RPCError(message: "WebSocket task is nil"))
                    return
                }
                sock.send(.string(text)) { [weak self] err in
                    guard let self else { return }
                    if let err {
                        let cont2 = self.lock.withLock { self.pending.removeValue(forKey: id) }
                        cont2?.resume(throwing: err)
                    }
                }
            }
        } onCancel: { [weak self] in
            guard let self else { return }
            let cont = self.lock.withLock { self.pending.removeValue(forKey: id) }
            cont?.resume(throwing: CancellationError())
        }
    }

    private func withTimeoutRPC(seconds: UInt64, _ work: @escaping () async throws -> Any) async throws -> Any {
        try await withThrowingTaskGroup(of: Any.self) { group in
            group.addTask { try await work() }
            group.addTask {
                try await Task.sleep(nanoseconds: seconds * 1_000_000_000)
                throw RPCError(message: "timed out after \(seconds)s (no daemon response)")
            }
            defer { group.cancelAll() }
            return try await group.next()!
        }
    }

    // MARK: - DaemonClient

    public func listWorkspaces() async throws -> [Workspace] {
        let result = try await call("workspace.list")
        guard let dict = result as? [String: Any], let arr = dict["workspaces"] else { return [] }
        return try JSONDecoder().decode([Workspace].self,
                                       from: JSONSerialization.data(withJSONObject: arr))
    }

    public func listProjects() async throws -> [Project] {
        let result = try await call("project.list")
        guard let dict = result as? [String: Any], let arr = dict["projects"] else { return [] }
        return try JSONDecoder().decode([Project].self,
                                        from: JSONSerialization.data(withJSONObject: arr))
    }

    public func createProject(repo: String) async throws -> Project {
        let result = try await call("project.create", params: ["repo": repo])
        guard let dict = result as? [String: Any], let raw = dict["project"] else {
            throw RPCError(message: "unexpected response from project.create")
        }
        return try JSONDecoder().decode(Project.self,
                                        from: JSONSerialization.data(withJSONObject: raw))
    }

    public func listRelations() async throws -> [RelationsGroup] {
        let result = try await call("workspace.relations.list")
        guard let dict = result as? [String: Any], let arr = dict["relations"] else { return [] }
        return try JSONDecoder().decode([RelationsGroup].self,
                                       from: JSONSerialization.data(withJSONObject: arr))
    }

    public func createWorkspace(spec: WorkspaceCreateSpec) async throws -> Workspace {
        let project = try await createProject(repo: spec.repo)
        let request = SandboxCreateRequest(
            projectId: project.id,
            targetBranch: spec.ref,
            fresh: false,
            workspaceName: spec.workspaceName,
            agentProfile: spec.agentProfile,
            backend: spec.backend,
            configBundle: ConfigBundle.build()
        )
        return try await createSandbox(request: request)
    }

    public func createSandbox(request: SandboxCreateRequest) async throws -> Workspace {
        var params: [String: Any] = [
            "projectId": request.projectId,
            "targetBranch": request.targetBranch,
            "fresh": request.fresh,
            "workspaceName": request.workspaceName,
            "agentProfile": request.agentProfile,
            "backend": request.backend
        ]
        if let sourceBranch = request.sourceBranch, !sourceBranch.isEmpty {
            params["sourceBranch"] = sourceBranch
        }
        if let sourceWorkspaceID = request.sourceWorkspaceId, !sourceWorkspaceID.isEmpty {
            params["sourceWorkspaceId"] = sourceWorkspaceID
        }
        if let bundle = request.configBundle, !bundle.isEmpty {
            params["configBundle"] = bundle
        }
        let result = try await call("workspace.create", params: params)
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

    public func removeWorkspace(id: String) async throws {
        _ = try await call("workspace.remove", params: ["id": id])
    }

    public func markWorkspaceReady(id: String) async throws {
        _ = try await call("workspace.ready", params: ["workspaceId": id])
    }

    public func listPorts(workspaceId: String) async throws -> [ForwardedPort] {
        let result = try await call("workspace.ports.list", params: ["workspaceId": workspaceId])
        guard let dict = result as? [String: Any] else { return [] }
        return parseForwardedPorts(from: dict["items"])
    }

    public func addPort(workspaceId: String, port: Int) async throws {
        _ = try await call("workspace.ports.add", params: ["workspaceId": workspaceId, "port": port])
    }

    public func removePort(workspaceId: String, port: Int) async throws {
        _ = try await call("workspace.ports.remove", params: ["workspaceId": workspaceId, "port": port])
    }

    public func activateTunnels(workspaceId: String) async throws -> TunnelStatus {
        let result = try await call("workspace.tunnels.activate", params: ["workspaceId": workspaceId])
        guard let dict = result as? [String: Any] else {
            throw RPCError(message: "unexpected workspace.tunnels.activate response")
        }
        return parseTunnelStatus(dict: dict)
    }

    public func deactivateTunnels(workspaceId: String) async throws -> TunnelStatus {
        let result = try await call("workspace.tunnels.deactivate", params: ["workspaceId": workspaceId])
        guard let dict = result as? [String: Any] else {
            throw RPCError(message: "unexpected workspace.tunnels.deactivate response")
        }
        return parseTunnelStatus(dict: dict)
    }

    public func tunnelStatus(workspaceId: String) async throws -> TunnelStatus {
        let result = try await call("workspace.ports.list", params: ["workspaceId": workspaceId])
        guard let dict = result as? [String: Any] else {
            throw RPCError(message: "unexpected workspace.ports.list response")
        }
        let activeWorkspaceId = dict["activeWorkspaceId"] as? String ?? ""
        return TunnelStatus(active: activeWorkspaceId == workspaceId, activeWorkspaceId: activeWorkspaceId)
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
        let ports = parseForwardedPorts(from: dict["spotlight"])
        return WorkspaceInfo(workspaceId: wsId, workspacePath: wsPath, ports: ports)
    }

    public func getDaemonSandboxResourceSettings() async throws -> SandboxResourceSettings {
        let result = try await call("daemon.settings.get", params: [:])
        guard let dict = result as? [String: Any],
              let resources = dict["sandboxResources"] as? [String: Any] else {
            throw RPCError(message: "unexpected daemon.settings.get response")
        }
        return parseSandboxResourceSettings(resources)
    }

    public func updateDaemonSandboxResourceSettings(_ settings: SandboxResourceSettings) async throws -> SandboxResourceSettings {
        let result = try await call("daemon.settings.update", params: [
            "sandboxResources": [
                "defaultMemoryMiB": settings.defaultMemoryMiB,
                "defaultVCPUs": settings.defaultVCPUs,
                "maxMemoryMiB": settings.maxMemoryMiB,
                "maxVCPUs": settings.maxVCPUs,
            ],
        ])
        guard let dict = result as? [String: Any],
              let resources = dict["sandboxResources"] as? [String: Any] else {
            throw RPCError(message: "unexpected daemon.settings.update response")
        }
        return parseSandboxResourceSettings(resources)
    }

    private func parseForwardedPorts(from raw: Any?) -> [ForwardedPort] {
        guard let items = raw as? [[String: Any]] else { return [] }
        return items.compactMap { item in
            let port: Int?
            if let n = item["port"] as? Int {
                port = n
            } else if let n = item["port"] as? NSNumber {
                port = n.intValue
            } else if let n = item["localPort"] as? Int {
                port = n
            } else if let n = item["localPort"] as? NSNumber {
                port = n.intValue
            } else {
                port = nil
            }
            guard let port, port > 0 else { return nil }
            let remote: Int?
            if let r = item["remotePort"] as? Int { remote = r }
            else if let r = item["remotePort"] as? NSNumber { remote = r.intValue }
            else { remote = nil }
            let preferred = item["preferred"] as? Bool ?? false
            let tunneled = item["tunneled"] as? Bool ?? false
            let process = item["process"] as? String
            return ForwardedPort(id: port, remotePort: remote, preferred: preferred, tunneled: tunneled, process: process)
        }
    }

    private func parseTunnelStatus(dict: [String: Any]) -> TunnelStatus {
        TunnelStatus(
            active: dict["active"] as? Bool ?? false,
            activeWorkspaceId: dict["activeWorkspaceId"] as? String ?? ""
        )
    }

    private func parseSandboxResourceSettings(_ dict: [String: Any]) -> SandboxResourceSettings {
        let defaultMemoryMiB = (dict["defaultMemoryMiB"] as? NSNumber)?.intValue ?? 1024
        let defaultVCPUs = (dict["defaultVCPUs"] as? NSNumber)?.intValue ?? 1
        let maxMemoryMiB = (dict["maxMemoryMiB"] as? NSNumber)?.intValue ?? 4096
        let maxVCPUs = (dict["maxVCPUs"] as? NSNumber)?.intValue ?? 4
        return SandboxResourceSettings(
            defaultMemoryMiB: defaultMemoryMiB,
            defaultVCPUs: defaultVCPUs,
            maxMemoryMiB: maxMemoryMiB,
            maxVCPUs: maxVCPUs
        )
    }
    // MARK: - PTY

    /// Opens a PTY session in the workspace.  Returns the session ID.
    public func openPTY(workspaceId: String, cols: Int, rows: Int, useTmux: Bool = false) async throws -> String {
        let result = try await call("pty.open", params: [
            "workspaceId": workspaceId,
            "cols": cols,
            "rows": rows,
            "useTmux": useTmux,
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

    /// Reattaches the current websocket connection to an existing PTY session.
    public func attachPTY(sessionId: String) async throws -> Bool {
        let result = try await call("pty.attach", params: ["sessionId": sessionId])
        guard let dict = result as? [String: Any] else { return false }
        return dict["attached"] as? Bool ?? false
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

    // MARK: - Multi-tab PTY Session Management

    /// Opens a PTY session with a custom name for tab display
    public func openPTY(workspaceId: String, name: String, cols: Int, rows: Int, useTmux: Bool = false) async throws -> String {
        let result = try await call("pty.open", params: [
            "workspaceId": workspaceId,
            "name": name,
            "cols": cols,
            "rows": rows,
            "useTmux": useTmux,
        ])
        guard let dict = result as? [String: Any],
              let sid  = dict["sessionId"] as? String else {
            throw RPCError(message: "unexpected pty.open response")
        }
        return sid
    }

    /// Lists all PTY sessions for a workspace
    public func listPTYSessions(workspaceId: String) async throws -> [PTYSessionInfo] {
        let result = try await call("pty.list", params: ["workspaceId": workspaceId])
        guard let dict = result as? [String: Any],
              let sessions = dict["sessions"] as? [[String: Any]] else {
            return []
        }
        return sessions.compactMap { PTYSessionInfo(from: $0) }
    }

    /// Gets info for a specific PTY session
    public func getPTYSession(sessionId: String) async throws -> PTYSessionInfo {
        let result = try await call("pty.get", params: ["sessionId": sessionId])
        guard let dict = result as? [String: Any],
              let session = dict["session"] as? [String: Any],
              let info = PTYSessionInfo(from: session) else {
            throw RPCError(message: "session not found")
        }
        return info
    }

    /// Renames a PTY session (updates tab name)
    public func renamePTYSession(sessionId: String, name: String) async throws -> Bool {
        let result = try await call("pty.rename", params: [
            "sessionId": sessionId,
            "name": name,
        ])
        guard let dict = result as? [String: Any] else { return false }
        return dict["success"] as? Bool ?? false
    }

    // MARK: - Tmux Support

    /// Executes a tmux command on a tmux-based session
    public func tmuxCommand(sessionId: String, command: String, args: [String] = []) async throws -> TmuxCommandResult {
        let result = try await call("pty.tmux", params: [
            "sessionId": sessionId,
            "command": command,
            "args": args,
        ])
        guard let dict = result as? [String: Any] else {
            throw RPCError(message: "unexpected tmux response")
        }
        return TmuxCommandResult(
            success: dict["success"] as? Bool ?? false,
            output: dict["output"] as? String,
            error: dict["error"] as? String
        )
    }
}

// MARK: - PTY Session Info

public struct PTYSessionInfo: Identifiable, Sendable {
    public let id: String
    public let workspaceId: String
    public let name: String
    public let shell: String
    public let workDir: String
    public let cols: Int
    public let rows: Int
    public let createdAt: String
    public let isRemote: Bool
    public let isTmux: Bool
    public let tmuxSession: String?

    public init?(from dict: [String: Any]) {
        guard let id = dict["id"] as? String,
              let workspaceId = dict["workspaceId"] as? String,
              let name = dict["name"] as? String else { return nil }
        self.id = id
        self.workspaceId = workspaceId
        self.name = name
        self.shell = dict["shell"] as? String ?? "bash"
        self.workDir = dict["workDir"] as? String ?? "/workspace"
        self.cols = dict["cols"] as? Int ?? 80
        self.rows = dict["rows"] as? Int ?? 24
        self.createdAt = dict["createdAt"] as? String ?? ""
        self.isRemote = dict["isRemote"] as? Bool ?? false
        self.isTmux = dict["isTmux"] as? Bool ?? false
        self.tmuxSession = dict["tmuxSession"] as? String
    }
}

// MARK: - Tmux Command Result

public struct TmuxCommandResult: Sendable {
    public let success: Bool
    public let output: String?
    public let error: String?
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
              let port = daemonURL.port else {
            StartupTrace.checkpoint("http.version.skip", "bad daemonURL host/port")
            return nil
        }
        let scheme = daemonURL.scheme == "wss" ? "https" : "http"
        guard let url = URL(string: "\(scheme)://\(host):\(port)/version") else {
            StartupTrace.checkpoint("http.version.skip", "bad version URL")
            return nil
        }
        StartupTrace.checkpoint("http.version.req", url.absoluteString)
        var req = URLRequest(url: url)
        req.timeoutInterval = 2
        guard let (data, _) = try? await URLSession.shared.data(for: req) else {
            StartupTrace.checkpoint("http.version.fail", "no response")
            return nil
        }
        guard let info = try? JSONDecoder().decode(DaemonInfo.self, from: data) else {
            StartupTrace.checkpoint("http.version.fail", "decode")
            return nil
        }
        StartupTrace.checkpoint("http.version.ok", "v=\(info.version) protocol=\(info.protocolVersion)")
        return info
    }
}
