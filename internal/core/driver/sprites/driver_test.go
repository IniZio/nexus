package sprites

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	sdk "github.com/superfly/sprites-go"

	"github.com/IniZio/nexus/internal/core/agent/agentpb"
	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/driver"
	"github.com/IniZio/nexus/internal/core/driver/registry"
)

type fakeAPI struct {
	mu        sync.Mutex
	calls     []string
	created   []string
	deleted   []string
	policies  []*sdk.NetworkPolicy
	execs     []ExecRequest
	exists    bool
	existsErr error
	exitCode  int32
	execErr   error
	stderr    string
	policyErr func(n int) error
	createErr error
}

func (f *fakeAPI) rec(s string) { f.calls = append(f.calls, s) }

func (f *fakeAPI) CreateSprite(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rec("create")
	f.created = append(f.created, name)
	return f.createErr
}

func (f *fakeAPI) SpriteExists(context.Context, string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rec("exists")
	return f.exists, f.existsErr
}

func (f *fakeAPI) DeleteSprite(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rec("delete")
	f.deleted = append(f.deleted, name)
	return nil
}

func (f *fakeAPI) SetNetworkPolicy(_ context.Context, _ string, p *sdk.NetworkPolicy) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rec("policy")
	f.policies = append(f.policies, p)
	if f.policyErr != nil {
		return f.policyErr(len(f.policies))
	}
	return nil
}

func (f *fakeAPI) Exec(_ context.Context, _ string, req ExecRequest) (int32, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rec("exec")
	f.execs = append(f.execs, req)
	if req.Stderr != nil && f.stderr != "" {
		_, _ = req.Stderr.Write([]byte(f.stderr))
	}
	return f.exitCode, f.execErr
}

