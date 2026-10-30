package extensions_test

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/execctx"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/policydecision"
)

type captureObserver struct {
	mu      sync.Mutex
	records []policydecision.Record
	err     error
}

func (c *captureObserver) OnPolicyDecision(_ context.Context, record policydecision.Record) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.records = append(c.records, record)
	return c.err
}

func (c *captureObserver) snapshot() []policydecision.Record {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]policydecision.Record, len(c.records))
	copy(out, c.records)
	return out
}

func newTestLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
}

func TestEvidenceEmitterDefaultIsBounded(t *testing.T) {
	t.Parallel()
	obs := &captureObserver{}
	var buf bytes.Buffer
	emitter := extensions.NewEvidenceEmitter(obs, newTestLogger(&buf), false)
	emitter.Emit(context.Background(), policydecision.Record{
		Stage:         feature.StageIDPreRequest,
		Provider:      policydecision.ProviderRef{ID: "p1", Stage: feature.StageIDPreRequest},
		Outcome:       policydecision.OutcomeDeny,
		Effect:        policydecision.EffectNone,
		ReasonCode:    "policy_denied",
		ClientMessage: "no",
		TraceID:       "trace-1",
	})
	recs := obs.snapshot()
	if len(recs) != 1 {
		t.Fatalf("observer must receive one record, got %d", len(recs))
	}
	if recs[0].ReasonCode != "policy_denied" {
		t.Fatalf("reason code not normalized: %q", recs[0].ReasonCode)
	}
	if !strings.Contains(buf.String(), "policy decision") {
		t.Fatalf("structured log missing: %q", buf.String())
	}
	if !strings.Contains(buf.String(), "stage=pre_request_admission") {
		t.Fatalf("structured log missing stage: %q", buf.String())
	}
}

func TestEvidenceEmitterNormalizesBeforeDelivery(t *testing.T) {
	t.Parallel()
	obs := &captureObserver{}
	emitter := extensions.NewEvidenceEmitter(obs, nil, false)
	emitter.Emit(context.Background(), policydecision.Record{
		Stage:       "   " + feature.StageIDPreRequest + "   ",
		Provider:    policydecision.ProviderRef{ID: "   ", Stage: feature.StageIDPreRequest},
		Outcome:     policydecision.OutcomeDeny,
		Effect:      policydecision.EffectNone,
		Annotations: map[string]string{"valid_key": "v", "bad key!": "drop"},
	})
	recs := obs.snapshot()
	if len(recs) != 1 {
		t.Fatalf("observer must receive one record, got %d", len(recs))
	}
	if recs[0].Provider.ID != "unknown" {
		t.Fatalf("provider id not normalized: %q", recs[0].Provider.ID)
	}
	if recs[0].Stage != feature.StageIDPreRequest {
		t.Fatalf("stage not normalized: %q", recs[0].Stage)
	}
	if _, ok := recs[0].Annotations["bad key!"]; ok {
		t.Fatalf("invalid annotation key must be dropped")
	}
	if _, ok := recs[0].Annotations["valid_key"]; !ok {
		t.Fatalf("valid annotation key must be kept")
	}
}

func TestEvidenceEmitterWithholdsPrivilegedByDefault(t *testing.T) {
	t.Parallel()
	obs := &captureObserver{}
	var buf bytes.Buffer
	emitter := extensions.NewEvidenceEmitter(obs, newTestLogger(&buf), false)
	emitter.Emit(context.Background(), policydecision.Record{
		Stage:      feature.StageIDPreRequest,
		Outcome:    policydecision.OutcomeDeny,
		Effect:     policydecision.EffectNone,
		Visibility: policydecision.EvidencePrivileged,
	})
	if len(obs.snapshot()) != 0 {
		t.Fatalf("privileged record must be withheld by default")
	}
	if buf.Len() != 0 {
		t.Fatalf("privileged record must not be logged by default: %q", buf.String())
	}
}

