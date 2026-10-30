package adapter

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	coremetering "github.com/aiproxer/aiproxer/internal/core/metering"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

// hostBoundNegotiation is the negotiated peer that opted into the host-bound
// economic evidence contract.
func hostBoundNegotiation() backendplugin.Negotiation {
	return backendplugin.Negotiation{
		Compatible:      true,
		NegotiatedMinor: backendplugin.ProtocolMinorHostBoundEconomicEvidenceV2,
		EnabledFeatures: []string{
			backendplugin.FeatureAccountingEvidenceV2,
			backendplugin.FeatureHostBoundEconomicEvidenceV2,
		},
	}
}

// hostBoundStream builds a managed stream for the host-binding proofs.
func hostBoundStream(t *testing.T, policy execbackend.ConnectorEconomicEvidencePolicy, neg backendplugin.Negotiation) *managedStream {
	t.Helper()
	return &managedStream{
		ctx:           context.Background(),
		opt:           Options{Negotiation: neg, ConnectorEconomicEvidencePolicy: policy},
		maxFrame:      int(backendplugin.DefaultMaxStreamFrameBytes),
		claims:        newConnectorClaimState(policy, backendplugin.HostBoundEconomicEvidenceV2Negotiated(neg)),
		lineageBLegID: "bleg-pseudonym",
	}
}

// hostBoundClaim builds one connector provider claim.
func hostBoundClaim(t *testing.T, localID, sourceEventKey string) backendplugin.AccountingEvidenceV2 {
	t.Helper()
	now := time.Unix(1700000000, 0).UTC()
	key := metering.ComponentKey{
		Direction: metering.DirectionInput,
		Component: metering.ComponentImage,
		Unit:      metering.UnitImage,
		SchemaID:  "provider.media.v2",
	}
	value := metering.Decimal{Coefficient: "2", Scale: 0}
	observation := metering.Observation{
		Version:        metering.ObservationVersionV2,
		ID:             localID,
		SourceEventKey: sourceEventKey,
		Revision:       1,
		StreamID:       "connector-stream",
		Origin:         metering.OriginProvider,
		Acquisition:    metering.AcquisitionProviderResponse,
		Authority:      metering.AuthorityObservedClaim,
		Perspective:    metering.PerspectiveOperator,
		Boundary:       metering.BoundaryBackendIngress,
		Lifecycle:      metering.LifecycleBackendAttempt,
		Subject: metering.SubjectRef{
			Kind:    metering.SubjectBLeg,
			StoreID: "connector-placeholder-store",
			BLegID:  "bleg-pseudonym",
		},
		Correlation: metering.CorrelationV2{
			StoreID:            "connector-placeholder-store",
			BLegID:             "bleg-pseudonym",
			ProviderRequestID:  "provider-request-1",
			ProviderAccountKey: "provider-account-1",
		},
		Semantics:  metering.SemanticsDelta,
		ObservedAt: now,
		ReceivedAt: now,
		MappingRef: "provider-schema-v2",
		Measures:   []metering.Measure{{Key: key, Value: &value, Quality: metering.QualityObserved}},
	}
	if err := observation.Validate(); err != nil {
		t.Fatalf("connector claim is not a valid observation: %v", err)
	}
	return backendplugin.AccountingEvidenceV2{Observation: observation, Coverage: backendplugin.EvidenceCoverageComplete}
}

// hostBoundIdentity returns the trusted host facts of one attempt.
func hostBoundIdentity() coremetering.ObservationIdentity {
	return hostBoundIdentityFor("bleg-1", "bleg-1")
}

// hostBoundIdentityFor returns the trusted host facts of one attempt for an
// explicit attempt lineage.
func hostBoundIdentityFor(blegID, attemptID string) coremetering.ObservationIdentity {
	return coremetering.ObservationIdentity{
		StoreID:       "store-1",
		RequestID:     "request-1",
		CallID:        "call-1",
		BillingCallID: "call-1",
		ALegID:        "aleg-1",
		BLegID:        blegID,
		AttemptID:     attemptID,
		AttemptSeq:    2,
		ReceivedAt:    time.Unix(1700000000, 0).UTC(),
		Provider: coremetering.ProviderBinding{
			AccountID:    "acct-host",
			Scope:        scope.PrincipalScopeView{PrincipalID: scope.Known("principal-1")},
			ScopePresent: true,
		},
	}
}

// hostBoundAccept sends the accepted frame the stream validator requires
// before any other frame.
func hostBoundAccept(t *testing.T, stream *managedStream) {
	t.Helper()
	if err := stream.onPluginFrame(backendplugin.ServerFrame{Kind: backendplugin.ServerFrameAccepted}); err != nil {
		t.Fatalf("accepted frame: %v", err)
	}
}

