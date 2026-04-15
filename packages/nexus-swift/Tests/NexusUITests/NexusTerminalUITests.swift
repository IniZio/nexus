import Foundation
import XCTest

// MARK: - Terminal XCUITest suite
//
// RECORDING NEW TESTS
// ────────────────────
// 1. Open NexusApp.xcodeproj in Xcode.
// 2. Place cursor inside any empty test body.
// 3. Press ● Record in the Debug bar.
// 4. Interact with the app (click workspace, type in terminal, etc.).
// 5. Stop recording — Xcode writes the XCUIElement calls for you.
//
// ACCESSIBILITY IDs WIRED UP FOR TERMINAL
// ─────────────────────────────────────────
// PTY state markers live in the sidebar footer (which IS accessible via XCUITest).
// The NavigationSplitView detail column is not traversable by the macOS accessibility
// API, so all terminal state signals are exposed as sidebar Buttons.
//
//   terminal_view        — sidebar Button: PTY session open (app.buttons)
//   terminal_placeholder — sidebar Button: workspace stopped / placeholder showing
//   terminal_error       — sidebar Button: PTY failed, error banner shown
//   workspace_row_<id>   — sidebar Button for each workspace
//   connection_status    — sidebar footer daemon status pill
//
// Clicking terminal_view refocuses the terminal NSView so keyboard input works.

final class NexusTerminalUITests: XCTestCase {

    var app: XCUIApplication!

    override func setUpWithError() throws {
        continueAfterFailure = false
        app = XCUIApplication(bundleIdentifier: "com.nexus.NexusApp")
        app.launchEnvironment["NEXUS_DAEMON_URL"]   = "ws://localhost:63987"
        app.launchEnvironment["NEXUS_DAEMON_TOKEN"] = resolveDaemonToken() ?? ""
    }

    override func setUp() {
        super.setUp()
        XCUIApplication().terminate()
    }

    // ── 1. Terminal view or placeholder appears after selecting workspace ──

    func testTerminalViewAppearsWhenWorkspaceSelected() throws {
        app.launch()
        _ = app.windows.firstMatch.waitForExistence(timeout: 10)
        app.activate()
        try waitForConnected()

        let row = try firstWorkspaceRow(timeout: 15)
        guard row.isHittable else {
            throw XCTSkip("workspace row is not hittable in current window layout")
        }
        row.click()

        // Either terminal (running) or placeholder (stopped/paused) must appear.
        // Both are sidebar Buttons reflecting PTY state (see comment at top).
        let terminal    = terminalView
        let placeholder = terminalPlaceholder

        let appeared = terminal.waitForExistence(timeout: 10)
                    || placeholder.waitForExistence(timeout: 5)

        XCTAssertTrue(appeared,
            "Either terminal_view or terminal_placeholder must appear after clicking a workspace row")
    }

    // ── 2. No PTY error banner for a running workspace ────────────────────

    func testNoPTYErrorBannerOnNormalOpen() throws {
        app.launch()
        _ = app.windows.firstMatch.waitForExistence(timeout: 10)
        try waitForConnected()
        try selectFirstRunningWorkspace()

        let errorBanner = terminalError
        // Allow up to 3 s for pty.open to complete and any error to surface
        _ = errorBanner.waitForExistence(timeout: 3)
        XCTAssertFalse(errorBanner.exists,
            "PTY error banner must not appear on a healthy workspace open. " +
            "If it does, check daemon logs for pty.open errors.")
    }

    // ── 3. Terminal accepts keyboard input without crashing ───────────────

    func testTerminalAcceptsKeyboardInput() throws {
        app.launch()
        _ = app.windows.firstMatch.waitForExistence(timeout: 10)
        app.activate()
        try waitForConnected()
        try selectFirstRunningWorkspace()

        guard terminalView.waitForExistence(timeout: 10) else {
            throw XCTSkip("No running workspace with active terminal found")
        }

        // Wait a moment for the PTY to settle after opening
        Thread.sleep(forTimeInterval: 2)

        // Click terminal_view (sidebar button) — its action calls makeFirstResponder
        // on the actual SwiftTerm NSView, giving it keyboard focus.
        terminalView.click()
        Thread.sleep(forTimeInterval: 0.3)

        // Type a recognisable command
        let tag = "nexus-ui-test-\(Int.random(in: 10000...99999))"
        app.typeText("echo \(tag)\n")

        // The window must still be alive (no crash)
        XCTAssertTrue(app.windows.firstMatch.exists,
            "App should not crash after typing in terminal")

        // No error banner should appear
        _ = terminalError.waitForExistence(timeout: 2)
        XCTAssertFalse(terminalError.exists, "No error banner after typing in terminal")
    }

