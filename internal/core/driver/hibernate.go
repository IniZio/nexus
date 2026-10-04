package driver

import (
	"context"
	"errors"

	"github.com/IniZio/nexus/internal/core/domain"
)

// RestoreMode selects how guest memory is loaded on restore.
type RestoreMode string

const (
	RestoreModeCopy     RestoreMode = "copy"     // read the whole memory file up front
	RestoreModeOnDemand RestoreMode = "ondemand" // fault pages in lazily
)

// ErrHibernateInvalid: the snapshot dir is missing, torn, already attempted or
// already consumed. ErrHibernateIncompatible: it is intact but cannot be
// restored here (CH version or disk set changed). Callers cold-start on both.
var (
	ErrHibernateInvalid      = errors.New("hibernate snapshot invalid")
	ErrHibernateIncompatible = errors.New("hibernate snapshot incompatible")
)

// HibernateResult reports a completed HibernateTo.
type HibernateResult struct {
	PauseMs             int64 // time to pause the guest
	SnapshotMs          int64 // time to write the snapshot
	TotalMs             int64
	SnapshotBytes       int64 // sum of logical file sizes under dir/snap
	SnapshotBytesOnDisk int64 // allocated blocks (stat) under dir/snap
}

// RestoreOptions tunes RestoreInPlace.
type RestoreOptions struct {
	Mode RestoreMode // zero value means copy
}

// RestoreResult reports a completed RestoreInPlace.
type RestoreResult struct {
	RestoreMs int64
	ResumeMs  int64
	// AgentReadyMs is the time from vm.resume until the guest agent answered.
	AgentReadyMs int64
	TotalMs   int64
	Mode      RestoreMode // mode actually used
}

// Hibernator is optional: snapshot a running sandbox's VM state into dir and
// stop it, then later restore the same sandbox from dir.
type Hibernator interface {
	HibernateTo(ctx context.Context, id domain.SandboxID, dir string) (HibernateResult, error)
	// StopAfterHibernate kills the VMM once the hibernated record is committed.
	StopAfterHibernate(ctx context.Context, id domain.SandboxID) error
	RestoreInPlace(ctx context.Context, id domain.SandboxID, dir string, opts RestoreOptions) (RestoreResult, error)
}