func TestEvidenceEmitterEmitsPrivilegedWhenDiagnosticsEnabled(t *testing.T) {
	t.Parallel()
	obs := &captureObserver{}
	var buf bytes.Buffer
	emitter := extensions.NewEvidenceEmitter(obs, newTestLogger(&buf), true)
	emitter.Emit(context.Background(), policydecision.Record{
		Stage:      feature.StageIDPreRequest,
		Outcome:    policydecision.OutcomeDeny,
		Effect:     policydecision.EffectNone,
		Visibility: policydecision.EvidencePrivileged,
	})
	if len(obs.snapshot()) != 1 {
		t.Fatalf("privileged record must be emitted when diagnostics enabled")
	}
	if !strings.Contains(buf.String(), "policy decision") {
		t.Fatalf("privileged record must be logged when diagnostics enabled: %q", buf.String())
	}
}

func TestEvidenceEmitterIsolatesObserverFailures(t *testing.T) {
	t.Parallel()
	failing := &captureObserver{err: context.Canceled}
	good := &captureObserver{}
	chain := policydecision.NewChainObserver(failing, good)
	var buf bytes.Buffer
	emitter := extensions.NewEvidenceEmitter(chain, newTestLogger(&buf), false)
	emitter.Emit(context.Background(), policydecision.Record{
		Stage: feature.StageIDPreRequest, Outcome: policydecision.OutcomeDeny, Effect: policydecision.EffectNone,
	})
	if len(good.snapshot()) != 1 {
		t.Fatalf("good observer must still receive record after failing sibling")
	}
	if !strings.Contains(buf.String(), "policy decision") {
		t.Fatalf("emitter must still log after observer failure: %q", buf.String())
	}
}

func TestEvidenceEmitterNilObserverIsSafe(t *testing.T) {
	t.Parallel()
	emitter := extensions.NewEvidenceEmitter(nil, nil, false)
	emitter.Emit(context.Background(), policydecision.Record{
		Stage: feature.StageIDPreRequest, Outcome: policydecision.OutcomeDeny, Effect: policydecision.EffectNone,
	})
}

func TestEvidenceEmitterDiagnosticsEnabledAccessor(t *testing.T) {
	t.Parallel()
	if extensions.NewEvidenceEmitter(nil, nil, true).DiagnosticsEnabled() != true {
		t.Fatalf("diagnostics enabled must be reported")
	}
	if extensions.NewEvidenceEmitter(nil, nil, false).DiagnosticsEnabled() != false {
		t.Fatalf("diagnostics disabled must be reported")
	}
}

func TestEvidenceEmitterDropsIllegalRecord(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		record policydecision.Record
	}{
		{
			name:   "unknown_stage",
			record: policydecision.Record{Stage: "bogus_stage", Outcome: policydecision.OutcomeDeny, Effect: policydecision.EffectNone},
		},
		{
			name:   "unknown_outcome",
			record: policydecision.Record{Stage: feature.StageIDPreRequest, Outcome: policydecision.OutcomeUnknown, Effect: policydecision.EffectNone},
		},
		{
			name:   "unknown_effect",
			record: policydecision.Record{Stage: feature.StageIDPreRequest, Outcome: policydecision.OutcomeAllow, Effect: policydecision.Effect("bogus")},
		},
		{
			name:   "illegal_pair",
			record: policydecision.Record{Stage: feature.StageIDPreRequest, Outcome: policydecision.OutcomeAllow, Effect: policydecision.EffectMutate},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			obs := &captureObserver{}
			var buf bytes.Buffer
			emitter := extensions.NewEvidenceEmitter(obs, newTestLogger(&buf), false)
			emitter.Emit(context.Background(), tc.record)
			if len(obs.snapshot()) != 0 {
				t.Fatalf("observer must not receive illegal record, got %d: %#v", len(obs.snapshot()), obs.snapshot())
			}
			if strings.Contains(buf.String(), "failure_behavior=") {
				t.Fatalf("illegal record must not be delivered as a normalized policy decision log: %q", buf.String())
			}
			if !strings.Contains(buf.String(), "policy decision evidence dropped") {
				t.Fatalf("illegal record must be logged as dropped: %q", buf.String())
			}
		})
	}
}