    // ── 4. Terminal re-opens cleanly after workspace switch ───────────────
    //
    // Directly tests the lazy-umount fix: navigating away and back must NOT
    // produce a "target is busy" PTY error banner.

    func testTerminalReopensAfterWorkspaceSwitch() throws {
        app.launch()
        _ = app.windows.firstMatch.waitForExistence(timeout: 10)
        app.activate()
        try waitForConnected()

        let rows = workspaceRows()
        guard rows.firstMatch.waitForExistence(timeout: 15) else {
            throw XCTSkip("No workspace rows found")
        }

        // Select first workspace
        rows.firstMatch.click()
        guard terminalView.waitForExistence(timeout: 10) else {
            throw XCTSkip("First workspace is not running — cannot test reopen")
        }

        // Navigate away (second row if available; otherwise click the header)
        if rows.count >= 2 {
            rows.element(boundBy: 1).click()
            Thread.sleep(forTimeInterval: 1)
        } else {
            // Click empty area to deselect
            app.windows.firstMatch.coordinate(withNormalizedOffset: CGVector(dx: 0.7, dy: 0.5)).click()
            Thread.sleep(forTimeInterval: 0.5)
        }
        // Navigate back
        rows.firstMatch.click()

        // Terminal should reopen — allow a few seconds for pty.open to complete
        XCTAssertTrue(terminalView.waitForExistence(timeout: 10),
            "Terminal should reopen after workspace switch")

        _ = terminalError.waitForExistence(timeout: 3)
        XCTAssertFalse(terminalError.exists,
            "PTY error banner must NOT appear after reopen (lazy unmount fix)")
    }

    // ── 5. Placeholder shown for stopped / paused workspace ──────────────

    func testPlaceholderShownForStoppedWorkspace() throws {
        app.launch()
        _ = app.windows.firstMatch.waitForExistence(timeout: 10)
        app.activate()
        try waitForConnected()

        let rows = workspaceRows()
        guard rows.firstMatch.waitForExistence(timeout: 15) else {
            throw XCTSkip("No workspace rows found")
        }

        // Click each row until we find one that shows the placeholder
        var foundPlaceholder = false
        for i in 0..<min(rows.count, 5) {
            rows.element(boundBy: i).click()
            if terminalPlaceholder.waitForExistence(timeout: 4) {
                foundPlaceholder = true
                XCTAssertFalse(terminalView.exists,
                    "terminal_view must not coexist with terminal_placeholder")
                break
            }
        }

        if !foundPlaceholder {
            throw XCTSkip("All visible workspaces appear to be running — none show the placeholder")
        }
    }

    // ── 6. Recording playground ───────────────────────────────────────────
    // Place cursor on the empty line below and press ● Record.

    func testRecordTerminalInteraction() throws {
        app.launch()
        _ = app.windows.firstMatch.waitForExistence(timeout: 10)
        app.activate()
        try waitForConnected()
        try selectFirstRunningWorkspace()
        _ = terminalView.waitForExistence(timeout: 10)

        // ← Xcode inserts recorded steps here.
    }

