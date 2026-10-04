package sprites

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/driver"
	"github.com/IniZio/nexus/internal/core/driver/sprites/broker"
)

// lcDriver wires a broker Manager whose launcher records "ensure" into the
// same ordered log the fake API uses.
func lcDriver(t *testing.T, f *fakeAPI, spawnErr error) (*Driver, *broker.Manager, *fakeLauncher, domain.SandboxID) {
	t.Helper()
	dir := t.TempDir()
	id := domain.NewSandboxID()
	m := &broker.Manager{StateDir: dir, ReadyTimeout: 200 * time.Millisecond, StopGrace: time.Second}
	fl := &fakeLauncher{onSpawn: func(string) error {
		f.mu.Lock()
		f.rec("ensure")
		f.mu.Unlock()
		if spawnErr != nil {
			return spawnErr
		}
		return broker.WriteState(dir, id.String(), liveBrokerState(t))
	}}
	m.Launcher = fl
	d, err := New(Config{API: f, StateDir: dir, Broker: m})
	if err != nil {
		t.Fatal(err)
	}
	return d, m, fl, id
}

func idx(calls []string, s string) int {
	for i, c := range calls {
		if c == s {
			return i
		}
	}
	return -1
}

func TestLifecycleBrokerOrder(t *testing.T) {
	f := &fakeAPI{}
	d, m, fl, id := lcDriver(t, f, nil)
	ctx := context.Background()
	spec := Spec{OpenEgress: true, CredMode: CredModeBroker, Sync: SyncPush, Repo: "git@github.com:a/b.git", SecretNames: []string{SecretGitHub}}
	if err := d.Provision(ctx, id, spec); err != nil {
		t.Fatal(err)
	}
	e, c := idx(f.calls, "ensure"), idx(f.calls, "exec")
	if e < 0 || c < 0 || e > c || idx(f.calls, "create") > e {
		t.Fatalf("calls = %v", f.calls)
	}
	if !m.Alive(id.String()) {
		t.Fatal("broker not alive after Provision")
	}
	// Start: Ensure is a no-op while alive, spawns when dead.
	if err := broker.RemoveState(m.StateDir, id.String()); err != nil {
		t.Fatal(err)
	}
	f.exists = true
	n := fl.calls
	if _, err := d.Start(ctx, driver.StartRequest{SandboxID: id}); err != nil {
		t.Fatal(err)
	}
	if fl.calls != n+1 || !m.Alive(id.String()) {
		t.Fatalf("Start did not ensure: calls %d->%d", n, fl.calls)
	}
	if err := d.Stop(ctx, id); err != nil {
		t.Fatal(err)
	}
	if m.Alive(id.String()) {
		t.Fatal("Stop left broker alive")
	}
	if _, err := broker.ReadState(m.StateDir, id.String()); err == nil {
		t.Fatal("Stop left broker.json")
	}
	if _, err := d.Start(ctx, driver.StartRequest{SandboxID: id}); err != nil {
		t.Fatal(err)
	}
	if err := d.Deprovision(ctx, id); err != nil {
		t.Fatal(err)
	}
	if m.Alive(id.String()) {
		t.Fatal("Deprovision left broker alive")
	}
	if len(f.deleted) != 1 {
		t.Fatalf("deleted = %v", f.deleted)
	}
}

