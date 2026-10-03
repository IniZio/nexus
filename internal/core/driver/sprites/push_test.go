package sprites

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IniZio/nexus/internal/core/domain"
)

func TestNormalizeSyncMode(t *testing.T) {
	for in, want := range map[string]string{"": SyncBundle, "bundle": SyncBundle, "push": SyncPush} {
		if got, err := NormalizeSyncMode(in); err != nil || got != want {
			t.Fatalf("%q = %q, %v", in, got, err)
		}
	}
	if _, err := NormalizeSyncMode("rsync"); err == nil {
		t.Fatal("unknown mode accepted")
	}
}

func TestProvisionSyncModePersistence(t *testing.T) {
	for in, want := range map[string]string{"": SyncBundle, "bundle": SyncBundle, "push": SyncPush} {
		f := &fakeAPI{}
		d, _ := newTestDriver(t, f)
		id := domain.NewSandboxID()
		if err := d.Provision(context.Background(), id, Spec{Sync: in}); err != nil {
			t.Fatal(err)
		}
		if got, err := d.SyncMode(id); err != nil || got != want {
			t.Fatalf("%q: SyncMode = %q, %v", in, got, err)
		}
		sp, _ := d.Spec(id)
		hasGH := len(sp.SecretNames) == 1 && sp.SecretNames[0] == SecretGitHub
		if hasGH != (want == SyncPush) {
			t.Fatalf("%q: secrets = %v", in, sp.SecretNames)
		}
		var hosts []string
		for _, r := range f.policies[0].Rules {
			hosts = append(hosts, r.Domain)
		}
		if got := strings.Contains(strings.Join(hosts, ","), "api.github.com"); got != (want == SyncPush) {
			t.Fatalf("%q: github egress = %v (%v)", in, got, hosts)
		}
	}
	if err := (&Driver{}).Provision(context.Background(), domain.NewSandboxID(), Spec{Sync: "x"}); err == nil {
		t.Fatal("bad sync mode accepted")
	}
}

func TestPreparePushBranchAndUnpushed(t *testing.T) {
	const secret = "ghp_SECRETTOKENVALUE"
	root := t.TempDir()
	origin := filepath.Join(root, "origin.git")
	seed := filepath.Join(root, "seed")
	guest := filepath.Join(root, "guest")
	git(t, root, "init", "-q", "--bare", "-b", "main", origin)
	if err := os.MkdirAll(seed, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, seed, "init", "-q", "-b", "main")
	commit(t, seed, "a")
	git(t, seed, "push", "-q", origin, "main")
	git(t, root, "clone", "-q", origin, guest)

	api := &localExecAPI{}
	d, err := New(Config{StateDir: t.TempDir(), API: api, EnvResolver: func(_ context.Context, names []string) (map[string]string, error) {
		if len(names) != 1 || names[0] != SecretGitHub {
			t.Fatalf("names = %v", names)
		}
		return map[string]string{SecretGitHub: secret}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	id := domain.NewSandboxID()
	ctx := context.Background()
	if err := d.PreparePushBranch(ctx, id, guest, "task/x"); err != nil {
		t.Fatal(err)
	}
	if got := git(t, guest, "rev-parse", "--abbrev-ref", "HEAD"); got != "task/x" {
		t.Fatalf("branch = %s", got)
	}
	cfg, err := os.ReadFile(filepath.Join(guest, ".git", "config"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(cfg), secret) {
		t.Fatal("token written to git config")
	}
	if !strings.Contains(string(cfg), "$GH_TOKEN") {
		t.Fatalf("credential helper not env-based:\n%s", cfg)
	}
	if un, _ := d.GuestUnpushed(ctx, id, guest); !strings.Contains(un, "no pushed upstream") {
		t.Fatalf("unpushed before first push = %q", un)
	}
	commit(t, guest, "w")
	git(t, guest, "push", "-q", "origin", "task/x")
	if un, err := d.GuestUnpushed(ctx, id, guest); err != nil || un != "" {
		t.Fatalf("after push = %q, %v", un, err)
	}
	commit(t, guest, "w2")
	if un, _ := d.GuestUnpushed(ctx, id, guest); !strings.Contains(un, "1 commit") {
		t.Fatalf("after new commit = %q", un)
	}
	// existing remote branch is checked out, not recreated
	guest2 := filepath.Join(root, "guest2")
	git(t, root, "clone", "-q", origin, guest2)
	if err := d.PreparePushBranch(ctx, id, guest2, "task/x"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(guest2, "w")); err != nil {
		t.Fatalf("existing remote branch not checked out: %v", err)
	}
	for _, r := range api.reqs {
		if strings.Contains(strings.Join(r.Argv, " "), secret) {
			t.Fatal("token on argv")
		}
	}
	if err := d.PreparePushBranch(ctx, id, guest, "-bad"); err == nil {
		t.Fatal("option-like branch accepted")
	}
}
