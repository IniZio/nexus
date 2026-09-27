//go:build integration

package herdr

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/IniZio/nexus/internal/controller"
	"github.com/IniZio/nexus/internal/herdragent"
	"github.com/IniZio/nexus/internal/testutil/livenexus"
)

func TestBackendContractLive(t *testing.T) {
	h := livenexus.New(t)

	repoPath := initMinimalRepo(t)
	cfg := Config{
		RepoPath:        repoPath,
		Model:           "claude-haiku-4-5",
		HerdrSocketPath: h.SocketPath(),
		NexusBin:        h.NexusBin(),
		ExtraEnv:        h.ExtraEnv(),
		WorktreeDir:     h.WorktreeDir(),
		PermissionMode:  "default",
	}

	b := New(cfg)
	b.agentOpts = []herdragent.Option{
		herdragent.WithSettle(200 * time.Millisecond),
		herdragent.WithUnknownWindow(30 * time.Second),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	sb, ag, err := b.Provision(ctx, repoPath, controller.NewThreadRef("T", "C", "1"), "u:test")
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	t.Logf("provisioned sandbox=%s agent=%s", sb, ag)

	t.Run("unknown agentRef errors", func(t *testing.T) {
		if err := b.Prompt(ctx, "bogus", "hi"); err == nil {
			t.Error("Prompt on unknown agentRef should error")
		}
		if _, err := b.Observe(ctx, "bogus", false); err == nil {
			t.Error("Observe on unknown agentRef should error")
		}
		if err := b.Answer(ctx, "bogus", controller.AgentInput{Text: "x"}); err == nil {
			t.Error("Answer on unknown agentRef should error")
		}
	})

	t.Run("Answer invalid input", func(t *testing.T) {
		if err := b.Answer(ctx, ag, controller.AgentInput{}); err == nil {
			t.Error("Answer with empty input should error")
		}
		if err := b.Answer(ctx, ag, controller.AgentInput{Text: "hi", Key: "1"}); err == nil {
			t.Error("Answer with both Text and Key should error")
		}
	})

	t.Run("Prompt+Observe reaches terminal", func(t *testing.T) {
		deadline := time.Now().Add(3 * time.Minute)
		reachedIdle := false
		var lastStatus herdragent.Status
		for !reachedIdle && time.Now().Before(deadline) {
			st, err := b.Observe(ctx, ag, false)
			if err != nil {
				t.Fatalf("Observe: %v", err)
			}
			lastStatus = st.Status
			switch st.Status {
			case herdragent.StatusIdle:
				reachedIdle = true
			case herdragent.StatusDone:
				t.Logf("agent reached done after unblock; terminal observed")
				return
			case herdragent.StatusBlocked:
				_ = b.Answer(ctx, ag, controller.AgentInput{Key: "Down"})
				time.Sleep(100 * time.Millisecond)
				_ = b.Answer(ctx, ag, controller.AgentInput{Key: "Enter"})
				time.Sleep(500 * time.Millisecond)
			default:
				time.Sleep(500 * time.Millisecond)
			}
		}
		if !reachedIdle {
			pane := captureAgentPaneViaBackend(t, b, ag)
			t.Fatalf("timed out waiting for idle/done; last status: %s\npane tail:\n%s", lastStatus, pane)
		}
		if err := b.Prompt(ctx, ag, "echo hello"); err != nil {
			t.Fatalf("Prompt: %v", err)
		}
		postDeadline := time.Now().Add(90 * time.Second)
		for time.Now().Before(postDeadline) {
			st, err := b.Observe(ctx, ag, false)
			if err != nil {
				t.Fatalf("Observe: %v", err)
			}
			if st.Status == herdragent.StatusDone || st.Status == herdragent.StatusBlocked {
				t.Logf("reached terminal status: %s", st.Status)
				return
			}
			time.Sleep(500 * time.Millisecond)
		}
		pane := captureAgentPaneViaBackend(t, b, ag)
		t.Errorf("timed out waiting for done/blocked\npane tail:\n%s", pane)
	})

	t.Run("ReadAnswer", func(t *testing.T) {
		if _, err := b.ReadAnswer(ctx, ag); err != nil {
			t.Errorf("ReadAnswer: %v", err)
		}
	})

	t.Run("Teardown idempotent", func(t *testing.T) {
		if err := b.Teardown(ctx, sb); err != nil {
			t.Fatalf("Teardown: %v", err)
		}
		if err := b.Teardown(ctx, sb); err != nil {
			t.Fatalf("second Teardown: %v", err)
		}
		if err := b.Teardown(ctx, "sb-unknown"); err == nil {
			t.Error("Teardown for unknown id must return error; no recorded sb- id means sandbox rm would be unsafe")
		}
		if err := b.Prompt(ctx, ag, "hi"); err == nil {
			t.Error("Prompt after Teardown should error")
		}
	})

	prodPS := captureNexusPS(t)
	prodSessions := captureHerdrSessions(t)
	t.Logf("prod nexus ps before harness cleanup: %s", prodPS)
	t.Logf("prod herdr sessions before harness cleanup: %d", strings.Count(prodSessions, "\n")+1)
}

func initMinimalRepo(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/var/tmp", "nexus-ctrl-repo-")
	if err != nil {
		t.Fatalf("create repo dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	for _, args := range [][]string{
		{"git", "-C", dir, "init"},
		{"git", "-C", dir, "config", "user.email", "test@nexus"},
		{"git", "-C", dir, "config", "user.name", "nexus-test"},
	} {
		if out, err := exec.Command(args[0], args[1:]...).CombinedOutput(); err != nil {
			t.Fatalf("git init: %v\n%s", err, out)
		}
	}

	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("nexus ctrl test repo\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	nexusDir := filepath.Join(dir, ".nexus")
	if err := os.MkdirAll(nexusDir, 0o755); err != nil {
		t.Fatalf("mkdir .nexus: %v", err)
	}
	if err := os.WriteFile(filepath.Join(nexusDir, "Containerfile"), []byte("FROM ghcr.io/inizio/nexus-base:latest\n"), 0o644); err != nil {
		t.Fatalf("write .nexus/Containerfile: %v", err)
	}
	for _, args := range [][]string{
		{"git", "-C", dir, "add", "."},
		{"git", "-C", dir, "commit", "-m", "init"},
	} {
		if out, err := exec.Command(args[0], args[1:]...).CombinedOutput(); err != nil {
			t.Fatalf("git commit: %v\n%s", err, out)
		}
	}
	return dir
}

func captureAgentPaneViaBackend(t *testing.T, b *Backend, ag string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := b.ReadAnswer(ctx, ag)
	if err != nil {
		return fmt.Sprintf("(pane read error: %v)", err)
	}
	return out
}

func captureNexusPS(t *testing.T) string {
	t.Helper()
	nexusBin, _ := exec.LookPath("nexus")
	if nexusBin == "" {
		nexusBin = os.Getenv("NEXUS_BIN")
	}
	cmd := exec.Command(nexusBin, "ps")
	out, _ := cmd.CombinedOutput()
	return strings.TrimSpace(string(out))
}

func captureHerdrSessions(t *testing.T) string {
	t.Helper()
	cmd := exec.Command("herdr", "session", "list")
	out, _ := cmd.CombinedOutput()
	return strings.TrimSpace(string(out))
}
