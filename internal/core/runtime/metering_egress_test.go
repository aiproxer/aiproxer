package runtime

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/metering/checkpoint"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

type recordingMeter struct {
	mu    sync.Mutex
	facts []metering.Fact
}

func (r *recordingMeter) Append(ctx context.Context, fact metering.Fact) error {
	_ = ctx
	r.mu.Lock()
	defer r.mu.Unlock()
	r.facts = append(r.facts, fact)
	return nil
}

// Facts returns a snapshot of recorded facts under the recorder lock.
func (r *recordingMeter) Facts() []metering.Fact {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]metering.Fact, len(r.facts))
	copy(out, r.facts)
	return out
}

func TestRecordingMeter_ConcurrentAppendPreservesAllFacts(t *testing.T) {
	t.Parallel()
	rec := &recordingMeter{}
	const goroutines = 32
	const perG = 50
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := range goroutines {
		go func(g int) {
			defer wg.Done()
			for i := range perG {
				fact := metering.Fact{FactID: fmt.Sprintf("g%d-%d", g, i)}
				if err := rec.Append(context.Background(), fact); err != nil {
					t.Errorf("Append: %v", err)
					return
				}
			}
		}(g)
	}
	wg.Wait()
	facts := rec.Facts()
	want := goroutines * perG
	if len(facts) != want {
		t.Fatalf("facts=%d want %d", len(facts), want)
	}
	seen := make(map[string]struct{}, len(facts))
	for _, f := range facts {
		if _, dup := seen[f.FactID]; dup {
			t.Fatalf("duplicate FactID %q", f.FactID)
		}
		seen[f.FactID] = struct{}{}
	}
	for g := range goroutines {
		for i := range perG {
			id := fmt.Sprintf("g%d-%d", g, i)
			if _, ok := seen[id]; !ok {
				t.Fatalf("missing fact %s", id)
			}
		}
	}
}

func TestAppendMeteringFact_NilRecorderNoPanic(t *testing.T) {
	t.Parallel()
	ex := &Executor{}
	if err := ex.appendMeteringFact(context.Background(), metering.Fact{FactID: "x"}); err != nil {
		t.Fatal(err)
	}
}

