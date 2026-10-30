package backendplugin

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/promptcache"
	"gopkg.in/yaml.v3"
)

// Mode selects deterministic broken or valid fake plugin behavior.
type Mode string

const (
	ModeValid             Mode = "valid"
	ModeMalformedFrame    Mode = "malformed_frame"
	ModeSlowOutput        Mode = "slow_output"
	ModeBlockedCancel     Mode = "blocked_cancel"
	ModeProcessExit       Mode = "process_exit"
	ModeCommitThenFail    Mode = "commit_then_fail"
	ModeDuplicateTerminal Mode = "duplicate_terminal"
	ModeOversize          Mode = "oversize"
	ModeUnauthorizedPeer  Mode = "unauthorized_peer"
	ModeShutdown          Mode = "shutdown"
	ModeUnknownEventKind  Mode = "unknown_event_kind"
	ModeSecretTerminal    Mode = "secret_terminal"
	ModeExactReasoning    Mode = "exact_reasoning_event"
)

// Stable diagnostic codes for broken modes.
const (
	DiagMalformedFrame    = "fake:malformed_frame"
	DiagSlowOutput        = "fake:slow_output"
	DiagBlockedCancel     = "fake:blocked_cancel"
	DiagProcessExit       = "fake:process_exit"
	DiagCommitThenFail    = "fake:commit_then_fail"
	DiagDuplicateTerminal = "fake:duplicate_terminal"
	DiagOversize          = "fake:oversize"
	DiagUnauthorizedPeer  = "fake:unauthorized_peer"
	DiagShutdown          = "fake:shutdown"
	DiagUnknownEventKind  = "fake:unknown_event_kind"
	DiagSecretTerminal    = "fake:secret_terminal"
	DiagExactReasoning    = "fake:exact_reasoning_event"
)

// FakeService is an in-process deterministic backendplugin.Service for conformance.
type FakeService struct {
	Mode                Mode
	SlowWait            time.Duration
	ExecuteCount        atomic.Int64
	LastStartInvocation *backendplugin.Invocation
	LastStartCall       *aipapi.Call
	ProtocolMinor       uint32
	ExtraFeatures       []backendplugin.Feature
	PromptCache         bool
	// TerminalError, when set, makes a valid execution publish this exact
	// failure terminal after ACCEPTED. Adapter tests use it to prove the
	// host-visible mapping without inventing a provider.
	TerminalError *backendplugin.PluginError
	// CanonicalModelID/NativeModelID override the advertised inventory model
	// identity (the default keeps the legacy "fake-model").
	CanonicalModelID string
	NativeModelID    string
	// PromptCacheActivation makes a successful execute emit one renewable
	// prompt-cache observation (no expiry: the host keep-warm heuristic decides
	// the schedule) and a finished OS-command tool event, so a real host can arm
	// provider maintenance accounting through the ordinary runtime path.
	PromptCacheActivation bool
	// EconomicsV2 enables the explicit host-bound V2 opt-in of this
	// connector. The connector then advertises protocol minor 10 and the
	// host-bound economic evidence feature, and emits typed V2 provider
	// claims that carry identity and quantity facts only: no payer, no
	// amount, no price, no money value anywhere in the fixture.
	//
	// The mode is off by default, so the ordinary V1 behavior is unchanged.
	EconomicsV2 bool
	// promptCacheSeq makes each activation observation handle unique per execute.
	promptCacheSeq atomic.Int64
}

// Describe returns a minimal advertised-capability descriptor.
func (f *FakeService) Describe(ctx context.Context) (backendplugin.PluginDescriptor, error) {
	_ = ctx
	minor := f.ProtocolMinor
	if minor == 0 {
		minor = backendplugin.ProtocolMinorCancellationHandshake
	}
	if f.PromptCache && minor < backendplugin.ProtocolMinorPromptCacheResidency {
		minor = backendplugin.ProtocolMinorPromptCacheResidency
	}
	features := []backendplugin.Feature{
		{Name: backendplugin.FeatureOrderedItems, Required: false},
		{Name: backendplugin.FeatureExactOpenResponsesFields, Required: false},
		{Name: backendplugin.FeatureCancellationHandshake, Required: false},
		{Name: "count_tokens", Required: false},
		{Name: "finalize_billing", Required: false},
	}
	features = append(features, f.ExtraFeatures...)
	if f.PromptCache {
		features = append(features,
			backendplugin.Feature{Name: backendplugin.FeaturePromptCacheResidency},
			backendplugin.Feature{Name: backendplugin.FeatureAccountingEvidence},
		)
	}
	if f.EconomicsV2 {
		// The host-bound contract is additive: the connector advertises the
		// newer minor and the matching feature, both non-required, so a peer
		// that predates the contract keeps working.
		if minor < backendplugin.ProtocolMinorHostBoundEconomicEvidenceV2 {
			minor = backendplugin.ProtocolMinorHostBoundEconomicEvidenceV2
		}
		features = append(features,
			backendplugin.Feature{Name: backendplugin.FeatureAccountingEvidence},
			backendplugin.Feature{Name: backendplugin.FeatureAccountingEvidenceV2},
			backendplugin.Feature{Name: backendplugin.FeatureHostBoundEconomicEvidenceV2},
		)
	}
	return backendplugin.PluginDescriptor{
		ProtocolMajor: 1,
		ProtocolMinor: minor,
		PluginID:      "com.aiproxer.fake",
		Version:       "0.0.1",
		Features:      features,
		Factories: []backendplugin.FactoryDescriptor{{
			Kind:                     "fake",
			CredentialMode:           backendplugin.CredentialModeNone,
			AccessScope:              backendplugin.AccessScopeLocalOnly,
			ProcessSharing:           backendplugin.ProcessSharingPerInstance,
			SupportsCountTokens:      true,
			SupportsFinalizeBilling:  true,
			SupportsDynamicInventory: true,
			StaticCapabilities: backendplugin.CapabilitySummary{
				Streaming: true, Tools: true, Vision: true, Reasoning: true,
				OrderedItems: true, ItemReferences: true, Compaction: true, OpaqueExtensions: true,
			},
			TransportCapabilities: backendplugin.TransportCapabilitySummary{
				Cancellation: true, BidirectionalStream: true,
			},
		}},
	}, nil
}