// hostBoundSendAccountingFrame sends one accounting evidence frame.
func hostBoundSendAccountingFrame(t *testing.T, stream *managedStream, evidence *backendplugin.AccountingEvidenceV2, sequence uint64) error {
	t.Helper()
	return stream.onPluginFrame(backendplugin.ServerFrame{
		Kind:         backendplugin.ServerFrameAccountingEvidence,
		Sequence:     sequence,
		AccountingV2: evidence,
	})
}

func TestHostBinding_NonOptedPeerClaimIsDroppedWithCaptureLoss(t *testing.T) {
	t.Parallel()
	stream := hostBoundStream(t, execbackend.ConnectorEconomicEvidenceDegrade, phase7Negotiation())
	evidence := hostBoundClaim(t, "local-1", "source-event-1")
	hostBoundAccept(t, stream)
	if err := hostBoundSendAccountingFrame(t, stream, &evidence, 1); err != nil {
		t.Fatalf("non-opted peer frame: %v", err)
	}
	admitted, rejected := stream.drainBoundClaims()
	if len(admitted) != 0 {
		t.Fatalf("admitted observations = %d, want none (a non-opted peer is never rewritten or retained)", len(admitted))
	}
	if len(rejected) != 1 {
		t.Fatalf("rejections = %d, want one", len(rejected))
	}
	if rejected[0].RejectionReason != string(execbackend.ConnectorEvidenceRejectedPeerNotHostBound) {
		t.Fatalf("rejection reason = %q, want %q", rejected[0].RejectionReason, execbackend.ConnectorEvidenceRejectedPeerNotHostBound)
	}
	if rejected[0].Observation.ID != "" {
		t.Fatal("a rejected claim carries an observation")
	}
}

func TestHostBinding_OptedPeerClaimIsBoundIntoTheHostNamespace(t *testing.T) {
	t.Parallel()
	stream := hostBoundStream(t, execbackend.ConnectorEconomicEvidenceDegrade, hostBoundNegotiation())
	stream.BindEconomicEvidence(hostBoundIdentity())
	evidence := hostBoundClaim(t, "local-1", "source-event-1")
	hostBoundAccept(t, stream)
	if err := hostBoundSendAccountingFrame(t, stream, &evidence, 1); err != nil {
		t.Fatalf("opted peer frame: %v", err)
	}
	admitted, rejected := stream.drainBoundClaims()
	if len(rejected) != 0 {
		t.Fatalf("rejections = %+v, want none", rejected)
	}
	if len(admitted) != 1 {
		t.Fatalf("admitted observations = %d, want one", len(admitted))
	}
	bound := admitted[0]
	if !strings.HasPrefix(bound.ID, execbackend.HostBoundObservationIDPrefix) {
		t.Fatalf("bound id = %q, want the host namespace prefix", bound.ID)
	}
	if bound.Subject.StoreID != "store-1" || bound.Subject.AccountID != "acct-host" {
		t.Fatalf("bound subject = %+v, want the host-stamped store and frozen account", bound.Subject)
	}
}

func TestHostBinding_PreBindClaimIsBoundOnceWithItsOriginalReceipt(t *testing.T) {
	t.Parallel()
	stream := hostBoundStream(t, execbackend.ConnectorEconomicEvidenceDegrade, hostBoundNegotiation())
	evidence := hostBoundClaim(t, "local-1", "source-event-1")
	hostBoundAccept(t, stream)
	if err := hostBoundSendAccountingFrame(t, stream, &evidence, 1); err != nil {
		t.Fatalf("pre-bind frame: %v", err)
	}
	admitted, _ := stream.drainBoundClaims()
	if len(admitted) != 0 {
		t.Fatalf("admitted observations = %d, want none before the bind", len(admitted))
	}
	identity := hostBoundIdentity()
	stream.BindEconomicEvidence(identity)
	admitted, rejected := stream.drainBoundClaims()
	if len(rejected) != 0 {
		t.Fatalf("rejections = %+v, want none", rejected)
	}
	if len(admitted) != 1 {
		t.Fatalf("admitted observations = %d, want the queued claim", len(admitted))
	}
	if admitted[0].ReceivedAt.IsZero() {
		t.Fatal("the bound claim lost its host receipt time")
	}
}

