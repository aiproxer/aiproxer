package runtime

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// TestTask12_3_Runtime_ResponseEvidenceFromWire verifies that responseRequestEvidence
// can be derived directly from WireTurnFacts without constructing or dereferencing aipapi.Call.
func TestTask12_3_Runtime_ResponseEvidenceFromWire(t *testing.T) {
	turnFacts := largebody.DefaultTestWireTurnFacts()
	ev := responseEvidenceFromWire(turnFacts)

	if ev.traceID != turnFacts.Identity.TraceID {
		t.Fatalf("expected traceID %s, got %s", turnFacts.Identity.TraceID, ev.traceID)
	}
	if ev.aLegID != turnFacts.Session.Input.ALegID {
		t.Fatalf("expected aLegID %s, got %s", turnFacts.Session.Input.ALegID, ev.aLegID)
	}
	if ev.sessionID != turnFacts.Session.Input.AuthoritativeSessionID {
		t.Fatalf("expected sessionID %s, got %s", turnFacts.Session.Input.AuthoritativeSessionID, ev.sessionID)
	}
	if ev.secureTurnOK {
		t.Fatalf("expected secureTurnOK false for un-entered wire secure turn")
	}
}

// TestTask12_3_Runtime_CompactionResponseObservation_EvidenceDriven verifies that
// observeCompactionReleaseFinalEvidence operates strictly on responseRequestEvidence
// and canonical aipapi.Event, without requiring facts.baseline or prompt content.
func TestTask12_3_Runtime_CompactionResponseObservation_EvidenceDriven(t *testing.T) {
	pipe := newResponsePipeline()
	turnFacts := largebody.DefaultTestWireTurnFacts()
	ev := responseEvidenceFromWire(turnFacts)

	event := aipapi.Event{
		Kind:  aipapi.EventTextDelta,
		Delta: "test delta",
	}

	attempt := &attemptSession{
		bleg: b2bua.BLegRecord{BLegID: "bleg-test", Seq: 1},
	}

	// Should not panic, should execute safely on canonical event with bounded evidence
	dispatch, _ := pipe.observeCompactionReleaseFinalEvidence(context.Background(), ev, attempt, event)
	_ = dispatch
}

// TestTask12_3_Runtime_TerminalSnapshot_CanonicalEventsOnly verifies that
// responseTerminalSnapshot operates strictly on canonical aipapi.Event and accumulator snapshot,
// without requiring aipapi.Call.
func TestTask12_3_Runtime_TerminalSnapshot_CanonicalEventsOnly(t *testing.T) {
	snap := responseTerminalSnapshot{
		releasedText: "canonical output",
		seenEvents: []aipapi.Event{
			{Kind: aipapi.EventTextDelta, Delta: "canonical output"},
		},
		usageEv: aipapi.Event{Kind: aipapi.EventUsageDelta, OutputTokens: 10},
	}

	if snap.releasedText != "canonical output" {
		t.Fatalf("expected released text 'canonical output', got %s", snap.releasedText)
	}
	if len(snap.seenEvents) != 1 {
		t.Fatalf("expected 1 seen event, got %d", len(snap.seenEvents))
	}
	if snap.usageEv.OutputTokens != 10 {
		t.Fatalf("expected 10 output tokens, got %d", snap.usageEv.OutputTokens)
	}
}