    // ── 7. PTY session exposes SSH_AUTH_SOCK ───────────────────────────────
    //
    // Regressions here break `git fetch` over SSH and tools like opencode.
    func testPTYSessionHasSSHAuthSock() throws {
        let requireSSHAgent = ProcessInfo.processInfo.environment["NEXUS_UI_TEST_REQUIRE_SSH_AGENT"] == "1"
        guard requireSSHAgent else {
            throw XCTSkip("Set NEXUS_UI_TEST_REQUIRE_SSH_AGENT=1 to enforce SSH agent relay assertions")
        }
        app.launch()
        _ = app.windows.firstMatch.waitForExistence(timeout: 10)
        app.activate()

        let token = resolveDaemonToken() ?? ""
        let client: PTYRPCClient
        do {
            client = try PTYRPCClient(urlString: "ws://localhost:63987", token: token)
        } catch {
            throw XCTSkip("Could not connect PTY RPC client; verify daemon token and auth.")
        }
        defer { client.close() }

        let all: [PTYRPCClient.WorkspaceRecord]
        do {
            all = try client.listWorkspaces()
        } catch {
            throw XCTSkip("Could not list workspaces via PTY RPC (auth/session mismatch).")
        }
        guard let target = all.first(where: { $0.backend == "seatbelt" || $0.backend == "firecracker" }) else {
            XCTFail("No seatbelt/firecracker workspace available for PTY SSH_AUTH_SOCK verification")
            return
        }
        if target.state != "running" {
            try client.startWorkspace(id: target.id)
        }

        let sessionID = try client.openPTY(workspaceID: target.id)
        defer { _ = try? client.closePTY(sessionID: sessionID) }

        let markerSummary = "__SSH_SUMMARY__:"
        let cmd = """
        sock_status=no
        if [ -S "${SSH_AUTH_SOCK:-}" ]; then sock_status=yes; fi
        ssh_add_rc=1
        if command -v ssh-add >/dev/null 2>&1; then
          ssh-add -l >/dev/null 2>&1
          ssh_add_rc=$?
        fi
        git_rc=1
        if command -v git >/dev/null 2>&1; then
          git ls-remote git@github.com:inizio/nexus.git >/dev/null 2>&1
          git_rc=$?
        fi
        printf '%s%s|%s|%s|%s\\n' '\(markerSummary)' "${SSH_AUTH_SOCK:-}" "$sock_status" "$ssh_add_rc" "$git_rc"
        exit
        """
        try client.writePTY(sessionID: sessionID, data: cmd + "\n")

        let summaryLine: String
        do {
            summaryLine = try client.awaitDataLineWithPrefix(sessionID: sessionID, prefix: markerSummary, timeout: 45)
        } catch {
            summaryLine = try client.awaitDataLine(sessionID: sessionID, containing: markerSummary, timeout: 10)
        }
        guard let markerRange = summaryLine.range(of: markerSummary) else {
            XCTFail("Summary marker missing from PTY output: \(summaryLine)")
            return
        }
        let summaryValue = String(summaryLine[markerRange.upperBound...]).trimmingCharacters(in: .whitespacesAndNewlines)
        let fields = summaryValue.components(separatedBy: "|")
        XCTAssertEqual(fields.count, 4, "Expected 4 SSH summary fields, got: \(summaryValue)")
        guard fields.count == 4 else { return }

        let sockPath = fields[0]
        let sockStatus = fields[1]
        let sshAddRC = fields[2]
        let gitRC = fields[3]

        XCTAssertFalse(sockPath.isEmpty, "Expected SSH_AUTH_SOCK to be non-empty in PTY shell")
        XCTAssertEqual(sockStatus, "yes", "Expected SSH_AUTH_SOCK path to be a live Unix socket")
        XCTAssertEqual(sshAddRC, "0", "Expected ssh-add -l to succeed via SSH agent")
        XCTAssertEqual(gitRC, "0", "Expected git ls-remote over SSH to succeed")
    }
}

// MARK: - Helpers

extension NexusTerminalUITests {

    // Terminal state elements are sidebar Buttons (NavigationSplitView detail
    // pane is not accessible on macOS via XCUITest — sidebar IS accessible).
    var terminalView:    XCUIElement { app.buttons["terminal_view"] }
    var terminalPlaceholder: XCUIElement { app.buttons["terminal_placeholder"] }
    var terminalError:   XCUIElement { app.buttons["terminal_error"] }

    /// Returns the query for all workspace rows in the sidebar.
    func workspaceRows() -> XCUIElementQuery {
        app.buttons.matching(
            NSPredicate(format: "identifier BEGINSWITH 'workspace_row_'")
        )
    }

