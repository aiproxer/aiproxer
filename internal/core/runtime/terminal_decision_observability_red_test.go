package runtime

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/terminaldecision"
)

func TestTerminalDecisionObservationIsBoundedAndPrivate(t *testing.T) {
	probe := &terminalDecisionObservationProbe{}
	provider := terminalDecisionObservationProvider{
		id: "provider.alpha",
		decide: func(context.Context, terminaldecision.Input) (terminaldecision.Decision, error) {
			return terminaldecision.Decision{Kind: terminaldecision.DecisionAllowStop, ReasonCode: "complete"}, nil
		},
	}
	turn := &turnTerminal{
		log:                           slog.New(probe),
		terminalDecisionProviderID:    provider.ID(),
		terminalDecisionProviderHasID: true,
	}
	input := observedTerminalDecisionInput(true)
	input.Candidate.Reference = "candidate-secret"
	input.Request = terminaldecision.RequestIdentity{
		RequestID: "request-secret",
		TraceID:   "trace-secret",
		ALegID:    "a-leg-secret",
		BLegID:    "b-leg-secret",
	}
	input.Evidence.Objective = "prompt-secret"

	turn.sharedTerminalDecision(context.Background(), provider, input)

	records := probe.snapshot()
	if len(records) != 1 {
		t.Fatalf("observations = %d, want exactly one", len(records))
	}
	record := records[0]
	if record.message != "terminal_decision_evaluation" {
		t.Fatalf("message = %q, want terminal_decision_evaluation", record.message)
	}
	wantKeys := map[string]bool{
		"candidate_cause":  true,
		"provider_id":      true,
		"decision_kind":    true,
		"reason_code":      true,
		"output_committed": true,
	}
	if len(record.attrs) != len(wantKeys) {
		t.Fatalf("attribute keys = %#v, want exactly bounded decision fields", record.attrs)
	}
	for key := range record.attrs {
		if !wantKeys[key] {
			t.Fatalf("unexpected observation attribute %q", key)
		}
	}
	if record.attrs["candidate_cause"] != string(terminaldecision.CandidateCauseNormal) {
		t.Fatalf("candidate cause = %#v", record.attrs["candidate_cause"])
	}
	if record.attrs["provider_id"] != "provider.alpha" {
		t.Fatalf("provider id = %#v", record.attrs["provider_id"])
	}
	if record.attrs["decision_kind"] != string(terminaldecision.DecisionAllowStop) {
		t.Fatalf("decision kind = %#v", record.attrs["decision_kind"])
	}
	if record.attrs["reason_code"] != "complete" {
		t.Fatalf("reason code = %#v", record.attrs["reason_code"])
	}
	if committed, ok := record.attrs["output_committed"].(bool); !ok || !committed {
		t.Fatalf("output committed = %#v, want true", record.attrs["output_committed"])
	}
	for _, secret := range []string{"candidate-secret", "request-secret", "trace-secret", "a-leg-secret", "b-leg-secret", "prompt-secret"} {
		if record.contains(secret) {
			t.Fatalf("observation leaked %q: %#v", secret, record)
		}
	}
	reasonCode, ok := record.attrs["reason_code"].(string)
	if !ok {
		t.Fatalf("reason code is not string: %#v", record.attrs["reason_code"])
	}
	if len(reasonCode) > terminaldecision.MaxReasonCodeBytes {
		t.Fatalf("reason code exceeded bound: %d", len(reasonCode))
	}
}

