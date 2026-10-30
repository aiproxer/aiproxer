package sessionclassification_test

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	featureclassification "github.com/aiproxer/aiproxer/internal/plugins/features/sessionclassification"
	"github.com/aiproxer/aiproxer/internal/standardplugins/featurehost/sessionclassification"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	sdkclassification "github.com/aiproxer/aiproxer/pkg/aipsdk/sessionclassification"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

const (
	hostileSessionID = "sess-secret-7c41ab90"
	hostileALegID    = "a-leg-secret-7c41ab90"
	hostileUserAgent = "codex_cli_rs/9.9.9-secret-build"
	hostilePath      = "/home/dev/private-repo/internal/secret_handler.go"
	hostilePrompt    = "refactor secret_handler.go to use a worker pool"
	hostileMarker    = "private-repo-go.mod"

	// wantRemoteLatencyBuckets is the fixed finite latency bound set the remote
	// histogram must export for every observed outcome.
	wantRemoteLatencyBuckets = 13
)

// stubStateAuthority models a process that never prepared classification state.
// Every turn therefore fails open through the bounded state-unavailable outcome
// without ever writing a session- or request-shaped label.
type stubStateAuthority struct{}

func (stubStateAuthority) ClassificationState() (featureclassification.Store, error) {
	return nil, errors.New("process state is not initialized")
}

// coordinatorAuthority satisfies the feature-owned state-authority contract with
// a real coordinator, mirroring what the process state holder publishes once an
// enabled generation has initialized state.
type coordinatorAuthority struct{ store featureclassification.Store }

func (a coordinatorAuthority) ClassificationState() (featureclassification.Store, error) {
	return a.store, nil
}

// coalescingRaceStore wraps a real memory store and blocks the first durable
// promotion. While that promotion is in flight the durable state holds no
// positive, so a second turn for the same proxy-authority key must also miss on
// Load and join the coordinator's in-flight Promote. That makes the
// first-positive race deterministic instead of timing dependent.
type coalescingRaceStore struct {
	*sessionclassification.MemoryStore

	loads    atomic.Int32
	promoted chan struct{}
	release  chan struct{}
	entered  sync.Once
}

var _ featureclassification.Store = (*coalescingRaceStore)(nil)

