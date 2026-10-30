package runtime

import (
	"context"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	coremetering "github.com/aiproxer/aiproxer/internal/core/metering"
	"github.com/aiproxer/aiproxer/internal/core/usageevidence"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

// Task 6 RED: the two local wire adapters (wireBoundaryEventStream outer of
// the provider stream, wireBodyClosingStream outer of that) must delegate the
// provider-owned evidence capabilities instead of hiding them behind the
// embedded aipapi.ManagedEventStream promotion. Every test below drives the
// actual two-adapter chain; before the fix the capability assertions fail
// because the chain exposes none of the optional evidence methods.

// task6BaseStream is a controllable provider stream fragment. Capability sets
// are separate types below because Go exposes optional methods statically;
// each fixture names the exact provider capability it carries.
type task6BaseStream struct {
	aipapi.ManagedEventStream
	bindCalls []coremetering.ObservationIdentity
	closed    bool
	cancelRes aipapi.CancelResult
}

func task6BaseWithEvents(events ...aipapi.Event) *task6BaseStream {
	return &task6BaseStream{ManagedEventStream: aipapi.CloseOnlyManagedStream{Stream: aipapi.NewFixedEventStream(events)}}
}

func (s *task6BaseStream) recordBind(identity coremetering.ObservationIdentity) {
	s.bindCalls = append(s.bindCalls, identity)
}

func (s *task6BaseStream) bindCount() int { return len(s.bindCalls) }

func (s *task6BaseStream) Close() error {
	s.closed = true
	if s.ManagedEventStream != nil {
		return s.ManagedEventStream.Close()
	}
	return nil
}

func (s *task6BaseStream) Cancel(ctx context.Context, cause aipapi.CancelCause) aipapi.CancelResult {
	if s.ManagedEventStream != nil {
		s.cancelRes = s.ManagedEventStream.Cancel(ctx, cause)
		return s.cancelRes
	}
	return aipapi.CancelResult{Mode: aipapi.CancelModeCloseOnly}
}

// task6RichInner carries the full enabled evidence set: rich records with
// partial/unsupported coverage, native observations, legacy usage, an explicit
// enabled accounting state, and a bind recorder.
type task6RichInner struct {
	*task6BaseStream
	usageBatch       []aipapi.Event
	observationBatch []metering.Observation
	recordBatch      []execbackend.EconomicEvidence
}

func (s *task6RichInner) BindEconomicEvidence(identity coremetering.ObservationIdentity) {
	s.recordBind(identity)
}

func (s *task6RichInner) DrainUsageEvidence() []aipapi.Event {
	batch := s.usageBatch
	s.usageBatch = nil
	return batch
}

func (s *task6RichInner) DrainEconomicObservations() []metering.Observation {
	batch := s.observationBatch
	s.observationBatch = nil
	return batch
}

func (s *task6RichInner) DrainEconomicEvidenceRecords() []execbackend.EconomicEvidence {
	batch := s.recordBatch
	s.recordBatch = nil
	return batch
}

func (s *task6RichInner) AccountingEvidenceEnabled() bool { return true }

// task6NativeInner carries native observations only: no rich source, no
// legacy usage, no explicit accounting state.
type task6NativeInner struct {
	*task6BaseStream
	observationBatch []metering.Observation
}

func (s *task6NativeInner) BindEconomicEvidence(identity coremetering.ObservationIdentity) {
	s.recordBind(identity)
}

func (s *task6NativeInner) DrainEconomicObservations() []metering.Observation {
	batch := s.observationBatch
	s.observationBatch = nil
	return batch
}

// task6DisabledInner carries native and legacy evidence behind an explicitly
// disabled accounting state.
type task6DisabledInner struct {
	*task6BaseStream
	usageBatch       []aipapi.Event
	observationBatch []metering.Observation
}

func (s *task6DisabledInner) BindEconomicEvidence(identity coremetering.ObservationIdentity) {
	s.recordBind(identity)
}

func (s *task6DisabledInner) DrainUsageEvidence() []aipapi.Event {
	batch := s.usageBatch
	s.usageBatch = nil
	return batch
}

func (s *task6DisabledInner) DrainEconomicObservations() []metering.Observation {
	batch := s.observationBatch
	s.observationBatch = nil
	return batch
}

func (s *task6DisabledInner) AccountingEvidenceEnabled() bool { return false }

// task6LegacyInner carries legacy usage evidence only.
type task6LegacyInner struct {
	*task6BaseStream
	usageBatch []aipapi.Event
}

func (s *task6LegacyInner) DrainUsageEvidence() []aipapi.Event {
	batch := s.usageBatch
	s.usageBatch = nil
	return batch
}

func task6Observation(id string) metering.Observation {
	return metering.Observation{ID: id, SourceEventKey: "task6-source-" + id, StreamID: "task6-stream", Revision: 1}
}

func task6UsageEvent(input, output int) aipapi.Event {
	return aipapi.Event{Kind: aipapi.EventUsageDelta, InputTokens: input, OutputTokens: output, TotalTokens: input + output}
}

func task6Identity() coremetering.ObservationIdentity {
	return coremetering.ObservationIdentity{
		StoreID: "task6-store", RequestID: "req-task6", CallID: "task6-call", BillingCallID: "task6-call",
		ALegID: "a-task6", BLegID: "b-task6", AttemptID: "b-task6", AttemptSeq: 1,
		ObservedAt: time.Unix(1_700_600_000, 0).UTC(), ReceivedAt: time.Unix(1_700_600_001, 0).UTC(),
	}
}

// task6WrapChain builds the actual production two-adapter chain around one
// provider stream: provider -> wireBoundaryEventStream ->
// wireBodyClosingStream, exactly as openWireAttemptTx stacks them.
func task6WrapChain(inner aipapi.ManagedEventStream) aipapi.ManagedEventStream {
	return &wireBodyClosingStream{ManagedEventStream: &wireBoundaryEventStream{ManagedEventStream: inner}}
}

func task6RequireBinder(t *testing.T, chain aipapi.ManagedEventStream) coremetering.ProviderEvidenceBinder {
	t.Helper()
	binder, ok := chain.(coremetering.ProviderEvidenceBinder)
	if !ok {
		t.Fatal("two-adapter chain hides BindEconomicEvidence from the provider stream")
	}
	return binder
}

func task6RequireRichSource(t *testing.T, chain aipapi.ManagedEventStream) execbackend.EconomicEvidenceSource {
	t.Helper()
	richSource, ok := chain.(execbackend.EconomicEvidenceSource)
	if !ok {
		t.Fatal("two-adapter chain hides DrainEconomicEvidenceRecords from the provider stream")
	}
	return richSource
}

func task6RequireNativeSource(t *testing.T, chain aipapi.ManagedEventStream) metering.ObservationSource {
	t.Helper()
	nativeSource, ok := chain.(metering.ObservationSource)
	if !ok {
		t.Fatal("two-adapter chain hides DrainEconomicObservations from the provider stream")
	}
	return nativeSource
}

func task6RequireLegacySource(t *testing.T, chain aipapi.ManagedEventStream) aipapi.UsageEvidenceSource {
	t.Helper()
	legacySource, ok := chain.(aipapi.UsageEvidenceSource)
	if !ok {
		t.Fatal("two-adapter chain hides DrainUsageEvidence from the provider stream")
	}
	return legacySource
}

func task6AccountingEnabled(t *testing.T, chain aipapi.ManagedEventStream) bool {
	t.Helper()
	state, ok := chain.(interface{ AccountingEvidenceEnabled() bool })
	if !ok {
		t.Fatal("two-adapter chain hides AccountingEvidenceEnabled from the provider stream")
	}
	return state.AccountingEvidenceEnabled()
}

// TestTask6WireChainBindsProviderStreamOnce proves requirement 4.2 through
// the actual chain: one bind on the outer adapter reaches the provider binder
// exactly once with the trusted lineage.
func TestTask6WireChainBindsProviderStreamOnce(t *testing.T) {
	t.Parallel()
	rich := &task6RichInner{task6BaseStream: task6BaseWithEvents()}
	native := &task6NativeInner{task6BaseStream: task6BaseWithEvents()}
	disabled := &task6DisabledInner{task6BaseStream: task6BaseWithEvents()}
	for name, inner := range map[string]interface {
		aipapi.ManagedEventStream
		bindCount() int
	}{"rich": rich, "native": native, "disabled": disabled} {
		chain := task6WrapChain(inner)
		task6RequireBinder(t, chain).BindEconomicEvidence(task6Identity())
		if got := inner.bindCount(); got != 1 {
			t.Fatalf("%s: provider bindings through chain = %d, want exactly one", name, got)
		}
	}

	// Streams without bind capability keep their behavior: binding through the
	// chain is a safe no-op, never a panic or a fabricated binding.
	bare := task6WrapChain(task6BaseWithEvents())
	task6RequireBinder(t, bare).BindEconomicEvidence(task6Identity())
	legacy := &task6LegacyInner{task6BaseStream: task6BaseWithEvents()}
	task6RequireBinder(t, task6WrapChain(legacy)).BindEconomicEvidence(task6Identity())
}

// TestTask6WireChainForwardsRichRecordsUnchanged proves requirement 4.3 for
// the enabled stream: rich partial/unsupported coverage and reasons pass
// through the chain unchanged, legacy usage stays independent, and the native
// queue is not consumed by the rich drain.
func TestTask6WireChainForwardsRichRecordsUnchanged(t *testing.T) {
	t.Parallel()
	partialObs := task6Observation("task6-partial")
	unsupportedObs := task6Observation("task6-unsupported")
	nativeObs := task6Observation("task6-native-beside-rich")
	inner := &task6RichInner{
		task6BaseStream: task6BaseWithEvents(),
		recordBatch: []execbackend.EconomicEvidence{
			{Observation: partialObs, Coverage: "partial", CoverageReason: "task6 partial reason"},
			{Observation: unsupportedObs, Coverage: "unsupported", CoverageReason: "task6 unsupported reason"},
		},
		observationBatch: []metering.Observation{nativeObs},
		usageBatch:       []aipapi.Event{task6UsageEvent(10, 2)},
	}
	chain := task6WrapChain(inner)

	records := task6RequireRichSource(t, chain).DrainEconomicEvidenceRecords()
	if len(records) != 2 {
		t.Fatalf("rich records through chain = %d, want 2", len(records))
	}
	if records[0].Coverage != "partial" || records[0].CoverageReason != "task6 partial reason" ||
		records[0].Observation.ID != "task6-partial" {
		t.Fatalf("first rich record mutated through chain: %+v", records[0])
	}
	if records[1].Coverage != "unsupported" || records[1].CoverageReason != "task6 unsupported reason" ||
		records[1].Observation.ID != "task6-unsupported" {
		t.Fatalf("second rich record mutated through chain: %+v", records[1])
	}
	if again := task6RequireRichSource(t, chain).DrainEconomicEvidenceRecords(); len(again) != 0 {
		t.Fatalf("second rich drain through chain = %d records, want 0 (forward once)", len(again))
	}

	// The rich drain must not consume the native queue or the legacy queue.
	observations := task6RequireNativeSource(t, chain).DrainEconomicObservations()
	if len(observations) != 1 || observations[0].ID != "task6-native-beside-rich" {
		t.Fatalf("native observations after rich drain = %+v, want the untouched native batch", observations)
	}
	usage := task6RequireLegacySource(t, chain).DrainUsageEvidence()
	if len(usage) != 1 || usage[0].Kind != aipapi.EventUsageDelta || usage[0].InputTokens != 10 || usage[0].OutputTokens != 2 {
		t.Fatalf("legacy usage after rich drain = %+v, want the independent usage batch", usage)
	}
	if !task6AccountingEnabled(t, chain) {
		t.Fatal("enabled rich chain reports AccountingEvidenceEnabled = false, want true")
	}
	if !hasHostOnlyEconomicEvidenceSource(chain) {
		t.Fatal("hasHostOnlyEconomicEvidenceSource(chain) = false for the enabled stream, want true")
	}
}

// TestTask6WireChainLiftsNativeWithoutShadowingRich proves the shadowing
// hazard: the chain exposes the rich drain method, so capability-preferring
// consumers take the rich branch for native-only streams. The adapter must
// lift the native observation into a carrier instead of returning empty.
func TestTask6WireChainLiftsNativeWithoutShadowingRich(t *testing.T) {
	t.Parallel()
	chain := task6WrapChain(&task6NativeInner{
		task6BaseStream:  task6BaseWithEvents(),
		observationBatch: []metering.Observation{task6Observation("task6-native-lift")},
	})

	records := task6RequireRichSource(t, chain).DrainEconomicEvidenceRecords()
	if len(records) != 1 {
		t.Fatalf("lifted rich records for native-only stream = %d, want 1 (native drain shadowed)", len(records))
	}
	if records[0].Observation.ID != "task6-native-lift" {
		t.Fatalf("lifted carrier observation = %+v, want the native observation", records[0].Observation)
	}
	if records[0].Coverage != "" || records[0].CoverageReason != "" {
		t.Fatalf("lifted carrier invents coverage: %+v, want empty coverage/reason", records[0])
	}
	// Each provider-owned batch forwards once: the native queue is consumed by
	// the lift, so a later native drain finds nothing to duplicate.
	if observations := task6RequireNativeSource(t, chain).DrainEconomicObservations(); len(observations) != 0 {
		t.Fatalf("native drain after rich lift = %+v, want empty (forward once)", observations)
	}
	if !task6AccountingEnabled(t, chain) {
		t.Fatal("native-only chain reports AccountingEvidenceEnabled = false, want true from genuine native capability")
	}

	// Reverse order on a fresh chain: a native drain first must leave the rich
	// drain empty, proving the same batch never drains twice.
	fresh := task6WrapChain(&task6NativeInner{
		task6BaseStream:  task6BaseWithEvents(),
		observationBatch: []metering.Observation{task6Observation("task6-native-first")},
	})
	observations := task6RequireNativeSource(t, fresh).DrainEconomicObservations()
	if len(observations) != 1 || observations[0].ID != "task6-native-first" {
		t.Fatalf("native-first drain = %+v, want the native batch", observations)
	}
	if records := task6RequireRichSource(t, fresh).DrainEconomicEvidenceRecords(); len(records) != 0 {
		t.Fatalf("rich drain after native drain = %+v, want empty (forward once)", records)
	}
}

// TestTask6WireChainPreservesExplicitlyDisabledAccounting proves requirement
// 4.5: an explicit disabled state wins over genuine native/legacy capability,
// while both evidence queues still forward.
func TestTask6WireChainPreservesExplicitlyDisabledAccounting(t *testing.T) {
	t.Parallel()
	inner := &task6DisabledInner{
		task6BaseStream:  task6BaseWithEvents(),
		observationBatch: []metering.Observation{task6Observation("task6-disabled-native")},
		usageBatch:       []aipapi.Event{task6UsageEvent(4, 1)},
	}
	chain := task6WrapChain(inner)

	if task6AccountingEnabled(t, chain) {
		t.Fatal("explicitly disabled chain reports AccountingEvidenceEnabled = true, want explicit false to win")
	}
	if hasHostOnlyEconomicEvidenceSource(chain) {
		t.Fatal("hasHostOnlyEconomicEvidenceSource(disabled chain) = true, want false to match the explicit state")
	}
	if hasHostOnlyEconomicEvidenceSource(inner) {
		t.Fatal("hasHostOnlyEconomicEvidenceSource(inner) = true for the disabled stream, want false (test premise)")
	}
	usage := task6RequireLegacySource(t, chain).DrainUsageEvidence()
	if len(usage) != 1 || usage[0].InputTokens != 4 {
		t.Fatalf("legacy usage through disabled chain = %+v, want the forwarded batch", usage)
	}
	records := task6RequireRichSource(t, chain).DrainEconomicEvidenceRecords()
	if len(records) != 1 || records[0].Observation.ID != "task6-disabled-native" {
		t.Fatalf("lifted records through disabled chain = %+v, want the native observation", records)
	}
}

// TestTask6WireChainPreservesAbsentEvidenceBehavior proves requirement 4.5
// for capability-free streams: no StoreID, usage, or finality is fabricated,
// and the chain reports no evidence authority.
func TestTask6WireChainPreservesAbsentEvidenceBehavior(t *testing.T) {
	t.Parallel()
	chain := task6WrapChain(task6BaseWithEvents(
		aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "task6 content"},
		aipapi.Event{Kind: aipapi.EventResponseFinished},
	))

	if task6AccountingEnabled(t, chain) {
		t.Fatal("capability-free chain reports AccountingEvidenceEnabled = true, want false")
	}
	if hasHostOnlyEconomicEvidenceSource(chain) {
		t.Fatal("hasHostOnlyEconomicEvidenceSource(bare chain) = true, want false")
	}
	if records := task6RequireRichSource(t, chain).DrainEconomicEvidenceRecords(); len(records) != 0 {
		t.Fatalf("rich drain on capability-free chain = %+v, want nil (never fabricate)", records)
	}
	if observations := task6RequireNativeSource(t, chain).DrainEconomicObservations(); len(observations) != 0 {
		t.Fatalf("native drain on capability-free chain = %+v, want nil (never fabricate)", observations)
	}
	if usage := task6RequireLegacySource(t, chain).DrainUsageEvidence(); len(usage) != 0 {
		t.Fatalf("legacy drain on capability-free chain = %+v, want nil (never fabricate)", usage)
	}
	// Binding a capability-free chain is a safe no-op.
	task6RequireBinder(t, chain).BindEconomicEvidence(task6Identity())
}