// fakeInstanceConfig is the test-owned plugin config subtree. It is never
// interpreted by production code; tests use it to steer bounded adversarial
// behaviors of the fake connector (lineage origin and a provider-side renewal
// gate) through the ordinary plugin discovery/Configure path.
type fakeInstanceConfig struct {
	// ObservationLineageMode selects the lineage a prompt-cache observation
	// claims: "" (trusted invocation origin), "foreign_backend", or
	// "foreign_session".
	ObservationLineageMode string `yaml:"observation_lineage_mode"`
	// RenewGatePath, when set, makes every renewal signal its sequence into
	// this path: ungated renewals write "<path>.<seq>.done"; renewals after
	// RenewGateAfter write "<path>.<seq>.started" and then wait (bounded by
	// RenewGateTimeout) for "<path>.<seq>.release", a context cancellation, or
	// the timeout, writing the matching terminal marker first.
	RenewGatePath    string `yaml:"renew_gate_path"`
	RenewGateAfter   int    `yaml:"renew_gate_after"`
	RenewGateTimeout string `yaml:"renew_gate_timeout"`
	// EconomicsV2 is the explicit source of the typed V2 provider claims
	// this instance emits. It is present only when the connector advertises
	// the host-bound V2 capability.
	EconomicsV2 *EconomicsV2Fixture `yaml:"economics_v2"`
}

// Configure creates a fake instance or fails for unauthorized/shutdown modes.
func (f *FakeService) Configure(ctx context.Context, req backendplugin.ConfigureRequest) (backendplugin.ConfiguredInstance, error) {
	_ = ctx
	if f.Mode == ModeUnauthorizedPeer {
		return nil, backendplugin.ModeError{Code: DiagUnauthorizedPeer}
	}
	if f.Mode == ModeShutdown {
		return nil, backendplugin.ModeError{Code: DiagShutdown}
	}
	if err := req.Validate(); err != nil {
		return nil, err
	}
	var instCfg fakeInstanceConfig
	if len(req.ConfigYAML) > 0 {
		if err := yaml.Unmarshal(req.ConfigYAML, &instCfg); err != nil {
			return nil, fmt.Errorf("fake: instance config: %w", err)
		}
	}
	gateTimeout := 30 * time.Second
	if raw := instCfg.RenewGateTimeout; raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil {
			return nil, fmt.Errorf("fake: instance config: renew_gate_timeout: %w", err)
		}
		gateTimeout = parsed
	}
	// The typed V2 mode and its explicit fixture source are declared
	// together: a connector that advertises the capability without a
	// complete source, or an instance that supplies a source the connector
	// does not advertise, refuses the configuration instead of emitting a
	// partial claim.
	if f.EconomicsV2 != (instCfg.EconomicsV2 != nil) {
		return nil, fmt.Errorf("fake: instance config: economics_v2 must be configured exactly when the connector advertises the host-bound V2 capability")
	}
	if instCfg.EconomicsV2 != nil {
		if err := instCfg.EconomicsV2.validate(); err != nil {
			return nil, err
		}
	}
	return &fakeInstance{
		svc:              f,
		mode:             f.Mode,
		slow:             f.SlowWait,
		id:               req.InstanceID,
		neg:              req.Negotiation,
		activation:       f.PromptCacheActivation,
		canonicalID:      f.CanonicalModelID,
		nativeID:         f.NativeModelID,
		lineageMode:      instCfg.ObservationLineageMode,
		renewGatePath:    instCfg.RenewGatePath,
		renewGateAfter:   instCfg.RenewGateAfter,
		renewGateTimeout: gateTimeout,
		economicsV2:      instCfg.EconomicsV2,
	}, nil
}

type fakeInstance struct {
	svc              *FakeService
	mode             Mode
	slow             time.Duration
	id               string
	neg              backendplugin.Negotiation
	activation       bool
	canonicalID      string
	nativeID         string
	renewMu          sync.Mutex
	renewed          map[string]int
	lineageMode      string
	renewGatePath    string
	renewGateAfter   int
	renewGateTimeout time.Duration
	renewSeq         atomic.Int64
	// economicsV2 is the explicit source of the typed V2 provider claims
	// this instance emits. Nil keeps the ordinary V1 behavior.
	economicsV2 *EconomicsV2Fixture
}

// economicsV2EmitsClaims reports whether this instance emits its typed V2
// provider claims. The suppression switch lets a proof observe the same
// connector with the accounting evidence off.
func (f *fakeInstance) economicsV2EmitsClaims() bool {
	return f.economicsV2 != nil && !f.economicsV2.SuppressClaim
}

