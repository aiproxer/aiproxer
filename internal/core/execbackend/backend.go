// Package execbackend defines the executor-consumed outbound seam for opening
// canonical backend attempts (introduce-hexagonal-architecture). Concrete backend
// plugins and composition roots construct [Backend] values; the executor consumes
// them without importing provider or transport packages.
package execbackend

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	accountingapp "github.com/aiproxer/aiproxer/internal/core/tokenaccounting/app"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/modelinventory"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/promptcache"
)

// Backend opens a canonical event stream for one route candidate.
// Client operation and delivery metadata are carried on [aipapi.Call].Invocation.
type Backend struct {
	Caps aipapi.BackendCaps
	// ResolveCaps, when set, supplies model/candidate-aware capabilities; otherwise Caps is used.
	ResolveCaps func(ctx context.Context, call aipapi.Call, cand routing.AttemptCandidate) aipapi.BackendCaps
	// ResolveWireCaps, when set, supplies candidate/model-aware capabilities for wire fast-path evaluation without requiring a synthesized aipapi.Call.
	ResolveWireCaps func(ctx context.Context, cand routing.AttemptCandidate) aipapi.BackendCaps

	// ResolveEffortSupport, when set, reports whether the provider carries one
	// reasoning-effort value for the candidate's model. It is called with the
	// call that will be attempted, so a call-sensitive provider may answer per
	// call. A nil callback means the provider published no fact: the answer is
	// EffortSupportUnknown, never a refusal.
	ResolveEffortSupport func(ctx context.Context, call aipapi.Call, cand routing.AttemptCandidate, effort string) EffortSupportState
	TransportCaps        aipapi.BackendTransportCaps
	// ResolveTransportCaps, when set, supplies model/candidate-aware transport capabilities; otherwise TransportCaps is used.
	ResolveTransportCaps func(ctx context.Context, call aipapi.Call, cand routing.AttemptCandidate) aipapi.BackendTransportCaps
	Open                 func(ctx context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error)
	ModelInventory       modelinventory.Provider
	// BackendPrefixes names this connector kind for model-inventory discovery. Prefixes may be
	// shared by instances of the same backend kind, but different backend kinds must not claim the
	// same prefix. Canonical model IDs must not use the qualifier form "<prefix>:<canonical-id>".
	BackendPrefixes []string
	// ReplaySupport is the static historical-reasoning dialect profile for this backend.
	// Prefer ResolveReplaySupport when support depends on candidate/model.
	ReplaySupport aipapi.ReasoningReplaySupport
	// ResolveReplaySupport, when set, supplies candidate/model-aware replay support; otherwise ReplaySupport is used.
	ResolveReplaySupport func(ctx context.Context, call aipapi.Call, cand routing.AttemptCandidate) aipapi.ReasoningReplaySupport

	// DialectSupport declares exact item/reasoning/compaction/extension dialects this backend satisfies.
	DialectSupport aipapi.DialectSupport
	// ResolveDialectSupport, when set, supplies candidate/model-aware dialect support; otherwise DialectSupport is used.
	ResolveDialectSupport func(ctx context.Context, call aipapi.Call, cand routing.AttemptCandidate) aipapi.DialectSupport

	// ResolvePromptCacheProfile supplies model/candidate-aware provider-neutral
	// residency capability. nil means observation/control unsupported.
	ResolvePromptCacheProfile func(context.Context, aipapi.Call, routing.AttemptCandidate) promptcache.Profile
	// RenewPromptCache and ReleasePromptCache are direct operations on an
	// already-issued backend-owned handle. They never receive a selector and
	// must not invoke ordinary route selection or inference.
	RenewPromptCache   func(context.Context, promptcache.RenewRequest) (promptcache.RenewResponse, error)
	ReleasePromptCache func(context.Context, promptcache.ReleaseRequest) error

	BillingFinalizationSupported bool
	FinalizeBilling              func(ctx context.Context, in BillingFinalizationInput) (aipapi.Event, error)
	// FinalizeBillingV2 preserves host-only economic observations returned by
	// an executable connector. FinalizeBilling remains the compatibility
	// projection consumed by legacy authority paths.
	FinalizeBillingV2 func(ctx context.Context, in BillingFinalizationInput) (BillingFinalizationResult, error)

	// EnforcesMaxOutputTokens reports whether this backend serializes a
	// non-nil/positive MaxOutputTokens onto the provider wire so an authority
	// spend-cap clamp actually binds. Zero value (false) is fail-closed: the
	// executor excludes candidates that cannot represent the clamp rather than
	// opening with an unenforced limit. Backends that drop or omit the option
	// (Codex, ACP family, OpenCode, local-stub) must leave this false.
	EnforcesMaxOutputTokens bool
	// IgnoresAuthorityMaxOutputTokensClamp, when set, reports call-specific reasons
	// the backend drops MaxOutputTokens despite EnforcesMaxOutputTokens.
	IgnoresAuthorityMaxOutputTokensClamp func(call aipapi.Call) bool

	ProviderCounter accountingapp.ProviderCounter
	// LocalCounter, when set, supplies instance-local tokenizer counting for
	// compatible modes configured with an explicit tokenizer override.
	LocalCounter accountingapp.LocalCounter
	// TokenizerID is the bounded configured local tokenizer identifier exposed
	// for diagnostics and accounting when LocalCounter is attached.
	TokenizerID string

	// Close, when non-nil, releases backend-owned persistent runtime resources
	// (for example a companion process). nil means the backend owns no such
	// resources or cleans them through individual streams. Callers treat a
	// non-nil callback as idempotent; it is not a request-cancellation API.
	Close func() error

	// Optional generation-local lifecycle / transport seams. nil means unsupported.
	// Composition roots adapt these into candidate prepare/rollback ownership;
	// legacy backends that only set Close remain fully compatible.
	Start                 func(context.Context) error
	Stop                  func(context.Context) error
	CleanupIdleTransports func(context.Context) error
	// PreflightCapability, when set, is an explicit non-billable readiness probe.
	// It is never invoked automatically as a publication gate.
	PreflightCapability func(context.Context) (CapabilityPreflight, error)

	// ResolveWireRequest, when set, supplies candidate-aware pure exact wire
	// capability proof for one candidate (design section 9, Requirements 8, 9).
	// nil means wire execution unsupported (canonical fallback).
	ResolveWireRequest func(ctx context.Context, facts largebody.WireRequestFacts, cand routing.AttemptCandidate) largebody.WireRequestSupport

	// ResolveWireDomain, when set, supplies pure late-route domain wire
	// capability proof (Requirement 7, design section 9).
	// nil means wire execution unsupported (canonical fallback).
	ResolveWireDomain func(ctx context.Context, facts largebody.WireDomainFacts) largebody.WireDomainSupport

	// WireBackend, when non-nil, provides the optional wire capability interface.
	WireBackend largebody.WireBackend

	// OpenWire, when set, opens a backend attempt directly from a wire request
	// without constructing a aipapi.Call (design section 9, Requirement 8).
	OpenWire func(ctx context.Context, req largebody.WireOpenRequest) (aipapi.ManagedEventStream, error)
}

