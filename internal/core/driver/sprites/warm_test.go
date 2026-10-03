package sprites

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	sdk "github.com/superfly/sprites-go"

	"github.com/IniZio/nexus/internal/core/domain"
)

func hasHost(p *sdk.NetworkPolicy, h string) bool {
	for _, r := range p.Rules {
		if r.Domain == h && r.Action == "allow" {
			return true
		}
	}
	return false
}

func seedRepo(t *testing.T, goMod bool) string {
	t.Helper()
	host := filepath.Join(t.TempDir(), "host")
	if err := os.MkdirAll(host, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, host, "init", "-q", "-b", "main")
	if goMod {
		if err := os.WriteFile(filepath.Join(host, "go.mod"), []byte("module x\n\ngo 1.26.0\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		git(t, host, "add", "go.mod")
		git(t, host, "commit", "-q", "-m", "m")
	} else {
		commit(t, host, "a")
	}
	return host
}

func TestWarmPolicySequence(t *testing.T) {
	f := &fakeAPI{}
	d, _ := newTestDriver(t, f)
	id := domain.NewSandboxID()
	if err := d.writeSpec(id, Spec{}); err != nil {
		t.Fatal(err)
	}
	if err := d.SeedWorktree(context.Background(), id, seedRepo(t, true), "HEAD", "/work"); err != nil {
		t.Fatal(err)
	}
	if len(f.policies) != 2 {
		t.Fatalf("policies = %d, want 2", len(f.policies))
	}
	if !hasHost(f.policies[0], GoWarmHost) {
		t.Fatal("window policy lacks GCS")
	}
	if hasHost(f.policies[1], GoWarmHost) || !hasHost(f.policies[1], "proxy.golang.org") {
		t.Fatalf("tight policy wrong: %+v", f.policies[1])
	}
	// exec order: seed, go version, go mod download — policy open before, tighten after
	want := []string{"exec", "policy", "exec", "exec", "policy"}
	if len(f.calls) != len(want) {
		t.Fatalf("calls %v, want %v", f.calls, want)
	}
	for i := range want {
		if f.calls[i] != want[i] {
			t.Fatalf("calls %v, want %v", f.calls, want)
		}
	}
	if f.execs[1].Env["GOTOOLCHAIN"] != "auto" || f.execs[1].Dir != "/work" {
		t.Fatalf("warm exec: %+v", f.execs[1])
	}
}

func TestWarmTightensOnWarmFailure(t *testing.T) {
	f := &fakeAPI{execErr: errors.New("boom")}
	d, _ := newTestDriver(t, f)
	id := domain.NewSandboxID()
	if err := d.writeSpec(id, Spec{}); err != nil {
		t.Fatal(err)
	}
	if err := d.warmGo(context.Background(), id, "/work"); err != nil {
		t.Fatalf("warm failure must not fail: %v", err)
	}
	if len(f.policies) != 2 || hasHost(f.policies[1], GoWarmHost) {
		t.Fatalf("not tightened: %+v", f.policies)
	}
}

func TestWarmTightensOnCancel(t *testing.T) {
	f := &fakeAPI{}
	d, _ := newTestDriver(t, f)
	id := domain.NewSandboxID()
	if err := d.writeSpec(id, Spec{}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	f.policyErr = func(n int) error {
		if n == 1 {
			cancel()
		}
		return nil
	}
	_ = d.warmGo(ctx, id, "/work")
	if len(f.policies) != 2 || hasHost(f.policies[1], GoWarmHost) {
		t.Fatalf("not tightened after cancel: %+v", f.policies)
	}
}

func TestWarmTightenFailureFailsClosed(t *testing.T) {
	f := &fakeAPI{policyErr: func(n int) error {
		if n == 2 {
			return errors.New("api down")
		}
		return nil
	}}
	d, _ := newTestDriver(t, f)
	id := domain.NewSandboxID()
	if err := d.writeSpec(id, Spec{}); err != nil {
		t.Fatal(err)
	}
	if err := d.warmGo(context.Background(), id, "/work"); err == nil {
		t.Fatal("tighten failure must error")
	}
}

func TestWarmSkippedWithoutGoMod(t *testing.T) {
	f := &fakeAPI{}
	d, _ := newTestDriver(t, f)
	id := domain.NewSandboxID()
	if err := d.SeedWorktree(context.Background(), id, seedRepo(t, false), "HEAD", "/work"); err != nil {
		t.Fatal(err)
	}
	if len(f.policies) != 0 {
		t.Fatalf("no go.mod must not touch policy: %d", len(f.policies))
	}
}
