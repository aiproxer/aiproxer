package metering

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	usageevidence "github.com/aiproxer/aiproxer/internal/core/usageevidence"
	sdkmetering "github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

type lateTestLegReader struct {
	mu       sync.Mutex
	key      string
	record   usageevidence.CallLegUsageRecord
	resolver *lateTestObservationResolver
	calls    atomic.Int32
	missing  bool
}

func (r *lateTestLegReader) GetCallLegUsage(_ context.Context, key string) (usageevidence.CallLegUsageRecord, error) {
	if r == nil {
		return usageevidence.CallLegUsageRecord{}, fmt.Errorf("late test leg %q not found", key)
	}
	r.calls.Add(1)
	if r.missing || key != r.key {
		return usageevidence.CallLegUsageRecord{}, fmt.Errorf("late test leg %q not found", key)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.record, nil
}

type lateTestObservationSink struct {
	mu              sync.Mutex
	observed        map[string]sdkmetering.Observation
	appends         atomic.Int32
	economicAppends atomic.Int32
	ambiguousOnce   atomic.Bool
}

func (s *lateTestObservationSink) Append(ctx context.Context, observation sdkmetering.Observation) error {
	return s.AppendObservations(ctx, []sdkmetering.Observation{observation})
}

func (s *lateTestObservationSink) AppendObservations(_ context.Context, observations []sdkmetering.Observation) error {
	s.appends.Add(1)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.observed == nil {
		s.observed = make(map[string]sdkmetering.Observation)
	}
	for _, observation := range observations {
		canonical, err := observation.Canonical()
		if err != nil {
			return err
		}
		key := canonical.IdentityKey()
		if prior, ok := s.observed[key]; ok {
			priorHash, _ := prior.ReplayFingerprint()
			incomingHash, _ := canonical.ReplayFingerprint()
			if priorHash != incomingHash {
				return fmt.Errorf("%w: %s", sdkmetering.ErrInvalidObservation, key)
			}
			continue
		}
		s.observed[key] = canonical
	}
	return nil
}

func (s *lateTestObservationSink) AppendEconomicObservationWithOutbox(ctx context.Context, observation sdkmetering.Observation) error {
	s.economicAppends.Add(1)
	err := s.AppendObservations(ctx, []sdkmetering.Observation{observation})
	if s.ambiguousOnce.Swap(false) {
		return errors.New("late test: ambiguous economic commit")
	}
	return err
}

type lateTestPlainObservationSink struct {
	appends atomic.Int32
}

func (s *lateTestPlainObservationSink) Append(_ context.Context, _ sdkmetering.Observation) error {
	s.appends.Add(1)
	return nil
}

type lateTestAtomicObservationOnlySink struct {
	appends atomic.Int32
}

func (s *lateTestAtomicObservationOnlySink) Append(ctx context.Context, observation sdkmetering.Observation) error {
	s.appends.Add(1)
	return nil
}

func (s *lateTestAtomicObservationOnlySink) AppendObservations(_ context.Context, _ []sdkmetering.Observation) error {
	s.appends.Add(1)
	return nil
}

type lateTestObservationResolver struct {
	mu    sync.Mutex
	items map[string]sdkmetering.Observation
}

func (r *lateTestObservationResolver) GetObservation(_ context.Context, id string, revision uint64) (sdkmetering.Observation, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if observation, ok := r.items[id+fmt.Sprintf("\x00%d", revision)]; ok {
		return observation, nil
	}
	return sdkmetering.Observation{}, fmt.Errorf("late test observation %s/%d not found", id, revision)
}

func TestLateEconomicAppenderAcceptsProviderFinalizerAfterClosedLeg(t *testing.T) {
	t.Parallel()
	base := lateTestProviderObservation("finalizer", 1)
	appender, sink, _ := newLateTestAppender(t, true, base)
	if err := appender.AppendLateEconomicEvidence(context.Background(), lateEvidence(base, LateEconomicProviderFinalizer)); err != nil {
		t.Fatalf("append provider finalizer: %v", err)
	}
	if got := lateSinkLen(sink); got != 1 {
		t.Fatalf("durable economic observations = %d, want 1", got)
	}
	if got := sink.economicAppends.Load(); got != 1 {
		t.Fatalf("economic outbox appends = %d, want 1", got)
	}
}

func TestLateEconomicAppenderAcceptsStatementLineWithClosedBLegCorrelation(t *testing.T) {
	t.Parallel()
	statement := lateTestStatementObservation("statement-line", 1)
	appender, sink, _ := newLateTestAppender(t, true, statement)
	if err := appender.AppendLateEconomicEvidence(context.Background(), lateEvidence(statement, LateEconomicStatement)); err != nil {
		t.Fatalf("append statement line: %v", err)
	}
	if got := lateSinkLen(sink); got != 1 {
		t.Fatalf("durable statement observations = %d, want 1", got)
	}
}

func TestLateEconomicAppenderAcceptsResolvedProviderCorrection(t *testing.T) {
	t.Parallel()
	prior := lateTestProviderObservation("prior", 1)
	correction := lateTestProviderObservation("correction", 2)
	correction.SourceEventKey = prior.SourceEventKey
	correction.Subject = prior.Subject
	correction.Correlation = prior.Correlation
	correction.Semantics = sdkmetering.SemanticsCorrection
	ref, err := prior.Ref("metering-1")
	if err != nil {
		t.Fatalf("prior ref: %v", err)
	}
	correction.Supersedes = []sdkmetering.ObservationRef{ref}
	appender, sink, reader := newLateTestAppender(t, true, correction)
	reader.resolver.items[prior.ID+fmt.Sprintf("\x00%d", prior.Revision)] = prior
	if err := appender.AppendLateEconomicEvidence(context.Background(), lateEvidence(correction, LateEconomicCorrection)); err != nil {
		t.Fatalf("append provider correction: %v", err)
	}
	if got := lateSinkLen(sink); got != 1 {
		t.Fatalf("durable correction observations = %d, want 1", got)
	}
}

func TestLateEconomicAppenderExactReplayIsIdempotent(t *testing.T) {
	t.Parallel()
	observation := lateTestProviderObservation("replay", 1)
	appender, sink, _ := newLateTestAppender(t, true, observation)
	sink.ambiguousOnce.Store(true)
	evidence := lateEvidence(observation, LateEconomicProviderFinalizer)
	if err := appender.AppendLateEconomicEvidence(context.Background(), evidence); err == nil {
		t.Fatal("ambiguous first append must report unknown commit status")
	}
	if err := appender.AppendLateEconomicEvidence(context.Background(), evidence); err != nil {
		t.Fatalf("replay append: %v", err)
	}
	if got := lateSinkLen(sink); got != 1 {
		t.Fatalf("replayed observations = %d, want 1", got)
	}
	if got := sink.economicAppends.Load(); got != 2 {
		t.Fatalf("ambiguous replay economic appends = %d, want 2 attempts", got)
	}
}

func TestLateEconomicAppenderRejectsBillingCallMismatch(t *testing.T) {
	t.Parallel()
	observation := lateTestProviderObservation("call-mismatch", 1)
	appender, _, _ := newLateTestAppender(t, true, observation)
	evidence := lateEvidence(observation, LateEconomicProviderFinalizer)
	other, err := usageevidence.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	evidence.Identity.BillingCallID = other
	if err := appender.AppendLateEconomicEvidence(context.Background(), evidence); !errors.Is(err, ErrLateEconomicLineageMismatch) {
		t.Fatalf("call mismatch error = %v, want lineage mismatch", err)
	}
}

func TestLateEconomicAppenderRejectsBLegMismatch(t *testing.T) {
	t.Parallel()
	observation := lateTestProviderObservation("b-leg-mismatch", 1)
	appender, _, _ := newLateTestAppender(t, true, observation)
	evidence := lateEvidence(observation, LateEconomicProviderFinalizer)
	evidence.Identity.BLegID = "different-b-leg"
	if err := appender.AppendLateEconomicEvidence(context.Background(), evidence); !errors.Is(err, ErrLateEconomicLineageMismatch) {
		t.Fatalf("B-leg mismatch error = %v, want lineage mismatch", err)
	}
}

func TestLateEconomicAppenderRejectsAttemptSequenceMismatch(t *testing.T) {
	t.Parallel()
	observation := lateTestProviderObservation("attempt-mismatch", 1)
	appender, _, _ := newLateTestAppender(t, true, observation)
	evidence := lateEvidence(observation, LateEconomicProviderFinalizer)
	evidence.Identity.AttemptSeq = 2
	if err := appender.AppendLateEconomicEvidence(context.Background(), evidence); !errors.Is(err, ErrLateEconomicLineageMismatch) {
		t.Fatalf("attempt sequence mismatch error = %v, want lineage mismatch", err)
	}
}

func TestLateEconomicAppenderRejectsWrongStoreID(t *testing.T) {
	t.Parallel()
	observation := lateTestProviderObservation("store-mismatch", 1)
	appender, _, _ := newLateTestAppender(t, true, observation)
	evidence := lateEvidence(observation, LateEconomicProviderFinalizer)
	evidence.Identity.StoreID = "other-store"
	if err := appender.AppendLateEconomicEvidence(context.Background(), evidence); !errors.Is(err, ErrLateEconomicStoreMismatch) {
		t.Fatalf("store mismatch error = %v, want store mismatch", err)
	}
}

func TestLateEconomicAppenderRejectsWrongProviderAuthority(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(*sdkmetering.Observation, *LateEconomicIdentity)
	}{
		{name: "provider ID", mutate: func(_ *sdkmetering.Observation, identity *LateEconomicIdentity) {
			identity.ProviderID = "other-provider"
		}},
		{name: "missing trusted authority", mutate: func(_ *sdkmetering.Observation, _ *LateEconomicIdentity) {}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			observation := lateTestProviderObservation("provider-mismatch-"+test.name, 1)
			appender, sink, reader := newLateTestAppender(t, true, observation)
			if test.name == "missing trusted authority" {
				withoutAuthority := reader.record
				withoutAuthority.Observations = nil
				withoutAuthority.EvidenceVersion = 0
				withoutAuthority.EvidenceProjection = ""
				sealed, err := withoutAuthority.Seal()
				if err != nil {
					t.Fatal(err)
				}
				reader.record = sealed
			}
			evidence := lateEvidence(observation, LateEconomicProviderFinalizer)
			test.mutate(&evidence.Observation, &evidence.Identity)
			if err := appender.AppendLateEconomicEvidence(context.Background(), evidence); !errors.Is(err, ErrLateEconomicAuthorityMismatch) {
				t.Fatalf("provider authority mismatch error = %v, want authority mismatch", err)
			}
			if got := lateSinkLen(sink); got != 0 {
				t.Fatalf("authority-rejected observations = %d, want 0", got)
			}
		})
	}
}

