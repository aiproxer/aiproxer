package archtest

import (
	"testing"
)

// TestFrontendsDoNotImportBackends keeps plugin package zones separate: frontend
// production code must not import internal/plugins/backends (openai-responses-reasoning-
// preservation independent review finding 3). Test packages are excluded via -test=false.
func TestFrontendsDoNotImportBackends(t *testing.T) {
	t.Parallel()
	forbidden := []struct {
		sub, msg string
	}{
		{
			"github.com/aiproxer/aiproxer/internal/plugins/backends",
			"internal/plugins/frontends must not import internal/plugins/backends",
		},
	}
	assertGoListImportsExclude(t, "./internal/plugins/frontends/...", forbidden)
}

// TestOpenResponsesProtocolBoundaryRules verifies core and openresponses boundary rules.
func TestOpenResponsesProtocolBoundaryRules(t *testing.T) {
	t.Parallel()
	assertGoListImportsExclude(t, "./internal/core/...", []struct{ sub, msg string }{
		{
			"github.com/aiproxer/aiproxer/internal/plugins/protocols/openresponses",
			"core must not import protocol wire codecs",
		},
	})
	assertGoListImportsExclude(t, "./internal/plugins/protocols/openresponses/...", []struct{ sub, msg string }{
		{
			"github.com/aiproxer/aiproxer/internal/refclient",
			"openresponses must not import refclient",
		},
		{
			"github.com/aiproxer/aiproxer/internal/refbackend",
			"openresponses must not import refbackend",
		},
		{
			"github.com/openai/openai-go",
			"openresponses must not import openai-go SDK",
		},
		{
			"github.com/anthropics/anthropic-sdk-go",
			"openresponses must not import anthropic-sdk-go",
		},
		{
			"github.com/google/generative-ai-go",
			"openresponses must not import generative-ai-go",
		},
		{
			"github.com/aws/aws-sdk-go-v2",
			"openresponses must not import aws-sdk-go-v2",
		},
	})
}