func TestHostBinding_DegradePolicyKeepsTheSessionAlive(t *testing.T) {
	t.Parallel()
	stream := hostBoundStream(t, execbackend.ConnectorEconomicEvidenceDegrade, hostBoundNegotiation())
	stream.BindEconomicEvidence(hostBoundIdentity())
	// A connector claim that contradicts the trusted attempt identity is
	// rejected; the default policy continues the request.
	evidence := hostBoundClaim(t, "local-1", "source-event-1")
	evidence.Observation.Subject.ALegID = "foreign-aleg"
	hostBoundAccept(t, stream)
	if err := hostBoundSendAccountingFrame(t, stream, &evidence, 1); err != nil {
		t.Fatalf("degrade frame: %v", err)
	}
	_, rejected := stream.drainBoundClaims()
	if len(rejected) != 1 {
		t.Fatalf("rejections = %d, want one", len(rejected))
	}
	if rejected[0].RejectionReason != string(execbackend.ConnectorEvidenceRejectedTrustAttemptIdentityConflict) {
		t.Fatalf("rejection reason = %q", rejected[0].RejectionReason)
	}
}

func TestHostBinding_StrictPolicyRefusesBeforeCommitmentWithTheRecognizedTerminal(t *testing.T) {
	t.Parallel()
	stream := hostBoundStream(t, execbackend.ConnectorEconomicEvidenceStrict, hostBoundNegotiation())
	stream.BindEconomicEvidence(hostBoundIdentity())
	evidence := hostBoundClaim(t, "local-1", "source-event-1")
	evidence.Observation.Subject.ALegID = "foreign-aleg"
	hostBoundAccept(t, stream)
	err := hostBoundSendAccountingFrame(t, stream, &evidence, 1)
	if err == nil {
		t.Fatal("the strict policy did not refuse the unadmittable claim")
	}
	var failure *ExecuteFailureError
	if !errors.As(err, &failure) {
		t.Fatalf("error = %T, want *ExecuteFailureError", err)
	}
	if failure.Kind != executeFailureHostEvidenceRefused {
		t.Fatalf("failure kind = %v, want the private host evidence refusal", failure.Kind)
	}
	if failure.InvalidatesGeneration() {
		t.Fatal("the refusal invalidated the generation")
	}
	// The kind survives the reclassification boundary.
	classified := ClassifyExecuteError(err, false)
	if classified.Kind != executeFailureHostEvidenceRefused {
		t.Fatalf("classified kind = %v, want the private host evidence refusal", classified.Kind)
	}
	mapped := classified.ToClassifiedError()
	if mapped.Code != "failed_precondition" {
		t.Fatalf("classified code = %q, want the existing failed_precondition terminal", mapped.Code)
	}
	if mapped.Retryable {
		t.Fatal("the classified refusal is retryable")
	}
	if mapped.OutputCommitted {
		t.Fatal("the classified refusal claims committed output")
	}
	// The callback performed no close and no cancel.
	if stream.closed.Load() {
		t.Fatal("the refusal closed the managed stream")
	}
	if stream.ctx != nil && stream.ctx.Err() != nil {
		t.Fatal("the refusal cancelled the execute context")
	}
}

func TestHostBinding_PostCommitRejectionNeverRaisesAnError(t *testing.T) {
	t.Parallel()
	for _, policy := range []execbackend.ConnectorEconomicEvidencePolicy{
		execbackend.ConnectorEconomicEvidenceDegrade,
		execbackend.ConnectorEconomicEvidenceStrict,
	} {
		stream := hostBoundStream(t, policy, hostBoundNegotiation())
		stream.BindEconomicEvidence(hostBoundIdentity())
		if !stream.commitOutput() {
			t.Fatalf("the output did not commit under %q", policy)
		}
		evidence := hostBoundClaim(t, "local-1", "source-event-1")
		evidence.Observation.Subject.ALegID = "foreign-aleg"
		hostBoundAccept(t, stream)
		if err := hostBoundSendAccountingFrame(t, stream, &evidence, 1); err != nil {
			t.Fatalf("post-commit frame under %q: %v", policy, err)
		}
		_, rejected := stream.drainBoundClaims()
		if len(rejected) != 1 {
			t.Fatalf("post-commit rejections = %d, want one", len(rejected))
		}
	}
}

func TestHostBinding_ExactReplayIsANoOp(t *testing.T) {
	t.Parallel()
	stream := hostBoundStream(t, execbackend.ConnectorEconomicEvidenceDegrade, hostBoundNegotiation())
	stream.BindEconomicEvidence(hostBoundIdentity())
	evidence := hostBoundClaim(t, "local-1", "source-event-1")
	hostBoundAccept(t, stream)
	for i := range 2 {
		if err := hostBoundSendAccountingFrame(t, stream, &evidence, uint64(i+1)); err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
	}
	admitted, rejected := stream.drainBoundClaims()
	if len(rejected) != 0 {
		t.Fatalf("rejections = %+v, want none", rejected)
	}
	if len(admitted) != 1 {
		t.Fatalf("admitted observations = %d, want one replay-safe provider charge", len(admitted))
	}
}

