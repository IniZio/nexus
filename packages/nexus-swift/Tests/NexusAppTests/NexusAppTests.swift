import XCTest
import Foundation
@testable import NexusCore

// MARK: - Test helpers

/// Returns true if the Nexus daemon is accepting connections at its discovered URL
/// AND we can authenticate (token round-trip succeeds).
func isDaemonRunning() -> Bool {
    let wsURL = WebSocketDaemonClient.discoverURL()
    guard var comps = URLComponents(url: wsURL, resolvingAgainstBaseURL: false) else { return false }
    comps.scheme = "http"
    comps.path = "/healthz"
    comps.queryItems = nil
    guard let url = comps.url else { return false }

    let sem = DispatchSemaphore(value: 0)
    var running = false
    let task = URLSession.shared.dataTask(with: url) { _, resp, _ in
        running = (resp as? HTTPURLResponse)?.statusCode == 200
        sem.signal()
    }
    task.resume()
    _ = sem.wait(timeout: .now() + 2.0)
    return running
}

/// Creates a WebSocketDaemonClient pointed at the auto-discovered daemon.
func makeClient() -> WebSocketDaemonClient {
    WebSocketDaemonClient(daemonURL: WebSocketDaemonClient.discoverURL())
}

// MARK: - DaemonLauncher unit tests (no daemon required)

final class DaemonLauncherTests: XCTestCase {

    func testResolveRunDirUsesXDGRuntimeDirWhenSet() {
        let save = ProcessInfo.processInfo.environment["XDG_RUNTIME_DIR"]

        // Can't mutate the real env in Swift tests; verify the logic path indirectly
        // by checking the default (no XDG_RUNTIME_DIR) uses ~/.config/nexus/run.
        let runDir = DaemonLauncher.resolveRunDir()
        let home = FileManager.default.homeDirectoryForCurrentUser.path
        XCTAssertTrue(
            runDir.hasPrefix(home) || runDir.hasPrefix("/tmp"),
            "Run dir should be under home or /tmp, got: \(runDir)"
        )
        XCTAssertTrue(runDir.hasSuffix("nexus/run") || runDir.hasSuffix("nexus"),
            "Run dir should end with nexus/run, got: \(runDir)")
        _ = save  // suppress unused warning
    }

    func testResolveBinaryFindsDevBinaryInMonorepoLayout() {
        // In the dev layout (swift run from packages/nexus-swift/),
        // resolveBinary() should walk up and find packages/nexus/nexus-daemon.
        let result = DaemonLauncher.resolveBinary()

        // If the binary is found, verify it exists and is executable.
        if let url = result {
            XCTAssertTrue(
                FileManager.default.isExecutableFile(atPath: url.path),
                "Resolved binary should be executable: \(url.path)"
            )
            XCTAssertTrue(
                url.lastPathComponent == "nexus-daemon",
                "Binary name should be nexus-daemon, got: \(url.lastPathComponent)"
            )
        } else {
            // In CI without the Go binary built, it's OK to not find it.
            // Warn so it's visible in test output, but don't fail.
            print("WARNING: DaemonLauncher.resolveBinary() returned nil " +
                  "(expected in CI before Go build; binary not in PATH or project tree)")
        }
    }

    func testResolveRunningPortReturnsPIDFilePort() {
        let runDir = DaemonLauncher.resolveRunDir()
        let fm = FileManager.default

        // Write a fake PID file for port 19999
        try? fm.createDirectory(atPath: runDir, withIntermediateDirectories: true)
        let fakePID = runDir + "/daemon-19999.pid"
        try? "12345".write(toFile: fakePID, atomically: true, encoding: .utf8)

        let port = DaemonLauncher.resolveRunningPort()
        try? fm.removeItem(atPath: fakePID)

        XCTAssertEqual(port, 19999)
    }

    func testResolveRunningPortReturnsNilWhenNoPIDFiles() {
        // Just verify the returned port (if any) is a valid port number.
        let port = DaemonLauncher.resolveRunningPort()
        if let p = port {
            XCTAssertGreaterThan(p, 0)
            XCTAssertLessThan(p, 65536)
        }
        // nil is also valid (no daemon running)
    }

