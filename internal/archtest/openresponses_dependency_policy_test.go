package archtest

import (
	"testing"
)

// TestOpenResponsesForbiddenImportsPolicy verifies that centralized ForbiddenImports rules
// in archtest correctly catch forbidden provider SDKs and reference client/backend imports
// for package internal/plugins/protocols/openresponses.
func TestOpenResponsesForbiddenImportsPolicy(t *testing.T) {
	t.Parallel()

	requiredRules := []struct {
		source string
		target string
	}{
		{"internal/plugins/protocols/openresponses", "github.com/openai/openai-go"},
		{"internal/plugins/protocols/openresponses", "github.com/anthropics/anthropic-sdk-go"},
		{"internal/plugins/protocols/openresponses", "github.com/google/generative-ai-go"},
		{"internal/plugins/protocols/openresponses", "github.com/aws/aws-sdk-go-v2"},
		{"internal/plugins/protocols/openresponses", "/internal/refclient"},
		{"internal/plugins/protocols/openresponses", "/internal/refbackend"},
		{"pkg/aipapi", "/internal/plugins/protocols/openresponses"},
		{"pkg/aipsdk", "/internal/plugins/protocols/openresponses"},
		{"internal/refclient/openresponses", "/internal/plugins/protocols/openresponses"},
		{"internal/refbackend/openresponses", "/internal/plugins/protocols/openresponses"},
		{"internal/plugins/backends/openresponsescompat", "github.com/openai/openai-go"},
	}

	for _, req := range requiredRules {
		found := false
		for _, rule := range ForbiddenImports {
			if rule.SourcePattern == req.source && rule.TargetPattern == req.target {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("ForbiddenImports missing rule for source %q target %q", req.source, req.target)
		}
	}
}

// TestForbiddenImportRuleMatcherMatchesRepresentativeIllegalImports verifies that the rule matcher
// flags representative illegal import strings for openresponses.
func TestForbiddenImportRuleMatcherMatchesRepresentativeIllegalImports(t *testing.T) {
	t.Parallel()

	representativeIllegalImports := []struct {
		pkg    string
		imp    string
		target string
	}{
		{"internal/plugins/protocols/openresponses", "github.com/openai/openai-go", "github.com/openai/openai-go"},
		{"internal/plugins/protocols/openresponses", "github.com/anthropics/anthropic-sdk-go", "github.com/anthropics/anthropic-sdk-go"},
		{"internal/plugins/protocols/openresponses", "github.com/google/generative-ai-go", "github.com/google/generative-ai-go"},
		{"internal/plugins/protocols/openresponses", "github.com/aws/aws-sdk-go-v2", "github.com/aws/aws-sdk-go-v2"},
		{"internal/plugins/protocols/openresponses", "github.com/aiproxer/aiproxer/internal/refclient", "/internal/refclient"},
		{"internal/plugins/protocols/openresponses", "github.com/aiproxer/aiproxer/internal/refbackend", "/internal/refbackend"},
		{"pkg/aipapi", "github.com/aiproxer/aiproxer/internal/plugins/protocols/openresponses/codec", "/internal/plugins/protocols/openresponses"},
		{"pkg/aipsdk", "github.com/aiproxer/aiproxer/internal/plugins/protocols/openresponses/codec", "/internal/plugins/protocols/openresponses"},
	}

	for _, tc := range representativeIllegalImports {
		matched := false
		for _, rule := range ForbiddenImports {
			if rule.SourcePattern == tc.pkg && matchImportTarget(tc.imp, rule.TargetPattern) {
				matched = true
				break
			}
		}
		if !matched {
			t.Errorf("rule matcher failed to match illegal import %q for package %q", tc.imp, tc.pkg)
		}
	}
}
