import Foundation
import Security

// MARK: - Errors

public enum DaemonLaunchError: Error, LocalizedError {
    case binaryNotFound
    case launchFailed(String)
    case timeout

    public var errorDescription: String? {
        switch self {
        case .binaryNotFound:
            return "nexus-daemon not found. Install Nexus or set NEXUS_DAEMON_BIN to its path."
        case .launchFailed(let msg):
            return "Failed to start daemon: \(msg)"
        case .timeout:
            return "Daemon did not become ready in time."
        }
    }
}

// MARK: - DaemonLauncher

/// Finds, health-checks, and auto-starts the nexus workspace daemon.
///
/// Binary resolution order:
///   1. NEXUS_DAEMON_BIN environment variable (CI / developer override)
///   2. Local dev source daemon (only when `NEXUS_USE_SOURCE_DAEMON=1`)
///   3. App bundle Resources daemon (preferred to avoid app/daemon skew)
///   4. Downloaded/system daemon fallback (`which nexus-daemon`)
///   5. Next to the running executable (legacy co-install layout)
///   6. Dev layout fallback: walk up from the executable looking for
///      `packages/nexus/nexus-daemon` or `nexus/nexus-daemon`
///      (covers `swift run` from packages/nexus-swift/).
public struct DaemonLauncher {

    public enum DaemonBinarySource: String {
        case envOverride = "explicit"
        case localSource = "local-source"
        case bundled = "bundled"
        case externalInstalled = "external-installed"
        case colocated = "colocated"
        case devFallback = "dev-fallback"
        case notFound = "not-found"
    }

    public struct RuntimeVariantInfo {
        public let mode: String
        public let preferredPort: Int
        public let binarySource: DaemonBinarySource
        public let binaryPath: String
    }

    // MARK: - Port preferences

    /// Default daemon port for all local app modes.
    private static let standardPort = 63987
    private static let processPortBase = 64100
    private static let processPortSpan = 900

    /// Resolves the preferred daemon port for this app instance.
    ///
    /// Priority:
    ///  1. `NEXUS_DAEMON_PORT` (explicit override)
    ///  2. Standard mode defaults to `standardPort`
    public static func preferredPort(env: [String: String] = ProcessInfo.processInfo.environment) -> Int {
        if let raw = env["NEXUS_DAEMON_PORT"]?.trimmingCharacters(in: .whitespacesAndNewlines),
           let parsed = Int(raw), (1...65535).contains(parsed) {
            return parsed
        }
        return resolveWorktreeAwarePort(env: env)
    }

    /// Returns user-visible daemon variant diagnostics for the current app runtime.
    public static func runtimeVariantInfo(env: [String: String] = ProcessInfo.processInfo.environment) -> RuntimeVariantInfo {
        let prefersSource = shouldPreferSourceDaemon(env: env)
        let mode = prefersSource ? "local-source" : "bundled-or-installed"
        let preferred = preferredPort(env: env)
        if let resolved = resolveBinaryWithSource(env: env) {
            return RuntimeVariantInfo(
                mode: mode,
                preferredPort: preferred,
                binarySource: resolved.source,
                binaryPath: resolved.url.path
            )
        }
        return RuntimeVariantInfo(
            mode: mode,
            preferredPort: preferred,
            binarySource: .notFound,
            binaryPath: "unresolved"
        )
    }

    // MARK: - Health check

    /// Returns true if the daemon is already answering /healthz on `port`.
    public static func isHealthy(port: Int) async -> Bool {
        guard let url = URL(string: "http://localhost:\(port)/healthz") else { return false }
        var req = URLRequest(url: url)
        req.timeoutInterval = 0.6
        return await (try? URLSession.shared.data(for: req))
            .flatMap { _, resp in (resp as? HTTPURLResponse)?.statusCode == 200 } ?? false
    }

    // MARK: - Port discovery

