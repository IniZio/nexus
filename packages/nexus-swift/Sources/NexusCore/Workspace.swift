import Foundation
import Combine

// MARK: - Status

/// Maps workspacemgr.WorkspaceState values from the daemon.
public enum WorkspaceStatus: String, Codable, Equatable, Sendable {
    case running   = "running"
    case paused    = "paused"
    case stopped   = "stopped"
    case created   = "created"
    case restored  = "restored"

    public var isActive: Bool { self == .running || self == .restored }

    public var displayName: String {
        switch self {
        case .running:  "Running"
        case .paused:   "Paused"
        case .stopped:  "Stopped"
        case .created:  "Ready"
        case .restored: "Running"
        }
    }
}

// MARK: - Actions

public enum WorkspaceAction: String, Sendable {
    case start, stop, remove, fork, create
}

// MARK: - Workspace
// Field names match workspacemgr.Workspace JSON keys exactly.

public struct Workspace: Identifiable, Codable, Equatable, Sendable {
    public let id: String
    public let workspaceName: String
    public let repo: String
    public let ref: String
    public let targetBranch: String?
    public let currentRef: String?
    public let currentCommit: String?
    public let parentWorkspaceId: String?
    public var state: WorkspaceStatus
    public let rootPath: String
    public let agentProfile: String
    public var repoId: String?
    public var projectId: String?
    /// Daemon runtime backend id, e.g. firecracker, process.
    public let backend: String?
    /// Human-readable summary from daemon (`runtimeLabel` JSON), e.g. backend + isolation.
    public let runtimeLabel: String?
    public var ports: [ForwardedPort]
    public var hasActiveTunnels: Bool

    public var name: String   { workspaceName }
    public var branch: String {
        let candidate = (currentRef?.isEmpty == false ? currentRef : nil)
            ?? (targetBranch?.isEmpty == false ? targetBranch : nil)
            ?? (ref.isEmpty ? nil : ref)
        return candidate ?? "main"
    }
    public var status: WorkspaceStatus { state }
    public var snapshotCount: Int { 0 }

    /// Short tag for lists (sidebar); full `runtimeLabel` is shown in the workspace detail strip when present.
    public var shortRuntimeBadge: String {
        let b = (backend ?? "").trimmingCharacters(in: .whitespacesAndNewlines).lowercased()
        switch b {
        case "firecracker": return "VM"
        case "process": return "proc"
        case "":
            let label = (runtimeLabel ?? "").trimmingCharacters(in: .whitespacesAndNewlines)
            if label.isEmpty { return "" }
            return String(label.prefix(8))
        default: return String(b.prefix(6))
        }
    }

    /// Line for detail UI: prefer daemon-composed label, else backend name.
    public var detailRuntimeLine: String {
        let label = (runtimeLabel ?? "").trimmingCharacters(in: .whitespacesAndNewlines)
        if !label.isEmpty { return label }
        let b = (backend ?? "").trimmingCharacters(in: .whitespacesAndNewlines)
        return b
    }

    enum CodingKeys: String, CodingKey {
        case id, workspaceName, repo, ref, state, rootPath, agentProfile
        case targetBranch, currentRef, currentCommit
        case parentWorkspaceId = "parentWorkspaceId"
        case repoId = "repoId"
        case projectId = "projectId"
        case backend, runtimeLabel
    }

    public init(from decoder: Decoder) throws {
        let c    = try decoder.container(keyedBy: CodingKeys.self)
        id           = try c.decode(String.self, forKey: .id)
        workspaceName = try c.decodeIfPresent(String.self, forKey: .workspaceName) ?? ""
        repo         = try c.decodeIfPresent(String.self, forKey: .repo) ?? ""
        ref          = try c.decodeIfPresent(String.self, forKey: .ref) ?? "main"
        targetBranch = try c.decodeIfPresent(String.self, forKey: .targetBranch)
        currentRef   = try c.decodeIfPresent(String.self, forKey: .currentRef)
        currentCommit = try c.decodeIfPresent(String.self, forKey: .currentCommit)
        parentWorkspaceId = try c.decodeIfPresent(String.self, forKey: .parentWorkspaceId)
        state        = try c.decodeIfPresent(WorkspaceStatus.self, forKey: .state) ?? .stopped
        rootPath     = try c.decodeIfPresent(String.self, forKey: .rootPath) ?? ""
        agentProfile = try c.decodeIfPresent(String.self, forKey: .agentProfile) ?? ""
        repoId       = try c.decodeIfPresent(String.self, forKey: .repoId)
        projectId    = try c.decodeIfPresent(String.self, forKey: .projectId)
        backend      = try c.decodeIfPresent(String.self, forKey: .backend)
        runtimeLabel = try c.decodeIfPresent(String.self, forKey: .runtimeLabel)
        ports        = []
        hasActiveTunnels = false
    }

    public func encode(to encoder: Encoder) throws {
        var c = encoder.container(keyedBy: CodingKeys.self)
        try c.encode(id, forKey: .id)
        try c.encode(workspaceName, forKey: .workspaceName)
        try c.encode(repo, forKey: .repo)
        try c.encode(ref, forKey: .ref)
        try c.encodeIfPresent(targetBranch, forKey: .targetBranch)
        try c.encodeIfPresent(currentRef, forKey: .currentRef)
        try c.encodeIfPresent(currentCommit, forKey: .currentCommit)
        try c.encodeIfPresent(parentWorkspaceId, forKey: .parentWorkspaceId)
        try c.encode(state, forKey: .state)
        try c.encode(rootPath, forKey: .rootPath)
        try c.encode(agentProfile, forKey: .agentProfile)
        try c.encodeIfPresent(repoId, forKey: .repoId)
        try c.encodeIfPresent(projectId, forKey: .projectId)
        try c.encodeIfPresent(backend, forKey: .backend)
        try c.encodeIfPresent(runtimeLabel, forKey: .runtimeLabel)
    }

