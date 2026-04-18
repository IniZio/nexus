package spotlight

import "context"

// Repository defines persistence operations for Forward entities.
type Repository interface {
	Create(ctx context.Context, fwd *Forward) error
	Get(ctx context.Context, id string) (*Forward, error)
	ListByWorkspace(ctx context.Context, workspaceID string) ([]*Forward, error)
	Update(ctx context.Context, fwd *Forward) error
	Delete(ctx context.Context, id string) error
}
