// Package harness provides the shared component-wiring seam used by both the
// acceptance and spec (Gherkin) test suites. Keeping the wiring in one place
// prevents the duplicated-wiring drift that emerges as the scenario count grows.
//
// Usage:
//
//	h, err := harness.New(root)   // root is a fresh temporary directory
//	if err != nil { ... }
//	// h.Svc, h.St, h.Drv, h.Rec are ready to use
package harness

import (
	"github.com/IniZio/nexus3/internal/core/driver/fake"
	"github.com/IniZio/nexus3/internal/core/lifecycle"
	"github.com/IniZio/nexus3/internal/core/recovery"
	"github.com/IniZio/nexus3/internal/core/service"
	"github.com/IniZio/nexus3/internal/core/store"
)

// Harness holds the wired-together core components for a single test scenario.
// Never share a Harness across scenarios or sub-tests that mutate state.
type Harness struct {
	St  *store.FileStore
	Drv *fake.FakeDriver
	Svc *service.Service
	Rec *recovery.Recoverer
}

// New returns a Harness backed by root. root must be a fresh, caller-owned
// temporary directory (e.g. t.TempDir() or os.MkdirTemp).
func New(root string) (*Harness, error) {
	st, err := store.NewFileStore(root)
	if err != nil {
		return nil, err
	}
	drv := fake.New()
	mach := lifecycle.New()
	return &Harness{
		St:  st,
		Drv: drv,
		Svc: service.New(st, drv, mach),
		Rec: recovery.New(st, drv),
	}, nil
}