    public init(id: String, workspaceName: String, repo: String = "",
                ref: String = "main", state: WorkspaceStatus = .stopped,
                rootPath: String = "", agentProfile: String = "default",
                repoId: String? = nil, projectId: String? = nil,
                ports: [ForwardedPort] = [], hasActiveTunnels: Bool = false,
                backend: String? = nil, runtimeLabel: String? = nil) {
        self.id           = id
        self.workspaceName = workspaceName
        self.repo         = repo
        self.ref          = ref
        self.targetBranch = nil
        self.currentRef   = nil
        self.currentCommit = nil
        self.parentWorkspaceId = nil
        self.state        = state
        self.rootPath     = rootPath
        self.agentProfile = agentProfile
        self.repoId       = repoId
        self.projectId    = projectId
        self.backend      = backend
        self.runtimeLabel = runtimeLabel
        self.ports        = ports
        self.hasActiveTunnels = hasActiveTunnels
    }
}

public struct Project: Identifiable, Codable, Equatable, Sendable {
    public let id: String
    public let name: String
    public let primaryRepo: String
    public let rootPath: String?

    enum CodingKeys: String, CodingKey {
        case id, name, primaryRepo, rootPath
    }
}

// MARK: - Workspace create spec

public struct WorkspaceCreateSpec: Encodable, Sendable {
    public let repo: String
    public let ref: String
    public let workspaceName: String
    public let agentProfile: String
    public let backend: String

    public init(repo: String, ref: String, workspaceName: String,
                agentProfile: String = "default", backend: String = "") {
        self.repo          = repo
        self.ref           = ref
        self.workspaceName = workspaceName
        self.agentProfile  = agentProfile
        self.backend       = backend
    }
}

// MARK: - Repo

public struct Repo: Identifiable, Sendable {
    public let id: String
    public let name: String
    public let remoteURL: String
    public var workspaces: [Workspace]

    public init(id: String, name: String, remoteURL: String, workspaces: [Workspace]) {
        self.id        = id
        self.name      = name
        self.remoteURL = remoteURL
        self.workspaces = workspaces
    }

    public static func fromRelations(_ groups: [RelationsGroup], workspaces: [Workspace]) -> [Repo] {
        guard !groups.isEmpty else {
            return workspaces.isEmpty ? [] : [Repo(id: "nexus", name: "nexus", remoteURL: "", workspaces: workspaces)]
        }
        return groups.map { group in
            let wsInGroup = workspaces.filter { ws in
                group.nodes.contains { $0.workspaceId == ws.id }
            }
            let displayName = group.displayName.isEmpty
                ? (group.repo.split(separator: "/").last.map(String.init) ?? group.repo)
                : group.displayName
            return Repo(id: group.repoId, name: displayName,
                        remoteURL: group.repo, workspaces: wsInGroup)
        }
    }

    public static func fromProjects(_ projects: [Project], workspaces: [Workspace]) -> [Repo] {
        guard !projects.isEmpty else { return [] }
        return projects.map { project in
            let wsInProject = workspaces.filter { $0.projectId == project.id }
            return Repo(
                id: project.id,
                name: project.name,
                remoteURL: project.primaryRepo,
                workspaces: wsInProject
            )
        }
    }

    public static func grouping(_ workspaces: [Workspace]) -> [Repo] {
        var map: [String: [Workspace]] = [:]
        var order: [String] = []
        for ws in workspaces {
            let key = ws.repoId ?? "nexus"
            if map[key] == nil { order.append(key) }
            map[key, default: []].append(ws)
        }
        return order.map { key in
            let ws = map[key] ?? []
            let name = ws.first.flatMap { w in
                w.repo.split(separator: "/").last.map(String.init)
            } ?? key
            return Repo(id: key, name: name, remoteURL: ws.first?.repo ?? "", workspaces: ws)
        }
    }
}

// MARK: - Relations (wire types for workspace.relations.list)

public struct RelationsGroup: Decodable, Sendable {
    public let repoId: String
    public let repo: String
    public let displayName: String
    public let nodes: [RelationNode]
}

public struct RelationNode: Decodable, Sendable {
    public let workspaceId: String
    public let workspaceName: String
    public let state: WorkspaceStatus
}

// MARK: - Forwarded port

public struct ForwardedPort: Identifiable, Codable, Equatable, Sendable {
    public let id: Int
    public let remotePort: Int
    public let preferred: Bool
    public let tunneled: Bool
    public let process: String?
    public var port: Int { id }
    public var localURL: URL {
        var components = URLComponents()
        components.scheme = "http"
        components.host = "localhost"
        components.port = id
        return components.url ?? URL(fileURLWithPath: "/")
    }

    public init(id: Int, remotePort: Int? = nil, preferred: Bool = false, tunneled: Bool = false, process: String? = nil) {
        self.id = id
        self.remotePort = remotePort ?? id
        self.preferred = preferred
        self.tunneled = tunneled
        self.process = process
    }
}
