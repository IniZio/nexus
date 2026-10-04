package sprites

import (
	"context"
	"fmt"
	"github.com/IniZio/nexus/internal/core/driver/sprites/broker"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/driver"
)

const fakeSecret = "sk-ant-oat-FAKE-secret-value"

func credDriver(t *testing.T, f *fakeAPI) (*Driver, string) {
	t.Helper()
	dir := t.TempDir()
	d, err := New(Config{API: f, StateDir: dir, EnvResolver: func(_ context.Context, names []string) (map[string]string, error) {
		m := map[string]string{}
		for _, n := range names {
			m[n] = fakeSecret
		}
		return m, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	return d, dir
}

func TestExecMergesProjectedEnv(t *testing.T) {
	f := &fakeAPI{}
	d, _ := credDriver(t, f)
	id := domain.NewSandboxID()
	if err := d.Provision(context.Background(), id, Spec{OpenEgress: true, SecretNames: []string{"GITHUB_TOKEN", SecretClaudeOAuth}}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(context.Background(), id, driver.ExecOptions{Argv: []string{"claude"}, Env: map[string]string{"GH_TOKEN": "explicit"}}); err != nil {
		t.Fatal(err)
	}
	env := f.execs[len(f.execs)-1].Env
	if env[SecretClaudeOAuth] != fakeSecret || env["GH_TOKEN"] != "explicit" || env["GOTOOLCHAIN"] != "auto" {
		t.Fatalf("env = %v", env)
	}
}

func TestSpecPersistsNamesOnly(t *testing.T) {
	f := &fakeAPI{}
	d, dir := credDriver(t, f)
	id := domain.NewSandboxID()
	if err := d.Provision(context.Background(), id, Spec{OpenEgress: true, SecretNames: []string{SecretClaudeOAuth}}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(context.Background(), id, driver.ExecOptions{Argv: []string{"true"}}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(d.specPath(id))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), SecretClaudeOAuth) || strings.Contains(string(b), fakeSecret) {
		t.Fatalf("spec = %s", b)
	}
	_ = dir
}

func TestProvisionUnsupportedSecretFailsClosed(t *testing.T) {
	f := &fakeAPI{}
	d, _ := credDriver(t, f)
	err := d.Provision(context.Background(), domain.NewSandboxID(), Spec{SecretNames: []string{"AWS_SECRET_ACCESS_KEY"}})
	if err == nil || !strings.Contains(err.Error(), "secret kind AWS_SECRET_ACCESS_KEY unsupported on tier A") {
		t.Fatalf("err = %v", err)
	}
	if len(f.created) != 0 {
		t.Fatal("sprite created despite unsupported secret")
	}
}

func TestExecNoSecretsNoResolverOK(t *testing.T) {
	f := &fakeAPI{}
	d, _ := newTestDriver(t, f)
	id := domain.NewSandboxID()
	if err := d.Provision(context.Background(), id, Spec{OpenEgress: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(context.Background(), id, driver.ExecOptions{Argv: []string{"true"}}); err != nil {
		t.Fatal(err)
	}
}

type fakeLauncher struct {
	calls   int
	onSpawn func(id string) error
}

func (f *fakeLauncher) Spawn(_ context.Context, id string) error {
	f.calls++
	if f.onSpawn != nil {
		return f.onSpawn(id)
	}
	return nil
}

const placeholder = "nx-placeholder-ghtoken"

func brokerDriver(t *testing.T, f *fakeAPI, fl *fakeLauncher) (*Driver, *broker.Manager, domain.SandboxID) {
	t.Helper()
	dir := t.TempDir()
	m := &broker.Manager{StateDir: dir, Launcher: fl, ReadyTimeout: 200 * time.Millisecond}
	d, err := New(Config{API: f, StateDir: dir, Broker: m, EnvResolver: func(_ context.Context, names []string) (map[string]string, error) {
		m := map[string]string{}
		for _, n := range names {
			m[n] = fakeSecret
		}
		return m, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	id := domain.NewSandboxID()
	// Provision ensures the broker; give it a live one, then leave it dead
	// and the launcher as the caller configured it.
	orig := fl.onSpawn
	st := liveBrokerState(t)
	fl.onSpawn = func(string) error { return broker.WriteState(m.StateDir, id.String(), st) }
	if err := d.Provision(context.Background(), id, Spec{OpenEgress: true, CredMode: CredModeBroker, SecretNames: []string{SecretGitHub, SecretClaudeOAuth}}); err != nil {
		t.Fatal(err)
	}
	fl.onSpawn, fl.calls = orig, 0
	if err := broker.RemoveState(m.StateDir, id.String()); err != nil {
		t.Fatal(err)
	}
	return d, m, id
}

func liveBrokerState(t *testing.T) broker.State {
	t.Helper()
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	t.Cleanup(func() { _ = cmd.Process.Kill(); <-done })
	var argv []string
	for i := 0; i < 200; i++ {
		a, err := broker.ProcessCmdline(cmd.Process.Pid)
		if err == nil && len(a) > 0 && filepath.Base(a[0]) == "sleep" {
			argv = a
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if argv == nil {
		t.Fatal("helper never exec'd sleep")
	}
	return broker.State{PID: cmd.Process.Pid, Cmdline: argv, Started: time.Now(),
		GuestEnv: map[string]string{"GH_TOKEN": placeholder, "CLAUDE_CODE_OAUTH_TOKEN": "nx-placeholder-claude", "HTTPS_PROXY": "http://10.0.0.1:3128"}}
}

func TestBrokerExecEnvHasPlaceholderNotRealToken(t *testing.T) {
	f := &fakeAPI{}
	fl := &fakeLauncher{}
	d, m, id := brokerDriver(t, f, fl)
	st := liveBrokerState(t)
	if err := broker.WriteState(m.StateDir, id.String(), st); err != nil {
		t.Fatal(err)
	}
	_, err := d.Exec(context.Background(), id, driver.ExecOptions{Argv: []string{"gh", "api"}, Env: map[string]string{"GH_TOKEN": fakeSecret, "X": "1"}})
	if err != nil {
		t.Fatal(err)
	}
	if fl.calls != 0 {
		t.Fatalf("alive broker respawned: %d", fl.calls)
	}
	req := f.execs[len(f.execs)-1]
	if req.Env["GH_TOKEN"] != placeholder || req.Env["HTTPS_PROXY"] == "" || req.Env["X"] != "1" {
		t.Fatalf("env = %v", req.Env)
	}
	if blob := fmt.Sprint(f.execs); strings.Contains(blob, fakeSecret) {
		t.Fatalf("real token leaked: %s", blob)
	}
}

func TestBrokerExecEnsuresDeadBroker(t *testing.T) {
	f := &fakeAPI{}
	var m *broker.Manager
	var id domain.SandboxID
	st := liveBrokerState(t)
	fl := &fakeLauncher{onSpawn: func(string) error { return broker.WriteState(m.StateDir, id.String(), st) }}
	var d *Driver
	d, m, id = brokerDriver(t, f, fl)
	if _, err := d.Exec(context.Background(), id, driver.ExecOptions{Argv: []string{"true"}}); err != nil {
		t.Fatal(err)
	}
	if fl.calls != 1 || len(f.execs) == 0 {
		t.Fatalf("calls=%d execs=%d", fl.calls, len(f.execs))
	}
}

func TestBrokerExecRefusesWhenBrokerDead(t *testing.T) {
	f := &fakeAPI{}
	d, _, id := brokerDriver(t, f, &fakeLauncher{})
	n := len(f.execs)
	_, err := d.Exec(context.Background(), id, driver.ExecOptions{Argv: []string{"true"}})
	if err == nil || !strings.Contains(err.Error(), "broker") {
		t.Fatalf("err = %v", err)
	}
	if len(f.execs) != n {
		t.Fatal("command ran with dead broker")
	}
}
