package compaction

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestPhasesAndEvidenceConstants(t *testing.T) {
	t.Parallel()
	if string(PhaseStarted) != "started" || string(PhaseCompleted) != "completed" {
		t.Fatalf("phase constants drift: %q %q", PhaseStarted, PhaseCompleted)
	}
	wantEvidence := []string{"protocol_strict", "signature_strict", "history_heuristic"}
	gotEvidence := []string{string(EvidenceProtocolStrict), string(EvidenceSignatureStrict), string(EvidenceHistoryHeuristic)}
	if !reflect.DeepEqual(gotEvidence, wantEvidence) {
		t.Fatalf("evidence constants drift: %v want %v", gotEvidence, wantEvidence)
	}
}

// TestDispatch_orderedFailOpen proves callbacks run in observer order, an
// error or panic in one observer never suppresses later listeners, and no
// panic escapes Dispatch.
func TestDispatch_orderedFailOpen(t *testing.T) {
	t.Parallel()
	var order []string
	panicObs := ObserverFunc(func(context.Context, Event) error {
		panic("observer boom")
	})
	errObs := ObserverFunc(func(context.Context, Event) error {
		order = append(order, "err")
		return context.DeadlineExceeded
	})
	okObs := ObserverFunc(func(context.Context, Event) error {
		order = append(order, "ok")
		return nil
	})
	events := []Event{
		{Phase: PhaseStarted, RuleID: "a", TransactionID: "t1", TraceID: "tr1", ALegID: "a1", BLegID: "b1", AttemptSeq: 1, OccurredAt: time.Unix(1, 0)},
		{Phase: PhaseCompleted, Evidence: EvidenceSignatureStrict, RuleID: "b", TransactionID: "t2", TraceID: "tr2", ALegID: "a1", BLegID: "b1", AttemptSeq: 2, OccurredAt: time.Unix(2, 0)},
	}

	Dispatch(context.Background(), []Observer{errObs, panicObs, okObs}, events)

	want := []string{"err", "ok", "err", "ok"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("observer order=%v want %v", order, want)
	}
}

// TestDispatch_noObserversOrEventsIsNoop proves Dispatch never panics and does
// nothing with empty input.
func TestDispatch_noObserversOrEventsIsNoop(t *testing.T) {
	t.Parallel()
	Dispatch(context.Background(), nil, []Event{{Phase: PhaseStarted}})
	Dispatch(context.Background(), []Observer{ObserverFunc(nil)}, nil)
	Dispatch(context.Background(), []Observer{nil}, []Event{{Phase: PhaseStarted}})
}

// TestEvent_metadataOnlyPayload proves the wire shape of an Event carries only
// metadata fields: no canonical/raw request or response content can leak
// through the JSON contract.
func TestEvent_metadataOnlyPayload(t *testing.T) {
	t.Parallel()
	ev := Event{
		Phase:         PhaseCompleted,
		Evidence:      EvidenceProtocolStrict,
		RuleID:        "protocol.context_compaction.v1",
		TransactionID: "tx-abc",
		TraceID:       "trace-1",
		ALegID:        "a-leg-1",
		BLegID:        "b-leg-1",
		AttemptSeq:    3,
		SessionID:     "sess-1",
		OccurredAt:    time.Unix(1700000000, 0).UTC(),
	}
	raw, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	wantKeys := []string{"Phase", "Evidence", "RuleID", "TransactionID", "TraceID", "ALegID", "BLegID", "AttemptSeq", "SessionID", "OccurredAt"}
	for _, k := range wantKeys {
		if _, ok := m[k]; !ok {
			t.Fatalf("event JSON missing metadata key %q: %s", k, raw)
		}
	}
	if len(m) != len(wantKeys) {
		t.Fatalf("event JSON exposes %d keys %v; want exactly %v (content must never leak)", len(m), reflect.ValueOf(m).MapKeys(), wantKeys)
	}
}

// TestPreserver_isContentBearingAndDistinctFromObserver freezes the additive
// preservation contract without changing the metadata-only Observer surface.
func TestPreserver_isContentBearingAndDistinctFromObserver(t *testing.T) {
	t.Parallel()
	var _ Preserver = preservingStub{}
	var _ Observer = ObserverFunc(nil)
	if reflect.TypeFor[Preserver]() == reflect.TypeFor[Observer]() {
		t.Fatal("Preserver must remain distinct from Observer")
	}
	call := aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: "secret"}}}}}
	p := preservingStub{}
	if err := p.BeforeRequest(context.Background(), &call, RequestPreview{Kind: PreviewStartCandidate}, PreservationMeta{}, Services{}); err != nil {
		t.Fatal(err)
	}
	if err := p.RequestOpened(context.Background(), call, []Event{{Phase: PhaseStarted}}, PreservationMeta{}, Services{}); err != nil {
		t.Fatal(err)
	}
	ev := aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "secret"}
	if err := p.BeforeResponseRelease(context.Background(), &ev, ResponsePreview{Kind: PreviewCompletionCandidate}, PreservationMeta{}, Services{}); err != nil {
		t.Fatal(err)
	}
}

type preservingStub struct{}

func (preservingStub) ID() string { return "preserver" }
func (preservingStub) BeforeRequest(context.Context, *aipapi.Call, RequestPreview, PreservationMeta, Services) error {
	return nil
}

func (preservingStub) RequestOpened(context.Context, aipapi.Call, []Event, PreservationMeta, Services) error {
	return nil
}

func (preservingStub) BeforeResponseRelease(context.Context, *aipapi.Event, ResponsePreview, PreservationMeta, Services) error {
	return nil
}

// ObserverFunc adapts a plain func to Observer (test helper).
type ObserverFunc func(context.Context, Event) error

func (f ObserverFunc) OnCompaction(ctx context.Context, ev Event) error {
	if f == nil {
		return nil
	}
	return f(ctx, ev)
}