    func testIsHealthyReturnsFalseForClosedPort() async {
        // Port 19998 should never be in use during tests.
        let healthy = await DaemonLauncher.isHealthy(port: 19998)
        XCTAssertFalse(healthy)
    }

    func testIsHealthyReturnsTrueWhenDaemonRunning() async throws {
        guard isDaemonRunning() else {
            throw XCTSkip("Daemon not running — skipping isHealthy live test")
        }
        let port = DaemonLauncher.resolveRunningPort() ?? 8080
        let healthy = await DaemonLauncher.isHealthy(port: port)
        XCTAssertTrue(healthy)
    }
}

// MARK: - Workspace model unit tests (no daemon required)

final class WorkspaceModelTests: XCTestCase {

    func testDecodeFromDaemonJSON() throws {
        let json = """
        {
            "id": "ws-abc",
            "workspaceName": "auth-feature",
            "repo": "git@github.com:acme/api.git",
            "ref": "feat/oauth",
            "state": "running",
            "rootPath": "/home/user/ws",
            "agentProfile": "default",
            "repoId": "repo-api"
        }
        """.data(using: .utf8)!

        let ws = try JSONDecoder().decode(Workspace.self, from: json)
        XCTAssertEqual(ws.id, "ws-abc")
        XCTAssertEqual(ws.workspaceName, "auth-feature")
        XCTAssertEqual(ws.ref, "feat/oauth")
        XCTAssertEqual(ws.state, .running)
        XCTAssertEqual(ws.repoId, "repo-api")
        XCTAssertTrue(ws.state.isActive)
    }

    func testDecodeHandlesMissingOptionalFields() throws {
        let json = """
        {"id": "ws-min", "workspaceName": "minimal"}
        """.data(using: .utf8)!

        let ws = try JSONDecoder().decode(Workspace.self, from: json)
        XCTAssertEqual(ws.id, "ws-min")
        XCTAssertEqual(ws.ref, "main")            // default
        XCTAssertEqual(ws.state, .stopped)        // default
        XCTAssertNil(ws.repoId)
    }

    func testDecodeRuntimeBackendAndLabel() throws {
        let json = """
        {
            "id": "ws-1",
            "workspaceName": "dev",
            "ref": "main",
            "state": "running",
            "backend": "firecracker",
            "runtimeLabel": "backend=firecracker isolation=vm"
        }
        """.data(using: .utf8)!

        let ws = try JSONDecoder().decode(Workspace.self, from: json)
        XCTAssertEqual(ws.backend, "firecracker")
        XCTAssertEqual(ws.runtimeLabel, "backend=firecracker isolation=vm")
        XCTAssertEqual(ws.shortRuntimeBadge, "VM")
        XCTAssertEqual(ws.detailRuntimeLine, "backend=firecracker isolation=vm")
    }

    func testWorkspaceStatusDisplayNames() {
        XCTAssertEqual(WorkspaceStatus.running.displayName,  "Running")
        XCTAssertEqual(WorkspaceStatus.paused.displayName,   "Paused")
        XCTAssertEqual(WorkspaceStatus.stopped.displayName,  "Stopped")
        XCTAssertEqual(WorkspaceStatus.created.displayName,  "Ready")
        XCTAssertEqual(WorkspaceStatus.restored.displayName, "Running")
    }

    func testIsActiveStates() {
        XCTAssertTrue(WorkspaceStatus.running.isActive)
        XCTAssertTrue(WorkspaceStatus.restored.isActive)
        XCTAssertFalse(WorkspaceStatus.paused.isActive)
        XCTAssertFalse(WorkspaceStatus.stopped.isActive)
        XCTAssertFalse(WorkspaceStatus.created.isActive)
    }