func TestTerminalDecisionObservationCoversNormalizedOutcomes(t *testing.T) {
	const attackerReason = "attacker-provider-detail-that-must-not-become-a-platform-failure"
	cases := []struct {
		name       string
		provider   terminaldecision.Provider
		wantKind   terminaldecision.DecisionKind
		wantReason string
		wantID     string
	}{
		{
			name:       "nil provider",
			wantKind:   terminaldecision.DecisionAllowStop,
			wantReason: "no_provider",
			wantID:     "none",
		},
		{
			name: "invalid provider",
			provider: terminalDecisionObservationProvider{
				id: strings.Repeat("p", terminaldecision.MaxProviderIDBytes+1),
				decide: func(context.Context, terminaldecision.Input) (terminaldecision.Decision, error) {
					t.Fatal("invalid provider was called")
					return terminaldecision.Decision{}, nil
				},
			},
			wantKind:   terminaldecision.DecisionAllowStop,
			wantReason: "invalid_provider",
			wantID:     "invalid",
		},
		{
			name: "provider error",
			provider: terminalDecisionObservationProvider{
				id: "provider.error",
				decide: func(context.Context, terminaldecision.Input) (terminaldecision.Decision, error) {
					return terminaldecision.Decision{}, errors.New(attackerReason)
				},
			},
			wantKind:   terminaldecision.DecisionAllowStop,
			wantReason: "provider_error",
			wantID:     "provider.error",
		},
		{
			name: "provider panic",
			provider: terminalDecisionObservationProvider{
				id: "provider.panic",
				decide: func(context.Context, terminaldecision.Input) (terminaldecision.Decision, error) {
					panic(attackerReason)
				},
			},
			wantKind:   terminaldecision.DecisionAllowStop,
			wantReason: "provider_panic",
			wantID:     "provider.panic",
		},
		{
			name: "provider timeout",
			provider: terminalDecisionObservationProvider{
				id: "provider.timeout",
				decide: func(context.Context, terminaldecision.Input) (terminaldecision.Decision, error) {
					return terminaldecision.Decision{}, context.DeadlineExceeded
				},
			},
			wantKind:   terminaldecision.DecisionAllowStop,
			wantReason: "deadline_exceeded",
			wantID:     "provider.timeout",
		},
		{
			name: "continue",
			provider: terminalDecisionObservationProvider{
				id: "provider.continue",
				decide: func(context.Context, terminaldecision.Input) (terminaldecision.Decision, error) {
					return terminaldecision.Decision{
						Kind:       terminaldecision.DecisionContinue,
						ReasonCode: "continue",
						Continue: &terminaldecision.ContinuationIntent{
							TrajectoryRef: "trajectory",
							ControlRef:    "control",
							Provenance:    "internal-control",
							ReasonCode:    "continue",
						},
					}, nil
				},
			},
			wantKind:   terminaldecision.DecisionContinue,
			wantReason: "continue",
			wantID:     "provider.continue",
		},
		{
			name: "allow stop",
			provider: terminalDecisionObservationProvider{
				id: "provider.stop",
				decide: func(context.Context, terminaldecision.Input) (terminaldecision.Decision, error) {
					return terminaldecision.Decision{Kind: terminaldecision.DecisionAllowStop, ReasonCode: "done"}, nil
				},
			},
			wantKind:   terminaldecision.DecisionAllowStop,
			wantReason: "done",
			wantID:     "provider.stop",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			probe := &terminalDecisionObservationProbe{}
			var provID string
			var hasID bool
			if tc.provider != nil {
				if id, err := terminaldecision.ProviderIdentity(tc.provider); err == nil {
					provID = id
					hasID = true
				}
			}
			turn := &turnTerminal{
				log:                           slog.New(probe),
				terminalDecisionProviderID:    provID,
				terminalDecisionProviderHasID: hasID,
			}
			turn.sharedTerminalDecision(context.Background(), tc.provider, observedTerminalDecisionInput(false))
			records := probe.snapshot()
			if len(records) != 1 {
				t.Fatalf("observations = %d, want one", len(records))
			}
			record := records[0]
			if record.attrs["provider_id"] != tc.wantID {
				t.Fatalf("provider id = %#v, want %q", record.attrs["provider_id"], tc.wantID)
			}
			if record.attrs["decision_kind"] != string(tc.wantKind) {
				t.Fatalf("decision kind = %#v, want %q", record.attrs["decision_kind"], tc.wantKind)
			}
			if record.attrs["reason_code"] != tc.wantReason {
				t.Fatalf("reason code = %#v, want %q", record.attrs["reason_code"], tc.wantReason)
			}
			candidateCause, ok := record.attrs["candidate_cause"].(string)
			if !ok {
				t.Fatalf("candidate cause is not string: %#v", record.attrs["candidate_cause"])
			}
			if len(candidateCause) > terminaldecision.MaxReasonCodeBytes {
				t.Fatalf("candidate cause exceeded bound")
			}
			reasonCode, ok := record.attrs["reason_code"].(string)
			if !ok {
				t.Fatalf("reason code is not string: %#v", record.attrs["reason_code"])
			}
			if len(reasonCode) > terminaldecision.MaxReasonCodeBytes {
				t.Fatalf("reason code exceeded bound")
			}
			if record.contains(attackerReason) {
				t.Fatalf("attacker provider detail leaked: %#v", record)
			}
		})
	}
}

