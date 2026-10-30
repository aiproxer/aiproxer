//go:build integration

// Live provider probe for the OpenAI-compatible commandcode endpoint.
//
// Prerequisites (external topology, never part of the default hermetic path):
//   - COMMANDCODE_API_KEY for a provider account with quota
//   - network access to the configured provider base URL
//   - remaining provider quota for the probed model
//
// Run it explicitly with: make test-live-connectors
package commandcodeopenai_test

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/connector-support/openaicompat"
	"github.com/aiproxer/aiproxer/connectors/commandcode-openai/internal/service"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

func TestLive_CommandCodeOpenAI(t *testing.T) {
	t.Parallel()
	key := strings.TrimSpace(os.Getenv("COMMANDCODE_API_KEY"))
	if key == "" {
		t.Skip("COMMANDCODE_API_KEY not set; skipping live probe")
	}

	svc := service.New()
	req := backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		InstanceID:  "live-test",
		ConfigYAML:  []byte("base_url: https://api.commandcode.ai/provider/v1\n"),
		Secrets: backendplugin.SecretBundle{
			Values: map[string][]byte{"api_key": []byte(key)},
		},
		Negotiation:   backendplugin.Negotiation{Compatible: true},
		RuntimePolicy: backendplugin.RuntimePolicy{DisableTransportRetries: true},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	inst, err := svc.Configure(ctx, req)
	if err != nil {
		t.Fatalf("Configure failed: %v", err)
	}

	// 1. Live model discovery
	listed, err := inst.ListModels(ctx, 100)
	if err != nil {
		t.Fatalf("ListModels failed: %v", err)
	}
	if len(listed.Models) == 0 {
		t.Fatalf("expected at least 1 model in catalog, got 0")
	}
	var foundQwen bool
	for _, m := range listed.Models {
		if strings.Contains(m.CanonicalModelID, "Qwen3.7-Flash") || strings.Contains(m.NativeModelID, "Qwen3.7-Flash") {
			foundQwen = true
			break
		}
	}
	if !foundQwen {
		t.Logf("Note: Qwen3.7-Flash not found by exact string in %d models", len(listed.Models))
	}

	// 2. Live chat completions streaming with Qwen/Qwen3.7-Flash
	cfg, err := service.ParseConfigYAML([]byte("base_url: https://api.commandcode.ai/provider/v1\n"))
	if err != nil {
		t.Fatal(err)
	}
	hc, err := cfg.HTTPClient()
	if err != nil {
		t.Fatal(err)
	}
	cl := service.NewCompatClient(cfg, hc, key, service.ProviderHooks())
	call := aipapi.Call{
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: "Respond with the single word PONG"}}},
		},
		Invocation: aipapi.Invocation{
			Operation:     aipapi.OperationOpenAIChatCompletions,
			DeliveryMode:  aipapi.DeliveryModeStreaming,
			TransportMode: aipapi.TransportModeStreaming,
		},
	}

	es, err := cl.Open(ctx, call, "Qwen/Qwen3.7-Flash", openaicompat.FlavorChat)
	if err != nil {
		t.Fatalf("Live Open failed: %v", err)
	}
	defer func() { _ = es.Close() }()

	var textBuilder strings.Builder
	var sawUsage bool
	for {
		ev, err := es.Recv(ctx)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Live Recv error: %v", err)
		}
		if ev.Kind == aipapi.EventTextDelta {
			textBuilder.WriteString(ev.Delta)
		}
		if ev.Kind == aipapi.EventUsageDelta {
			sawUsage = true
		}
	}

	respText := strings.TrimSpace(textBuilder.String())
	t.Logf("Live Qwen3.7-Flash response: %q (usage reported: %v)", respText, sawUsage)
	if respText == "" {
		t.Fatalf("expected non-empty response text from Qwen3.7-Flash")
	}
}
