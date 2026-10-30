package acp_test

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/aiproxer/aiproxer/connectors/acp/internal/service"
	"github.com/aiproxer/aiproxer/connectors/acp/internal/testemu"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

// The ACP usage object carries the counters but does not define whether the
// input counter contains the cached and the rewritten prompt subsets, and
// shipping agents disagree (agent-client-protocol#1860). The generic
// connector therefore does not assert a relation: an agent that reports an
// inclusive input keeps the prompt it reported.
func TestUsageInputSemantics_defaultKeepsInclusiveAgent(t *testing.T) {
	t.Parallel()
	usage := publishUsage(t, "")
	if usage.InputTokensExcludeCacheRead || usage.InputTokensExcludeCacheWrite {
		t.Fatalf("usage evidence = %+v", usage)
	}
	if usage.InputTokens == nil || *usage.InputTokens != 120 ||
		usage.OutputTokens == nil || *usage.OutputTokens != 45 ||
		usage.CacheReadTokens == nil || *usage.CacheReadTokens != 80 ||
		usage.TotalTokens == nil || *usage.TotalTokens != 165 {
		t.Fatalf("usage counters changed: %+v", usage)
	}
}

// An operator whose agent reports the exclusive shape declares it, and the
// declaration travels with the counters to the host.
func TestUsageInputSemantics_operatorOptInDeclaresRelation(t *testing.T) {
	t.Parallel()
	usage := publishUsage(t, "usage_input_excludes_cache: true\n")
	if !usage.InputTokensExcludeCacheRead || !usage.InputTokensExcludeCacheWrite {
		t.Fatalf("usage evidence = %+v", usage)
	}
	if usage.InputTokens == nil || *usage.InputTokens != 120 ||
		usage.CacheReadTokens == nil || *usage.CacheReadTokens != 80 {
		t.Fatalf("usage counters changed: %+v", usage)
	}
}

// publishUsage drives the connector's execute path against the module-local
// ACP emulator and returns the usage evidence it published.
func publishUsage(t *testing.T, extraConfig string) *backendplugin.UsageEvidence {
	t.Helper()
	srv := httptest.NewServer(testemu.NewHandler())
	t.Cleanup(srv.Close)
	svc := service.New()
	inst, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		InstanceID:  "usage-1",
		ConfigYAML:  []byte("base_url: " + srv.URL + "\n" + extraConfig),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = inst.Close(context.Background()) })

	text := "hi"
	inv := backendplugin.Invocation{
		RequestID: "r", AttemptID: "a", ALegID: "a", BLegID: "b",
		CanonicalModelID: "acp/agent",
		Messages: []backendplugin.Message{{
			Role:  backendplugin.RoleUser,
			Parts: []backendplugin.Part{{Kind: backendplugin.PartKindText, Text: &text}},
		}},
		Options: backendplugin.GenerationOptions{ResponseSchemaJSON: backendplugin.RawJSONAbsentValue()},
	}
	ms := &memStream{ctx: context.Background(), inbox: []backendplugin.ClientFrame{{
		Kind: backendplugin.ClientFrameStart, InstanceID: "usage-1", Invocation: &inv,
	}}}
	if err := inst.Execute(ms); err != nil {
		t.Fatal(err)
	}
	for _, fr := range ms.outbox {
		if fr.Kind == backendplugin.ServerFrameEvent && fr.Event != nil && fr.Event.Kind == backendplugin.EventUsageDelta {
			return fr.Event.Usage
		}
	}
	t.Fatalf("no usage event published: %+v", ms.outbox)
	return nil
}
