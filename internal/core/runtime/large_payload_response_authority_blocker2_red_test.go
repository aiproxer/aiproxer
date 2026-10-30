package runtime

import (
	"context"
	"crypto/sha256"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/securesession/app"
	"github.com/aiproxer/aiproxer/internal/core/securesession/domain"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/completion"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	sdkresponse "github.com/aiproxer/aiproxer/pkg/aipsdk/response"
	sdkusage "github.com/aiproxer/aiproxer/pkg/aipsdk/usage"
)

// blocker2PrefixResponseHook transforms any TextDelta event by prepending "PREFIX:".
type blocker2PrefixResponseHook struct{}

func (blocker2PrefixResponseHook) ID() string                        { return "blocker2-prefix-hook" }
func (blocker2PrefixResponseHook) Order() int                        { return 1 }
func (blocker2PrefixResponseHook) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }
func (blocker2PrefixResponseHook) HandleEvent(ctx context.Context, ev *aipapi.Event, meta sdkhooks.PartMeta) error {
	if ev != nil && ev.Kind == aipapi.EventTextDelta {
		ev.Delta = "PREFIX:" + ev.Delta
	}
	return nil
}

// blocker2TrackingCompletionGate records when Handle is invoked.
type blocker2TrackingCompletionGate struct {
	invoked atomic.Bool
}

func (g *blocker2TrackingCompletionGate) ID() string { return "blocker2-tracking-gate" }
func (g *blocker2TrackingCompletionGate) Order() int { return 1 }
func (g *blocker2TrackingCompletionGate) FailureMode() sdkhooks.FailureMode {
	return sdkhooks.FailClosed
}

func (g *blocker2TrackingCompletionGate) Handle(ctx context.Context, meta completion.Meta, buf completion.Buffered, svc completion.Services) (completion.Outcome, error) {
	g.invoked.Store(true)
	return completion.PassOriginalOutcome(), nil
}

// blocker2TrackingSecureRecorder records when RecordPostHookStreamEvent is invoked.
type blocker2TrackingSecureRecorder struct {
	postHookStreamEventCalls atomic.Int64
}

func (r *blocker2TrackingSecureRecorder) RecordClientTurnAfterGate(context.Context, app.ClientTurnRecordInput) error {
	return nil
}

func (r *blocker2TrackingSecureRecorder) RecordPostHookStreamEvent(context.Context, app.StreamEventRecordInput) error {
	r.postHookStreamEventCalls.Add(1)
	return nil
}

func (r *blocker2TrackingSecureRecorder) AppendTranscript(context.Context, domain.TranscriptItem) error {
	return nil
}

func (r *blocker2TrackingSecureRecorder) AppendAudit(context.Context, domain.AuditItem) error {
	return nil
}

func (r *blocker2TrackingSecureRecorder) AddUsage(context.Context, domain.UsageDelta) error {
	return nil
}

func (r *blocker2TrackingSecureRecorder) TouchActivity(context.Context, domain.SessionID, time.Time, domain.ActivitySource) error {
	return nil
}

type blocker2WireBackendStub struct {
	reqSupport    largebody.WireRequestSupport
	domainSupport largebody.WireDomainSupport
}

func (b blocker2WireBackendStub) ResolveWireRequest(ctx context.Context, facts largebody.WireRequestFacts, cand routing.AttemptCandidate) largebody.WireRequestSupport {
	return b.reqSupport
}

func (b blocker2WireBackendStub) ResolveWireDomain(ctx context.Context, facts largebody.WireDomainFacts) largebody.WireDomainSupport {
	return b.domainSupport
}

