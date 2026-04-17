import Foundation

/// Builds a base64-encoded gzipped tar of credential files from the user's home directory.
/// Mirrors `credsbundle.BuildFromHome` in the Go daemon.
enum ConfigBundle {
    private static let maxBundleSizeBytes: Int = 8 * 1024 * 1024

    /// Relative paths (from $HOME) of files/directories to include.
    private static let credPaths: [String] = [
        // opencode
        ".local/share/opencode/auth.json",
        ".local/share/opencode/mcp-auth.json",
        ".config/opencode/opencode.json",
        ".config/opencode/ocx.jsonc",
        ".config/opencode/dcp.jsonc",
        ".config/opencode/opencode-mem.jsonc",
        ".config/opencode/skills",
        ".config/opencode/plugin",
        ".config/opencode/plugins",
        ".config/opencode/profiles",
        // claude
        ".claude/.credentials.json",
        ".claude.json",
        // github copilot
        ".config/github-copilot/hosts.json",
        ".config/github-copilot/apps.json",
    ]

    /// Returns a base64-encoded gzipped tar of all credential files found under `home`,
    /// or nil if nothing was found or the bundle exceeds the size cap.
    static func build(home: String = FileManager.default.homeDirectoryForCurrentUser.path) -> String? {
        let fm = FileManager.default

        // Collect only paths that exist
        var includedPaths: [String] = []
        for rel in credPaths {
            let full = (home as NSString).appendingPathComponent(rel)
            if fm.fileExists(atPath: full) {
                includedPaths.append(rel)
            }
        }
        guard !includedPaths.isEmpty else { return nil }

        let args = ["-czf", "-"] + includedPaths

        let proc = Process()
        proc.executableURL = URL(fileURLWithPath: "/usr/bin/tar")
        proc.currentDirectoryURL = URL(fileURLWithPath: home)
        proc.arguments = args

        let outPipe = Pipe()
        proc.standardOutput = outPipe
        proc.standardError = Pipe() // discard stderr

        do {
            try proc.run()
        } catch {
            return nil
        }

        let data = outPipe.fileHandleForReading.readDataToEndOfFile()
        proc.waitUntilExit()

        guard proc.terminationStatus == 0, !data.isEmpty else { return nil }
        guard data.count <= maxBundleSizeBytes else { return nil }

        return data.base64EncodedString()
    }
}