func newCoalescingRaceStore(t *testing.T) *coalescingRaceStore {
	t.Helper()
	inner, err := sessionclassification.NewMemoryStore(sessionclassification.MemoryStoreConfig{MaxEntries: 32, IdleTTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	return &coalescingRaceStore{
		MemoryStore: inner,
		promoted:    make(chan struct{}),
		release:     make(chan struct{}),
	}
}

func (s *coalescingRaceStore) Load(ctx context.Context, key featureclassification.Key) (featureclassification.Record, bool, error) {
	s.loads.Add(1)
	return s.MemoryStore.Load(ctx, key)
}

func (s *coalescingRaceStore) Promote(
	ctx context.Context,
	key featureclassification.Key,
	proposal session.Classification,
	now time.Time,
) (featureclassification.Record, bool, error) {
	s.entered.Do(func() { close(s.promoted) })
	select {
	case <-s.release:
	case <-ctx.Done():
		return featureclassification.Record{}, false, ctx.Err()
	case <-time.After(30 * time.Second):
		// The suite must never hang on a missed handshake.
		return featureclassification.Record{}, false, errors.New("blocked promotion was never released")
	}
	return s.MemoryStore.Promote(ctx, key, proposal, now)
}

func waitForStoreLoads(t *testing.T, store *coalescingRaceStore, want int32) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if store.loads.Load() >= want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("durable loads = %d, want at least %d", store.loads.Load(), want)
}

func newObservedCollector(t *testing.T) (*sessionclassification.PrometheusCollector, *prometheus.Registry) {
	t.Helper()
	registry := prometheus.NewRegistry()
	collector := sessionclassification.NewPrometheusCollector()
	if err := registry.Register(collector); err != nil {
		t.Fatalf("Register: %v", err)
	}
	return collector, registry
}

// TestPrometheusCollectorExportsNoSeriesBeforeAnyObservation is requirement 9.6
// in its strongest form: an untouched collector emits nothing but the static
// feature/inventory state, so a deployment that never classifies has no
// classification-specific hot-path series at all.
func TestPrometheusCollectorExportsNoSeriesBeforeAnyObservation(t *testing.T) {
	t.Parallel()

	_, registry := newObservedCollector(t)
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	if len(families) != 1 || families[0].GetName() != "aip_session_classification_store_ready" {
		names := metricFamilyNames(families)
		t.Fatalf("untouched collector families = %v, want only the static store-readiness gauge", names)
	}
}

// TestPrometheusCollectorDescribesTheBoundedRemoteVocabulary proves the remote
// egress surface exists as a bounded enum vocabulary with a latency histogram
// while staying completely silent until a remote attempt is actually observed.
func TestPrometheusCollectorDescribesTheBoundedRemoteVocabulary(t *testing.T) {
	t.Parallel()

	collector, registry := newObservedCollector(t)
	descs := make(chan *prometheus.Desc, 16)
	collector.Describe(descs)
	close(descs)
	var remoteTotal, remoteSeconds bool
	for desc := range descs {
		text := desc.String()
		if strings.Contains(text, `fqName: "aip_session_classification_remote_total"`) {
			remoteTotal = true
		}
		if strings.Contains(text, `fqName: "aip_session_classification_remote_seconds"`) {
			remoteSeconds = true
		}
	}
	if !remoteTotal || !remoteSeconds {
		t.Fatalf("described remote metrics = (total=%t, seconds=%t), want both bounded descriptors", remoteTotal, remoteSeconds)
	}

	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	assertNoFamily(t, families, "aip_session_classification_remote_total")
	assertNoFamily(t, families, "aip_session_classification_remote_seconds")
}

// TestPrometheusCollectorRecordsBoundedRemoteOutcomesAndLatency proves
// requirement 9.3: the remote counter and latency histogram carry only the
// bounded outcome label, never a request or response body.
func TestPrometheusCollectorRecordsBoundedRemoteOutcomesAndLatency(t *testing.T) {
	t.Parallel()

	collector, registry := newObservedCollector(t)
	observations := []featureclassification.RemoteObservation{
		{Outcome: featureclassification.RemoteTimeout, Latency: 750 * time.Millisecond},
		{Outcome: featureclassification.RemoteTimeout, Latency: 1500 * time.Millisecond},
		{Outcome: featureclassification.RemoteBelowThreshold, Latency: 120 * time.Millisecond},
		{Outcome: featureclassification.RemoteMalformed, Latency: 40 * time.Millisecond},
	}
	for _, observation := range observations {
		collector.ObserveRemote(observation)
	}
	if got := collector.DroppedObservations(); got != 0 {
		t.Fatalf("dropped observations = %d, want zero for bounded remote outcomes", got)
	}

	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	counter := findFamily(t, families, "aip_session_classification_remote_total")
	assertOnlyLabel(t, counter, "outcome")
	byOutcome := map[string]float64{}
	for _, metric := range counter.GetMetric() {
		byOutcome[metric.GetLabel()[0].GetValue()] = metric.GetCounter().GetValue()
	}
	wantCounters := map[string]float64{"timeout": 2, "below_threshold": 1, "malformed": 1}
	if len(byOutcome) != len(wantCounters) {
		t.Fatalf("remote outcomes = %v, want exactly %v", byOutcome, wantCounters)
	}
	for outcome, want := range wantCounters {
		if byOutcome[outcome] != want {
			t.Fatalf("remote_total{outcome=%q} = %v, want %v", outcome, byOutcome[outcome], want)
		}
	}

	histogram := findFamily(t, families, "aip_session_classification_remote_seconds")
	assertOnlyLabel(t, histogram, "outcome")
	total := float64(0)
	for _, metric := range histogram.GetMetric() {
		series := metric.GetHistogram()
		total += float64(series.GetSampleCount())
		if len(series.GetBucket()) != wantRemoteLatencyBuckets {
			t.Fatalf("remote_seconds{outcome=%q} bucket count = %d, want the fixed bounded bucket set",
				metric.GetLabel()[0].GetValue(), len(series.GetBucket()))
		}
		var previous float64
		for i, bucket := range series.GetBucket() {
			if i > 0 && bucket.GetUpperBound() <= previous {
				t.Fatalf("remote_seconds{outcome=%q} bucket bounds are not strictly increasing: %v",
					metric.GetLabel()[0].GetValue(), series.GetBucket())
			}
			previous = bucket.GetUpperBound()
		}
		last := series.GetBucket()[len(series.GetBucket())-1]
		if float64(last.GetCumulativeCount()) != float64(series.GetSampleCount()) {
			t.Fatalf("remote_seconds{outcome=%q} +Inf bucket = %v, want the full sample count %d",
				metric.GetLabel()[0].GetValue(), last.GetCumulativeCount(), series.GetSampleCount())
		}
	}
	if total != float64(len(observations)) {
		t.Fatalf("remote_seconds sample count = %v, want %d", total, len(observations))
	}
}

// TestPrometheusCollectorDropsEveryHostileObservation is requirement 9.4's
// executable proof: identities, raw User-Agents, paths, prompts, and arbitrary
// vendor result strings are rejected instead of exported as labels.
func TestPrometheusCollectorDropsEveryHostileObservation(t *testing.T) {
	t.Parallel()

	collector, registry := newObservedCollector(t)
	hostile := []string{hostileSessionID, hostileALegID, hostileUserAgent, hostilePath, hostilePrompt, hostileMarker}
	const observationKinds = 4
	offered := 0
	for _, value := range hostile {
		for _, mode := range []featureclassification.Mode{featureclassification.Mode(value), featureclassification.ModeHeuristic} {
			offered += observationKinds
			collector.ObserveEvaluation(featureclassification.EvaluationObservation{
				Mode:    mode,
				Outcome: featureclassification.EvaluationOutcome(value),
			})
			collector.ObserveTransition(featureclassification.TransitionObservation{
				Source:     session.ClassificationSource(value),
				Confidence: session.ConfidenceBand(value),
				Evidence:   session.EvidenceCode(value),
				Revision:   1,
			})
			collector.ObserveRemote(featureclassification.RemoteObservation{
				Outcome: featureclassification.RemoteOutcome(value),
				Latency: time.Duration(len(value)),
			})
			collector.ObserveStore(featureclassification.StoreObservation{
				Operation: featureclassification.StoreOperation(value),
				Outcome:   featureclassification.StoreOutcome(value),
			})
		}
	}
	if got, want := collector.DroppedObservations(), uint64(offered); got != want {
		t.Fatalf("dropped observations = %d, want %d", got, want)
	}

	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	names := metricFamilyNames(families)
	for _, name := range names {
		switch name {
		case "aip_session_classification_store_ready",
			"aip_session_classification_evaluations_total",
			"aip_session_classification_transitions_total",
			"aip_session_classification_remote_total",
			"aip_session_classification_remote_seconds",
			"aip_session_classification_store_total":
		default:
			t.Fatalf("collector exported unexpected family %q", name)
		}
	}
	assertNoHostileLabelText(t, families, hostile)
}

// TestPrometheusCollectorKeepsLabelCardinalityInsideTheClosedProduct proves the
// metrics surface can only ever produce the finite product of the closed
// vocabularies, no matter how many distinct sessions are classified.
func TestPrometheusCollectorKeepsLabelCardinalityInsideTheClosedProduct(t *testing.T) {
	t.Parallel()

	collector, registry := newObservedCollector(t)
	const sessions = 512
	for i := range sessions {
		sessionID := "sess-cardinality-" + strings.Repeat("z", i%17) + "-" + time.Duration(i).String()
		classifier, err := featureclassification.NewClassifier(
			featureclassification.Config{Mode: featureclassification.ModeHeuristic},
			featureclassification.ClassifierDeps{
				State:    stubStateAuthority{},
				Observer: collector,
				Now:      time.Now,
			},
		)
		if err != nil {
			t.Fatalf("NewClassifier: %v", err)
		}
		if _, err := classifier.Classify(context.Background(), sdkclassification.Input{
			Session:  session.SessionView{AuthoritativeSessionID: sessionID},
			Evidence: sdkclassification.Evidence{ClientUserAgent: hostileUserAgent},
		}); err == nil {
			t.Fatalf("Classify against an uninitialized process authority unexpectedly succeeded")
		}
	}

	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	evaluations := findFamily(t, families, "aip_session_classification_evaluations_total")
	assertOnlyLabel(t, evaluations, "mode", "outcome")
	if len(evaluations.GetMetric()) != 1 {
		t.Fatalf("evaluation series = %d, want a single bounded (mode,outcome) series for %d distinct sessions",
			len(evaluations.GetMetric()), sessions)
	}
	labels := labelMap(evaluations.GetMetric()[0])
	if labels["mode"] != "heuristic" || labels["outcome"] != "state_unavailable" {
		t.Fatalf("evaluation labels = %v, want bounded heuristic/state_unavailable", labels)
	}

	// No turn reached a positive, so no transition series exists at all.
	assertNoFamily(t, families, "aip_session_classification_transitions_total")
	assertNoFamily(t, families, "aip_session_classification_store_total")
	assertNoFamily(t, families, "aip_session_classification_remote_total")
	assertNoFamily(t, families, "aip_session_classification_remote_seconds")
	assertNoHostileLabelText(t, families, []string{hostileSessionID, hostileALegID, hostileUserAgent, hostilePath, hostilePrompt})
}

// TestPrometheusCollectorGathersBoundedEvaluationTransitionAndStoreSeries walks
// the whole enabled path: a real classifier over the process coordinator records
// evaluation, transition, and store series with closed labels only.
func TestPrometheusCollectorGathersBoundedEvaluationTransitionAndStoreSeries(t *testing.T) {
	t.Parallel()

	collector, registry := newObservedCollector(t)
	ctx := context.Background()
	holder := sessionclassification.NewStateHolder(nil, collector)
	if err := holder.Acquire(ctx); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	t.Cleanup(func() { _ = holder.Close() })
	classifier, err := featureclassification.NewClassifier(
		featureclassification.Config{Mode: featureclassification.ModeHeuristic},
		featureclassification.ClassifierDeps{State: holder, Observer: holder.Observer(), Now: time.Now},
	)
	if err != nil {
		t.Fatalf("NewClassifier: %v", err)
	}
	in := sdkclassification.Input{
		Session:  session.SessionView{AuthoritativeSessionID: hostileSessionID},
		Evidence: sdkclassification.Evidence{ClientUserAgent: "codex_cli_rs/1.2.3"},
	}
	promoted, err := classifier.Classify(ctx, in)
	if err != nil {
		t.Fatalf("first Classify: %v", err)
	}
	if !promoted.IsCodingAgent() {
		t.Fatalf("first classification = %+v, want the accepted positive", promoted)
	}
	if _, err := classifier.Classify(ctx, in); err != nil {
		t.Fatalf("replayed Classify: %v", err)
	}

	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	evaluations := findFamily(t, families, "aip_session_classification_evaluations_total")
	assertOnlyLabel(t, evaluations, "mode", "outcome")
	evaluationOutcomes := counterValues(t, evaluations)
	if evaluationOutcomes["heuristic|promoted"] != 1 || evaluationOutcomes["heuristic|restored"] != 1 {
		t.Fatalf("evaluation outcomes = %v, want one promoted and one restored", evaluationOutcomes)
	}

	transitions := findFamily(t, families, "aip_session_classification_transitions_total")
	assertOnlyLabel(t, transitions, "source", "confidence", "evidence")
	if len(transitions.GetMetric()) != 1 {
		t.Fatalf("transition series = %d, want exactly one first-positive transition", len(transitions.GetMetric()))
	}
	labels := labelMap(transitions.GetMetric()[0])
	if labels["source"] != "local_identity" || labels["confidence"] != "high" || labels["evidence"] != "client_family.codex" {
		t.Fatalf("transition labels = %v, want the bounded positive snapshot labels", labels)
	}
	if value := transitions.GetMetric()[0].GetCounter().GetValue(); value != 1 {
		t.Fatalf("transition counter = %v, want 1", value)
	}

	storeFamily := findFamily(t, families, "aip_session_classification_store_total")
	assertOnlyLabel(t, storeFamily, "operation", "outcome")
	storeOutcomes := counterValues(t, storeFamily)
	wantStoreOutcomes := map[string]float64{
		// The first turn found nothing and promoted; the replayed warm turn was
		// served from the positive cache without durable work.
		"load|miss": 1, "promote|applied": 1, "load|hit": 1,
	}
	for series, want := range wantStoreOutcomes {
		if storeOutcomes[series] != want {
			t.Fatalf("store_total{%q} = %v, want %v (all series %v)", series, storeOutcomes[series], want, storeOutcomes)
		}
	}
	if len(storeOutcomes) != len(wantStoreOutcomes) {
		t.Fatalf("store outcomes = %v, want exactly %v", storeOutcomes, wantStoreOutcomes)
	}

	assertNoHostileLabelText(t, families, []string{hostileSessionID, "codex_cli_rs"})
}

// TestPrometheusCollectorEmitsOneTransitionForConcurrentFirstPositive is the
// requirement 9.1/2.5 race guard: two turns that race to classify one logical
// session must converge on exactly ONE transition observation.
//
// The real coordinator hands the coalesced-flight waiter and the cached-positive
// short-circuit the winner's positive record with promoted=false, so transition
// emission has to depend on first-positive ownership (the promoted bool) rather
// than on the returned record's positivity. This test also pins the requirement
// 1.4 immutability contract that both racing turns still project the positive
// classification to their caller.
func TestPrometheusCollectorEmitsOneTransitionForConcurrentFirstPositive(t *testing.T) {
	t.Parallel()

	collector, registry := newObservedCollector(t)
	ctx := context.Background()
	store := newCoalescingRaceStore(t)
	coordinator, err := sessionclassification.NewCoordinator(store, sessionclassification.CoordinatorConfig{Observer: collector})
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}
	classifier, err := featureclassification.NewClassifier(
		featureclassification.Config{Mode: featureclassification.ModeHeuristic},
		featureclassification.ClassifierDeps{
			State:    coordinatorAuthority{store: coordinator},
			Observer: collector,
			Now:      time.Now,
		},
	)
	if err != nil {
		t.Fatalf("NewClassifier: %v", err)
	}

	in := sdkclassification.Input{
		Session:  session.SessionView{AuthoritativeSessionID: hostileSessionID},
		Evidence: sdkclassification.Evidence{ClientUserAgent: "codex_cli_rs/1.2.3"},
	}
	results := make([]session.Classification, 2)
	failures := make([]error, 2)
	var wg sync.WaitGroup
	wg.Go(func() {
		results[0], failures[0] = classifier.Classify(ctx, in)
	})

	// The first turn is now durably blocked inside Promote, so no positive
	// exists yet.
	<-store.promoted
	wg.Go(func() {
		results[1], failures[1] = classifier.Classify(ctx, in)
	})
	// Wait until the racing turn has also read the durable state. That proves it
	// observed the still-unknown session instead of an established positive.
	waitForStoreLoads(t, store, 2)
	close(store.release)
	wg.Wait()

	for i, failure := range failures {
		if failure != nil {
			t.Fatalf("racing turn %d Classify error = %v", i, failure)
		}
	}
	// Requirement 1.4/2.5: both racing turns still project the positive, and
	// they agree on one coherent revision.
	want := session.Classification{
		Kind:       session.KindCodingAgent,
		Source:     session.SourceLocalIdentity,
		Confidence: session.ConfidenceHigh,
		Evidence:   "client_family.codex",
		Revision:   1,
	}
	for i, got := range results {
		if got != want {
			t.Fatalf("racing turn %d classification = %+v, want %+v", i, got, want)
		}
	}

	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	transitions := findFamily(t, families, "aip_session_classification_transitions_total")
	assertOnlyLabel(t, transitions, "source", "confidence", "evidence")
	total := float64(0)
	for _, metric := range transitions.GetMetric() {
		total += metric.GetCounter().GetValue()
	}
	if total != 1 {
		t.Fatalf("transitions_total for one raced logical session = %v, want exactly 1 (series %v)",
			total, counterValues(t, transitions))
	}
	// Prometheus orders label pairs alphabetically (confidence, evidence, source).
	if got := counterValues(t, transitions)["high|client_family.codex|local_identity"]; got != 1 {
		t.Fatalf("transitions_total{source=local_identity,evidence=client_family.codex,confidence=high} = %v, want 1", got)
	}

	evaluationOutcomes := counterValues(t, findFamily(t, families, "aip_session_classification_evaluations_total"))
	if promoted := evaluationOutcomes["heuristic|promoted"]; promoted > 1 {
		t.Fatalf("evaluations_total{outcome=promoted} = %v, want at most 1 (all %v)", promoted, evaluationOutcomes)
	}
	if promoted := evaluationOutcomes["heuristic|promoted"]; promoted != 1 {
		t.Fatalf("evaluations_total{outcome=promoted} = %v, want exactly 1 owning the transition", promoted)
	}
	if restored := evaluationOutcomes["heuristic|restored"]; restored != 1 {
		t.Fatalf("evaluations_total{outcome=restored} = %v, want the losing turn reported as restored", restored)
	}
	if storeOutcomes := counterValues(t, findFamily(t, families, "aip_session_classification_store_total")); storeOutcomes["promote|applied"] != 1 {
		t.Fatalf("store_total{promote,applied} = %v, want exactly one durable first-accepted promotion", storeOutcomes["promote|applied"])
	}
	assertNoHostileLabelText(t, families, []string{hostileSessionID, "codex_cli_rs"})
}