func TestHostBinding_ChangedPayloadUnderOneBoundIdentityPreservesTheFirstBinding(t *testing.T) {
	t.Parallel()
	stream := hostBoundStream(t, execbackend.ConnectorEconomicEvidenceDegrade, hostBoundNegotiation())
	stream.BindEconomicEvidence(hostBoundIdentity())
	first := hostBoundClaim(t, "local-1", "source-event-1")
	second := hostBoundClaim(t, "local-1", "source-event-1")
	second.Observation.Measures = append([]metering.Measure(nil), first.Observation.Measures...)
	value := metering.Decimal{Coefficient: "3", Scale: 0}
	second.Observation.Measures[0].Value = &value
	hostBoundAccept(t, stream)
	for i, evidence := range []*backendplugin.AccountingEvidenceV2{&first, &second} {
		frame := backendplugin.ServerFrame{Kind: backendplugin.ServerFrameAccountingEvidence, Sequence: uint64(i + 1), AccountingV2: evidence}
		if err := stream.onPluginFrame(frame); err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
	}
	admitted, rejected := stream.drainBoundClaims()
	if len(admitted) != 1 {
		t.Fatalf("admitted observations = %d, want the first binding only", len(admitted))
	}
	if admitted[0].Measures[0].Value.Coefficient != "2" {
		t.Fatal("the first binding was not preserved")
	}
	if len(rejected) != 1 {
		t.Fatalf("rejections = %d, want one", len(rejected))
	}
	if rejected[0].RejectionReason != string(execbackend.ConnectorEvidenceRejectedPayloadConflict) {
		t.Fatalf("rejection reason = %q, want %q", rejected[0].RejectionReason, execbackend.ConnectorEvidenceRejectedPayloadConflict)
	}
}

func TestHostBinding_RejectionRecordsCarryOnlyTheClosedReasonToken(t *testing.T) {
	t.Parallel()
	stream := hostBoundStream(t, execbackend.ConnectorEconomicEvidenceDegrade, phase7Negotiation())
	evidence := hostBoundClaim(t, "local-1", "source-event-1")
	hostBoundAccept(t, stream)
	if err := hostBoundSendAccountingFrame(t, stream, &evidence, 1); err != nil {
		t.Fatalf("frame: %v", err)
	}
	_, rejected := stream.drainBoundClaims()
	if len(rejected) != 1 {
		t.Fatalf("rejections = %d, want one", len(rejected))
	}
	record := rejected[0]
	if record.Observation.ID != "" || record.Observation.SourceEventKey != "" {
		t.Fatal("a rejection record carries connector content")
	}
	if record.Coverage != "" || record.CoverageReason != "" {
		t.Fatal("a rejection record carries a coverage disposition")
	}
	if record.RejectionReason == "" {
		t.Fatal("a rejection record carries no reason token")
	}
}

// A strict refusal that is published before the output commits owns the
// attempt: the output must not commit and the content must not reach the
// caller. Exactly one of the two decisions is taken.
func TestHostBinding_PendingRefusalPreventsALaterOutputCommitment(t *testing.T) {
	t.Parallel()
	stream := hostBoundManagedStream(t, execbackend.ConnectorEconomicEvidenceStrict, hostBoundNegotiation())
	hostBoundAccept(t, stream)
	claim := hostBoundClaim(t, "local-1", "source-event-1")
	claim.Observation.Subject.ALegID = "foreign-aleg"
	if err := hostBoundSendAccountingFrame(t, stream, &claim, 1); err != nil {
		t.Fatalf("pre-bind frame: %v", err)
	}
	stream.BindEconomicEvidence(hostBoundIdentity())

	// A content frame arrives after the refusal was published.
	if err := stream.onPluginFrame(hostBoundContentFrame(2)); err != nil {
		t.Fatalf("content frame: %v", err)
	}
	if stream.OutputCommitted() {
		t.Fatal("the output committed although a strict refusal was pending")
	}
	if len(stream.events) != 0 {
		t.Fatalf("the refused content was queued: %d events", len(stream.events))
	}
	_, err := stream.Recv(context.Background())
	var classified *ClassifiedError
	if !errors.As(err, &classified) || classified.Code != "failed_precondition" || classified.Retryable || classified.OutputCommitted {
		t.Fatalf("the pending refusal was not delivered as the recognized non-retryable refusal: %T %v", err, err)
	}
}