    /// Returns the port of the currently recorded daemon, or nil if no PID file exists.
    ///
    /// If a preferred port is provided and its PID file exists, that value is returned first.
    public static func resolveRunningPort(preferredPort: Int? = nil) -> Int? {
        let runDir = resolveRunDir()
        if let preferredPort {
            let preferredPath = "\(runDir)/daemon-\(preferredPort).pid"
            if FileManager.default.fileExists(atPath: preferredPath) {
                return preferredPort
            }
        }
        guard let entries = try? FileManager.default.contentsOfDirectory(atPath: runDir) else {
            return nil
        }
        for entry in entries where entry.hasPrefix("daemon-") && entry.hasSuffix(".pid") {
            let inner = String(entry.dropFirst("daemon-".count).dropLast(".pid".count))
            if let port = Int(inner) { return port }
        }
        return nil
    }

    // MARK: - Kill existing

    /// Stops nexus-daemon process(es) and removes PID files.
    ///
    /// When `port` is provided, only that daemon PID file is targeted so multiple
    /// local daemon variants can run side-by-side.
    public static func killRunning(port: Int? = nil) {
        let runDir = resolveRunDir()

        if let targetPort = port {
            let specificPIDPath = "\(runDir)/daemon-\(targetPort).pid"
            var targetedPID: Int32?
            if let pidStr = try? String(contentsOfFile: specificPIDPath, encoding: .utf8)
                                    .trimmingCharacters(in: .whitespacesAndNewlines),
               let pid = Int32(pidStr), pid > 1 {
                targetedPID = pid
                Foundation.kill(pid, SIGTERM)
            }
            try? FileManager.default.removeItem(atPath: specificPIDPath)
            if let genericPidStr = try? String(contentsOfFile: "\(runDir)/daemon.pid", encoding: .utf8)
                .trimmingCharacters(in: .whitespacesAndNewlines),
               let pid = Int32(genericPidStr), pid > 1,
               let targetedPID, pid == targetedPID {
                try? FileManager.default.removeItem(atPath: "\(runDir)/daemon.pid")
            }
            // If no recorded PID was available (or it failed to stop), terminate any
            // daemon still listening on the target port so we can relaunch latest.
            killProcessListening(on: targetPort)
            return
        }

        if let entries = try? FileManager.default.contentsOfDirectory(atPath: runDir) {
            for entry in entries where entry.hasSuffix(".pid") {
                let path = "\(runDir)/\(entry)"
                if let pidStr = try? String(contentsOfFile: path, encoding: .utf8)
                                        .trimmingCharacters(in: .whitespacesAndNewlines),
                   let pid = Int32(pidStr), pid > 1 {
                    Foundation.kill(pid, SIGTERM)
                }
                try? FileManager.default.removeItem(atPath: path)
            }
        }

        // Broad fallback only when no target port was requested.
        let pkill = Process()
        pkill.executableURL = URL(fileURLWithPath: "/usr/bin/pkill")
        pkill.arguments = ["-TERM", "nexus-daemon"]
        pkill.standardOutput = FileHandle.nullDevice
        pkill.standardError  = FileHandle.nullDevice
        try? pkill.run()
        pkill.waitUntilExit()
    }

    // MARK: - Binary resolution

    public static func resolveBinary() -> URL? {
        return resolveBinaryWithSource()?.url
    }

    private static func resolveBinaryWithSource(env: [String: String] = ProcessInfo.processInfo.environment) -> (url: URL, source: DaemonBinarySource)? {
        let fm = FileManager.default

        // 0. Environment override (CI / developer convenience)
        if let envBin = env["NEXUS_DAEMON_BIN"], !envBin.isEmpty {
            let u = URL(fileURLWithPath: envBin)
            if fm.isExecutableFile(atPath: u.path) { return (u, .envOverride) }
        }

        // 1. Development override: prefer source daemon during local app development.
        //    This keeps local Swift app runs aligned with active Go daemon sources.
        if shouldPreferSourceDaemon(env: env), let devBinary = resolveDevBinary() {
            return (devBinary, .localSource)
        }

        // 2. Prefer bundled daemon to keep app and daemon versions aligned.
        if let resourceURL = Bundle.main.resourceURL {
            let bundledCandidates = [
                resourceURL.appendingPathComponent("nexus-daemon"),
                resourceURL.appendingPathComponent("tools/nexus-daemon"),
            ]
            for bundled in bundledCandidates where fm.isExecutableFile(atPath: bundled.path) {
                return (bundled, .bundled)
            }
        }

        // 3. Downloaded/system daemon fallback.
        if let path = which("nexus-daemon") { return (URL(fileURLWithPath: path), .externalInstalled) }

        // 4. Co-located with this executable (legacy co-install layout)
        let exeURL: URL = {
            if let u = Bundle.main.executableURL { return u.resolvingSymlinksInPath() }
            let cwd = FileManager.default.currentDirectoryPath
            let arg0 = ProcessInfo.processInfo.arguments.first ?? ""
            let raw = arg0.hasPrefix("/") ? arg0 : "\(cwd)/\(arg0)"
            return URL(fileURLWithPath: raw).standardized
        }()
        let colocated = exeURL.deletingLastPathComponent().appendingPathComponent("nexus-daemon")
        if fm.isExecutableFile(atPath: colocated.path) { return (colocated, .colocated) }

        // 5. Dev layout fallback.
        if let fallback = resolveDevBinary() {
            return (fallback, .devFallback)
        }
        return nil
    }

