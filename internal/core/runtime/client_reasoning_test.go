package runtime

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// Claim table:
// | TestAttemptPolicy_DefaultLogSkipRejectOption | empty->log_skip, reject->reject, unknown fails closed | req 1.3, 1.4 | default reject / accept unknown -> FAIL |
// | TestStripDowngradedReasoningReplay_RecordsSkipOnce | one counter + one content-safe entry per stripped attempt | req 4.1-4.3 | drop record / log bodies -> FAIL |

func TestAttemptPolicy_DefaultLogSkipRejectOption(t *testing.T) {
	t.Parallel()
	ex := TestExecutor()
	if got := ex.attemptPolicy(); got != aipapi.AttemptLogSkip {
		t.Fatalf("unset policy must default to log_skip, got %q", got)
	}
	ex.AttemptPolicy = aipapi.AttemptLogSkip
	if got := ex.attemptPolicy(); got != aipapi.AttemptLogSkip {
		t.Fatalf("explicit log_skip must resolve, got %q", got)
	}
	ex.AttemptPolicy = aipapi.AttemptReject
	if got := ex.attemptPolicy(); got != aipapi.AttemptReject {
		t.Fatalf("explicit reject must resolve, got %q", got)
	}
	ex.AttemptPolicy = "bogus"
	if got := ex.attemptPolicy(); got != aipapi.AttemptReject {
		t.Fatalf("unknown policy must fail closed to reject, got %q", got)
	}
	var nilEx *Executor
	if got := nilEx.attemptPolicy(); got != aipapi.AttemptLogSkip {
		t.Fatalf("nil executor must default to log_skip, got %q", got)
	}
}

// skipRecordingSink is a MetricsSink fake that also observes the optional
// reasoning-replay skip extension.
type skipRecordingSink struct {
	skips   atomic.Int64
	backend atomic.Value
}

func (s *skipRecordingSink) OnAttemptRecorded(aipapi.AttemptOutcome, string) {}
func (s *skipRecordingSink) OnBackendOpenDuration(string, float64)           {}
func (s *skipRecordingSink) OnTransportNegotiation(aipapi.Operation, aipapi.TransportMode, string) {
}
func (s *skipRecordingSink) OnCancellation(CancellationObservation) {}
func (s *skipRecordingSink) OnReasoningReplaySkipped(backend string) {
	s.skips.Add(1)
	s.backend.Store(backend)
}

func TestStripDowngradedReasoningReplay_RecordsSkipOnce(t *testing.T) {
	t.Parallel()
	var logs bytes.Buffer
	ex := TestExecutor()
	ex.Log = slog.New(slog.NewJSONHandler(&logs, nil))
	sink := &skipRecordingSink{}
	ex.Metrics = sink
	cand := routing.AttemptCandidate{Key: "profile:m", Primary: routing.Primary{Backend: "profile", Model: "m"}}
	attempt := clientReasoningHistoryCall("profile:m")
	res := aipapi.NegotiationResult{Kind: aipapi.NegotiationDowngrade, Downgraded: []aipapi.Capability{aipapi.CapabilityReasoningReplay}}
	ex.stripDowngradedReasoningReplay(context.Background(), "trace-1", attempt, cand, res)
	if sink.skips.Load() != 1 {
		t.Fatalf("one stripped attempt must record exactly one skip, got %d", sink.skips.Load())
	}
	if got, _ := sink.backend.Load().(string); got != "profile" {
		t.Fatalf("skip must be labelled by backend, got %q", got)
	}
	for _, m := range attempt.Messages {
		for _, p := range m.Parts {
			if p.Kind == aipapi.PartReasoning {
				t.Fatal("attempt clone must be stripped")
			}
		}
	}
	entry := logs.String()
	if !strings.Contains(entry, "reasoning_replay_skipped") || !strings.Contains(entry, "profile:m") || !strings.Contains(entry, "log_skip") {
		t.Fatalf("decision entry must name candidate and policy: %s", entry)
	}
	for _, secret := range []string{"client-thought", "rs_1"} {
		if strings.Contains(entry, secret) {
			t.Fatalf("decision entry must stay content-safe, leaked %q: %s", secret, entry)
		}
	}
	// A second call on the already-stripped attempt records nothing: no double strip.
	ex.stripDowngradedReasoningReplay(context.Background(), "trace-1", attempt, cand, res)
	if sink.skips.Load() != 1 {
		t.Fatalf("already-stripped attempt must not record again, got %d", sink.skips.Load())
	}
}