// An output that committed first keeps the committed stream: a refusal
// published afterwards is dropped and the content is preserved.
func TestHostBinding_CommittedOutputDropsALaterPendingRefusal(t *testing.T) {
	t.Parallel()
	stream := hostBoundManagedStream(t, execbackend.ConnectorEconomicEvidenceStrict, hostBoundNegotiation())
	hostBoundAccept(t, stream)
	if err := stream.onPluginFrame(hostBoundContentFrame(1)); err != nil {
		t.Fatalf("content frame: %v", err)
	}
	if !stream.OutputCommitted() {
		t.Fatal("the output did not commit")
	}
	claim := hostBoundClaim(t, "local-1", "source-event-1")
	claim.Observation.Subject.ALegID = "foreign-aleg"
	if err := hostBoundSendAccountingFrame(t, stream, &claim, 2); err != nil {
		t.Fatalf("post-commit accounting frame: %v", err)
	}
	stream.BindEconomicEvidence(hostBoundIdentity())

	ev, err := stream.Recv(context.Background())
	if err != nil {
		t.Fatalf("the committed stream did not deliver its content: %T %v", err, err)
	}
	if ev.Kind != aipapi.EventTextDelta {
		t.Fatalf("delivered event = %v, want the committed content", ev.Kind)
	}
}

// The refusal delivery and the output commitment are one linearizable
// transition: an output commitment that lands inside the refusal delivery
// window cannot follow the refusal, and the refusal cannot follow an output
// commitment. Exactly one of the two decisions is taken.
func TestHostBinding_PendingRefusalDeliveryAndOutputCommitmentAreLinearizable(t *testing.T) {
	t.Parallel()
	stream := hostBoundManagedStream(t, execbackend.ConnectorEconomicEvidenceStrict, hostBoundNegotiation())
	hostBoundAccept(t, stream)
	claim := hostBoundClaim(t, "local-1", "source-event-1")
	claim.Observation.Subject.ALegID = "foreign-aleg"
	if err := hostBoundSendAccountingFrame(t, stream, &claim, 1); err != nil {
		t.Fatalf("pre-bind frame: %v", err)
	}
	stream.BindEconomicEvidence(hostBoundIdentity())

	// The output commitment lands inside the refusal delivery window.
	stream.beforeRefusalDelivery = func() {
		if err := stream.onPluginFrame(hostBoundContentFrame(2)); err != nil {
			t.Fatalf("content frame inside the delivery window: %v", err)
		}
	}
	_, err := stream.Recv(context.Background())
	var classified *ClassifiedError
	if !errors.As(err, &classified) || classified.Code != "failed_precondition" || classified.Retryable || classified.OutputCommitted {
		t.Fatalf("the pending refusal was not delivered as the recognized non-retryable refusal: %T %v", err, err)
	}
	if stream.OutputCommitted() {
		t.Fatal("the output committed although the refusal won the decision")
	}
	if len(stream.events) != 0 {
		t.Fatalf("the refused content was queued: %d events", len(stream.events))
	}
}

// hostBoundContentFrame builds a frame that commits the client's output.
func hostBoundContentFrame(sequence uint64) backendplugin.ServerFrame {
	delta := "committed content"
	return backendplugin.ServerFrame{
		Kind:     backendplugin.ServerFrameEvent,
		Sequence: sequence,
		Event: &backendplugin.CanonicalEvent{
			Kind:  backendplugin.EventTextDelta,
			Delta: &delta,
		},
	}
}

// hostBoundManagedStream builds a managed stream whose delivery path is
// usable: the event channel and the error channel exist.
func hostBoundManagedStream(t *testing.T, policy execbackend.ConnectorEconomicEvidencePolicy, neg backendplugin.Negotiation) *managedStream {
	t.Helper()
	stream := hostBoundStream(t, policy, neg)
	stream.events = make(chan aipapi.Event, 1)
	stream.errCh = make(chan error, 1)
	return stream
}

// hostBoundSuccessor builds a successor claim that references a predecessor
// with the given payload hash.
func hostBoundSuccessor(t *testing.T, localID, sourceEventKey, predecessorID string, payloadHash string) backendplugin.AccountingEvidenceV2 {
	t.Helper()
	claim := hostBoundClaim(t, localID, sourceEventKey)
	claim.Observation.Semantics = metering.SemanticsCorrection
	claim.Observation.Supersedes = []metering.ObservationRef{{
		StoreID:       "connector-placeholder-store",
		ObservationID: predecessorID,
		Revision:      1,
		PayloadHash:   payloadHash,
	}}
	return claim
}

