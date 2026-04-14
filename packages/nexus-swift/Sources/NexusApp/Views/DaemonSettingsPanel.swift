import NexusCore
import SwiftUI
import Foundation

/// Popover shown when the user clicks the connection status pill.
/// Mirrors Docker Desktop's engine status panel.
struct DaemonSettingsPanel: View {
    @EnvironmentObject var appState: AppState
    @State private var toolSnapshot: HostToolSnapshot?
    @State private var toolingBusy = false
    @State private var toolingMessage: String?
    @State private var toolingError: String?
    @State private var useAdminPrivileges = false
    @State private var progressTitle: String?
    @State private var progressValue: Double?
    @State private var toolingLogs: [String] = []

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            header
            Divider().opacity(0.4)
            controls
        }
        .frame(width: 320)
        .background(Theme.bgContent)
        .task {
            await refreshToolingSnapshot()
        }
    }

    // MARK: - Header

    private var header: some View {
        VStack(alignment: .leading, spacing: 4) {
            HStack(spacing: 6) {
                Circle()
                    .fill(statusColor)
                    .frame(width: 8)
                Text(statusTitle)
                    .font(.system(size: 13, weight: .semibold))
                    .foregroundColor(Theme.label)
                Spacer()
            }
            if let sub = statusSubtitle {
                Text(sub)
                    .font(.system(size: 11, design: .monospaced))
                    .foregroundColor(Theme.labelTertiary)
                    .lineLimit(3)
                    .fixedSize(horizontal: false, vertical: true)
            }
        }
        .padding(12)
    }

    // MARK: - Controls

    @ViewBuilder
    private var controls: some View {
        VStack(alignment: .leading, spacing: 0) {
            if appState.isBusy || toolingBusy {
                VStack(alignment: .leading, spacing: 6) {
                    HStack(spacing: 8) {
                        ProgressView(value: progressValue)
                            .scaleEffect(0.8, anchor: .leading)
                            .frame(maxWidth: .infinity)
                    }
                    Text(progressTitle ?? "Working…")
                        .font(.system(size: 11))
                        .foregroundColor(Theme.labelTertiary)
                }
                .padding(.horizontal, 12)
                .padding(.vertical, 10)
            }
            daemonControls
            Divider().opacity(0.35)
            toolingControls
        }
    }

    @ViewBuilder
    private var daemonControls: some View {
        switch appState.daemonStatus {
        case .outdated:
            panelButton("Update Daemon", icon: "arrow.triangle.2.circlepath", accent: true) {
                Task { await appState.restartDaemon() }
            }
        case .offline:
            panelButton("Start Daemon", icon: "play.fill", accent: true) {
                Task { await appState.restartDaemon() }
            }
        case .running:
            panelButton("Restart Daemon", icon: "arrow.clockwise") {
                Task { await appState.restartDaemon() }
            }
            Divider().opacity(0.35)
            panelButton("Stop Daemon", icon: "stop.fill", destructive: true) {
                Task { await appState.stopDaemon() }
            }
        case .unknown:
            EmptyView()
        }
    }

    private var toolingControls: some View {
        VStack(alignment: .leading, spacing: 6) {
            Text("Host Tools")
                .font(.system(size: 11, weight: .semibold))
                .foregroundColor(Theme.labelSecondary)
                .padding(.horizontal, 12)
                .padding(.top, 8)

            if let snapshot = toolSnapshot {
                ForEach(snapshot.checks) { check in
                    HStack(spacing: 8) {
                        Image(systemName: check.isInstalled ? "checkmark.circle.fill" : "exclamationmark.circle")
                            .font(.system(size: 10))
                            .foregroundColor(check.isInstalled ? Theme.green : Theme.orange)
                        Text(check.name)
                            .font(.system(size: 11))
                            .foregroundColor(Theme.label)
                        Spacer()
                        Text(check.isInstalled ? "Installed" : "Missing")
                            .font(.system(size: 10))
                            .foregroundColor(Theme.labelTertiary)
                    }
                    .padding(.horizontal, 12)
                    .padding(.vertical, 2)
                }

                HStack(spacing: 6) {
                    Image(systemName: snapshot.nestedVirtualizationSupported ? "cpu.fill" : "cpu")
                        .font(.system(size: 10))
                        .foregroundColor(snapshot.nestedVirtualizationSupported ? Theme.green : Theme.orange)
                    Text(snapshot.nestedVirtualizationSupported
                        ? "Nested virtualization supported"
                        : "Nested virtualization unavailable (will fallback)")
                        .font(.system(size: 10))
                        .foregroundColor(Theme.labelTertiary)
                }
                .padding(.horizontal, 12)
                .padding(.top, 2)
            } else {
                Text("Tool status unavailable")
                    .font(.system(size: 10))
                    .foregroundColor(Theme.labelTertiary)
                    .padding(.horizontal, 12)
                    .padding(.vertical, 2)
            }

            panelButton("Refresh Tool Checks", icon: "arrow.clockwise") {
                Task { await refreshToolingSnapshot() }
            }

            panelButton("Install Missing Tools", icon: "tray.and.arrow.down") {
                Task { await installMissingTools() }
            }

            panelButton("Install/Update Daemon", icon: "square.and.arrow.down") {
                Task { await installOrUpdateDaemon() }
            }

            panelButton("Provision Firecracker Runtime", icon: "wrench.and.screwdriver") {
                Task { await provisionRuntime() }
            }

            Toggle(isOn: $useAdminPrivileges) {
                Text("Use admin privileges for provisioning")
                    .font(.system(size: 10))
                    .foregroundColor(Theme.labelTertiary)
            }
            .toggleStyle(.checkbox)
            .padding(.horizontal, 12)
            .padding(.bottom, 4)

            if let message = toolingMessage, !message.isEmpty {
                Text(message)
                    .font(.system(size: 10, design: .monospaced))
                    .foregroundColor(Theme.labelTertiary)
                    .lineLimit(3)
                    .padding(.horizontal, 12)
                    .padding(.bottom, 6)
            }
            if !toolingLogs.isEmpty {
                VStack(alignment: .leading, spacing: 4) {
                    Text("Install Log")
                        .font(.system(size: 10, weight: .semibold))
                        .foregroundColor(Theme.labelSecondary)
                    ScrollView {
                        Text(toolingLogs.joined(separator: "\n"))
                            .font(.system(size: 10, design: .monospaced))
                            .foregroundColor(Theme.labelTertiary)
                            .frame(maxWidth: .infinity, alignment: .leading)
                            .textSelection(.enabled)
                    }
                    .frame(height: 120)
                }
                .padding(.horizontal, 12)
                .padding(.bottom, 8)
            }
            if let error = toolingError, !error.isEmpty {
                Text(error)
                    .font(.system(size: 10, design: .monospaced))
                    .foregroundColor(Theme.red)
                    .lineLimit(4)
                    .padding(.horizontal, 12)
                    .padding(.bottom, 6)
            }
        }
    }

    // MARK: - Computed helpers

    private var statusColor: Color {
        switch appState.daemonStatus {
        case .running:              return Theme.green
        case .outdated:             return Theme.orange
        case .offline, .unknown:    return Color.gray
        }
    }

    private var statusTitle: String {
        switch appState.daemonStatus {
        case .running:  return "Daemon Running"
        case .outdated: return "Daemon Outdated"
        case .offline:  return "Daemon Offline"
        case .unknown:  return "Connecting…"
        }
    }

    private var statusSubtitle: String? {
        switch appState.daemonStatus {
        case .running(let info):
            let devNote = info.version == "0.0.0-dev" ? "  (dev build)" : ""
            return "v\(info.version)  ·  protocol \(info.protocolVersion)\(devNote)"
        case .outdated(let info):
            return "Running protocol v\(info.protocolVersion), requires v\(DaemonInfo.requiredProtocol). Install/Update Daemon, then restart."
        case .offline:
            return "No daemon found on port 63987."
        case .unknown:
            return nil
        }
    }

    @ViewBuilder
    private func panelButton(
        _ title: String,
        icon: String,
        accent: Bool = false,
        destructive: Bool = false,
        action: @escaping () -> Void
    ) -> some View {
        Button(action: action) {
            HStack(spacing: 8) {
                Image(systemName: icon)
                    .font(.system(size: 12))
                    .frame(width: 16)
                Text(title)
                    .font(.system(size: 12))
                Spacer()
            }
            .padding(.horizontal, 12)
            .padding(.vertical, 9)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .foregroundColor(
            destructive ? Theme.red :
            accent      ? Theme.accent :
                          Theme.label
        )
        .background(Color.clear)
    }

    private func refreshToolingSnapshot() async {
        toolingError = nil
        let snapshot = await HostToolsSetup.inspect()
        toolSnapshot = snapshot
    }

    private func installMissingTools() async {
        toolingBusy = true
        toolingMessage = nil
        toolingError = nil
        toolingLogs = []
        progressTitle = "Preparing tool installation…"
        progressValue = nil
        defer { toolingBusy = false }

        let snapshot: HostToolSnapshot
        if let existing = toolSnapshot {
            snapshot = existing
        } else {
            snapshot = await HostToolsSetup.inspect()
        }
        toolSnapshot = snapshot
        do {
            toolingMessage = try await HostToolsSetup.installMissingTools(snapshot: snapshot) { update in
                DispatchQueue.main.async { applyProgress(update) }
            }
            await refreshToolingSnapshot()
            progressTitle = "Completed"
            progressValue = 1.0
        } catch {
            toolingError = error.localizedDescription
        }
    }

    private func provisionRuntime() async {
        toolingBusy = true
        toolingMessage = nil
        toolingError = nil
        toolingLogs = []
        progressTitle = "Preparing runtime provisioning…"
        progressValue = nil
        defer { toolingBusy = false }

        let projectRoot = provisioningProjectRoot()
        do {
            toolingMessage = try await HostToolsSetup.provisionFirecrackerRuntime(
                projectRoot: projectRoot,
                useAdministratorPrivileges: useAdminPrivileges
            ) { update in
                DispatchQueue.main.async { applyProgress(update) }
            }
            await refreshToolingSnapshot()
            progressTitle = "Completed"
            progressValue = 1.0
        } catch {
            toolingError = error.localizedDescription
        }
    }

    private func installOrUpdateDaemon() async {
        toolingBusy = true
        toolingMessage = nil
        toolingError = nil
        toolingLogs = []
        progressTitle = "Preparing daemon install…"
        progressValue = nil
        defer { toolingBusy = false }

        do {
            toolingMessage = try await HostToolsSetup.installOrUpdateDaemon { update in
                DispatchQueue.main.async { applyProgress(update) }
            }
            await appState.restartDaemon()
            await refreshToolingSnapshot()
            progressTitle = "Completed"
            progressValue = 1.0
        } catch {
            toolingError = error.localizedDescription
        }
    }

    private func applyProgress(_ update: HostSetupProgress) {
        progressTitle = "[\(update.step)/\(update.totalSteps)] \(update.title)"
        if update.totalSteps > 0 {
            progressValue = Double(update.step) / Double(update.totalSteps)
        } else {
            progressValue = nil
        }
        if let detail = update.detail, !detail.isEmpty {
            toolingLogs.append("• \(detail)")
        } else {
            toolingLogs.append("• \(update.title)")
        }
    }

    private func provisioningProjectRoot() -> String {
        if let root = appState.selectedWorkspace?.rootPath,
           root.hasPrefix("/"),
           !root.hasPrefix("/workspace"),
           !root.hasPrefix("/nexus/ws/") {
            return root
        }
        return FileManager.default.currentDirectoryPath
    }
}