// TestPrometheusCollectorExercisesTheBoundedRemoteStoreVocabulary proves the
// store metric also covers the remote lease operations with closed labels, so the
// vocabulary is real before a remote decider is wired to it.
func TestPrometheusCollectorExercisesTheBoundedRemoteStoreVocabulary(t *testing.T) {
	t.Parallel()

	collector, registry := newObservedCollector(t)
	ctx := context.Background()
	holder := sessionclassification.NewStateHolder(nil, collector)
	if err := holder.Acquire(ctx); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	t.Cleanup(func() { _ = holder.Close() })
	coordinator := holder.Coordinator()
	if coordinator == nil {
		t.Fatal("enabled holder published no coordinator")
	}

	key := featureclassification.Key{Kind: featureclassification.ScopeSecureSession, ID: hostileSessionID}
	start := time.Now().UTC()
	claim, _, ok, err := coordinator.ClaimRemote(ctx, key, start, 2, time.Minute, 0)
	if err != nil || !ok {
		t.Fatalf("ClaimRemote = (ok=%t, err=%v), want one bounded grant", ok, err)
	}
	if _, _, ok, err := coordinator.ClaimRemote(ctx, key, start, 2, time.Minute, 0); err != nil || ok {
		t.Fatalf("second ClaimRemote = (ok=%t, err=%v), want bounded denial", ok, err)
	}
	if _, err := coordinator.CompleteRemote(ctx, claim, featureclassification.RemoteCompletion{}, start.Add(time.Second)); err != nil {
		t.Fatalf("CompleteRemote: %v", err)
	}

	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	storeOutcomes := counterValues(t, findFamily(t, families, "aip_session_classification_store_total"))
	wantStoreOutcomes := map[string]float64{
		"remote_claim|applied":      1,
		"remote_claim|denied":       1,
		"remote_complete|unchanged": 1,
	}
	for series, want := range wantStoreOutcomes {
		if storeOutcomes[series] != want {
			t.Fatalf("store_total{%q} = %v, want %v (all series %v)", series, storeOutcomes[series], want, storeOutcomes)
		}
	}
	// A durable remote operation still produces no remote egress series: the
	// bounded remote outcome and latency are owned by the remote decider.
	assertNoFamily(t, families, "aip_session_classification_remote_total")
	assertNoFamily(t, families, "aip_session_classification_remote_seconds")
	assertNoHostileLabelText(t, families, []string{hostileSessionID, claim.LeaseID})
}