    /// Throws if at least one workspace row doesn't appear within `timeout` seconds.
    func waitForConnected(timeout: TimeInterval = 30) throws {
        let rows = workspaceRows()
        let appeared = rows.firstMatch.waitForExistence(timeout: timeout)
        if !appeared {
            guard app.windows.firstMatch.exists else {
                throw XCTSkip("Nexus app is not running while waiting for workspace rows.")
            }
            let allDesc = app.descendants(matching: .any)
            let labels = (0..<min(allDesc.count, 40)).compactMap { i -> String? in
                let el = allDesc.element(boundBy: i)
                let lbl = el.label; let id = el.identifier
                return (lbl.isEmpty && id.isEmpty) ? nil : "[\(el.elementType.rawValue)] id='\(id)' lbl='\(lbl)'"
            }.joined(separator: "\n  ")
            throw XCTSkip("No workspace rows after \(timeout)s.\nAll visible elements:\n  \(labels)")
        }
    }

    /// Returns the first workspace row, waiting up to `timeout` for it to appear.
    func firstWorkspaceRow(timeout: TimeInterval = 15) throws -> XCUIElement {
        let rows = workspaceRows()
        guard rows.firstMatch.waitForExistence(timeout: timeout) else {
            throw XCTSkip("No workspace rows visible — create a workspace first")
        }
        return rows.firstMatch
    }

    /// Clicks workspace rows until one shows the active terminal_view.
    /// Skips the test if no running workspace is found.
    func selectFirstRunningWorkspace() throws {
        let rows = workspaceRows()
        guard rows.firstMatch.waitForExistence(timeout: 15) else {
            throw XCTSkip("No workspace rows visible — create a workspace first")
        }

        let count = rows.count
        for i in 0..<min(count, 5) {
            rows.element(boundBy: i).click()
            if terminalView.waitForExistence(timeout: 4) {
                return
            }
        }
        throw XCTSkip("None of the \(count) visible workspace(s) show an active terminal")
    }

}

// MARK: - Token helper

private func resolveDaemonToken() -> String? {
    if let env = ProcessInfo.processInfo.environment["NEXUS_DAEMON_TOKEN"]?
        .trimmingCharacters(in: .whitespacesAndNewlines),
       !env.isEmpty {
        return env
    }

    let configuredService = ProcessInfo.processInfo.environment["NEXUS_DAEMON_TOKEN_KEYCHAIN_SERVICE"]?
        .trimmingCharacters(in: .whitespacesAndNewlines)
    let services = [
        configuredService,
        "nexus-daemon-token",
        "nexus/token",
        "nexus-daemon",
        "nexus",
    ]
    for maybeService in services {
        guard let service = maybeService, !service.isEmpty else { continue }
        if let token = readMacKeychainPassword(service: service), !token.isEmpty {
            return token
        }
    }
    return nil
}

private func readMacKeychainPassword(service: String) -> String? {
    let task = Process()
    task.executableURL = URL(fileURLWithPath: "/usr/bin/security")
    task.arguments = ["find-generic-password", "-s", service, "-w"]

    let out = Pipe()
    task.standardOutput = out
    task.standardError = Pipe()

    do {
        try task.run()
    } catch {
        return nil
    }
    task.waitUntilExit()
    guard task.terminationStatus == 0 else { return nil }

    let data = out.fileHandleForReading.readDataToEndOfFile()
    guard let raw = String(data: data, encoding: .utf8) else { return nil }
    let token = raw.trimmingCharacters(in: .whitespacesAndNewlines)
    return token.isEmpty ? nil : token
}

private final class PTYRPCClient {
    private let ws: URLSessionWebSocketTask
    private let session: URLSession
    private var nextID: Int = 1

    init(urlString: String, token: String) throws {
        guard let url = URL(string: urlString) else {
            throw NSError(domain: "PTYRPCClient", code: 1, userInfo: [NSLocalizedDescriptionKey: "Invalid URL"])
        }
        let config = URLSessionConfiguration.default
        self.session = URLSession(configuration: config)
        var request = URLRequest(url: url)
        if !token.isEmpty {
            request.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")
        }
        self.ws = session.webSocketTask(with: request)
        self.ws.resume()
    }

    func close() {
        ws.cancel(with: .normalClosure, reason: nil)
        session.invalidateAndCancel()
    }

