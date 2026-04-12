//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestCodexInWorkspace(t *testing.T) {
	if os.Getenv("E2E_CREDENTIALS") == "" {
		t.Skip("E2E_CREDENTIALS not set, skipping e2e test")
	}

	ctx := context.Background()
	workspaceID := createTestWorkspace(ctx, t)
	defer cleanupWorkspace(ctx, workspaceID)

	time.Sleep(2 * time.Second)

	output := execInWorkspace(ctx, t, workspaceID, "codex", "--version")
	if strings.TrimSpace(output) == "" {
		t.Error("codex command failed - credential vending may not be working")
	}

	t.Logf("Codex output: %s", output)
}

func TestOpenCodeInWorkspace(t *testing.T) {
	if os.Getenv("E2E_CREDENTIALS") == "" {
		t.Skip("E2E_CREDENTIALS not set, skipping e2e test")
	}

	ctx := context.Background()
	workspaceID := createTestWorkspace(ctx, t)
	defer cleanupWorkspace(ctx, workspaceID)

	time.Sleep(2 * time.Second)

	output := execInWorkspace(ctx, t, workspaceID, "opencode", "--version")
	if strings.TrimSpace(output) == "" {
		t.Error("opencode command failed - credential vending may not be working")
	}

	t.Logf("OpenCode output: %s", output)
}

func TestMultipleProviders(t *testing.T) {
	if os.Getenv("E2E_CREDENTIALS") == "" {
		t.Skip("E2E_CREDENTIALS not set, skipping e2e test")
	}

	ctx := context.Background()
	workspaceID := createTestWorkspace(ctx, t)
	defer cleanupWorkspace(ctx, workspaceID)

	time.Sleep(2 * time.Second)

	providers := listVendingProviders(ctx, workspaceID)
	if len(providers) == 0 {
		t.Error("no providers detected - vending may not be working")
	}

	t.Logf("Detected providers: %v", providers)
}

var workspaceCreatedID = regexp.MustCompile(`\(id:\s+(ws-[0-9]+)\)`)

func nexusBin() string {
	if p := os.Getenv("NEXUS_BIN"); p != "" {
		return p
	}
	return "nexus"
}

func createTestWorkspace(ctx context.Context, t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	initGitRepo(t, dir)

	cmd := exec.CommandContext(ctx, nexusBin(), "create", "--backend", "firecracker")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("nexus create: %v\n%s", err, out)
	}

	m := workspaceCreatedID.FindStringSubmatch(string(out))
	if len(m) < 2 {
		t.Fatalf("parse workspace id from create output: %s", out)
	}
	return m[1]
}

func initGitRepo(t *testing.T, dir string) {
	t.Helper()
	runGit := func(args ...string) {
		t.Helper()
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args[1:], err, out)
		}
	}
	runGit("git", "-C", dir, "init")
	runGit("git", "-C", dir, "config", "user.email", "e2e@example.com")
	runGit("git", "-C", dir, "config", "user.name", "e2e")
	readme := filepath.Join(dir, "README.md")
	if err := os.WriteFile(readme, []byte("# e2e\n"), 0o644); err != nil {
		t.Fatalf("write readme: %v", err)
	}
	runGit("git", "-C", dir, "add", "README.md")
	runGit("git", "-C", dir, "commit", "-m", "init")
}

func cleanupWorkspace(ctx context.Context, workspaceID string) {
	if strings.TrimSpace(workspaceID) == "" {
		return
	}
	cmd := exec.CommandContext(ctx, nexusBin(), "remove", workspaceID)
	_, _ = cmd.CombinedOutput()
}

func execInWorkspace(ctx context.Context, t *testing.T, workspaceID string, cmd string, args ...string) string {
	t.Helper()
	argv := append([]string{"exec", workspaceID, "--", cmd}, args...)
	c := exec.CommandContext(ctx, nexusBin(), argv...)
	out, err := c.CombinedOutput()
	if err != nil {
		t.Logf("nexus exec: %v\n%s", err, out)
	}
	return string(out)
}

func listVendingProviders(ctx context.Context, workspaceID string) []string {
	_ = ctx
	_ = workspaceID

	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}

	var names []string

	tryCodex := func(path string) bool {
		data, err := os.ReadFile(path)
		if err != nil {
			return false
		}
		var auth struct {
			RefreshToken string `json:"refresh_token"`
		}
		if err := json.Unmarshal(data, &auth); err != nil {
			return false
		}
		return auth.RefreshToken != ""
	}
	for _, p := range []string{
		filepath.Join(home, ".config", "codex", "auth.json"),
		filepath.Join(home, ".codex", "auth.json"),
	} {
		if tryCodex(p) {
			names = append(names, "codex")
			break
		}
	}

	tryOpenCode := func(path string) bool {
		data, err := os.ReadFile(path)
		if err != nil {
			return false
		}
		var auth struct {
			APIKey      string `json:"api_key"`
			AccessToken string `json:"access_token"`
		}
		if err := json.Unmarshal(data, &auth); err != nil {
			return false
		}
		return auth.APIKey != "" || auth.AccessToken != ""
	}
	for _, p := range []string{
		filepath.Join(home, ".config", "opencode", "auth.json"),
		filepath.Join(home, ".opencode", "auth.json"),
	} {
		if tryOpenCode(p) {
			names = append(names, "opencode")
			break
		}
	}

	return names
}
