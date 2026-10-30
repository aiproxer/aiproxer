package adapter

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	accountingapp "github.com/aiproxer/aiproxer/internal/core/tokenaccounting/app"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/processhost"
	"github.com/aiproxer/aiproxer/internal/plugins/features/codexclientcompat"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/modelinventory"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/promptcache"
)

// Options configures adapter deadlines and stream bounds.
type Options struct {
	MetadataTimeout      time.Duration
	MaxPendingEvents     int
	MaxStreamFrame       int
	MaxStderrBytes       int
	CancelTimeout        time.Duration
	DisableGRPCRetry     bool
	RoutePrefixes        []string
	InstanceID           string
	EnforcesMaxOutput    bool
	Stderr               io.Reader
	InvalidateGeneration func()
	Negotiation          backendplugin.Negotiation
	// ConnectorEconomicEvidencePolicy selects the host's reaction to connector
	// economic evidence it cannot admit. The default preserves the client
	// session with an explicit, durable degradation.
	ConnectorEconomicEvidencePolicy execbackend.ConnectorEconomicEvidencePolicy
}

// Build constructs a composition-owned backend from a configured plugin session.
func Build(session ExecuteSession, profile backendplugin.ResolvedProfile, opt Options) *processhost.BuildResult {
	if opt.MetadataTimeout <= 0 {
		opt.MetadataTimeout = 5 * time.Second
	}
	if opt.MaxPendingEvents <= 0 {
		opt.MaxPendingEvents = 64
	}
	if opt.CancelTimeout <= 0 {
		opt.CancelTimeout = 2 * time.Second
	}
	if isZeroNegotiation(opt.Negotiation) {
		if negger, ok := session.(NegotiatedSession); ok {
			opt.Negotiation = negger.Negotiation()
		}
	}
	opt.DisableGRPCRetry = true

	prefixes := append([]string(nil), opt.RoutePrefixes...)
	if len(prefixes) == 0 {
		prefixes = append([]string(nil), profile.RoutePrefixes...)
	}

	be := execbackend.Backend{
		Caps:                         capsToAipapi(profile.Capabilities),
		DialectSupport:               backendplugin.DialectSupportToAipapi(profile.DialectSupport),
		EnforcesMaxOutputTokens:      opt.EnforcesMaxOutput || profile.EnforceMaxOutput,
		BackendPrefixes:              prefixes,
		BillingFinalizationSupported: profile.SupportsFinalizeBilling,
	}
	if be.EnforcesMaxOutputTokens {
		be.IgnoresAuthorityMaxOutputTokensClamp = codexclientcompat.IgnoresClampViaCodexUnsupportedGenParams
	}

	be.ResolveCaps = func(ctx context.Context, call aipapi.Call, cand routing.AttemptCandidate) aipapi.BackendCaps {
		model := cand.Primary.Model
		var mid *string
		if model != "" {
			mid = &model
		}
		p, err := session.Resolve(ctx, mid)
		if err != nil {
			return be.Caps
		}
		return capsToAipapi(p.Capabilities)
	}
	be.ResolvePromptCacheProfile = func(ctx context.Context, call aipapi.Call, cand routing.AttemptCandidate) promptcache.Profile {
		model := cand.Primary.Model
		var mid *string
		if model != "" {
			mid = &model
		}
		p, err := session.Resolve(ctx, mid)
		if err != nil {
			return promptcache.Profile{}
		}
		return p.PromptCacheProfile
	}
	be.ResolveDialectSupport = func(ctx context.Context, call aipapi.Call, cand routing.AttemptCandidate) aipapi.DialectSupport {
		model := cand.Primary.Model
		var mid *string
		if model != "" {
			mid = &model
		}
		p, err := session.Resolve(ctx, mid)
		if err != nil {
			return be.DialectSupport
		}
		return backendplugin.DialectSupportToAipapi(p.DialectSupport)
	}

	// Provider-owned graded effort facts ride the negotiated ResolveProfile
	// response. Unnegotiated sessions keep a nil callback (Unknown).
	if backendplugin.ReasoningEffortValuesNegotiated(opt.Negotiation) {
		be.ResolveEffortSupport = func(ctx context.Context, call aipapi.Call, cand routing.AttemptCandidate, effort string) execbackend.EffortSupportState {
			return resolveEffortSupport(ctx, session, opt.MetadataTimeout, cand, effort)
		}
	}

	be.ResolveTransportCaps = func(_ context.Context, call aipapi.Call, _ routing.AttemptCandidate) aipapi.BackendTransportCaps {
		return transportCapsFromProfile(profile, call.Invocation.Operation)
	}
	be.TransportCaps = transportCapsFromProfile(profile, "")

	if profile.ReasoningReplaySupported || profile.Capabilities.ReasoningReplay {
		be.ResolveReplaySupport = func(ctx context.Context, call aipapi.Call, cand routing.AttemptCandidate) aipapi.ReasoningReplaySupport {
			model := cand.Primary.Model
			var mid *string
			if model != "" {
				mid = &model
			}
			p, err := session.Resolve(ctx, mid)
			if err != nil {
				return aipapi.ReasoningReplaySupport{}
			}
			if !p.ReasoningReplaySupported && !p.Capabilities.ReasoningReplay {
				return aipapi.ReasoningReplaySupport{}
			}
			return aipapi.ReasoningReplaySupport{
				Dialects: []aipapi.ReasoningDialect{aipapi.ReasoningDialectOpenAIResponsesItemV1},
			}
		}
	}

	if profile.PromptCacheProfile.RenewalSupported {
		if controller, ok := session.(OptionalPromptCacheController); ok && backendplugin.PromptCacheNegotiated(opt.Negotiation) {
			be.RenewPromptCache = controller.RenewPromptCache
			be.ReleasePromptCache = controller.ReleasePromptCache
		}
	}
	if profile.SupportsDynamicInventory {
		be.ModelInventory = &inventoryProvider{session: session, timeout: opt.MetadataTimeout, source: profile.EvidenceSource}
	}
	if profile.SupportsFinalizeBilling {
		if fin, ok := session.(OptionalBillingFinalizer); ok {
			finalize := func(ctx context.Context, in execbackend.BillingFinalizationInput) (execbackend.BillingFinalizationResult, error) {
				cctx, cancel := context.WithTimeout(ctx, opt.MetadataTimeout)
				defer cancel()
				resp, err := fin.FinalizeBilling(cctx, backendplugin.FinalizeBillingRequest{
					InstanceID: opt.InstanceID, ALegID: in.ALegID, BLegID: in.BLegID,
					ModelID: in.Model, Reason: in.Reason, IdempotencyKey: finalizationIdempotencyKey(in),
				})
				if err != nil {
					return execbackend.BillingFinalizationResult{}, err
				}
				usage, err := finalizeBillingResponseToEvent(resp, finalizationIdempotencyKey(in))
				if err != nil {
					return execbackend.BillingFinalizationResult{}, err
				}
				evidence := make([]execbackend.EconomicEvidence, len(resp.AccountingV2))
				for i, item := range resp.AccountingV2 {
					evidence[i] = execbackend.EconomicEvidence{
						Observation:    item.Observation.Clone(),
						Coverage:       string(item.Coverage),
						CoverageReason: item.CoverageReason,
					}
				}
				return execbackend.BillingFinalizationResult{Usage: usage, EconomicEvidence: evidence}, nil
			}
			if profile.SupportsAccountingEvidenceV2 && backendplugin.AccountingEvidenceV2Negotiated(opt.Negotiation) {
				be.FinalizeBillingV2 = finalize
			}
			be.FinalizeBilling = func(ctx context.Context, in execbackend.BillingFinalizationInput) (aipapi.Event, error) {
				result, err := finalize(ctx, in)
				if err == nil && len(result.EconomicEvidence) != 0 {
					return aipapi.Event{}, backendplugin.ErrAccountingEvidenceV2Unsupported
				}
				return result.Usage, err
			}
		}
	}
	if profile.SupportsCountTokens {
		if counter, ok := session.(OptionalTokenCounter); ok {
			be.ProviderCounter = &tokenCounterBridge{session: counter, instanceID: opt.InstanceID, timeout: opt.MetadataTimeout, negotiation: opt.Negotiation}
		}
	}
	be.Open = func(ctx context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
		return openStream(ctx, session, opt, call, cand)
	}
	return processhost.NewBuildResult(be, func() error {
		return session.Close(context.Background())
	})
}