func TestLateEconomicAppenderRejectsWrongProviderAccount(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(*sdkmetering.Observation, *LateEconomicIdentity)
	}{
		{name: "account", mutate: func(observation *sdkmetering.Observation, identity *LateEconomicIdentity) {
			observation.Subject.ProviderAccountKey = "spoof-account"
			observation.Correlation.ProviderAccountKey = "spoof-account"
			identity.ProviderAccountKey = "spoof-account"
		}},
		{name: "request", mutate: func(observation *sdkmetering.Observation, identity *LateEconomicIdentity) {
			observation.Subject.ProviderRequestID = "spoof-request"
			observation.Correlation.ProviderRequestID = "spoof-request"
			identity.ProviderRequestID = "spoof-request"
		}},
		{name: "charge", mutate: func(observation *sdkmetering.Observation, identity *LateEconomicIdentity) {
			observation.Subject.ProviderChargeID = "spoof-charge"
			observation.Correlation.ProviderChargeID = "spoof-charge"
			identity.ProviderChargeID = "spoof-charge"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			observation := lateTestProviderObservation("authority-spoof-"+test.name, 1)
			appender, sink, _ := newLateTestAppender(t, true, observation)
			evidence := lateEvidence(observation, LateEconomicProviderFinalizer)
			test.mutate(&evidence.Observation, &evidence.Identity)
			if err := appender.AppendLateEconomicEvidence(context.Background(), evidence); !errors.Is(err, ErrLateEconomicAuthorityMismatch) {
				t.Fatalf("provider %s spoof error = %v, want authority mismatch", test.name, err)
			}
			if got := lateSinkLen(sink); got != 0 {
				t.Fatalf("spoofed authority observations = %d, want 0", got)
			}
		})
	}
}

