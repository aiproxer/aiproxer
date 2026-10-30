package reasoningpreservation_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/reasoningpreservation"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func collectedFromText(t *testing.T, text string) aipapi.Collected {
	t.Helper()
	var c aipapi.Collected
	c.Text.WriteString(text)
	c.FinishReceived = true
	return c
}

func collectedWithToolCall(t *testing.T, text string) aipapi.Collected {
	t.Helper()
	var c aipapi.Collected
	c.Text.WriteString(text)
	c.FinishReceived = true
	c.ToolArgs = map[string]*strings.Builder{"call-1": func() *strings.Builder { b := &strings.Builder{}; b.WriteString("{}"); return b }()}
	c.ToolNames = map[string]string{"call-1": "my_tool"}
	c.ToolCallOrder = []string{"call-1"}
	return c
}

func TestRawExtractor_RejectsToolCallsBeforeSize(t *testing.T) {
	t.Parallel()
	c := collectedWithToolCall(t, `{"schema_version":1,"segments":[]}`)
	_, err := reasoningpreservation.ExtractBoundedRaw(c, 1024)
	require.Error(t, err)
	assert.ErrorIs(t, err, reasoningpreservation.ErrRawInvalidChannel)
}

func TestRawExtractor_OversizeBeforeDecode_TruncatedPrefixInvalid(t *testing.T) {
	t.Parallel()
	// full payload is valid JSON, but truncated prefix under limit is invalid.
	valid := `{"schema_version":1,"segments":[{"index":0,"text":"ok"}]}`
	require.True(t, json.Valid([]byte(valid)))
	max := 10 // intentionally less than len(valid)
	c := collectedFromText(t, valid)
	raw, err := reasoningpreservation.ExtractBoundedRaw(c, max)
	require.Error(t, err)
	assert.ErrorIs(t, err, reasoningpreservation.ErrRawOversize)
	assert.Nil(t, raw)
	// ensure we did not attempt decode: truncated prefix is invalid JSON, but error must be raw_oversize not decode error
	assert.NotContains(t, err.Error(), "invalid character")
	// also prove we did not materialize beyond bound: raw is nil/short
}

func TestRawExtractor_HardCeilingDistinct(t *testing.T) {
	t.Parallel()
	require.Greater(t, reasoningpreservation.HardRawOutputCeiling, 0)
	// configured limit larger than hard ceiling still bounded by hard ceiling
	big := strings.Repeat("a", reasoningpreservation.HardRawOutputCeiling+1)
	c := collectedFromText(t, big)
	_, err := reasoningpreservation.ExtractBoundedRaw(c, reasoningpreservation.HardRawOutputCeiling+1000)
	require.Error(t, err)
	assert.ErrorIs(t, err, reasoningpreservation.ErrRawOversize)
}

func TestRawExtractor_WithinLimitReturnsBytesWithoutDecode(t *testing.T) {
	t.Parallel()
	valid := `{"schema_version":1,"segments":[{"index":0,"text":"compressed"}]}`
	c := collectedFromText(t, valid)
	raw, err := reasoningpreservation.ExtractBoundedRaw(c, 1024)
	require.NoError(t, err)
	require.Equal(t, valid, string(raw))
}

func TestRawExtractor_ExactLimitBoundary(t *testing.T) {
	t.Parallel()
	text := strings.Repeat("x", 100)
	c := collectedFromText(t, text)
	_, err := reasoningpreservation.ExtractBoundedRaw(c, 100)
	require.NoError(t, err)
	c2 := collectedFromText(t, text+"y")
	_, err = reasoningpreservation.ExtractBoundedRaw(c2, 100)
	require.Error(t, err)
	assert.ErrorIs(t, err, reasoningpreservation.ErrRawOversize)
}
