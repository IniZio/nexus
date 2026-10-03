package sprites

import (
	"context"
	"os"
	"strings"
	"testing"

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
