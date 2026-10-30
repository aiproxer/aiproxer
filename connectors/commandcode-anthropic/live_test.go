//go:build integration

// Live provider probe for the Anthropic-compatible commandcode endpoint.
//
// Prerequisites (external topology, never part of the default hermetic path):
//   - COMMANDCODE_API_KEY for a provider account with quota
//   - network access to the configured provider base URL
//   - remaining provider quota for the probed model
//
// Run it explicitly with: make test-live-connectors
package commandcodeanthropic_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/connectors/commandcode-anthropic/internal/anthropic"
	"github.com/aiproxer/aiproxer/connectors/commandcode-anthropic/internal/service"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

func TestLive_CommandCodeAnthropic(t *testing.T) {
	t.Parallel()
	key := strings.TrimSpace(os.Getenv("COMMANDCODE_API_KEY"))
	if key == "" {
		t.Skip("COMMANDCODE_API_KEY not set; skipping live probe")
	}

	svc := service.New()
	req := backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		InstanceID:  "live-test-anthropic",
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
	var claudeCount int
	for _, m := range listed.Models {
		if strings.Contains(strings.ToLower(m.CanonicalModelID), "claude") {
			claudeCount++
		}
	}
	t.Logf("Live CommandCode Anthropic models found: %d total, %d Claude models", len(listed.Models), claudeCount)

	// 2. Live messages call to CommandCode Anthropic endpoint:
	// Proves endpoint contact and protocol classification. Depending on the plan
	// of the credential (GOAT vs Provider), CommandCode returns either 200 or 403 MODEL_NOT_IN_PLAN.
	cfg, err := service.ParseConfigYAML([]byte("base_url: https://api.commandcode.ai/provider/v1\n"))
	if err != nil {
		t.Fatal(err)
	}
	hc, err := cfg.HTTPClient()
	if err != nil {
		t.Fatal(err)
	}
	cl := anthropic.NewClient(cfg.BaseURL, key, hc)

	call := aipapi.Call{
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: "Say hello"}}},
		},
		Invocation: aipapi.Invocation{
			Operation:     aipapi.Operation("anthropic.messages"),
			DeliveryMode:  aipapi.DeliveryModeNonStreaming,
			TransportMode: aipapi.TransportModeNonStreaming,
		},
	}

	es, err := cl.Open(ctx, call, "claude-haiku-4-5-20251001")
	if err != nil {
		if he, ok := errors.AsType[*anthropic.HTTPError](err); ok {
			t.Logf("Live CommandCode Anthropic endpoint responded with expected HTTP error: status=%d type=%q message=%q", he.StatusCode, he.Type, he.Message)
		} else {
			t.Fatalf("Live Open failed with unexpected error: %v", err)
		}
	} else {
		defer func() { _ = es.Close() }()
		ev, err := es.Recv(ctx)
		t.Logf("Live CommandCode Anthropic response event: %+v err: %v", ev, err)
	}
}