// An ordinary runtime drain does not finalize an unresolved reference: the
// predecessor is released as soon as it arrives, even after several drains.
func TestHostBinding_IntermediateDrainKeepsTheHeldClaimAndResolvesLater(t *testing.T) {
	t.Parallel()
	stream := hostBoundStream(t, execbackend.ConnectorEconomicEvidenceDegrade, hostBoundNegotiation())
	stream.BindEconomicEvidence(hostBoundIdentity())
	hostBoundAccept(t, stream)

	predecessor := hostBoundClaim(t, "local-predecessor", "source-event-predecessor")
	successor := hostBoundSuccessor(t, "local-successor", "source-event-successor", predecessor.Observation.ID, predecessor.Observation.Fingerprint())
	if err := hostBoundSendAccountingFrame(t, stream, &successor, 1); err != nil {
		t.Fatalf("successor frame: %v", err)
	}
	if got := stream.DrainEconomicEvidenceRecords(); len(got) != 0 {
		t.Fatalf("an intermediate drain finalized the unresolved reference: %+v", got)
	}
	if err := hostBoundSendAccountingFrame(t, stream, &predecessor, 2); err != nil {
		t.Fatalf("predecessor frame: %v", err)
	}
	got := stream.DrainEconomicEvidenceRecords()
	if len(got) != 2 {
		t.Fatalf("the predecessor and held successor were not both admitted after the intermediate drain: %+v", got)
	}
}

// A predecessor that is separated by several drains still resolves.
func TestHostBinding_ForwardReferenceResolvesAcrossSeveralDrains(t *testing.T) {
	t.Parallel()
	stream := hostBoundStream(t, execbackend.ConnectorEconomicEvidenceDegrade, hostBoundNegotiation())
	stream.BindEconomicEvidence(hostBoundIdentity())
	hostBoundAccept(t, stream)

	predecessor := hostBoundClaim(t, "local-predecessor", "source-event-predecessor")
	successor := hostBoundSuccessor(t, "local-successor", "source-event-successor", predecessor.Observation.ID, predecessor.Observation.Fingerprint())
	if err := hostBoundSendAccountingFrame(t, stream, &successor, 1); err != nil {
		t.Fatalf("successor frame: %v", err)
	}
	for i := range 3 {
		if got := stream.DrainEconomicEvidenceRecords(); len(got) != 0 {
			t.Fatalf("drain %d finalized the unresolved reference: %+v", i, got)
		}
	}
	if err := hostBoundSendAccountingFrame(t, stream, &predecessor, 2); err != nil {
		t.Fatalf("predecessor frame: %v", err)
	}
	got := stream.DrainEconomicEvidenceRecords()
	if len(got) != 2 {
		t.Fatalf("admitted records = %d, want the predecessor and the held successor", len(got))
	}
}

// The terminal drain rejects an unresolved claim exactly once. A second
// terminal drain reports nothing.
func TestHostBinding_TerminalDrainRejectsTheUnresolvedHeldClaimExactlyOnce(t *testing.T) {
	t.Parallel()
	stream := hostBoundStream(t, execbackend.ConnectorEconomicEvidenceDegrade, hostBoundNegotiation())
	stream.BindEconomicEvidence(hostBoundIdentity())
	hostBoundAccept(t, stream)

	successor := hostBoundSuccessor(t, "local-successor", "source-event-successor", "never-arrives", strings.Repeat("a", 64))
	if err := hostBoundSendAccountingFrame(t, stream, &successor, 1); err != nil {
		t.Fatalf("successor frame: %v", err)
	}
	if got := stream.DrainEconomicEvidenceRecords(); len(got) != 0 {
		t.Fatalf("an intermediate drain finalized the unresolved reference: %+v", got)
	}
	// The evidence window closes with the plugin's terminal frame.
	if err := stream.onPluginFrame(backendplugin.ServerFrame{
		Kind:     backendplugin.ServerFrameTerminal,
		Sequence: 2,
		Terminal: &backendplugin.Terminal{Status: backendplugin.TerminalSuccess},
	}); err != nil {
		t.Fatalf("terminal frame: %v", err)
	}
	got := stream.DrainEconomicEvidenceRecords()
	if len(got) != 1 || got[0].RejectionReason != string(execbackend.ConnectorEvidenceRejectedPredecessorAbsent) {
		t.Fatalf("terminal drain rejections = %+v, want one predecessor_absent", got)
	}
	if again := stream.DrainEconomicEvidenceRecords(); len(again) != 0 {
		t.Fatalf("a second terminal drain reported %+v, want nothing", again)
	}
}

