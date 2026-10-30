package stdhttp

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/modelregistry"
)

func TestBuildOpenAIModelsList_empty(t *testing.T) {
	t.Parallel()
	got := buildOpenAIModelsList(nil)
	if got.Object != "list" || got.Data == nil || len(got.Data) != 0 {
		t.Fatalf("got=%#v", got)
	}
}

func TestBuildOpenAIModelsList_instancePinnedStableDedup(t *testing.T) {
	t.Parallel()
	got := buildOpenAIModelsList([]modelregistry.BackendModel{
		{CanonicalID: "google/gemini-3.5-flash-high", NativeID: "Gemini High", BackendID: "agycliacp.project", Kind: "agycliacp"},
		{CanonicalID: "openai/gpt-4o", NativeID: "gpt-4o-secret", BackendID: "openai-a", Kind: "openai-responses"},
		{CanonicalID: "google/gemini-3.5-flash-high", NativeID: "Gemini High", BackendID: "agycliacp.other", Kind: "agycliacp"},
		{CanonicalID: "openai/gpt-4o", NativeID: "gpt-4o-dup", BackendID: "openai-a", Kind: "openai-responses"},
	})
	wantIDs := []string{
		"agycliacp.other:google/gemini-3.5-flash-high",
		"agycliacp.project:google/gemini-3.5-flash-high",
		"openai-a:openai/gpt-4o",
	}
	if len(got.Data) != len(wantIDs) {
		t.Fatalf("got=%+v", got.Data)
	}
	for i, id := range wantIDs {
		if got.Data[i].ID != id {
			t.Fatalf("data[%d].ID=%q want %q", i, got.Data[i].ID, id)
		}
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "gpt-4o-secret") || strings.Contains(string(raw), "Gemini High") {
		t.Fatalf("native leak: %s", raw)
	}
}
