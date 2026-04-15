import NexusCore
import SwiftUI
import AppKit

// MARK: - Sheet root

struct NewWorkspaceSheet: View {
    @EnvironmentObject var appState: AppState
    @Environment(\.dismiss) private var dismiss
    let fixedProjectID: String?

    @State private var workspaceName = "main"
    @State private var localPath     = ""
    @State private var useRemote     = false
    @State private var remoteURL     = ""
    @State private var branch        = "main"
    @State private var selectedProjectID = ""
    @State private var createNewProject = false
    @State private var sourceMode: SourceMode = .projectRoot
    @State private var selectedSourceWorkspaceID = ""
    @State private var workspaceNameEdited = false
    @State private var updatingNameFromBranch = false
    @State private var freshSandbox = false
    @State private var isCreating    = false
    @State private var localError: String?

    init(fixedProjectID: String? = nil) {
        self.fixedProjectID = fixedProjectID
    }

    private enum SourceMode: String, CaseIterable, Identifiable {
        case projectRoot
        case specificSandbox
        case fresh
        var id: String { rawValue }
    }

    private var selectedProject: Project? {
        appState.projects.first { $0.id == selectedProjectID }
    }

    private var projectWorkspaces: [Workspace] {
        guard let pid = selectedProject?.id else { return [] }
        return appState.repos.first(where: { $0.id == pid })?.workspaces ?? []
    }

    private var projectRootWorkspace: Workspace? {
        if let root = projectWorkspaces.first(where: { ($0.parentWorkspaceId ?? "").isEmpty }) {
            return root
        }
        return projectWorkspaces.first
    }

    private var isValid: Bool {
        if createNewProject {
            if useRemote {
                return !remoteURL.trimmingCharacters(in: .whitespaces).isEmpty
            }
            return !localPath.trimmingCharacters(in: .whitespaces).isEmpty
        }
        let name = workspaceName.trimmingCharacters(in: .whitespaces)
        if name.isEmpty { return false }
        if sourceMode == .specificSandbox && selectedSourceWorkspaceID.trimmingCharacters(in: .whitespaces).isEmpty {
            return false
        }
        return !selectedProjectID.isEmpty
    }

