package volumestore

import (
	"context"
	"log/slog"
	"path/filepath"

	"github.com/IniZio/nexus/internal/core/audit"
)

// audit records a destructive volume operation. Failures are logged but never
// returned — audit errors must not block the operation itself.
func (s *VolumeStore) audit(ctx context.Context, op string, names []string, err error) {
	stateRoot := s.stateRoot()
	errStr := ""
	if err != nil {
		errStr = err.Error()
	}
	if aerr := audit.Record(ctx, stateRoot, audit.Event{
		Op:      op,
		Volumes: names,
		Err:     errStr,
	}); aerr != nil {
		slog.Warn("volumestore: audit write failed", "op", op, "err", aerr)
	}
}

// stateRoot returns the stateRoot directory (parent of s.root).
func (s *VolumeStore) stateRoot() string {
	return filepath.Dir(s.root)
}
