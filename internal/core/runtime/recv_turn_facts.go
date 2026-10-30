package runtime

import (
	"context"
	"errors"
	"log/slog"
	"maps"
	"slices"
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/conversationprojection"
	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/execctx"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/metering/checkpoint"
	"github.com/aiproxer/aiproxer/internal/core/modelcatalog"
	"github.com/aiproxer/aiproxer/internal/core/modelregistry"
	"github.com/aiproxer/aiproxer/internal/core/modelview"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/usageevidence"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/terminaldecision"
)

func (f recvTurnFacts) attemptDiagAttrs(attempt *attemptSession) diag.AttrOpts {
	attrs := diag.AttrOpts{CallID: f.traceID, BLegID: f.aLegID}
	if attempt != nil {
		attrs.BLegID = attempt.bleg.BLegID
	}
	return attrs
}

func (f recvTurnFacts) logDecision(ctx context.Context, logger *slog.Logger, key string, attrs ...slog.Attr) {
	diag.LogDecision(ctx, logger, key, diag.AttrOpts{CallID: f.traceID, BLegID: f.aLegID}, attrs...)
}

func (f recvTurnFacts) logEconomicCheckpointCapacityRejection(ctx context.Context, logger *slog.Logger, attempt *attemptSession, diagnostic economicCheckpointCapacityDiagnostic) {
	diag.LogDecision(ctx, logger, economicCheckpointCapacityRejectionLogMessage, f.attemptDiagAttrs(attempt),
		slog.String("reason", economicCheckpointCapacityRejectionReason),
		slog.String("checkpoint_semantics", string(diagnostic.semantics)),
		slog.Uint64("checkpoint_revision", diagnostic.revision),
	)
}

func (f recvTurnFacts) logRecoverablePreOutput(ctx context.Context, logger *slog.Logger, candidate string, err error) {
	attrs := []slog.Attr{
		slog.String("candidate_key", candidate),
		slog.String("phase", "recv"),
		slog.String("reason", recoverablePreOutputReason(err)),
	}
	f.logDecision(ctx, logger, "recoverable_pre_output_swallowed", attrs...)
}

// recoverablePreOutputReason returns a stable, constant diagnostic category for
// a swallowed pre-output error. The producer error message itself is never
// logged: a native backend error may carry credentials that no pattern-based
// redactor can reliably recognize.
func recoverablePreOutputReason(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, execbackend.ErrPromptCacheLineageConflict):
		return "lineage_conflict"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline_exceeded"
	}
	// Classified backend errors expose a stable machine code; only known
	// constants are surfaced, and the untrusted message is never logged.
	var classified interface{ ClassificationCode() string }
	if errors.As(err, &classified) {
		switch classified.ClassificationCode() {
		case "prompt_cache_lineage":
			return "lineage_conflict"
		case "protocol":
			return "protocol_violation"
		case "transport":
			return "transport_error"
		case "provider":
			return "provider_error"
		case "canceled":
			return "canceled"
		default:
			return "backend_error"
		}
	}
	return "recoverable_pre_output"
}

// recvTurnFactsInput is the assembly-only input for the immutable request facts
// boundary. It is deliberately concrete and private; it is not a turn state bag.
type recvTurnFactsInput struct {
	baseline aipapi.Call
	traceID  string
	aLegID   string

	recvViews   execctx.Views
	recvViewsOK bool
	routePrefs  []string

	secureTurn   execctx.SecureSessionTurn
	secureTurnOK bool

	terminalDecisionPolicy  terminaldecision.PolicySnapshot
	terminalDecisionEnabled bool

	boundRegistry   modelregistry.BoundView
	boundRegistryOK bool
	boundCatalog    modelcatalog.BoundView
	boundCatalogOK  bool
	nativeResolver  routing.NativeModelResolver
	modelViewID     modelview.Identity
	modelViewIDOK   bool
	// auxiliaryRole is the opaque label of the auxiliary request that
	// produced this call. It is captured once, at the assembly boundary,
	// from the core-owned context carrier, so a request-part hook cannot drop
	// it between the request and the provider open.
	auxiliaryRole string

	metering    *checkpoint.RequestHolder
	requestAuth *requestAuthorityState

	billingAccountID       string
	billingStoreID         string
	billingStoreIDStamped  bool
	billingCustomerPricing usageevidence.VersionRef
	billingChargePolicy    usageevidence.VersionRef
	billingIdentityStamped bool
	billingCallID          usageevidence.BillingCallID
	billingCallState       *billingCallState
	submissionID           string

	conversationSnapshot         conversationprojection.Snapshot
	conversationProvenance       []conversationprojection.OverlayProvenance
	conversationFilteredBaseline aipapi.Call
	ingressCall                  aipapi.Call
	continuationIntent           continuationIntentFacts
	wirePayload                  *wireAttemptPayload
}