func TestLateEconomicAppenderRejectsProviderFinalizerWrongProvenance(t *testing.T) {
	t.Parallel()
	observation := lateTestStatementObservation("wrong-finalizer-provenance", 1)
	appender, _, _ := newLateTestAppender(t, true, observation)
	if err := appender.AppendLateEconomicEvidence(context.Background(), lateEvidence(observation, LateEconomicProviderFinalizer)); !errors.Is(err, ErrLateEconomicProvenanceMismatch) {
		t.Fatalf("wrong finalizer provenance error = %v, want provenance mismatch", err)
	}
}

func TestLateEconomicAppenderRejectsStatementWrongProvenance(t *testing.T) {
	t.Parallel()
	observation := lateTestProviderObservation("wrong-statement-provenance", 1)
	appender, _, _ := newLateTestAppender(t, true, observation)
	if err := appender.AppendLateEconomicEvidence(context.Background(), lateEvidence(observation, LateEconomicStatement)); !errors.Is(err, ErrLateEconomicProvenanceMismatch) {
		t.Fatalf("wrong statement provenance error = %v, want provenance mismatch", err)
	}
}

func TestLateEconomicAppenderRejectsCorrectionWithoutSupersedes(t *testing.T) {
	t.Parallel()
	observation := lateTestProviderObservation("missing-supersedes", 2)
	observation.Semantics = sdkmetering.SemanticsCorrection
	appender, _, _ := newLateTestAppender(t, true, observation)
	if err := appender.AppendLateEconomicEvidence(context.Background(), lateEvidence(observation, LateEconomicCorrection)); !errors.Is(err, sdkmetering.ErrInvalidObservation) {
		t.Fatalf("missing supersedes error = %v, want invalid observation", err)
	}
}

