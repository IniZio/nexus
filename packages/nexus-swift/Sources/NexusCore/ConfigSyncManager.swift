import Foundation
import os.log

private let logger = Logger(subsystem: "com.nexus.app", category: "ConfigSync")

public final class ConfigSyncManager: Sendable {
    public static let shared = ConfigSyncManager()

    private let lock = NSLock()
    private var sessions: [String: [String]] = [:]
    private var wrapperPaths: [String: String] = [:]

    private struct SyncSpec {
        let alpha: String
        let beta: String
        let ignores: [String]
        let label: String
    }

    public func startConfigSync(workspaceID: String, backend: String?) {
        guard isSupportedBackend(backend) else { return }

        lock.lock()
        let alreadyStarted = sessions[workspaceID] != nil
        lock.unlock()
        if alreadyStarted { return }

        let mutagenPath = resolveMutagen()
        guard let mutagenPath else {
            logger.warning("ConfigSync: mutagen binary not found, skipping sync for \(workspaceID)")
            return
        }

        let sshConfigPath = nexusSSHConfigPath()
        let wrapperDir = writeSSHWrapper(sshConfigPath: sshConfigPath, workspaceID: workspaceID)

        if let wrapperDir {
            ensureMutagenDaemon(mutagenPath: mutagenPath, sshPath: wrapperDir)
        }

        let specs = syncSpecs()
        var started: [String] = []

        for spec in specs {
            guard FileManager.default.fileExists(atPath: spec.alpha) else { continue }
            let sessionName = "nexus-cfg-\(workspaceID)-\(spec.label)"

            let args = buildMutagenArgs(
                sessionName: sessionName,
                alpha: spec.alpha,
                beta: spec.beta,
                ignores: spec.ignores
            )

            let proc = Process()
            proc.executableURL = URL(fileURLWithPath: mutagenPath)
            proc.arguments = args
            let err = Pipe()
            proc.standardError = err
            proc.standardOutput = Pipe()

            do {
                try proc.run()
                proc.waitUntilExit()
                if proc.terminationStatus == 0 {
                    started.append(sessionName)
                    logger.info("ConfigSync: started session \(sessionName)")
                } else {
                    let errData = err.fileHandleForReading.readDataToEndOfFile()
                    let errStr = String(data: errData, encoding: .utf8) ?? "unknown"
                    logger.warning("ConfigSync: mutagen sync create failed for \(sessionName): \(errStr)")
                }
            } catch {
                logger.warning("ConfigSync: failed to run mutagen for \(sessionName): \(error.localizedDescription)")
            }
        }

        if !started.isEmpty {
            lock.lock()
            sessions[workspaceID] = started
            lock.unlock()
        }
    }

    public func stopConfigSync(workspaceID: String) {
        lock.lock()
        let sessionNames = sessions.removeValue(forKey: workspaceID) ?? []
        let wrapperPath = wrapperPaths.removeValue(forKey: workspaceID)
        lock.unlock()

        if let wrapperPath {
            try? FileManager.default.removeItem(atPath: wrapperPath)
        }
        guard !sessionNames.isEmpty || wrapperPath != nil else { return }

        let mutagenPath = resolveMutagen() ?? "mutagen"

        for name in sessionNames {
            let proc = Process()
            proc.executableURL = URL(fileURLWithPath: mutagenPath)
            proc.arguments = ["sync", "terminate", name]
            let err = Pipe()
            proc.standardError = err
            proc.standardOutput = Pipe()
            do {
                try proc.run()
                proc.waitUntilExit()
            } catch {
                logger.warning("ConfigSync: failed to terminate session \(name): \(error.localizedDescription)")
            }
        }
        logger.info("ConfigSync: terminated \(sessionNames.count) sessions for \(workspaceID)")
    }

    // MARK: - Private