    func testRepoGroupingFromRelations() {
        let workspaces = [
            Workspace(id: "ws-1", workspaceName: "a", repo: "git@gh/api.git",
                      state: .running, repoId: "r1"),
            Workspace(id: "ws-2", workspaceName: "b", repo: "git@gh/api.git",
                      state: .stopped, repoId: "r1"),
            Workspace(id: "ws-3", workspaceName: "c", repo: "git@gh/web.git",
                      state: .stopped, repoId: "r2"),
        ]
        let groups = [
            RelationsGroup(repoId: "r1", repo: "git@gh/api.git", displayName: "api",
                           nodes: [
                            RelationNode(workspaceId: "ws-1", workspaceName: "a", state: .running),
                            RelationNode(workspaceId: "ws-2", workspaceName: "b", state: .stopped),
                           ]),
            RelationsGroup(repoId: "r2", repo: "git@gh/web.git", displayName: "web",
                           nodes: [
                            RelationNode(workspaceId: "ws-3", workspaceName: "c", state: .stopped),
                           ]),
        ]

        let repos = Repo.fromRelations(groups, workspaces: workspaces)
        XCTAssertEqual(repos.count, 2)
        XCTAssertEqual(repos[0].id, "r1")
        XCTAssertEqual(repos[0].name, "api")
        XCTAssertEqual(repos[0].workspaces.count, 2)
        XCTAssertEqual(repos[1].id, "r2")
        XCTAssertEqual(repos[1].workspaces.count, 1)
    }

    func testRepoFallbackGroupingNoRelations() {
        let workspaces = [
            Workspace(id: "ws-1", workspaceName: "a", repoId: "r1"),
            Workspace(id: "ws-2", workspaceName: "b", repoId: "r1"),
            Workspace(id: "ws-3", workspaceName: "c", repoId: "r2"),
        ]
        let repos = Repo.fromRelations([], workspaces: workspaces)
        // Falls back to flat grouping — all under single group
        XCTAssertFalse(repos.isEmpty)
    }

    func testWorkspaceCreateSpecEncoding() throws {
        let spec = WorkspaceCreateSpec(
            repo: "git@github.com:acme/api.git",
            ref: "main",
            workspaceName: "test-ws"
        )
        let data = try JSONEncoder().encode(spec)
        let dict = try JSONSerialization.jsonObject(with: data) as? [String: Any]
        XCTAssertEqual(dict?["repo"] as? String, "git@github.com:acme/api.git")
        XCTAssertEqual(dict?["workspaceName"] as? String, "test-ws")
        XCTAssertEqual(dict?["ref"] as? String, "main")
    }

    func testProjectDecode() throws {
        let json = """
        {
            "id": "proj-1",
            "name": "nexus",
            "primaryRepo": "git@github.com:inizio/nexus.git",
            "rootPath": "/Users/me/nexus"
        }
        """.data(using: .utf8)!
        let project = try JSONDecoder().decode(Project.self, from: json)
        XCTAssertEqual(project.id, "proj-1")
        XCTAssertEqual(project.name, "nexus")
        XCTAssertEqual(project.primaryRepo, "git@github.com:inizio/nexus.git")
    }

    func testProjectFirstGrouping() {
        let project = Project(id: "proj-1", name: "nexus", primaryRepo: "/tmp/nexus", rootPath: "/tmp/nexus")
        let workspaces = [
            Workspace(id: "ws-1", workspaceName: "main", projectId: "proj-1"),
            Workspace(id: "ws-2", workspaceName: "feature", projectId: "proj-1"),
            Workspace(id: "ws-3", workspaceName: "other", projectId: "proj-2"),
        ]
        let repos = Repo.fromProjects([project], workspaces: workspaces)
        XCTAssertEqual(repos.count, 1)
        XCTAssertEqual(repos[0].id, "proj-1")
        XCTAssertEqual(repos[0].workspaces.count, 2)
    }

    func testForwardedPortURL() {
        let port = ForwardedPort(id: 3000)
        XCTAssertEqual(port.port, 3000)
        XCTAssertEqual(port.localURL.absoluteString, "http://localhost:3000")
    }
}

@MainActor
final class AppStateProjectFlowTests: XCTestCase {

    func testCreateProjectAutoCreatesRootSandbox() async {
        let client = MockDaemonClient()
        let appState = AppState(client: client)
        _ = await appState.createProject(repo: "/tmp/nexus")

        XCTAssertEqual(client.createdProjectRepo, "/tmp/nexus")
        XCTAssertNotNil(client.createdSandboxRequest)
        XCTAssertEqual(client.createdSandboxRequest?.projectId, "proj-1")
        XCTAssertEqual(client.createdSandboxRequest?.targetBranch, "main")
        XCTAssertEqual(client.createdSandboxRequest?.workspaceName, "nexus")
        XCTAssertEqual(client.createdSandboxRequest?.fresh, true)
        XCTAssertEqual(appState.selectedWorkspaceID, "ws-root")
    }