func TestLateEconomicAppenderRejectsUnknownSupersedesLineage(t *testing.T) {
	t.Parallel()
	prior := lateTestProviderObservation("unknown-prior", 1)
	correction := lateTestProviderObservation("unknown-correction", 2)
	correction.SourceEventKey = prior.SourceEventKey
	correction.Semantics = sdkmetering.SemanticsCorrection
	correction.Supersedes = []sdkmetering.ObservationRef{{StoreID: "metering-1", ObservationID: "missing", Revision: 1, PayloadHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}
	appender, _, _ := newLateTestAppender(t, true, correction)
	if err := appender.AppendLateEconomicEvidence(context.Background(), lateEvidence(correction, LateEconomicCorrection)); !errors.Is(err, ErrLateEconomicLineageMismatch) {
		t.Fatalf("unknown supersedes error = %v, want lineage mismatch", err)
	}
}

func TestLateEconomicAppenderRejectsSupersedesPayloadHashMismatch(t *testing.T) {
	t.Parallel()
	prior := lateTestProviderObservation("hash-prior", 1)
	correction := lateTestProviderObservation("hash-correction", 2)
	correction.SourceEventKey = prior.SourceEventKey
	correction.Semantics = sdkmetering.SemanticsCorrection
	correction.Supersedes = []sdkmetering.ObservationRef{{StoreID: "metering-1", ObservationID: prior.ID, Revision: prior.Revision, PayloadHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}
	appender, _, reader := newLateTestAppender(t, true, correction)
	reader.resolver.items[prior.ID+fmt.Sprintf("\x00%d", prior.Revision)] = prior
	if err := appender.AppendLateEconomicEvidence(context.Background(), lateEvidence(correction, LateEconomicCorrection)); !errors.Is(err, ErrLateEconomicLineageMismatch) {
		t.Fatalf("hash mismatch error = %v, want lineage mismatch", err)
	}
}

func TestLateEconomicAppenderRejectsCrossBLegCorrection(t *testing.T) {
	t.Parallel()
	prior := lateTestProviderObservation("cross-b-leg-prior", 1)
	prior.Subject.BLegID = "other-b-leg"
	prior.Correlation.BLegID = "other-b-leg"
	correction := lateTestProviderObservation("cross-b-leg-correction", 2)
	correction.SourceEventKey = prior.SourceEventKey
	correction.Subject = prior.Subject
	correction.Correlation = prior.Correlation
	correction.Subject.BLegID = "b-leg"
	correction.Correlation.BLegID = "b-leg"
	correction.Semantics = sdkmetering.SemanticsCorrection
	ref, err := prior.Ref("metering-1")
	if err != nil {
		t.Fatal(err)
	}
	correction.Supersedes = []sdkmetering.ObservationRef{ref}
	appender, _, reader := newLateTestAppender(t, true, correction)
	reader.resolver.items[prior.ID+fmt.Sprintf("\x00%d", prior.Revision)] = prior
	if err := appender.AppendLateEconomicEvidence(context.Background(), lateEvidence(correction, LateEconomicCorrection)); !errors.Is(err, ErrLateEconomicLineageMismatch) {
		t.Fatalf("cross B-leg correction error = %v, want lineage mismatch", err)
	}
}

func TestLateEconomicAppenderRejectsUnclosedLeg(t *testing.T) {
	t.Parallel()
	observation := lateTestProviderObservation("unclosed", 1)
	appender, _, reader := newLateTestAppender(t, true, observation)
	reader.record.FinishedAt = time.Time{}
	if err := appender.AppendLateEconomicEvidence(context.Background(), lateEvidence(observation, LateEconomicProviderFinalizer)); !errors.Is(err, ErrLateEconomicLegNotClosed) {
		t.Fatalf("unclosed leg error = %v, want closed-leg error", err)
	}
}

func TestLateEconomicAppenderRejectsMissingClosedBLegWithoutReplacement(t *testing.T) {
	t.Parallel()
	observation := lateTestProviderObservation("missing-closed-leg", 1)
	appender, sink, reader := newLateTestAppender(t, true, observation)
	reader.missing = true
	if err := appender.AppendLateEconomicEvidence(context.Background(), lateEvidence(observation, LateEconomicProviderFinalizer)); err == nil {
		t.Fatal("missing closed B-leg must fail closed")
	}
	if got := lateSinkLen(sink); got != 0 {
		t.Fatalf("late observations after missing closed leg = %d, want 0", got)
	}
}

func TestLateEconomicAppenderRejectsPlainOrObservationOnlySinkBeforeAppend(t *testing.T) {
	t.Parallel()
	plain := &lateTestPlainObservationSink{}
	atomicOnly := &lateTestAtomicObservationOnlySink{}
	var typedNil *lateTestObservationSink
	tests := []struct {
		name  string
		sink  sdkmetering.ObservationSink
		calls func() int32
	}{
		{name: "nil sink", sink: nil, calls: func() int32 { return 0 }},
		{name: "typed nil sink", sink: typedNil, calls: func() int32 { return 0 }},
		{name: "plain observation sink", sink: plain, calls: plain.appends.Load},
		{name: "atomic observation-only sink", sink: atomicOnly, calls: atomicOnly.appends.Load},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			observation := lateTestProviderObservation("sink-rejected-"+test.name, 1)
			reader := newLateTestLegReaderForObservation(t, observation, true)
			appender, err := NewLateEconomicAppender(LateEconomicAppenderConfig{StoreID: "metering-1", Legs: reader, Sink: test.sink})
			if err == nil || !errors.Is(err, ErrLateEconomicSinkCapability) || !errors.Is(err, ErrLateEconomicAppend) {
				t.Fatalf("sink constructor error = %v, want classified capability rejection", err)
			}
			if appender != nil {
				t.Fatal("rejected sink must not construct an appender")
			}
			if got := test.calls(); got != 0 {
				t.Fatalf("rejected sink append calls = %d, want 0", got)
			}
		})
	}
}

