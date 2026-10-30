package reasoningpreservation

import (
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// spyRawSource asserts that TextString is not invoked on oversize path.
type spyRawSource struct {
	lenVal      int
	stringCalls int
	str         string
}

func (s *spyRawSource) TextLen() int { return s.lenVal }
func (s *spyRawSource) TextString() string {
	s.stringCalls++
	return s.str
}

func TestExtractBoundedRawFromSource_OversizeDoesNotMaterialize(t *testing.T) {
	t.Parallel()
	spy := &spyRawSource{lenVal: 1024, str: strings.Repeat("a", 1024)}
	// Collected metadata must be valid (finish received, no terminal error) to reach size check.
	var c aipapi.Collected
	c.FinishReceived = true
	// limit 10 < len 1024 => oversize, String must NOT be called.
	_, err := extractBoundedRawFromSource(spy, c, 10)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrRawOversize)
	assert.Equal(t, 0, spy.stringCalls, "oversize path must not call TextString (no payload materialization)")
}

func TestExtractBoundedRawFromSource_OversizeHardCeilingDoesNotMaterialize(t *testing.T) {
	t.Parallel()
	spy := &spyRawSource{lenVal: HardRawOutputCeiling + 1, str: strings.Repeat("a", HardRawOutputCeiling+1)}
	var c aipapi.Collected
	c.FinishReceived = true
	_, err := extractBoundedRawFromSource(spy, c, HardRawOutputCeiling+1000)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrRawOversize)
	assert.Equal(t, 0, spy.stringCalls)
}

func TestExtractBoundedRawFromSource_TerminalErrorRejectedBeforeString(t *testing.T) {
	t.Parallel()
	spy := &spyRawSource{lenVal: 10, str: "hello"}
	var c aipapi.Collected
	c.FinishReceived = true
	c.TerminalError = &aipapi.Event{Kind: aipapi.EventError, ErrorCode: "upstream", ErrorMessage: "boom"}
	_, err := extractBoundedRawFromSource(spy, c, 1024)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrRawInvalidChannel)
	assert.Equal(t, 0, spy.stringCalls, "terminal error must be rejected before TextString")
}

func TestExtractBoundedRawFromSource_FinishNotReceivedRejectedBeforeString(t *testing.T) {
	t.Parallel()
	spy := &spyRawSource{lenVal: 10, str: "hello"}
	var c aipapi.Collected
	c.FinishReceived = false
	_, err := extractBoundedRawFromSource(spy, c, 1024)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrRawInvalidChannel)
	assert.Equal(t, 0, spy.stringCalls)
}

func TestExtractBoundedRawFromSource_ToolChannelRejectedBeforeString(t *testing.T) {
	t.Parallel()
	spy := &spyRawSource{lenVal: 1024, str: strings.Repeat("a", 1024)}
	var c aipapi.Collected
	c.FinishReceived = true
	c.ToolArgs = map[string]*strings.Builder{"a": {}}
	_, err := extractBoundedRawFromSource(spy, c, 10)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrRawInvalidChannel)
	assert.Equal(t, 0, spy.stringCalls)
}

func TestExtractBoundedRawFromSource_ClampToHardCeiling(t *testing.T) {
	t.Parallel()
	// configured limit > hard ceiling should be clamped to hard ceiling
	spy := &spyRawSource{lenVal: HardRawOutputCeiling, str: strings.Repeat("a", HardRawOutputCeiling)}
	var c aipapi.Collected
	c.FinishReceived = true
	raw, err := extractBoundedRawFromSource(spy, c, HardRawOutputCeiling+1000)
	require.NoError(t, err)
	require.Equal(t, HardRawOutputCeiling, len(raw))
	assert.Equal(t, 1, spy.stringCalls, "within clamped limit should materialize once")

	spy2 := &spyRawSource{lenVal: HardRawOutputCeiling + 1, str: strings.Repeat("a", HardRawOutputCeiling+1)}
	_, err = extractBoundedRawFromSource(spy2, c, HardRawOutputCeiling+1000)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrRawOversize)
	assert.Equal(t, 0, spy2.stringCalls)
}
