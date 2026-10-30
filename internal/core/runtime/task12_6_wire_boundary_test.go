package runtime

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTask12_6_Runtime_WireResponseEvidence_CallFree verifies that
// responseEvidenceFromWire derives responseRequestEvidence purely from bounded
// WireTurnFacts without accepting, dereferencing, or retaining aipapi.Call.
func TestTask12_6_Runtime_WireResponseEvidence_CallFree(t *testing.T) {
	facts := largebody.DefaultTestWireTurnFacts()

	ev := responseEvidenceFromWire(facts)

	assert.Equal(t, facts.Identity.TraceID, ev.traceID)
	assert.Equal(t, facts.Session.Input.ALegID, ev.aLegID)
	assert.Equal(t, facts.Session.Input.AuthoritativeSessionID, ev.sessionID)
	assert.False(t, ev.secureTurnOK, "wire response evidence must not report active canonical secure turn")
	assert.Empty(t, ev.secureTurn.TurnID, "secure turn must remain zero on initial wire response evidence")
}

// TestTask12_6_Runtime_WireEvidence_FallbackAndIsolation verifies fallback
// behavior when bounded session identifiers are empty: falls back to RequestID,
// never resurrects from context or Call.
func TestTask12_6_Runtime_WireEvidence_FallbackAndIsolation(t *testing.T) {
	facts := largebody.DefaultTestWireTurnFacts()
	facts.Session.Input.AuthoritativeSessionID = ""
	facts.Session.Input.ALegID = ""

	ev := responseEvidenceFromWire(facts)

	assert.Equal(t, facts.Identity.RequestID, ev.sessionID, "empty sessionID must fall back to requestID")
	assert.Equal(t, facts.Identity.RequestID, ev.aLegID, "empty aLegID must fall back to requestID")
	assert.Equal(t, facts.Identity.TraceID, ev.traceID)
	assert.False(t, ev.secureTurnOK)
}

// TestTask12_6_Runtime_NoShadowCall_OnWireResponseFacts verifies that
// WireTurnFacts transforms to ResponseFacts without mirroring aipapi.Call.
func TestTask12_6_Runtime_NoShadowCall_OnWireResponseFacts(t *testing.T) {
	facts := largebody.DefaultTestWireTurnFacts()
	respFacts := facts.ToResponseFacts("gpt-5-turbo")

	require.Equal(t, "gpt-5-turbo", respFacts.EffectiveModel)
	require.Equal(t, facts.Identity.RequestID, respFacts.RequestID)
	require.Equal(t, facts.Session.Input.AuthoritativeSessionID, respFacts.SessionID)
	require.Equal(t, facts.Source.BodyBytes, respFacts.BodyBytes)
}
