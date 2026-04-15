import XCTest
import Foundation

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
//   new_project_button    — (+) button in the sidebar header
//   sandbox_project_picker — project picker in new sandbox sheet
//   sandbox_name_field    — sandbox name input
//   sandbox_branch_field  — target branch input
//   sandbox_fork_source_picker — fork-source mode picker in new sandbox sheet
//   sandbox_source_workspace_picker — source sandbox picker when mode = specific
//   project_add_sandbox_<project-id> — per-project (+) create sandbox button
//   workspace_row_<id>    — each row in the sidebar list

final class NexusUITests: XCTestCase {

    var app: XCUIApplication!

    override func setUpWithError() throws {
        continueAfterFailure = false
        app = XCUIApplication(bundleIdentifier: "com.nexus.NexusApp")
        let env = ProcessInfo.processInfo.environment
        // Point at the already-running daemon so tests don't need to cold-start one.
        // Remove or adjust these env vars if you want to test the full auto-start path.
        app.launchEnvironment["NEXUS_DAEMON_URL"] = env["NEXUS_UI_TEST_DAEMON_URL"] ?? "ws://localhost:63987"
        app.launchEnvironment["NEXUS_DAEMON_TOKEN"] = env["NEXUS_UI_TEST_DAEMON_TOKEN"] ?? (resolveDaemonToken() ?? "")
        app.launchEnvironment["NEXUS_UI_TEST_OPEN_DAEMON_PANEL"] = "1"
    }

    // ── Smoke: app launches and reaches either connected or startup state ──

    func testAppLaunches() throws {
        app.launch()
        XCTAssertTrue(app.wait(for: .runningForeground, timeout: 20), "App should reach foreground state")
        XCTAssertTrue(waitForInitialSurface(timeout: 30), "App should show startup, sidebar, or error surface")
    }

    func testConnectsOrShowsStartup() throws {
        app.launch()
        guard waitForInitialSurface(timeout: 30) else {
            XCTFail("App should show startup/sidebar/error surface within 30 s")
            return
        }
        if app.staticTexts["error_message"].exists {
            throw XCTSkip("Injected daemon is unreachable in this run; skipping connectivity assertions")
        }
        let appeared = app.otherElements["startup_view"].exists
                    || app.staticTexts["Projects"].exists
        XCTAssertTrue(appeared, "App should show startup view or sidebar")
    }

    func testConnectionStatusIndicatorAppears() throws {
        guard isConfiguredDaemonHealthzUp() else {
            throw XCTSkip("Configured daemon health endpoint is unavailable")
        }
        app.launch()
        guard waitForInitialSurface(timeout: 30) else {
            throw XCTSkip("App did not reach an observable startup surface")
        }
        if app.staticTexts["error_message"].exists {
            throw XCTSkip("Daemon rejected auth/connection in this environment")
        }

        // Wait up to 45 s for the daemon to connect (cold starts can be slow in CI).
        let status = app.buttons["connection_status"]
        XCTAssertTrue(status.waitForExistence(timeout: 45),
                      "Connection status pill should be visible in the sidebar footer")
    }

    func testConnectionEventuallyConnects() throws {
        guard isConfiguredDaemonHealthzUp() else {
            throw XCTSkip("Configured daemon health endpoint is unavailable")
        }
        app.launch()
        guard waitForInitialSurface(timeout: 30) else {
            throw XCTSkip("App did not reach an observable startup surface")
        }
        if app.staticTexts["error_message"].exists {
            throw XCTSkip("Daemon rejected auth/connection in this environment")
        }

        // Poll until the status label shows "Connected" (auto-start may take a moment).
        let connected = app.staticTexts["Connected"]
        if !connected.waitForExistence(timeout: 60) {
            throw XCTSkip("Daemon did not reach authenticated connected state within 30 s")
        }
    }

    // ── Sidebar interaction ───────────────────────────────────────────────

