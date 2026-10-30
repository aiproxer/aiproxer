package extensions_test

import (
	"context"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
)

type recordingSecretGuardMetrics struct {
	decisions   int
	matches     int
	quarantines int
	failures    int
	scanLimits  int
	lastAction  string
	lastOutcome string
	lastCat     string
	matchCats   []string
}

func (m *recordingSecretGuardMetrics) IncDecision(action, outcome, sourceCategory string) {
	m.decisions++
	m.lastAction, m.lastOutcome, m.lastCat = action, outcome, sourceCategory
}

func (m *recordingSecretGuardMetrics) IncMatch(action, outcome, sourceCategory string) {
	m.matches++
	m.matchCats = append(m.matchCats, sourceCategory)
}

func (m *recordingSecretGuardMetrics) IncQuarantine(action, outcome, sourceCategory string) {
	m.quarantines++
}

func (m *recordingSecretGuardMetrics) IncFailure(action, outcome, sourceCategory string) {
	m.failures++
}

func (m *recordingSecretGuardMetrics) IncScanLimit(action, outcome, sourceCategory string) {
	m.scanLimits++
}

func TestRunSecretGuardStage_emitsBoundedDecisionMetrics(t *testing.T) {
	t.Parallel()
	call := validCall()
	m := &recordingSecretGuardMetrics{}
	block, err := extensions.RunSecretGuardStage(t.Context(), nil, nil, []secretguard.Guard{
		sgGuard{
			id: "blocker",
			decision: secretguard.Decision{
				Outcome: secretguard.OutcomeBlock,
				Findings: []secretguard.Finding{{
					SecretRefName:   "OPENAI_API_KEY",
					SourceCategory:  secretguard.SourceCategoryProxyEnv,
					Location:        "messages[0].parts[0].text",
					OccurrenceCount: 1,
				}},
			},
		},
	}, &call, secretguard.Meta{}, secretguard.Services{}, nil, m)
	if err != nil {
		t.Fatalf("block decision must not return stage error: %v", err)
	}
	if block == nil {
		t.Fatal("expected block result")
	}
	if m.decisions != 1 || m.matches != 1 {
		t.Fatalf("decisions=%d matches=%d", m.decisions, m.matches)
	}
	if m.lastAction != "block" || m.lastOutcome != "block" || m.lastCat != "proxy_env" {
		t.Fatalf("labels action=%q outcome=%q cat=%q", m.lastAction, m.lastOutcome, m.lastCat)
	}
}

func TestRunSecretGuardStage_logScanLimit_emitsScanLimitMetricAndAudit(t *testing.T) {
	t.Parallel()
	call := validCall()
	m := &recordingSecretGuardMetrics{}
	var got secretguard.DecisionEvent
	obs := secretguard.ObserverFunc(func(_ context.Context, ev secretguard.DecisionEvent) error {
		got = ev
		return nil
	})
	audit := &extensions.SecretGuardAudit{
		Observer:      obs,
		AccessMode:    "single_user",
		ConfigVersion: "v-test",
		TurnID:        "turn-sl",
		Now:           func() time.Time { return time.Unix(2000, 0).UTC() },
	}
	_, err := extensions.RunSecretGuardStage(t.Context(), nil, nil, []secretguard.Guard{
		sgGuard{
			id: "logger",
			decision: secretguard.Decision{
				Outcome:       secretguard.OutcomeLog,
				ScanLimitHit:  true,
				FailureKind:   "scan_limit",
				FailureReason: "scan_max_bytes exceeded",
			},
		},
	}, &call, secretguard.Meta{TraceID: "tr-sl", FrontendID: "openai-responses"}, secretguard.Services{}, audit, m)
	if err != nil {
		t.Fatal(err)
	}
	if m.scanLimits != 1 {
		t.Fatalf("scanLimits=%d want 1", m.scanLimits)
	}
	if m.decisions != 1 {
		t.Fatalf("decisions=%d want 1", m.decisions)
	}
	if !got.ScanLimitHit {
		t.Fatal("audit event must set ScanLimitHit")
	}
	if got.Outcome != secretguard.OutcomeLog {
		t.Fatalf("outcome=%q want log", got.Outcome)
	}
}

func TestRunSecretGuardStage_multiCategoryMetrics_mixedDecisionAndPerCategoryMatch(t *testing.T) {
	t.Parallel()
	call := validCall()
	m := &recordingSecretGuardMetrics{}
	block, err := extensions.RunSecretGuardStage(t.Context(), nil, nil, []secretguard.Guard{
		sgGuard{
			id: "blocker",
			decision: secretguard.Decision{
				Outcome: secretguard.OutcomeBlock,
				Findings: []secretguard.Finding{
					{SecretRefName: "OPENAI_API_KEY", SourceCategory: secretguard.SourceCategoryProxyEnv, Location: "messages[0].parts[0].text", OccurrenceCount: 1},
					{SecretRefName: "GITHUB_TOKEN", SourceCategory: secretguard.SourceCategoryPopularEnv, Location: "messages[0].parts[0].text", OccurrenceCount: 1},
				},
			},
		},
	}, &call, secretguard.Meta{}, secretguard.Services{}, nil, m)
	if err != nil {
		t.Fatalf("block decision must not return stage error: %v", err)
	}
	if block == nil {
		t.Fatal("expected block result")
	}
	if m.lastCat != "mixed" {
		t.Fatalf("decision source_category=%q want mixed", m.lastCat)
	}
	if m.matches != 2 {
		t.Fatalf("matches=%d want 2 (one per distinct category)", m.matches)
	}
	seen := map[string]bool{}
	for _, c := range m.matchCats {
		seen[c] = true
	}
	if !seen["proxy_env"] || !seen["popular_env"] {
		t.Fatalf("match categories=%v want proxy_env and popular_env", m.matchCats)
	}
}
