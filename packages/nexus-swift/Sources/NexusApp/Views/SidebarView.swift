import NexusCore
import SwiftUI

// MARK: - Root

struct SidebarView: View {
    @EnvironmentObject var appState: AppState

    var body: some View {
        VStack(spacing: 0) {
            SidebarHeader()
            Divider().overlay(Theme.separator).opacity(0.6)

            ScrollView(.vertical, showsIndicators: false) {
                VStack(alignment: .leading, spacing: 0) {
                    ForEach(appState.repos) { repo in
                        RepoSection(repo: repo)
                    }
                }
                .padding(.top, 6)
                .padding(.bottom, 10)
            }

            Divider().overlay(Theme.separator).opacity(0.6)
            SidebarFooter()
        }
        .background(SidebarMaterial().ignoresSafeArea())
    }
}

// MARK: - Header

private struct SidebarHeader: View {
    @EnvironmentObject var appState: AppState

    var body: some View {
        HStack(spacing: 0) {
            Text("Workspaces")
                .font(.system(size: 12))
                .foregroundColor(Theme.labelSecondary)
                .padding(.leading, 16)

            Spacer()

            // Collapse sidebar — icon tints accent when sidebar is "locked open"
            SidebarHeaderBtn(
                icon: "sidebar.leading",
                active: appState.sidebarVisible
            ) {
                withAnimation(.easeInOut(duration: 0.18)) {
                    appState.sidebarVisible.toggle()
                }
            }

            SidebarHeaderBtn(icon: "plus") { appState.showNewWorkspace = true }
                .padding(.trailing, 6)
                .accessibilityIdentifier("new_workspace_button")
        }
        .frame(height: 36)
    }
}

private struct SidebarHeaderBtn: View {
    let icon: String
    var active: Bool = false
    let action: () -> Void
    @State private var hover = false

    var body: some View {
        Button(action: action) {
            Image(systemName: icon)
                .font(.system(size: 11))
                .foregroundColor(
                    active
                        ? (hover ? Theme.accent : Theme.labelSecondary)
                        : (hover ? Theme.labelSecondary : Theme.labelTertiary)
                )
                .frame(width: 28, height: 28)
                .background(RoundedRectangle(cornerRadius: 5)
                    .fill(hover ? Theme.sidebarHover : .clear))
        }
        .buttonStyle(.plain)
        .onHover { hover = $0 }
    }
}

// MARK: - Repo section

private struct RepoSection: View {
    @EnvironmentObject var appState: AppState
    let repo: Repo
    @State private var expanded = true

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            // Tappable section header with animated chevron
            Button {
                withAnimation(.easeInOut(duration: 0.18)) { expanded.toggle() }
            } label: {
                HStack(spacing: 5) {
                    Image(systemName: "chevron.right")
                        .font(.system(size: 9, weight: .semibold))
                        .foregroundColor(Theme.labelSecondary)
                        .rotationEffect(.degrees(expanded ? 90 : 0))
                        .animation(.easeInOut(duration: 0.18), value: expanded)
                    Text(repo.name)
                        .font(.system(size: 11, weight: .medium))
                        .foregroundColor(Theme.labelSecondary)
                    Spacer()
                }
                .padding(.leading, 12)
                .padding(.vertical, 8)
                .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .padding(.top, 4)

            if expanded {
                VStack(alignment: .leading, spacing: 1) {
                    ForEach(repo.workspaces) { ws in
                        Button {
                            appState.selectedWorkspaceID = ws.id
                        } label: {
                            WorkspaceRow(workspace: ws, isSelected: ws.id == appState.selectedWorkspaceID)
                        }
                        .buttonStyle(.plain)
                        .accessibilityIdentifier("workspace_row_\(ws.id)")
                        .accessibilityLabel(ws.name)
                        .accessibilityAddTraits(.isButton)
                    }
                }
                .padding(.top, 2)
                .padding(.bottom, 6)
                .transition(.opacity.combined(with: .move(edge: .top)))
            }
        }
    }
}

// MARK: - Workspace row

private struct WorkspaceRow: View {
    let workspace: Workspace
    let isSelected: Bool
    @EnvironmentObject var appState: AppState
    @State private var hover = false

    var body: some View {
        HStack(spacing: 7) {
            StatusDot(status: workspace.status)
            Text(workspace.name)
                .font(.system(size: 13))
                .foregroundColor(Theme.label)
                .lineLimit(1)
            Spacer(minLength: 0)
        }
        .padding(.leading, 22)
        .padding(.trailing, 10)
        .frame(height: 28)
        .background(
            RoundedRectangle(cornerRadius: 5)
                .fill(isSelected ? Theme.sidebarSelected : hover ? Theme.sidebarHover : .clear)
                .padding(.horizontal, 6)
        )
        .contentShape(Rectangle())
        .onHover { hover = $0 }
        .contextMenu { WorkspaceContextMenu(workspace: workspace) }
    }
}

// MARK: - Context menu

