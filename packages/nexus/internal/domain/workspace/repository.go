package workspace

import "context"

// Repository defines persistence operations for Workspace entities.
type Repository interface {
	Create(ctx context.Context, ws *Workspace) error
	Get(ctx context.Context, id string) (*Workspace, error)
	List(ctx context.Context) ([]*Workspace, error)
	Update(ctx context.Context, ws *Workspace) error
	Delete(ctx context.Context, id string) error
}
