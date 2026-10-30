package proxymemory

// Component characterization of the three first-turn decision states (spec
// task 3.2, third bullet; Requirement 3.2; design §3).
//
// Requirement 3.2's effective rule: only a DURABLE or a PUBLISHED decision
// authorizes a turn; a PENDING decision (cached, never published, not durable)
// authorizes only the authoritative first turn's own admission.
//
// This file characterizes TODAY's component behaviour at the artificial seam:
// what each cache state does when a turn reads it, and what the seam does with
// the owner, the project, the erasure epoch, the call and the A-leg. It does
// NOT change production policy, it does NOT add a production refusal rule, and
// it does NOT claim that the pending state is reachable in the validated
// runtime.
//
// THE HAZARD THIS CHARACTERIZES. A pending entry — cached, never published,
// not durable — is REPLAYED into a later turn's call by today's component
// code. That is the theoretical component hazard design §3 names. The runtime
// closure keeps it unreachable: an A-leg has exactly one authoritative first
// turn, the resume token is the only binder to that A-leg, and the
// request-transform stage precedes the token attachment, so the producer's
// put-to-admit interval always closes before any later turn can exist. The
// composed host case pins that closure; this file pins the seam rule it
// rests on.
//
// The seam also has a documented residual: a put of a DIFFERENT decision for
// the same key restarts a queued or failed entry as pending. The last row
// below records that transition.

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
)

// firstTurnStateStore is a configurable store stub for the decision-state
// characterization. It answers the bounded admission read with the snapshot
// the case needs and records the commit and the capture. Every other store
// method stays nil: the characterization must never call one.
type firstTurnStateStore struct {
	Store
	mu       sync.Mutex
	snapshot AdmissionSnapshot
	reads    []AdmissionQuery
	commits  []FirstTurnJob
	upserts  []SessionCapture
}

func (s *firstTurnStateStore) ReadAdmission(_ context.Context, q AdmissionQuery) (AdmissionSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reads = append(s.reads, q)
	out := s.snapshot
	if out.Project.Key == "" && q.CachedProject != nil {
		out.Project = *q.CachedProject
	}
	return out, nil
}

func (s *firstTurnStateStore) CommitFirstTurn(_ context.Context, job FirstTurnJob) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.commits = append(s.commits, job)
	return true, nil
}

func (s *firstTurnStateStore) UpsertSession(_ context.Context, c SessionCapture) (UpsertOutcome, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.upserts = append(s.upserts, c)
	return UpsertStored, nil
}

// firstTurnStateCase is one row of the decision-state characterization.
type firstTurnStateCase struct {
	name string
	// state is the cache state the case starts from.
	state DecisionStatus
	// authoritative marks the turn as the A-leg's authoritative first turn.
	authoritative bool
	// bound marks the store's A-leg as durably bound.
	bound bool
	// durable is the decision the store already holds, if any.
	durable *FirstTurnDecision
	// cacheEpoch and storeEpoch are the erasure epochs the cached entry and
	// the store report.
	cacheEpoch int64
	storeEpoch int64
	// wantEvent is the bounded counter the turn must produce.
	wantEvent string
	// wantInjected says the call must carry the frozen injection.
	wantInjected bool
	// wantCaptured says the turn must be authorized for capture.
	wantCaptured bool
	// wantState is the cache state the turn must leave behind.
	wantState DecisionStatus
}