// TestTask6WireChainKeepsLegacyDrainIndependent proves legacy usage evidence
// stays on its own drain: rich and native drains find nothing on a
// legacy-only stream while the legacy batches arrive in order, once.
func TestTask6WireChainKeepsLegacyDrainIndependent(t *testing.T) {
	t.Parallel()
	chain := task6WrapChain(&task6LegacyInner{
		task6BaseStream: task6BaseWithEvents(),
		usageBatch:      []aipapi.Event{task6UsageEvent(7, 3), task6UsageEvent(1, 1)},
	})

	usage := task6RequireLegacySource(t, chain).DrainUsageEvidence()
	if len(usage) != 2 || usage[0].InputTokens != 7 || usage[1].InputTokens != 1 {
		t.Fatalf("legacy usage through chain = %+v, want both batches in order", usage)
	}
	if again := task6RequireLegacySource(t, chain).DrainUsageEvidence(); len(again) != 0 {
		t.Fatalf("second legacy drain = %+v, want empty (forward once)", again)
	}
	if records := task6RequireRichSource(t, chain).DrainEconomicEvidenceRecords(); len(records) != 0 {
		t.Fatalf("rich drain on legacy-only chain = %+v, want nil", records)
	}
	if observations := task6RequireNativeSource(t, chain).DrainEconomicObservations(); len(observations) != 0 {
		t.Fatalf("native drain on legacy-only chain = %+v, want nil", observations)
	}
	if !task6AccountingEnabled(t, chain) {
		t.Fatal("legacy-only chain reports AccountingEvidenceEnabled = false, want true from genuine legacy capability")
	}
}

