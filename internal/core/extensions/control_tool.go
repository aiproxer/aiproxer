package extensions

import (
	"context"
	"errors"
	"fmt"

	"github.com/aiproxer/aiproxer/internal/core/safety"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/controltool"
)

// ErrControlHandleFailed is the single static, content-free failure every
// provider-side control-handler problem collapses into. It exists so a caller can
// classify a handled control call as failed with one sentinel while the concrete
// cause — a provider error carrying arbitrary text, an oversized or unknown
// outcome, or a handler that violates the frozen SDK contract — never reaches a
// client surface, a log line, or a metric label.
var ErrControlHandleFailed = errors.New("extensions: control tool handler failed")

// ControlToolProjectionRequest is the generic input of the one bounded
// control-projection decision taken for a single candidate call. It carries no
// client-derived provenance: ProviderID is the generation-frozen identity the
// caller already pinned, and Provider is the provider that same generation
// admitted.
type ControlToolProjectionRequest struct {
	// ProviderID is the frozen identity of the generation control-tool provider.
	// It is validated here so an unnamed owner can never project a contract.
	ProviderID string
	// Provider is the generation provider. The caller must branch on snapshot
	// absence before calling, because this stage always calls the provider.
	Provider controltool.Provider
	// Call is the candidate call after ordinary request mutation.
	Call aipapi.Call
	// Caps are the candidate's own resolved backend capabilities. They are
	// eligibility input for the optional control feature, never authoritative
	// admission or accounting; the caller's own rederive stays authoritative.
	Caps aipapi.BackendCaps
}

// ControlToolProjection is the trusted, in-process result of one control
// projection. Everything it holds is deep-owned and request/attempt-local: it is
// never canonical Extensions, never serialized frontend or backend metadata, and
// never reconstructed from a tool name observed on the wire.
type ControlToolProjection struct {
	// ProviderID is the validated frozen identity of the pinned provider.
	ProviderID string
	// Provider is the pinned generation provider. It is retained so later
	// response handling addresses the same generation instance, never a
	// re-resolved one.
	Provider controltool.Provider
	// Projection is the immutable approved projection. An inactive projection
	// keeps its bounded reason and never claims a proxy-owned control call.
	Projection controltool.Projection
}

// Active reports whether the control tool and instruction were approved for the
// projected candidate.
func (p ControlToolProjection) Active() bool { return p.Projection.Active() }

// Reason returns the bounded, content-free reason an inactive projection carries.
func (p ControlToolProjection) Reason() string { return p.Projection.Reason() }

// ProjectControlTool runs the single generic control-projection decision for
// one candidate call and returns the backend-effective call plus the trusted
// activation the caller must own for the rest of the attempt.
//
// The provider spec is read exactly once per candidate behind the extension
// safety boundary, so a provider panic becomes a bounded, content-free error
// instead of a candidate mutation or a client-visible failure. The pure
// projection owns eligibility and the byte-stable contract, so this stage adds no
// tool-name, feature-name, or eligibility policy of its own.
//
// An invalid spec is a generation defect, not a per-request best effort: it is
// returned as an error and the call is returned unchanged. Ordinary
// ineligibility is not an error; the projection carries its bounded reason and
// the call is returned exactly as supplied.
func ProjectControlTool(in ControlToolProjectionRequest) (aipapi.Call, ControlToolProjection, error) {
	if in.Provider == nil {
		return in.Call, ControlToolProjection{}, fmt.Errorf("extensions: %w: control tool provider is required", controltool.ErrInvalidProvider)
	}
	if err := controltool.ValidateProviderID(in.ProviderID); err != nil {
		return in.Call, ControlToolProjection{}, err
	}
	spec, err := safety.CallValue(safety.BoundaryExtension, "control_tool_provider_spec", func() (controltool.Spec, error) {
		return in.Provider.Spec(), nil
	})
	if err != nil {
		return in.Call, ControlToolProjection{ProviderID: in.ProviderID}, err
	}
	projected, projection, err := controltool.Project(in.Call, spec, in.Caps)
	if err != nil {
		return in.Call, ControlToolProjection{ProviderID: in.ProviderID}, err
	}
	return projected, ControlToolProjection{
		ProviderID: in.ProviderID,
		Provider:   in.Provider,
		Projection: projection,
	}, nil
}