// CapabilityPreflight is an optional non-billable backend readiness probe result.
// Billable must be false when consumed by candidate composition (req 8.11).
type CapabilityPreflight struct {
	Ready       bool
	Billable    bool
	Description string
}

type BillingFinalizationInput struct {
	TraceID string
	ALegID  string
	BLegID  string
	Backend string
	Model   string
	Reason  string
}

// BillingFinalizationResult keeps the canonical compatibility usage event and
// the additive host-only V2 observations from one idempotent finalizer call.
// The executor owns terminal capture; no finalizer result is client content.
type BillingFinalizationResult struct {
	Usage            aipapi.Event
	EconomicEvidence []EconomicEvidence
}

// EconomicEvidence is the neutral core handoff for one validated connector
// observation. Coverage is transport metadata retained through the adapter
// boundary; Observation is the canonical metering model consumed by core.
// Keeping this carrier in execbackend prevents core from importing gRPC or the
// executable backend-plugin wire package.
type EconomicEvidence struct {
	Observation    metering.Observation
	Coverage       string
	CoverageReason string
	// RejectionReason carries the closed rejection token of a claim the host
	// could not admit. It is empty for an admitted claim. A rejected claim
	// carries no observation and no disposition: a disposition requires a
	// retained observation.
	RejectionReason string
}

