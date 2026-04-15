import NexusCore
import SwiftTerm
import SwiftUI
import AppKit

// MARK: - Multi-Tab Terminal View (Conductor-style)

/// A Conductor-style multi-tab terminal with workspace-scoped sessions.
/// Features:
/// - Tab bar with draggable tabs
/// - Add/close tabs
/// - Tab renaming
/// - Clean, minimal UI like Conductor
struct MultiTabTerminalView: View {
    let workspace: Workspace
    @EnvironmentObject var appState: AppState
    @StateObject private var sessionManager: PTYSessionManager
    @State private var ptyError: String?
    @State private var showingRenameSheet = false
    @State private var renamingTabId: String?
    @State private var newTabName = ""
    private let client: WebSocketDaemonClient

    init(workspace: Workspace, client: WebSocketDaemonClient) {
        self.workspace = workspace
        self.client = client
        _sessionManager = StateObject(wrappedValue: PTYSessionManager(
            workspaceId: workspace.id,
            client: client
        ))
    }

    var body: some View {
        VStack(spacing: 0) {
            // Tab Bar
            tabBar
                .background(Color(hex: "#F5F5F7"))

            Divider()
                .background(Color(hex: "#E2E2E6"))

            // Terminal Content
            terminalContent
        }
        .background(Color(hex: "#1A1A1A"))
        .onAppear {
            sessionManager.startRefreshLoop()
            Task {
                await sessionManager.refreshTabs()
                // Create initial tab only when there are no persisted sessions.
                if sessionManager.tabs.isEmpty {
                    await sessionManager.createTab(name: "Terminal 1")
                }
            }
        }
        .onDisappear {
            sessionManager.stopRefreshLoop()
        }
        .alert("Rename Tab", isPresented: $showingRenameSheet) {
            TextField("Tab name", text: $newTabName)
            Button("Cancel", role: .cancel) {}
            Button("Rename") {
                if let tabId = renamingTabId {
                    Task {
                        await sessionManager.renameTab(id: tabId, to: newTabName)
                    }
                }
            }
        } message: {
            Text("Enter a new name for this tab")
        }
    }

    // MARK: - Tab Bar

    private var tabBar: some View {
        HStack(spacing: 0) {
            // Scrollable tabs
            ScrollView(.horizontal, showsIndicators: false) {
                HStack(spacing: 0) {
                    ForEach(sessionManager.tabs) { tab in
                        TabButton(
                            tab: tab,
                            onSelect: { sessionManager.switchToTab(id: tab.id) },
                            onClose: { closeTab(tab.id) },
                            onRename: { startRename(tab) }
                        )
                        .id(tab.id)
                    }
                }
                .padding(.horizontal, 8)
                .padding(.vertical, 4)
            }

            // Add Tab Button
            Button {
                Task {
                    let tabNumber = sessionManager.tabs.count + 1
                    await sessionManager.createTab(name: "Terminal \(tabNumber)")
                }
            } label: {
                Image(systemName: "plus")
                    .font(.system(size: 12, weight: .medium))
                    .foregroundColor(Color(hex: "#666666"))
                    .frame(width: 28, height: 28)
                    .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .padding(.horizontal, 4)
            .accessibilityLabel("Add new tab")

            Spacer()

            // Session info button (shows active session details)
            if let activeTab = sessionManager.tabs.first(where: { $0.isActive }) {
                Menu {
                    Text("Session: \(activeTab.id.prefix(16))...")
                        .font(.system(size: 11))
                    Divider()
                    Button {
                        Task {
                            await sessionManager.renameTab(id: activeTab.id, to: "Main")
                        }
                    } label: {
                        Label("Rename to 'Main'", systemImage: "pencil")
                    }
                    Button {
                        Task {
                            await sessionManager.renameTab(id: activeTab.id, to: "Server")
                        }
                    } label: {
                        Label("Rename to 'Server'", systemImage: "pencil")
                    }
                    Button {
                        Task {
                            await sessionManager.renameTab(id: activeTab.id, to: "Tests")
                        }
                    } label: {
                        Label("Rename to 'Tests'", systemImage: "pencil")
                    }
                } label: {
                    Image(systemName: "info.circle")
                        .font(.system(size: 12))
                        .foregroundColor(Color(hex: "#888888"))
                        .frame(width: 28, height: 28)
                }
                .buttonStyle(.plain)
            }
        }
        .frame(height: 36)
    }

    // MARK: - Terminal Content

    @ViewBuilder
    private var terminalContent: some View {
        if let activeTabId = sessionManager.activeTabId,
           let activeTab = sessionManager.tabs.first(where: { $0.id == activeTabId }) {

            ZStack {
                // Terminal view
                if workspace.state.isActive {
                    TabTerminalView(
                        tab: activeTab,
                        workspace: workspace,
                        client: client,
                        onError: { err in ptyError = err }
                    )
                    .id(activeTab.id) // Force recreation when tab changes
                } else {
                    TerminalPlaceholderView(workspace: workspace)
                }

                // Error banner
                if let err = ptyError {
                    VStack {
                        HStack(spacing: 6) {
                            Image(systemName: "exclamationmark.triangle.fill")
                                .foregroundColor(.orange)
                                .font(.system(size: 11))
                            Text(err)
                                .font(.system(size: 11, design: .monospaced))
                                .foregroundColor(.white)
                            Spacer()
                            Button {
                                ptyError = nil
                            } label: {
                                Image(systemName: "xmark")
                                    .font(.system(size: 10))
                                    .foregroundColor(.white.opacity(0.6))
                            }
                            .buttonStyle(.plain)
                        }
                        .padding(.horizontal, 10)
                        .padding(.vertical, 6)
                        .background(Color.black.opacity(0.85))

                        Spacer()
                    }
                }

                // Loading overlay
                if activeTab.isLoading {
                    VStack(spacing: 8) {
                        ProgressView()
                            .scaleEffect(0.8)
                        Text("Opening terminal...")
                            .font(.system(size: 11))
                            .foregroundColor(.gray)
                    }
                    .frame(maxWidth: .infinity, maxHeight: .infinity)
                    .background(Color.black.opacity(0.5))
                }
            }
        } else {
            // No tabs
            VStack(spacing: 12) {
                Image(systemName: "terminal")
                    .font(.system(size: 32, weight: .ultraLight))
                    .foregroundColor(.gray)
                Text("No terminal tabs")
                    .font(.system(size: 13))
                    .foregroundColor(.gray)
                Button {
                    Task {
                        await sessionManager.createTab(name: "Terminal 1")
                    }
                } label: {
                    Text("Open Terminal")
                        .font(.system(size: 12, weight: .medium))
                }
                .buttonStyle(.borderedProminent)
                .controlSize(.small)
            }
            .frame(maxWidth: .infinity, maxHeight: .infinity)
            .background(Color(hex: "#1A1A1A"))
        }
    }

    // MARK: - Actions

    private func closeTab(_ id: String) {
        Task {
            await sessionManager.closeTab(id: id)
        }
    }

    private func startRename(_ tab: PTYSessionManager.Tab) {
        renamingTabId = tab.id
        newTabName = tab.name
        showingRenameSheet = true
    }
}

// MARK: - Tab Button

private struct TabButton: View {
    let tab: PTYSessionManager.Tab
    let onSelect: () -> Void
    let onClose: () -> Void
    let onRename: () -> Void