// ControlToolHandleRequest is the generic input of the single bounded
// control-handler stage. Every field is frozen request/attempt provenance the
// caller already captured when it admitted the control contract: this stage
// never re-resolves a runtime snapshot, never calls Provider.ID or Provider.Spec,
// and never re-derives capability eligibility.
type ControlToolHandleRequest struct {
	// ProviderID is the generation-frozen identity that admitted the projection.
	// It is revalidated here so an unnamed owner can never handle a call.
	ProviderID string
	// Provider is the pinned generation provider instance. The caller must branch
	// on absence before calling, because this stage always calls Handle.
	Provider controltool.Provider
	// Call is the one already-captured, bounded completion. Its bytes are owned by
	// the caller: they are validated against MaxArgsBytes and handed to the
	// provider by value, and are never logged, echoed, or retained here.
	Call controltool.CompletedCall
	// Meta is the frozen request/attempt provenance handed to the provider. The
	// platform owns it; the provider cannot mutate it.
	Meta controltool.Meta
	// MaxArgsBytes is the frozen args budget from the approved projection. It is
	// the only budget this stage applies, and it is never recomputed from a spec,
	// a request, or a wire event.
	MaxArgsBytes int
}

// HandleControlTool invokes the pinned control provider at most once for one
// completed, bounded call and returns a normalized, content-free outcome.
//
// The provider call runs behind the extension safety boundary, so a panic becomes
// a bounded isolated-panic error rather than an attempt-level crash. Every
// returned outcome is re-checked with the pure SDK validator before it leaves
// this stage: OutcomeInvalid must carry a bounded content-free reason and no
// result, OutcomeComplete must carry bounded non-empty UTF-8 result text, and an
// unknown kind is rejected. A provider error, an invalid outcome, and an unknown
// kind all collapse into [ErrControlHandleFailed], so no provider text, argument
// byte, call ID, or result byte can reach a caller, a log, or a metric.
//
// An already-canceled context is reported as cancellation before the provider is
// invoked. The caller's own context is passed through unchanged: this stage adds
// no goroutine, no deadline, and no new timeout budget.
func HandleControlTool(ctx context.Context, in ControlToolHandleRequest) (controltool.Outcome, error) {
	if in.Provider == nil {
		return controltool.Outcome{}, fmt.Errorf("extensions: %w: control tool provider is required", controltool.ErrInvalidProvider)
	}
	// The frozen request is validated with the same pure SDK validators the
	// provider contract is defined by, so this stage owns no duplicate identity,
	// provenance, or budget policy. A rejected request is a wiring defect and is
	// reported as such; the provider is never invoked for it.
	if err := controltool.ValidateProviderID(in.ProviderID); err != nil {
		return controltool.Outcome{}, err
	}
	if err := controltool.ValidateMeta(in.Meta); err != nil {
		return controltool.Outcome{}, err
	}
	if err := controltool.ValidateCompletedCall(in.Call, in.MaxArgsBytes); err != nil {
		return controltool.Outcome{}, err
	}
	if err := ctx.Err(); err != nil {
		return controltool.Outcome{}, err
	}
	// The provider call is the only provider interaction, and it runs behind the
	// extension safety boundary so a panic becomes an isolated-panic error rather
	// than an attempt-level crash. Spec() and ID() are never called: the frozen
	// identity above is what addresses this generation instance.
	outcome, err := safety.CallValue(safety.BoundaryExtension, "control_tool_handle", func() (controltool.Outcome, error) {
		return in.Provider.Handle(ctx, in.Call, in.Meta)
	})
	if err != nil {
		// Cancellation stays distinguishable so the caller honors it; every other
		// provider-side failure, including an isolated panic with its captured
		// server stack, collapses into the static sentinel. The raw error is
		// deliberately neither wrapped nor logged: it is provider-controlled text
		// and would otherwise reach operator attempt records and terminal detail.
		if ctxErr := ctx.Err(); ctxErr != nil {
			return controltool.Outcome{}, ctxErr
		}
		return controltool.Outcome{}, ErrControlHandleFailed
	}
	// A provider may return an apparently valid outcome from a context that is
	// already canceled. Cancellation wins, so a canceled turn cannot smuggle an
	// outcome out through the handler.
	if ctxErr := ctx.Err(); ctxErr != nil {
		return controltool.Outcome{}, ctxErr
	}
	if err := controltool.ValidateOutcome(outcome); err != nil {
		// An unknown kind, a missing or non-content-free reason code, an invalid
		// outcome carrying result text, and empty/oversized/NUL/invalid-UTF-8
		// result text are all one static failure. The validator's own message may
		// quote field names only, but the returned sentinel keeps every such
		// detail, including any oversized value, off every caller-visible surface.
		return controltool.Outcome{}, ErrControlHandleFailed
	}
	return outcome, nil
}
