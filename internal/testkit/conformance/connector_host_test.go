package conformance

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/refclient/refclienttest"
)

// TestConnectorHostSpecs_CoverTheConnectorColumns locks the connector-host
// metadata table to exactly the optional connector columns the authoritative
// matrix references (ACP, OpenRouter, NVIDIA, the self-contained stub,
// llamacpp/lmstudio/vllm/ollama/huggingface/infomaniak/commandcode-openai
// staged-proof columns, and the azure/cloudflare/databricks/snowflake
// vendor-endpoint columns) and proves every spec points at a
// real optional connector module on disk. The harness never builds or launches
// a connector here; this default-build test only pins the table that the
// integration matrix deployments consume.
func TestConnectorHostSpecs_CoverTheConnectorColumns(t *testing.T) {
	t.Parallel()
	want := []string{BackendACP, BackendOpenRouter, BackendNVIDIA, BackendLocalStub, BackendLlamaCpp, BackendLmstudio, BackendVllm, BackendOllama, BackendHuggingFace, BackendInfomaniak, BackendCommandCodeOpenAI, BackendNousPortal, BackendQwenOAuth, BackendXaiOAuth, BackendAzureOpenAI, BackendCloudflare, BackendDatabricks, BackendSnowflake}
	if got := connectorHostBackendIDs(); !slices.Equal(got, want) {
		t.Fatalf("connector host backend IDs = %v, want %v", got, want)
	}
	root := refclienttest.ModuleRoot(t)
	for _, id := range want {
		spec, ok := connectorHostLookup(id)
		if !ok {
			t.Fatalf("connector column %q has no connector host spec", id)
		}
		if spec.backendID != id {
			t.Fatalf("connector %q spec backendID = %q", id, spec.backendID)
		}
		if strings.TrimSpace(spec.module) == "" || spec.bin == "" || spec.listenMarker == "" || spec.instanceID == "" {
			t.Fatalf("connector %q spec is incomplete: %+v", id, spec)
		}
		mod := filepath.Join(root, filepath.FromSlash(spec.module))
		if _, err := os.Stat(filepath.Join(mod, "go.mod")); err != nil {
			t.Fatalf("connector %q module go.mod not found at %s: %v", id, mod, err)
		}
	}
}

// TestConnectorHost_OptionalConnectorsNotInRootGraph proves the connector-host
// harness does not drag optional connector modules into the root module graph:
// the root go.mod must not require connectors/openrouter or connectors/nvidia
// (the hybrid-backend rule; the harness builds them in their own modules with
// GOWORK=off).
func TestConnectorHost_OptionalConnectorsNotInRootGraph(t *testing.T) {
	t.Parallel()
	root := refclienttest.ModuleRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{BackendOpenRouter, BackendNVIDIA, BackendACP, BackendLocalStub, BackendLlamaCpp, BackendLmstudio, BackendVllm, BackendOllama, BackendHuggingFace, BackendInfomaniak, BackendCommandCodeOpenAI, BackendNousPortal, BackendQwenOAuth, BackendXaiOAuth, BackendAzureOpenAI, BackendCloudflare, BackendDatabricks, BackendSnowflake} {
		spec, _ := connectorHostLookup(id)
		if bytes.Contains(raw, []byte(spec.module)) {
			t.Fatalf("root go.mod requires optional connector module %q; hybrid-backend rule forbids it", spec.module)
		}
	}
}
