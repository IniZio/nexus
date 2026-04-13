import NexusCore
import SwiftTerm
import SwiftUI
import AppKit

// MARK: - SwiftUI wrapper
//
// Fetches the workspace state, then renders a daemon-backed PTY terminal.
// The shell runs inside the daemon (in the workspace source directory),
// not as a child of this macOS process.

struct TerminalView: View {
    let workspace: Workspace
    @EnvironmentObject var appState: AppState
    @State private var ptyError: String?

    var body: some View {
        if workspace.state.isActive,
           let client = appState.client as? WebSocketDaemonClient {
            ZStack(alignment: .topLeading) {
                DaemonPTYTerminalView(
                    workspaceId: workspace.id,
                    client: client,
                    onError: { err in ptyError = err },
                    onPTYActive: { appState.ptyState = .active },
                    onPTYError: { appState.ptyState = .error },
                    onNSViewCreated: { [weak appState] view in
                        appState?.refocusTerminalAction = { [weak view] in
                            view?.window?.makeFirstResponder(view)
                        }
                    }
                )
                .id(workspace.id + workspace.state.rawValue)
                .accessibilityLabel("Terminal — \(workspace.name)")
                .onAppear {
                    appState.ptyState = .idle
                }

                // Visible + accessible PTY error banner (e.g. "target is busy")
                if let err = ptyError {
                    HStack(spacing: 6) {
                        Image(systemName: "exclamationmark.triangle.fill")
                            .foregroundColor(Theme.orange)
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
                    .accessibilityIdentifier("terminal_error")
                    .accessibilityLabel(err)
                }
            }
            .onChange(of: workspace.id) { _ in ptyError = nil }
        } else {
            TerminalPlaceholder(workspace: workspace)
                .onAppear { appState.ptyState = .idle }
        }
    }
}

// MARK: - Auto-focus wrapper

/// TerminalView subclass that claims first responder as soon as it enters
/// the window hierarchy, ensuring keyboard events reach the terminal.
final class AutoFocusTerminalView: SwiftTerm.TerminalView {
    override func viewDidMoveToWindow() {
        super.viewDidMoveToWindow()
        window?.makeFirstResponder(self)
    }
}

// MARK: - Daemon PTY via WebSocket

struct DaemonPTYTerminalView: NSViewRepresentable {
    let workspaceId: String
    let client: WebSocketDaemonClient
    let onError: (String) -> Void
    let onPTYActive: () -> Void
    let onPTYError: () -> Void
    // Passes the NSView reference back to TerminalView so AppState can store it for refocus
    let onNSViewCreated: (NSView) -> Void

    func makeCoordinator() -> Coordinator {
        Coordinator(client: client, workspaceId: workspaceId,
                    onError: onError, onPTYActive: onPTYActive,
                    onPTYError: onPTYError, onNSViewCreated: onNSViewCreated)
    }

    func makeNSView(context: Context) -> AutoFocusTerminalView {
        // Non-zero initial frame so the terminal knows its initial dimensions
        let view = AutoFocusTerminalView(frame: NSRect(x: 0, y: 0, width: 800, height: 500))
        view.autoresizingMask = [.width, .height]
        view.terminalDelegate = context.coordinator
        view.setAccessibilityIdentifier("terminal_view")
        context.coordinator.termView = view
        context.coordinator.onNSViewCreated(view)
        applyStyle(to: view)
        // Open the PTY asynchronously; output will flow into view.feed(text:)
        Task { await context.coordinator.openSession() }
        return view
    }

    func updateNSView(_ nsView: AutoFocusTerminalView, context: Context) {}

    static func dismantleNSView(_ nsView: AutoFocusTerminalView, coordinator: Coordinator) {
        Task { await coordinator.closeSession() }
    }

    // MARK: - Style

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
        let workspaceId: String
        let onError: (String) -> Void
        let onPTYActive: () -> Void
        let onPTYError: () -> Void
        let onNSViewCreated: (NSView) -> Void
        weak var termView: AutoFocusTerminalView?
        private var sessionId: String?
        private var pendingResize: (cols: Int, rows: Int)?