// A held claim that is rejected while it is released produces exactly one
// closed rejection record that carries no observation, no coverage
// disposition and no connector content.
func TestHostBinding_HeldClaimWithAWrongHashRecordsTheP2CaptureLoss(t *testing.T) {
	t.Parallel()
	state := newConnectorClaimState(execbackend.ConnectorEconomicEvidenceDegrade, true)
	trusted := hostBoundIdentity()
	state.bindTrusted(trusted, "bleg-pseudonym", trusted.ReceivedAt)
	predecessor := hostBoundClaim(t, "local-predecessor", "source-event-predecessor")
	successor := hostBoundSuccessor(t, "local-successor", "source-event-successor", predecessor.Observation.ID, strings.Repeat("0", 64))
	now := time.Now().UTC()
	if reason := state.accept(successor, now); reason != "" {
		t.Fatalf("forward reference should be held, got %q", reason)
	}
	if reason := state.accept(predecessor, now.Add(time.Second)); reason != "" {
		t.Fatalf("predecessor should bind, got %q", reason)
	}
	_, rejected := state.drain()
	if len(rejected) != 1 || rejected[0].RejectionReason != string(execbackend.ConnectorEvidenceRejectedSourceFingerprintMismatch) {
		t.Fatalf("released bad reference had no explicit rejection record: %+v", rejected)
	}
	record := rejected[0]
	if record.Observation.ID != "" || record.Observation.SourceEventKey != "" {
		t.Fatal("a rejection record carries connector content")
	}
	if record.Coverage != "" || record.CoverageReason != "" {
		t.Fatal("a rejection record carries a coverage disposition")
	}
}

// The strict policy refuses the attempt when a claim the bind released is
// rejected.
func TestHostBinding_StrictReleaseRejectionRefusesTheUncommittedAttempt(t *testing.T) {
	t.Parallel()
	stream := hostBoundStream(t, execbackend.ConnectorEconomicEvidenceStrict, hostBoundNegotiation())
	stream.BindEconomicEvidence(hostBoundIdentity())
	hostBoundAccept(t, stream)

	predecessor := hostBoundClaim(t, "local-predecessor", "source-event-predecessor")
	successor := hostBoundSuccessor(t, "local-successor", "source-event-successor", predecessor.Observation.ID, strings.Repeat("0", 64))
	if err := hostBoundSendAccountingFrame(t, stream, &successor, 1); err != nil {
		t.Fatalf("successor frame: %v", err)
	}
	err := hostBoundSendAccountingFrame(t, stream, &predecessor, 2)
	var failure *ExecuteFailureError
	if !errors.As(err, &failure) || failure.Kind != executeFailureHostEvidenceRefused {
		t.Fatalf("the strict policy did not refuse the released rejection: %T %v", err, err)
	}
	if failure.InvalidatesGeneration() {
		t.Fatal("the refusal invalidated the generation")
	}
	_, rejected := stream.drainBoundClaims()
	if len(rejected) != 1 || rejected[0].RejectionReason != string(execbackend.ConnectorEvidenceRejectedSourceFingerprintMismatch) {
		t.Fatalf("rejections = %+v, want the recorded source fingerprint mismatch", rejected)
	}
}

// A claim whose Correlation carrier contradicts the trusted attempt identity is
// rejected as a conflicting claim.
func TestHostBinding_CorrelationCarrierConflictIsRejected(t *testing.T) {
	t.Parallel()
	stream := hostBoundStream(t, execbackend.ConnectorEconomicEvidenceDegrade, hostBoundNegotiation())
	stream.BindEconomicEvidence(hostBoundIdentity())
	hostBoundAccept(t, stream)
	claim := hostBoundClaim(t, "local-correlation", "source-event-correlation")
	claim.Observation.Correlation.ALegID = "foreign-aleg"
	if err := hostBoundSendAccountingFrame(t, stream, &claim, 1); err != nil {
		t.Fatalf("accounting frame: %v", err)
	}
	_, rejected := stream.drainBoundClaims()
	if len(rejected) != 1 || rejected[0].RejectionReason != string(execbackend.ConnectorEvidenceRejectedTrustAttemptIdentityConflict) {
		t.Fatalf("conflicting correlation lineage was not rejected: %+v", rejected)
	}
}

// The degrade policy records the capture loss of a claim the bind rejected and
// lets the request continue.
func TestHostBinding_DegradePolicyRecordsThePreBindLossAndContinues(t *testing.T) {
	t.Parallel()
	stream := hostBoundManagedStream(t, execbackend.ConnectorEconomicEvidenceDegrade, hostBoundNegotiation())
	hostBoundAccept(t, stream)
	claim := hostBoundClaim(t, "local-1", "source-event-1")
	claim.Observation.Subject.ALegID = "foreign-aleg"
	if err := hostBoundSendAccountingFrame(t, stream, &claim, 1); err != nil {
		t.Fatalf("pre-bind frame: %v", err)
	}
	stream.BindEconomicEvidence(hostBoundIdentity())
	_, rejected := stream.drainBoundClaims()
	if len(rejected) != 1 || rejected[0].RejectionReason != string(execbackend.ConnectorEvidenceRejectedTrustAttemptIdentityConflict) {
		t.Fatalf("rejections = %+v, want the recorded trust rejection", rejected)
	}
	if stream.refusalPending() {
		t.Fatal("the degrade policy published a pending refusal")
	}
}

