package toolcall

import (
	"context"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
)

type Action int

const (
	ActionUnspecified Action = iota
	ActionPass
	ActionRewrite
	ActionReject
)

const (
	ReasonValidPassThrough          = "valid_pass_through"
	ReasonSyntaxRepaired            = "syntax_repaired"
	ReasonToolNameNormalized        = "tool_name_normalized"
	ReasonPropertyRenamed           = "property_renamed"
	ReasonDefaultInserted           = "default_inserted"
	ReasonConstInserted             = "const_inserted"
	ReasonEnumInserted              = "enum_inserted"
	ReasonAdditionalPropertyRemoved = "additional_property_removed"
	ReasonAmbiguousToolName         = "ambiguous_tool_name"
	ReasonAmbiguousProperty         = "ambiguous_property"
	ReasonUnrepairable              = "unrepairable"
	ReasonSchemaInvalid             = "schema_invalid"
	ReasonSchemaUnsupported         = "schema_unsupported"
	ReasonArgsTooLarge              = "args_too_large"
	ReasonScalarCoercionDisabled    = "scalar_coercion_disabled"
	ReasonCanceled                  = "canceled"
)

type CompletedCall struct {
	ToolCallID string
	ToolName   string
	ArgsJSON   []byte
}

// Meta carries read-only execution context for one completed tool-call
// finalization pass.
//
// Scope, Session, and Workspace are the authoritative request views, typed
// exactly as in hooks.ToolMeta: the runtime populates them from the same
// proxy-validated request-scoped snapshot it already hands tool policy and
// tool reactor metadata, so every tool plane reads one and the same state
// instead of a re-derived or client-supplied copy. No client-provided raw
// metadata is ever promoted to authority through this struct.
//
// They are value views, not pointers or interfaces, so the zero value is a
// valid "no authoritative view" state: a zero Scope, Session, and Workspace
// preserve the pre-existing local/anonymous identity semantics of the four
// trace fields, exactly as a zero hooks.ToolMeta does. The pre-existing trace
// fields are unchanged and this extension is additive.
//
// Finalizers must treat all three views as read-only input. Scope roles, safe
// claims and policy labels, Session labels, and Workspace labels and markers
// are reference-typed, so the producer hands over a detached copy and a
// finalizer that needs to retain a view past its call must copy those payloads
// itself.
//
// Detachment is a single producer obligation covering all three views, not a
// per-field SDK helper: [scope.PrincipalScopeView.Clone] deep-copies Scope
// because the SDK already needed it for scope projection, while
// [session.SessionView] and [workspace.WorkspaceView] intentionally expose no
// Clone method and are detached by the producer with standard library copies.
// hooks.ToolMeta's identical Session and Workspace views already rely on exactly
// that producer-side detachment.
type Meta struct {
	TraceID    string
	ALegID     string
	BLegID     string
	AttemptSeq int

	Scope     scope.PrincipalScopeView
	Session   session.SessionView
	Workspace workspace.WorkspaceView
}

type Result struct {
	Action     Action
	ToolName   string
	ArgsJSON   []byte
	ReasonCode string

	// Steer asks core to withhold this completed call and continue the turn
	// once, hidden, with the request's instruction. It is set only with
	// [ActionSteer]; a non-nil Steer on any other action makes the whole
	// result unusable - core takes its pre-existing unusable-result fallback -
	// exactly as an [ActionSteer] whose request does not validate does. See
	// [Result.UsableSteer].
	Steer *SteerRequest
}

// Finalizer is a generation-shared completed-call decision. Methods must be safe
// for concurrent invocation. Finalize must honor ctx and the synchronous work
// ownership contract documented by this package.
type Finalizer interface {
	ID() string
	Order() int
	Finalize(ctx context.Context, call CompletedCall, tool aipapi.ToolDef, catalog []aipapi.ToolDef, meta Meta) (Result, error)
}