func TestEmitEgressFacts_OrderedViaRecorder(t *testing.T) {
	t.Parallel()
	rec := &recordingMeter{}
	ex := &Executor{}
	ex.MeteringRecorder = rec
	ex.Now = func() time.Time { return time.Unix(50, 0).UTC() }

	holder := &checkpoint.RequestHolder{}
	_, err := holder.CaptureOrReuseFrontendIngress(checkpoint.FrontendIngressInput{
		Call:         aipapi.Call{ID: "req-eg"},
		CheckpointID: "fe",
		StreamID:     "fe-stream",
		Now:          time.Unix(1, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = holder.StoreBackendIngress(checkpoint.BackendIngressInput{
		Call: aipapi.Call{
			ID:       "req-eg",
			Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("x")}}},
		},
		AttemptID:    "b-leg-1",
		BLegID:       "b-leg-1",
		CheckpointID: "be",
		StreamID:     "be-stream",
		Now:          time.Unix(2, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := withMeteringHolder(context.Background(), holder)
	stream := &retryRecvStream{
		facts: testRecvTurnFacts(recvTurnFacts{
			traceID: "req-eg",
		}),
		attempt: testAttemptSlot(b2bua.BLegRecord{BLegID: "b-leg-1"}, routing.AttemptCandidate{}, authorityLifecycle{}),
	}
	bindTestRuntimeOwners(stream, ex)
	usageEv := aipapi.Event{Kind: aipapi.EventUsageDelta, InputTokens: 3, OutputTokens: 1, TotalTokens: 4}
	stream.terminal.emitBackendEgressMeteringFactForAttempt(ctx, stream.attempt.snapshot(), metering.AttemptOutcomeWinner, metering.SurfacedYes, usageEv)
	stream.terminal.emitFrontendEgressMeteringFact(ctx, stream.facts.traceID, usageEv)
	if len(rec.facts) != 2 {
		t.Fatalf("facts=%d", len(rec.facts))
	}
	if rec.facts[0].Boundary != metering.BoundaryBackendEgress {
		t.Fatal(rec.facts[0].Boundary)
	}
	if rec.facts[1].Boundary != metering.BoundaryFrontendEgress {
		t.Fatal(rec.facts[1].Boundary)
	}
}

func TestEmitBackendEgress_LoserFailedCanceledOutcomes(t *testing.T) {
	t.Parallel()
	rec := &recordingMeter{}
	ex := &Executor{}
	ex.MeteringRecorder = rec
	ex.Now = func() time.Time { return time.Unix(50, 0).UTC() }
	holder := &checkpoint.RequestHolder{}
	_, err := holder.CaptureOrReuseFrontendIngress(checkpoint.FrontendIngressInput{
		Call:         aipapi.Call{ID: "req-out"},
		CheckpointID: "fe",
		StreamID:     "fe-stream",
		Now:          time.Unix(1, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = holder.StoreBackendIngress(checkpoint.BackendIngressInput{
		Call: aipapi.Call{
			ID:       "req-out",
			Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("x")}}},
		},
		AttemptID:    "b-leg-out",
		BLegID:       "b-leg-out",
		CheckpointID: "be",
		StreamID:     "be-stream",
		Now:          time.Unix(2, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := withMeteringHolder(context.Background(), holder)
	usageEv := aipapi.Event{Kind: aipapi.EventUsageDelta, InputTokens: 1, OutputTokens: 0, TotalTokens: 1}

	cases := []struct {
		name     string
		outcome  metering.AttemptOutcome
		surfaced metering.SurfacedState
	}{
		{"loser", metering.AttemptOutcomeLoser, metering.SurfacedNo},
		{"failed_swallowed", metering.AttemptOutcomeFailed, metering.SurfacedNo},
		{"failed_surfaced", metering.AttemptOutcomeFailed, metering.SurfacedYes},
		{"canceled", metering.AttemptOutcomeCanceled, metering.SurfacedNo},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := len(rec.facts)
			ex.emitBackendEgressMeteringFact(ctx, "b-leg-out", tc.outcome, tc.surfaced, usageEv)
			if len(rec.facts) != before+1 {
				t.Fatalf("facts=%d want %d", len(rec.facts), before+1)
			}
			got := rec.facts[len(rec.facts)-1]
			if got.Boundary != metering.BoundaryBackendEgress {
				t.Fatalf("boundary=%s", got.Boundary)
			}
			if got.AttemptOutcome != tc.outcome {
				t.Fatalf("outcome=%s want %s", got.AttemptOutcome, tc.outcome)
			}
			if got.Surfaced != tc.surfaced {
				t.Fatalf("surfaced=%s want %s", got.Surfaced, tc.surfaced)
			}
		})
	}
}

func TestEmitFrontendEgress_WithoutWinnerFinalize(t *testing.T) {
	t.Parallel()
	rec := &recordingMeter{}
	ex := &Executor{}
	ex.MeteringRecorder = rec
	ex.Now = func() time.Time { return time.Unix(50, 0).UTC() }
	holder := &checkpoint.RequestHolder{}
	_, err := holder.CaptureOrReuseFrontendIngress(checkpoint.FrontendIngressInput{
		Call:         aipapi.Call{ID: "req-fe-term"},
		CheckpointID: "fe",
		StreamID:     "fe-stream",
		Now:          time.Unix(1, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := withMeteringHolder(context.Background(), holder)
	ex.emitFrontendEgressMeteringFact(ctx, "req-fe-term", aipapi.Event{Kind: aipapi.EventUsageDelta, InputTokens: 2, OutputTokens: 1, TotalTokens: 3})
	if len(rec.facts) != 1 {
		t.Fatalf("facts=%d", len(rec.facts))
	}
	if rec.facts[0].Boundary != metering.BoundaryFrontendEgress {
		t.Fatal(rec.facts[0].Boundary)
	}
}
