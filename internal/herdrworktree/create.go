package herdrworktree

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/IniZio/nexus/internal/core/driver/registry"
	"github.com/IniZio/nexus/internal/core/driver/sprites"
	"github.com/IniZio/nexus/internal/core/store"
	"github.com/IniZio/nexus/internal/herdrout"
)

// CreateArgs are the inputs of delegate_worktree_create and `nexus herdr worktree-create`.
type CreateArgs struct {
	RepoPath        string   `json:"repo_path"                  jsonschema:"absolute host path to the git repo checkout that is open as a herdr workspace (required)"`
	Branch          string   `json:"branch"                     jsonschema:"branch name for the new linked git worktree (required)"`
	Base            string   `json:"base,omitempty"             jsonschema:"base ref for the new branch (optional; herdr default when empty)"`
	ImageRef        string   `json:"image_ref,omitempty"        jsonschema:"MUST NOT be set — the image comes from the checkout's .nexus/config.yaml (or .nexus/Containerfile); any value here returns an error"`
	MemoryMiB       uint32   `json:"memory_mib,omitempty"       jsonschema:"MUST NOT be set — not supported by the herdr worktree-sandbox path; any value here returns an error"`
	VCPUs           uint32   `json:"vcpus,omitempty"            jsonschema:"MUST NOT be set — not supported by the herdr worktree-sandbox path; any value here returns an error"`
	AllowedBranches []string `json:"allowed_branches,omitempty" jsonschema:"MUST NOT be set — branch policy is derived from the worktree; any value here returns an error"`
	Backend         string   `json:"backend,omitempty"          jsonschema:"optional sandbox backend (e.g. sprites); precedence: this arg > repo .nexus/config.yaml backend > NEXUS_BACKEND > default"`
	Sync            string   `json:"sync,omitempty"             jsonschema:"optional worktree sync mode for the sprites backend: bundle (default; git bundle over exec, no credentials) or push (clone origin in the sprite and push a task branch with a GH_TOKEN projected from the host; allows github.com egress)"`
	Posture         string   `json:"-"`
	BriefPath       string   `json:"brief_path,omitempty"       jsonschema:"absolute host path to a brief file; copied into the worktree as .brief.md and excluded from commits (optional)"`
}

func validateCreate(args CreateArgs) error {
	_, err := validateCreateBackend(args)
	return err
}

// validateCreateBackend runs the create validation and returns the effective backend.
func validateCreateBackend(args CreateArgs) (string, error) {
	if err := validateCreateBase(args); err != nil {
		return "", err
	}
	backend, err := ResolveBackend(args.Backend, args.RepoPath)
	if err != nil {
		return "", err
	}
	mode, err := sprites.NormalizeSyncMode(args.Sync)
	if err != nil {
		return "", err
	}
	if mode == sprites.SyncPush && backend != registry.Sprites {
		return "", fmt.Errorf("sync %q requires the sprites backend", mode)
	}
	return backend, nil
}

func validateCreateBase(args CreateArgs) error {
	if args.RepoPath == "" {
		return fmt.Errorf("repo_path is required")
	}
	if !filepath.IsAbs(args.RepoPath) {
		return fmt.Errorf("repo_path must be an absolute path (got %q)", args.RepoPath)
	}
	if st, err := os.Stat(args.RepoPath); err != nil {
		return fmt.Errorf("repo_path %q: %w", args.RepoPath, err)
	} else if !st.IsDir() {
		return fmt.Errorf("repo_path %q is not a directory", args.RepoPath)
	}
	if args.Branch == "" {
		return fmt.Errorf("branch is required")
	}
	if err := ValidateBriefPath(args.BriefPath); err != nil {
		return err
	}
	if len(args.AllowedBranches) > 0 {
		return fmt.Errorf(
			"delegate_worktree_create: allowed_branches cannot be set by the caller "+
				"(value %v rejected); branch policy is derived from the worktree's "+
				"current branch by the service layer and cannot be widened via this tool",
			args.AllowedBranches,
		)
	}
	if args.ImageRef != "" {
		return fmt.Errorf(
			"delegate_worktree_create: image_ref is not supported (value %q rejected); "+
				"the image is taken from %s (or .nexus/Containerfile) exactly as a herdr worktree sandbox",
			args.ImageRef, filepath.Join(args.RepoPath, ".nexus", "config.yaml"),
		)
	}
	if args.MemoryMiB > 0 || args.VCPUs > 0 {
		return fmt.Errorf(
			"delegate_worktree_create: memory_mib/vcpus are not supported by the herdr worktree-sandbox path "+
				"(memory_mib=%d vcpus=%d rejected); the verb has no flags for them",
			args.MemoryMiB, args.VCPUs,
		)
	}
	return nil
}