func (f *fakeInstance) Negotiation() backendplugin.Negotiation {
	if f.neg.Compatible && f.neg.NegotiatedMinor > 0 {
		return f.neg
	}
	minor := backendplugin.ProtocolMinorCancellationHandshake
	features := []string{
		backendplugin.FeatureOrderedItems,
		backendplugin.FeatureExactOpenResponsesFields,
		backendplugin.FeatureCancellationHandshake,
	}
	if f.economicsV2 != nil {
		// The V2 mode never claims a negotiated capability it does not
		// implement: the minor and the features follow the advertised offer.
		minor = backendplugin.ProtocolMinorHostBoundEconomicEvidenceV2
		features = append(features,
			backendplugin.FeatureAccountingEvidence,
			backendplugin.FeatureAccountingEvidenceV2,
			backendplugin.FeatureHostBoundEconomicEvidenceV2,
		)
	}
	return backendplugin.Negotiation{
		Compatible:      true,
		NegotiatedMinor: minor,
		EnabledFeatures: features,
	}
}

func (f *fakeInstance) Resolve(ctx context.Context, modelID *string) (backendplugin.ResolvedProfile, error) {
	_ = ctx
	_ = modelID
	return backendplugin.ResolvedProfile{
		Capabilities: backendplugin.CapabilitySummary{
			Streaming: true, Tools: true, Vision: true, Reasoning: true,
			OrderedItems: true, ItemReferences: true, Compaction: true, OpaqueExtensions: true,
		},
		DialectSupport: backendplugin.DialectSupport{
			ItemDialects: []backendplugin.DialectRequirementDTO{
				{Kind: "item", Dialect: "item_reference"},
			},
			CompactionDialects: []backendplugin.DialectRequirementDTO{
				{Kind: "compaction", Dialect: "compact.v1"},
			},
			ReasoningDialects: []backendplugin.DialectRequirementDTO{
				{Kind: "reasoning", Dialect: string(aipapi.ReasoningDialectOpenAIChatTextV1)},
			},
		},
		TransportCapabilities:    backendplugin.TransportCapabilitySummary{Cancellation: true, BidirectionalStream: true},
		SupportsCountTokens:      true,
		SupportsFinalizeBilling:  true,
		SupportsDynamicInventory: true,
		EvidenceSource:           "fake",
		PromptCacheProfile:       promptcache.Profile{ObservationSupported: f.svc != nil && f.svc.PromptCache, RenewalSupported: f.svc != nil && f.svc.PromptCache, LifecycleKinds: []promptcache.LifecycleKind{promptcache.LifecycleSlidingExpiry}},
		ProfileVersion:           "1",
	}, nil
}

func (f *fakeInstance) ListModels(ctx context.Context, maxModels uint32) (backendplugin.ListModelsResponse, error) {
	_ = ctx
	canonical, native := f.canonicalID, f.nativeID
	if canonical == "" {
		canonical = "fake-model"
	}
	if native == "" {
		native = canonical
	}
	out := backendplugin.ListModelsResponse{
		Models: []backendplugin.ModelDescriptor{{
			CanonicalModelID: canonical,
			NativeModelID:    native,
			FactoryKind:      "fake",
			Capabilities:     backendplugin.CapabilitySummary{Streaming: true},
		}},
		InventorySource: "fake",
		FetchedUnixMS:   1,
	}
	return out, out.Validate(maxModels)
}

