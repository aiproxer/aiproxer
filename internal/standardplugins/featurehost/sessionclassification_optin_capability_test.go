package featurehost

// Task 10.3 consumer capability and neutrality certification.
//
// This file proves the two halves of "the feature is usable, and nothing else
// changed because of it":
//
//   - requirements 1.6 and 4.2: a downstream consumer that opts into
//     classification can gate on the VERY FIRST eligible turn, deciding from the
//     immutable snapshot alone - no User-Agent, no prompt or transcript, no
//     classifier re-run, no vendor result.
//
//   - requirements 1.8 and 11.7: that capability is not exercised by anything
//     that ships. With the plane absent the consumer stays inert, the turn still
//     routes and streams, and the feature emits no observation of any kind.
//
// Everything runs through the real composed surfaces: a real featurehost process,
// a real compiled generation carrying the production classifier, a real
// generic-core executor, the production pre-request plane, and the production
// durable SQLite store.
//
// The wire lane is served by the SAME generation but with the UNMODIFIED plane
// set, because PlanePreRequestHandlers is canonical-required and a consumer there
// statically blocks the large-payload lane (task 7.3). The harness serves a wire
// turn, so "the test-only consumer does not take the wire lane down with it" is
// measured rather than asserted in a comment.

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	sdkclassification "github.com/aiproxer/aiproxer/pkg/aipsdk/sessionclassification"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/uptrace/bun"
)

// optInAmbiguousUserAgent is the identity of the CONTROL session. It is a generic
// SDK value that requirement 3.2 forbids from classifying, so the control turn
// proves the gate is driven by the classification snapshot and not by blanket
// denial: a consumer that denied everything would make the first-turn assertion
// meaningless.
const optInAmbiguousUserAgent = "OpenAI/JS 4.70.0"

// optInTurnPrompt is a real user prompt. It is delivered on both the gated turn
// and the control turn, so the consumer demonstrably had prompt material
// available and still decided from the snapshot alone.
const optInTurnPrompt = "Refactor the parser and run the tests."

// optInRig is one compiled generation's serving surfaces over the shared process
// state. The canonical executor carries the test-only opt-in consumer; the wire
// executor deliberately does not.
type optInRig struct {
	database  *bun.DB
	registry  *prometheus.Registry
	consumer  *optInCodingAgentGate
	canonical *runtime.Executor

	canonicalOpen *atomic.Int32
	wire          *runtime.Executor
	wireOpen      *atomic.Int32
}

// newOptInRig composes one started generation. enabled selects whether the
// session-classification plane is published at all, which is what requirement
// 11.7 needs.
func newOptInRig(tb testing.TB, name string, enabled bool) *optInRig {
	tb.Helper()

	rt, database, registry := hotPathProcess(tb, name)
	generation := certificationCompile(tb, rt, enabled, "")
	if enabled && generation.classifier == nil {
		tb.Fatal("enabled generation published no classifier")
	}
	if !enabled && generation.classifier != nil {
		tb.Fatalf("disabled generation published classifier %v; requirement 11.7 requires an absent plane",
			generation.classifier)
	}
	generation.start(tb)

	consumer := newOptInCodingAgentGate("opt-in-coding-agent-gate")
	canonical, canonicalOpen := certificationExecutor(tb, database,
		certificationPlanesWithConsumer(tb, generation.planes, consumer))
	// The wire lane gets the unmodified plane set. PlanePreRequestHandlers is
	// canonical-required, so adding the consumer there statically blocks the wire
	// lane; that is the condition task 7.3 recorded and it is why this split
	// exists rather than sharing one plane set.
	wire, wireOpen := certificationExecutor(tb, database, generation.planes)

	return &optInRig{
		database:      database,
		registry:      registry,
		consumer:      consumer,
		canonical:     canonical,
		canonicalOpen: canonicalOpen,
		wire:          wire,
		wireOpen:      wireOpen,
	}
}

