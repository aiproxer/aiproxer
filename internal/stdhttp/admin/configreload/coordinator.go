package configreload

import (
	"context"

	sdkreload "github.com/aiproxer/aiproxer/pkg/aipsdk/configreload"
)

// ReloadCoordinator is the narrow seam onto the runtimehost Coordinator.
// The management adapter never duplicates compile/prepare/publish logic.
type ReloadCoordinator interface {
	Reload(ctx context.Context, trigger sdkreload.Trigger) sdkreload.Result
	Status() sdkreload.Status
	// FixedSourcePath is the HTTP-only capability for the fixed startup source.
	// It must not appear on the canonical Status contract.
	FixedSourcePath() string
}