    // MARK: - Launch

    /// Ensures a nexus-daemon we own is running on `port`.
    ///
    /// If a healthy daemon is already recorded in our PID files on `port`,
    /// returns immediately (nothing to do).  Otherwise, launches a fresh
    /// daemon on `port`, waits up to `timeout` seconds for /healthz, then
    /// returns.  The daemon runs detached and outlives the app process.
    ///
    /// Unlike `isHealthy`, this does NOT treat any arbitrary process that
    /// happens to answer /healthz as "our daemon" — it only reuses daemons
    /// whose PID was recorded by a previous `ensureRunning` call.
    public static func ensureRunning(port: Int? = nil, timeout: TimeInterval = 10) async throws {
        let targetPort = port ?? preferredPort()

        // Reuse a daemon only if WE recorded its PID and it is still healthy.
        if let recorded = resolveRunningPort(preferredPort: targetPort), await isHealthy(port: recorded) {
            return  // Our managed daemon is already healthy.
        }

        // If an unmanaged daemon is occupying the port, replace it with latest.
        if await isHealthy(port: targetPort) {
            killProcessListening(on: targetPort)
            try? await Task.sleep(for: .seconds(0.2))
        }

        guard let binary = resolveBinary() else {
            throw DaemonLaunchError.binaryNotFound
        }
        let daemonToken = try resolveOrCreateLaunchToken()
        setenv("NEXUS_DAEMON_TOKEN", daemonToken, 1)

        let runDir = resolveRunDir()
        try FileManager.default.createDirectory(
            atPath: runDir, withIntermediateDirectories: true, attributes: nil)

        let logPath = runDir + "/daemon.log"
        FileManager.default.createFile(atPath: logPath, contents: nil)
        guard let logHandle = try? FileHandle(forWritingTo: URL(fileURLWithPath: logPath)) else {
            throw DaemonLaunchError.launchFailed("cannot open log file at \(logPath)")
        }

        let proc = Process()
        proc.executableURL = binary
        proc.arguments = ["--port", "\(targetPort)", "--token", daemonToken]
        proc.standardInput = FileHandle.nullDevice
        proc.standardOutput = logHandle
        proc.standardError = logHandle

        // GUI-launched apps have a stripped PATH that typically excludes
        // /opt/homebrew/bin (where limactl lives).  Prepend common tool
        // locations so the daemon can find limactl for firecracker preflight.
        var env = ProcessInfo.processInfo.environment
        let extraPaths = [
            "/opt/homebrew/bin",
            "/opt/homebrew/sbin",
            "/usr/local/bin",
        ].filter { FileManager.default.fileExists(atPath: $0) }
        if !extraPaths.isEmpty {
            let existingPath = env["PATH"] ?? "/usr/bin:/bin:/usr/sbin:/sbin"
            let combined = extraPaths.joined(separator: ":") + ":" + existingPath
            env["PATH"] = combined
        }
        proc.environment = env

        do {
            try proc.run()
        } catch {
            throw DaemonLaunchError.launchFailed(error.localizedDescription)
        }

        // Record PID so URL discovery and the token reader can find this daemon.
        let pidPath = runDir + "/daemon-\(targetPort).pid"
        try? "\(proc.processIdentifier)".write(toFile: pidPath, atomically: true, encoding: .utf8)
        if let root = processWorktreeRoot() {
            let canonicalRoot = URL(fileURLWithPath: root).resolvingSymlinksInPath().path
            let ownerPath = runDir + "/daemon-\(targetPort).owner"
            try? (canonicalRoot + "\n").write(toFile: ownerPath, atomically: true, encoding: .utf8)
        }
        if targetPort == preferredPort() {
            try? "\(proc.processIdentifier)".write(
                toFile: runDir + "/daemon.pid", atomically: true, encoding: .utf8)
        }

        // Poll /healthz until the daemon is ready.
        // NOTE: do NOT call proc.waitUntilExit() — it would block forever.
        let deadline = Date(timeIntervalSinceNow: timeout)
        var interval: TimeInterval = 0.1
        while Date() < deadline {
            if await isHealthy(port: targetPort) { return }
            try await Task.sleep(for: .seconds(interval))
            interval = min(interval * 1.5, 0.5)
        }
        throw DaemonLaunchError.timeout
    }

