import Foundation

public enum ProfileMode: String, Codable, Equatable {
    case local
    case remote
}

public enum ConnectionScheme: String, Codable, Equatable {
    case ws
    case wss
}

public enum TokenRef: Codable, Equatable {
    case keychain(service: String)
    case env(variable: String)
    case inline(token: String)

    private enum CodingKeys: String, CodingKey {
        case type, value
    }

    public init(from decoder: Decoder) throws {
        let container = try decoder.container(keyedBy: CodingKeys.self)
        let type_ = try container.decode(String.self, forKey: .type)
        let value = try container.decode(String.self, forKey: .value)
        switch type_ {
        case "keychain": self = .keychain(service: value)
        case "env": self = .env(variable: value)
        case "inline": self = .inline(token: value)
        default:
            throw DecodingError.dataCorruptedError(
                forKey: .type,
                in: container,
                debugDescription: "Unknown TokenRef type: \(type_)"
            )
        }
    }

    public func encode(to encoder: Encoder) throws {
        var container = encoder.container(keyedBy: CodingKeys.self)
        switch self {
        case .keychain(let service):
            try container.encode("keychain", forKey: .type)
            try container.encode(service, forKey: .value)
        case .env(let variable):
            try container.encode("env", forKey: .type)
            try container.encode(variable, forKey: .value)
        case .inline(let token):
            try container.encode("inline", forKey: .type)
            try container.encode(token, forKey: .value)
        }
    }
}

public enum ProfileStatus: String, Codable, Equatable {
    case unknown
    case connected
    case unreachable
    case authFailed
    case tlsError
    case protocolMismatch
}

public struct DaemonProfile: Codable, Equatable, Identifiable {
    public var id: String { profileId }
    public var profileId: String
    public var name: String
    public var mode: ProfileMode
    public var host: String
    public var port: Int
    public var scheme: ConnectionScheme
    public var tokenRef: TokenRef
    public var connectTimeoutSec: Int
    public var isDefault: Bool
    public var lastKnownStatus: ProfileStatus

    public init(
        profileId: String = UUID().uuidString,
        name: String,
        mode: ProfileMode,
        host: String = "",
        port: Int = 7777,
        scheme: ConnectionScheme = .ws,
        tokenRef: TokenRef = .env(variable: "NEXUS_TOKEN"),
        connectTimeoutSec: Int = 10,
        isDefault: Bool = false,
        lastKnownStatus: ProfileStatus = .unknown
    ) {
        self.profileId = profileId
        self.name = name
        self.mode = mode
        self.host = host
        self.port = port
        self.scheme = scheme
        self.tokenRef = tokenRef
        self.connectTimeoutSec = connectTimeoutSec
        self.isDefault = isDefault
        self.lastKnownStatus = lastKnownStatus
    }
}

public final class DaemonProfileStore {
    private let defaults: UserDefaults
    private let key = "nexus.daemonProfiles"
    private let encoder = JSONEncoder()
    private let decoder = JSONDecoder()

    public init(defaults: UserDefaults = .standard) {
        self.defaults = defaults
    }

    public func load() -> [DaemonProfile] {
        guard let data = defaults.data(forKey: key) else { return [] }
        return (try? decoder.decode([DaemonProfile].self, from: data)) ?? []
    }

    public func save(_ profiles: [DaemonProfile]) {
        guard let data = try? encoder.encode(profiles) else { return }
        defaults.set(data, forKey: key)
    }

    public func defaultProfile() -> DaemonProfile? {
        load().first { $0.isDefault }
    }

    public static func localDefault() -> DaemonProfile {
        DaemonProfile(
            profileId: "local-default",
            name: "Local",
            mode: .local,
            host: "",
            port: 7777,
            scheme: .ws,
            tokenRef: .env(variable: "NEXUS_TOKEN"),
            connectTimeoutSec: 10,
            isDefault: true,
            lastKnownStatus: .unknown
        )
    }
}
