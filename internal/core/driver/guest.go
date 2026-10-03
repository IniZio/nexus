package driver

import (
	"context"
	"errors"
	"io"

	"github.com/IniZio/nexus/internal/core/agent/agentpb"
	"github.com/IniZio/nexus/internal/core/agent/wire"
	"github.com/IniZio/nexus/internal/core/domain"
)

// ErrUnsupported is returned by a driver method the substrate cannot provide.
var ErrUnsupported = errors.New("driver: operation not supported by substrate")

// ExecOptions configures a guest process run through [Driver.Exec].
// Field semantics are those of the guest agent's exec contract.
type ExecOptions struct {
	SessionID string
	Argv      []string
	Env       map[string]string
	Cwd       string
	Pty       *agentpb.PtyOptions
	Stdin     io.Reader
	Stdout    io.Writer
	Stderr    io.Writer
	// WinsizeCh: when non-nil the caller MUST close it after Exec returns.
	WinsizeCh <-chan wire.Winsize
}

// AttachOptions configures [SessionAttacher.Attach].
type AttachOptions struct {
	SessionID        string
	ResumeFromOffset uint64
	Stdin            io.Reader
	Stdout           io.Writer
	Stderr           io.Writer
	WinsizeCh        <-chan wire.Winsize
}

// CopyOptions configures a tar-stream file transfer through [Driver.Copy].
type CopyOptions struct {
	Direction     agentpb.CopyDirection
	GuestPath     string
	IsDirectory   bool
	Src           io.Reader // PUSH source
	Dst           io.Writer // PULL sink
	ExpectedBytes *int64    // required for single-file PUSH, including size 0
}

// SessionAttacher is optional: reattach to a running guest session.
type SessionAttacher interface {
	Attach(ctx context.Context, id domain.SandboxID, opts AttachOptions) (int32, error)
}

// GuestOS names the guest operating system a substrate runs.
type GuestOS string

const (
	GuestOSUnknown GuestOS = ""
	GuestOSLinux   GuestOS = "linux"
	GuestOSDarwin  GuestOS = "darwin"
)

// EgressLevel describes how strongly the substrate enforces network egress policy.
type EgressLevel string

const (
	EgressNone     EgressLevel = "none"     // no enforcement
	EgressAdvisory EgressLevel = "advisory" // best-effort / process-level
	EgressEnforced EgressLevel = "enforced"
)

// Isolation names what bounds a sandbox from the host.
type Isolation string

const (
	IsolationWorktree Isolation = "worktree" // host fs/worktree is the boundary
	IsolationGuest    Isolation = "guest"    // the whole guest is the boundary
)

// CapabilitySet is the declared capability surface of a driver.
type CapabilitySet struct {
	Pause          bool
	GuestDial      bool
	Snapshot       bool
	Fork           bool
	SnapshotRemove bool
	NetworkHook    bool
	NetnsState     bool
	SessionAttach  bool
	GuestOS        GuestOS
	Egress         EgressLevel
	Isolation      Isolation
}

// OptionalInterfaces derives the interface-backed flags of a CapabilitySet
// from drv's method set. GuestOS and Egress are left zero; drivers declare them.
func OptionalInterfaces(drv Driver) CapabilitySet {
	var c CapabilitySet
	_, c.Pause = drv.(PauseResumer)
	_, c.GuestDial = drv.(GuestDialer)
	_, c.Snapshot = drv.(Snapshotter)
	_, c.Fork = drv.(Forker)
	_, c.SnapshotRemove = drv.(SnapshotRemover)
	_, c.NetworkHook = drv.(NetworkHook)
	_, c.NetnsState = drv.(NetnsStateProvider)
	_, c.SessionAttach = drv.(SessionAttacher)
	return c
}

// NoGuest is embedded by drivers with no guest exec/file channel (mostly
// test doubles). Every method returns [ErrUnsupported].
type NoGuest struct{}

func (NoGuest) Exec(context.Context, domain.SandboxID, ExecOptions) (int32, error) {
	return 0, ErrUnsupported
}

func (NoGuest) Copy(context.Context, domain.SandboxID, CopyOptions) error {
	return ErrUnsupported
}

func (NoGuest) Capabilities() CapabilitySet { return CapabilitySet{} }

// WorktreeSyncer is optional: substrates without a shared filesystem seed a
// guest checkout from a host repo and export guest commits back. The host
// worktree stays the source of truth; callers type-assert.
type WorktreeSyncer interface {
	// SeedWorktree makes guestDir a checkout of hostRepoDir's ref at the same commit.
	SeedWorktree(ctx context.Context, id domain.SandboxID, hostRepoDir, ref, guestDir string) error
	// ExportWorktree fast-forwards hostRepoDir's branch to guestDir's HEAD and
	// returns the new head. A non-fast-forward is refused.
	ExportWorktree(ctx context.Context, id domain.SandboxID, guestDir, hostRepoDir, branch string) (string, error)
}