private struct WorkspaceContextMenu: View {
    let workspace: Workspace
    @EnvironmentObject var appState: AppState

    var body: some View {
        switch workspace.state {
        case .stopped, .created:
            Button("Start") { Task { await appState.start(workspace) } }
        case .running, .restored:
            Button("Pause") { Task { await appState.pause(workspace) } }
            Button("Stop")  { Task { await appState.stop(workspace) } }
        case .paused:
            Button("Resume") { Task { await appState.resume(workspace) } }
            Button("Stop")   { Task { await appState.stop(workspace) } }
        }

        Divider()

        Button("Remove…", role: .destructive) {
            Task { await appState.remove(workspace) }
        }
    }
}

// MARK: - Status dot

struct StatusDot: View {
    let status: WorkspaceStatus
    @State private var pulse = false

    var body: some View {
        ZStack {
            if status == .running || status == .restored {
                Circle()
                    .fill(Theme.green.opacity(0.22))
                    .frame(width: 14)
                    .scaleEffect(pulse ? 2.0 : 1.0)
                    .opacity(pulse ? 0 : 0.5)
                    .animation(.easeOut(duration: 1.9).repeatForever(autoreverses: false), value: pulse)
            }
            Circle()
                .fill(Theme.statusColor(status).opacity(status == .stopped || status == .created ? 0 : 1))
                .overlay(Circle().stroke(
                    (status == .stopped || status == .created) ? Theme.labelTertiary : .clear,
                    lineWidth: 1.5))
                .frame(width: 7)
        }
        .frame(width: 14)
        .onAppear { if status == .running || status == .restored { pulse = true } }
    }
}

// MARK: - Footer

private struct SidebarFooter: View {
    @EnvironmentObject var appState: AppState
    @State private var showDaemonPanel = false

    private var connectionLabel: String {
        // Outdated overrides connection state — the daemon is reachable but incompatible.
        if case .outdated = appState.daemonStatus { return "Outdated" }
        switch appState.connectionState {
        case .starting:     return "Starting…"
        case .connecting:   return "Connecting…"
        case .connected:    return "Connected"
        case .disconnected: return "Offline"
        }
    }

    private var dotColor: Color {
        switch appState.daemonStatus {
        case .running:           return Theme.green
        case .outdated:          return Theme.orange
        case .offline, .unknown: return Color.gray
        }
    }

    var body: some View {
        HStack(spacing: 2) {
            FooterBtn(icon: "questionmark.circle") {}
            FooterMenuBtn()
            Spacer()

            // Clickable pill → opens DaemonSettingsPanel
            Button {
                showDaemonPanel.toggle()
            } label: {
                HStack(spacing: 5) {
                    if appState.connectionState == .starting {
                        ProgressView()
                            .scaleEffect(0.45)
                            .frame(width: 6, height: 6)
                    } else {
                        Circle().fill(dotColor).frame(width: 6)
                    }
                    Text(connectionLabel)
                        .font(.system(size: 10))
                        .foregroundColor(Theme.labelTertiary)
                }
                .padding(.horizontal, 8)
                .padding(.vertical, 4)
                .background(
                    RoundedRectangle(cornerRadius: 5)
                        .fill(showDaemonPanel ? Color.primary.opacity(0.08) : .clear)
                )
            }
            .buttonStyle(.plain)
            .popover(isPresented: $showDaemonPanel, arrowEdge: .bottom) {
                DaemonSettingsPanel()
                    .environmentObject(appState)
            }
            .accessibilityIdentifier("connection_status")
            .accessibilityLabel(connectionLabel)
            .padding(.trailing, 4)
        }
        .padding(.horizontal, 6)
        .frame(height: 34)
    }
}

private struct FooterBtn: View {
    let icon: String; let action: () -> Void
    @State private var hover = false
    var body: some View {
        Button(action: action) {
            Image(systemName: icon)
                .font(.system(size: 13))
                .foregroundColor(hover ? Theme.labelSecondary : Theme.labelTertiary)
                .frame(width: 28, height: 28)
        }
        .buttonStyle(.plain)
        .onHover { hover = $0 }
    }
}

private struct FooterMenuBtn: View {
    @EnvironmentObject var appState: AppState
    @State private var hover = false

    var body: some View {
        Menu {
            Button("New Workspace…") { appState.showNewWorkspace = true }

            Divider()

            Button("Connect to Daemon…") {
                // future: open connection sheet
            }
            Button("Disconnect") {
                // future: drop WS connection
            }

            Divider()

            Button("Preferences…") {
                // future: open preferences window
            }

            Divider()

            Button("Quit Nexus") { NSApp.terminate(nil) }
        } label: {
            Image(systemName: "gearshape")
                .font(.system(size: 13))
                .foregroundColor(hover ? Theme.labelSecondary : Theme.labelTertiary)
                .frame(width: 28, height: 28)
                .contentShape(Rectangle())
        }
        .menuStyle(.borderlessButton)
        .fixedSize()
        .onHover { hover = $0 }
    }
}