func TestEvidenceEmitterValidatesWhitespaceStageBeforeDelivery(t *testing.T) {
	t.Parallel()
	obs := &captureObserver{}
	emitter := extensions.NewEvidenceEmitter(obs, nil, false)
	emitter.Emit(context.Background(), policydecision.Record{
		Stage:   "   " + feature.StageIDPreRequest + "   ",
		Outcome: policydecision.OutcomeDeny,
		Effect:  policydecision.EffectNone,
	})
	recs := obs.snapshot()
	if len(recs) != 1 {
		t.Fatalf("whitespace-padded legal stage must be trimmed, validated, and delivered, got %d records", len(recs))
	}
	if recs[0].Stage != feature.StageIDPreRequest {
		t.Fatalf("stage not normalized: %q", recs[0].Stage)
	}
}

// TestDecisionEvidenceWithViewsRefreshesViewsAndSharesSeam proves WithViews
// returns a new seam with refreshed Views while sharing the Emitter,
// TimeoutBudget and OutputCommittedSource, so runtime can refresh views for
// submit vs post-submit pre-backend stages without rebuilding the seam.
func TestDecisionEvidenceWithViewsRefreshesViewsAndSharesSeam(t *testing.T) {
	t.Parallel()
	obs := &captureObserver{}
	emitter := extensions.NewEvidenceEmitter(obs, nil, false)
	budget := extensions.StaticTimeoutBudgetSource{Budget: 250 * time.Millisecond}
	committed := func() bool { return true }
	base := &extensions.DecisionEvidence{
		Emitter:               emitter,
		Views:                 execctx.Views{Annotations: map[string]string{"phase": "submit"}, Attempt: execview.AttemptView{TraceID: "trace-1"}},
		TimeoutBudget:         budget,
		OutputCommittedSource: committed,
	}
	refreshed := base.WithViews(execctx.Views{Annotations: map[string]string{"phase": "post-submit"}, Attempt: execview.AttemptView{TraceID: "trace-1"}})
	if refreshed == nil || refreshed == base {
		t.Fatalf("WithViews must return a new non-nil seam")
	}
	if refreshed.Views.Annotations["phase"] != "post-submit" {
		t.Fatalf("WithViews must replace Views, got %v", refreshed.Views.Annotations)
	}
	if refreshed.Emitter != base.Emitter {
		t.Fatalf("WithViews must share Emitter")
	}
	if refreshed.TimeoutBudget != base.TimeoutBudget {
		t.Fatalf("WithViews must share TimeoutBudget")
	}
	if refreshed.OutputCommittedSource == nil || refreshed.OutputCommittedSource() != true {
		t.Fatalf("WithViews must share OutputCommittedSource")
	}
	// Base seam views are unchanged (submit evidence func keeps submit-time views).
	if base.Views.Annotations["phase"] != "submit" {
		t.Fatalf("WithViews must not mutate the base seam, got %v", base.Views.Annotations)
	}
	// Shared emitter/budget remain functional on the refreshed seam.
	if got := refreshed.TimeoutBudget.TimeoutFor(feature.StageIDPreRequest, "p"); got != 250*time.Millisecond {
		t.Fatalf("shared budget must still resolve, got %v", got)
	}
}

// TestDecisionEvidenceWithViewsNilSafe proves WithViews on a nil seam returns
// nil so callers can chain on an absent seam without nil checks.
func TestDecisionEvidenceWithViewsNilSafe(t *testing.T) {
	t.Parallel()
	var base *extensions.DecisionEvidence
	if got := base.WithViews(execctx.Views{}); got != nil {
		t.Fatalf("WithViews on nil seam must return nil, got %v", got)
	}
}