// A committed stream keeps its content: the pending refusal of a claim the
// bind rejected is dropped, the capture loss stays recorded and no error is
// raised.
func TestHostBinding_PendingRefusalAfterCommittedContentIsNoError(t *testing.T) {
	t.Parallel()
	stream := hostBoundManagedStream(t, execbackend.ConnectorEconomicEvidenceStrict, hostBoundNegotiation())
	hostBoundAccept(t, stream)
	claim := hostBoundClaim(t, "local-1", "source-event-1")
	claim.Observation.Subject.ALegID = "foreign-aleg"
	if err := hostBoundSendAccountingFrame(t, stream, &claim, 1); err != nil {
		t.Fatalf("pre-bind frame: %v", err)
	}
	// The output committed before the bind.
	if !stream.commitOutput() {
		t.Fatal("the output did not commit")
	}
	stream.BindEconomicEvidence(hostBoundIdentity())
	if stream.refusalPending() {
		t.Fatal("a committed stream published a pending refusal")
	}
	_, rejected := stream.drainBoundClaims()
	if len(rejected) != 1 || rejected[0].RejectionReason != string(execbackend.ConnectorEvidenceRejectedTrustAttemptIdentityConflict) {
		t.Fatalf("rejections = %+v, want the recorded trust rejection", rejected)
	}
}

// A claim that arrives before the runtime bound the attempt is refused with
// the recognized non-retryable pre-commit refusal on the real route: the
// frame callback, the session, the stream and the core's receive boundary.
func TestHostBinding_StrictPreBindRejectionRefusesBeforeCommitment(t *testing.T) {
	t.Parallel()
	claims := make(chan backendplugin.AccountingEvidenceV2, 1)
	claimSent := make(chan error, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseSession := func() { releaseOnce.Do(func() { close(release) }) }
	sess := &dummyExecuteSession{executeFn: func(stream backendplugin.ExecuteStream) error {
		if _, err := stream.Recv(); err != nil {
			return err
		}
		if err := stream.Send(backendplugin.ServerFrame{Kind: backendplugin.ServerFrameAccepted}); err != nil {
			return err
		}
		evidence := <-claims
		err := stream.Send(backendplugin.ServerFrame{
			Kind:         backendplugin.ServerFrameAccountingEvidence,
			Sequence:     1,
			AccountingV2: &evidence,
		})
		claimSent <- err
		if err != nil {
			return err
		}
		<-release
		return nil
	}}
	evidenceStream, err := openStream(context.Background(), sess, Options{
		InstanceID:                      "prebind-instance",
		Negotiation:                     hostBoundNegotiation(),
		ConnectorEconomicEvidencePolicy: execbackend.ConnectorEconomicEvidenceStrict,
	}, testCallWithMessages("prebind-request"), routing.AttemptCandidate{
		Key:     "prebind-candidate",
		Primary: routing.Primary{Backend: "prebind-backend", Model: "prebind-model"},
	})
	if err != nil {
		t.Fatalf("openStream: %v", err)
	}
	defer func() {
		releaseSession()
		_ = evidenceStream.Close()
	}()
	stream, ok := evidenceStream.(*managedStream)
	if !ok {
		t.Fatalf("stream type = %T, want *managedStream", evidenceStream)
	}

	claim := hostBoundClaim(t, "local-1", "source-event-1")
	claim.Observation.Subject.BLegID = stream.lineageBLegID
	claim.Observation.Correlation.BLegID = stream.lineageBLegID
	claim.Observation.Subject.ALegID = "foreign-aleg"
	claims <- claim
	if err := <-claimSent; err != nil {
		t.Fatalf("pre-bind claim frame was unexpectedly refused before binding: %v", err)
	}

	// This is the runtime's actual post-Open binding seam. The claim was
	// queued by the frame callback; the bind must not swallow the strict
	// pre-commit rejection.
	stream.BindEconomicEvidence(hostBoundIdentityFor(stream.lineageBLegID, stream.lineageBLegID))
	releaseSession()
	_, err = evidenceStream.Recv(context.Background())
	var classified *ClassifiedError
	if !errors.As(err, &classified) || classified.Code != "failed_precondition" || classified.Retryable || classified.OutputCommitted {
		t.Fatalf("strict pre-bind rejection did not reach the recognized non-retryable pre-commit refusal; got %T %v", err, err)
	}
	// The refusal is not reclassified as a cancellation and it reaches the
	// frontend reject branch.
	if _, ok := errors.AsType[*aipapi.RejectError](err); !ok {
		t.Fatalf("the refusal did not bridge to the frontend reject branch: %T %v", err, err)
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("the refusal was reclassified as a cancellation: %v", err)
	}
}