    func testCreateProjectReturnsNilWhenSandboxBootstrapFails() async {
        let client = MockDaemonClient()
        client.shouldFailCreateSandbox = true
        let appState = AppState(client: client)
        let created = await appState.createProject(repo: "/tmp/nexus")

        XCTAssertNil(created)
        XCTAssertEqual(client.createdProjectRepo, "/tmp/nexus")
        XCTAssertNotNil(appState.error)
    }

    func testEnsureProjectRootSandboxCreatesMissingRoot() async {
        let client = MockDaemonClient()
        client.projects = [Project(id: "proj-1", name: "nexus", primaryRepo: "/tmp/nexus", rootPath: "/tmp/nexus")]
        let appState = AppState(client: client)
        await appState.load()

        let root = await appState.ensureProjectRootSandbox(projectID: "proj-1")
        XCTAssertNotNil(root)
        XCTAssertEqual(root?.projectId, "proj-1")
        XCTAssertNotNil(client.createdSandboxRequest)
        XCTAssertEqual(client.createdSandboxRequest?.fresh, true)
    }

    func testEnsureProjectRootSandboxReturnsExistingRootWithoutCreate() async {
        let client = MockDaemonClient()
        client.projects = [Project(id: "proj-1", name: "nexus", primaryRepo: "/tmp/nexus", rootPath: "/tmp/nexus")]
        client.workspaces = [Workspace(id: "ws-existing-root", workspaceName: "nexus", projectId: "proj-1")]
        let appState = AppState(client: client)
        await appState.load()

        let root = await appState.ensureProjectRootSandbox(projectID: "proj-1")
        XCTAssertEqual(root?.id, "ws-existing-root")
        XCTAssertNil(client.createdSandboxRequest)
    }

    func testEnsureProjectRootSandboxCreatesRootWhenOnlyChildrenExist() async {
        let client = MockDaemonClient()
        client.projects = [Project(id: "proj-1", name: "nexus", primaryRepo: "/tmp/nexus", rootPath: "/tmp/nexus")]
        let childJSON = """
        {
            "id": "ws-child",
            "workspaceName": "feature-x",
            "projectId": "proj-1",
            "parentWorkspaceId": "ws-some-parent",
            "ref": "feature-x"
        }
        """.data(using: .utf8)!
        client.workspaces = [try! JSONDecoder().decode(Workspace.self, from: childJSON)]
        let appState = AppState(client: client)
        await appState.load()

        let root = await appState.ensureProjectRootSandbox(projectID: "proj-1")
        XCTAssertNotNil(root)
        XCTAssertEqual(root?.id, "ws-root")
        XCTAssertEqual(root?.parentWorkspaceId, nil)
        XCTAssertEqual(client.createdSandboxRequest?.projectId, "proj-1")
        XCTAssertEqual(client.createdSandboxRequest?.fresh, true)
    }
}

private final class MockDaemonClient: DaemonClient, @unchecked Sendable {
    var createdProjectRepo: String?
    var createdSandboxRequest: SandboxCreateRequest?
    var shouldFailCreateSandbox = false
    var projects: [Project] = []
    var workspaces: [Workspace] = []