        init(client: WebSocketDaemonClient, workspaceId: String,
             onError: @escaping (String) -> Void,
             onPTYActive: @escaping () -> Void,
             onPTYError: @escaping () -> Void,
             onNSViewCreated: @escaping (NSView) -> Void) {
            self.client = client
            self.workspaceId = workspaceId
            self.onError = onError
            self.onPTYActive = onPTYActive
            self.onPTYError = onPTYError
            self.onNSViewCreated = onNSViewCreated
        }

        // MARK: PTY lifecycle

        func openSession() async {
            let cols = pendingResize?.cols ?? termView.map { Int($0.bounds.width  / 8) } ?? 80
            let rows = pendingResize?.rows ?? termView.map { Int($0.bounds.height / 16) } ?? 24
            let safeCols = max(cols, 10)
            let safeRows = max(rows, 5)

            do {
                let sid = try await client.openPTY(
                    workspaceId: workspaceId,
                    cols: safeCols,
                    rows: safeRows
                )
                sessionId = sid

                client.subscribePTY(
                    sessionId: sid,
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

                DispatchQueue.main.async { [weak self] in self?.onPTYActive() }

                // Apply any resize that arrived before the session opened
                if let r = pendingResize {
                    try? await client.resizePTY(sessionId: sid, cols: r.cols, rows: r.rows)
                    pendingResize = nil
                }
            } catch {
                let msg = error.localizedDescription
                DispatchQueue.main.async { [weak self] in
                    self?.termView?.feed(text: "\u{001b}[31mpty.open failed: \(msg)\u{001b}[0m\r\n")
                    self?.onError(msg)
                    self?.onPTYError()
                }
            }
        }

        func closeSession() async {
            guard let sid = sessionId else { return }
            client.unsubscribePTY(sessionId: sid)
            try? await client.closePTY(sessionId: sid)
            sessionId = nil
        }

        // MARK: TerminalViewDelegate

        /// User keystrokes → send to daemon PTY
        func send(source: SwiftTerm.TerminalView, data: ArraySlice<UInt8>) {
            guard let sid = sessionId else { return }
            let str = String(bytes: data, encoding: .utf8)
                   ?? String(bytes: data, encoding: .isoLatin1)
                   ?? ""
            guard !str.isEmpty else { return }
            Task { try? await client.writePTY(sessionId: sid, data: str) }
        }

        /// Terminal was resized (font change or view resize) → tell daemon
        func sizeChanged(source: SwiftTerm.TerminalView, newCols: Int, newRows: Int) {
            guard newCols > 0, newRows > 0 else { return }
            if let sid = sessionId {
                Task { try? await client.resizePTY(sessionId: sid, cols: newCols, rows: newRows) }
            } else {
                pendingResize = (cols: newCols, rows: newRows)
            }
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

// MARK: - Placeholder for stopped / paused workspaces

private struct TerminalPlaceholder: View {
    let workspace: Workspace

    var body: some View {
        VStack(spacing: 12) {
            Image(systemName: iconName)
                .font(.system(size: 24, weight: .ultraLight))
                .foregroundColor(Theme.termDim)
            Text(message)
                .font(.system(size: 12, design: .monospaced))
                .foregroundColor(Theme.termDim)
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .background(Theme.bgTerm)
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
        case .paused:             "Workspace is paused — resume to open a shell"
        case .stopped, .created: "Workspace is stopped — start it to open a shell"
        default:                 "Workspace not available"
        }
    }
}

// MARK: - NSColor hex helper

private extension NSColor {
    convenience init?(hex: String) {
        let s = hex.hasPrefix("#") ? String(hex.dropFirst()) : hex
        guard s.count == 6, let n = UInt64(s, radix: 16) else { return nil }
        self.init(
            red:   CGFloat((n >> 16) & 0xFF) / 255,
            green: CGFloat((n >>  8) & 0xFF) / 255,
            blue:  CGFloat( n        & 0xFF) / 255,
            alpha: 1
        )
    }
}
