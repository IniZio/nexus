import NexusCore
import SwiftUI

/// Popover shown when the user clicks the connection status pill.
/// Mirrors Docker Desktop's engine status panel.
struct DaemonSettingsPanel: View {
    @EnvironmentObject var appState: AppState

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            header
            Divider().opacity(0.4)
            controls
        }
        .frame(width: 280)
        .background(Theme.bgContent)
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
        if appState.isBusy {
            HStack(spacing: 8) {
                ProgressView().scaleEffect(0.7)
                Text("Working…")
                    .font(.system(size: 12))
                    .foregroundColor(Theme.labelTertiary)
            }
            .padding(.horizontal, 12)
            .padding(.vertical, 10)
        } else {
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
            return "Running protocol v\(info.protocolVersion), requires v\(DaemonInfo.requiredProtocol). Click Update to restart with the bundled binary."
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
}