// TestPrometheusCollectorIsSafeUnderConcurrentObservation keeps the collector
// usable from concurrent request turns and proves no observation is lost: every
// concurrent write is counted exactly once under the closed vocabulary.
func TestPrometheusCollectorIsSafeUnderConcurrentObservation(t *testing.T) {
	t.Parallel()

	collector, registry := newObservedCollector(t)
	const workers = 16
	const perWorker = 64
	done := make(chan struct{})
	for range workers {
		go func() {
			defer func() { done <- struct{}{} }()
			for i := range perWorker {
				collector.ObserveEvaluation(featureclassification.EvaluationObservation{
					Mode:    featureclassification.ModeHeuristic,
					Outcome: featureclassification.EvaluationUnknown,
				})
				collector.ObserveStore(featureclassification.StoreObservation{
					Operation: featureclassification.StoreOperationLoad,
					Outcome:   featureclassification.StoreOutcomeMiss,
				})
				collector.ObserveRemote(featureclassification.RemoteObservation{
					Outcome: featureclassification.RemoteBelowThreshold,
					Latency: time.Duration(i) * time.Millisecond,
				})
			}
		}()
	}
	for range workers {
		<-done
	}
	if got := collector.DroppedObservations(); got != 0 {
		t.Fatalf("dropped observations = %d, want 0 for bounded concurrent observations", got)
	}
	families, err := registry.Gather()
	if err != nil {
		t.Fatalf("Gather after concurrent observation: %v", err)
	}
	const total = float64(workers * perWorker)
	evaluations := findFamily(t, families, "aip_session_classification_evaluations_total")
	assertOnlyLabel(t, evaluations, "mode", "outcome")
	if got := counterValues(t, evaluations)["heuristic|unknown"]; got != total {
		t.Fatalf("evaluations_total{mode=heuristic,outcome=unknown} = %v, want %v", got, total)
	}
	storeFamily := findFamily(t, families, "aip_session_classification_store_total")
	assertOnlyLabel(t, storeFamily, "operation", "outcome")
	if got := counterValues(t, storeFamily)["load|miss"]; got != total {
		t.Fatalf("store_total{operation=load,outcome=miss} = %v, want %v", got, total)
	}
	remoteCounter := findFamily(t, families, "aip_session_classification_remote_total")
	assertOnlyLabel(t, remoteCounter, "outcome")
	if got := counterValues(t, remoteCounter)["below_threshold"]; got != total {
		t.Fatalf("remote_total{outcome=below_threshold} = %v, want %v", got, total)
	}
	histogram := findFamily(t, families, "aip_session_classification_remote_seconds")
	assertOnlyLabel(t, histogram, "outcome")
	var samples uint64
	for _, metric := range histogram.GetMetric() {
		series := metric.GetHistogram()
		samples += series.GetSampleCount()
		if len(series.GetBucket()) != wantRemoteLatencyBuckets {
			t.Fatalf("remote_seconds{outcome=%q} bucket count = %d, want the fixed bounded bucket set",
				metric.GetLabel()[0].GetValue(), len(series.GetBucket()))
		}
	}
	if float64(samples) != total {
		t.Fatalf("remote_seconds sample count = %d, want %v", samples, total)
	}
}