func (f *fakeInstance) Execute(stream backendplugin.ExecuteStream) error {
	if f.mode == ModeProcessExit {
		return backendplugin.ModeError{Code: DiagProcessExit}
	}
	start, err := stream.Recv()
	if err != nil {
		return err
	}
	if start.Kind != backendplugin.ClientFrameStart || start.Invocation == nil {
		return fmt.Errorf("%w: expected start", backendplugin.ErrInvalidFrame)
	}
	if err := backendplugin.ValidateClientFrameBounds(start); err != nil {
		return err
	}
	if f.svc != nil {
		f.svc.ExecuteCount.Add(1)
		invCopy := *start.Invocation
		f.svc.LastStartInvocation = &invCopy
		if call, err := backendplugin.CallFromInvocation(*start.Invocation); err == nil {
			f.svc.LastStartCall = &call
		}
	}
	if err := stream.Send(backendplugin.ServerFrame{Kind: backendplugin.ServerFrameAccepted}); err != nil {
		return err
	}
	seq := uint64(1)
	if f.svc != nil && f.svc.TerminalError != nil {
		return stream.Send(backendplugin.ServerFrame{
			Kind: backendplugin.ServerFrameTerminal, Sequence: seq,
			Terminal: &backendplugin.Terminal{Status: backendplugin.TerminalFailure, Error: f.svc.TerminalError},
		})
	}
	if f.economicsV2 != nil {
		// The connector records one line per execute, so a proof can observe
		// how many attempts reached the connector across the process
		// boundary.
		if err := appendInvocationCount(f.economicsV2.InvocationCountPath, start.Invocation); err != nil {
			return err
		}
	}
	if f.economicsV2 != nil && f.economicsV2.ClaimBeforeContent && f.economicsV2EmitsClaims() {
		// The claim arrives before the first content frame, so the host sees
		// the rejection before the output is committed.
		claims, err := f.economicsV2Claims(start.Invocation)
		if err != nil {
			return err
		}
		for _, claim := range claims {
			if err := stream.Send(backendplugin.ServerFrame{
				Kind: backendplugin.ServerFrameAccountingEvidence, Sequence: seq,
				AccountingV2: &claim,
			}); err != nil {
				return err
			}
			seq++
		}
	}
	if f.economicsV2 != nil {
		// The V2 mode owns the canonical event script of this connector.
		//
		// The sequence is valid and correctly ordered: the response opens, the
		// content commits the output, the claim may follow the content, and
		// the response closes after both. Two ordering properties matter. A
		// content event before the opening events is rejected as an
		// out-of-order event, and the canonical collector returns at the
		// closing event, so a closing event placed before the content would
		// leave the client with an empty output. The ordinary V1 script below
		// is unchanged.
		delta := "hello"
		finishReason, responseStatus := "stop", "completed"
		for _, event := range []*backendplugin.CanonicalEvent{
			{Kind: backendplugin.EventResponseStarted},
			{Kind: backendplugin.EventMessageStarted},
			{Kind: backendplugin.EventTextDelta, Delta: &delta},
		} {
			if err := stream.Send(backendplugin.ServerFrame{
				Kind: backendplugin.ServerFrameEvent, Sequence: seq, Event: event,
			}); err != nil {
				return err
			}
			seq++
		}
		if !f.economicsV2.ClaimBeforeContent && f.economicsV2EmitsClaims() {
			// The claim arrives after the content frames: the output is
			// already committed, so the host records the capture loss and
			// keeps the committed stream.
			claims, err := f.economicsV2Claims(start.Invocation)
			if err != nil {
				return err
			}
			for _, claim := range claims {
				if err := stream.Send(backendplugin.ServerFrame{
					Kind: backendplugin.ServerFrameAccountingEvidence, Sequence: seq,
					AccountingV2: &claim,
				}); err != nil {
					return err
				}
				seq++
			}
		}
		// The response closes after the content and after the claim, so the
		// collector reads the whole open response.
		if err := stream.Send(backendplugin.ServerFrame{
			Kind: backendplugin.ServerFrameEvent, Sequence: seq,
			Event: &backendplugin.CanonicalEvent{
				Kind:           backendplugin.EventResponseFinished,
				FinishReason:   &finishReason,
				ResponseStatus: &responseStatus,
			},
		}); err != nil {
			return err
		}
		seq++
		return stream.Send(backendplugin.ServerFrame{
			Kind: backendplugin.ServerFrameTerminal, Sequence: seq,
			Terminal: &backendplugin.Terminal{Status: backendplugin.TerminalSuccess},
		})
	}
	if f.activation {
		now := time.Now().UTC()
		one := int64(1)
		observation := promptcache.Observation{
			ALegID: start.Invocation.ALegID, BLegID: start.Invocation.BLegID,
			BackendInstanceID: f.id,
			TargetID:          promptcache.TargetID("fake-maintenance-target"),
			GenerationID:      promptcache.GenerationID("fake-maintenance-generation"),
			Lifecycle:         promptcache.LifecycleSlidingExpiry,
			Timing:            promptcache.Timing{ObservedAt: now},
			Renewable:         true,
			Handle:            promptcache.Handle(fmt.Sprintf("fake-maintenance-%d", f.svc.promptCacheSeq.Add(1))),
			Evidence:          promptcache.CacheEvidence{TotalTokens: &one},
		}
		switch f.lineageMode {
		case "foreign_backend":
			observation.BackendInstanceID = "foreign-backend"
		case "foreign_session":
			observation.ALegID = "foreign-a-leg"
			observation.BLegID = "foreign-b-leg"
		}
		if err := stream.Send(backendplugin.ServerFrame{
			Kind: backendplugin.ServerFramePromptCacheObservation, Sequence: seq,
			PromptCacheObservation: &observation,
		}); err != nil {
			return err
		}
		seq++
		delta, toolArgs := "ok", `{"command":"true"}`
		toolCallID, toolName := "os-1", "bash"
		finishReason, responseStatus := "stop", "completed"
		for _, event := range []*backendplugin.CanonicalEvent{
			{Kind: backendplugin.EventResponseStarted},
			{Kind: backendplugin.EventMessageStarted},
			{Kind: backendplugin.EventTextDelta, Delta: &delta},
			{Kind: backendplugin.EventToolCallStarted, ToolCallID: &toolCallID, ToolName: &toolName},
			{Kind: backendplugin.EventToolCallArgsDelta, ToolCallID: &toolCallID, ToolName: &toolName, Delta: &toolArgs},
			{Kind: backendplugin.EventToolCallFinished, ToolCallID: &toolCallID, ToolName: &toolName},
			{Kind: backendplugin.EventResponseFinished, FinishReason: &finishReason, ResponseStatus: &responseStatus},
		} {
			if err := stream.Send(backendplugin.ServerFrame{
				Kind: backendplugin.ServerFrameEvent, Sequence: seq, Event: event,
			}); err != nil {
				return err
			}
			seq++
		}
		return stream.Send(backendplugin.ServerFrame{
			Kind: backendplugin.ServerFrameTerminal, Sequence: seq,
			Terminal: &backendplugin.Terminal{Status: backendplugin.TerminalSuccess},
		})
	}
	// Terminal-semantics promotion: the terminal-upstream-error corpus
	// scenario executes against this reference connector with a terminal
	// error shape (error event before the terminal frame) so the connector
	// TCK observes error evidence. Keyed strictly on the scenario
	// RequestID; every other invocation keeps the default clean script.
	if start.Invocation != nil && start.Invocation.RequestID == "terminal-upstream-error" {
		d := "ok"
		if err := stream.Send(backendplugin.ServerFrame{
			Kind: backendplugin.ServerFrameEvent, Sequence: seq,
			Event: &backendplugin.CanonicalEvent{Kind: backendplugin.EventTextDelta, Delta: &d},
		}); err != nil {
			return err
		}
		seq++
		if err := stream.Send(backendplugin.ServerFrame{
			Kind: backendplugin.ServerFrameEvent, Sequence: seq,
			Event: &backendplugin.CanonicalEvent{Kind: backendplugin.EventError, Error: &backendplugin.PluginError{Code: backendplugin.ErrorCodeProviderTerminal, Message: "terminal upstream error"}},
		}); err != nil {
			return err
		}
		seq++
		return stream.Send(backendplugin.ServerFrame{
			Kind: backendplugin.ServerFrameTerminal, Sequence: seq,
			Terminal: &backendplugin.Terminal{Status: backendplugin.TerminalFailure},
		})
	}
	switch f.mode {
	case ModeMalformedFrame:
		return backendplugin.ModeError{Code: DiagMalformedFrame}
	case ModeSlowOutput:
		wait := f.slow
		if wait == 0 {
			wait = 20 * time.Millisecond
		}
		select {
		case <-stream.Context().Done():
			return backendplugin.ModeError{Code: DiagSlowOutput}
		case <-time.After(wait):
		}
		return backendplugin.ModeError{Code: DiagSlowOutput}
	case ModeOversize:
		big := make([]byte, 256)
		for i := range big {
			big[i] = 'x'
		}
		ds := string(big)
		if err := stream.Send(backendplugin.ServerFrame{
			Kind: backendplugin.ServerFrameEvent, Sequence: seq,
			Event: &backendplugin.CanonicalEvent{Kind: backendplugin.EventTextDelta, Delta: &ds},
		}); err != nil {
			return err
		}
		return backendplugin.ModeError{Code: DiagOversize}
	case ModeBlockedCancel:
		for {
			f, err := stream.Recv()
			if err != nil {
				// Host/context cancellation must surface as context error (no generation
				// invalidation). ModeError is reserved for blocked-cancel protocol faults.
				if cerr := stream.Context().Err(); cerr != nil {
					return cerr
				}
				if errors.Is(err, io.EOF) {
					return backendplugin.ModeError{Code: DiagBlockedCancel}
				}
				return err
			}
			if f.Kind == backendplugin.ClientFrameCancel {
				return backendplugin.ModeError{Code: DiagBlockedCancel}
			}
		}
	case ModeDuplicateTerminal:
		if err := stream.Send(backendplugin.ServerFrame{
			Kind: backendplugin.ServerFrameTerminal, Sequence: seq,
			Terminal: &backendplugin.Terminal{Status: backendplugin.TerminalSuccess},
		}); err != nil {
			return err
		}
		if err := stream.Send(backendplugin.ServerFrame{
			Kind: backendplugin.ServerFrameTerminal, Sequence: seq + 1,
			Terminal: &backendplugin.Terminal{Status: backendplugin.TerminalSuccess},
		}); err != nil {
			return err
		}
		return backendplugin.ModeError{Code: DiagDuplicateTerminal}
	case ModeCommitThenFail:
		d := "committed"
		if err := stream.Send(backendplugin.ServerFrame{
			Kind: backendplugin.ServerFrameEvent, Sequence: seq,
			Event: &backendplugin.CanonicalEvent{Kind: backendplugin.EventTextDelta, Delta: &d},
		}); err != nil {
			return err
		}
		return backendplugin.ModeError{Code: DiagCommitThenFail}
	case ModeUnknownEventKind:
		if err := stream.Send(backendplugin.ServerFrame{
			Kind: backendplugin.ServerFrameEvent, Sequence: seq,
			Event: &backendplugin.CanonicalEvent{Kind: backendplugin.EventKind("evil_kind")},
		}); err != nil {
			return err
		}
		return backendplugin.ModeError{Code: DiagUnknownEventKind}
	case ModeSecretTerminal:
		return stream.Send(backendplugin.ServerFrame{
			Kind: backendplugin.ServerFrameTerminal, Sequence: seq,
			Terminal: &backendplugin.Terminal{
				Status: backendplugin.TerminalFailure,
				Error: &backendplugin.PluginError{
					Code:    backendplugin.ErrorCodeUnavailable,
					Message: `provider rejected api_key=sk-or-v1-openrouter-leak Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.sig sk-ant-api03-ABCDEFGHIJKLMNOP`,
				},
			},
		})
	case ModeExactReasoning:
		dialect := string(aipapi.ReasoningDialectOpenAIResponsesItemV1)
		ev := &backendplugin.CanonicalEvent{
			Kind:                      backendplugin.EventReasoningPart,
			ReasoningDialect:          &dialect,
			ReasoningSummary:          backendplugin.RawJSONFromBytes([]byte(`[{"type":"summary_text","text":"s"}]`)),
			ReasoningContent:          backendplugin.RawJSONFromBytes([]byte(`[{"type":"output_text","text":"t"}]`)),
			ReasoningEncryptedContent: backendplugin.RawJSONNullValue(),
		}
		if err := stream.Send(backendplugin.ServerFrame{Kind: backendplugin.ServerFrameEvent, Sequence: seq, Event: ev}); err != nil {
			return err
		}
		return stream.Send(backendplugin.ServerFrame{
			Kind: backendplugin.ServerFrameTerminal, Sequence: seq + 1,
			Terminal: &backendplugin.Terminal{Status: backendplugin.TerminalSuccess},
		})
	}

	kinds := []backendplugin.EventKind{
		backendplugin.EventTextDelta,
		backendplugin.EventReasoningDelta,
		backendplugin.EventToolCallStarted,
		backendplugin.EventAssistantImageRef,
		backendplugin.EventUsageDelta,
	}
	for _, kind := range kinds {
		ev := &backendplugin.CanonicalEvent{Kind: kind}
		switch kind {
		case backendplugin.EventTextDelta:
			d := "ok"
			ev.Delta = &d
		case backendplugin.EventReasoningDelta:
			d := "think"
			ev.Delta = &d
		case backendplugin.EventToolCallStarted:
			id := "t1"
			name := "tool"
			ev.ToolCallID = &id
			ev.ToolName = &name
		case backendplugin.EventAssistantImageRef:
			ref := "img://1"
			ev.ImageRef = &ref
		case backendplugin.EventUsageDelta:
			zero := int64(1)
			ev.Usage = &backendplugin.UsageEvidence{
				TotalTokens:  &zero,
				Presence:     backendplugin.UsagePresence{TotalTokens: true},
				RawUsageJSON: backendplugin.RawJSONAbsentValue(),
			}
		}
		if err := stream.Send(backendplugin.ServerFrame{
			Kind: backendplugin.ServerFrameEvent, Sequence: seq, Event: ev,
		}); err != nil {
			return err
		}
		seq++
	}
	// The V2 mode returns from its own script above, so the ordinary script
	// that reaches this point is the unchanged V1 behavior.
	return stream.Send(backendplugin.ServerFrame{
		Kind: backendplugin.ServerFrameTerminal, Sequence: seq,
		Terminal: &backendplugin.Terminal{Status: backendplugin.TerminalSuccess},
	})
}