    func openPTY(workspaceID: String) throws -> String {
        let reqID = nextRequestID()
        let payload: [String: Any] = [
            "jsonrpc": "2.0",
            "id": reqID,
            "method": "pty.open",
            "params": [
                "workspaceId": workspaceID,
                "shell": "bash",
                "workdir": "/workspace",
                "cols": 120,
                "rows": 40
            ]
        ]
        try send(payload)

        let deadline = Date().addingTimeInterval(10)
        while Date() < deadline {
            let msg = try receiveJSON(timeout: 10)
            if String(describing: msg["id"] ?? "") == reqID {
                if let error = msg["error"] as? [String: Any] {
                    throw NSError(domain: "PTYRPCClient", code: 2, userInfo: [NSLocalizedDescriptionKey: "pty.open error: \(error)"])
                }
                guard
                    let result = msg["result"] as? [String: Any],
                    let sid = result["sessionId"] as? String
                else {
                    throw NSError(domain: "PTYRPCClient", code: 3, userInfo: [NSLocalizedDescriptionKey: "pty.open missing sessionId"])
                }
                return sid
            }
        }
        throw NSError(domain: "PTYRPCClient", code: 4, userInfo: [NSLocalizedDescriptionKey: "pty.open timed out"])
    }

    func listWorkspaces() throws -> [WorkspaceRecord] {
        let reqID = nextRequestID()
        try send([
            "jsonrpc": "2.0",
            "id": reqID,
            "method": "workspace.list",
            "params": [:]
        ])
        let deadline = Date().addingTimeInterval(10)
        while Date() < deadline {
            let msg = try receiveJSON(timeout: 10)
            if String(describing: msg["id"] ?? "") != reqID {
                continue
            }
            if let error = msg["error"] as? [String: Any] {
                throw NSError(domain: "PTYRPCClient", code: 11, userInfo: [NSLocalizedDescriptionKey: "workspace.list error: \(error)"])
            }
            guard
                let result = msg["result"] as? [String: Any],
                let workspaces = result["workspaces"] as? [[String: Any]]
            else {
                return []
            }
            return workspaces.compactMap { raw in
                guard let id = raw["id"] as? String else { return nil }
                let state = (raw["state"] as? String) ?? ""
                let backend = (raw["backend"] as? String) ?? ""
                return WorkspaceRecord(id: id, state: state, backend: backend)
            }
        }
        throw NSError(domain: "PTYRPCClient", code: 12, userInfo: [NSLocalizedDescriptionKey: "workspace.list timed out"])
    }

    func startWorkspace(id: String) throws {
        let reqID = nextRequestID()
        try send([
            "jsonrpc": "2.0",
            "id": reqID,
            "method": "workspace.start",
            "params": ["id": id]
        ])
        let deadline = Date().addingTimeInterval(20)
        while Date() < deadline {
            let msg = try receiveJSON(timeout: 20)
            if String(describing: msg["id"] ?? "") != reqID {
                continue
            }
            if let error = msg["error"] as? [String: Any] {
                throw NSError(domain: "PTYRPCClient", code: 13, userInfo: [NSLocalizedDescriptionKey: "workspace.start error: \(error)"])
            }
            return
        }
        throw NSError(domain: "PTYRPCClient", code: 14, userInfo: [NSLocalizedDescriptionKey: "workspace.start timed out"])
    }

    func writePTY(sessionID: String, data: String) throws {
        let payload: [String: Any] = [
            "jsonrpc": "2.0",
            "id": nextRequestID(),
            "method": "pty.write",
            "params": [
                "sessionId": sessionID,
                "data": data
            ]
        ]
        try send(payload)
    }

    func closePTY(sessionID: String) throws {
        let payload: [String: Any] = [
            "jsonrpc": "2.0",
            "id": nextRequestID(),
            "method": "pty.close",
            "params": [
                "sessionId": sessionID
            ]
        ]
        try send(payload)
    }

    func awaitDataLine(sessionID: String, containing marker: String, timeout: TimeInterval) throws -> String {
        var buffer = ""
        let deadline = Date().addingTimeInterval(timeout)
        while Date() < deadline {
            let msg: [String: Any]
            do {
                msg = try receiveJSON(timeout: 2)
            } catch let err as NSError where err.domain == "PTYRPCClient" && err.code == 8 {
                continue
            }
            guard
                let method = msg["method"] as? String,
                method == "pty.data",
                let params = msg["params"] as? [String: Any],
                let sid = params["sessionId"] as? String,
                sid == sessionID,
                let data = params["data"] as? String
            else {
                continue
            }
            buffer.append(data)
            let lines = buffer.components(separatedBy: "\n")
            for line in lines where line.contains(marker) {
                return line
            }
            if let last = lines.last {
                buffer = last
            }
        }
        throw NSError(domain: "PTYRPCClient", code: 5, userInfo: [NSLocalizedDescriptionKey: "Timed out waiting for marker \(marker)"])
    }