    // MARK: - Helpers

    static func resolveRunDir() -> String {
        let env = ProcessInfo.processInfo.environment
        if let xdg = env["XDG_RUNTIME_DIR"], !xdg.isEmpty {
            return xdg + "/nexus"
        }
        let configHome = env["XDG_CONFIG_HOME"]
            ?? (FileManager.default.homeDirectoryForCurrentUser.path + "/.config")
        return configHome + "/nexus/run"
    }

    static func which(_ name: String) -> String? {
        let proc = Process()
        proc.executableURL = URL(fileURLWithPath: "/usr/bin/which")
        proc.arguments = [name]
        let pipe = Pipe()
        proc.standardOutput = pipe
        proc.standardError = FileHandle.nullDevice
        guard (try? proc.run()) != nil else { return nil }
        proc.waitUntilExit()
        guard proc.terminationStatus == 0 else { return nil }
        let raw = String(data: pipe.fileHandleForReading.readDataToEndOfFile(), encoding: .utf8)?
            .trimmingCharacters(in: .whitespacesAndNewlines)
        return raw?.isEmpty == false ? raw : nil
    }

    private static func killProcessListening(on port: Int) {
        let script = "pids=$(lsof -ti tcp:\(port) -sTCP:LISTEN 2>/dev/null); if [ -n \"$pids\" ]; then kill -TERM $pids 2>/dev/null || true; fi"
        let proc = Process()
        proc.executableURL = URL(fileURLWithPath: "/bin/sh")
        proc.arguments = ["-lc", script]
        proc.standardOutput = FileHandle.nullDevice
        proc.standardError = FileHandle.nullDevice
        try? proc.run()
        proc.waitUntilExit()
    }

    /// When `true`, resolves `packages/nexus/nexus-daemon` from the repo tree before the app-bundled binary.
    /// Opt-in only: in DEBUG, preferring the source tree by default caused eyeball runs to launch a stale or
    /// mismatched daemon while the bundled `Resources/nexus-daemon` (from `task swift:prepare-resources`) was ignored.
    private static func shouldPreferSourceDaemon(env: [String: String]) -> Bool {
        env["NEXUS_USE_SOURCE_DAEMON"] == "1"
    }

    private static func resolveDevBinary() -> URL? {
        let fm = FileManager.default
        let exeURL: URL = {
            if let u = Bundle.main.executableURL { return u.resolvingSymlinksInPath() }
            let cwd = FileManager.default.currentDirectoryPath
            let arg0 = ProcessInfo.processInfo.arguments.first ?? ""
            let raw = arg0.hasPrefix("/") ? arg0 : "\(cwd)/\(arg0)"
            return URL(fileURLWithPath: raw).standardized
        }()
        var dir = exeURL.deletingLastPathComponent()
        for _ in 0..<8 {
            for sub in ["packages/nexus/nexus-daemon", "nexus/nexus-daemon"] {
                let candidate = dir.appendingPathComponent(sub)
                if fm.isExecutableFile(atPath: candidate.path) { return candidate }
            }
            dir = dir.deletingLastPathComponent()
        }
        return nil
    }

