package hooks

import (
	"context"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// PartMeta carries request-scoped identifiers for part hooks (no globals).
type PartMeta struct {
	TraceID    string
	ALegID     string
	BLegID     string
	AttemptSeq int
	BackendID  string
}

// RequestPartHook mutates canonical request parts before backend translation.
// The four-method shape matches [SubmitHook] so the part hook bus can reuse the same chaining
// model (identity, order, fail-open vs fail-closed) without a parallel metadata type.
type RequestPartHook interface {
	ID() string
	Order() int
	FailureMode() FailureMode
	HandleRequestParts(ctx context.Context, call *aipapi.Call, meta PartMeta) error
}

// ResponsePartHook mutates a single canonical stream event before frontend encoding.
// It mirrors RequestPartHook's four-method layout for the same bus and observability reasons.
type ResponsePartHook interface {
	ID() string
	Order() int
	FailureMode() FailureMode
	HandleEvent(ctx context.Context, ev *aipapi.Event, meta PartMeta) error
}