func (f *fakeInstance) RenewPromptCache(ctx context.Context, req promptcache.RenewRequest) (promptcache.RenewResponse, error) {
	if err := ctx.Err(); err != nil {
		return promptcache.RenewResponse{}, err
	}
	if err := req.Validate(); err != nil {
		return promptcache.RenewResponse{}, err
	}
	if err := f.gateRenewal(ctx); err != nil {
		return promptcache.RenewResponse{}, err
	}
	now := time.Now().UTC()
	observation := promptcache.Observation{ALegID: "a", BLegID: "b", BackendInstanceID: f.id, TargetID: promptcache.TargetID(string(req.Handle)), GenerationID: "generation", Lifecycle: promptcache.LifecycleSlidingExpiry, Timing: promptcache.Timing{ObservedAt: now}, Renewable: true, Handle: append(promptcache.Handle(nil), req.Handle...)}
	// Provider-billable accounting is reported once per resident target; a
	// repeat refresh keeps residency without inventing a second provider charge.
	f.renewMu.Lock()
	if f.renewed == nil {
		f.renewed = make(map[string]int)
	}
	f.renewed[string(req.Handle)]++
	count := f.renewed[string(req.Handle)]
	f.renewMu.Unlock()
	if count > 1 {
		return promptcache.RenewResponse{Result: promptcache.RenewResult{Status: promptcache.StillResident, Observation: &observation}}, nil
	}
	input := int64(1)
	accounting := &promptcache.AccountingEvidence{InputTokens: &input, Presence: aipapi.UsagePresence{InputTokens: true}, Source: promptcache.AccountingSourceProviderReported, Authority: promptcache.AccountingAuthorityAuthoritative, Plane: promptcache.AccountingPlaneProviderBillable, DedupeKey: "prompt-cache:" + req.OperationID}
	return promptcache.RenewResponse{Result: promptcache.RenewResult{Status: promptcache.Renewed, Observation: &observation}, Accounting: accounting}, nil
}