func finalizationIdempotencyKey(in execbackend.BillingFinalizationInput) string {
	return "finalize-billing:v1:" + strings.TrimSpace(in.TraceID) + ":" + strings.TrimSpace(in.ALegID) + ":" + strings.TrimSpace(in.BLegID)
}

func transportCapsFromProfile(profile backendplugin.ResolvedProfile, op aipapi.Operation) aipapi.BackendTransportCaps {
	if op == "" {
		return nil
	}
	if !profile.Capabilities.Streaming && !profile.TransportCapabilities.BidirectionalStream {
		return nil
	}
	modes := make([]aipapi.TransportMode, 0, 2)
	if profile.Capabilities.Streaming {
		modes = append(modes, aipapi.TransportModeStreaming)
	}
	if profile.Capabilities.Streaming || profile.TransportCapabilities.BidirectionalStream {
		modes = append(modes, aipapi.TransportModeNonStreaming)
	}
	if len(modes) == 0 {
		return nil
	}
	return aipapi.NewBackendTransportCaps(aipapi.OperationTransportSupport{
		Operation: op,
		Modes:     modes,
	})
}

type inventoryProvider struct {
	session ExecuteSession
	timeout time.Duration
	source  string
}

func (p *inventoryProvider) LoadModels(ctx context.Context) (modelinventory.Snapshot, error) {
	if ctx == nil {
		return modelinventory.Snapshot{}, modelinventory.ErrNilContext
	}
	// Inventory's caller owns its fetch budget; metadata timeout is only a
	// fallback for direct callers without a deadline.
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, p.timeout)
		defer cancel()
	}
	resp, err := p.session.ListModels(ctx, 1024)
	if err != nil {
		return modelinventory.Snapshot{}, err
	}
	out := make([]modelinventory.Model, 0, len(resp.Models))
	for _, m := range resp.Models {
		out = append(out, modelinventory.Model{
			CanonicalID: m.CanonicalModelID,
			NativeID:    m.NativeModelID,
			DisplayName: m.CanonicalModelID,
			// The connector owns the default choice: only the connector knows
			// which of its models is the one a request that names no model
			// should use.
			IsDefault: m.IsDefault,
		})
	}
	src := modelinventory.Source(p.source)
	if src == "" {
		src = modelinventory.Source(resp.InventorySource)
	}
	return modelinventory.Snapshot{
		Source:   src,
		LoadedAt: time.UnixMilli(resp.FetchedUnixMS),
		Models:   out,
	}, nil
}