func makeBlocker2Assessor(t *testing.T, genID, domainGenID string, census largebody.DependencyCensus) *ProductionLargeBodyAssessor {
	t.Helper()
	summary, err := largebody.CompileWireEligibilitySummary(largebody.WireEligibilityInput{
		GenerationID:              genID,
		Planes:                    census.Planes,
		Hooks:                     census.Hooks,
		Ports:                     census.Ports,
		TwoPhaseExecutorAvailable: census.TwoPhaseExecutorAvailable,
	}, 4096)
	require.NoError(t, err)

	authGate := largebody.NewAuthorityAssessmentGate(summary, census, genID)
	wb := blocker2WireBackendStub{
		reqSupport:    largebody.WireRequestSupport{Compatible: true, Reason: largebody.WireSupportReasonNone},
		domainSupport: largebody.WireDomainSupport{Compatible: true, AnyAcceptedModel: true, Reason: largebody.WireSupportReasonNone},
	}
	resolver := largebody.WireBackendMap{"default": wb}
	execResolver := routing.BackendExecutionResolverFunc(func(string) (aipsdk.BackendExecutionClass, bool) {
		return aipsdk.BackendExecutionInference, true
	})
	validator := routing.NewGenerationSelectorValidator(nil, "default", map[string]struct{}{"default": {}}, execResolver, config.ExecutionCompositionSafe)
	initialGate := largebody.NewInitialRouteAssessmentGate(nil, "default", execResolver, config.ExecutionCompositionSafe, nil, resolver)
	overrideGate := largebody.NewRouteOverrideAssessmentGate(nil, validator, resolver)
	wireGate := largebody.NewBackendWireProofGate(initialGate, overrideGate, nil, resolver)

	return NewProductionLargeBodyAssessor(genID, domainGenID, authGate, wireGate, StandardLaneDomainPolicies())
}

func makeBlocker2ValidProof(profileID string, op aipapi.Operation, delivery aipapi.DeliveryMode) largebody.Proof {
	return largebody.Proof{
		ProfileID:       profileID,
		Operation:       op,
		Delivery:        delivery,
		RouteSelector:   "gpt-4o",
		ClientModel:     "gpt-4o",
		MaxOutputTokens: 4096,
		Facts: largebody.ProtocolFacts{
			RequirementsID: "std-req-v1",
			ControlCount:   1,
		},
		Mode:      largebody.BodyModeIdentityJSON,
		Rewrite:   largebody.NewNoRewrite(),
		Identity:  largebody.NewIdentityDigest([32]byte{1, 2, 3}),
		Source:    largebody.NewSourceDigest([32]byte{4, 5, 6}),
		BodyBytes: 1024,
	}
}