// TestOptInConsumerGatesOnTheFirstTurnPositiveFromTheSnapshotAlone is the
// composed capability proof for requirements 1.6 and 4.2.
//
// The first eligible turn carries a recognized high-confidence coding-harness
// identity and a real user prompt, and the consumer gates on THAT turn: no second
// turn is served, so a consumer that could only gate from restored state would
// never gate here.
//
// "From the snapshot alone" is then measured, not asserted:
//
//   - the production evaluation census shows exactly ONE evaluation and ONE
//     promotion for the gated turn, so the consumer did not cause a second
//     classifier invocation;
//   - the durable census shows one applied promotion and NO remote lease
//     operation, and the remote observation families are absent, so nothing
//     consulted a vendor-specific result;
//   - the consumer's decision function takes only the snapshot, enforced
//     structurally by TestOptInConsumerDecidesFromTheSnapshotAlone.
//
// The control session carries a generic SDK identity and is neither gated nor
// refused, which is what makes the gate's engagement meaningful. One wire turn is
// then served from the same generation, proving the canonical-only consumer did
// not take the large-payload lane down with it.
func TestOptInConsumerGatesOnTheFirstTurnPositiveFromTheSnapshotAlone(t *testing.T) {
	t.Parallel()

	rig := newOptInRig(t, "opt-in-first-turn.db", true)

	const clientSessionID = "opt-in-gated"
	sessionID, resumeToken := certificationResumableSession(t, rig.canonical, clientSessionID)

	stream, err := rig.canonical.Execute(context.Background(),
		certificationCallWith(clientSessionID, resumeToken, certificationPositiveUserAgent, optInTurnPrompt, ""))
	if err == nil {
		_, err = aipapi.Collect(context.Background(), stream)
	}
	if err == nil {
		t.Fatal("the opt-in consumer did not gate the positively classified first turn")
	}
	if !strings.Contains(err.Error(), optInGateReason) {
		t.Fatalf("first-turn failure = %v, want the opt-in consumer's own rejection %q", err, optInGateReason)
	}
	// The gate engaged during preparation, so the backend was never opened. That is
	// what makes this a real gate rather than an annotation.
	if opens := rig.canonicalOpen.Load(); opens != 0 {
		t.Errorf("the gated turn opened the backend %d times; a pre-request gate must reject before routing", opens)
	}

	turns := rig.consumer.recorded()
	if len(turns) != 1 {
		t.Fatalf("the opt-in consumer observed %d turns, want exactly 1: the gate fired on the FIRST "+
			"eligible turn (requirement 4.2)", len(turns))
	}
	first := turns[0]
	if !first.Snapshot.IsCodingAgent() {
		t.Fatalf("first-turn snapshot = %+v, want a positive the consumer can gate on", first.Snapshot)
	}
	if first.SessionID != sessionID {
		t.Errorf("consumer saw authoritative session %q, want the served session %q", first.SessionID, sessionID)
	}
	// The decisive evidence is the bounded identity rule, so nothing the consumer
	// could have read - the prompt, the transcript, a tool catalog - produced it.
	if !strings.HasPrefix(string(first.Snapshot.Evidence), "client_family.") {
		t.Errorf("first-turn evidence = %q, want a client_family code so the positive came from the bounded "+
			"identity rule rather than from anything the consumer could have re-derived", first.Snapshot.Evidence)
	}
	if !first.Gated {
		t.Error("the consumer recorded an ungated turn for a positive snapshot; its own decision function " +
			"disagrees with the value it recorded")
	}

	// The positive was committed by the same canonical lane the gate ran in.
	record, found := certificationLoadRow(t, rig.database, sessionID)
	if !found || record.Classification != first.Snapshot {
		t.Errorf("durable row = %+v (set=%t), want the exact snapshot the consumer gated on %+v",
			record.Classification, found, first.Snapshot)
	}

	// One evaluation, one promotion: the consumer did not re-run the classifier.
	evaluations := certificationEvaluations(t, rig.registry)
	if got := evaluations["heuristic/promoted"]; got != 1 {
		t.Errorf("production evaluation census = %v, want exactly one heuristic/promoted; a consumer that "+
			"re-ran the classifier would add a second evaluation", evaluations)
	}
	for outcome, count := range evaluations {
		if outcome != "heuristic/promoted" {
			t.Errorf("the gated turn produced classification observation %q x%v, want none besides the "+
				"single promotion", outcome, count)
		}
	}

	// No lease was claimed and no vendor decision was consulted: the bounded
	// evidence carried on the turn was already sufficient.
	operations := certificationStoreOperations(t, rig.registry)
	if got := operations["promote/applied"]; got != 1 {
		t.Errorf("durable store census = %v, want exactly one applied promotion", operations)
	}
	for operation, count := range operations {
		if strings.HasPrefix(operation, "remote_") && count != 0 {
			t.Errorf("the opt-in turn performed durable operation %q x%v; a consumer that consulted a "+
				"vendor-specific result would have claimed a remote lease", operation, count)
		}
	}
	if remotes := optInRemoteObservations(t, rig.registry); len(remotes) != 0 {
		t.Errorf("the opt-in turn emitted remote classification observations %v, want none", remotes)
	}

	// Control: a generic SDK identity is not classified (requirement 3.2), so the
	// same consumer must leave that turn alone AND the proxy must still serve it.
	const controlClientID = "opt-in-control"
	controlSession, controlToken := certificationResumableSession(t, rig.canonical, controlClientID)
	opensBefore := rig.canonicalOpen.Load()
	controlStream, err := rig.canonical.Execute(context.Background(),
		certificationCallWith(controlClientID, controlToken, optInAmbiguousUserAgent, optInTurnPrompt, ""))
	if err != nil {
		t.Fatalf("control turn was rejected: %v; the gate must not fire without a positive classification", err)
	}
	if _, err := aipapi.Collect(context.Background(), controlStream); err != nil {
		t.Fatalf("collect the control turn: %v", err)
	}
	if got := rig.canonicalOpen.Load() - opensBefore; got != 1 {
		t.Errorf("the control turn opened the backend %d times, want exactly 1", got)
	}
	controlTurns := rig.consumer.recorded()
	if len(controlTurns) != 2 {
		t.Fatalf("the consumer observed %d turns in total, want 2 (gated + control)", len(controlTurns))
	}
	if controlTurns[1].Gated || controlTurns[1].Snapshot.IsCodingAgent() {
		t.Errorf("control turn = %+v, want an ungated unknown: a generic SDK identity must not classify",
			controlTurns[1])
	}
	if certificationRowIsPositive(t, rig.database, controlSession) {
		t.Error("the control session owns a durable positive; a generic SDK identity must not classify")
	}

	// The wire lane, served by the same generation with the consumer-free plane
	// set, still routes and streams.
	optInServeWire(t, rig, "opt-in-wire")

	if got := rig.consumer.gated(); got != 1 {
		t.Errorf("the consumer gated %d times over 2 turns, want exactly 1", got)
	}
	if got := rig.consumer.total(); got != 2 {
		t.Errorf("the consumer ran %d times, want 2", got)
	}
	t.Logf("first-turn gate: %+v denied before routing; control %q served normally; wire lane served",
		first.Snapshot, optInAmbiguousUserAgent)
}