type tokenCounterBridge struct {
	session     OptionalTokenCounter
	instanceID  string
	timeout     time.Duration
	negotiation backendplugin.Negotiation
}

var _ accountingapp.ProviderCounter = (*tokenCounterBridge)(nil)

// SupportsCount reports the narrow capability the backend-plugin count ABI
// actually offers: the input tokens of one invocation. Text and output counting
// have no invocation to send, so they are reported unsupported and the
// accounting service falls back to the local tokenizer instead of accepting
// provider input-token evidence for non-input content.
func (t *tokenCounterBridge) SupportsCount(_ context.Context, in accountingapp.ProviderCountInput) accountingapp.ProviderSupport {
	if in.Kind != accountingapp.CountKindCall {
		return accountingapp.ProviderSupport{
			Status:  accountingapp.SupportStatusUnsupported,
			Message: "executable connector token counting covers canonical calls only",
		}
	}
	return accountingapp.ProviderSupport{Status: accountingapp.SupportStatusSupported}
}

func (t *tokenCounterBridge) CountText(context.Context, accountingapp.CountTextInput) (accountingapp.CountResult, error) {
	return accountingapp.CountResult{}, accountingapp.ErrProviderUnsupported
}

func (t *tokenCounterBridge) CountOutput(context.Context, accountingapp.CountOutputInput) (accountingapp.CountResult, error) {
	return accountingapp.CountResult{}, accountingapp.ErrProviderUnsupported
}