// EconomicEvidenceSource is the narrow host-to-core drain seam for the
// neutral finalizer/stream evidence carrier. It is intentionally separate
// from the executable connector ABI and has no transport dependency.
type EconomicEvidenceSource interface {
	DrainEconomicEvidenceRecords() []EconomicEvidence
}

// EffectiveCaps returns the caps used for negotiation for one backend and candidate.
func EffectiveCaps(
	ctx context.Context,
	be Backend,
	call aipapi.Call,
	cand routing.AttemptCandidate,
) aipapi.BackendCaps {
	if be.ResolveCaps != nil {
		return be.ResolveCaps(ctx, call, cand)
	}
	return be.Caps
}

// EffectiveTransportCaps returns the transport caps used for negotiation for one backend and candidate.
func EffectiveTransportCaps(
	ctx context.Context,
	be Backend,
	call aipapi.Call,
	cand routing.AttemptCandidate,
) aipapi.BackendTransportCaps {
	if be.ResolveTransportCaps != nil {
		return be.ResolveTransportCaps(ctx, call, cand)
	}
	return be.TransportCaps
}

func EffectiveReplaySupport(
	ctx context.Context,
	be Backend,
	call aipapi.Call,
	cand routing.AttemptCandidate,
) aipapi.ReasoningReplaySupport {
	support := be.ReplaySupport
	if be.ResolveReplaySupport != nil {
		support = be.ResolveReplaySupport(ctx, call, cand)
	}
	support.Dialects = aipapi.NormalizeReasoningDialects(support.Dialects)
	return support
}

func EffectiveDialectSupport(
	ctx context.Context,
	be Backend,
	call aipapi.Call,
	cand routing.AttemptCandidate,
) aipapi.DialectSupport {
	support := be.DialectSupport
	if be.ResolveDialectSupport != nil {
		support = be.ResolveDialectSupport(ctx, call, cand)
	}
	replay := EffectiveReplaySupport(ctx, be, call, cand)
	for _, d := range replay.Dialects {
		support.ReasoningDialects = append(support.ReasoningDialects, aipapi.DialectRequirement{
			Kind:    "reasoning",
			Dialect: string(aipapi.NormalizeReasoningDialect(d)),
		})
	}
	return aipapi.NormalizeDialectSupport(support)
}

func CloneBackendPrefixes(be Backend) []string {
	return slices.Clone(be.BackendPrefixes)
}

// EffectivePromptCacheProfile resolves capability for the selected effective
// model/candidate. A nil resolver is intentionally observation/control unknown.
func EffectivePromptCacheProfile(ctx context.Context, be Backend, call aipapi.Call, cand routing.AttemptCandidate) promptcache.Profile {
	if be.ResolvePromptCacheProfile == nil {
		return promptcache.Profile{}
	}
	profile := be.ResolvePromptCacheProfile(ctx, call, cand)
	if normalized, err := profile.Normalize(); err == nil {
		return normalized
	}
	return promptcache.Profile{}
}

// ErrPromptCacheLineageConflict is returned when a producer-declared
// prompt-cache observation claims an A-leg, B-leg, or backend instance that
// contradicts the trusted attempt lineage.
var ErrPromptCacheLineageConflict = errors.New("execbackend: prompt-cache observation lineage conflict")

// ConnectorLegPseudonym is the transport leg pseudonym the host supplies to an
// executable connector for one route candidate. It is the only non-attempt leg
// value an observation may echo back on that transport.
func ConnectorLegPseudonym(candidateKey, primaryString string) string {
	bLeg := strings.TrimSpace(candidateKey)
	if bLeg == "" {
		bLeg = strings.TrimSpace(primaryString)
	}
	if bLeg == "" {
		bLeg = "bleg"
	}
	return bLeg
}

