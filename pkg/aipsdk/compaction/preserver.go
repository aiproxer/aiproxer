package compaction

import (
	"context"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/state"
)

// PreviewKind identifies a content-free detector candidate exposed to a
// preservation callback. A preview never commits detector state or emits an
// Observer event.
type PreviewKind string

const (
	PreviewNone                PreviewKind = "none"
	PreviewStartCandidate      PreviewKind = "start_candidate"
	PreviewCompletionCandidate PreviewKind = "completion_candidate"
)

// RequestPreview is the content-free request-side detector candidate. The
// boundary fingerprint is a stable, non-billable identity for completion-only
// candidates that do not yet have a committed transaction.
type RequestPreview struct {
	Evidence            Evidence
	RuleID              string
	Kind                PreviewKind
	TransactionID       string
	BoundaryFingerprint string
}

// ResponsePreview is the content-free response-side detector candidate. It
// does not mark a transaction complete; committed detection runs after
// preservation finalization on the exact released event.
type ResponsePreview struct {
	Evidence      Evidence
	RuleID        string
	Kind          PreviewKind
	TransactionID string
}

// PreservationMeta carries bounded correlation and detector metadata. It does
// not carry raw prompt, response, capsule, or provider payload content.
type PreservationMeta struct {
	TraceID       string
	SessionID     string
	ALegID        string
	BLegID        string
	AttemptSeq    int
	TransactionID string
	RuleID        string
	Evidence      Evidence
}

// Services exposes narrow preservation capabilities. BackgroundAux is the
// process-owned bounded background collection surface; a nil capability means
// disabled. State remains the ordinary plugin state facade and is safe to leave
// nil.
type Services struct {
	State         state.Store
	BackgroundAux auxiliary.BackgroundClient
}

// Preserver is the content-bearing compaction preservation seam. It is
// deliberately distinct from Observer: callbacks may inspect canonical
// request/response content at the three explicit lifecycle boundaries.
//
// Core invokes BeforeRequest and BeforeResponseRelease transactionally. A
// callback error, panic, or invalid canonical mutation restores the exact
// pre-callback object and is isolated from primary traffic. RequestOpened is
// called only after the primary upstream request opened and is fail-open; its
// content arguments are callback-local defensive copies.
type Preserver interface {
	ID() string
	BeforeRequest(context.Context, *aipapi.Call, RequestPreview, PreservationMeta, Services) error
	RequestOpened(context.Context, aipapi.Call, []Event, PreservationMeta, Services) error
	BeforeResponseRelease(context.Context, *aipapi.Event, ResponsePreview, PreservationMeta, Services) error
}

// RequestOpenFailedPreserver is an optional lifecycle side-channel for a
// preservation implementation that needs to discard a pre-open, non-billable
// intent after the primary open loop exhausts without opening a B-leg. It is
// deliberately separate from Preserver so existing implementations remain
// source-compatible. The callback is metadata-only, synchronous, and fail-open.
type RequestOpenFailedPreserver interface {
	RequestOpenFailed(context.Context, PreservationMeta, Services) error
}

// AfterResponseReleasePreserver is an optional, non-mutating notification at
// the last synchronous point before a canonical event is returned to the
// client. Core passes an isolated event copy; errors and panics are fail-open.
// It is deliberately separate from Preserver so existing implementations
// remain source-compatible.
type AfterResponseReleasePreserver interface {
	AfterResponseRelease(context.Context, aipapi.Event, PreservationMeta, Services) error
}