func TestLateEconomicAppenderConcurrentDuplicateIsOneLogicalObservation(t *testing.T) {
	t.Parallel()
	observation := lateTestProviderObservation("concurrent-replay", 1)
	appender, sink, _ := newLateTestAppender(t, true, observation)
	evidence := lateEvidence(observation, LateEconomicProviderFinalizer)
	const workers = 16
	var wg sync.WaitGroup
	var failures atomic.Int32
	for range workers {
		wg.Go(func() {
			if err := appender.AppendLateEconomicEvidence(context.Background(), evidence); err != nil {
				failures.Add(1)
			}
		})
	}
	wg.Wait()
	if got := failures.Load(); got != 0 {
		t.Fatalf("concurrent duplicate failures = %d, want 0", got)
	}
	if got := lateSinkLen(sink); got != 1 {
		t.Fatalf("concurrent logical observations = %d, want 1", got)
	}
}

func newLateTestAppender(t *testing.T, closed bool, observation sdkmetering.Observation) (*LateEconomicAppender, *lateTestObservationSink, *lateTestLegReader) {
	t.Helper()
	reader := newLateTestLegReaderForObservation(t, observation, closed)
	sink := &lateTestObservationSink{}
	resolver := &lateTestObservationResolver{items: make(map[string]sdkmetering.Observation)}
	reader.resolver = resolver
	appender, err := NewLateEconomicAppender(LateEconomicAppenderConfig{
		StoreID: "metering-1", Legs: reader, Sink: sink, Resolver: resolver,
	})
	if err != nil {
		t.Fatal(err)
	}
	return appender, sink, reader
}