// gateRenewal is the bounded, test-owned provider-side renewal barrier. It
// writes a monotonic sequence marker and, for renewals after the configured
// threshold, waits for an operator file, a context cancellation, or a hard
// timeout before continuing. The production host never configures it.
func (f *fakeInstance) gateRenewal(ctx context.Context) error {
	gate := strings.TrimSpace(f.renewGatePath)
	if gate == "" {
		return nil
	}
	seq := f.renewSeq.Add(1)
	if int(seq) <= f.renewGateAfter {
		return writeGateMarker(gate, seq, "done")
	}
	if err := writeGateMarker(gate, seq, "started"); err != nil {
		return nil
	}
	deadline := time.Now().Add(f.renewGateTimeout)
	for {
		if _, err := os.Stat(gatePath(gate, seq, "release")); err == nil {
			return writeGateMarker(gate, seq, "done")
		}
		if err := ctx.Err(); err != nil {
			_ = writeGateMarker(gate, seq, "canceled")
			return err
		}
		if time.Now().After(deadline) {
			return writeGateMarker(gate, seq, "timeout")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func gatePath(base string, seq int64, suffix string) string {
	return fmt.Sprintf("%s.%d.%s", strings.TrimRight(base, "/"), seq, suffix)
}

func writeGateMarker(base string, seq int64, suffix string) error {
	return os.WriteFile(gatePath(base, seq, suffix), []byte(suffix), 0o600)
}

func (f *fakeInstance) ReleasePromptCache(ctx context.Context, req promptcache.ReleaseRequest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return req.Validate()
}

func (f *fakeInstance) Close(ctx context.Context) error {
	_ = ctx
	return nil
}

func (f *fakeInstance) CountTokens(ctx context.Context, req backendplugin.CountTokensRequest) (backendplugin.CountTokensResponse, error) {
	_ = ctx
	n := int64(len(req.Invocation.Messages))
	return backendplugin.CountTokensResponse{
		InputTokens:     &n,
		Presence:        backendplugin.UsagePresence{InputTokens: true},
		EvidenceQuality: "fake",
	}, nil
}

func (f *fakeInstance) FinalizeBilling(ctx context.Context, req backendplugin.FinalizeBillingRequest) (backendplugin.FinalizeBillingResponse, error) {
	_ = ctx
	if err := req.Validate(); err != nil {
		return backendplugin.FinalizeBillingResponse{}, err
	}
	finalizeQuality := "fake"
	if f.activation {
		finalizeQuality = "provider_reported"
	}
	n := int64(1)
	return backendplugin.FinalizeBillingResponse{
		Usage: backendplugin.UsageEvidence{
			TotalTokens:  &n,
			Presence:     backendplugin.UsagePresence{TotalTokens: true},
			RawUsageJSON: backendplugin.RawJSONAbsentValue(),
		},
		EvidenceQuality: finalizeQuality,
	}, nil
}

// CommandName is the future executable skeleton name for process-host launch.
const CommandName = "aip-backendplugin-fake"

// EconomicsV2Fixture is the explicit, test-owned source of one connector's
// typed V2 provider claim.
//
// The fixture carries identity and quantity facts only. It never prices,
// rates, selects a payer, applies a policy, or infers an account: every value
// is supplied explicitly by the test that owns the configuration, so no money
// value exists anywhere in the fixture.
type EconomicsV2Fixture struct {
	// StoreID is the connector's own store placeholder. The host replaces it
	// with the trusted store.
	StoreID string `yaml:"store_id"`
	// TenantID is the connector's own tenant placeholder. The host replaces
	// it with the trusted tenant.
	TenantID string `yaml:"tenant_id"`
	// AccountID is the connector's own account placeholder. The host replaces
	// it with the frozen credited account.
	AccountID string `yaml:"account_id"`
	// ProviderAccountKey is the provider's own account identity. The host
	// preserves it byte-for-byte and never treats it as the billing account.
	ProviderAccountKey string `yaml:"provider_account_key"`
	// ProviderRequestID is the provider's own request identity.
	ProviderRequestID string `yaml:"provider_request_id"`
	// InputTokens and OutputTokens are the provider's reported quantities.
	// They are quantities, never money.
	InputTokens  int64 `yaml:"input_tokens"`
	OutputTokens int64 `yaml:"output_tokens"`
	// ClaimBeforeContent emits the claim before the first content frame, so
	// the host sees the rejection before the output is committed.
	ClaimBeforeContent bool `yaml:"claim_before_content"`
	// EmitReference emits a second claim that supersedes the first one, so a
	// proof can exercise the host's reference contract.
	EmitReference bool `yaml:"emit_reference"`
	// BadReferenceHash makes the superseding claim carry a payload hash that
	// does not match its predecessor.
	BadReferenceHash bool `yaml:"bad_reference_hash"`
	// BadProviderKey makes the superseding claim name a different provider
	// account than its predecessor, so the reference crosses the provider
	// scope.
	BadProviderKey bool `yaml:"bad_provider_key"`
	// AttemptIdentityConflict claims a B-leg that is neither the trusted
	// value nor the transport pseudonym the host supplied.
	AttemptIdentityConflict bool `yaml:"attempt_identity_conflict"`
	// SuppressClaim makes the instance emit its canonical content without any
	// provider claim, so a proof can observe the client-visible result of the
	// same connector with the accounting evidence off.
	SuppressClaim bool `yaml:"suppress_claim"`
	// ClaimCount makes the connector emit that many genuinely new provider
	// claims, each with its own source event key and its own local identity,
	// so a proof can reach the host's attempt-lifetime logical-item cap with
	// real claims. One keeps the single-claim script.
	ClaimCount int `yaml:"claim_count"`
	// InvocationCountPath is a file the connector appends one line to per
	// execute. It makes "exactly one attempt reached the connector"
	// observable across the process boundary.
	InvocationCountPath string `yaml:"invocation_count_path"`
}

// maxEconomicsV2ClaimCount bounds the number of provider claims one V2
// instance emits. The bound keeps the fixture a bounded test capability.
const maxEconomicsV2ClaimCount = 4096

// validate rejects an incomplete explicit fixture source. The connector fails
// closed instead of emitting a partial claim.
func (f EconomicsV2Fixture) validate() error {
	for name, value := range map[string]string{
		"store_id": f.StoreID, "provider_account_key": f.ProviderAccountKey,
		"provider_request_id": f.ProviderRequestID,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("fake: economics_v2: %s is required", name)
		}
	}
	if f.InputTokens < 0 || f.OutputTokens < 0 {
		return fmt.Errorf("fake: economics_v2: reported token counts must not be negative")
	}
	if f.ClaimCount < 0 || f.ClaimCount > maxEconomicsV2ClaimCount {
		return fmt.Errorf("fake: economics_v2: claim_count must be within [0, %d]", maxEconomicsV2ClaimCount)
	}
	if f.ClaimCount > 1 && f.EmitReference {
		return fmt.Errorf("fake: economics_v2: claim_count above one and emit_reference are mutually exclusive")
	}
	return nil
}

// appendInvocationCount records one line per execute, so a proof can observe
// how many attempts reached the connector across the process boundary.
func appendInvocationCount(path string, inv *backendplugin.Invocation) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("fake: economics_v2: invocation count: %w", err)
	}
	defer func() { _ = file.Close() }()
	requestID := ""
	if inv != nil {
		requestID = strings.TrimSpace(inv.RequestID)
	}
	if _, err := file.WriteString(requestID + "\n"); err != nil {
		return fmt.Errorf("fake: economics_v2: invocation count: %w", err)
	}
	return nil
}

