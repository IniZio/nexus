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
        app.launchEnvironment["NEXUS_DAEMON_TOKEN"] = tokenFromDisk() ?? ""
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
        app.activate()
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
            let allDesc = app.descendants(matching: .any)
            let labels = (0..<min(allDesc.count, 40)).compactMap { i -> String? in
                let el = allDesc.element(boundBy: i)
                let lbl = el.label; let id = el.identifier
                return (lbl.isEmpty && id.isEmpty) ? nil : "[\(el.elementType.rawValue)] id='\(id)' lbl='\(lbl)'"
            }.joined(separator: "\n  ")
            XCTFail("No workspace rows after \(timeout)s.\nAll visible elements:\n  \(labels)")
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

private func tokenFromDisk() -> String? {
    let home = FileManager.default.homeDirectoryForCurrentUser.path
    let paths = [
        "\(home)/.local/share/nexus/token",
        "\(home)/.config/nexus/run/token",
    ]
    for path in paths {
        if let raw = try? String(contentsOfFile: path, encoding: .utf8) {
            let tok = raw.trimmingCharacters(in: .whitespacesAndNewlines)
            if !tok.isEmpty { return tok }
        }
    }
    return nil
}
