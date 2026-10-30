package reasoningpreservation_test

import (
	"context"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/plugins/features/reasoningpreservation"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
)

// Claim: attempt policy unifies restored/client-sent reasoning (req 3.1-3.4):
// log_skip withholds unrepresentable restores, reject/empty excludes.
// Mutant-checked (see report).
func TestHandleAttempt_AttemptPolicyUnifiesUnrepresentableRestore(t *testing.T) {
	t.Parallel()
	const restoreYAML = `
action: restore
use_builtin_catalog: false
rules:
  - id: be
    backend: be
    enabled: true
on_ambiguous: log_skip
on_unrepresentable: reject
on_state_error: reject
state:
  ttl: 1h
  max_turns_per_session: 4
  max_reasoning_bytes_per_turn: 1024
  max_session_bytes: 4096
`
	setup := func(t *testing.T) (*reasoningpreservation.AttemptTransform, aipapi.Call) {
		t.Helper()
		cfg := decodeValidConfig(t, restoreYAML)
		store := newMemoryStore(t, reasoningpreservation.StoreOptions{
			TTL: time.Hour, MaxTurnsPerSession: 4, MaxReasoningBytesPerTurn: 1024,
			MaxSessionBytes: 4096, Now: time.Now,
		})
		call, artifacts := missingRestoreFixture(t)
		artifacts[0].CreatedAt = time.Now()
		if _, err := store.Append(context.Background(), reasoningpreservation.NewSessionPartition("sess-unrep"), artifacts[0]); err != nil {
			t.Fatalf("Append: %v", err)
		}
		return reasoningpreservation.NewAttemptTransform(cfg, store), call
	}
	meta := func(policy aipapi.AttemptPolicy) request.AttemptMeta {
		return request.AttemptMeta{
			BackendID: "be", Model: "m",
			ReplaySupport: aipapi.ReasoningReplaySupport{},
			Session:       session.SessionView{AuthoritativeSessionID: "sess-unrep"},
			AttemptPolicy: policy,
		}
	}

	t.Run("log_skip withholds", func(t *testing.T) {
		t.Parallel()
		xform, call := setup(t)
		dec, err := xform.HandleAttempt(context.Background(), &call, meta(aipapi.AttemptLogSkip), request.Services{})
		if err != nil {
			t.Fatalf("HandleAttempt: %v", err)
		}
		if dec.Kind != request.AttemptContinue {
			t.Fatalf("attempt log_skip must withhold instead of excluding, got %+v", dec)
		}
		for _, m := range call.Messages {
			for _, p := range m.Parts {
				if p.Kind == aipapi.PartReasoning {
					t.Fatalf("withheld restore must not mutate the call: %+v", m)
				}
			}
		}
	})

	for _, policy := range []aipapi.AttemptPolicy{aipapi.AttemptReject, ""} {
		t.Run("reject preserves exclude", func(t *testing.T) {
			t.Parallel()
			xform, call := setup(t)
			dec, err := xform.HandleAttempt(context.Background(), &call, meta(policy), request.Services{})
			if err != nil {
				t.Fatalf("HandleAttempt: %v", err)
			}
			if dec.Kind != request.AttemptExcludeCandidate {
				t.Fatalf("policy %q must preserve the existing exclude, got %+v", policy, dec)
			}
		})
	}
}