// TestBlocker2_ResponseAuthority_WireBypassProvesViolation verifies Blocker 2:
// When response-side machinery is configured (response-part hooks, completion gates,
// secure-session recorder), wire execution (ExecuteLargeBody) completely bypasses them.
// Therefore, the conservative fail-safe requires wire eligibility to decline, ensuring
// the canonical path executes all configured response-side authorities.
func TestBlocker2_ResponseAuthority_WireBypassProvesViolation(t *testing.T) {
	ex, _, _ := setupTestExecutor(t)

	// 1. Configure real response hook on Bus
	prefixHook := blocker2PrefixResponseHook{}
	ex.Bus = hooks.New(hooks.Config{
		ResponsePartHooks: []sdkhooks.ResponsePartHook{prefixHook},
	})

	// 2. Configure completion gate on RuntimeSnapshot
	gate := &blocker2TrackingCompletionGate{}
	ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(ex.Bus, extensions.SnapshotOptions{
		FeaturePlanes: freezeBundle(testFeatureBundle{
			CompletionGates: []completion.Gate{gate},
		}),
	})

	// 3. Configure secure-session recorder on Executor
	rec := &blocker2TrackingSecureRecorder{}
	ex.SecureSessionRecorder = rec
	ex.SecureSessionRecordingMandatory = true

	// 4. Configure backend with both wire and canonical handlers emitting "hello"
	ex.Backends = map[string]execbackend.Backend{
		"default": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			OpenWire: func(ctx context.Context, req largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
				return aipapi.CloseOnlyManagedStream{Stream: aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventMessageStarted},
					{Kind: aipapi.EventTextDelta, Delta: "hello"},
					{Kind: aipapi.EventResponseFinished},
				})}, nil
			},
			Open: func(ctx context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return aipapi.CloseOnlyManagedStream{Stream: aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventMessageStarted},
					{Kind: aipapi.EventTextDelta, Delta: "hello"},
					{Kind: aipapi.EventResponseFinished},
				})}, nil
			},
		},
	}
	ex.Rand = routing.NewSeededRng(1)

	// 5. Build census reflecting the configured response authorities
	census := largebody.NewStandardDependencyCensus("gen-1")
	census.Hooks.ResponsePartOccupied = true
	idxHooks, _ := largebody.WireEligibilityPlaneIndex("response_part_hooks")
	census.Planes[idxHooks].Occupied = true
	idxGates, _ := largebody.WireEligibilityPlaneIndex("completion_gates")
	census.Planes[idxGates].Occupied = true
	census.AddPort("security.session_recorder", true)

	ex.LargeBodyAssessor = makeBlocker2Assessor(t, "gen-1", "dom-gen-1", census)

	// 6. Assess a large-body request
	ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "usr-1"})
	ctx = largebody.WithWireIdentity(ctx, "req-1", "trace-1")
	rawJSON := `{"model":"gpt-4o","messages":[{"role":"user","content":"wire test"}]}`
	proof := makeBlocker2ValidProof("openai-chat", aipapi.OperationOpenAIChatCompletions, aipapi.DeliveryModeStreaming)
	proof.Source = largebody.NewSourceDigest(sha256.Sum256([]byte(rawJSON)))
	proof.BodyBytes = int64(len(rawJSON))
	assessment, err := ex.AssessLargeBody(ctx, proof)
	require.NoError(t, err)

	// If wire accepts (as it does on current code), run wire execution and demonstrate bypass.
	if assessment.Decision == largebody.AssessmentDecisionAccept {
		src := newTestSource(rawJSON)
		assessment.WireRequest.CandidateModel = "default:gpt-4o"

		res, err := ex.ExecuteLargeBody(ctx, assessment, src)
		require.NoError(t, err)
		defer func() { _ = res.Stream.Close() }()

		var text strings.Builder
		for {
			ev, rerr := res.Stream.Recv(ctx)
			if rerr != nil {
				break
			}
			if ev.Kind == aipapi.EventTextDelta {
				text.WriteString(ev.Delta)
			}
		}

		// On current code, this asserts that the hook prefix MUST be present,
		// the completion gate MUST have been invoked, and the recorder MUST have been invoked.
		// These will FAIL on current code because ExecuteLargeBody bypasses all of them!
		assert.Equal(t, largebody.AssessmentDecisionDecline, assessment.Decision,
			"wire assessment must decline when response-side authorities are occupied")
		assert.Equal(t, "PREFIX:hello", text.String(),
			"accepted wire response lacks prefix (hook bypassed)")
		assert.True(t, gate.invoked.Load(),
			"accepted wire response bypassed completion gate")
		assert.True(t, rec.postHookStreamEventCalls.Load() > 0,
			"accepted wire response bypassed secure session recorder")
		return
	}

	// Conservative blocker fix (GREEN): wire declined.
	assert.Equal(t, largebody.AssessmentDecisionDecline, assessment.Decision)
	assert.Equal(t, largebody.DeclineReasonAuthorityBlocker, assessment.Reason)

	// Canonical fallback execution runs all response machinery:
	call := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "default:gpt-4o"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hello")},
		}},
	}
	stream, err := ex.Execute(ctx, call)
	require.NoError(t, err)
	defer func() { _ = stream.Close() }()

	var text strings.Builder
	for {
		ev, rerr := stream.Recv(ctx)
		if rerr != nil {
			break
		}
		if ev.Kind == aipapi.EventTextDelta {
			text.WriteString(ev.Delta)
		}
	}
	assert.Equal(t, "PREFIX:hello", text.String(), "canonical execution must run response hook")
	assert.True(t, gate.invoked.Load(), "canonical execution must run completion gate")
	assert.True(t, rec.postHookStreamEventCalls.Load() > 0, "canonical execution must run secure session recorder")
}

// TestBlocker2_ResponseHookChain_DeclinesWireEligibility verifies that an occupied
// response-part hook chain causes wire assessment to decline.
func TestBlocker2_ResponseHookChain_DeclinesWireEligibility(t *testing.T) {
	ex, _, _ := setupTestExecutor(t)

	census := largebody.NewStandardDependencyCensus("gen-1")
	census.Hooks.ResponsePartOccupied = true

	ex.LargeBodyAssessor = makeBlocker2Assessor(t, "gen-1", "dom-gen-1", census)

	proof := makeBlocker2ValidProof("openai-chat", aipapi.OperationOpenAIChatCompletions, aipapi.DeliveryModeStreaming)
	assessment, err := ex.AssessLargeBody(context.Background(), proof)
	require.NoError(t, err)

	assert.Equal(t, largebody.AssessmentDecisionDecline, assessment.Decision,
		"wire assessment must decline when response hook chain is occupied")
	assert.Equal(t, largebody.DeclineReasonAuthorityBlocker, assessment.Reason)
}