    func awaitDataLineWithPrefix(sessionID: String, prefix: String, timeout: TimeInterval) throws -> String {
        var buffer = ""
        let deadline = Date().addingTimeInterval(timeout)
        while Date() < deadline {
            let msg: [String: Any]
            do {
                msg = try receiveJSON(timeout: 2)
            } catch let err as NSError where err.domain == "PTYRPCClient" && err.code == 8 {
                continue
            }
            guard
                let method = msg["method"] as? String,
                method == "pty.data",
                let params = msg["params"] as? [String: Any],
                let sid = params["sessionId"] as? String,
                sid == sessionID,
                let data = params["data"] as? String
            else {
                continue
            }
            buffer.append(data)
            let lines = buffer.components(separatedBy: "\n")
            for line in lines {
                let trimmed = line.trimmingCharacters(in: .whitespacesAndNewlines)
                guard trimmed.contains(prefix) else { continue }

                // Skip echoed command lines (they often contain marker literals)
                // and only keep lines that look like evaluated output.
                if trimmed.contains("${")
                    || trimmed.contains("printf ")
                    || trimmed.contains("echo ")
                    || trimmed.contains("$sock_status")
                    || trimmed.contains("$ssh_add_rc")
                    || trimmed.contains("$git_rc") {
                    continue
                }
                if let r = trimmed.range(of: prefix) {
                    return String(trimmed[r.lowerBound...])
                }
            }
            if let last = lines.last {
                buffer = last
            }
        }
        throw NSError(domain: "PTYRPCClient", code: 15, userInfo: [NSLocalizedDescriptionKey: "Timed out waiting for marker prefix \(prefix)"])
    }

    private func nextRequestID() -> String {
        defer { nextID += 1 }
        return String(nextID)
    }

    struct WorkspaceRecord {
        let id: String
        let state: String
        let backend: String
    }

    private func send(_ obj: [String: Any]) throws {
        let data = try JSONSerialization.data(withJSONObject: obj, options: [])
        guard let text = String(data: data, encoding: .utf8) else {
            throw NSError(domain: "PTYRPCClient", code: 6, userInfo: [NSLocalizedDescriptionKey: "JSON encode failed"])
        }
        let sem = DispatchSemaphore(value: 0)
        var sendError: Error?
        ws.send(.string(text)) { err in
            sendError = err
            sem.signal()
        }
        _ = sem.wait(timeout: .now() + 10)
        if let err = sendError {
            throw err
        }
    }

    private func receiveJSON(timeout: TimeInterval) throws -> [String: Any] {
        let sem = DispatchSemaphore(value: 0)
        var recvError: Error?
        var recvData: Data?
        ws.receive { result in
            switch result {
            case .failure(let err):
                recvError = err
            case .success(let msg):
                switch msg {
                case .string(let s):
                    recvData = s.data(using: .utf8)
                case .data(let d):
                    recvData = d
                @unknown default:
                    recvError = NSError(domain: "PTYRPCClient", code: 7, userInfo: [NSLocalizedDescriptionKey: "Unknown websocket message"])
                }
            }
            sem.signal()
        }
        if sem.wait(timeout: .now() + timeout) == .timedOut {
            throw NSError(domain: "PTYRPCClient", code: 8, userInfo: [NSLocalizedDescriptionKey: "Websocket receive timed out"])
        }
        if let err = recvError {
            throw err
        }
        guard let data = recvData else {
            throw NSError(domain: "PTYRPCClient", code: 9, userInfo: [NSLocalizedDescriptionKey: "No websocket data"])
        }
        guard let obj = try JSONSerialization.jsonObject(with: data) as? [String: Any] else {
            throw NSError(domain: "PTYRPCClient", code: 10, userInfo: [NSLocalizedDescriptionKey: "Invalid JSON payload"])
        }
        return obj
    }
}
