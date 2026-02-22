package interfaces

import "context"

type MutagenClient interface {
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
}
