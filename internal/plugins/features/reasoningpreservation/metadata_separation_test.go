package reasoningpreservation_test

import (
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/reasoningpreservation"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMetadataSeparation_ControlPlaneNotInModelMessages(t *testing.T) {
	t.Parallel()
	sanitized := []reasoningpreservation.CompressorInputSegment{
		{Index: 0, Text: "sanitized reasoning A"},
		{Index: 2, Text: "sanitized reasoning B"},
	}
	traceID := "trace-abc-123"
	aLegID := "aleg-xyz-789"
	bLegID := "bleg-456"
	branch := "branch-1"
	route := "compressor-route"
	principal := reasoningpreservation.NewEgressPrincipalView("principal-sensitive-999")
	_ = principal

	req, err := reasoningpreservation.BuildCompressorAuxRequest(reasoningpreservation.CompressorAuxRequestParams{
		Route:               route,
		ParentTraceID:       traceID,
		ParentALegID:        aLegID,
		ParentBLegID:        bLegID,
		ParentBranchBinding: branch,
		Segments:            sanitized,
		MaxOutputTokens:     1024,
	})
	require.NoError(t, err)
	// envelope retains control-plane
	assert.Equal(t, "reasoning_preservation_compressor", req.Role)
	assert.Equal(t, "private", req.Visibility)
	assert.Equal(t, auxiliary.SessionModeDetached, req.SessionMode)
	assert.Equal(t, traceID, req.ParentTraceID)
	assert.Equal(t, aLegID, req.ParentALegID)
	assert.Equal(t, bLegID, req.ParentBLegID)
	assert.Equal(t, branch, req.ParentBranchBinding)
	require.Contains(t, req.DisablePlugins, "reasoning-output-preservation")
	// model content must contain only sanitized segments
	require.NotNil(t, req.Call)
	require.NotEmpty(t, req.Call.Messages)
	var modelBlob strings.Builder
	for _, m := range req.Call.Messages {
		for _, p := range m.Parts {
			modelBlob.WriteString(p.Text)
			modelBlob.WriteString(string(p.Content))
			if p.Reasoning != nil {
				modelBlob.WriteString(p.Reasoning.Text)
			}
		}
		// also check Items if used
		for _, it := range req.Call.Items {
			modelBlob.WriteString(it.ID)
		}
	}
	blob := modelBlob.String()
	// sanitized texts must be present
	assert.Contains(t, blob, "sanitized reasoning A")
	assert.Contains(t, blob, "sanitized reasoning B")
	// control-plane markers must NOT appear in model blob
	for _, marker := range []string{
		"reasoning_preservation_compressor",
		"private",
		traceID,
		aLegID,
		bLegID,
		branch,
		"principal-sensitive",
	} {
		assert.NotContains(t, blob, marker, "control-plane marker %q leaked into Call.Messages", marker)
	}
	// also envelope Call.Messages must not contain role/visibility strings as content
}

func TestMetadataSeparation_EnvelopeRetainsButPromptClean(t *testing.T) {
	t.Parallel()
	segs := []reasoningpreservation.CompressorInputSegment{{Index: 0, Text: "clean"}}
	req, err := reasoningpreservation.BuildCompressorAuxRequest(reasoningpreservation.CompressorAuxRequestParams{
		Route:           "route-x",
		ParentTraceID:   "trace-1",
		ParentALegID:    "aleg-1",
		ParentBLegID:    "bleg-1",
		Segments:        segs,
		MaxOutputTokens: 1024,
	})
	require.NoError(t, err)
	// envelope fields
	assert.Equal(t, "reasoning_preservation_compressor", req.Role)
	// prompt inspection: Call.Messages serialized must not contain envelope values
	// collect all message texts
	var prompt strings.Builder
	for _, m := range req.Call.Messages {
		for _, p := range m.Parts {
			prompt.WriteString(p.Text)
		}
	}
	assert.NotContains(t, prompt.String(), "reasoning_preservation_compressor")
	assert.NotContains(t, prompt.String(), "private")
	assert.NotContains(t, prompt.String(), "trace-1")
	assert.NotContains(t, prompt.String(), "aleg-1")
	// disable list not in prompt
	assert.NotContains(t, prompt.String(), "reasoning-output-preservation")
}
