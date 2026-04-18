package runtime

import "context"

// ForkSnapshotter is an optional runtime capability that allows a backend to
// checkpoint a workspace lineage snapshot during fork.
//
// Implementations should return a backend-specific snapshot identifier.
type ForkSnapshotter interface {
	CheckpointFork(ctx context.Context, workspaceID, childWorkspaceID string) (string, error)
}
