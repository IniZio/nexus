package cli

import (
	"errors"
	"runtime"
	"testing"

	"github.com/IniZio/nexus/internal/core/driver/registry"
)

func TestRequireBackend(t *testing.T) {
	t.Setenv("NEXUS_BACKEND", "")
	if runtime.GOOS == "linux" {
		if err := requireBackend("disk"); err != nil {
			t.Fatalf("linux default: %v", err)
		}
		if err := requireBackend("anything", registry.Sprites); !errors.Is(err, registry.ErrUnsupported) {
			t.Fatalf("want ErrUnsupported, got %v", err)
		}
		if err := requireBackend("unlisted"); err != nil {
			t.Fatalf("unlisted feature: %v", err)
		}
	} else if err := requireBackend("disk"); !errors.Is(err, registry.ErrNoBackend) {
		t.Fatalf("want ErrNoBackend, got %v", err)
	}
}

func TestRequireBackend_UnknownEnv(t *testing.T) {
	t.Setenv("NEXUS_BACKEND", "bogus")
	if err := requireBackend("disk"); !errors.Is(err, registry.ErrNoBackend) {
		t.Fatalf("want ErrNoBackend, got %v", err)
	}
}