func withBillingStoreID(input recvTurnFactsInput, storeID string, stamped bool) recvTurnFactsInput {
	input.billingStoreID = storeID
	input.billingStoreIDStamped = stamped
	return input
}

// recvTurnFacts is the request-lifetime authority for facts needed after stream
// assembly. Its slices/maps are owned clones; the referenced owner pointers have
// their own lifecycle and synchronization. It intentionally contains no retry,
// event, terminal, current-attempt, or lock-bearing state.
type recvTurnFacts struct {
	baseline aipapi.Call
	traceID  string
	aLegID   string

	recvViews   execctx.Views
	recvViewsOK bool
	routePrefs  []string

	secureTurn   execctx.SecureSessionTurn
	secureTurnOK bool

	terminalDecisionPolicy  terminaldecision.PolicySnapshot
	terminalDecisionEnabled bool

	boundRegistry   modelregistry.BoundView
	boundRegistryOK bool
	boundCatalog    modelcatalog.BoundView
	boundCatalogOK  bool
	nativeResolver  routing.NativeModelResolver
	modelViewID     modelview.Identity
	modelViewIDOK   bool
	// auxiliaryRole is the opaque label of the auxiliary request that
	// produced this call. It is captured once, at the assembly boundary, from
	// the core-owned context carrier, so it cannot be dropped between the
	// request and the provider open.
	auxiliaryRole string

	metering    *checkpoint.RequestHolder
	requestAuth *requestAuthorityState

	billingAccountID       string
	billingStoreID         string
	billingStoreIDStamped  bool
	billingCustomerPricing usageevidence.VersionRef
	billingChargePolicy    usageevidence.VersionRef
	billingIdentityStamped bool
	billingCallID          usageevidence.BillingCallID
	billingCallState       *billingCallState
	submissionID           string

	conversationSnapshot         conversationprojection.Snapshot
	conversationProvenance       []conversationprojection.OverlayProvenance
	conversationFilteredBaseline aipapi.Call
	ingressCall                  aipapi.Call
	continuationIntent           continuationIntentFacts
	wirePayload                  *wireAttemptPayload
}

func (f requestTerminalFacts) toRecvTurnFacts(ctx context.Context) recvTurnFacts {
	return newRecvTurnFacts(ctx, withBillingStoreID(recvTurnFactsInput{
		baseline:                f.call,
		traceID:                 f.traceID,
		aLegID:                  f.aLegID,
		secureTurn:              f.secureTurn,
		secureTurnOK:            f.secureTurnOK,
		terminalDecisionPolicy:  f.terminalDecisionPolicy,
		terminalDecisionEnabled: f.terminalDecisionEnabled,
		billingCallID:           f.billingCallID,
		billingCallState:        f.billingState,
		submissionID:            f.submissionID,
		billingAccountID:        f.accountID,
		billingCustomerPricing:  f.pricing,
		billingChargePolicy:     f.chargePolicy,
		billingIdentityStamped:  f.identityStamped,
		requestAuth:             f.requestAuth,
		routePrefs:              f.routePrefs,
		recvViews:               f.recvViews,
		recvViewsOK:             true,
		metering:                f.metering,
		conversationSnapshot:    f.conversationSnapshot,
		conversationProvenance:  f.conversationProvenance,
		// newRecvTurnFacts deep-copies every mutable input, including the
		// full-history calls. Passing the frozen values through directly
		// avoids a second copy of each here.
		conversationFilteredBaseline: f.conversationFilteredBaseline,
		ingressCall:                  f.ingressCall,
		continuationIntent:           f.continuationIntent,
		wirePayload:                  f.wirePayload,
	}, f.storeID, f.storeIDStamped))
}

