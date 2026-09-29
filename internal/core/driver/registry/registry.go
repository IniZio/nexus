// Package registry maps backend names to driver constructors. Backends
// register from init() in their own package; callers select by name.
package registry

import (
	"errors"
	"fmt"
	"runtime"
	"sort"
	"sync"

	"github.com/IniZio/nexus/internal/core/driver"
)

const (
	CloudHypervisor = "cloud-hypervisor"
	Sprites         = "sprites"
)

// Constructor builds a driver. cfg is backend-specific and opaque to the
// registry; each backend documents the concrete type it accepts.
type Constructor func(cfg any) (driver.Driver, error)

// ErrUnsupported is wrapped by every "backend lacks this feature" error.
var ErrUnsupported = errors.New("not supported by backend")

// ErrNoBackend is returned when no backend can be selected.
var ErrNoBackend = errors.New("no backend available")

var (
	mu    sync.RWMutex
	ctors = map[string]Constructor{}
)

// Register adds a constructor. It panics on an empty name, nil constructor,
// or duplicate registration, because those are programmer errors at init.
func Register(name string, c Constructor) {
	if name == "" || c == nil {
		panic("registry: empty name or nil constructor")
	}
	mu.Lock()
	defer mu.Unlock()
	if _, dup := ctors[name]; dup {
		panic("registry: duplicate backend " + name)
	}
	ctors[name] = c
}

// Names returns registered backend names, sorted.
func Names() []string {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]string, 0, len(ctors))
	for n := range ctors {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Registered reports whether name is registered in this build.
func Registered(name string) bool {
	mu.RLock()
	defer mu.RUnlock()
	_, ok := ctors[name]
	return ok
}

// Resolve returns the effective backend name. An empty name selects the
// platform default: cloud-hypervisor on linux, an error elsewhere.
func Resolve(name string) (string, error) {
	if name == "" {
		if runtime.GOOS != "linux" {
			return "", fmt.Errorf("%w: no default backend on %s; set NEXUS_BACKEND", ErrNoBackend, runtime.GOOS)
		}
		name = CloudHypervisor
	}
	if !Registered(name) {
		return "", fmt.Errorf("%w: backend %q is not registered in this build (registered: %v)", ErrNoBackend, name, Names())
	}
	return name, nil
}

// New resolves name and constructs the backend's driver with cfg.
func New(name string, cfg any) (driver.Driver, error) {
	n, err := Resolve(name)
	if err != nil {
		return nil, err
	}
	mu.RLock()
	c := ctors[n]
	mu.RUnlock()
	return c(cfg)
}

// Unsupported builds the standard error for a feature the backend lacks.
func Unsupported(backend, feature string) error {
	if backend == "" {
		backend = "current"
	}
	return fmt.Errorf("%s: %w %q", feature, ErrUnsupported, backend)
}

// ActiveName returns the backend selected by Resolve(""), or "" when none.
func ActiveName() string {
	n, err := Resolve("")
	if err != nil {
		return ""
	}
	return n
}