// TestTask6WireChainReachesLiveConsumerOnce drives the real attempt consumer
// through the chain with a genuine bound provider buffer: the lifted native
// observation must reach attempt-owned evidence exactly once.
func TestTask6WireChainReachesLiveConsumerOnce(t *testing.T) {
	t.Parallel()
	callID, err := usageevidence.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_610_000, 0).UTC()
	buffer := coremetering.NewProviderEvidenceBuffer()
	buffer.BindEconomicEvidence(coremetering.ObservationIdentity{
		StoreID: "task6-store", RequestID: "req-task6-live", CallID: callID.String(), BillingCallID: callID.String(),
		ALegID: "a-task6-live", BLegID: "b-task6-live", AttemptID: "b-task6-live", AttemptSeq: 1,
		ObservedAt: now, ReceivedAt: now,
	})
	buffer.Add(r5cProviderDraft("task6-live-native", 7))
	chain := task6WrapChain(&r5cProviderStream{buffer: buffer})

	attempt := newAttemptSession(attemptSessionInput{})
	attempt.drainStreamUsageEvidence(chain)
	economic, _ := attempt.economicEvidenceDrain()
	if len(economic) != 1 {
		t.Fatalf("attempt-owned economic evidence through chain = %d, want 1", len(economic))
	}
	if economic[0].Observation.SourceEventKey != "task6-live-native" {
		t.Fatalf("attempt-owned observation source = %q, want task6-live-native", economic[0].Observation.SourceEventKey)
	}
	// A second consumer drain finds nothing: the batch forwarded once.
	attempt.drainStreamUsageEvidence(chain)
	economic, _ = attempt.economicEvidenceDrain()
	if len(economic) != 0 {
		t.Fatalf("second consumer drain through chain = %d observations, want 0 (forward once)", len(economic))
	}
}

