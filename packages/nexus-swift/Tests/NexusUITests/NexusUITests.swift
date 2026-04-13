import XCTest

// MARK: - Nexus UI Test Suite
//
// HOW TO RECORD A NEW TEST
// ─────────────────────────
// 1. Open NexusApp.xcodeproj in Xcode.
// 2. Select the NexusUITests target.
// 3. Place the cursor inside an empty test method body (any `func testXxx` below).
// 4. Click the red ● Record button in the Debug bar (or Editor ▸ Start Recording UI Test).
// 5. Interact with the app — Xcode types the XCUIElement calls for you.
// 6. Stop recording, then run the test to verify.
//
// ACCESSIBILITY IDs ALREADY WIRED UP
// ────────────────────────────────────
//   startup_view          — spinner shown while daemon launches
//   empty_state_view      — "Select a workspace" placeholder
//   error_message         — error text when daemon unreachable
//   connection_status     — footer pill ("Connected" / "Starting…" / "Offline")
//   new_workspace_button  — (+) button in the sidebar header
//   workspace_row_<id>    — each row in the sidebar list

final class NexusUITests: XCTestCase {

    var app: XCUIApplication!

    override func setUpWithError() throws {
        continueAfterFailure = false
        app = XCUIApplication()
        // Point at the already-running daemon so tests don't need to cold-start one.
        // Remove or adjust these env vars if you want to test the full auto-start path.
        app.launchEnvironment["NEXUS_DAEMON_URL"]   = "ws://localhost:8080"
        app.launchEnvironment["NEXUS_DAEMON_TOKEN"] = tokenFromDisk() ?? ""
    }

    // ── Smoke: app launches and reaches either connected or startup state ──

    func testAppLaunches() throws {
        app.launch()

        // The window should appear within a few seconds.
        let window = app.windows.firstMatch
        XCTAssertTrue(window.waitForExistence(timeout: 10), "Main window should appear")
    }

    func testConnectsOrShowsStartup() throws {
        app.launch()

        // Either the startup spinner or the sidebar "Workspaces" header should appear,
        // indicating the app initialised successfully.
        let startupView  = app.otherElements["startup_view"]
        let sidebarLabel = app.staticTexts["Workspaces"]

        let appeared = startupView.waitForExistence(timeout: 5)
                    || sidebarLabel.waitForExistence(timeout: 5)
        XCTAssertTrue(appeared, "App should show startup view or sidebar within 5 s")
    }

    func testConnectionStatusIndicatorAppears() throws {
        app.launch()

        // Wait up to 15 s for the daemon to connect (auto-start may run).
        let status = app.otherElements["connection_status"]
        XCTAssertTrue(status.waitForExistence(timeout: 15),
                      "Connection status pill should be visible in the sidebar footer")
    }

    func testConnectionEventuallyConnects() throws {
        app.launch()

        // Poll until the status label shows "Connected" (auto-start may take a moment).
        let connected = app.staticTexts["Connected"]
        XCTAssertTrue(connected.waitForExistence(timeout: 30),
                      "Daemon should connect within 30 s")
    }

    // ── Sidebar interaction ───────────────────────────────────────────────

    func testNewWorkspaceButtonExists() throws {
        app.launch()

        // Wait for sidebar header to load.
        _ = app.staticTexts["Workspaces"].waitForExistence(timeout: 15)

        // The + button is a SidebarHeaderBtn (plain Button), so query as a button.
        let plusBtn = app.buttons["new_workspace_button"]
        XCTAssertTrue(plusBtn.waitForExistence(timeout: 5),
                      "New-workspace (+) button should be in the sidebar header")
    }

    // ── Recording playground ─────────────────────────────────────────────
    // Paste cursor here and press ● Record to capture any interaction.

    func testRecordInteraction() throws {
        app.launch()
        _ = app.staticTexts["Connected"].waitForExistence(timeout: 30)

        // Select first workspace row if any exist (otherElements, not cells)
        let rows = app.otherElements.matching(
            NSPredicate(format: "identifier BEGINSWITH 'workspace_row_'")
        )
        if rows.firstMatch.waitForExistence(timeout: 10) {
            rows.firstMatch.click()
        }

        // ← Xcode will insert recorded steps below this line.
    }
}

// MARK: - Launch performance

final class NexusUILaunchTests: XCTestCase {

    func testLaunchPerformance() throws {
        measure(metrics: [XCTApplicationLaunchMetric()]) {
            XCUIApplication().launch()
        }
    }
}

// MARK: - Helpers

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