    private func ensureMutagenDaemon(mutagenPath: String, sshPath: String) {
        let probe = Process()
        probe.executableURL = URL(fileURLWithPath: mutagenPath)
        probe.arguments = ["daemon", "status"]
        probe.standardOutput = Pipe()
        probe.standardError = Pipe()
        let alreadyRunning: Bool = {
            guard (try? probe.run()) != nil else { return false }
            probe.waitUntilExit()
            return probe.terminationStatus == 0
        }()

        if alreadyRunning {
            let stop = Process()
            stop.executableURL = URL(fileURLWithPath: mutagenPath)
            stop.arguments = ["daemon", "stop"]
            stop.standardOutput = Pipe()
            stop.standardError = Pipe()
            try? stop.run()
            stop.waitUntilExit()
            Thread.sleep(forTimeInterval: 0.5)
        }

        let start = Process()
        start.executableURL = URL(fileURLWithPath: mutagenPath)
        start.arguments = ["daemon", "start"]
        var env = ProcessInfo.processInfo.environment
        env["MUTAGEN_SSH_PATH"] = sshPath
        start.environment = env
        start.standardOutput = Pipe()
        start.standardError = Pipe()
        do {
            try start.run()
            start.waitUntilExit()
            if start.terminationStatus == 0 {
                logger.info("ConfigSync: started mutagen daemon with SSH path \(sshPath)")
            } else {
                logger.error("ConfigSync: mutagen daemon start failed — config sync will not work")
            }
        } catch {
            logger.error("ConfigSync: failed to start mutagen daemon: \(error.localizedDescription)")
        }
    }

    private func isSupportedBackend(_ backend: String?) -> Bool {
        guard let b = backend?.trimmingCharacters(in: .whitespacesAndNewlines).lowercased(),
              !b.isEmpty else { return false }
        return b == "firecracker"
    }

    private func resolveMutagen() -> String? {
        return ToolBinaryResolver.resolvePreferred("mutagen")
    }

    private func nexusSSHConfigPath() -> String {
        let home = FileManager.default.homeDirectoryForCurrentUser.path
        return "\(home)/.nexus/ssh/nexus.ssh.config"
    }

    private func syncSpecs() -> [SyncSpec] {
        let home = FileManager.default.homeDirectoryForCurrentUser.path
        let host = "nexus"

        return [
            SyncSpec(
                alpha: "\(home)/.config/opencode",
                beta: "\(host):~/.config/opencode",
                ignores: ["auth.json", "mcp-auth.json"],
                label: "config"
            ),
            SyncSpec(
                alpha: "\(home)/.local/share/opencode",
                beta: "\(host):~/.local/share/opencode",
                ignores: [],
                label: "auth"
            ),
        ]
    }

    private func buildMutagenArgs(
        sessionName: String,
        alpha: String,
        beta: String,
        ignores: [String]
    ) -> [String] {
        var args = [
            "sync", "create",
            "--name", sessionName,
            "--sync-mode", "one-way-replica",
            "--watch-polling-interval", "5",
            "--symlink-mode", "ignore",
        ]
        for ignore in ignores {
            args += ["--ignore", ignore]
        }
        args += [alpha, beta]
        return args
    }

    private func writeSSHWrapper(sshConfigPath: String, workspaceID: String) -> String? {
        guard FileManager.default.fileExists(atPath: sshConfigPath) else { return nil }
        let baseDir = (NSTemporaryDirectory() as NSString).appendingPathComponent("nexus-configsync")
        let wrapperDir = (baseDir as NSString).appendingPathComponent(workspaceID)
        let fm = FileManager.default
        try? fm.createDirectory(atPath: wrapperDir, withIntermediateDirectories: true)
        let quoted = shellQuote(sshConfigPath)
        let sshScript = "#!/bin/sh\nexec /usr/bin/ssh -F \(quoted) \"$@\"\n"
        let scpScript = "#!/bin/sh\nexec /usr/bin/scp -F \(quoted) \"$@\"\n"
        do {
            try sshScript.write(toFile: (wrapperDir as NSString).appendingPathComponent("ssh"), atomically: true, encoding: .utf8)
            try scpScript.write(toFile: (wrapperDir as NSString).appendingPathComponent("scp"), atomically: true, encoding: .utf8)
            try fm.setAttributes([.posixPermissions: 0o755], ofItemAtPath: (wrapperDir as NSString).appendingPathComponent("ssh"))
            try fm.setAttributes([.posixPermissions: 0o755], ofItemAtPath: (wrapperDir as NSString).appendingPathComponent("scp"))
            lock.lock()
            wrapperPaths[workspaceID] = wrapperDir
            lock.unlock()
            return wrapperDir
        } catch {
            logger.warning("ConfigSync: failed to write SSH wrapper: \(error.localizedDescription)")
            return nil
        }
    }

    private func shellQuote(_ s: String) -> String {
        "'" + s.replacingOccurrences(of: "'", with: "'\\''") + "'"
    }
}