// PromptCacheObservationLineage is the trusted attempt lineage used to bind a
// drained prompt-cache observation. ConnectorLegPseudonym, when non-empty, is
// the exact transport pseudonym the host already supplied to that connector;
// no other producer-declared leg value is accepted.
type PromptCacheObservationLineage struct {
	ALegID                string
	BLegID                string
	BackendInstanceID     string
	ConnectorLegPseudonym string
}

// PromptCacheLineageConflictClass identifies which trusted identity field a
// producer-declared observation contradicted. It is a constant diagnostic
// category; it never carries the producer-declared value.
type PromptCacheLineageConflictClass string

const (
	PromptCacheLineageConflictALeg            PromptCacheLineageConflictClass = "a_leg"
	PromptCacheLineageConflictBLeg            PromptCacheLineageConflictClass = "b_leg"
	PromptCacheLineageConflictBackendInstance PromptCacheLineageConflictClass = "backend_instance"
)

// PromptCacheLineageConflictError carries only the stable conflict class. The
// producer-declared identity values are untrusted and may contain credential
// text, so they are deliberately never embedded in the error (which is safe to
// return internally and to log as a category); errors.Is matches
// [ErrPromptCacheLineageConflict].
type PromptCacheLineageConflictError struct {
	Class PromptCacheLineageConflictClass
}

func (e *PromptCacheLineageConflictError) Error() string {
	if e == nil {
		return ErrPromptCacheLineageConflict.Error()
	}
	return ErrPromptCacheLineageConflict.Error() + ": " + string(e.Class)
}

func (e *PromptCacheLineageConflictError) Unwrap() error { return ErrPromptCacheLineageConflict }

// PromptCacheLineageConflictClassOf reports the stable conflict class carried
// by err, if any, without exposing producer-declared identity.
func PromptCacheLineageConflictClassOf(err error) (PromptCacheLineageConflictClass, bool) {
	var conflict *PromptCacheLineageConflictError
	if errors.As(err, &conflict) && conflict != nil {
		return conflict.Class, true
	}
	return "", false
}

// ReconcilePromptCacheObservationLineage validates a producer-declared
// observation against the trusted attempt lineage and fills only the identity
// fields the producer left empty. A non-empty field that conflicts with the
// trusted value (directly or through the validated connector pseudonym) is
// rejected with [ErrPromptCacheLineageConflict], so a foreign session/backend
// observation can never be rewritten into trusted facts. Target, handle, and
// evidence identity is always left untouched.
func ReconcilePromptCacheObservationLineage(o promptcache.Observation, trusted PromptCacheObservationLineage) (promptcache.Observation, error) {
	switch {
	case o.ALegID == "":
		o.ALegID = trusted.ALegID
	case o.ALegID != trusted.ALegID:
		return promptcache.Observation{}, &PromptCacheLineageConflictError{Class: PromptCacheLineageConflictALeg}
	}
	switch {
	case o.BLegID == "":
		o.BLegID = trusted.BLegID
	case o.BLegID == trusted.BLegID:
		// Native/in-process producers report the attempt B-leg directly.
	case trusted.ConnectorLegPseudonym != "" && o.BLegID == trusted.ConnectorLegPseudonym:
		o.BLegID = trusted.BLegID
	default:
		return promptcache.Observation{}, &PromptCacheLineageConflictError{Class: PromptCacheLineageConflictBLeg}
	}
	switch {
	case o.BackendInstanceID == "":
		o.BackendInstanceID = trusted.BackendInstanceID
	case o.BackendInstanceID != trusted.BackendInstanceID:
		return promptcache.Observation{}, &PromptCacheLineageConflictError{Class: PromptCacheLineageConflictBackendInstance}
	}
	return o, nil
}

// DrainPromptCacheObservations drains only the optional host-only sideband.
// Implementations decide whether a successful terminal committed the buffer.
func DrainPromptCacheObservations(stream aipapi.ManagedEventStream) []promptcache.Observation {
	if stream == nil {
		return nil
	}
	source, ok := stream.(promptcache.ObservationSource)
	if !ok {
		return nil
	}
	return source.DrainPromptCacheObservations()
}