    var body: some View {
        VStack(spacing: 0) {
            // ── Header ────────────────────────────────────────────
            HStack {
                Text("New Sandbox")
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

                if fixedProjectID == nil {
                    FormField(label: "Project", isRequired: true) {
                        VStack(alignment: .leading, spacing: 8) {
                            Picker("Project", selection: $selectedProjectID) {
                                ForEach(appState.projects) { project in
                                    Text(project.name).tag(project.id)
                                }
                                Text("Create new project…").tag("__new__")
                            }
                            .labelsHidden()
                            .onChange(of: selectedProjectID) { _, value in
                                createNewProject = (value == "__new__")
                                if !createNewProject {
                                    if let root = appState.repos.first(where: { $0.id == value })?.workspaces.first(where: { ($0.parentWorkspaceId ?? "").isEmpty }) {
                                        selectedSourceWorkspaceID = root.id
                                    } else {
                                        selectedSourceWorkspaceID = ""
                                    }
                                }
                            }
                            .accessibilityIdentifier("sandbox_project_picker")
                        }
                    }
                }

                if !createNewProject {
                    // Sandbox name defaults to branch and can be overridden.
                    FormField(label: "Sandbox name", isRequired: true) {
                        NexusTextField(
                            placeholder: "e.g. feature-auth",
                            text: $workspaceName,
                            accessibilityID: "sandbox_name_field"
                        )
                            .onChange(of: workspaceName) { old, new in
                                if old != new && !updatingNameFromBranch {
                                    workspaceNameEdited = true
                                }
                            }
                    }

                    FormField(label: "Target branch") {
                        NexusTextField(
                            placeholder: "main",
                            text: $branch,
                            accessibilityID: "sandbox_branch_field"
                        )
                            .onChange(of: branch) { _, value in
                                guard !workspaceNameEdited else { return }
                                let trimmed = value.trimmingCharacters(in: .whitespaces)
                                updatingNameFromBranch = true
                                workspaceName = trimmed.isEmpty ? "main" : trimmed
                                DispatchQueue.main.async {
                                    updatingNameFromBranch = false
                                }
                            }
                    }

                    FormField(label: "Fork source") {
                        Picker("Fork source", selection: $sourceMode) {
                            Text("Project root").tag(SourceMode.projectRoot)
                            Text("Specific sandbox").tag(SourceMode.specificSandbox)
                            Text("Fresh").tag(SourceMode.fresh)
                        }
                        .labelsHidden()
                        .accessibilityIdentifier("sandbox_fork_source_picker")
                    }

                    if sourceMode == .specificSandbox {
                        FormField(label: "Source sandbox", isRequired: true) {
                            Picker("Source sandbox", selection: $selectedSourceWorkspaceID) {
                                ForEach(projectWorkspaces) { ws in
                                    Text(ws.name).tag(ws.id)
                                }
                            }
                            .labelsHidden()
                            .accessibilityIdentifier("sandbox_source_workspace_picker")
                        }
                    }
                    if sourceMode == .projectRoot && projectRootWorkspace == nil {
                        Text("Project root sandbox does not exist yet. It will be created automatically first.")
                            .font(.system(size: 11))
                            .foregroundColor(Theme.labelTertiary)
                    }
                }

                if createNewProject {
                    // Source toggle for project creation
                    Picker("Repository source", selection: $useRemote) {
                        Text("Local path").tag(false)
                        Text("Remote URL").tag(true)
                    }
                    .pickerStyle(.segmented)
                    .labelsHidden()

                    if !useRemote {
                        FormField(label: "Project directory",
                                  hint: "Pick an existing local repository",
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
                    } else {
                        FormField(label: "Repository URL", isRequired: true) {
                            NexusTextField(
                                placeholder: "git@github.com:org/repo.git",
                                text: $remoteURL
                            )
                        }
                    }
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
                        Text(isCreating ? "Creating…" : (createNewProject ? "Create Project" : "Create Sandbox"))
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
        .frame(width: 520)
        .background(Theme.bgApp)
        .onAppear {
            if let fixed = fixedProjectID, !fixed.isEmpty {
                if fixed == "__new__" {
                    selectedProjectID = "__new__"
                    createNewProject = true
                } else {
                    selectedProjectID = fixed
                    createNewProject = false
                }
            } else if selectedProjectID.isEmpty {
                if let first = appState.projects.first {
                    selectedProjectID = first.id
                    createNewProject = false
                } else {
                    selectedProjectID = "__new__"
                    createNewProject = true
                }
            }
            if selectedSourceWorkspaceID.isEmpty, let root = projectRootWorkspace {
                selectedSourceWorkspaceID = root.id
            }
        }
    }

    // MARK: - Directory picker

    private func pickDirectory() {
        let panel = NSOpenPanel()
        panel.canChooseFiles         = false
        panel.canChooseDirectories   = true
        panel.allowsMultipleSelection = false
        panel.prompt                 = "Choose"
        panel.message                = "Select a project directory for this sandbox"
        if panel.runModal() == .OK {
            localPath = panel.url?.path ?? ""
        }
    }

    // MARK: - Create

    private func create() async {
        localError = nil
        isCreating = true
        defer { isCreating = false }

        var projectID = selectedProjectID
        if createNewProject {
            let repo = useRemote
                ? remoteURL.trimmingCharacters(in: .whitespaces)
                : localPath.trimmingCharacters(in: .whitespaces)
            if let project = await appState.createProject(repo: repo) {
                projectID = project.id
                dismiss()
            } else {
                localError = appState.error
                appState.error = nil
            }
            return
        }
        if projectID == "__new__" || projectID.isEmpty {
            localError = "Select an existing project or create a new one."
            return
        }

        let name = workspaceName.trimmingCharacters(in: .whitespaces)
        let ref = branch.trimmingCharacters(in: .whitespaces).isEmpty ? "main"
            : branch.trimmingCharacters(in: .whitespaces)
        let explicitSourceID: String?
        switch sourceMode {
        case .projectRoot:
            if let root = projectRootWorkspace {
                explicitSourceID = root.id
            } else if let root = await appState.ensureProjectRootSandbox(projectID: projectID) {
                selectedSourceWorkspaceID = root.id
                explicitSourceID = root.id
            } else {
                localError = appState.error ?? "Unable to create project root sandbox."
                appState.error = nil
                return
            }
        case .specificSandbox:
            if selectedSourceWorkspaceID.isEmpty {
                localError = "Select a source sandbox."
                return
            }
            explicitSourceID = selectedSourceWorkspaceID
        case .fresh:
            explicitSourceID = nil
        }
        let useFresh = sourceMode == .fresh || freshSandbox

        let request = SandboxCreateRequest(
            projectId: projectID,
            targetBranch: ref,
            sourceBranch: nil,
            sourceWorkspaceId: explicitSourceID,
            fresh: useFresh,
            workspaceName: name
        )
        await appState.createSandbox(request: request)

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
    var accessibilityID: String? = nil
    @FocusState private var focused: Bool

    var body: some View {
        let field = TextField(placeholder, text: $text)
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
        if let accessibilityID {
            field.accessibilityIdentifier(accessibilityID)
        } else {
            field
        }
    }
}