// economicsV2Claims builds the typed V2 provider claims of one attempt.
//
// The claims carry identity and quantity facts only: the provider's own
// account and request identities, the transport B-leg pseudonym the host
// supplied, and the provider's reported token quantities. There is no payer,
// no amount, no price and no reference to a money value.
//
// The claim echoes the transport B-leg pseudonym the host supplied in the
// invocation. The host replaces that pseudonym with the sealed B-leg; any
// other leg value is a conflicting claim.
func (f *fakeInstance) economicsV2Claims(inv *backendplugin.Invocation) ([]backendplugin.AccountingEvidenceV2, error) {
	fixture := f.economicsV2
	if fixture == nil || inv == nil {
		return nil, nil
	}
	if err := fixture.validate(); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	bLeg := strings.TrimSpace(inv.BLegID)
	if fixture.AttemptIdentityConflict {
		bLeg = "foreign-b-leg"
	}
	requestID := strings.TrimSpace(inv.RequestID)
	input := metering.Decimal{Coefficient: strconv.FormatInt(fixture.InputTokens, 10), Scale: 0}
	output := metering.Decimal{Coefficient: strconv.FormatInt(fixture.OutputTokens, 10), Scale: 0}
	measures := []metering.Measure{
		{
			Key: metering.ComponentKey{
				Direction: metering.DirectionInput, Component: metering.ComponentInputTokenTotal, Unit: metering.UnitToken,
			},
			Value:   &input,
			Quality: metering.QualityObserved,
		},
		{
			Key: metering.ComponentKey{
				Direction: metering.DirectionOutput, Component: metering.ComponentOutputToken, Unit: metering.UnitToken,
			},
			Value:   &output,
			Quality: metering.QualityObserved,
		},
	}
	build := func(
		localID, sourceEventKey string,
		revision uint64,
		providerKey, semantics string,
		supersedes []metering.ObservationRef,
	) (backendplugin.AccountingEvidenceV2, error) {
		observation := metering.Observation{
			Version:        metering.ObservationVersionV2,
			ID:             localID,
			SourceEventKey: sourceEventKey,
			Revision:       revision,
			StreamID:       "fake.hostbound.v2",
			Sequence:       revision,
			Origin:         metering.OriginProvider,
			Acquisition:    metering.AcquisitionProviderResponse,
			Authority:      metering.AuthorityObservedClaim,
			Perspective:    metering.PerspectiveOperator,
			Boundary:       metering.BoundaryBackendIngress,
			Lifecycle:      metering.LifecycleBackendAttempt,
			Subject: metering.SubjectRef{
				Kind:               metering.SubjectBLeg,
				StoreID:            fixture.StoreID,
				TenantID:           fixture.TenantID,
				AccountID:          fixture.AccountID,
				BLegID:             bLeg,
				ProviderAccountKey: providerKey,
				ProviderRequestID:  fixture.ProviderRequestID,
			},
			Correlation: metering.CorrelationV2{
				StoreID:            fixture.StoreID,
				TenantID:           fixture.TenantID,
				BLegID:             bLeg,
				ProviderAccountKey: providerKey,
				ProviderRequestID:  fixture.ProviderRequestID,
			},
			Semantics:  semantics,
			ObservedAt: now,
			ReceivedAt: now,
			MappingRef: "fake.hostbound.v1",
			Measures:   measures,
			Supersedes: supersedes,
		}
		if err := observation.Validate(); err != nil {
			return backendplugin.AccountingEvidenceV2{}, fmt.Errorf("fake: economics_v2: %w", err)
		}
		return backendplugin.AccountingEvidenceV2{
			Observation: observation,
			Coverage:    backendplugin.EvidenceCoverageComplete,
		}, nil
	}
	predecessorKey := "fake.hostbound.v2.source:" + requestID + ":" + bLeg
	if fixture.ClaimCount > 1 {
		// The capacity proof needs genuinely new source identities: every
		// claim carries its own source event key, its own local identity and
		// its own revision, so the host's attempt-lifetime map reaches its
		// real logical-item cap with real claims.
		claims := make([]backendplugin.AccountingEvidenceV2, 0, fixture.ClaimCount)
		for i := 0; i < fixture.ClaimCount; i++ {
			key := predecessorKey + ":claim-" + strconv.Itoa(i)
			claim, err := build(
				"fake-hostbound-v2-"+key, key, uint64(i+1),
				fixture.ProviderAccountKey, metering.SemanticsDelta, nil,
			)
			if err != nil {
				return nil, err
			}
			claims = append(claims, claim)
		}
		return claims, nil
	}
	predecessor, err := build(
		"fake-hostbound-v2-"+predecessorKey, predecessorKey, 1,
		fixture.ProviderAccountKey, metering.SemanticsDelta, nil,
	)
	if err != nil {
		return nil, err
	}
	claims := []backendplugin.AccountingEvidenceV2{predecessor}
	if !fixture.EmitReference {
		return claims, nil
	}
	payloadHash, err := predecessor.Observation.ReplayFingerprint()
	if err != nil {
		return nil, fmt.Errorf("fake: economics_v2: predecessor fingerprint: %w", err)
	}
	if fixture.BadReferenceHash {
		payloadHash = "fake-hostbound-v2-wrong-reference-hash"
	}
	providerKey := fixture.ProviderAccountKey
	if fixture.BadProviderKey {
		providerKey = fixture.ProviderAccountKey + "-other"
	}
	successorKey := predecessorKey + ":successor"
	successor, err := build(
		"fake-hostbound-v2-"+successorKey, successorKey, 2,
		providerKey, metering.SemanticsCorrection,
		[]metering.ObservationRef{{
			StoreID:       fixture.StoreID,
			ObservationID: predecessor.Observation.ID,
			Revision:      predecessor.Observation.Revision,
			PayloadHash:   payloadHash,
		}},
	)
	if err != nil {
		return nil, err
	}
	return append(claims, successor), nil
}
