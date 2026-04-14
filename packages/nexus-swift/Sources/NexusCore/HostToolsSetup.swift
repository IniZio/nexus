import Foundation

public struct HostToolCheck: Identifiable, Sendable {
    public let id: String
    public let name: String
    public let isInstalled: Bool
    public let details: String
    public let installFormula: String?

    public init(id: String, name: String, isInstalled: Bool, details: String, installFormula: String?) {
        self.id = id
        self.name = name
        self.isInstalled = isInstalled
        self.details = details
        self.installFormula = installFormula
    }
}

public struct HostToolSnapshot: Sendable {
    public let checks: [HostToolCheck]
    public let nestedVirtualizationSupported: Bool
    public let hasHomebrew: Bool

    public init(checks: [HostToolCheck], nestedVirtualizationSupported: Bool, hasHomebrew: Bool) {
        self.checks = checks
        self.nestedVirtualizationSupported = nestedVirtualizationSupported
        self.hasHomebrew = hasHomebrew
    }
}

public struct HostSetupProgress: Sendable {
    public let step: Int
    public let totalSteps: Int
    public let title: String
    public let detail: String?

    public init(step: Int, totalSteps: Int, title: String, detail: String? = nil) {
        self.step = step
        self.totalSteps = totalSteps
        self.title = title
        self.detail = detail
    }
}

public enum HostToolsSetupError: LocalizedError {
    case homebrewMissing
    case installFailed(String)
    case provisioningFailed(String)
    case daemonInstallFailed(String)

    public var errorDescription: String? {
        switch self {
        case .homebrewMissing:
            return "Homebrew is required to install host tools. Install from https://brew.sh and retry."
        case .installFailed(let output):
            return output.isEmpty ? "Tool installation failed." : output
        case .provisioningFailed(let output):
            return output.isEmpty ? "Runtime provisioning failed." : output
        case .daemonInstallFailed(let output):
            return output.isEmpty ? "Daemon install/update failed." : output
        }
    }
}

public enum HostToolsSetup {
    public static func inspect() async -> HostToolSnapshot {
        let hasBrew = await commandExists("brew")
        let hasLima = await commandExists("limactl")
        let hasMutagen = await commandExists("mutagen")
        let hasTmux = await commandExists("tmux")
        let hasNexus = await commandExists("nexus")
        let hasNexusDaemon = await commandExists("nexus-daemon")
        let nestedSupported = await checkNestedVirtualizationSupport()

        return HostToolSnapshot(
            checks: [
                HostToolCheck(
                    id: "lima",
                    name: "Lima",
                    isInstalled: hasLima,
                    details: resolvedDetail(name: "limactl", fallbackInstalled: "limactl available") ?? "Required for macOS firecracker runtime",
                    installFormula: "lima"
                ),
                HostToolCheck(
                    id: "mutagen",
                    name: "Mutagen",
                    isInstalled: hasMutagen,
                    details: resolvedDetail(name: "mutagen", fallbackInstalled: "mutagen available") ?? "Recommended for sync performance",
                    installFormula: "mutagen-io/mutagen/mutagen"
                ),
                HostToolCheck(
                    id: "tmux",
                    name: "tmux",
                    isInstalled: hasTmux,
                    details: resolvedDetail(name: "tmux", fallbackInstalled: "tmux available") ?? "Required for terminal session persistence",
                    installFormula: "tmux"
                ),
                HostToolCheck(
                    id: "nexus",
                    name: "Nexus CLI",
                    isInstalled: hasNexus,
                    details: resolvedDetail(name: "nexus", fallbackInstalled: "nexus available") ?? "Required for daemon install/update flow",
                    installFormula: nil
                ),
                HostToolCheck(
                    id: "nexus-daemon",
                    name: "Nexus Daemon",
                    isInstalled: hasNexusDaemon,
                    details: resolvedDetail(name: "nexus-daemon", fallbackInstalled: "nexus-daemon available") ?? "Installed and updated via Nexus installer/updater",
                    installFormula: nil
                ),
            ],
            nestedVirtualizationSupported: nestedSupported,
            hasHomebrew: hasBrew
        )
    }