// TestCaptureFirstTurnDecisionStateCharacterization characterizes the current
// behaviour of the three decision states, the authoritative rule and the
// epoch fence at the component seam.
//
// The expectations are TODAY's behaviour, not a desired policy: the pending
// row records the replay hazard, the published row records the capture
// authorization, the failed row records the terminal state, and the durable
// row records that the current durable read wins over any local state.
func TestCaptureFirstTurnDecisionStateCharacterization(t *testing.T) {
	t.Parallel()

	const injection = "FIRST-TURN-MEMORY-INJECTION"
	project := Project{Key: ProjectKey(captureTestProject), Label: captureTestProject}
	matched := FirstTurnDecision{Outcome: FirstTurnMatched, Injection: injection, SummaryVersion: 7}
	noSummary := FirstTurnDecision{Outcome: FirstTurnNoSummary}

	cases := []firstTurnStateCase{
		{
			// The theoretical hazard: today's component code replays a pending
			// entry into a later turn's call. The seam keeps the capture
			// unauthorized, and the runtime closure keeps the state
			// unreachable.
			name:         "pending on a later turn replays the decision and authorizes no capture",
			state:        DecisionPending,
			wantEvent:    EventInjectMatched,
			wantInjected: true,
			wantCaptured: false,
			wantState:    DecisionPending,
		},
		{
			// Requirement 3.2's rule: the pending decision authorizes the
			// authoritative first turn's OWN admission, which publishes it.
			name:          "pending on the authoritative first turn is admitted and authorizes the capture",
			state:         DecisionPending,
			authoritative: true,
			wantEvent:     EventInjectMatched,
			wantInjected:  true,
			wantCaptured:  true,
			wantState:     DecisionQueued,
		},
		{
			name:         "published on a later turn replays the decision and authorizes the capture",
			state:        DecisionQueued,
			wantEvent:    EventInjectMatched,
			wantInjected: true,
			wantCaptured: true,
			wantState:    DecisionQueued,
		},
		{
			name:         "failed on a later turn authorizes neither the injection nor the capture",
			state:        DecisionFailed,
			wantEvent:    EventInjectDecisionUnavailable,
			wantInjected: false,
			wantCaptured: false,
			wantState:    DecisionFailed,
		},
		{
			// The current durable read wins over a failed local entry, and the
			// DIFFERENT durable decision restarts the local entry as pending:
			// the documented residual of the cache's put path.
			name:         "a durable decision wins over a failed local entry and restarts it as pending",
			state:        DecisionFailed,
			bound:        true,
			durable:      &matched,
			wantEvent:    EventInjectMatched,
			wantInjected: true,
			wantCaptured: true,
			wantState:    DecisionPending,
		},
		{
			// The erasure epoch fences the replay: a cached entry from another
			// epoch never authorizes a turn.
			name:         "a stale epoch invalidates the cached entry",
			state:        DecisionPending,
			cacheEpoch:   3,
			storeEpoch:   4,
			wantEvent:    EventInjectDecisionUnavailable,
			wantInjected: false,
			wantCaptured: false,
			wantState:    DecisionPending,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			store := &firstTurnStateStore{snapshot: AdmissionSnapshot{
				Bound:        tc.bound,
				Project:      project,
				Decision:     tc.durable,
				NextBSeq:     1,
				ErasureEpoch: tc.storeEpoch,
			}}
			metrics := NewPrometheusCollector()
			decisions := NewDecisionCache(0)
			capture := NewCapture(captureTestConfig(), store, metrics, newCaptureFakeClock().Now, slog.New(slog.DiscardHandler), decisions)

			principal := Principal{TenantID: captureTestTenant, PrincipalID: captureTestPrincipal}
			local := FirstTurnDecision{Outcome: FirstTurnMatched, Injection: injection, SummaryVersion: 7}
			switch tc.state {
			case DecisionPending:
				decisions.Put(FirstTurnJob{
					Principal: principal, ALegID: captureTestALeg, Project: project,
					Decision: local, ErasureEpoch: tc.cacheEpoch, EpochKnown: true,
				})
			case DecisionQueued:
				decisions.Put(FirstTurnJob{
					Principal: principal, ALegID: captureTestALeg, Project: project,
					Decision: local, ErasureEpoch: tc.cacheEpoch, EpochKnown: true,
				})
				decisions.Admit(FirstTurnJob{
					Principal: principal, ALegID: captureTestALeg, Project: project,
					Decision: local, ErasureEpoch: tc.cacheEpoch, EpochKnown: true,
				}, func() bool { return true })
			case DecisionFailed:
				decisions.Put(FirstTurnJob{
					Principal: principal, ALegID: captureTestALeg, Project: project,
					Decision: noSummary, ErasureEpoch: tc.cacheEpoch, EpochKnown: true,
				})
				decisions.MarkWriteFailed(FirstTurnJob{
					Principal: principal, ALegID: captureTestALeg, Project: project,
					Decision: noSummary, ErasureEpoch: tc.cacheEpoch, EpochKnown: true,
				})
			}

			meta := captureTestMeta("trace-first-turn-state", captureTestALeg)
			meta.Session.AuthoritativeFirstTurn = tc.authoritative
			call := captureTestCall(t)
			if err := capture.Handle(context.Background(), call, meta, request.Services{}); err != nil {
				t.Fatalf("Handle: %v", err)
			}

			if got := captureCounter(t, metrics, tc.wantEvent); got != 1 {
				t.Fatalf("counter %s = %v, want 1", tc.wantEvent, got)
			}
			if injected := firstTurnCallCarries(call, injection); injected != tc.wantInjected {
				t.Fatalf("call carries the injection = %v, want %v", injected, tc.wantInjected)
			}
			if captured := capturePendingCount(capture) == 1; captured != tc.wantCaptured {
				t.Fatalf("turn authorized for capture = %v, want %v", captured, tc.wantCaptured)
			}
			_, state := decisions.GetStatus(principal, captureTestALeg)
			if state != tc.wantState {
				t.Fatalf("cache state = %v, want %v", state, tc.wantState)
			}
		})
	}
}

// firstTurnCallCarries reports whether the call carries the injection text.
func firstTurnCallCarries(call *aipapi.Call, injection string) bool {
	for _, message := range call.Instructions {
		for _, part := range message.Parts {
			if strings.Contains(part.Text, injection) {
				return true
			}
		}
	}
	return false
}
