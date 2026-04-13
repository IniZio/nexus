import NexusCore
import SwiftUI

// The bottom panel now lives in the sidebar, not the main content area.
// Tabs: Ports | Log

enum BottomTab: String, CaseIterable {
    case ports = "Ports"
    case log   = "Log"
}

// MARK: - Panel root

struct BottomPanelView: View {
    let workspace: Workspace
    @Binding var activeTab: BottomTab

    var body: some View {
        VStack(spacing: 0) {
            // Tab strip
            HStack(spacing: 0) {
                ForEach(BottomTab.allCases, id: \.self) { tab in
                    TabBtn(tab: tab, active: activeTab == tab) {
                        withAnimation(.easeInOut(duration: 0.12)) { activeTab = tab }
                    }
                }
                Spacer()
            }
            .padding(.leading, 2)
            .frame(height: 26)
            .background(SidebarMaterial())

            Divider().overlay(Theme.separator)

            Group {
                switch activeTab {
                case .ports: PortsPane(ports: workspace.ports)
                case .log:   LogPane(workspace: workspace)
                }
            }
            .frame(maxWidth: .infinity, maxHeight: .infinity)
            .background(Theme.bgContent)
        }
    }
}

// MARK: - Tab button

private struct TabBtn: View {
    let tab: BottomTab; let active: Bool; let action: () -> Void
    @State private var hover = false

    var body: some View {
        Button(action: action) {
            Text(tab.rawValue)
                .font(.system(size: 11, weight: active ? .medium : .regular))
                .foregroundColor(active ? Theme.label : hover ? Theme.labelSecondary : Theme.labelTertiary)
                .padding(.horizontal, 10)
                .frame(height: 26)
                .overlay(alignment: .bottom) {
                    if active {
                        Rectangle()
                            .fill(Theme.accent)
                            .frame(height: 1.5)
                    }
                }
        }
        .buttonStyle(.plain)
        .contentShape(Rectangle())
        .onHover { hover = $0 }
    }
}

// MARK: - Ports

private struct PortsPane: View {
    let ports: [ForwardedPort]
    var body: some View {
        if ports.isEmpty {
            Text("No forwarded ports")
                .font(Theme.fontSm)
                .foregroundColor(Theme.labelTertiary)
                .frame(maxWidth: .infinity, maxHeight: .infinity)
        } else {
            VStack(spacing: 0) {
                ForEach(ports) { p in
                    PortRow(port: p)
                    Divider().overlay(Theme.separator).padding(.leading, 12)
                }
                Spacer()
            }
            .padding(.top, 4)
        }
    }
}

private struct PortRow: View {
    let port: ForwardedPort
    @State private var hover = false
    var body: some View {
        HStack(spacing: 6) {
            Circle().fill(Theme.green).frame(width: 5)
            Text("\(port.port)")
                .font(.system(size: 11, weight: .medium, design: .monospaced))
                .foregroundColor(Theme.label)
            Text("·").foregroundColor(Theme.labelTertiary).font(.system(size: 11))
            Text("localhost:\(port.port)")
                .font(.system(size: 11, design: .monospaced))
                .foregroundColor(Theme.labelSecondary)
            Spacer()
            Button("Open ↗") { NSWorkspace.shared.open(port.localURL) }
                .buttonStyle(.plain)
                .font(.system(size: 10, weight: .medium))
                .foregroundColor(hover ? Theme.accent : Theme.labelSecondary)
                .onHover { hover = $0 }
        }
        .padding(.horizontal, 12)
        .padding(.vertical, 6)
    }
}

// MARK: - Log (real git log via exec)

private struct LogPane: View {
    let workspace: Workspace
    @EnvironmentObject var appState: AppState
    @State private var entries: [LogEntry] = []
    @State private var state: LoadState = .idle

    enum LoadState { case idle, loading, loaded, error(String) }

    struct LogEntry: Identifiable {
        let id = UUID()
        let hash: String
        let subject: String
        let author: String
        let date: String
    }