func (t *tokenCounterBridge) CountCall(ctx context.Context, in accountingapp.CountCallInput) (accountingapp.CountResult, error) {
	// A count is issued outside routing, so CountCallInput carries no planned
	// AttemptCandidate. Reuse the Open path's canonical->Invocation mapping with
	// the only candidate content the count itself describes — the backend:model
	// identity it asks about — so the connector counts the caller's own payload
	// instead of an empty invocation.
	inv, err := InvocationFromCall(in.Call, countCandidate(in.Backend, in.Model), t.negotiation)
	if err != nil {
		return accountingapp.CountResult{}, fmt.Errorf("%w: count invocation: %w", accountingapp.ErrProviderUnsupported, err)
	}
	return t.count(ctx, in.Model, inv)
}

func (t *tokenCounterBridge) count(ctx context.Context, model string, inv backendplugin.Invocation) (accountingapp.CountResult, error) {
	cctx, cancel := context.WithTimeout(ctx, t.timeout)
	defer cancel()
	resp, err := t.session.CountTokens(cctx, backendplugin.CountTokensRequest{
		InstanceID: t.instanceID,
		ModelID:    model,
		Invocation: inv,
	})
	if err != nil {
		return accountingapp.CountResult{}, err
	}
	if !resp.Presence.InputTokens || resp.InputTokens == nil {
		return accountingapp.CountResult{}, fmt.Errorf("strict accounting: missing token evidence")
	}
	out := accountingapp.CountResult{InputTokens: int(*resp.InputTokens)}
	return out, nil
}

// countCandidate is the minimal attempt a count request implies: the Open path
// receives a router-planned candidate, while a count only knows which
// backend:model it was asked about. Key mirrors Primary.String() per
// routing.AttemptCandidate.
func countCandidate(backend, model string) routing.AttemptCandidate {
	primary := routing.Primary{Backend: backend, Model: model}
	return routing.AttemptCandidate{Primary: primary, Key: primary.String()}
}

func isZeroNegotiation(n backendplugin.Negotiation) bool {
	return !n.Compatible && n.NegotiatedMinor == 0 && n.PluginMajor == 0 && n.PluginMinor == 0 && len(n.EnabledFeatures) == 0 && n.RejectReason == "" && n.NegotiationToken == ""
}

// mapEffortSupport maps a provider declaration to the core tri-state. It is
// pure: nil/invalid/mismatched declarations yield Unknown, never a refusal.
func mapEffortSupport(decl *backendplugin.ReasoningEffortValuesV1, model, effort string) execbackend.EffortSupportState {
	supported, decided := backendplugin.ClassifyEffortSupport(decl, model, effort)
	if !decided {
		return execbackend.EffortSupportUnknown
	}
	if supported {
		return execbackend.EffortSupportSupported
	}
	return execbackend.EffortSupportUnsupported
}

// resolveEffortSupport queries the backend-facing actual candidate's
// model-bound graded declaration. It is metadata only: no trial inference,
// no background process, no cross-model/cross-credential caching. RPC
// errors, cancellation, timeouts, and malformed peers yield Unknown.
func resolveEffortSupport(ctx context.Context, session ExecuteSession, metadataTimeout time.Duration, cand routing.AttemptCandidate, effort string) execbackend.EffortSupportState {
	wire := routing.BackendFacingCandidate(cand)
	model := wire.Primary.Model
	if model == "" || effort == "" {
		return execbackend.EffortSupportUnknown
	}
	if ctx == nil {
		return execbackend.EffortSupportUnknown
	}
	if metadataTimeout <= 0 {
		metadataTimeout = 5 * time.Second
	}
	// Always bound the metadata RPC: WithTimeout preserves a shorter parent
	// deadline while enforcing the metadata budget when the parent allows
	// longer (for example an HTTP request deadline).
	resolveCtx, cancel := context.WithTimeout(ctx, metadataTimeout)
	defer cancel()
	mid := model
	profile, err := session.Resolve(resolveCtx, &mid)
	if err != nil {
		return execbackend.EffortSupportUnknown
	}
	return mapEffortSupport(profile.ReasoningEffortValues, model, effort)
}
