package reasoningpreservation_test

import (
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/reasoningpreservation"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These RED tests exercise the hardened compressor request contract.
// Before hardening they fail (missing validation / swallowed marshal / no-tools / route/output bounds).
func TestCompressorRequest_Hardened_RouteRequired(t *testing.T) {
	t.Parallel()
	_, err := reasoningpreservation.BuildCompressorAuxRequest(reasoningpreservation.CompressorAuxRequestParams{
		Route:           "   ",
		Segments:        []reasoningpreservation.CompressorInputSegment{{Index: 0, Text: "hello"}},
		MaxOutputTokens: 100,
	})
	require.Error(t, err, "empty/whitespace route must be rejected")
}

func TestCompressorRequest_Hardened_EmptySegments(t *testing.T) {
	t.Parallel()
	_, err := reasoningpreservation.BuildCompressorAuxRequest(reasoningpreservation.CompressorAuxRequestParams{
		Route:           "compressor-route",
		Segments:        nil,
		MaxOutputTokens: 100,
	})
	require.Error(t, err)
	_, err = reasoningpreservation.BuildCompressorAuxRequest(reasoningpreservation.CompressorAuxRequestParams{
		Route:           "compressor-route",
		Segments:        []reasoningpreservation.CompressorInputSegment{},
		MaxOutputTokens: 100,
	})
	require.Error(t, err)
}

func TestCompressorRequest_Hardened_InvalidDuplicateIndexes(t *testing.T) {
	t.Parallel()
	_, err := reasoningpreservation.BuildCompressorAuxRequest(reasoningpreservation.CompressorAuxRequestParams{
		Route:           "r",
		Segments:        []reasoningpreservation.CompressorInputSegment{{Index: -1, Text: "a"}},
		MaxOutputTokens: 100,
	})
	require.Error(t, err, "negative index must be rejected")
	_, err = reasoningpreservation.BuildCompressorAuxRequest(reasoningpreservation.CompressorAuxRequestParams{
		Route: "r",
		Segments: []reasoningpreservation.CompressorInputSegment{
			{Index: 0, Text: "a"},
			{Index: 0, Text: "b"},
		},
		MaxOutputTokens: 100,
	})
	require.Error(t, err, "duplicate index must be rejected")
}

func TestCompressorRequest_Hardened_EmptyTextRejected(t *testing.T) {
	t.Parallel()
	_, err := reasoningpreservation.BuildCompressorAuxRequest(reasoningpreservation.CompressorAuxRequestParams{
		Route:           "r",
		Segments:        []reasoningpreservation.CompressorInputSegment{{Index: 0, Text: ""}},
		MaxOutputTokens: 100,
	})
	require.Error(t, err)
	_, err = reasoningpreservation.BuildCompressorAuxRequest(reasoningpreservation.CompressorAuxRequestParams{
		Route:           "r",
		Segments:        []reasoningpreservation.CompressorInputSegment{{Index: 0, Text: "   "}},
		MaxOutputTokens: 100,
	})
	require.Error(t, err)
}

func TestCompressorRequest_Hardened_OutputTokenBound(t *testing.T) {
	t.Parallel()
	_, err := reasoningpreservation.BuildCompressorAuxRequest(reasoningpreservation.CompressorAuxRequestParams{
		Route:           "r",
		Segments:        []reasoningpreservation.CompressorInputSegment{{Index: 0, Text: "a"}},
		MaxOutputTokens: 0,
	})
	require.Error(t, err, "zero max_output_tokens must be rejected")
	_, err = reasoningpreservation.BuildCompressorAuxRequest(reasoningpreservation.CompressorAuxRequestParams{
		Route:           "r",
		Segments:        []reasoningpreservation.CompressorInputSegment{{Index: 0, Text: "a"}},
		MaxOutputTokens: reasoningpreservation.HardCompressionMaxOutputTokens + 1,
	})
	require.Error(t, err, "exceeding hard ceiling must be rejected")
}

func TestCompressorRequest_Hardened_NoTools(t *testing.T) {
	t.Parallel()
	req, err := reasoningpreservation.BuildCompressorAuxRequest(reasoningpreservation.CompressorAuxRequestParams{
		Route:           "r",
		Segments:        []reasoningpreservation.CompressorInputSegment{{Index: 0, Text: "hello"}},
		MaxOutputTokens: 100,
	})
	require.NoError(t, err)
	require.NotNil(t, req.Call)
	assert.Empty(t, req.Call.Tools, "compressor must have no tools")
	assert.Equal(t, "none", string(req.Call.ToolChoice.Mode))
}

func TestCompressorRequest_Hardened_PrivateDetachedEnvelope(t *testing.T) {
	t.Parallel()
	req, err := reasoningpreservation.BuildCompressorAuxRequest(reasoningpreservation.CompressorAuxRequestParams{
		Route:               "r",
		ParentTraceID:       "trace-1",
		ParentALegID:        "aleg-1",
		ParentBLegID:        "bleg-1",
		ParentBranchBinding: "branch-1",
		Segments:            []reasoningpreservation.CompressorInputSegment{{Index: 0, Text: "hello"}},
		MaxOutputTokens:     100,
	})
	require.NoError(t, err)
	assert.Equal(t, "reasoning_preservation_compressor", req.Role)
	assert.Equal(t, "private", req.Visibility)
	assert.Equal(t, auxiliary.SessionModeDetached, req.SessionMode)
	assert.Equal(t, "trace-1", req.ParentTraceID)
	assert.Equal(t, "aleg-1", req.ParentALegID)
	assert.Equal(t, "bleg-1", req.ParentBLegID)
	assert.Equal(t, "branch-1", req.ParentBranchBinding)
	require.Contains(t, req.DisablePlugins, "reasoning-output-preservation")
	assert.Equal(t, "r", req.Call.Route.Selector)
	require.NotNil(t, req.Call.Options.MaxOutputTokens)
	assert.Equal(t, 100, *req.Call.Options.MaxOutputTokens)
}

func TestCompressorRequest_Hardened_OneCallMultiSegment(t *testing.T) {
	t.Parallel()
	segs := []reasoningpreservation.CompressorInputSegment{
		{Index: 0, Text: "first"},
		{Index: 2, Text: "second"},
		{Index: 5, Text: "third"},
	}
	req, err := reasoningpreservation.BuildCompressorAuxRequest(reasoningpreservation.CompressorAuxRequestParams{
		Route:           "r",
		Segments:        segs,
		MaxOutputTokens: 200,
	})
	require.NoError(t, err)
	require.NotNil(t, req.Call)
	require.Len(t, req.Call.Messages, 2)
	// user payload must contain all segments exactly once and index matches
	var payload strings.Builder
	for _, m := range req.Call.Messages {
		for _, p := range m.Parts {
			payload.WriteString(p.Text)
		}
	}
	for _, s := range segs {
		assert.Contains(t, payload.String(), s.Text)
	}
	// ensure ONE call contains multiple segments: check JSON quoted payload contains 3 segments
	assert.Contains(t, payload.String(), `"index":0`)
	assert.Contains(t, payload.String(), `"index":2`)
	assert.Contains(t, payload.String(), `"index":5`)
}

func TestCompressorRequest_Hardened_PromptEnvelopeSeparation(t *testing.T) {
	t.Parallel()
	trace := "trace-sensitive-123"
	aleg := "aleg-sensitive-456"
	bleg := "bleg-sensitive-789"
	branch := "branch-sensitive-xyz"
	segs := []reasoningpreservation.CompressorInputSegment{{Index: 0, Text: "sanitized hello"}}
	req, err := reasoningpreservation.BuildCompressorAuxRequest(reasoningpreservation.CompressorAuxRequestParams{
		Route:               "r",
		ParentTraceID:       trace,
		ParentALegID:        aleg,
		ParentBLegID:        bleg,
		ParentBranchBinding: branch,
		Segments:            segs,
		MaxOutputTokens:     100,
	})
	require.NoError(t, err)
	var modelBlob strings.Builder
	for _, m := range req.Call.Messages {
		for _, p := range m.Parts {
			modelBlob.WriteString(p.Text)
			modelBlob.WriteString(string(p.Content))
		}
	}
	// sanitized text must be present as quoted JSON
	assert.Contains(t, modelBlob.String(), "sanitized hello")
	// versioned schema must be present
	assert.Contains(t, modelBlob.String(), "schema_version")
	// fixed instruction must be present
	assert.Contains(t, modelBlob.String(), reasoningpreservation.CompressorSystemPrompt)
	// output schema must be present
	assert.Contains(t, modelBlob.String(), reasoningpreservation.CompressorOutputSchema)
	// untrusted wrapper must be present
	assert.Contains(t, modelBlob.String(), "<untrusted-compression-input>")
	assert.Contains(t, modelBlob.String(), "</untrusted-compression-input>")
	// control-plane must NOT leak into model blob
	for _, leak := range []string{trace, aleg, bleg, branch, "reasoning_preservation_compressor", "private"} {
		assert.NotContains(t, modelBlob.String(), leak)
	}
	// also ensure principal not leaked: we don't put principal in params, but envelope should not copy it
	assert.NotContains(t, modelBlob.String(), "principal")
}

func TestCompressorRequest_Hardened_MarshalAndValidationErrorPaths(t *testing.T) {
	t.Parallel()
	// Invalid UTF-8 should be rejected before marshal swallowing
	invalidUTF8 := string([]byte{0xff, 0xfe, 0xfd})
	_, err := reasoningpreservation.BuildCompressorAuxRequest(reasoningpreservation.CompressorAuxRequestParams{
		Route:           "r",
		Segments:        []reasoningpreservation.CompressorInputSegment{{Index: 0, Text: invalidUTF8}},
		MaxOutputTokens: 100,
	})
	require.Error(t, err)

	// Canonical Call validation: route selector exceeding limit must be rejected via Call.Validate
	hugeRoute := strings.Repeat("x", 70000)
	_, err = reasoningpreservation.BuildCompressorAuxRequest(reasoningpreservation.CompressorAuxRequestParams{
		Route:           hugeRoute,
		Segments:        []reasoningpreservation.CompressorInputSegment{{Index: 0, Text: "hello"}},
		MaxOutputTokens: 100,
	})
	require.Error(t, err)

	// Control character text (null byte) should be rejected as unsanitized
	_, err = reasoningpreservation.BuildCompressorAuxRequest(reasoningpreservation.CompressorAuxRequestParams{
		Route:           "r",
		Segments:        []reasoningpreservation.CompressorInputSegment{{Index: 0, Text: "hello\x00world"}},
		MaxOutputTokens: 100,
	})
	require.Error(t, err)
}