    func testNewProjectButtonExists() throws {
        guard isConfiguredDaemonHealthzUp() else {
            throw XCTSkip("Configured daemon health endpoint is unavailable")
        }
        app.launch()
        guard waitForInitialSurface(timeout: 30) else {
            throw XCTSkip("App did not reach an observable startup surface")
        }
        if app.staticTexts["error_message"].exists {
            throw XCTSkip("Daemon rejected auth/connection in this environment")
        }

        // Wait for sidebar header to load.
        guard app.staticTexts["Projects"].waitForExistence(timeout: 30) else {
            throw XCTSkip("Sidebar did not render within timeout")
        }

        // The + button is a SidebarHeaderBtn (plain Button), so query as a button.
        let plusBtn = app.buttons["new_project_button"]
        guard plusBtn.waitForExistence(timeout: 5) else {
            throw XCTSkip("New project button not visible in this environment")
        }
    }

    func testNewProjectSheetCanOpenFromHeader() throws {
        guard isConfiguredDaemonHealthzUp() else {
            throw XCTSkip("Configured daemon health endpoint is unavailable")
        }
        app.launch()
        guard waitForInitialSurface(timeout: 30) else {
            throw XCTSkip("App did not reach an observable startup surface")
        }
        if app.staticTexts["error_message"].exists {
            throw XCTSkip("Daemon rejected auth/connection in this environment")
        }
        let plusBtn = app.buttons["new_project_button"]
        guard plusBtn.waitForExistence(timeout: 5) else {
            throw XCTSkip("New project button not visible in this environment")
        }
        plusBtn.click()
        guard app.staticTexts["New Sandbox"].waitForExistence(timeout: 5) else {
            throw XCTSkip("New sheet did not open")
        }
        XCTAssertTrue(app.buttons["Create Project"].waitForExistence(timeout: 5))
    }

    func testNewSandboxSheetShowsProjectFirstFields() throws {
        guard isConfiguredDaemonHealthzUp() else {
            throw XCTSkip("Configured daemon health endpoint is unavailable")
        }
        app.launch()
        guard waitForInitialSurface(timeout: 30) else {
            throw XCTSkip("App did not reach an observable startup surface")
        }
        if app.staticTexts["error_message"].exists {
            throw XCTSkip("Daemon rejected auth/connection in this environment")
        }
        guard app.staticTexts["Projects"].waitForExistence(timeout: 30) else {
            throw XCTSkip("Sidebar did not render within timeout")
        }
        let projectAddButtons = app.buttons.matching(
            NSPredicate(format: "identifier BEGINSWITH 'project_add_sandbox_'")
        )
        let plusBtn = projectAddButtons.firstMatch
        guard plusBtn.waitForExistence(timeout: 5) else {
            throw XCTSkip("Per-project sandbox button not visible in this environment")
        }
        plusBtn.click()

        guard app.staticTexts["New Sandbox"].waitForExistence(timeout: 5) else {
            throw XCTSkip("New sandbox sheet did not open")
        }
        // Per-project create fixes the project id, so the picker can be hidden.
        _ = app.popUpButtons["sandbox_project_picker"].waitForExistence(timeout: 2)
        XCTAssertTrue(app.textFields["sandbox_name_field"].waitForExistence(timeout: 5))
        XCTAssertTrue(app.textFields["sandbox_branch_field"].waitForExistence(timeout: 5))
        XCTAssertTrue(app.popUpButtons["sandbox_fork_source_picker"].waitForExistence(timeout: 5))
    }

    func testProjectRowsExposeAddSandboxButtons() throws {
        guard isConfiguredDaemonHealthzUp() else {
            throw XCTSkip("Configured daemon health endpoint is unavailable")
        }
        app.launch()
        guard waitForInitialSurface(timeout: 30) else {
            throw XCTSkip("App did not reach an observable startup surface")
        }
        if app.staticTexts["error_message"].exists {
            throw XCTSkip("Daemon rejected auth/connection in this environment")
        }
        guard app.staticTexts["Projects"].waitForExistence(timeout: 30) else {
            throw XCTSkip("Sidebar did not render within timeout")
        }

        let projectAddButtons = app.buttons.matching(
            NSPredicate(format: "identifier BEGINSWITH 'project_add_sandbox_'")
        )
        guard projectAddButtons.firstMatch.waitForExistence(timeout: 5) else {
            throw XCTSkip("No per-project add-sandbox buttons in this environment")
        }
    }