// TestTask6WireChainKeepsCloseCancelOwnership guards the Done clause: Close,
// Cancel, and Recv stay owned by the existing components, unchanged by
// evidence delegation.
func TestTask6WireChainKeepsCloseCancelOwnership(t *testing.T) {
	t.Parallel()
	inner := &task6RichInner{
		task6BaseStream: task6BaseWithEvents(
			aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "task6 passthrough"},
			aipapi.Event{Kind: aipapi.EventResponseFinished},
		),
	}
	chain := task6WrapChain(inner)

	first, err := chain.Recv(context.Background())
	if err != nil {
		t.Fatalf("chain Recv failed: %v", err)
	}
	if first.Kind != aipapi.EventTextDelta || first.Delta != "task6 passthrough" {
		t.Fatalf("chain Recv = %+v, want the provider TextDelta unchanged", first)
	}
	res := chain.Cancel(context.Background(), aipapi.CancelCause{Kind: aipapi.CancelContextDone})
	if res.Mode != aipapi.CancelModeCloseOnly {
		t.Fatalf("chain Cancel mode = %q, want close_only from the inner stream", res.Mode)
	}
	if err := chain.Close(); err != nil {
		t.Fatalf("chain Close failed: %v", err)
	}
	if !inner.closed {
		t.Fatal("chain Close did not reach the provider stream")
	}
}
