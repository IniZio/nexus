import NexusCore
import SwiftUI
import Foundation

/// Popover shown when the user clicks the connection status pill.
/// Mirrors Docker Desktop's engine status panel.
struct DaemonSettingsPanel: View {
    @EnvironmentObject var appState: AppState
    @State private var defaultMemoryMiB = 1024
    @State private var defaultVCPUs = 1
    @State private var maxMemoryMiB = 4096
    @State private var maxVCPUs = 4
    @State private var sandboxSettingsBusy = false
    @State private var sandboxSettingsMessage: String?
    @State private var sandboxSettingsError: String?
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
            await refreshSandboxSettings()
        }
    }

    // MARK: - Header

    private var header: some View {
        let variantInfo = DaemonLauncher.runtimeVariantInfo()
        return VStack(alignment: .leading, spacing: 4) {
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
            VStack(alignment: .leading, spacing: 2) {
                Text("mode: \(variantInfo.mode)")
                Text("port: \(variantInfo.preferredPort)")
                Text("binary: \(variantInfo.binarySource.rawValue)")
                Text(variantInfo.binaryPath)
                    .lineLimit(2)
            }
            .font(.system(size: 10, design: .monospaced))
            .foregroundColor(Theme.labelTertiary)
            .padding(.top, 4)
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
            sandboxResourceControls
            Divider().opacity(0.35)
            toolingControls
            Divider().opacity(0.35)
            profilesSection
        }
    }

    // MARK: - Profiles section

    @ViewBuilder
    private var profilesSection: some View {
        let activeProfile = DaemonProfileStore().defaultProfile()
        let isRemoteMode = activeProfile?.mode == .remote
        VStack(alignment: .leading, spacing: 0) {
            if isRemoteMode {
                HStack(spacing: 6) {
                    Image(systemName: "network")
                        .font(.system(size: 11))
                        .foregroundColor(.blue)
                    Text("Remote mode — daemon lifecycle managed externally")
                        .font(.caption)
                        .foregroundColor(.secondary)
                }
                .padding(.horizontal, 12)
                .padding(.vertical, 8)
                Divider().opacity(0.25)
            }
            RemoteProfileSettingsView()
                .padding(.horizontal, 12)
                .padding(.vertical, 10)
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

    private var sandboxResourceControls: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text("Sandbox Resources")
                .font(.system(size: 11, weight: .semibold))
                .foregroundColor(Theme.labelSecondary)
                .padding(.horizontal, 12)
                .padding(.top, 8)

            Stepper(value: $defaultMemoryMiB, in: 256...maxMemoryMiB, step: 256) {
                HStack {
                    Text("Default RAM")
                        .font(.system(size: 11))
                        .foregroundColor(Theme.label)
                    Spacer()
                    Text("\(defaultMemoryMiB) MiB")
                        .font(.system(size: 10, design: .monospaced))
                        .foregroundColor(Theme.labelTertiary)
                }
            }
            .padding(.horizontal, 12)

            Stepper(value: $defaultVCPUs, in: 1...maxVCPUs, step: 1) {
                HStack {
                    Text("Default vCPUs")
                        .font(.system(size: 11))
                        .foregroundColor(Theme.label)
                    Spacer()
                    Text("\(defaultVCPUs)")
                        .font(.system(size: 10, design: .monospaced))
                        .foregroundColor(Theme.labelTertiary)
                }
            }
            .padding(.horizontal, 12)

            Stepper(value: $maxMemoryMiB, in: 256...65536, step: 256) {
                HStack {
                    Text("Maximum RAM")
                        .font(.system(size: 11))
                        .foregroundColor(Theme.label)
                    Spacer()
                    Text("\(maxMemoryMiB) MiB")
                        .font(.system(size: 10, design: .monospaced))
                        .foregroundColor(Theme.labelTertiary)
                }
            }
            .padding(.horizontal, 12)

            Stepper(value: $maxVCPUs, in: 1...64, step: 1) {
                HStack {
                    Text("Maximum vCPUs")
                        .font(.system(size: 11))
                        .foregroundColor(Theme.label)
                    Spacer()
                    Text("\(maxVCPUs)")
                        .font(.system(size: 10, design: .monospaced))
                        .foregroundColor(Theme.labelTertiary)
                }
            }
            .padding(.horizontal, 12)

            Text("Applies to new or restarted sandboxes. Running sandboxes keep current limits until restart.")
                .font(.system(size: 10))
                .foregroundColor(Theme.labelTertiary)
                .padding(.horizontal, 12)

            if sandboxSettingsBusy {
                Text("Saving daemon settings…")
                    .font(.system(size: 10))
                    .foregroundColor(Theme.labelTertiary)
                    .padding(.horizontal, 12)
            }
            if let message = sandboxSettingsMessage, !message.isEmpty {
                Text(message)
                    .font(.system(size: 10))
                    .foregroundColor(Theme.labelTertiary)
                    .padding(.horizontal, 12)
            }
            if let error = sandboxSettingsError, !error.isEmpty {
                Text(error)
                    .font(.system(size: 10, design: .monospaced))
                    .foregroundColor(Theme.red)
                    .padding(.horizontal, 12)
            }

            panelButton("Apply Limits (Restart Daemon)", icon: "arrow.clockwise") {
                Task { await applySandboxSettingsAndRestartDaemon() }
            }
        }
        .onChange(of: maxMemoryMiB) { _, newValue in
            if newValue < 256 { maxMemoryMiB = 256 }
            if defaultMemoryMiB > maxMemoryMiB { defaultMemoryMiB = maxMemoryMiB }
        }
        .onChange(of: maxVCPUs) { _, newValue in
            if newValue < 1 { maxVCPUs = 1 }
            if defaultVCPUs > maxVCPUs { defaultVCPUs = maxVCPUs }
        }
        .onChange(of: defaultMemoryMiB) { _, newValue in
            if newValue < 256 { defaultMemoryMiB = 256 }
            if defaultMemoryMiB > maxMemoryMiB { defaultMemoryMiB = maxMemoryMiB }
        }
        .onChange(of: defaultVCPUs) { _, newValue in
            if newValue < 1 { defaultVCPUs = 1 }
            if defaultVCPUs > maxVCPUs { defaultVCPUs = maxVCPUs }
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
            return "No daemon found on port \(DaemonLauncher.preferredPort())."
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

    private func refreshSandboxSettings() async {
        sandboxSettingsError = nil
        do {
            let settings = try await appState.client.getDaemonSandboxResourceSettings()
            defaultMemoryMiB = settings.defaultMemoryMiB
            defaultVCPUs = settings.defaultVCPUs
            maxMemoryMiB = settings.maxMemoryMiB
            maxVCPUs = settings.maxVCPUs
            sandboxSettingsMessage = nil
        } catch {
            sandboxSettingsError = error.localizedDescription
        }
    }

    private func applySandboxSettingsAndRestartDaemon() async {
        sandboxSettingsBusy = true
        sandboxSettingsError = nil
        sandboxSettingsMessage = nil
        defer { sandboxSettingsBusy = false }
        do {
            _ = try await appState.client.updateDaemonSandboxResourceSettings(
                SandboxResourceSettings(
                    defaultMemoryMiB: defaultMemoryMiB,
                    defaultVCPUs: defaultVCPUs,
                    maxMemoryMiB: maxMemoryMiB,
                    maxVCPUs: maxVCPUs
                )
            )
            sandboxSettingsMessage = "Settings saved. Restarting daemon…"
            await appState.restartDaemon()
            await refreshSandboxSettings()
            sandboxSettingsMessage = "Settings applied."
        } catch {
            sandboxSettingsError = error.localizedDescription
        }
    }
}