    private static func resolveOrCreateLaunchToken() throws -> String {
        let env = ProcessInfo.processInfo.environment
        if let token = env["NEXUS_DAEMON_TOKEN"]?.trimmingCharacters(in: .whitespacesAndNewlines),
           !token.isEmpty {
            return token
        }

        let service = daemonTokenService(env: env)
        if let keychainToken = readMacKeychainPassword(service: service) {
            return keychainToken
        }

        let token = try generateSecureToken()
        guard writeMacKeychainPassword(token, service: service) else {
            throw DaemonLaunchError.launchFailed("failed to write daemon token to keychain service \(service)")
        }
        return token
    }

    private static func resolveWorktreeAwarePort(env: [String: String]) -> Int {
        guard let root = processWorktreeRoot() else {
            return standardPort
        }
        let canonicalRoot = URL(fileURLWithPath: root).resolvingSymlinksInPath().path
        let runDir = resolveRunDir()
        let preferred = preferredProcessPort(canonicalRoot)
        for offset in 0..<processPortSpan {
            let candidate = processPortBase + ((preferred - processPortBase + offset) % processPortSpan)
            let owner = readDaemonOwner(runDir: runDir, port: candidate)
            if isPortHealthySync(candidate) {
                if owner == canonicalRoot { return candidate }
                continue
            }
            if let owner, !owner.isEmpty, owner != canonicalRoot {
                let ownerPath = "\(runDir)/daemon-\(candidate).owner"
                try? FileManager.default.removeItem(atPath: ownerPath)
            }
            return candidate
        }
        return preferred
    }

    private static func processWorktreeRoot() -> String? {
        let fm = FileManager.default
        var dir = URL(fileURLWithPath: fm.currentDirectoryPath).standardizedFileURL
        while true {
            let workspacePath = dir.appendingPathComponent(".nexus/workspace.json").path
            if fm.fileExists(atPath: workspacePath),
               workspaceConfigEnablesProcessIsolation(atPath: workspacePath) {
                return dir.path
            }
            let parent = dir.deletingLastPathComponent()
            if parent.path == dir.path { break }
            dir = parent
        }
        return nil
    }

    private static func workspaceConfigEnablesProcessIsolation(atPath path: String) -> Bool {
        struct WorkspaceConfig: Decodable {
            struct Isolation: Decodable {
                let level: String?
            }
            let isolation: Isolation?
        }
        guard let data = try? Data(contentsOf: URL(fileURLWithPath: path)),
              let cfg = try? JSONDecoder().decode(WorkspaceConfig.self, from: data) else {
            return false
        }
        return cfg.isolation?.level == "process"
    }

    private static func preferredProcessPort(_ canonicalRoot: String) -> Int {
        var hash: UInt32 = 2166136261
        for byte in canonicalRoot.utf8 {
            hash ^= UInt32(byte)
            hash = hash &* 16777619
        }
        return processPortBase + Int(hash % UInt32(processPortSpan))
    }

    private static func readDaemonOwner(runDir: String, port: Int) -> String? {
        let path = "\(runDir)/daemon-\(port).owner"
        guard let raw = try? String(contentsOfFile: path, encoding: .utf8) else { return nil }
        let trimmed = raw.trimmingCharacters(in: .whitespacesAndNewlines)
        return trimmed.isEmpty ? nil : trimmed
    }

    private static func isPortHealthySync(_ port: Int) -> Bool {
        guard let url = URL(string: "http://localhost:\(port)/healthz") else { return false }
        var req = URLRequest(url: url)
        req.timeoutInterval = 0.4
        let sem = DispatchSemaphore(value: 0)
        final class Box: @unchecked Sendable { var ok = false }
        let box = Box()
        URLSession.shared.dataTask(with: req) { _, resp, _ in
            if let http = resp as? HTTPURLResponse {
                box.ok = http.statusCode == 200
            }
            sem.signal()
        }.resume()
        _ = sem.wait(timeout: .now() + 1)
        return box.ok
    }