// optInRemoteObservations reads the production remote-classification observation
// families. The opt-in turn must produce none: requirement 1.6 forbids a consumer
// from consulting a vendor-specific result, and requirement 6.1 forbids egress
// where remote classification is not configured.
func optInRemoteObservations(tb testing.TB, registry *prometheus.Registry) map[string]float64 {
	tb.Helper()
	families, err := registry.Gather()
	if err != nil {
		tb.Fatalf("gather feature metrics: %v", err)
	}
	observed := map[string]float64{}
	for _, family := range families {
		switch family.GetName() {
		case "aip_session_classification_remote_total", "aip_session_classification_remote_seconds":
		default:
			continue
		}
		for _, metric := range family.GetMetric() {
			observed[fmt.Sprintf("%s/%d", family.GetName(), len(metric.GetLabel()))] += metric.GetCounter().GetValue()
		}
	}
	return observed
}

// optInServeWire serves one large-payload wire turn from the consumer-free plane
// set and requires it to route and stream. It is the measurement behind "a
// canonical-only test consumer does not take the wire lane down with it".
func optInServeWire(t *testing.T, rig *optInRig, clientSessionID string) {
	t.Helper()
	before := rig.wireOpen.Load()
	source := certificationNewWireSource(t, crossHarnessWireBody)
	assessment := certificationWireAssessment(t, source)
	proof := largebody.Proof{
		ProfileID: "openai-chat",
		Operation: aipapi.OperationOpenAIChatCompletions,
		Delivery:  aipapi.DeliveryModeStreaming,
		Identity:  largebody.NewIdentityDigest([32]byte{0x6f, 0x70, 0x74, 0x69}),
		Session:   certificationNewWireSession(clientSessionID),
		ClassificationEvidence: sdkclassification.Evidence{
			Operation: aipapi.OperationOpenAIChatCompletions,
		},
	}
	ctx := largebody.ContextWithWireProof(context.Background(), proof, "req-"+clientSessionID)
	result, err := rig.wire.ExecuteLargeBody(ctx, assessment, source)
	if err != nil {
		t.Fatalf("wire turn %q: ExecuteLargeBody: %v", clientSessionID, err)
	}
	if _, err := aipapi.Collect(context.Background(), result.Stream); err != nil {
		t.Fatalf("collect wire turn %q: %v", clientSessionID, err)
	}
	if got := rig.wireOpen.Load() - before; got != 1 {
		t.Errorf("wire turn %q opened the backend %d times, want exactly 1", clientSessionID, got)
	}
	if result.Session.AuthoritativeSessionID == "" {
		t.Errorf("wire turn %q committed without a proxy-owned authoritative session", clientSessionID)
	}
}