    @State private var isHovering = false

    var body: some View {
        HStack(spacing: 6) {
            // Tab name (editable on double-click)
            Text(tab.name)
                .font(.system(size: 12, weight: tab.isActive ? .medium : .regular))
                .foregroundColor(tab.isActive ? Color(hex: "#1F1F1F") : Color(hex: "#6E6E73"))
                .lineLimit(1)
                .frame(maxWidth: 120, alignment: .leading)
                .onTapGesture(count: 2, perform: onRename)

            // Close button (visible on hover or if tab has error)
            if isHovering || tab.error != nil {
                let helpText = tab.error.map { "Error: \($0)" } ?? "Close tab"
                Button {
                    onClose()
                } label: {
                    Image(systemName: tab.error != nil ? "exclamationmark.circle" : "xmark")
                        .font(.system(size: 10, weight: .medium))
                        .foregroundColor(tab.error != nil ? .orange : Color(hex: "#8E8E93"))
                        .frame(width: 16, height: 16)
                }
                .buttonStyle(.plain)
                .help(helpText)
            } else {
                // Spacer for consistent width
                Color.clear
                    .frame(width: 16, height: 16)
            }
        }
        .padding(.horizontal, 12)
        .padding(.vertical, 6)
        .background(
            RoundedRectangle(cornerRadius: 6)
                .fill(tab.isActive ? Color.white : Color.clear)
        )
        .overlay(
            RoundedRectangle(cornerRadius: 6)
                .stroke(tab.isActive ? Color(hex: "#D6D6DA") : Color.clear, lineWidth: 1)
        )
        .onHover { hovering in
            withAnimation(.easeInOut(duration: 0.15)) {
                isHovering = hovering
            }
        }
        .onTapGesture {
            if !tab.isActive {
                onSelect()
            }
        }
        .contextMenu {
            Button {
                onRename()
            } label: {
                Label("Rename", systemImage: "pencil")
            }

            Button {
                onClose()
            } label: {
                Label("Close", systemImage: "xmark")
            }

            Divider()

            Button {
                // Duplicate - create new tab with same name + " (2)"
            } label: {
                Label("Duplicate", systemImage: "doc.on.doc")
            }
        }
    }
}

// MARK: - Tab Terminal View

/// Single terminal view for a specific tab session
private struct TabTerminalView: NSViewRepresentable {
    typealias NSViewType = AutoFocusTerminalView