// TestBlocker2_CompletionGatePlane_DeclinesWireEligibility verifies that an occupied
// completion_gates plane causes wire assessment to decline.
func TestBlocker2_CompletionGatePlane_DeclinesWireEligibility(t *testing.T) {
	ex, _, _ := setupTestExecutor(t)

	census := largebody.NewStandardDependencyCensus("gen-1")
	idx, _ := largebody.WireEligibilityPlaneIndex("completion_gates")
	census.Planes[idx].Occupied = true

	ex.LargeBodyAssessor = makeBlocker2Assessor(t, "gen-1", "dom-gen-1", census)

	proof := makeBlocker2ValidProof("openai-chat", aipapi.OperationOpenAIChatCompletions, aipapi.DeliveryModeStreaming)
	assessment, err := ex.AssessLargeBody(context.Background(), proof)
	require.NoError(t, err)

	assert.Equal(t, largebody.AssessmentDecisionDecline, assessment.Decision,
		"wire assessment must decline when completion_gates plane is occupied")
	assert.Equal(t, largebody.DeclineReasonAuthorityBlocker, assessment.Reason)
}

// TestBlocker2_StreamObserverFactoryPlane_DeclinesWireEligibility verifies that an occupied
// stream_observer_factories plane causes wire assessment to decline.
func TestBlocker2_StreamObserverFactoryPlane_DeclinesWireEligibility(t *testing.T) {
	ex, _, _ := setupTestExecutor(t)

	census := largebody.NewStandardDependencyCensus("gen-1")
	idx, _ := largebody.WireEligibilityPlaneIndex("stream_observer_factories")
	census.Planes[idx].Occupied = true

	ex.LargeBodyAssessor = makeBlocker2Assessor(t, "gen-1", "dom-gen-1", census)

	proof := makeBlocker2ValidProof("openai-chat", aipapi.OperationOpenAIChatCompletions, aipapi.DeliveryModeStreaming)
	assessment, err := ex.AssessLargeBody(context.Background(), proof)
	require.NoError(t, err)

	assert.Equal(t, largebody.AssessmentDecisionDecline, assessment.Decision,
		"wire assessment must decline when stream_observer_factories plane is occupied")
	assert.Equal(t, largebody.DeclineReasonAuthorityBlocker, assessment.Reason)
}

// TestBlocker2_UsageObserverPlane_DeclinesWireEligibility verifies that an occupied
// usage_observers plane causes wire assessment to decline.
func TestBlocker2_UsageObserverPlane_DeclinesWireEligibility(t *testing.T) {
	ex, _, _ := setupTestExecutor(t)

	census := largebody.NewStandardDependencyCensus("gen-1")
	idx, _ := largebody.WireEligibilityPlaneIndex("usage_observers")
	census.Planes[idx].Occupied = true

	ex.LargeBodyAssessor = makeBlocker2Assessor(t, "gen-1", "dom-gen-1", census)

	proof := makeBlocker2ValidProof("openai-chat", aipapi.OperationOpenAIChatCompletions, aipapi.DeliveryModeStreaming)
	assessment, err := ex.AssessLargeBody(context.Background(), proof)
	require.NoError(t, err)

	assert.Equal(t, largebody.AssessmentDecisionDecline, assessment.Decision,
		"wire assessment must decline when usage_observers plane is occupied")
	assert.Equal(t, largebody.DeclineReasonAuthorityBlocker, assessment.Reason)
}

// TestBlocker2_SecureSessionRecorderPort_AcceptsWireEligibility verifies that an occupied
// security.session_recorder narrow port accepts wire assessment (lifted in Item 5).
func TestBlocker2_SecureSessionRecorderPort_AcceptsWireEligibility(t *testing.T) {
	ex, _, _ := setupTestExecutor(t)

	census := largebody.NewStandardDependencyCensus("gen-1")
	census.AddPort("security.session_recorder", true)

	ex.LargeBodyAssessor = makeBlocker2Assessor(t, "gen-1", "dom-gen-1", census)

	proof := makeBlocker2ValidProof("openai-chat", aipapi.OperationOpenAIChatCompletions, aipapi.DeliveryModeStreaming)
	assessment, err := ex.AssessLargeBody(context.Background(), proof)
	require.NoError(t, err)

	assert.Equal(t, largebody.AssessmentDecisionAccept, assessment.Decision,
		"wire assessment must accept when only security.session_recorder port is occupied (lifted in item 5)")
	assert.Equal(t, largebody.DeclineReasonNone, assessment.Reason)
}

// Unused dummy types to satisfy package references if needed
var (
	_ sdkresponse.StreamObserverFactory = nil
	_ sdkusage.Observer                 = nil
)