func newRecvTurnFacts(ctx context.Context, in recvTurnFactsInput) recvTurnFacts {
	f := recvTurnFacts(in)
	f.baseline = aipapi.CloneCall(in.baseline)
	if len(in.ingressCall.Items) > 0 || len(in.ingressCall.Messages) > 0 {
		f.ingressCall = aipapi.CloneCall(in.ingressCall)
	} else {
		f.ingressCall = aipapi.CloneCall(in.baseline)
	}
	f.recvViews = cloneRecvViews(in.recvViews)
	f.routePrefs = slices.Clone(in.routePrefs)
	f.conversationSnapshot = cloneSnapshot(in.conversationSnapshot)
	f.conversationProvenance = slices.Clone(in.conversationProvenance)
	f.conversationFilteredBaseline = aipapi.CloneCall(in.conversationFilteredBaseline)
	if f.billingCallState == nil {
		if f.billingCallID == "" {
			if callID, err := usageevidence.NewBillingCallID(); err == nil {
				f.billingCallID = callID
			}
		}
		f.billingCallState = newBillingCallState(f.billingCallID)
	}
	f.captureBoundModelViews(ctx)
	return f
}

func (f recvTurnFacts) clone() recvTurnFacts {
	return newRecvTurnFacts(context.Background(), recvTurnFactsInput(f))
}

// captureBoundModelViews freezes the request's model publications before any
// recv-phase replacement. This is the ONLY allowed business-fact FromContext read;
// it runs at assembly (facts freeze) time, before the typed boundary. After freeze,
// contexts are write-only (projectContext) and must never be read for business facts.
// Explicit input wins over context: if the caller already populated the typed
// field, the context value is ignored, preserving authoritative typed facts.
func (f *recvTurnFacts) captureBoundModelViews(ctx context.Context) {
	if !f.boundRegistryOK {
		if v, ok := modelregistry.BoundViewFromContext(ctx); ok {
			f.boundRegistry = v
			f.boundRegistryOK = true
		}
	}
	if !f.boundCatalogOK {
		if v, ok := modelcatalog.BoundViewFromContext(ctx); ok {
			f.boundCatalog = v
			f.boundCatalogOK = true
		}
	}
	if f.nativeResolver == nil {
		if r, ok := routing.NativeModelResolverFromContext(ctx); ok {
			f.nativeResolver = r
		}
	}
	if !f.modelViewIDOK {
		if id, ok := modelview.FromContext(ctx); ok {
			f.modelViewID = id
			f.modelViewIDOK = true
		}
	}
	if f.auxiliaryRole == "" {
		if role, ok := execctx.AuxiliaryRoleFromContext(ctx); ok {
			f.auxiliaryRole = role
		}
	}
}

