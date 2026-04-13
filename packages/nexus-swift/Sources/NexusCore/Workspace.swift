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
    case start, stop, pause, resume, remove, fork, create
}

// MARK: - Workspace
// Field names match workspacemgr.Workspace JSON keys exactly.

public struct Workspace: Identifiable, Codable, Equatable, Sendable {
    public let id: String
    public let workspaceName: String
    public let repo: String
    public let ref: String
    public var state: WorkspaceStatus
    public let rootPath: String
    public let agentProfile: String
    public var repoId: String?
    public var projectId: String?
    public var ports: [ForwardedPort]

    public var name: String   { workspaceName }
    public var branch: String { ref.isEmpty ? "main" : ref }
    public var status: WorkspaceStatus { state }
    public var snapshotCount: Int { 0 }

    enum CodingKeys: String, CodingKey {
        case id, workspaceName, repo, ref, state, rootPath, agentProfile
        case repoId = "repoId"
        case projectId = "projectId"
    }

    public init(from decoder: Decoder) throws {
        let c    = try decoder.container(keyedBy: CodingKeys.self)
        id           = try c.decode(String.self, forKey: .id)
        workspaceName = try c.decodeIfPresent(String.self, forKey: .workspaceName) ?? ""
        repo         = try c.decodeIfPresent(String.self, forKey: .repo) ?? ""
        ref          = try c.decodeIfPresent(String.self, forKey: .ref) ?? "main"
        state        = try c.decodeIfPresent(WorkspaceStatus.self, forKey: .state) ?? .stopped
        rootPath     = try c.decodeIfPresent(String.self, forKey: .rootPath) ?? ""
        agentProfile = try c.decodeIfPresent(String.self, forKey: .agentProfile) ?? ""
        repoId       = try c.decodeIfPresent(String.self, forKey: .repoId)
        projectId    = try c.decodeIfPresent(String.self, forKey: .projectId)
        ports        = []
    }

    public func encode(to encoder: Encoder) throws {
        var c = encoder.container(keyedBy: CodingKeys.self)
        try c.encode(id, forKey: .id)
        try c.encode(workspaceName, forKey: .workspaceName)
        try c.encode(repo, forKey: .repo)
        try c.encode(ref, forKey: .ref)
        try c.encode(state, forKey: .state)
        try c.encode(rootPath, forKey: .rootPath)
        try c.encode(agentProfile, forKey: .agentProfile)
        try c.encodeIfPresent(repoId, forKey: .repoId)
        try c.encodeIfPresent(projectId, forKey: .projectId)
    }

    public init(id: String, workspaceName: String, repo: String = "",
                ref: String = "main", state: WorkspaceStatus = .stopped,
                rootPath: String = "", agentProfile: String = "default",
                repoId: String? = nil, projectId: String? = nil,
                ports: [ForwardedPort] = []) {
        self.id           = id
        self.workspaceName = workspaceName
        self.repo         = repo
        self.ref          = ref
        self.state        = state
        self.rootPath     = rootPath
        self.agentProfile = agentProfile
        self.repoId       = repoId
        self.projectId    = projectId
        self.ports        = ports
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
    public var port: Int { id }
    public var localURL: URL { URL(string: "http://localhost:\(id)")! }

    public init(id: Int) { self.id = id }
}
