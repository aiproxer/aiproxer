package aipsdk

import (
	"context"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// ExecutorView is the minimal inbound surface bundled [FrontendMount] handlers use to invoke
// core request execution. The public SDK cannot import the internal runtime package, so this
// narrow type is the supported compile-time seam. This follows introduce-hexagonal-architecture
// requirement 8.6: prefer concrete executor types where package boundaries allow, and add inbound
// interfaces only for a real module-boundary or multi-consumer need, not a generic "ports" layer.
// Widen the contract only when justified. The standard *runtime.Executor implements this interface.
type ExecutorView interface {
	// Execute requires a non-nil ctx (same as [context.Context] contract for all request paths).
	Execute(ctx context.Context, call *aipapi.Call) (aipapi.EventStream, error)
	// CancelALeg explicitly cancels a proxy-owned A-leg and its active B-legs.
	CancelALeg(ctx context.Context, req aipapi.ALegCancelRequest) error
	// WallClock returns the optional wall clock callback used for response metadata; nil means unset.
	WallClock() func() time.Time
}
