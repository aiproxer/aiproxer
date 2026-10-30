package runtime

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execctx"
	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/compaction"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
)

type spyCompactionDetector struct {
	capturedMeta compaction.PreservationMeta
}

func (d *spyCompactionDetector) PreviewRequest(compaction.PreservationMeta, aipapi.Call) compaction.RequestPreview {
	return compaction.RequestPreview{}
}

func (d *spyCompactionDetector) RequestOpened(compaction.PreservationMeta, aipapi.Call) []compaction.Event {
	return nil
}

func (d *spyCompactionDetector) PreviewResponse(meta compaction.PreservationMeta, ev aipapi.Event) compaction.ResponsePreview {
	d.capturedMeta = meta
	return compaction.ResponsePreview{}
}

func (d *spyCompactionDetector) ResponseReleased(compaction.PreservationMeta, aipapi.Event) []compaction.Event {
	return nil
}

// TestItem2_SessionID_PlumbingAlignment_AcrossConstructors verifies Item 2:
// When wire facts has an authoritative session ID and empty canonical session:
//  1. All three evidence constructors (requestTerminalFacts.responseEvidence,
//     recvTurnFacts.responseEvidence, responseEvidenceFromWire) produce identical
//     session IDs ("sess-wire-auth-42").
//  2. emitTrafficPTCFinal uses the unified evidence constructor so that compaction
//     observations receive the authoritative session ID instead of empty string.
func TestItem2_SessionID_PlumbingAlignment_AcrossConstructors(t *testing.T) {
	authSessID := "sess-wire-auth-42"
	reqID := "req-item2-wire"
	traceID := "trace-item2-wire"

	wireFacts := largebody.DefaultTestWireTurnFacts()
	wireFacts.Session.Input.AuthoritativeSessionID = authSessID
	wireFacts.Identity.RequestID = reqID
	wireFacts.Identity.TraceID = traceID

	wp := &wireAttemptPayload{
		turnFacts: wireFacts,
		sessionID: authSessID,
		requestID: reqID,
	}

	recvFacts := recvTurnFacts{
		traceID:     traceID,
		aLegID:      "aleg-item2",
		wirePayload: wp,
	}

	reqFacts := requestTerminalFacts{
		traceID:     traceID,
		aLegID:      "aleg-item2",
		wirePayload: wp,
		call:        aipapi.Call{}, // empty canonical session
	}

	ev1 := reqFacts.responseEvidence()
	ev2 := recvFacts.responseEvidence()
	ev3 := responseEvidenceFromWire(wireFacts)

	// Pin identical session IDs across all three constructors
	assert.Equal(t, authSessID, ev1.sessionID, "reqFacts.responseEvidence must resolve sessionID from wirePayload")
	assert.Equal(t, authSessID, ev2.sessionID, "recvFacts.responseEvidence must resolve sessionID from wirePayload")
	assert.Equal(t, authSessID, ev3.sessionID, "responseEvidenceFromWire must resolve sessionID from WireTurnFacts")
	assert.Equal(t, ev1.sessionID, ev2.sessionID, "constructors 1 and 2 must agree on session ID")
	assert.Equal(t, ev2.sessionID, ev3.sessionID, "constructors 2 and 3 must agree on session ID")

	// Verify emitTrafficPTCFinal plumbing:
	// Prior to unifying emitTrafficPTCFinal on recvFacts.responseEvidence(),
	// emitTrafficPTCFinal built evidence from facts.baseline.Session.AuthoritativeSessionID,
	// which was empty on wire requests, causing compaction metadata to lose the session ID.
	detector := &spyCompactionDetector{}
	pipe := newResponsePipeline()
	pipe.detector = detector

	attempt := &attemptSession{
		bleg: b2bua.BLegRecord{BLegID: "bleg-item2", Seq: 1},
		cand: routing.AttemptCandidate{Primary: routing.Primary{Backend: "b1", Model: "m1"}},
	}
	ev := aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "chunk"}
	pm := sdkhooks.PartMeta{}

	_, _ = pipe.emitTrafficPTCFinal(context.Background(), recvFacts, attempt, ev, pm)
	require.Equal(t, authSessID, detector.capturedMeta.SessionID,
		"emitTrafficPTCFinal must propagate authoritative wire session ID into compaction metadata")
}

// TestItem2_SessionID_FallbackCannotReachRecorder verifies that:
//  1. responseEvidenceFromWire fallback (RequestID when session is empty) has secureTurnOK=false
//     and can NEVER trigger RecordPostHookStreamEvent.
//  2. Even when recording client-facing events, RecordPostHookStreamEvent receives the real
//     secureTurn.SessionID and never any fallback ID.
func TestItem2_SessionID_FallbackCannotReachRecorder(t *testing.T) {
	reqID := "req-item2-fallback"
	traceID := "trace-item2-fallback"

	wireFacts := largebody.DefaultTestWireTurnFacts()
	wireFacts.Session.Input.AuthoritativeSessionID = ""
	wireFacts.Identity.RequestID = reqID
	wireFacts.Identity.TraceID = traceID

	// When authoritative session ID is empty, responseEvidenceFromWire falls back to RequestID
	evFromWire := responseEvidenceFromWire(wireFacts)
	assert.Equal(t, reqID, evFromWire.sessionID, "responseEvidenceFromWire falls back to RequestID for sessionID")
	assert.False(t, evFromWire.secureTurnOK, "secureTurnOK must remain false on responseEvidenceFromWire")

	spy := &item5RecordingSpy{}
	pipe := newResponsePipeline()
	pipe.secureSessionRecorder = spy
	pipe.secureRecordingMandatory = true

	attempt := &attemptSession{
		bleg: b2bua.BLegRecord{BLegID: "bleg-item2", Seq: 1},
		cand: routing.AttemptCandidate{Primary: routing.Primary{Backend: "b1", Model: "m1"}},
	}
	ev := aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "chunk"}

	// 1. Passing evFromWire to recordClientFacingEvidence must be skipped (0 recorder calls)
	res := pipe.recordClientFacingEvidence(context.Background(), evFromWire, attempt, ev, false)
	assert.Equal(t, responseRecordingSkipped, res.outcome)
	assert.Equal(t, 0, spy.calls(), "recorder must never be called when secureTurnOK is false")

	// 2. Active secure turn with distinct session ID:
	// Verify that the recorder receives secureTurn.SessionID ("real-secure-sess"), NEVER evidence.sessionID ("req-item2-fallback")
	evFromWire.secureTurnOK = true
	evFromWire.secureTurn = execctx.SecureSessionTurn{
		SessionID: "real-secure-sess",
		TurnID:    "turn-1",
	}

	res = pipe.recordClientFacingEvidence(context.Background(), evFromWire, attempt, ev, false)
	assert.Equal(t, responseRecordingRecorded, res.outcome)
	require.Equal(t, 1, spy.calls())

	recorded := spy.recordedEvents()
	require.Len(t, recorded, 1)
	assert.Equal(t, "real-secure-sess", string(recorded[0].SessionID), "recorder must receive real secureTurn.SessionID")
	assert.NotEqual(t, reqID, string(recorded[0].SessionID), "recorder must NEVER receive fallback RequestID as sessionID")
}
