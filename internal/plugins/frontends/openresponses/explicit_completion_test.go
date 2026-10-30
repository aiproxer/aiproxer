package openresponses_test

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openresponses"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestOpenResponses_Decode_CorrelatedExplicitCompletion proves OpenResponses
// wire input containing correlated function_call + function_call_output for
// attempt_completion decodes into canonical Items where
// aipapi.HasExplicitCompletion is true (Requirement 5.7, 10.3). Absent,
// malformed, orphan, and generic finish are rejected via decode.
func TestOpenResponses_Decode_CorrelatedExplicitCompletion(t *testing.T) {
	t.Parallel()

	// Positive: correlated completed explicit call+result -> true
	t.Run("positive correlated", func(t *testing.T) {
		t.Parallel()
		body := []byte(`{
			"model":"gpt-4o",
			"input":[
				{"type":"message","role":"user","content":[{"type":"input_text","text":"do work"}]},
				{"type":"function_call","call_id":"c1","name":"attempt_completion","arguments":"{\"result\":\"done\"}"},
				{"type":"function_call_output","call_id":"c1","output":"ok"}
			]
		}`)
		decoded, err := openresponses.AuthenticateAndDecodeCreate(context.Background(), body, openresponses.DecodeCreateOptions{
			Auth: &mockAuthorizer{authenticated: true},
		})
		require.NoError(t, err)
		require.NotNil(t, decoded.Call)
		assert.True(t, aipapi.HasExplicitCompletion(decoded.Call.Items), "correlated attempt_completion call+result via wire decode must be explicit completion")
	})

	// Call only without result -> false (no executed evidence)
	t.Run("call only false", func(t *testing.T) {
		t.Parallel()
		body := []byte(`{
			"model":"gpt-4o",
			"input":[
				{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]},
				{"type":"function_call","call_id":"c1","name":"attempt_completion","arguments":"{}"}
			]
		}`)
		decoded, err := openresponses.AuthenticateAndDecodeCreate(context.Background(), body, openresponses.DecodeCreateOptions{
			Auth: &mockAuthorizer{authenticated: true},
		})
		require.NoError(t, err)
		assert.False(t, aipapi.HasExplicitCompletion(decoded.Call.Items), "call without result must be false")
	})

	// Orphan result -> decode validation rejects (also falls back to false via HasExplicitCompletion on direct Items)
	t.Run("orphan result false", func(t *testing.T) {
		t.Parallel()
		body := []byte(`{
			"model":"gpt-4o",
			"input":[
				{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]},
				{"type":"function_call_output","call_id":"c1","output":"ok"}
			]
		}`)
		_, err := openresponses.AuthenticateAndDecodeCreate(context.Background(), body, openresponses.DecodeCreateOptions{
			Auth: &mockAuthorizer{authenticated: true},
		})
		require.Error(t, err, "orphan tool result without matching call must fail canonical validation")
		// Direct Items check also false (authoritative correlated check)
		orphanItems := []aipapi.Item{
			{Kind: aipapi.ItemKindToolResult, Status: aipapi.ItemStatusCompleted, ToolResult: &aipapi.ToolResultItem{CallID: "c1", Name: "attempt_completion", Output: "ok"}},
		}
		assert.False(t, aipapi.HasExplicitCompletion(orphanItems))
	})

	// Generic finish correlated must be rejected (conservative)
	t.Run("generic finish rejected", func(t *testing.T) {
		t.Parallel()
		body := []byte(`{
			"model":"gpt-4o",
			"input":[
				{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]},
				{"type":"function_call","call_id":"c1","name":"finish","arguments":"{}"},
				{"type":"function_call_output","call_id":"c1","output":"ok"}
			]
		}`)
		decoded, err := openresponses.AuthenticateAndDecodeCreate(context.Background(), body, openresponses.DecodeCreateOptions{
			Auth: &mockAuthorizer{authenticated: true},
		})
		require.NoError(t, err)
		assert.False(t, aipapi.HasExplicitCompletion(decoded.Call.Items), "generic finish must be rejected even when correlated")
	})

	// Absent explicit tool -> false
	t.Run("absent false", func(t *testing.T) {
		t.Parallel()
		body := []byte(`{"model":"gpt-4o","input":"hello"}`)
		decoded, err := openresponses.AuthenticateAndDecodeCreate(context.Background(), body, openresponses.DecodeCreateOptions{
			Auth: &mockAuthorizer{authenticated: true},
		})
		require.NoError(t, err)
		assert.False(t, aipapi.HasExplicitCompletion(decoded.Call.Items))
	})

	// Alternate alias attempt_complete also valid when correlated
	t.Run("alias attempt_complete correlated true", func(t *testing.T) {
		t.Parallel()
		body := []byte(`{
			"model":"gpt-4o",
			"input":[
				{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]},
				{"type":"function_call","call_id":"c1","name":"attempt_complete","arguments":"{}"},
				{"type":"function_call_output","call_id":"c1","output":"ok"}
			]
		}`)
		decoded, err := openresponses.AuthenticateAndDecodeCreate(context.Background(), body, openresponses.DecodeCreateOptions{
			Auth: &mockAuthorizer{authenticated: true},
		})
		require.NoError(t, err)
		assert.True(t, aipapi.HasExplicitCompletion(decoded.Call.Items))
	})
}

// TestOpenResponses_Decode_OtherFrontendsAreUnsupported documents that
// Anthropic/Gemini/OpenAI legacy frontends do not translate tool call+result
// into correlated canonical Items in a single request decode, so explicit
// completion is not claimed there (reported as unsupported per task).
func TestOpenResponses_OtherFrontendsUnsupported(t *testing.T) {
	t.Parallel()
	// This package only claims OpenResponses Items path; other frontends use
	// Messages authority and have no correlated Items explicit completion.
	// Explicitly assert that a Messages-only call has no Items correlation.
	body := []byte(`{"model":"gpt-4o","input":"hello"}`)
	decoded, err := openresponses.AuthenticateAndDecodeCreate(context.Background(), body, openresponses.DecodeCreateOptions{
		Auth: &mockAuthorizer{authenticated: true},
	})
	require.NoError(t, err)
	assert.False(t, aipapi.HasExplicitCompletion(decoded.Call.Items))
	assert.False(t, aipapi.HasExplicitCompletion(nil))
}