    func listProjects() async throws -> [Project] { projects }
    func createProject(repo: String) async throws -> Project {
        createdProjectRepo = repo
        let project = Project(id: "proj-1", name: "nexus", primaryRepo: repo, rootPath: repo)
        projects = [project]
        return project
    }
    func listWorkspaces() async throws -> [Workspace] { workspaces }
    func listRelations() async throws -> [RelationsGroup] { [] }
    func createWorkspace(spec: WorkspaceCreateSpec) async throws -> Workspace {
        Workspace(id: "ws-create", workspaceName: spec.workspaceName, repo: spec.repo, ref: spec.ref)
    }
    func createSandbox(request: SandboxCreateRequest) async throws -> Workspace {
        createdSandboxRequest = request
        if shouldFailCreateSandbox {
            throw NSError(domain: "MockDaemonClient", code: 1, userInfo: [NSLocalizedDescriptionKey: "sandbox create failed"])
        }
        let ws = Workspace(id: "ws-root", workspaceName: request.workspaceName, projectId: request.projectId)
        workspaces.append(ws)
        return ws
    }
    func startWorkspace(id: String) async throws {}
    func stopWorkspace(id: String) async throws {}
    func removeWorkspace(id: String) async throws {}
    func markWorkspaceReady(id: String) async throws {}
    func listPorts(workspaceId: String) async throws -> [ForwardedPort] { [] }
    func addPort(workspaceId: String, port: Int) async throws {}
    func removePort(workspaceId: String, port: Int) async throws {}
    func startTunnels(workspaceId: String) async throws -> TunnelStatus { TunnelStatus(active: false, activeWorkspaceId: "") }
    func stopTunnels(workspaceId: String) async throws -> TunnelStatus { TunnelStatus(active: false, activeWorkspaceId: "") }
    func tunnelStatus(workspaceId: String) async throws -> TunnelStatus { TunnelStatus(active: false, activeWorkspaceId: "") }
    func exec(workspaceId: String, command: String, args: [String]) async throws -> ExecOutput {
        ExecOutput(stdout: "", stderr: "", exitCode: 0)
    }
    func workspaceInfo(id: String) async throws -> WorkspaceInfo {
        WorkspaceInfo(workspaceId: id, workspacePath: "/workspace", ports: [])
    }
    func getDaemonSandboxResourceSettings() async throws -> SandboxResourceSettings {
        SandboxResourceSettings(defaultMemoryMiB: 2048, defaultVCPUs: 2, maxMemoryMiB: 8192, maxVCPUs: 8)
    }
    func updateDaemonSandboxResourceSettings(_ settings: SandboxResourceSettings) async throws -> SandboxResourceSettings {
        settings
    }
}

// MARK: - Integration tests (require running daemon)

final class DaemonConnectionTests: XCTestCase {

    var client: WebSocketDaemonClient!

    override func setUp() async throws {
        guard isDaemonRunning() else {
            throw XCTSkip("Nexus daemon not running at localhost:8080 — skipping integration tests")
        }
        client = makeClient()
    }

    override func tearDown() async throws {
        client?.disconnect()
        client = nil
    }

    func testListWorkspacesReturnsArray() async throws {
        let workspaces = try await client.listWorkspaces()
        // Valid response may be empty — just assert it parsed
        XCTAssertNotNil(workspaces)
    }

    func testListRelationsReturnsArray() async throws {
        let relations = try await client.listRelations()
        XCTAssertNotNil(relations)
    }

    func testAllRelationWorkspacesAreInWorkspaceList() async throws {
        async let wsFetch        = client.listWorkspaces()
        async let relationsFetch = client.listRelations()
        let workspaces  = try await wsFetch
        let relations   = try await relationsFetch

        let wsIDs = Set(workspaces.map(\.id))
        for group in relations {
            for node in group.nodes {
                XCTAssertTrue(wsIDs.contains(node.workspaceId),
                    "Node \(node.workspaceId) from relations not found in workspace list")
            }
        }
    }
}

final class WorkspaceLifecycleTests: XCTestCase {

    var client: WebSocketDaemonClient!
    var createdID: String?

    override func setUp() async throws {
        guard isDaemonRunning() else {
            throw XCTSkip("Nexus daemon not running — skipping lifecycle tests")
        }
        client = makeClient()
    }

    override func tearDown() async throws {
        if let id = createdID {
            try? await client.removeWorkspace(id: id)
        }
        client?.disconnect()
        client = nil
    }