func newLateTestLegReaderForObservation(t *testing.T, observation sdkmetering.Observation, closed bool) *lateTestLegReader {
	t.Helper()
	callID, err := usageevidence.ParseBillingCallID(observation.Correlation.BillingCallID)
	if err != nil {
		t.Fatal(err)
	}
	record := usageevidence.CallLegUsageRecord{
		CallID: callID, ALegID: "a-leg", BLegID: "b-leg", AttemptSeq: 1,
		BackendID: "backend-a", ProviderID: "provider-a", ModelID: "model-a",
		StartedAt: time.Unix(100, 0).UTC(), FinishedAt: time.Unix(101, 0).UTC(),
		Outcome: usageevidence.LegOutcomeWinner, Surfaced: usageevidence.SurfacedYes,
		Evidence: usageevidence.FinalBillingEvidence{
			InputTokens: usageevidence.Quantity{Value: 2, Present: true}, OutputTokens: usageevidence.Quantity{Value: 1, Present: true},
			Source: usageevidence.EvidenceSourceProviderReported, Authority: usageevidence.EvidenceAuthorityAuthoritative,
		},
		Observations: []sdkmetering.Observation{trustedLateProviderObservation(observation)},
	}
	if !closed {
		record.FinishedAt = time.Time{}
	}
	sealed, err := record.Seal()
	if err != nil {
		t.Fatal(err)
	}
	key, err := usageevidence.CallLegUsageKey(callID, record.BLegID)
	if err != nil {
		t.Fatal(err)
	}
	return &lateTestLegReader{key: key, record: sealed}
}

func lateEvidence(observation sdkmetering.Observation, kind LateEconomicEvidenceKind) LateEconomicEvidence {
	return LateEconomicEvidence{
		Kind: kind,
		Identity: LateEconomicIdentity{
			StoreID: "metering-1", BillingCallID: mustLateCallID(observation), BLegID: "b-leg", AttemptSeq: 1,
			ProviderID: "provider-a", ProviderAccountKey: "provider-account", ProviderRequestID: "request", ProviderChargeID: "charge",
		},
		Observation: observation,
	}
}

func trustedLateProviderObservation(observation sdkmetering.Observation) sdkmetering.Observation {
	trusted := observation.Clone()
	trusted.ID = "trusted-" + observation.ID
	trusted.SourceEventKey = "trusted-" + observation.SourceEventKey
	trusted.Revision = 1
	trusted.Sequence = 1
	trusted.Origin = sdkmetering.OriginProvider
	trusted.Acquisition = sdkmetering.AcquisitionProviderResponse
	trusted.Authority = sdkmetering.AuthorityObservedClaim
	trusted.Semantics = sdkmetering.SemanticsCumulative
	trusted.Supersedes = nil
	correlation := observation.Correlation
	trusted.Correlation = correlation
	if observation.Subject.Kind == sdkmetering.SubjectStatementLine {
		trusted.Subject = sdkmetering.SubjectRef{
			Kind: sdkmetering.SubjectBLeg, StoreID: correlation.StoreID, TenantID: correlation.TenantID,
			ALegID: correlation.ALegID, BillingCallID: correlation.BillingCallID, CallID: correlation.CallID,
			BLegID: correlation.BLegID, AttemptID: correlation.AttemptID, AttemptSeq: correlation.AttemptSeq,
			ProviderAccountKey: correlation.ProviderAccountKey, ProviderRequestID: correlation.ProviderRequestID,
			ProviderChargeID: correlation.ProviderChargeID,
		}
	} else {
		trusted.Subject = observation.Subject
		trusted.Subject.Kind = sdkmetering.SubjectBLeg
	}
	return trusted
}

