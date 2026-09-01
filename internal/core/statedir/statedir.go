// Package statedir owns the location, permissions and lifetime of the
// per-sandbox supervisor state directory
// (<storeRoot>/supervisors/<sandbox-id>/).
//
// # Why this is its own package
//
// Two packages need the same path and the same modes, and they cannot import
// each other: internal/supervisor imports internal/core/service, so service
// cannot import supervisor. Before this package existed, service.go duplicated
// the path literal with a comment explaining why (service.go:857) and picked a
// different mode from the supervisor package — which is exactly how the
// directory ended up world-readable at 0755 on the one path that mattered.
// Both now call in here, so the modes cannot drift apart again.
//
// # Why 0700 / 0600
//
// The directory holds, or is about to hold, secret material: the MITM CA
// private key (motive nexus3-host-supervisor-hotswap, ticket 13 / D-HSH-18).
// It already holds the egress decisions log, which is a record of every host a
// sandbox talked to. Host-root can read any of it regardless; the boundary
// being drawn here is against other unprivileged users on a shared host.
package statedir

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/IniZio/nexus3/internal/core/domain"
)

const (
	// Subdir is the component under the store root that holds every
	// per-sandbox supervisor state directory.
	Subdir = "supervisors"

	// DirMode is the mode of the per-sandbox state directory. Owner-only:
	// see the package doc.
	DirMode os.FileMode = 0o700

	// FileMode is the mode of every file created inside the state directory.
	// Owner-only, because the directory carries (or will carry) secrets and a
	// per-file exception is how a secret leaks.
	FileMode os.FileMode = 0o600
)

// SupervisorDir returns the durable per-sandbox supervisor directory.
// Unlike orca's /tmp state dir this survives reboot so start/stop can
// re-own the broker the same way the supervisor re-owns the VM.
func SupervisorDir(storeRoot string, id domain.SandboxID) string {
	return filepath.Join(storeRoot, Subdir, id.String())
}

// SupervisorsRoot returns <storeRoot>/supervisors, the directory the reaper
// enumerates.
func SupervisorsRoot(storeRoot string) string {
	return filepath.Join(storeRoot, Subdir)
}

// Ensure creates dir (and parents) with [DirMode] and tightens dir itself if
// it already exists with looser permissions.
//
// The explicit Chmod is load-bearing, not defensive: MkdirAll is a no-op on an
// existing directory, so without it the 641 directories this host had already
// accumulated at 0755 would stay 0755 forever, and the very next supervisor to
// take ownership of one would write a private key into a world-readable
// directory. Tightening happens when a supervisor next takes ownership, which
// is the only moment we know we are allowed to touch it.
//
// Only dir is chmodded, never its parents: <storeRoot>/supervisors and the
// store root itself are shared, and narrowing them here would be a surprising
// side effect of starting one supervisor.
func Ensure(dir string) error {
	if err := os.MkdirAll(dir, DirMode); err != nil {
		return fmt.Errorf("statedir: mkdir %s: %w", dir, err)
	}
	if err := os.Chmod(dir, DirMode); err != nil {
		return fmt.Errorf("statedir: chmod %s: %w", dir, err)
	}
	return nil
}

// Remove deletes the state directory and everything in it. Idempotent: a
// missing directory is not an error, so a crash mid-teardown is safe to retry.
func Remove(dir string) error {
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("statedir: remove %s: %w", dir, err)
	}
	return nil
}
