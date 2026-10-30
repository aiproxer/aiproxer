package openairesponses

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func effortCall() aipapi.Call { return aipapi.Call{} }

func effortCand(model string) routing.AttemptCandidate {
	return routing.AttemptCandidate{Primary: routing.Primary{Backend: ID, Model: model}, Key: ID + ":" + model}
}

func TestResolveEffortSupportOfficialTable(t *testing.T) {
	t.Parallel()
	be := New(Config{BaseURL: DefaultBaseURL, APIKey: "test"})
	if be.ResolveEffortSupport == nil {
		t.Fatalf("official backend must publish ResolveEffortSupport")
	}
	if got := be.ResolveEffortSupport(context.Background(), effortCall(), effortCand("gpt-6-astra"), "high"); got != execbackend.EffortSupportSupported {
		t.Fatalf("high must be supported, got %d", got)
	}
	for _, value := range []string{"low", "medium", "xhigh", "max"} {
		if got := be.ResolveEffortSupport(context.Background(), effortCall(), effortCand("gpt-6-astra"), value); got != execbackend.EffortSupportSupported {
			t.Fatalf("%s must be supported, got %d", value, got)
		}
	}
	if got := be.ResolveEffortSupport(context.Background(), effortCall(), effortCand("gpt-6-astra"), "none"); got != execbackend.EffortSupportUnsupported {
		t.Fatalf("none must be unsupported (exhaustive), got %d", got)
	}
	if got := be.ResolveEffortSupport(context.Background(), effortCall(), effortCand("gpt-6-astra"), "HIGH"); got != execbackend.EffortSupportUnsupported {
		t.Fatalf("case mismatch with exhaustive must be unsupported, got %d", got)
	}
}

func TestResolveEffortSupportUnknownPaths(t *testing.T) {
	t.Parallel()
	be := New(Config{BaseURL: DefaultBaseURL, APIKey: "test"})
	for _, tc := range []struct {
		name   string
		model  string
		effort string
	}{
		{"unknown model", "gpt-6-unknown", "high"},
		{"alias", "openai/gpt-6-astra", "high"},
		{"prefix inference", "gpt-6-astra-2", "high"},
		{"empty model", "", "high"},
		{"empty effort", "gpt-6-astra", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := be.ResolveEffortSupport(context.Background(), effortCall(), effortCand(tc.model), tc.effort); got != execbackend.EffortSupportUnknown {
				t.Fatalf("expected unknown, got %d", got)
			}
		})
	}
	// Compatible third-party endpoint is not evidence of OpenAI support.
	compat := New(Config{BaseURL: "https://compat.example.com/v1", APIKey: "test"})
	if compat.ResolveEffortSupport == nil {
		t.Fatalf("callback shape stays bound; answer is unknown")
	}
	if got := compat.ResolveEffortSupport(context.Background(), effortCall(), effortCand("gpt-6-astra"), "high"); got != execbackend.EffortSupportUnknown {
		t.Fatalf("compatible endpoint must be unknown, got %d", got)
	}
	// Trailing slash on the official endpoint is still official.
	slash := New(Config{BaseURL: DefaultBaseURL + "/", APIKey: "test"})
	if got := slash.ResolveEffortSupport(context.Background(), effortCall(), effortCand("gpt-6-astra"), "high"); got != execbackend.EffortSupportSupported {
		t.Fatalf("official endpoint with trailing slash must stay supported, got %d", got)
	}
}