    var body: some View {
        Group {
            switch state {
            case .idle, .loading:
                HStack(spacing: 6) {
                    ProgressView().scaleEffect(0.7)
                    Text("Loading…").font(Theme.fontSm).foregroundColor(Theme.labelTertiary)
                }
                .frame(maxWidth: .infinity, maxHeight: .infinity)

            case .error(let msg):
                VStack(spacing: 6) {
                    Image(systemName: "exclamationmark.triangle")
                        .foregroundColor(Theme.labelTertiary)
                    Text(msg)
                        .font(Theme.fontSm).foregroundColor(Theme.labelTertiary)
                        .multilineTextAlignment(.center).padding(.horizontal, 12)
                }
                .frame(maxWidth: .infinity, maxHeight: .infinity)

            case .loaded:
                if entries.isEmpty {
                    Text("No commits found")
                        .font(Theme.fontSm).foregroundColor(Theme.labelTertiary)
                        .frame(maxWidth: .infinity, maxHeight: .infinity)
                } else {
                    ScrollView {
                        LazyVStack(alignment: .leading, spacing: 0) {
                            ForEach(entries) { entry in
                                LogRow(entry: entry)
                                Divider().overlay(Theme.separator).padding(.leading, 12)
                            }
                        }
                        .padding(.vertical, 4)
                    }
                }
            }
        }
        .task(id: workspace.id) { await load() }
    }

    private func load() async {
        state = .loading
        guard let client = appState.client as? WebSocketDaemonClient else {
            state = .error("exec not available")
            return
        }

        // Resolve the actual workspace source path so git runs in the right dir.
        // Falls back to the rootPath stored on the workspace model if info fails.
        var workDir: String? = nil
        if let info = try? await client.workspaceInfo(id: workspace.id),
           !info.workspacePath.isEmpty {
            workDir = info.workspacePath
        } else if !workspace.rootPath.isEmpty {
            workDir = workspace.rootPath
        }

        do {
            let out = try await client.exec(
                workspaceId: workspace.id,
                command: "git",
                args: ["log", "--format=%h\t%s\t%an\t%ar", "-25"],
                workDir: workDir
            )
            if out.failed {
                state = .error(out.stderr.isEmpty ? "Not a git repo" : out.stderr)
                return
            }
            if out.output.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
                entries = []
                state = .loaded
                return
            }
            entries = out.output.split(separator: "\n", omittingEmptySubsequences: true)
                .compactMap { line in
                    let parts = line.split(separator: "\t", maxSplits: 3)
                    guard parts.count >= 2 else { return nil }
                    return LogEntry(
                        hash:    String(parts[0]),
                        subject: String(parts[1]),
                        author:  parts.count > 2 ? String(parts[2]) : "",
                        date:    parts.count > 3 ? String(parts[3]) : ""
                    )
                }
            state = .loaded
        } catch {
            state = .error(error.localizedDescription)
        }
    }
}

private struct LogRow: View {
    let entry: LogPane.LogEntry
    var body: some View {
        HStack(spacing: 8) {
            Text(entry.hash)
                .font(.system(size: 10, weight: .medium, design: .monospaced))
                .foregroundColor(Theme.accent)
                .frame(width: 52, alignment: .leading)
            VStack(alignment: .leading, spacing: 1) {
                Text(entry.subject)
                    .font(.system(size: 11))
                    .foregroundColor(Theme.label)
                    .lineLimit(1)
                if !entry.author.isEmpty || !entry.date.isEmpty {
                    HStack(spacing: 4) {
                        if !entry.author.isEmpty {
                            Text(entry.author)
                                .font(.system(size: 10))
                                .foregroundColor(Theme.labelTertiary)
                        }
                        if !entry.date.isEmpty {
                            Text("·").font(.system(size: 10)).foregroundColor(Theme.labelTertiary)
                            Text(entry.date)
                                .font(.system(size: 10))
                                .foregroundColor(Theme.labelTertiary)
                        }
                    }
                }
            }
            Spacer()
        }
        .padding(.horizontal, 12)
        .padding(.vertical, 5)
    }
}
