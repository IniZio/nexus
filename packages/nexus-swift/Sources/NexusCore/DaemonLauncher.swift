import Foundation

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
///   2. App bundle Resources (production: `Contents/Resources/nexus-daemon`)
///   3. Next to the running executable (co-installed / app bundle)
///   4. $PATH via `which nexus-daemon`
///   5. Dev layout: walk up from the executable looking for
///      `packages/nexus/nexus-daemon` or `nexus/nexus-daemon`
///      (covers `swift run` from packages/nexus-swift/).
public struct DaemonLauncher {

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
    public static func resolveRunningPort() -> Int? {
        let runDir = resolveRunDir()
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

    /// Stops any running nexus-daemon and removes PID files.
    ///
    /// First attempts to SIGTERM each PID recorded in PID files.  Then, as a
    /// fallback for stale PID files (where the recorded PID no longer matches
    /// the actual process), uses `pkill` to terminate any remaining process
    /// named "nexus-daemon".
    public static func killRunning() {
        let runDir = resolveRunDir()
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

        // Fallback: stale PID files mean the recorded PID may not be the real
        // process.  Use pkill to catch any surviving nexus-daemon.
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
        let fm = FileManager.default
        let env = ProcessInfo.processInfo.environment

        // 0. Environment override (CI / developer convenience)
        if let envBin = env["NEXUS_DAEMON_BIN"], !envBin.isEmpty {
            let u = URL(fileURLWithPath: envBin)
            if fm.isExecutableFile(atPath: u.path) { return u }
        }

        // 1. Bundled resource (production app bundle: Contents/Resources/nexus-daemon)
        if let resourceURL = Bundle.main.resourceURL {
            let bundled = resourceURL.appendingPathComponent("nexus-daemon")
            if fm.isExecutableFile(atPath: bundled.path) { return bundled }
        }

        // 2. Co-located with this executable (legacy co-install layout)
        let exeURL: URL = {
            if let u = Bundle.main.executableURL { return u.resolvingSymlinksInPath() }
            let cwd = FileManager.default.currentDirectoryPath
            let arg0 = ProcessInfo.processInfo.arguments.first ?? ""
            let raw = arg0.hasPrefix("/") ? arg0 : "\(cwd)/\(arg0)"
            return URL(fileURLWithPath: raw).standardized
        }()
        let colocated = exeURL.deletingLastPathComponent().appendingPathComponent("nexus-daemon")
        if fm.isExecutableFile(atPath: colocated.path) { return colocated }

        // 3. $PATH
        if let path = which("nexus-daemon") { return URL(fileURLWithPath: path) }

        // 4. Dev layout: walk up from exe looking for the Go binary.
        //    Covers `swift run` from packages/nexus-swift/ where the binary
        //    lives at packages/nexus/nexus-daemon (4 levels up from the exe).
        var dir = exeURL.deletingLastPathComponent()
        for _ in 0..<8 {
            for sub in ["packages/nexus/nexus-daemon", "nexus/nexus-daemon"] {
                let c = dir.appendingPathComponent(sub)
                if fm.isExecutableFile(atPath: c.path) { return c }
            }
            dir = dir.deletingLastPathComponent()
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
    public static func ensureRunning(port: Int = 63987, timeout: TimeInterval = 10) async throws {
        // Reuse a daemon only if WE recorded its PID and it is still healthy.
        let targetPort: Int
        if let recorded = resolveRunningPort(), await isHealthy(port: recorded) {
            return  // Our managed daemon is already healthy.
        } else {
            targetPort = resolveRunningPort() ?? port
        }

        guard let binary = resolveBinary() else {
            throw DaemonLaunchError.binaryNotFound
        }

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
        proc.arguments = ["--port", "\(targetPort)"]
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
        try? "\(proc.processIdentifier)".write(
            toFile: runDir + "/daemon.pid", atomically: true, encoding: .utf8)

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

    private static func which(_ name: String) -> String? {
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
}
