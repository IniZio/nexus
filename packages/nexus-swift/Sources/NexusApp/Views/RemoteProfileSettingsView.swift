import NexusCore
import SwiftUI

// MARK: - Status dot helper

private func statusColor(for status: ProfileStatus) -> Color {
    switch status {
    case .connected: return .green
    case .unreachable: return .red
    case .authFailed, .tlsError: return .orange
    case .protocolMismatch: return .yellow
    case .unknown: return .gray
    }
}

// MARK: - Profile row

private struct ProfileRow: View {
    let profile: DaemonProfile
    let onSetDefault: () -> Void
    let onEdit: () -> Void
    let onDelete: () -> Void

    var body: some View {
        HStack(spacing: 8) {
            Circle()
                .fill(statusColor(for: profile.lastKnownStatus))
                .frame(width: 8, height: 8)
            VStack(alignment: .leading, spacing: 2) {
                HStack(spacing: 4) {
                    Text(profile.name)
                        .font(.system(size: 12, weight: .medium))
                        .foregroundColor(.primary)
                    Text(profile.mode == .remote ? "remote" : "local")
                        .font(.system(size: 10))
                        .padding(.horizontal, 4)
                        .padding(.vertical, 1)
                        .background(profile.mode == .remote ? Color.blue.opacity(0.15) : Color.secondary.opacity(0.15))
                        .cornerRadius(3)
                    if profile.isDefault {
                        Text("default")
                            .font(.system(size: 10))
                            .padding(.horizontal, 4)
                            .padding(.vertical, 1)
                            .background(Color.green.opacity(0.15))
                            .cornerRadius(3)
                    }
                }
                if profile.mode == .remote {
                    Text("\(profile.scheme.rawValue)://\(profile.host):\(profile.port)")
                        .font(.system(size: 10, design: .monospaced))
                        .foregroundColor(.secondary)
                }
            }
            Spacer()
            if !profile.isDefault {
                Button("Set Default", action: onSetDefault)
                    .buttonStyle(.borderless)
                    .font(.system(size: 11))
            }
            Button(action: onEdit) {
                Image(systemName: "pencil")
                    .font(.system(size: 11))
            }
            .buttonStyle(.borderless)
            Button(action: onDelete) {
                Image(systemName: "trash")
                    .font(.system(size: 11))
                    .foregroundColor(.red)
            }
            .buttonStyle(.borderless)
        }
        .padding(.vertical, 4)
    }
}

// MARK: - Add / Edit sheet

private struct ProfileEditSheet: View {
    @Binding var profile: DaemonProfile
    let isNew: Bool
    let onCancel: () -> Void
    let onSave: (DaemonProfile) -> Void

    @State private var tokenType: String = "env"
    @State private var tokenValue: String = ""

    private var wsWarning: Bool {
        profile.scheme == .ws &&
        profile.host != "localhost" &&
        profile.host != "127.0.0.1" &&
        !profile.host.isEmpty
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text(isNew ? "Add Remote Profile" : "Edit Profile")
                .font(.headline)

            Group {
                LabeledField("Name") {
                    TextField("My Remote Daemon", text: $profile.name)
                        .textFieldStyle(.roundedBorder)
                }
                LabeledField("Host") {
                    TextField("hostname or IP", text: $profile.host)
                        .textFieldStyle(.roundedBorder)
                }
                LabeledField("Port") {
                    HStack {
                        TextField("7777", value: $profile.port, formatter: NumberFormatter())
                            .textFieldStyle(.roundedBorder)
                            .frame(width: 80)
                        Stepper("", value: $profile.port, in: 1...65535)
                            .labelsHidden()
                    }
                }
                LabeledField("Scheme") {
                    Picker("", selection: $profile.scheme) {
                        Text("ws (plain)").tag(ConnectionScheme.ws)
                        Text("wss (TLS)").tag(ConnectionScheme.wss)
                    }
                    .pickerStyle(.segmented)
                    .frame(width: 160)
                    if wsWarning {
                        Text("⚠️ ws (plain) on a non-localhost host is insecure. Use wss or SSH tunnel.")
                            .font(.caption)
                            .foregroundColor(.orange)
                    }
                }
                LabeledField("Token") {
                    VStack(alignment: .leading, spacing: 4) {
                        HStack {
                            Picker("", selection: $tokenType) {
                                Text("Keychain").tag("keychain")
                                Text("Env Var").tag("env")
                                Text("Manual").tag("inline")
                            }
                            .pickerStyle(.menu)
                            .frame(width: 110)
                            if tokenType == "inline" {
                                SecureField("paste token here", text: $tokenValue)
                                    .textFieldStyle(.roundedBorder)
                            } else {
                                TextField(tokenType == "keychain" ? "service name" : "VAR_NAME",
                                          text: $tokenValue)
                                    .textFieldStyle(.roundedBorder)
                            }
                        }
                        if tokenType == "keychain" {
                            Text("Read from macOS Keychain generic password at this service name.")
                                .font(.caption)
                                .foregroundColor(.secondary)
                        } else if tokenType == "env" {
                            Text("Read from this environment variable at connect time.")
                                .font(.caption)
                                .foregroundColor(.secondary)
                        } else {
                            Text("Token stored in app settings. Use for quick setup or testing.")
                                .font(.caption)
                                .foregroundColor(.secondary)
                        }
                    }
                }
                LabeledField("Timeout") {
                    HStack {
                        Text("\(profile.connectTimeoutSec)s")
                            .font(.system(size: 12, design: .monospaced))
                            .frame(width: 32, alignment: .trailing)
                        Stepper("", value: $profile.connectTimeoutSec, in: 1...60)
                            .labelsHidden()
                    }
                }
                Toggle("Set as Default", isOn: $profile.isDefault)
                    .font(.system(size: 12))
            }

            HStack {
                Spacer()
                Button("Cancel", action: onCancel)
                    .keyboardShortcut(.escape)
                Button("Save") {
                    var p = profile
                    p.tokenRef = resolvedTokenRef()
                    onSave(p)
                }
                .keyboardShortcut(.return)
                .buttonStyle(.borderedProminent)
                .disabled(profile.name.isEmpty || (profile.mode == .remote && profile.host.isEmpty) || (tokenType == "inline" && tokenValue.isEmpty))
            }
        }
        .padding(20)
        .frame(minWidth: 380)
        .onAppear {
            switch profile.tokenRef {
            case .keychain(let s): tokenType = "keychain"; tokenValue = s
            case .env(let v): tokenType = "env"; tokenValue = v
            case .inline(let t): tokenType = "inline"; tokenValue = t
            }
        }
    }

    private func resolvedTokenRef() -> TokenRef {
        switch tokenType {
        case "keychain": return .keychain(service: tokenValue)
        case "inline": return .inline(token: tokenValue)
        default: return .env(variable: tokenValue)
        }
    }
}