// projectContext mirrors authoritative facts into the caller context for
// existing hooks, SDK seams, and diagnostics. It never reads live generation
// state and does not own cancellation or deadlines.
func (f recvTurnFacts) projectContext(parent context.Context, logger *slog.Logger) context.Context {
	ctx := diag.EnsureCallDiag(parent, f.traceID, f.aLegID)

	if f.metering != nil {
		ctx = withMeteringHolder(ctx, f.metering)
	}
	if f.requestAuth != nil {
		ctx = withRequestAuthority(ctx, f.requestAuth)
	}
	if f.recvViewsOK {
		// WithViews takes its own defensive copy of the frozen snapshot, so the
		// projection must not clone the same maps and slices a second time.
		ctx = execctx.WithViews(ctx, f.recvViews)
	}

	if f.secureTurnOK {
		ctx = execctx.WithSecureSessionTurn(ctx, f.secureTurn)
	} else {
		// Overwrite with empty secure turn and mask any inherited policy.
		ctx = execctx.WithSecureSessionTurn(ctx, execctx.SecureSessionTurn{})
		ctx = session.WithoutSecureTurnPolicy(ctx)
	}

	if len(f.routePrefs) > 0 {
		ctx = execctx.WithRouteCandidatePreferences(ctx, slices.Clone(f.routePrefs))
	} else {
		ctx = execctx.WithoutRouteCandidatePreferences(ctx)
	}

	if f.boundRegistryOK {
		ctx = modelregistry.WithBoundView(ctx, f.boundRegistry)
	} else {
		ctx = modelregistry.WithBoundView(ctx, modelregistry.EmptyBoundView())
	}

	if f.boundCatalogOK {
		ctx = modelcatalog.WithBoundView(ctx, f.boundCatalog)
	} else {
		ctx = modelcatalog.WithBoundView(ctx, modelcatalog.EmptyBoundView())
	}

	if f.nativeResolver != nil {
		ctx = routing.WithNativeModelResolver(ctx, f.nativeResolver)
	}

	if f.modelViewIDOK {
		ctx = modelview.WithIdentity(ctx, f.modelViewID)
	} else {
		ctx = modelview.WithIdentity(ctx, modelview.Identity{})
	}

	if logger != nil {
		ctx = hooks.WithDiagnosticsLogger(ctx, logger)
	}
	return ctx
}

// partMeta is the additive part metadata for one attempt. It carries request and
// attempt identity only, so it never reads the frozen request views and allocates
// nothing per event.
func (f recvTurnFacts) partMeta(bleg b2bua.BLegRecord, cand routing.AttemptCandidate) sdk.PartMeta {
	return sdk.PartMeta{
		TraceID: f.traceID, ALegID: f.aLegID, BLegID: bleg.BLegID,
		BackendID: strings.TrimSpace(cand.Primary.Backend), AttemptSeq: bleg.Seq,
	}
}

// hookMeta is [recvTurnFacts.partMeta] plus the tool metadata that carries the
// frozen request views. viewsFor hands out a detached clone on every call, so
// only a consumer that passes the metadata to tool policies or tool reactors
// may ask for it.
func (f recvTurnFacts) hookMeta(bleg b2bua.BLegRecord, cand routing.AttemptCandidate) (sdk.PartMeta, sdk.ToolMeta) {
	pm := f.partMeta(bleg, cand)
	tm := sdk.ToolMeta{TraceID: f.traceID, ALegID: f.aLegID, BLegID: bleg.BLegID, AttemptSeq: bleg.Seq}
	if v, ok := f.viewsFor(nil); ok { //nolint:staticcheck // intentional nil context isolates frozen fact path
		tm.Principal, tm.Scope, tm.Session, tm.Workspace = v.Principal, v.Scope, v.Session, v.Workspace
	}
	return pm, tm
}

func (f recvTurnFacts) viewsFor(_ context.Context) (execctx.Views, bool) {
	// Frozen facts are authoritative; context is write-only for business facts.
	// Missing recvViews is a supported absent optional state, never resurrected from context.
	if f.recvViewsOK {
		return cloneRecvViews(f.recvViews), true
	}
	return execctx.Views{}, false
}

func cloneRecvViews(v execctx.Views) execctx.Views {
	v.Principal.Claims = maps.Clone(v.Principal.Claims)
	v.Principal.Roles = slices.Clone(v.Principal.Roles)
	v.Scope = v.Scope.Clone()
	v.Session.Labels = maps.Clone(v.Session.Labels)
	v.Workspace.Labels = maps.Clone(v.Workspace.Labels)
	v.Workspace.Markers = slices.Clone(v.Workspace.Markers)
	v.Annotations = maps.Clone(v.Annotations)
	return v
}