    private static func daemonTokenService(env: [String: String]) -> String {
        let configured = env["NEXUS_DAEMON_TOKEN_KEYCHAIN_SERVICE"]?
            .trimmingCharacters(in: .whitespacesAndNewlines)
        if let configured, !configured.isEmpty {
            return configured
        }
        return "nexus-daemon-token"
    }

    private static func generateSecureToken() throws -> String {
        var bytes = [UInt8](repeating: 0, count: 32)
        let status = SecRandomCopyBytes(kSecRandomDefault, bytes.count, &bytes)
        guard status == errSecSuccess else {
            throw DaemonLaunchError.launchFailed("failed to generate daemon token")
        }
        let data = Data(bytes)
        let token = data.base64EncodedString()
            .replacingOccurrences(of: "+", with: "-")
            .replacingOccurrences(of: "/", with: "_")
            .replacingOccurrences(of: "=", with: "")
            .trimmingCharacters(in: .whitespacesAndNewlines)
        guard !token.isEmpty else {
            throw DaemonLaunchError.launchFailed("generated daemon token was empty")
        }
        return token
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

    private static func writeMacKeychainPassword(_ token: String, service: String) -> Bool {
        let task = Process()
        task.executableURL = URL(fileURLWithPath: "/usr/bin/security")
        task.arguments = [
            "add-generic-password",
            "-U",
            "-s", service,
            "-a", "nexus-daemon",
            "-w", token,
        ]
        task.standardOutput = Pipe()
        task.standardError = Pipe()

        do {
            try task.run()
        } catch {
            return false
        }
        task.waitUntilExit()
        return task.terminationStatus == 0
    }
}

enum ToolBinaryResolver {
    static let managedToolNames = ["nexus", "nexus-daemon", "mutagen", "limactl", "tmux"]

    static func resolvePreferred(_ name: String) -> String? {
        let downloaded = DaemonLauncher.which(name)
        let bundled = bundledPath(name)
        guard let downloaded else { return bundled }
        guard let bundled else { return downloaded }

        if let d = semanticVersion(of: downloaded), let b = semanticVersion(of: bundled) {
            return compareVersions(d, b) >= 0 ? downloaded : bundled
        }
        // If we cannot parse versions, prefer external/downloaded install.
        return downloaded
    }

    static func preferredPathEntries() -> [String] {
        var entries: [String] = []
        var seen = Set<String>()
        for name in managedToolNames {
            guard let path = resolvePreferred(name) else { continue }
            let dir = URL(fileURLWithPath: path).deletingLastPathComponent().path
            if seen.insert(dir).inserted {
                entries.append(dir)
            }
        }
        return entries
    }

    private static func bundledPath(_ name: String) -> String? {
        guard let resourceURL = Bundle.main.resourceURL else { return nil }
        let fm = FileManager.default
        let candidates = [
            resourceURL.appendingPathComponent("tools/\(name)").path,
            resourceURL.appendingPathComponent(name).path,
        ]
        for candidate in candidates where fm.isExecutableFile(atPath: candidate) {
            return candidate
        }
        return nil
    }

    private static func semanticVersion(of executablePath: String) -> [Int]? {
        let proc = Process()
        proc.executableURL = URL(fileURLWithPath: executablePath)
        proc.arguments = ["--version"]
        let out = Pipe()
        proc.standardOutput = out
        proc.standardError = out
        guard (try? proc.run()) != nil else { return nil }
        proc.waitUntilExit()
        guard proc.terminationStatus == 0 else { return nil }
        let text = String(data: out.fileHandleForReading.readDataToEndOfFile(), encoding: .utf8) ?? ""
        guard let match = text.range(of: #"\d+\.\d+\.\d+"#, options: .regularExpression) else {
            return nil
        }
        return text[match].split(separator: ".").compactMap { Int($0) }
    }

    private static func compareVersions(_ lhs: [Int], _ rhs: [Int]) -> Int {
        let count = max(lhs.count, rhs.count)
        for i in 0..<count {
            let l = i < lhs.count ? lhs[i] : 0
            let r = i < rhs.count ? rhs[i] : 0
            if l != r {
                return l < r ? -1 : 1
            }
        }
        return 0
    }
}