    public static func installMissingTools(
        snapshot: HostToolSnapshot,
        onProgress: (@Sendable (HostSetupProgress) -> Void)? = nil
    ) async throws -> String {
        guard snapshot.hasHomebrew else {
            throw HostToolsSetupError.homebrewMissing
        }

        let formulas = snapshot.checks
            .filter { !$0.isInstalled }
            .compactMap(\.installFormula)
        if formulas.isEmpty {
            return "All managed host tools are already installed."
        }

        var logs: [String] = []
        let total = formulas.count
        for (idx, formula) in formulas.enumerated() {
            onProgress?(HostSetupProgress(
                step: idx + 1,
                totalSteps: total,
                title: "Installing \(formula)"
            ))
            let result = await runShell("brew install \(shellQuote(formula))")
            if result.exitCode != 0 {
                throw HostToolsSetupError.installFailed(result.output)
            }
            if !result.output.isEmpty {
                logs.append(result.output)
            }
        }
        if logs.isEmpty {
            return "Installed: \(formulas.joined(separator: ", "))"
        }
        return logs.joined(separator: "\n\n")
    }

    public static func provisionFirecrackerRuntime(
        projectRoot: String,
        useAdministratorPrivileges: Bool,
        onProgress: (@Sendable (HostSetupProgress) -> Void)? = nil
    ) async throws -> String {
        let escapedRoot = shellQuote(projectRoot)
        let command = "nexus init --force \(escapedRoot)"
        let result: CommandResult
        onProgress?(HostSetupProgress(
            step: 1,
            totalSteps: 1,
            title: "Running runtime provisioning",
            detail: command
        ))

        if useAdministratorPrivileges {
            result = await runPrivileged(command)
        } else {
            result = await runShell(command)
        }

        if result.exitCode != 0 {
            throw HostToolsSetupError.provisioningFailed(result.output)
        }
        return result.output.isEmpty
            ? "Firecracker runtime provisioning completed."
            : result.output
    }

    public static func installOrUpdateDaemon(
        onProgress: (@Sendable (HostSetupProgress) -> Void)? = nil
    ) async throws -> String {
        var logs: [String] = []
        let hasNexus = await commandExists("nexus")
        let nexusPath = ToolBinaryResolver.resolvePreferred("nexus")
        let daemonPath = ToolBinaryResolver.resolvePreferred("nexus-daemon")
        let command: String
        let result: CommandResult

        if let daemonPath {
            onProgress?(HostSetupProgress(step: 1, totalSteps: 3, title: "Using available daemon binary", detail: daemonPath))
        }

        if hasNexus, let nexusPath {
            onProgress?(HostSetupProgress(step: 1, totalSteps: 3, title: "Updating existing Nexus install"))
            command = """
\(shellQuote(nexusPath)) update --force
if ! command -v nexus-daemon >/dev/null 2>&1; then
  echo "nexus-daemon was not found in PATH after install/update." >&2
  exit 1
fi
"""
            result = await runShell(command)
        } else {
            // Fresh machines often require privileged writes to /usr/local/bin.
            // Use macOS admin-auth path instead of a non-interactive shell.
            onProgress?(HostSetupProgress(step: 1, totalSteps: 3, title: "Installing Nexus CLI + daemon (admin prompt may appear)"))
            command = """
curl -fsSL https://raw.githubusercontent.com/inizio/nexus/main/install.sh | bash
if ! command -v nexus-daemon >/dev/null 2>&1; then
  echo "nexus-daemon was not found in PATH after install/update." >&2
  exit 1
fi
"""
            result = await runPrivileged(command)
        }
        if !result.output.isEmpty {
            logs.append(result.output)
        }

        if result.exitCode != 0 {
            throw HostToolsSetupError.daemonInstallFailed(result.output)
        }

        onProgress?(HostSetupProgress(step: 2, totalSteps: 3, title: "Verifying installed binaries"))
        let verify = await runShell("""
command -v nexus >/dev/null 2>&1 && command -v nexus-daemon >/dev/null 2>&1
""")
        if verify.exitCode != 0 {
            throw HostToolsSetupError.daemonInstallFailed("Install completed but nexus/nexus-daemon was not found on PATH.")
        }

        onProgress?(HostSetupProgress(step: 3, totalSteps: 3, title: "Collecting version info"))
        let versions = await runShell("""
{ nexus --version 2>/dev/null || true; nexus-daemon --version 2>/dev/null || true; } | sed '/^$/d'
""")
        if !versions.output.isEmpty {
            logs.append(versions.output)
        }

        if logs.isEmpty {
            return "Nexus daemon install/update completed."
        }
        return logs.joined(separator: "\n\n")
    }

