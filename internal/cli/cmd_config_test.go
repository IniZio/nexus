package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeConfigRepo lays out <tmp>/.git + <tmp>/.nexus/config.yaml and returns tmp.
func writeConfigRepo(t *testing.T, yamlBody string, withContainerfile bool) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, ".nexus"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".nexus", "config.yaml"), []byte(yamlBody), 0o644); err != nil {
		t.Fatal(err)
	}
	if withContainerfile {
		if err := os.WriteFile(filepath.Join(root, ".nexus", "Containerfile"), []byte("FROM scratch\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// runRegisteredConfigValidate drives the verb through the registry entry, so the
// test fails if the two-token name is not registered.
func runRegisteredConfigValidate(t *testing.T, jsonMode bool, args ...string) (error, string, string) {
	t.Helper()
	cmd, ok := Lookup("config validate")
	if !ok {
		t.Fatal(`"config validate" is not registered`)
	}
	out, stdout, stderr := capture(jsonMode)
	err := cmd.Run(context.Background(), args, out)
	return err, stdout.String(), stderr.String()
}

const validConfigYAML = `version: 1
egress:
  allow:
    - proxy.golang.org
    - sum.golang.org
  policy:
    - host: github.com
      paths: ["/owner/name/**"]
    - host: api.github.com
      paths: ["/repos/owner/name/**"]
  secrets:
    - GH_TOKEN@api.github.com,uploads.github.com
sandbox:
  image: ghcr.io/owner/app:dev
`

func TestConfigValidate_OK(t *testing.T) {
	root := writeConfigRepo(t, validConfigYAML, true)

	err, stdout, stderr := runRegisteredConfigValidate(t, false, root)
	if err != nil {
		t.Fatalf("expected success, got %v (stderr=%q)", err, stderr)
	}
	wantPath := filepath.Join(root, ".nexus", "config.yaml")
	for _, want := range []string{
		"ok: " + wantPath,
		"version:       1",
		"image:         ghcr.io/owner/app:dev",
		"containerfile: " + filepath.Join(root, ".nexus", "Containerfile"),
		"egress:        mode=policy-gated allow=2 policy=2 secrets=2",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout missing %q\n--- stdout ---\n%s", want, stdout)
		}
	}
}

func TestConfigValidate_OK_JSON(t *testing.T) {
	root := writeConfigRepo(t, "version: 1\n", false)

	err, stdout, _ := runRegisteredConfigValidate(t, true, root)
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	var env struct {
		SchemaVersion int                `json:"schema_version"`
		Kind          string             `json:"kind"`
		Data          configValidateData `json:"data"`
	}
	decodeOne(t, strings.NewReader(stdout), &env)
	if env.SchemaVersion != 1 || env.Kind != "config_validate" {
		t.Errorf("envelope = %+v, want schema_version=1 kind=config_validate", env)
	}
	d := env.Data
	if d.Path != filepath.Join(root, ".nexus", "config.yaml") {
		t.Errorf("path = %q", d.Path)
	}
	if d.Version != 1 || d.Image != "" || d.ContainerfilePresent {
		t.Errorf("data = %+v, want version=1 image=\"\" containerfile_present=false", d)
	}
	if d.Egress.Mode != "default" || d.Egress.AllowHosts != 0 || d.Egress.PolicyHosts != 0 || d.Egress.SecretsHosts != 0 {
		t.Errorf("egress = %+v, want mode=default with zero counts", d.Egress)
	}
}

func TestConfigValidate_UnknownKey(t *testing.T) {
	root := writeConfigRepo(t, "version: 1\negress:\n  alow: [example.com]\n", false)

	err, stdout, _ := runRegisteredConfigValidate(t, false, root)
	assertConfigValidateFailure(t, err, stdout, "alow")
}

func TestConfigValidate_AllowPolicyOverlap(t *testing.T) {
	root := writeConfigRepo(t, `version: 1
egress:
  allow: [github.com]
  policy:
    - host: github.com
      paths: ["/**"]
`, false)

	err, stdout, _ := runRegisteredConfigValidate(t, false, root)
	assertConfigValidateFailure(t, err, stdout, "listed under egress.allow and egress.policy")
}

func TestConfigValidate_MissingVersion(t *testing.T) {
	root := writeConfigRepo(t, "egress:\n  allow: [example.com]\n", false)

	err, stdout, _ := runRegisteredConfigValidate(t, false, root)
	assertConfigValidateFailure(t, err, stdout, `missing required field "version"`)
}

func TestConfigValidate_MissingConfig(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	err, stdout, _ := runRegisteredConfigValidate(t, false, root)
	assertConfigValidateFailure(t, err, stdout, "no .nexus/config.yaml found from "+root)
}

func TestConfigValidate_TooManyArgs(t *testing.T) {
	err, _, _ := runRegisteredConfigValidate(t, false, "a", "b")
	var usage *UsageError
	if !errors.As(err, &usage) {
		t.Fatalf("want *UsageError, got %T: %v", err, err)
	}
}

// assertConfigValidateFailure checks the verb returned an invalid_argument
// CodedError (root.go maps it to exit 1) carrying wantSubstr, and wrote nothing
// to stdout.
func assertConfigValidateFailure(t *testing.T, err error, stdout, wantSubstr string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error containing %q, got nil (stdout=%q)", wantSubstr, stdout)
	}
	var coded *CodedError
	if !errors.As(err, &coded) {
		t.Fatalf("want *CodedError, got %T: %v", err, err)
	}
	if coded.Code != ErrCodeInvalidArgument {
		t.Errorf("code = %q, want %q", coded.Code, ErrCodeInvalidArgument)
	}
	if !strings.Contains(err.Error(), wantSubstr) {
		t.Errorf("error %q does not contain %q", err.Error(), wantSubstr)
	}
	if stdout != "" {
		t.Errorf("stdout should be empty on failure, got %q", stdout)
	}
}