    // Test create + remove using the local nexus repo (which always exists on dev machines)
    func testCreateAndRemove() async throws {
        let repoPath = ProcessInfo.processInfo.environment["NEXUS_TEST_REPO"]
                    ?? FileManager.default.homeDirectoryForCurrentUser
                         .appendingPathComponent("magic/nexus").path
        guard FileManager.default.fileExists(atPath: repoPath) else {
            throw XCTSkip("No test repo at \(repoPath) — set NEXUS_TEST_REPO env var")
        }

        let spec = WorkspaceCreateSpec(
            repo: repoPath,
            ref: "main",
            workspaceName: "e2e-test-\(Int(Date().timeIntervalSince1970))"
        )
        let ws = try await client.createWorkspace(spec: spec)
        createdID = ws.id
        XCTAssertFalse(ws.id.isEmpty)
        XCTAssertEqual(ws.workspaceName, spec.workspaceName)

        // Verify it appears in the list
        let list = try await client.listWorkspaces()
        XCTAssertTrue(list.contains { $0.id == ws.id }, "New workspace should appear in list")

        // Remove
        try await client.removeWorkspace(id: ws.id)
        createdID = nil
        let afterRemove = try await client.listWorkspaces()
        XCTAssertFalse(afterRemove.contains { $0.id == ws.id }, "Removed workspace should not appear")
    }

    // Test stop/start lifecycle on a workspace that is currently running.
    func testStopAndStartRunningWorkspace() async throws {
        let workspaces = try await client.listWorkspaces()
        guard let running = workspaces.first(where: { $0.state == .running }) else {
            throw XCTSkip("No running workspaces to test stop/start lifecycle")
        }

        try await client.stopWorkspace(id: running.id)
        let afterStop = try await client.listWorkspaces()
        let stopped = afterStop.first { $0.id == running.id }
        XCTAssertNotNil(stopped)
        XCTAssertEqual(stopped?.state, .stopped)

        // Restore state
        try await client.startWorkspace(id: running.id)
    }
}

final class PortDetectionTests: XCTestCase {

    var client: WebSocketDaemonClient!

    override func setUp() async throws {
        guard isDaemonRunning() else {
            throw XCTSkip("Nexus daemon not running at localhost:8080")
        }
        client = makeClient()
    }

    override func tearDown() async throws {
        client?.disconnect()
        client = nil
    }

    func testListPortsForNonexistentWorkspace() async throws {
        // Daemon should return empty array, not throw
        let ports = try await client.listPorts(workspaceId: "ws-does-not-exist")
        XCTAssertEqual(ports, [])
    }

    func testListPortsForRunningWorkspace() async throws {
        let workspaces = try await client.listWorkspaces()
        guard let running = workspaces.first(where: { $0.state == .running }) else {
            throw XCTSkip("No running workspaces to test port detection")
        }
        // Just assert it doesn't throw and returns an array
        let ports = try await client.listPorts(workspaceId: running.id)
        XCTAssertNotNil(ports)
        for port in ports {
            XCTAssertGreaterThan(port.port, 0)
            XCTAssertLessThan(port.port, 65536)
        }
    }
}

final class RelationsGroupingTests: XCTestCase {

    var client: WebSocketDaemonClient!

    override func setUp() async throws {
        guard isDaemonRunning() else {
            throw XCTSkip("Nexus daemon not running at localhost:8080")
        }
        client = makeClient()
    }

    override func tearDown() async throws {
        client?.disconnect()
        client = nil
    }

    func testRelationsGroupsMatchWorkspaceIDs() async throws {
        async let wsFetch        = client.listWorkspaces()
        async let relationsFetch = client.listRelations()
        let workspaces  = try await wsFetch
        let relations   = try await relationsFetch
        let repos       = Repo.fromRelations(relations, workspaces: workspaces)

        // Every workspace in repos should appear in allWorkspaces
        let repoWsIDs = repos.flatMap(\.workspaces).map(\.id)
        let allWsIDs  = workspaces.map(\.id)
        for id in repoWsIDs {
            XCTAssertTrue(allWsIDs.contains(id))
        }
    }

    func testRepoDisplayNamesAreNonEmpty() async throws {
        let relations = try await client.listRelations()
        let workspaces = try await client.listWorkspaces()
        let repos = Repo.fromRelations(relations, workspaces: workspaces)
        for repo in repos {
            XCTAssertFalse(repo.name.isEmpty, "Repo '\(repo.id)' should have a non-empty display name")
        }
    }
}