func lateTestProviderObservation(id string, revision uint64) sdkmetering.Observation {
	callID := mustLateCallIDFromSeed()
	now := time.Unix(1_700_001_000+int64(revision), 0).UTC()
	key := sdkmetering.ComponentKey{Direction: sdkmetering.DirectionOutput, Component: "vendor:tokens", Unit: sdkmetering.UnitToken, SchemaID: "late:v1"}
	value := sdkmetering.Decimal{Coefficient: "2", Scale: 0}
	amount := sdkmetering.Decimal{Coefficient: "3", Scale: 0}
	return sdkmetering.Observation{
		Version: sdkmetering.ObservationVersionV2, ID: "late-" + id, SourceEventKey: "late-source-" + id, Revision: revision,
		StreamID: "late-stream", Sequence: revision, Origin: sdkmetering.OriginProvider,
		Acquisition: sdkmetering.AcquisitionProviderFinalizer, Authority: sdkmetering.AuthorityObservedClaim,
		Perspective: sdkmetering.PerspectiveOperator, Boundary: sdkmetering.BoundaryBackendIngress, Lifecycle: sdkmetering.LifecycleBackendAttempt,
		Subject:     sdkmetering.SubjectRef{Kind: sdkmetering.SubjectBLeg, StoreID: "metering-1", TenantID: "tenant", AccountID: "account", ALegID: "a-leg", BillingCallID: callID.String(), CallID: callID.String(), BLegID: "b-leg", AttemptID: "attempt", AttemptSeq: 1, ProviderAccountKey: "provider-account", ProviderRequestID: "request", ProviderChargeID: "charge"},
		Correlation: sdkmetering.CorrelationV2{StoreID: "metering-1", TenantID: "tenant", CallID: callID.String(), BillingCallID: callID.String(), ALegID: "a-leg", BLegID: "b-leg", AttemptID: "attempt", AttemptSeq: 1, ProviderAccountKey: "provider-account", ProviderRequestID: "request", ProviderChargeID: "charge"},
		Semantics:   sdkmetering.SemanticsCumulative, ObservedAt: now, ReceivedAt: now, MappingRef: "late:v1",
		Measures: []sdkmetering.Measure{{Key: key, Value: &value, Quality: sdkmetering.QualityObserved}},
		Charges:  []sdkmetering.ReportedCharge{{ChargeItemID: "charge-" + id, Component: &key, Amount: &amount, Currency: "USD", Kind: sdkmetering.ChargeKindComponent}},
	}
}

func lateTestStatementObservation(id string, revision uint64) sdkmetering.Observation {
	provider := lateTestProviderObservation(id, revision)
	provider.Origin = sdkmetering.OriginStatement
	provider.Acquisition = sdkmetering.AcquisitionStatementImporter
	provider.Authority = sdkmetering.AuthorityVerifiedStatement
	provider.Subject = sdkmetering.SubjectRef{Kind: sdkmetering.SubjectStatementLine, StoreID: "metering-1", ProviderAccountKey: "provider-account", StatementID: "statement-1", StatementLineID: "line-" + id}
	provider.Correlation = sdkmetering.CorrelationV2{StoreID: "metering-1", TenantID: "tenant", CallID: provider.Correlation.CallID, BillingCallID: provider.Correlation.BillingCallID, ALegID: "a-leg", BLegID: "b-leg", AttemptID: "attempt", AttemptSeq: 1, ProviderAccountKey: "provider-account", ProviderRequestID: "request", ProviderChargeID: "charge"}
	return provider
}

func mustLateCallID(observation sdkmetering.Observation) usageevidence.BillingCallID {
	callID, err := usageevidence.ParseBillingCallID(observation.Correlation.BillingCallID)
	if err != nil {
		panic(err)
	}
	return callID
}

func mustLateCallIDFromSeed() usageevidence.BillingCallID {
	callID, err := usageevidence.NewBillingCallID()
	if err != nil {
		panic(err)
	}
	return callID
}

func lateSinkLen(sink *lateTestObservationSink) int {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	return len(sink.observed)
}