// TestOptInConsumerStaysInertWhileTheFeatureIsAbsent is requirement 11.7 as it
// applies to consumers: with the classification plane absent the test-only
// consumer runs, sees the conservative unknown zero value, does not gate, and the
// proxy still routes and streams the very same turn. The classification
// observation census stays completely empty and the feature's durable schema does
// not even exist, which is the strongest available form of "no classification
// infrastructure is required to serve".
func TestOptInConsumerStaysInertWhileTheFeatureIsAbsent(t *testing.T) {
	t.Parallel()

	rig := newOptInRig(t, "opt-in-absent-plane.db", false)

	const clientSessionID = "opt-in-absent"
	sessionID, resumeToken := certificationResumableSession(t, rig.canonical, clientSessionID)
	stream, err := rig.canonical.Execute(context.Background(),
		certificationCallWith(clientSessionID, resumeToken, certificationPositiveUserAgent, optInTurnPrompt, ""))
	if err != nil {
		t.Fatalf("turn with the classification plane absent was rejected: %v; the proxy must serve without it", err)
	}
	recorder := &falsePositiveStreamRecorder{inner: stream}
	if _, err := aipapi.Collect(context.Background(), recorder); err != nil {
		t.Fatalf("collect the turn: %v", err)
	}
	if !recorder.served() {
		t.Errorf("the absent-plane turn delivered event kinds %v, want a normal started/finished stream", recorder.kinds)
	}
	if got := rig.canonicalOpen.Load(); got != 1 {
		t.Errorf("the absent-plane turn opened the backend %d times, want exactly 1", got)
	}

	turns := rig.consumer.recorded()
	if len(turns) != 1 {
		t.Fatalf("the consumer observed %d turns, want exactly 1", len(turns))
	}
	if turns[0].Gated {
		t.Error("the consumer gated a turn while the classification plane was absent; no consumer gate may " +
			"engage without a classification")
	}
	if turns[0].Snapshot != (session.Classification{}) {
		t.Errorf("absent-plane snapshot = %+v, want the conservative unknown zero value", turns[0].Snapshot)
	}
	if turns[0].SessionID != sessionID {
		t.Errorf("consumer saw authoritative session %q, want %q", turns[0].SessionID, sessionID)
	}

	// No classification infrastructure was required: no durable schema and no
	// observation of any kind. The durable table is not merely empty, it was never
	// created, so there is nothing for a consumer to have consulted.
	if hotPathClassificationTableExists(t, rig.database) {
		t.Error("an absent classification plane created the durable classification schema")
	}
	if observed := hotPathClassificationObservations(t, rig.registry); len(observed) != 0 {
		t.Errorf("an absent classification plane emitted observations %v, want none (requirements 9.6, 10.8, 11.7)",
			observed)
	}

	// The wire lane is unaffected too.
	optInServeWire(t, rig, "opt-in-absent-wire")
	t.Logf("absent plane: consumer ran and stayed inert, backend opened once, stream %v, no schema, no observations",
		recorder.kinds)
}