func metricFamilyNames(families []*dto.MetricFamily) []string {
	names := make([]string, 0, len(families))
	for _, family := range families {
		names = append(names, family.GetName())
	}
	return names
}

func findFamily(t *testing.T, families []*dto.MetricFamily, name string) *dto.MetricFamily {
	t.Helper()
	for _, family := range families {
		if family.GetName() == name {
			return family
		}
	}
	t.Fatalf("metric family %q not gathered (have %v)", name, metricFamilyNames(families))
	return nil
}

func assertNoFamily(t *testing.T, families []*dto.MetricFamily, name string) {
	t.Helper()
	for _, family := range families {
		if family.GetName() == name {
			t.Fatalf("metric family %q was gathered while the feature emitted no observation", name)
		}
	}
}

// assertOnlyLabel proves a family's series carry exactly the expected bounded
// label names. Prometheus sorts label pairs by name, so the comparison is a set.
func assertOnlyLabel(t *testing.T, family *dto.MetricFamily, want ...string) {
	t.Helper()
	for _, metric := range family.GetMetric() {
		got := metric.GetLabel()
		if len(got) != len(want) {
			t.Fatalf("%s series carries labels %v, want exactly %v", family.GetName(), labelMap(metric), want)
		}
		gotNames := make([]string, 0, len(got))
		for _, pair := range got {
			gotNames = append(gotNames, pair.GetName())
		}
		wantNames := append([]string(nil), want...)
		sort.Strings(gotNames)
		sort.Strings(wantNames)
		for i := range wantNames {
			if gotNames[i] != wantNames[i] {
				t.Fatalf("%s labels = %v, want exactly %v", family.GetName(), gotNames, wantNames)
			}
		}
	}
}