    let tab: PTYSessionManager.Tab
    let workspace: Workspace
    let client: WebSocketDaemonClient
    let onError: (String) -> Void

    func makeCoordinator() -> Coordinator {
        Coordinator(client: client, sessionId: tab.id, onError: onError)
    }

    func makeNSView(context: Context) -> AutoFocusTerminalView {
        let view = AutoFocusTerminalView(frame: NSRect(x: 0, y: 0, width: 800, height: 500))
        view.autoresizingMask = [.width, .height]
        view.terminalDelegate = context.coordinator
        context.coordinator.termView = view
        applyStyle(to: view)

        // Subscribe to existing session
        Task {
            await context.coordinator.subscribeToSession()
        }

        return view
    }

    func updateNSView(_ nsView: AutoFocusTerminalView, context: Context) {}

    static func dismantleNSView(_ nsView: AutoFocusTerminalView, coordinator: Coordinator) {
        coordinator.unsubscribe()
    }

    private func applyStyle(to view: SwiftTerm.TerminalView) {
        view.nativeForegroundColor       = NSColor(hex: "#D4D4D4") ?? .white
        view.nativeBackgroundColor       = NSColor(hex: "#1A1A1A") ?? .black
        view.caretColor                  = NSColor(hex: "#D4D4D4") ?? .white
        view.selectedTextBackgroundColor = NSColor(hex: "#264F78") ?? .selectedTextBackgroundColor
        view.font = NSFont.monospacedSystemFont(ofSize: 13, weight: .regular)
    }

    // MARK: - Coordinator

    class Coordinator: NSObject, SwiftTerm.TerminalViewDelegate {
        let client: WebSocketDaemonClient
        let sessionId: String
        let onError: (String) -> Void
        weak var termView: AutoFocusTerminalView?

        init(client: WebSocketDaemonClient, sessionId: String, onError: @escaping (String) -> Void) {
            self.client = client
            self.sessionId = sessionId
            self.onError = onError
        }

        func subscribeToSession() async {
            _ = try? await client.attachPTY(sessionId: sessionId)
            client.subscribePTY(
                sessionId: sessionId,
                onData: { [weak self] text in
                    DispatchQueue.main.async { self?.termView?.feed(text: text) }
                },
                onExit: { [weak self] code in
                    DispatchQueue.main.async {
                        let msg = "\r\n\u{001b}[90m[process exited: \(code)]\u{001b}[0m\r\n"
                        self?.termView?.feed(text: msg)
                    }
                }
            )
        }

        func unsubscribe() {
            client.unsubscribePTY(sessionId: sessionId)
        }

        // MARK: TerminalViewDelegate

        func send(source: SwiftTerm.TerminalView, data: ArraySlice<UInt8>) {
            let str = String(bytes: data, encoding: .utf8)
                   ?? String(bytes: data, encoding: .isoLatin1)
                   ?? ""
            guard !str.isEmpty else { return }
            Task { try? await client.writePTY(sessionId: sessionId, data: str) }
        }

        func sizeChanged(source: SwiftTerm.TerminalView, newCols: Int, newRows: Int) {
            guard newCols > 0, newRows > 0 else { return }
            Task { try? await client.resizePTY(sessionId: sessionId, cols: newCols, rows: newRows) }
        }

        func setTerminalTitle(source: SwiftTerm.TerminalView, title: String) {}
        func hostCurrentDirectoryUpdate(source: SwiftTerm.TerminalView, directory: String?) {}
        func bell(source: SwiftTerm.TerminalView) { NSSound.beep() }
        func scrolled(source: SwiftTerm.TerminalView, position: Double) {}
        func clipboardCopy(source: SwiftTerm.TerminalView, content: Data) {
            NSPasteboard.general.clearContents()
            NSPasteboard.general.setData(content, forType: .string)
        }
        func rangeChanged(source: SwiftTerm.TerminalView, startY: Int, endY: Int) {}
    }
}

// MARK: - Terminal Placeholder (for stopped/paused workspaces)

private struct TerminalPlaceholderView: View {
    let workspace: Workspace

    var body: some View {
        VStack(spacing: 12) {
            Image(systemName: iconName)
                .font(.system(size: 24, weight: .ultraLight))
                .foregroundColor(.gray)
            Text(message)
                .font(.system(size: 12, design: .monospaced))
                .foregroundColor(.gray)
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .background(Color(hex: "#1A1A1A"))
    }

    private var iconName: String {
        switch workspace.state {
        case .paused:             "pause.circle"
        case .stopped, .created: "stop.circle"
        default:                 "terminal"
        }
    }

    private var message: String {
        switch workspace.state {
        case .paused:             "Sandbox is paused — start it to open a shell"
        case .stopped, .created: "Sandbox is stopped — start it to open a shell"
        default:                 "Sandbox not available"
        }
    }
}

// MARK: - Use Theme Color extension
// Color.init(hex:) is defined in Theme.swift
