import NexusCore
import SwiftUI
import AppKit

// MARK: - Sheet root

struct NewWorkspaceSheet: View {
    @EnvironmentObject var appState: AppState
    @Environment(\.dismiss) private var dismiss

    @State private var workspaceName = ""
    @State private var localPath     = ""
    @State private var useRemote     = false
    @State private var remoteURL     = ""
    @State private var branch        = "main"
    @State private var isCreating    = false
    @State private var localError: String?

    private var isValid: Bool {
        let name = workspaceName.trimmingCharacters(in: .whitespaces)
        if name.isEmpty { return false }
        if useRemote {
            return !remoteURL.trimmingCharacters(in: .whitespaces).isEmpty
        }
        return !localPath.trimmingCharacters(in: .whitespaces).isEmpty
    }

    var body: some View {
        VStack(spacing: 0) {
            // ── Header ────────────────────────────────────────────
            HStack {
                Text("New Workspace")
                    .font(.system(size: 15, weight: .semibold))
                    .foregroundColor(Theme.label)
                Spacer()
                Button { dismiss() } label: {
                    Image(systemName: "xmark.circle.fill")
                        .font(.system(size: 16))
                        .foregroundColor(Theme.labelTertiary)
                }
                .buttonStyle(.plain)
            }
            .padding(.horizontal, 20)
            .padding(.top, 20)
            .padding(.bottom, 16)

            Divider().overlay(Theme.separator)

            // ── Form ─────────────────────────────────────────────
            VStack(alignment: .leading, spacing: 18) {

                // Workspace name
                FormField(label: "Workspace name", isRequired: true) {
                    NexusTextField(placeholder: "e.g. auth-feature", text: $workspaceName)
                }

                // Source toggle
                Picker("Source", selection: $useRemote) {
                    Text("Local path").tag(false)
                    Text("Remote URL").tag(true)
                }
                .pickerStyle(.segmented)
                .labelsHidden()

                if !useRemote {
                    // ── Local path picker ─────────────────────────
                    FormField(label: "Directory",
                              hint: "Pick an existing project folder on this machine",
                              isRequired: true) {
                        HStack(spacing: 8) {
                            NexusTextField(
                                placeholder: "/Users/you/projects/my-app",
                                text: $localPath
                            )
                            Button {
                                pickDirectory()
                            } label: {
                                Text("Browse…")
                                    .font(.system(size: 12, weight: .medium))
                                    .foregroundColor(Theme.accent)
                                    .padding(.horizontal, 10)
                                    .padding(.vertical, 7)
                                    .background(
                                        RoundedRectangle(cornerRadius: 6)
                                            .stroke(Theme.accent.opacity(0.5), lineWidth: 1)
                                    )
                            }
                            .buttonStyle(.plain)
                        }
                    }
                    // Auto-fill workspace name from folder name
                    .onChange(of: localPath) { _, path in
                        if workspaceName.isEmpty, !path.isEmpty {
                            let folderName = URL(fileURLWithPath: path).lastPathComponent
                            if !folderName.isEmpty { workspaceName = folderName }
                        }
                    }
                } else {
                    // ── Remote URL ────────────────────────────────
                    FormField(label: "Repository URL", isRequired: true) {
                        NexusTextField(
                            placeholder: "git@github.com:org/repo.git",
                            text: $remoteURL
                        )
                    }
                }

                FormField(label: "Branch / ref") {
                    NexusTextField(placeholder: "main", text: $branch)
                }

                if let err = localError ?? appState.error {
                    HStack(spacing: 6) {
                        Image(systemName: "exclamationmark.triangle.fill")
                            .font(.system(size: 11))
                            .foregroundColor(Theme.red)
                        Text(err)
                            .font(.system(size: 11))
                            .foregroundColor(Theme.red)
                    }
                }
            }
            .padding(20)

            Divider().overlay(Theme.separator)

            // ── Footer ────────────────────────────────────────────
            HStack {
                Spacer()
                Button("Cancel") { dismiss() }
                    .keyboardShortcut(.escape, modifiers: [])
                    .buttonStyle(.plain)
                    .font(.system(size: 13))
                    .foregroundColor(Theme.labelSecondary)
                    .padding(.horizontal, 12)

                Button {
                    Task { await create() }
                } label: {
                    HStack(spacing: 6) {
                        if isCreating {
                            ProgressView().scaleEffect(0.7).frame(width: 12, height: 12)
                        }
                        Text(isCreating ? "Creating…" : "Create Workspace")
                    }
                    .font(.system(size: 13, weight: .medium))
                    .foregroundColor(.white)
                    .padding(.horizontal, 14)
                    .padding(.vertical, 7)
                    .background(
                        RoundedRectangle(cornerRadius: 7)
                            .fill(isValid ? Theme.accent : Theme.labelTertiary)
                    )
                }
                .buttonStyle(.plain)
                .disabled(!isValid || isCreating)
            }
            .padding(.horizontal, 20)
            .padding(.vertical, 14)
        }
        .frame(width: 480)
        .background(Theme.bgApp)
    }

    // MARK: - Directory picker

    private func pickDirectory() {
        let panel = NSOpenPanel()
        panel.canChooseFiles         = false
        panel.canChooseDirectories   = true
        panel.allowsMultipleSelection = false
        panel.prompt                 = "Choose"
        panel.message                = "Select a project directory for this workspace"
        if panel.runModal() == .OK {
            localPath = panel.url?.path ?? ""
        }
    }

    // MARK: - Create

    private func create() async {
        localError = nil
        isCreating = true
        defer { isCreating = false }

        let name   = workspaceName.trimmingCharacters(in: .whitespaces)
        let ref    = branch.trimmingCharacters(in: .whitespaces).isEmpty ? "main"
                   : branch.trimmingCharacters(in: .whitespaces)
        let repo   = useRemote
            ? remoteURL.trimmingCharacters(in: .whitespaces)
            : localPath.trimmingCharacters(in: .whitespaces)

        let spec = WorkspaceCreateSpec(repo: repo, ref: ref, workspaceName: name)
        await appState.createWorkspace(spec: spec)

        if appState.error == nil {
            dismiss()
        } else {
            localError = appState.error
            appState.error = nil
        }
    }
}

// MARK: - Helpers

private struct FormField<Content: View>: View {
    let label: String
    var hint: String? = nil
    var isRequired = false
    @ViewBuilder let content: () -> Content

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack(spacing: 4) {
                Text(label)
                    .font(.system(size: 12, weight: .medium))
                    .foregroundColor(Theme.labelSecondary)
                if isRequired {
                    Text("*")
                        .font(.system(size: 12, weight: .medium))
                        .foregroundColor(Theme.accent)
                }
            }
            content()
            if let hint {
                Text(hint)
                    .font(.system(size: 11))
                    .foregroundColor(Theme.labelTertiary)
            }
        }
    }
}

private struct NexusTextField: View {
    let placeholder: String
    @Binding var text: String
    @FocusState private var focused: Bool

    var body: some View {
        TextField(placeholder, text: $text)
            .textFieldStyle(.plain)
            .font(.system(size: 13, design: .monospaced))
            .foregroundColor(Theme.label)
            .padding(.horizontal, 10)
            .padding(.vertical, 8)
            .background(
                RoundedRectangle(cornerRadius: 7)
                    .fill(Theme.bgContent)
                    .overlay(
                        RoundedRectangle(cornerRadius: 7)
                            .stroke(focused ? Theme.accent.opacity(0.5) : Theme.separator, lineWidth: 1)
                    )
            )
            .focused($focused)
    }
}