func TestTerminalDecisionObservationExcludesWaiters(t *testing.T) {
	probe := &terminalDecisionObservationProbe{}
	entered := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	provider := terminalDecisionObservationProvider{
		id: "provider.shared",
		decide: func(ctx context.Context, _ terminaldecision.Input) (terminaldecision.Decision, error) {
			calls.Add(1)
			close(entered)
			select {
			case <-release:
				return terminaldecision.Decision{Kind: terminaldecision.DecisionAllowStop, ReasonCode: "done"}, nil
			case <-ctx.Done():
				return terminaldecision.Decision{}, ctx.Err()
			}
		},
	}
	turn := &turnTerminal{
		log:                           slog.New(probe),
		terminalDecisionProviderID:    provider.ID(),
		terminalDecisionProviderHasID: true,
	}
	input := observedTerminalDecisionInput(false)
	firstDone := make(chan struct{})
	go func() {
		turn.sharedTerminalDecision(context.Background(), provider, input)
		close(firstDone)
	}()
	<-entered
	secondDone := make(chan struct{})
	go func() {
		turn.sharedTerminalDecision(context.Background(), provider, input)
		close(secondDone)
	}()
	close(release)
	<-firstDone
	<-secondDone

	if got := calls.Load(); got != 1 {
		t.Fatalf("provider evaluations = %d, want one", got)
	}
	if got := len(probe.snapshot()); got != 1 {
		t.Fatalf("observations = %d, want one actual evaluation, not waiter", got)
	}
}

type terminalDecisionObservationProvider struct {
	id     string
	decide func(context.Context, terminaldecision.Input) (terminaldecision.Decision, error)
}

func (p terminalDecisionObservationProvider) ID() string { return p.id }

func (p terminalDecisionObservationProvider) Decide(ctx context.Context, input terminaldecision.Input) (terminaldecision.Decision, error) {
	return p.decide(ctx, input)
}

func observedTerminalDecisionInput(committed bool) terminaldecision.Input {
	return terminaldecision.Input{
		Candidate: terminaldecision.CanonicalTerminalCandidate{
			Cause:           terminaldecision.CandidateCauseNormal,
			Reference:       "candidate-ref",
			OutputCommitted: committed,
		},
		Request: terminaldecision.RequestIdentity{RequestID: "request-id", TraceID: "trace-id", ALegID: "a-leg", BLegID: "b-leg"},
		Policy:  terminaldecision.PolicySnapshot{Revision: "revision"},
		Evidence: terminaldecision.Evidence{
			Objective:     "objective",
			RecentText:    "recent",
			CandidateText: "candidate",
		},
		Deadline: time.Now().Add(time.Second),
	}
}

type terminalDecisionObservationProbe struct {
	mu      sync.Mutex
	records []terminalDecisionObservedRecord
}

type terminalDecisionObservedRecord struct {
	message string
	attrs   map[string]any
}

func (p *terminalDecisionObservationProbe) Enabled(context.Context, slog.Level) bool { return true }

func (p *terminalDecisionObservationProbe) Handle(_ context.Context, record slog.Record) error {
	attrs := make(map[string]any)
	record.Attrs(func(attr slog.Attr) bool {
		attrs[attr.Key] = attr.Value.Any()
		return true
	})
	p.mu.Lock()
	p.records = append(p.records, terminalDecisionObservedRecord{message: record.Message, attrs: attrs})
	p.mu.Unlock()
	return nil
}

func (p *terminalDecisionObservationProbe) WithAttrs([]slog.Attr) slog.Handler { return p }

func (p *terminalDecisionObservationProbe) WithGroup(string) slog.Handler { return p }

func (p *terminalDecisionObservationProbe) snapshot() []terminalDecisionObservedRecord {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]terminalDecisionObservedRecord(nil), p.records...)
}

func (r terminalDecisionObservedRecord) contains(value string) bool {
	if strings.Contains(r.message, value) {
		return true
	}
	for key, attr := range r.attrs {
		if strings.Contains(key, value) || strings.Contains(asObservationString(attr), value) {
			return true
		}
	}
	return false
}

func asObservationString(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	return ""
}
