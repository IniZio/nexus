import NexusCore
import SwiftUI

// MARK: - Detail root

struct WorkspaceDetailView: View {
    let workspace: Workspace
    @EnvironmentObject var appState: AppState

    var body: some View {
        VStack(spacing: 0) {
            SessionInfoStrip(workspace: workspace)
            Divider().overlay(Theme.separator)

            TerminalCard(workspace: workspace)
                .frame(maxWidth: .infinity, maxHeight: .infinity)
                .background(Theme.bgApp)
        }
        .background(Theme.bgApp)
        .accessibilityIdentifier("workspace_detail")
        .accessibilityLabel("Workspace \(workspace.name)")
        .toolbar {
            ToolbarItem(placement: .navigation) {
                WorkspaceBreadcrumb(workspace: workspace)
            }
            ToolbarItemGroup(placement: .primaryAction) {
                StatusPill(status: workspace.status)
                Divider().frame(height: 14).opacity(0.35)
                WorkspaceActionMenu(workspace: workspace)
                ToolbarBtn(icon: "ellipsis") {}
                Divider().frame(height: 14).opacity(0.35)
                ToolbarBtn(
                    icon: "sidebar.trailing",
                    active: appState.showInspector
                ) {
                    appState.showInspector.toggle()
                }
            }
        }
    }
}

// MARK: - Workspace action menu (toolbar)

private struct WorkspaceActionMenu: View {
    let workspace: Workspace
    @EnvironmentObject var appState: AppState

    private var primaryIcon: String {
        switch workspace.state {
        case .running, .restored: "pause.fill"
        case .paused:             "play.fill"
        case .stopped, .created:  "play.fill"
        }
    }

    var body: some View {
        Menu {
            switch workspace.state {
            case .stopped, .created:
                Button { Task { await appState.start(workspace) } } label: {
                    Label("Start", systemImage: "play.fill")
                }
            case .running, .restored:
                Button { Task { await appState.pause(workspace) } } label: {
                    Label("Pause", systemImage: "pause.fill")
                }
                Button { Task { await appState.stop(workspace) } } label: {
                    Label("Stop", systemImage: "stop.fill")
                }
            case .paused:
                Button { Task { await appState.resume(workspace) } } label: {
                    Label("Resume", systemImage: "play.fill")
                }
                Button { Task { await appState.stop(workspace) } } label: {
                    Label("Stop", systemImage: "stop.fill")
                }
            }

            Divider()

            Button(role: .destructive) {
                Task { await appState.remove(workspace) }
            } label: {
                Label("Remove Workspace…", systemImage: "trash")
            }
        } label: {
            Image(systemName: primaryIcon)
                .font(.system(size: 12))
                .frame(width: 28, height: 28)
        }
        .menuStyle(.borderlessButton)
        .fixedSize()
    }
}

// MARK: - Breadcrumb (toolbar)

private struct WorkspaceBreadcrumb: View {
    let workspace: Workspace
    var body: some View {
        HStack(spacing: 6) {
            Text(workspace.name)
                .font(.system(size: 13, weight: .semibold))
                .foregroundColor(.primary)
            Image(systemName: "chevron.right")
                .font(.system(size: 10, weight: .medium))
                .foregroundColor(Theme.labelTertiary)
            Text(workspace.branch)
                .font(.system(size: 13))
                .foregroundColor(Theme.labelSecondary)
        }
    }
}

// MARK: - Session info strip

private struct SessionInfoStrip: View {
    let workspace: Workspace
    @EnvironmentObject var appState: AppState
    @State private var resolvedPath: String = ""

    var body: some View {
        HStack(spacing: 20) {
            HStack(spacing: 4) {
                Image(systemName: "arrow.triangle.branch")
                    .font(.system(size: 10))
                    .foregroundColor(Theme.labelTertiary)
                Text(workspace.branch)
                    .font(.system(size: 11, design: .monospaced))
                    .foregroundColor(Theme.labelSecondary)
            }

            // Resolved workspace path from workspace.info
            if !resolvedPath.isEmpty {
                Divider().frame(height: 12).opacity(0.5)
                HStack(spacing: 4) {
                    Image(systemName: "folder")
                        .font(.system(size: 10))
                        .foregroundColor(Theme.labelTertiary)
                    Text(resolvedPath)
                        .font(.system(size: 11, design: .monospaced))
                        .foregroundColor(Theme.labelSecondary)
                        .lineLimit(1)
                        .truncationMode(.middle)
                }
            }

            if !workspace.ports.isEmpty {
                Divider().frame(height: 12).opacity(0.5)
                HStack(spacing: 4) {
                    Image(systemName: "arrow.left.arrow.right")
                        .font(.system(size: 10))
                        .foregroundColor(Theme.labelTertiary)
                    ForEach(workspace.ports) { port in
                        Text(":\(port.port)")
                            .font(.system(size: 11, design: .monospaced))
                            .foregroundColor(Theme.green)
                    }
                }
            }

            Spacer()
        }
        .padding(.horizontal, 16)
        .frame(height: 34)
        .background(Theme.bgContent)
        .task(id: workspace.id) {
            if let client = appState.client as? WebSocketDaemonClient,
               let info = try? await client.workspaceInfo(id: workspace.id) {
                resolvedPath = info.workspacePath
                    .replacingOccurrences(of: FileManager.default.homeDirectoryForCurrentUser.path, with: "~")
            }
        }
    }
}

// MARK: - Terminal inset card

/// The terminal lives inside a dark rounded card within the warm off-white background,
/// matching how Conductor embeds a terminal within a light window.
private struct TerminalCard: View {
    let workspace: Workspace

    var body: some View {
        // No ScrollView — terminal has its own built-in scrollback.
        // The NSViewRepresentable must fill its parent directly or it collapses to zero height.
        TerminalView(workspace: workspace)
            .frame(maxWidth: .infinity, maxHeight: .infinity)
            .clipShape(RoundedRectangle(cornerRadius: 8))
            .overlay(
                RoundedRectangle(cornerRadius: 8)
                    .stroke(Color.white.opacity(0.06), lineWidth: 1)
            )
            .padding(12)
            .background(Theme.bgApp)
    }
}

// MARK: - Status pill

struct StatusPill: View {
    let status: WorkspaceStatus

    var body: some View {
        HStack(spacing: 5) {
            Circle().fill(Theme.statusColor(status)).frame(width: 6)
            Text(status.displayName)
                .font(.system(size: 11.5, weight: .medium))
                .foregroundColor(Theme.labelSecondary)
        }
        .padding(.horizontal, 8)
        .padding(.vertical, 4)
        .background(RoundedRectangle(cornerRadius: 5).fill(Color.black.opacity(0.05)))
    }
}

// MARK: - Toolbar icon button

struct ToolbarBtn: View {
    let icon: String
    var active: Bool = false
    let action: () -> Void
    @State private var hover = false
    var body: some View {
        Button(action: action) {
            Image(systemName: icon)
                .font(.system(size: 13))
                .foregroundColor(
                    active
                        ? (hover ? Theme.accent : Theme.labelSecondary)
                        : (hover ? Theme.label : Theme.labelSecondary)
                )
                .frame(width: 28, height: 28)
                .background(RoundedRectangle(cornerRadius: 5)
                    .fill(active
                          ? Theme.accent.opacity(0.12)
                          : hover ? Color.black.opacity(0.06) : .clear))
        }
        .buttonStyle(.plain)
        .onHover { hover = $0 }
    }
}

typealias IconButton = ToolbarBtn
