package archtest

import (
	"testing"
)

// TestLargeBodyDoesNotImportProvidersOrFrontends keeps the core large-body
// DTO seam provider-neutral (Task 2.4, Requirements 1/22, design sections
// 3/6/8-11/13): internal/core/largebody production code must not import
// concrete frontend/backend adapters, protocol wire codecs, or provider SDKs.
// Test packages are excluded via -test=false.
func TestLargeBodyDoesNotImportProvidersOrFrontends(t *testing.T) {
	t.Parallel()
	forbidden := []struct {
		sub, msg string
	}{
		{
			"github.com/aiproxer/aiproxer/internal/plugins/frontends",
			"internal/core/largebody must not import frontend adapters",
		},
		{
			"github.com/aiproxer/aiproxer/internal/plugins/backends",
			"internal/core/largebody must not import backend adapters",
		},
		{
			"github.com/aiproxer/aiproxer/internal/plugins/features",
			"internal/core/largebody must not import feature plugins",
		},
		{
			"github.com/aiproxer/aiproxer/internal/plugins/protocols",
			"internal/core/largebody must not import protocol wire codecs",
		},
		{
			"github.com/openai/openai-go",
			"internal/core/largebody must not import openai-go SDK",
		},
		{
			"github.com/anthropics/anthropic-sdk-go",
			"internal/core/largebody must not import anthropic-sdk-go",
		},
		{
			"github.com/google/generative-ai-go",
			"internal/core/largebody must not import generative-ai-go",
		},
		{
			"github.com/aws/aws-sdk-go-v2",
			"internal/core/largebody must not import aws-sdk-go-v2",
		},
	}
	assertGoListImportsExclude(t, "./internal/core/largebody/...", forbidden)
}