func newTestDriver(t *testing.T, f *fakeAPI) (*Driver, string) {
	t.Helper()
	dir := t.TempDir()
	d, err := New(Config{API: f, StateDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	return d, dir
}

func TestSpriteName(t *testing.T) {
	id := domain.NewSandboxID()
	n := SpriteName(id)
	if !strings.HasPrefix(n, NamePrefix) || len(n) == len(NamePrefix) {
		t.Fatalf("name %q lacks prefix or suffix", n)
	}
	if SpriteName(id) != n {
		t.Fatal("SpriteName not deterministic")
	}
	if n2 := SpriteName(domain.NewSandboxID()); n2 == n {
		t.Fatalf("distinct ids collided: %q", n)
	}
	for _, r := range strings.TrimPrefix(n, NamePrefix) {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			t.Fatalf("unsanitized rune %q in %q", r, n)
		}
	}
}

func TestProvisionClosedEgress(t *testing.T) {
	f := &fakeAPI{}
	d, _ := newTestDriver(t, f)
	id := domain.NewSandboxID()
	if err := d.Provision(context.Background(), id, Spec{AllowedHosts: []string{"github.com"}}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(f.calls, ","); got != "create,policy" {
		t.Fatalf("calls = %s", got)
	}
	if f.created[0] != SpriteName(id) {
		t.Fatalf("created %q", f.created[0])
	}
	rules := f.policies[0].Rules
	last := rules[len(rules)-1]
	if last.Domain != "*" || last.Action != "deny" {
		t.Fatalf("last rule = %+v, want deny *", last)
	}
	var sawAllow bool
	for _, r := range rules {
		if r.Domain == "github.com" && r.Action == "allow" {
			sawAllow = true
		}
	}
	if !sawAllow {
		t.Fatalf("github.com not allowed: %+v", rules)
	}
}

func TestProvisionOpenEgressNoPolicy(t *testing.T) {
	f := &fakeAPI{}
	d, _ := newTestDriver(t, f)
	if err := d.Provision(context.Background(), domain.NewSandboxID(), Spec{OpenEgress: true}); err != nil {
		t.Fatal(err)
	}
	if len(f.policies) != 0 {
		t.Fatalf("policy calls = %d, want 0", len(f.policies))
	}
}

func TestProvisionCloneTokenNeverLeaks(t *testing.T) {
	const tok = "ghs_SECRETTOKEN123"
	f := &fakeAPI{}
	d, dir := newTestDriver(t, f)
	id := domain.NewSandboxID()
	if err := d.Provision(context.Background(), id, Spec{Repo: "https://github.com/o/r.git", GitToken: tok}); err != nil {
		t.Fatal(err)
	}
	if len(f.execs) != 1 {
		t.Fatalf("execs = %d, want 1", len(f.execs))
	}
	argv := strings.Join(f.execs[0].Argv, " ")
	if !strings.Contains(argv, "clone") || !strings.Contains(argv, "https://github.com/o/r.git") {
		t.Fatalf("argv = %q", argv)
	}
	if strings.Contains(argv, tok) {
		t.Fatalf("token in argv: %q", argv)
	}
	for _, v := range f.execs[0].Env {
		if v == tok {
			goto envOK
		}
	}
	t.Fatal("token not delivered via env")
envOK:
	b, err := os.ReadFile(filepath.Join(dir, "sprites", id.String(), "spec.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), tok) {
		t.Fatalf("token in spec.json: %s", b)
	}
}

func TestProvisionCloneFailureRollsBack(t *testing.T) {
	const tok = "ghs_SECRETTOKEN123"
	for name, f := range map[string]*fakeAPI{
		"exit code": {exitCode: 128, stderr: "fatal: auth failed for " + tok},
		"transport": {execErr: errors.New("boom " + tok)},
	} {
		t.Run(name, func(t *testing.T) {
			d, _ := newTestDriver(t, f)
			err := d.Provision(context.Background(), domain.NewSandboxID(), Spec{Repo: "https://x/y.git", GitToken: tok})
			if err == nil {
				t.Fatal("want error")
			}
			if strings.Contains(err.Error(), tok) {
				t.Fatalf("token in error: %v", err)
			}
			if len(f.deleted) != 1 || f.deleted[0] != f.created[0] {
				t.Fatalf("rollback: created=%v deleted=%v", f.created, f.deleted)
			}
		})
	}
}

func TestProvisionCreateErrorStillDeletes(t *testing.T) {
	f := &fakeAPI{createErr: errors.New("connection reset")}
	d, _ := newTestDriver(t, f)
	if err := d.Provision(context.Background(), domain.NewSandboxID(), Spec{}); err == nil {
		t.Fatal("want error")
	}
	if len(f.deleted) != 1 || f.deleted[0] != f.created[0] {
		t.Fatalf("created=%v deleted=%v", f.created, f.deleted)
	}
}

func TestCheckNameRefusesUnprefixed(t *testing.T) {
	if err := checkName("prod-db"); err == nil {
		t.Fatal("want refusal")
	}
	if err := checkName(NamePrefix + "abc"); err != nil {
		t.Fatal(err)
	}
}

func TestDeprovision(t *testing.T) {
	f := &fakeAPI{exists: true}
	d, dir := newTestDriver(t, f)
	id := domain.NewSandboxID()
	if err := d.Provision(context.Background(), id, Spec{OpenEgress: true}); err != nil {
		t.Fatal(err)
	}
	f.deleted = nil
	if err := d.Deprovision(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if len(f.deleted) != 1 || !strings.HasPrefix(f.deleted[0], NamePrefix) {
		t.Fatalf("deleted = %v", f.deleted)
	}
	if _, err := os.Stat(filepath.Join(dir, "sprites", id.String())); !os.IsNotExist(err) {
		t.Fatalf("state dir survives: %v", err)
	}
	f.exists = false
	f.deleted = nil
	if err := d.Deprovision(context.Background(), id); err != nil {
		t.Fatalf("second Deprovision: %v", err)
	}
	if len(f.deleted) != 0 {
		t.Fatalf("deleted absent sprite: %v", f.deleted)
	}
}

func TestObserve(t *testing.T) {
	id := domain.NewSandboxID()
	cases := []struct {
		name string
		f    *fakeAPI
		want driver.RunState
		err  bool
	}{
		{"exists unmarked", &fakeAPI{exists: true}, driver.Absent, false},
		{"absent", &fakeAPI{}, driver.Absent, false},
		{"api error", &fakeAPI{existsErr: errors.New("503")}, driver.Unknown, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d, _ := newTestDriver(t, c.f)
			o, err := d.Observe(context.Background(), id)
			if (err != nil) != c.err {
				t.Fatalf("err = %v", err)
			}
			if o.State != c.want {
				t.Fatalf("state = %v, want %v", o.State, c.want)
			}
		})
	}
}

func TestExecPassthrough(t *testing.T) {
	f := &fakeAPI{exitCode: 7}
	d, _ := newTestDriver(t, f)
	code, err := d.Exec(context.Background(), domain.NewSandboxID(), driver.ExecOptions{
		Argv: []string{"sh", "-c", "exit 7"},
		Env:  map[string]string{"A": "b"},
		Cwd:  "/tmp/x",
		Pty:  &agentpb.PtyOptions{Term: "xterm"},
	})
	if err != nil || code != 7 {
		t.Fatalf("code=%d err=%v", code, err)
	}
	r := f.execs[0]
	if r.Dir != "/tmp/x" || r.Env["A"] != "b" || !r.TTY || r.Term != "xterm" {
		t.Fatalf("req = %+v", r)
	}
}

func TestStartStopObserve(t *testing.T) {
	ctx := context.Background()
	f := &fakeAPI{exists: true}
	d, _ := newTestDriver(t, f)
	id := domain.NewSandboxID()
	state := func() driver.RunState {
		t.Helper()
		o, err := d.Observe(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return o.State
	}
	if state() == driver.Running {
		t.Fatal("running before Start")
	}
	for i := 0; i < 2; i++ {
		if _, err := d.Start(ctx, driver.StartRequest{SandboxID: id}); err != nil {
			t.Fatal(err)
		}
		if state() != driver.Running {
			t.Fatal("not running after Start")
		}
		for j := 0; j < 2; j++ {
			if err := d.Stop(ctx, id); err != nil {
				t.Fatal(err)
			}
		}
		if state() == driver.Running {
			t.Fatal("running after Stop")
		}
	}
	if len(f.deleted) != 0 {
		t.Fatalf("Stop deleted sprite: %v", f.deleted)
	}
}

func TestStartUnprovisioned(t *testing.T) {
	d, _ := newTestDriver(t, &fakeAPI{})
	if _, err := d.Start(context.Background(), driver.StartRequest{SandboxID: domain.NewSandboxID()}); err == nil {
		t.Fatal("want error")
	}
}

func TestCapabilities(t *testing.T) {
	d, _ := newTestDriver(t, &fakeAPI{})
	c := d.Capabilities()
	if c.GuestOS != driver.GuestOSLinux || c.Egress != driver.EgressEnforced {
		t.Fatalf("caps = %+v", c)
	}
	o := driver.OptionalInterfaces(d)
	if o.Pause || o.Snapshot || o.Fork {
		t.Fatalf("optional = %+v", o)
	}
}

func TestNewRequiresToken(t *testing.T) {
	const tok = "tok_SHOULDNOTLEAK"
	t.Setenv("SPRITES_TOKEN", "")
	t.Setenv("SPRITES_API_TOKEN", "")
	if _, err := New(Config{StateDir: t.TempDir()}); err == nil {
		t.Fatal("want error without token")
	}
	t.Setenv("SPRITES_TOKEN", tok)
	// StateDir empty forces an error while a token is present.
	_, err := New(Config{})
	if err == nil {
		t.Fatal("want error")
	}
	if strings.Contains(err.Error(), tok) {
		t.Fatalf("token in error: %v", err)
	}
}

func TestRegistryNew(t *testing.T) {
	d, err := registry.New(registry.Sprites, Config{API: &fakeAPI{}, StateDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := d.(*Driver); !ok {
		t.Fatalf("driver is %T", d)
	}
}