func labelMap(metric *dto.Metric) map[string]string {
	labels := make(map[string]string, len(metric.GetLabel()))
	for _, pair := range metric.GetLabel() {
		labels[pair.GetName()] = pair.GetValue()
	}
	return labels
}

func counterValues(t *testing.T, family *dto.MetricFamily) map[string]float64 {
	t.Helper()
	values := make(map[string]float64, len(family.GetMetric()))
	for _, metric := range family.GetMetric() {
		key := strings.Join(labelValues(metric), "|")
		values[key] = metric.GetCounter().GetValue()
	}
	return values
}

func labelValues(metric *dto.Metric) []string {
	values := make([]string, 0, len(metric.GetLabel()))
	for _, pair := range metric.GetLabel() {
		values = append(values, pair.GetValue())
	}
	return values
}

func assertNoHostileLabelText(t *testing.T, families []*dto.MetricFamily, secrets []string) {
	t.Helper()
	for _, family := range families {
		for _, metric := range family.GetMetric() {
			for _, pair := range metric.GetLabel() {
				for _, secret := range secrets {
					if secret != "" && strings.Contains(pair.GetValue(), secret) {
						t.Fatalf("metric %s label %s=%q contains hostile value %q", family.GetName(), pair.GetName(), pair.GetValue(), secret)
					}
				}
			}
		}
	}
}