func TestLifecycleTierANoBrokerCalls(t *testing.T) {
	f := &fakeAPI{}
	d, _, fl, id := lcDriver(t, f, nil)
	ctx := context.Background()
	if err := d.Provision(ctx, id, Spec{OpenEgress: true}); err != nil {
		t.Fatal(err)
	}
	f.exists = true
	if _, err := d.Start(ctx, driver.StartRequest{SandboxID: id}); err != nil {
		t.Fatal(err)
	}
	if err := d.Stop(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := d.Deprovision(ctx, id); err != nil {
		t.Fatal(err)
	}
	if fl.calls != 0 || idx(f.calls, "ensure") >= 0 {
		t.Fatalf("tier A touched broker: %d %v", fl.calls, f.calls)
	}
}

func TestProvisionEnsureFailureFailsAndRollsBack(t *testing.T) {
	f := &fakeAPI{}
	d, _, _, id := lcDriver(t, f, errors.New("spawn boom"))
	err := d.Provision(context.Background(), id, Spec{OpenEgress: true, CredMode: CredModeBroker, Sync: SyncPush, Repo: "git@github.com:a/b.git"})
	if err == nil || !strings.Contains(err.Error(), "broker") {
		t.Fatalf("err = %v", err)
	}
	if idx(f.calls, "exec") >= 0 {
		t.Fatalf("clone ran despite Ensure failure: %v", f.calls)
	}
	if len(f.deleted) != 1 {
		t.Fatalf("created sprite not rolled back: %v", f.deleted)
	}
}

func TestProvisionEnsureFailurePreexistingKept(t *testing.T) {
	f := &fakeAPI{createErr: errors.New("x")}
	d, _, fl, id := lcDriver(t, f, nil)
	_ = d.Provision(context.Background(), id, Spec{OpenEgress: true, CredMode: CredModeBroker})
	if fl.calls != 0 {
		t.Fatal("Ensure ran before CreateSprite succeeded")
	}
}

func TestBrokerModeNilBrokerFailsClosed(t *testing.T) {
	f := &fakeAPI{}
	d, _ := newTestDriver(t, f)
	err := d.Provision(context.Background(), domain.NewSandboxID(), Spec{OpenEgress: true, CredMode: CredModeBroker})
	if err == nil || !strings.Contains(err.Error(), "no broker configured") {
		t.Fatalf("err = %v", err)
	}
}

func TestBrokerPushCloneAndPrepUsePlaceholder(t *testing.T) {
	f := &fakeAPI{}
	d, _, _, id := lcDriver(t, f, nil)
	d.cfg.EnvResolver = func(_ context.Context, names []string) (map[string]string, error) {
		return map[string]string{SecretGitHub: fakeSecret}, nil
	}
	ctx := context.Background()
	spec := Spec{OpenEgress: true, CredMode: CredModeBroker, Sync: SyncPush, GitToken: fakeSecret,
		Repo: "https://user:" + fakeSecret + "@github.com/a/b.git", SecretNames: []string{SecretGitHub}}
	if err := d.Provision(ctx, id, spec); err != nil {
		t.Fatal(err)
	}
	if err := d.PreparePushBranch(ctx, id, CloneDir, "task/x"); err != nil {
		t.Fatal(err)
	}
	if blob := fmt.Sprintf("%+v", f.execs); strings.Contains(blob, fakeSecret) {
		t.Fatalf("real token leaked: %s", blob)
	}
	var clone, prep *ExecRequest
	for i := range f.execs {
		r := &f.execs[i]
		if len(r.Argv) > 1 && slices.Contains(r.Argv, "clone") {
			clone = r
		}
		if len(r.Argv) > 2 && r.Argv[0] == "sh" && strings.Contains(r.Argv[2], "check-ref-format") {
			prep = r
		}
	}
	if clone == nil || prep == nil {
		t.Fatalf("clone=%v prep=%v", clone, prep)
	}
	for _, r := range []*ExecRequest{clone, prep} {
		if r.Env["GH_TOKEN"] != placeholder || r.Env["HTTPS_PROXY"] == "" {
			t.Fatalf("env = %v", r.Env)
		}
	}
	if !slices.Contains(clone.Argv, "https://github.com/a/b.git") {
		t.Fatalf("clone argv = %v", clone.Argv)
	}
}

func TestBrokerModePolicyExcludesSecretHosts(t *testing.T) {
	f := &fakeAPI{}
	d, _, _, id := lcDriver(t, f, nil)
	spec := Spec{CredMode: CredModeBroker, Sync: SyncPush, Repo: "https://github.com/a/b.git",
		AllowedHosts: []string{"api.anthropic.com", "platform.claude.com", "GitHub.com", "api.github.com", "example.com"}}
	if err := d.Provision(context.Background(), id, spec); err != nil {
		t.Fatal(err)
	}
	got, err := d.Spec(id)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range got.AllowedHosts {
		if slices.Contains(SecretHosts, strings.ToLower(h)) {
			t.Errorf("secret host %s in persisted allowlist %v", h, got.AllowedHosts)
		}
	}
	if !slices.Contains(got.AllowedHosts, "example.com") {
		t.Errorf("non-secret host dropped: %v", got.AllowedHosts)
	}
	for _, p := range f.policies {
		for _, r := range p.Rules {
			if slices.Contains(SecretHosts, strings.ToLower(r.Domain)) {
				t.Errorf("policy rule for secret host %s", r.Domain)
			}
		}
	}
}