const BriefFileName = ".brief.md"

// briefExcludes keep the brief and the report file briefs ask for out of commits.
var briefExcludes = []string{BriefFileName, ".slice-report.md"}

func ValidateBriefPath(p string) error {
	if p == "" {
		return nil
	}
	if !filepath.IsAbs(p) {
		return fmt.Errorf("brief_path must be an absolute path (got %q)", p)
	}
	st, err := os.Stat(p)
	if err != nil {
		return fmt.Errorf("brief_path %q: %w", p, err)
	}
	if !st.Mode().IsRegular() {
		return fmt.Errorf("brief_path %q is not a regular file", p)
	}
	return nil
}

// InstallBrief copies briefPath into worktree as .brief.md and appends
// briefExcludes to info/exclude. Git reads info/exclude only from the common
// dir, so for a linked worktree `rev-parse --git-path info/exclude` resolves
// to the shared file; there is no per-worktree exclude to target.
func InstallBrief(ctx context.Context, r Runners, briefPath, worktree string) error {
	data, err := os.ReadFile(briefPath)
	if err != nil {
		return fmt.Errorf("read brief_path: %w", err)
	}
	if err := os.WriteFile(filepath.Join(worktree, BriefFileName), data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", BriefFileName, err)
	}
	out, err := r.git(ctx, "-C", worktree, "rev-parse", "--git-path", "info/exclude")
	if err != nil {
		return fmt.Errorf("rev-parse --git-path info/exclude: %w\n%s", err, out)
	}
	excl := strings.TrimSpace(out)
	if !filepath.IsAbs(excl) {
		excl = filepath.Join(worktree, excl)
	}
	existing, err := os.ReadFile(excl)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read %s: %w", excl, err)
	}
	have := map[string]bool{}
	for _, l := range strings.Split(string(existing), "\n") {
		have[strings.TrimSpace(l)] = true
	}
	var add strings.Builder
	if len(existing) > 0 && !strings.HasSuffix(string(existing), "\n") {
		add.WriteString("\n")
	}
	n := add.Len()
	for _, e := range briefExcludes {
		if !have[e] {
			add.WriteString(e + "\n")
		}
	}
	if add.Len() == n {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(excl), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(excl, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(add.String())
	return err
}

// PathForRef resolves the host worktree directory bound to ref.
func PathForRef(ctx context.Context, r Runners, ref string) (string, error) {
	herdrBin, err := ResolveHerdrBin()
	if err != nil {
		return "", err
	}
	listOut, err := r.Host(ctx, "herdr", "list")
	if err != nil {
		return "", fmt.Errorf("nexus herdr list: %w\n%s", err, listOut)
	}
	ws, _, _, _, ok := ParseListBindingByRef(listOut, ref)
	if !ok {
		return "", fmt.Errorf("no herdr binding for %q", ref)
	}
	wtOut, err := r.Herdr(ctx, herdrBin, "worktree", "list", "--json")
	if err != nil {
		return "", fmt.Errorf("herdr worktree list: %w\n%s", err, wtOut)
	}
	p := herdrout.WorktreePathByWorkspaceID(wtOut, ws)
	if p == "" {
		return "", fmt.Errorf("worktree path for workspace %s not found", ws)
	}
	return p, nil
}

func findWorkspaceID(workspaceListOut, repoPath string) (string, error) {
	var parsed struct {
		Result struct {
			Workspaces []struct {
				WorkspaceID string `json:"workspace_id"`
				Worktree    struct {
					CheckoutPath string `json:"checkout_path"`
				} `json:"worktree"`
			} `json:"workspaces"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(workspaceListOut), &parsed); err != nil {
		// Output may carry non-JSON lines; scan for the first JSON object line.
		for _, line := range strings.Split(workspaceListOut, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "{") && json.Unmarshal([]byte(line), &parsed) == nil {
				break
			}
		}
	}
	want := filepath.Clean(repoPath)
	for _, ws := range parsed.Result.Workspaces {
		if ws.Worktree.CheckoutPath != "" && filepath.Clean(ws.Worktree.CheckoutPath) == want {
			return ws.WorkspaceID, nil
		}
	}
	for _, ws := range parsed.Result.Workspaces {
		if ws.Worktree.CheckoutPath == "" {
			continue
		}
		cp := filepath.Clean(ws.Worktree.CheckoutPath)
		if strings.HasPrefix(want, cp+string(filepath.Separator)) || strings.HasPrefix(cp, want+string(filepath.Separator)) {
			return ws.WorkspaceID, nil
		}
	}
	if env := os.Getenv("HERDR_WORKSPACE_ID"); env != "" {
		return env, nil
	}
	return "", fmt.Errorf("repo %s is not open as a herdr workspace; open it in herdr first", repoPath)
}

// ParseListBinding finds the `nexus herdr list` line for workspaceID
// and returns its handle and sandbox_id.
func ParseListBinding(out, workspaceID string) (handle, sandboxID string, ok bool) {
	needle := "workspace_id=" + workspaceID
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Split(strings.TrimSpace(line), "\t")
		matched := false
		for _, f := range fields {
			if f == needle {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		for _, f := range fields {
			if v, found := strings.CutPrefix(f, "handle="); found {
				handle = v
			} else if v, found := strings.CutPrefix(f, "sandbox_id="); found {
				sandboxID = v
			}
		}
		return handle, sandboxID, true
	}
	return "", "", false
}

// ParseListBindingByRef matches ref against handle= or a sandbox_id= prefix.
func ParseListBindingByRef(out, ref string) (workspaceID, handle, sandboxID, paneID string, ok bool) {
	if ref == "" {
		return "", "", "", "", false
	}
	for _, line := range strings.Split(out, "\n") {
		var ws, h, sb, pi string
		for _, f := range strings.Split(strings.TrimSpace(line), "\t") {
			if v, found := strings.CutPrefix(f, "workspace_id="); found {
				ws = v
			} else if v, found := strings.CutPrefix(f, "handle="); found {
				h = v
			} else if v, found := strings.CutPrefix(f, "sandbox_id="); found {
				sb = v
			} else if v, found := strings.CutPrefix(f, "pane_id="); found {
				pi = v
			}
		}
		if ws == "" {
			continue
		}
		if h == ref || (sb != "" && strings.HasPrefix(sb, ref)) {
			return ws, h, sb, pi, true
		}
	}
	return "", "", "", "", false
}

// SandboxResult is the outcome of CreateSandbox.
type SandboxResult struct {
	WorkspaceID  string `json:"workspace_id"`
	WorktreePath string `json:"worktree_path"`
	Branch       string `json:"branch"`
	Handle       string `json:"handle"`
	SandboxID    string `json:"sandbox_id"`
	Output       string `json:"output"`
}

// CreateSandbox creates a herdr worktree for args.Branch and binds a nexus sandbox to it.
func CreateSandbox(ctx context.Context, args CreateArgs, r Runners) (SandboxResult, error) {
	backend, err := validateCreateBackend(args)
	if err != nil {
		return SandboxResult{}, err
	}
	herdrBin, err := ResolveHerdrBin()
	if err != nil {
		return SandboxResult{}, fmt.Errorf("delegate_worktree_create: %w; delegate_worktree_create binds the sandbox to a herdr workspace and needs herdr running", err)
	}

	wsListOut, err := r.Herdr(ctx, herdrBin, "workspace", "list")
	if err != nil {
		return SandboxResult{}, fmt.Errorf("delegate_worktree_create: herdr workspace list: %w\n%s", err, wsListOut)
	}
	parent, err := findWorkspaceID(wsListOut, args.RepoPath)
	if err != nil {
		return SandboxResult{}, fmt.Errorf("delegate_worktree_create: %w", err)
	}

	storeRoot, _ := store.DefaultRoot()
	defer herdrout.ClaimWorktree(storeRoot, args.Branch)()

	createArgv := []string{"worktree", "create", "--workspace", parent, "--branch", args.Branch}
	if args.Base != "" {
		createArgv = append(createArgv, "--base", args.Base)
	}
	createArgv = append(createArgv, "--no-focus")
	createOut, err := r.Herdr(ctx, herdrBin, createArgv...)
	if err != nil {
		return SandboxResult{}, fmt.Errorf("delegate_worktree_create: herdr worktree create: %w\n%s", err, createOut)
	}
	ws := herdrout.WorktreeCreateWorkspaceID(createOut)
	if ws == "" {
		return SandboxResult{}, fmt.Errorf("delegate_worktree_create: herdr worktree create: no workspace_id ({\"ws\":...} or result.workspace.workspace_id) in output\n%s", createOut)
	}

	worktreePath := ""
	if args.BriefPath != "" {
		if wtOut, wtErr := r.Herdr(ctx, herdrBin, "worktree", "list", "--workspace", ws, "--json"); wtErr == nil {
			worktreePath = herdrout.WorktreePath(wtOut, args.Branch)
		}
		if worktreePath == "" {
			return SandboxResult{}, fmt.Errorf("delegate_worktree_create: brief_path set but worktree path for branch %q not found (workspace %s was created)", args.Branch, ws)
		}
		if err := InstallBrief(ctx, r, args.BriefPath, worktreePath); err != nil {
			return SandboxResult{}, fmt.Errorf("delegate_worktree_create: brief_path: %w (workspace %s was created)", err, ws)
		}
	}

	bindArgv := []string{"herdr", "worktree-sandbox"}
	if args.Posture != "" {
		bindArgv = append(bindArgv, "--posture", args.Posture)
	}
	if backend != "" {
		bindArgv = append(bindArgv, "--backend", backend)
	}
	if args.Sync == sprites.SyncPush {
		bindArgv = append(bindArgv, "--sync", args.Sync)
	}
	bindOut, err := r.Host(ctx, append(bindArgv, ws)...)
	if err != nil {
		return SandboxResult{}, fmt.Errorf("delegate_worktree_create: nexus herdr worktree-sandbox: %w\n%s", err, bindOut)
	}

	listOut, err := r.Host(ctx, "herdr", "list")
	if err != nil {
		return SandboxResult{}, fmt.Errorf("delegate_worktree_create: nexus herdr list: %w\n%s", err, listOut)
	}
	handle, sandboxID, ok := ParseListBinding(listOut, ws)
	if !ok {
		return SandboxResult{}, fmt.Errorf("delegate_worktree_create: sandbox binding for workspace %s not found after worktree-sandbox\n%s", ws, listOut)
	}

	if worktreePath == "" {
		if wtOut, wtErr := r.Herdr(ctx, herdrBin, "worktree", "list", "--workspace", ws, "--json"); wtErr == nil {
			worktreePath = herdrout.WorktreePath(wtOut, args.Branch)
		}
	}

	return SandboxResult{
		WorkspaceID:  ws,
		WorktreePath: worktreePath,
		Branch:       args.Branch,
		Handle:       handle,
		SandboxID:    sandboxID,
		Output:       createOut + bindOut,
	}, nil
}