    private static func checkNestedVirtualizationSupport() async -> Bool {
        let result = await runShell("sysctl -n kern.hv_support")
        if result.exitCode != 0 {
            return false
        }
        return result.output.trimmingCharacters(in: .whitespacesAndNewlines) == "1"
    }

    private static func commandExists(_ command: String) async -> Bool {
        if ToolBinaryResolver.resolvePreferred(command) != nil {
            return true
        }
        let result = await runShell("command -v \(command)")
        return result.exitCode == 0
    }

    private static func resolvedDetail(name: String, fallbackInstalled: String) -> String? {
        guard let path = ToolBinaryResolver.resolvePreferred(name) else { return nil }
        if path.contains("/Resources/tools/") || path.hasSuffix("/Resources/\(name)") {
            return "\(name) using bundled fallback"
        }
        return "\(fallbackInstalled) (\(path))"
    }

    private static func shellQuote(_ value: String) -> String {
        if value.isEmpty { return "''" }
        let escaped = value.replacingOccurrences(of: "'", with: "'\"'\"'")
        return "'\(escaped)'"
    }

    private static func runPrivileged(_ command: String) async -> CommandResult {
        let escaped = command
            .replacingOccurrences(of: "\\", with: "\\\\")
            .replacingOccurrences(of: "\"", with: "\\\"")
        let script = "do shell script \"\(escaped)\" with administrator privileges"
        return await runProcess(
            executable: "/usr/bin/osascript",
            arguments: ["-e", script]
        )
    }

    private static func runShell(_ command: String) async -> CommandResult {
        await runProcess(
            executable: "/bin/zsh",
            arguments: ["-lc", command],
            includeCommonHomebrewPath: true
        )
    }

    private static func runProcess(
        executable: String,
        arguments: [String],
        includeCommonHomebrewPath: Bool = false,
        timeoutSeconds: TimeInterval = 180
    ) async -> CommandResult {
        await withCheckedContinuation { continuation in
            DispatchQueue.global(qos: .userInitiated).async {
                let process = Process()
                process.executableURL = URL(fileURLWithPath: executable)
                process.arguments = arguments

                if includeCommonHomebrewPath {
                    var env = ProcessInfo.processInfo.environment
                    let existingPath = env["PATH"] ?? "/usr/bin:/bin:/usr/sbin:/sbin"
                    let extras = ToolBinaryResolver.preferredPathEntries() + ["/opt/homebrew/bin", "/usr/local/bin"]
                    env["PATH"] = (extras + [existingPath]).joined(separator: ":")
                    process.environment = env
                }

                let output = Pipe()
                process.standardOutput = output
                process.standardError = output

                do {
                    try process.run()
                } catch {
                    continuation.resume(returning: CommandResult(exitCode: 1, output: error.localizedDescription))
                    return
                }

                let deadline = Date().addingTimeInterval(timeoutSeconds)
                var didTimeout = false
                while process.isRunning {
                    if Date() >= deadline {
                        didTimeout = true
                        process.terminate()
                        break
                    }
                    Thread.sleep(forTimeInterval: 0.1)
                }
                if process.isRunning {
                    process.waitUntilExit()
                }

                if didTimeout {
                    continuation.resume(returning: CommandResult(
                        exitCode: 124,
                        output: "Command timed out after \(Int(timeoutSeconds))s. Check network/auth prompts and retry."
                    ))
                    return
                }

                let data = output.fileHandleForReading.readDataToEndOfFile()
                let text = String(data: data, encoding: .utf8)?
                    .trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
                continuation.resume(returning: CommandResult(
                    exitCode: Int(process.terminationStatus),
                    output: text
                ))
            }
        }
    }
}

private struct CommandResult {
    let exitCode: Int
    let output: String
}