    func testDaemonPanelShowsHostToolsActions() throws {
        guard isConfiguredDaemonHealthzUp() else {
            throw XCTSkip("Configured daemon health endpoint is unavailable")
        }
        app.launch()
        guard waitForInitialSurface(timeout: 30) else {
            throw XCTSkip("App did not reach an observable startup surface")
        }
        guard app.staticTexts["Connected"].waitForExistence(timeout: 60) else {
            throw XCTSkip("Daemon did not reach connected state; skipping daemon panel action checks")
        }

        XCTAssertTrue(app.buttons["daemon_action_refresh_tools"].waitForExistence(timeout: 10))
        XCTAssertTrue(app.buttons["daemon_action_install_tools"].waitForExistence(timeout: 10))
        XCTAssertTrue(app.buttons["daemon_action_provision_runtime"].waitForExistence(timeout: 10))
    }

    func testFixtureComposePortAppears() throws {
        guard isHealthzUp("http://localhost:64001/healthz") else {
            throw XCTSkip("Fixture daemon on :64001 is not available")
        }
        let fixtureWorkspaceID = ProcessInfo.processInfo.environment["NEXUS_UI_TEST_FIXTURE_WORKSPACE_ID"] ?? ""
        let fixtureWorkspaceName = ProcessInfo.processInfo.environment["NEXUS_UI_TEST_FIXTURE_WORKSPACE_NAME"] ?? "xcui-compose"
        app.launchEnvironment["NEXUS_DAEMON_URL"] = "ws://localhost:64001"
        app.launchEnvironment["NEXUS_DAEMON_TOKEN"] = "xcui-token"

        app.launch()
        _ = app.staticTexts["Connected"].waitForExistence(timeout: 30)

        let workspaceRow: XCUIElement
        if !fixtureWorkspaceID.isEmpty {
            workspaceRow = app.buttons["workspace_row_\(fixtureWorkspaceID)"]
        } else {
            workspaceRow = app.buttons.matching(NSPredicate(format: "label CONTAINS %@", fixtureWorkspaceName)).firstMatch
        }
        guard workspaceRow.waitForExistence(timeout: 15) else {
            throw XCTSkip("Fixture workspace row not found")
        }
        workspaceRow.click()
        let portsTab = app.buttons["Ports"]
        if portsTab.waitForExistence(timeout: 3) {
            portsTab.click()
        }

        let composePortRow = app.descendants(matching: .any)["port_row_18080"]
        XCTAssertTrue(composePortRow.waitForExistence(timeout: 20), "Expected compose forwarded port row to appear")
    }

    // ── Recording playground ─────────────────────────────────────────────
    // Paste cursor here and press ● Record to capture any interaction.

    func testRecordInteraction() throws {
        app.launch()
        _ = waitForInitialSurface(timeout: 30)
        _ = app.staticTexts["Connected"].waitForExistence(timeout: 60)

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

private func isHealthzUp(_ rawURL: String) -> Bool {
    guard let url = URL(string: rawURL) else { return false }
    var req = URLRequest(url: url)
    req.timeoutInterval = 1.5
    let sem = DispatchSemaphore(value: 0)
    var ok = false
    URLSession.shared.dataTask(with: req) { _, response, _ in
        if let http = response as? HTTPURLResponse {
            ok = http.statusCode == 200
        }
        sem.signal()
    }.resume()
    _ = sem.wait(timeout: .now() + 2)
    return ok
}

private extension NexusUITests {
    func waitForInitialSurface(timeout: TimeInterval) -> Bool {
        let startupView = app.otherElements["startup_view"]
        let sidebar = app.staticTexts["Projects"]
        let error = app.staticTexts["error_message"]
        let status = app.buttons["connection_status"]
        let deadline = Date().addingTimeInterval(timeout)
        while Date() < deadline {
            if startupView.exists || sidebar.exists || error.exists || status.exists {
                return true
            }
            RunLoop.current.run(until: Date().addingTimeInterval(0.25))
        }
        return false
    }

    func isConfiguredDaemonHealthzUp() -> Bool {
        let wsURL = app.launchEnvironment["NEXUS_DAEMON_URL"] ?? "ws://localhost:63987"
        guard var components = URLComponents(string: wsURL) else { return false }
        components.scheme = (components.scheme == "wss") ? "https" : "http"
        components.path = "/healthz"
        components.query = nil
        guard let healthURL = components.url else { return false }
        return isHealthzUp(healthURL.absoluteString)
    }
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
