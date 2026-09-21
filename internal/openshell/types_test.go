package openshell_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/IniZio/nexus/internal/openshell"
)

func TestSandboxSpecJSONRoundTrip(t *testing.T) {
	orig := openshell.SandboxSpec{
		ID:       "abc123",
		Name:     "test-sandbox",
		ImageRef: "docker.io/library/ubuntu:22.04",
		Command:  []string{"/bin/sh", "-c", "echo hi"},
		Env:      map[string]string{"FOO": "bar", "BAZ": "qux"},
		Mounts: []openshell.Mount{
			{HostPath: "/host/data", GuestPath: "/data", ReadOnly: true},
		},
		Resources:   openshell.Resources{BootMemBytes: 512 * 1024 * 1024, CeilingMemBytes: 2 * 1024 * 1024 * 1024, VCPUs: 2},
		GatewayAddr: "10.0.0.1:9000",
		Generation:  7,
	}
	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	var got openshell.SandboxSpec
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if got.ID != orig.ID {
		t.Errorf("ID: got %q want %q", got.ID, orig.ID)
	}
	if got.Name != orig.Name {
		t.Errorf("Name: got %q want %q", got.Name, orig.Name)
	}
	if got.ImageRef != orig.ImageRef {
		t.Errorf("ImageRef: got %q want %q", got.ImageRef, orig.ImageRef)
	}
	if len(got.Command) != len(orig.Command) {
		t.Errorf("Command len: got %d want %d", len(got.Command), len(orig.Command))
	} else {
		for i := range orig.Command {
			if got.Command[i] != orig.Command[i] {
				t.Errorf("Command[%d]: got %q want %q", i, got.Command[i], orig.Command[i])
			}
		}
	}
	for k, v := range orig.Env {
		if got.Env[k] != v {
			t.Errorf("Env[%q]: got %q want %q", k, got.Env[k], v)
		}
	}
	if len(got.Mounts) != 1 || got.Mounts[0] != orig.Mounts[0] {
		t.Errorf("Mounts: got %+v want %+v", got.Mounts, orig.Mounts)
	}
	if got.Resources != orig.Resources {
		t.Errorf("Resources: got %+v want %+v", got.Resources, orig.Resources)
	}
	if got.GatewayAddr != orig.GatewayAddr {
		t.Errorf("GatewayAddr: got %q want %q", got.GatewayAddr, orig.GatewayAddr)
	}
	if got.Generation != orig.Generation {
		t.Errorf("Generation: got %d want %d", got.Generation, orig.Generation)
	}
}

func TestRuntimeIdentityJSONRoundTrip(t *testing.T) {
	orig := openshell.RuntimeIdentity{
		PID:         12345,
		APISocket:   "/run/nexus/api.sock",
		VsockSocket: "/run/nexus/vsock.sock",
		StateDir:    "/var/lib/nexus/sandboxes/abc",
	}
	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	var got openshell.RuntimeIdentity
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if got != orig {
		t.Errorf("got %+v want %+v", got, orig)
	}
}

func TestBootstrapBundleFiles(t *testing.T) {
	b := openshell.BootstrapBundle{
		Generation: 42,
		ConfigJSON: []byte(`{"test":1}`),
		CertPEM:    []byte("CERT"),
		KeyPEM:     []byte("KEY"),
	}
	files := b.Files()
	if len(files) != 3 {
		t.Fatalf("len(files): got %d want 3", len(files))
	}
	cases := []struct {
		key  string
		want []byte
	}{
		{fmt.Sprintf("/.openshell/state/bootstrap-%d.json", b.Generation), b.ConfigJSON},
		{fmt.Sprintf("/.openshell/state/sandbox-%d.crt", b.Generation), b.CertPEM},
		{fmt.Sprintf("/.openshell/state/sandbox-%d.key", b.Generation), b.KeyPEM},
	}
	for _, c := range cases {
		got, ok := files[c.key]
		if !ok {
			t.Errorf("missing key %q", c.key)
			continue
		}
		if !bytes.Equal(got, c.want) {
			t.Errorf("files[%q]: got %q want %q", c.key, got, c.want)
		}
	}
}

func TestIsUnsupported(t *testing.T) {
	err := openshell.ErrUnsupported{Capability: "balloon"}
	if !openshell.IsUnsupported(err) {
		t.Error("IsUnsupported(ErrUnsupported{}) = false, want true")
	}
	wrapped := fmt.Errorf("outer: %w", err)
	if !openshell.IsUnsupported(wrapped) {
		t.Error("IsUnsupported(wrapped ErrUnsupported{}) = false, want true")
	}
	if openshell.IsUnsupported(errors.New("other")) {
		t.Error("IsUnsupported(errors.New) = true, want false")
	}
}