// MARK: - Labeled field helper

private struct LabeledField<Content: View>: View {
    let label: String
    @ViewBuilder let content: () -> Content
    init(_ label: String, @ViewBuilder content: @escaping () -> Content) {
        self.label = label
        self.content = content
    }
    var body: some View {
        HStack(alignment: .top, spacing: 8) {
            Text(label)
                .font(.system(size: 12))
                .foregroundColor(.secondary)
                .frame(width: 64, alignment: .trailing)
            content()
        }
    }
}

// MARK: - Main view

public struct RemoteProfileSettingsView: View {
    @State private var profiles: [DaemonProfile] = []
    @State private var showSheet = false
    @State private var editingProfile: DaemonProfile = DaemonProfileStore.localDefault()
    @State private var isNewProfile = true

    private let store = DaemonProfileStore()

    public init() {}

    public var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack {
                Text("Daemon Profiles")
                    .font(.system(size: 12, weight: .semibold))
                    .foregroundColor(.secondary)
                Spacer()
                Button {
                    editingProfile = DaemonProfile(name: "", mode: .remote)
                    isNewProfile = true
                    showSheet = true
                } label: {
                    Image(systemName: "plus")
                        .font(.system(size: 11))
                }
                .buttonStyle(.borderless)
            }

            if profiles.isEmpty {
                Text("No profiles configured.")
                    .font(.caption)
                    .foregroundColor(.secondary)
            } else {
                ForEach(profiles) { profile in
                    ProfileRow(
                        profile: profile,
                        onSetDefault: { setDefault(profile) },
                        onEdit: {
                            editingProfile = profile
                            isNewProfile = false
                            showSheet = true
                        },
                        onDelete: { delete(profile) }
                    )
                    if profile.id != profiles.last?.id {
                        Divider().opacity(0.3)
                    }
                }
            }
        }
        .onAppear { profiles = store.load() }
        .sheet(isPresented: $showSheet) {
            ProfileEditSheet(
                profile: $editingProfile,
                isNew: isNewProfile,
                onCancel: { showSheet = false },
                onSave: { saved in
                    saveProfile(saved)
                    showSheet = false
                }
            )
        }
    }

    private func setDefault(_ target: DaemonProfile) {
        profiles = profiles.map { p in
            var copy = p
            copy.isDefault = (p.profileId == target.profileId)
            return copy
        }
        store.save(profiles)
    }

    private func delete(_ target: DaemonProfile) {
        profiles.removeAll { $0.profileId == target.profileId }
        store.save(profiles)
    }

    private func saveProfile(_ profile: DaemonProfile) {
        if isNewProfile {
            if profile.isDefault {
                profiles = profiles.map { p in var c = p; c.isDefault = false; return c }
            }
            profiles.append(profile)
        } else {
            if profile.isDefault {
                profiles = profiles.map { p in var c = p; c.isDefault = false; return c }
            }
            profiles = profiles.map { p in p.profileId == profile.profileId ? profile : p }
        }
        store.save(profiles)
    }
}
