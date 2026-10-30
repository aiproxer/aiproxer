package plugin

import "context"

// Lifecycle is implemented by plugins that need startup and shutdown hooks after
// construction at the composition root.
type Lifecycle interface {
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
}
