package sprites

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/IniZio/nexus/internal/core/domain"
)

func TestNormalizePresets(t *testing.T) {
	got, err := NormalizePresets([]string{" Docker ", "docker"})
	if err != nil || len(got) != 1 || got[0] != PresetDocker {
		t.Fatalf("got %v, %v", got, err)
	}
	if got, err := NormalizePresets(nil); err != nil || got != nil {
		t.Fatalf("nil: %v, %v", got, err)
	}
	if _, err := NormalizePresets([]string{"podman"}); !errors.Is(err, ErrUnknownPreset) || !strings.Contains(err.Error(), "podman") {
		t.Fatalf("err = %v", err)
	}
}

func TestProvisionUnknownPresetCreatesNothing(t *testing.T) {
	f := &fakeAPI{}
	d, _ := newTestDriver(t, f)
	err := d.Provision(context.Background(), domain.NewSandboxID(), Spec{Presets: []string{"nope"}})
	if !errors.Is(err, ErrUnknownPreset) || len(f.calls) != 0 {
		t.Fatalf("err=%v calls=%v", err, f.calls)
	}
}

func TestProvisionNoPresetInstallsNothing(t *testing.T) {
	f := &fakeAPI{}
	d, _ := newTestDriver(t, f)
	if err := d.Provision(context.Background(), domain.NewSandboxID(), Spec{AllowedHosts: []string{"github.com"}}); err != nil {
		t.Fatal(err)
	}
	if len(f.execs) != 1 || len(f.policies) != 1 {
		t.Fatalf("execs=%d policies=%d", len(f.execs), len(f.policies))
	}
	for _, h := range append(DockerAptHosts, DockerRegistryHosts...) {
		if hasHost(f.policies[0], h) {
			t.Fatalf("%s allowed without preset", h)
		}
	}
}

func TestProvisionDockerEgressWindow(t *testing.T) {
	f := &fakeAPI{}
	d, _ := newTestDriver(t, f)
	id := domain.NewSandboxID()
	if err := d.Provision(context.Background(), id, Spec{Presets: []string{"docker"}}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(f.calls, ","); got != "create,policy,policy,exec,policy,exec" {
		t.Fatalf("calls = %s", got)
	}
	base, window, tight := f.policies[0], f.policies[1], f.policies[2]
	for _, h := range DockerRegistryHosts {
		if !hasHost(base, h) || !hasHost(window, h) || !hasHost(tight, h) {
			t.Fatalf("%s must stay allowed throughout", h)
		}
	}
	for _, h := range DockerAptHosts {
		if hasHost(base, h) || !hasHost(window, h) || hasHost(tight, h) {
			t.Fatalf("%s must be allowed only in the window", h)
		}
	}
	if !strings.Contains(strings.Join(f.execs[0].Argv, " "), "docker.io") {
		t.Fatalf("install argv = %v", f.execs[0].Argv)
	}
	spec, err := d.Spec(id)
	if err != nil || len(spec.Presets) != 1 || !slices.Contains(spec.AllowedHosts, DockerRegistryHosts[0]) {
		t.Fatalf("spec = %+v, %v", spec, err)
	}
}

func TestProvisionDockerTightenFailureFailsClosed(t *testing.T) {
	f := &fakeAPI{policyErr: func(n int) error {
		if n == 3 {
			return errors.New("boom")
		}
		return nil
	}}
	d, _ := newTestDriver(t, f)
	err := d.Provision(context.Background(), domain.NewSandboxID(), Spec{Presets: []string{"docker"}})
	if err == nil || !strings.Contains(err.Error(), "tighten") || len(f.deleted) != 1 {
		t.Fatalf("err=%v deleted=%v", err, f.deleted)
	}
}

func TestProvisionDockerOpenEgressNoWindow(t *testing.T) {
	f := &fakeAPI{}
	d, _ := newTestDriver(t, f)
	if err := d.Provision(context.Background(), domain.NewSandboxID(), Spec{OpenEgress: true, Presets: []string{"docker"}}); err != nil {
		t.Fatal(err)
	}
	if len(f.policies) != 0 || len(f.execs) != 2 {
		t.Fatalf("policies=%d execs=%d", len(f.policies), len(f.execs))
	}
}
