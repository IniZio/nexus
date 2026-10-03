package registry_test

import (
	"errors"
	"runtime"
	"testing"

	_ "github.com/IniZio/nexus/internal/core/driver/cloudhypervisor"
	"github.com/IniZio/nexus/internal/core/driver/registry"
	_ "github.com/IniZio/nexus/internal/core/driver/sprites"
)

func TestResolveSprites(t *testing.T) {
	got, err := registry.Resolve("sprites")
	if err != nil || got != registry.Sprites {
		t.Fatalf("Resolve(sprites) = %q, %v", got, err)
	}
}

func TestResolveBogus(t *testing.T) {
	if _, err := registry.Resolve("bogus"); !errors.Is(err, registry.ErrNoBackend) {
		t.Fatalf("err = %v, want ErrNoBackend", err)
	}
}

func TestResolveEmptyDefault(t *testing.T) {
	got, err := registry.Resolve("")
	if runtime.GOOS != "linux" {
		if !errors.Is(err, registry.ErrNoBackend) {
			t.Fatalf("err = %v", err)
		}
		return
	}
	if err != nil || got != registry.CloudHypervisor {
		t.Fatalf("Resolve(\"\") = %q, %v", got, err)
	}
}
