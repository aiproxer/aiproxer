package openresponses

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/testkit/conformance"
)

// TestCompact_FullPathReturnsSchemaValidCompactionWindow drives the exact
// pinned compact full path through the independent deployment
// (independent client -> compact frontend -> core -> generic backend ->
// independent refbackend -> compact parser -> frontend BuildCompactResource)
// and asserts the returned response.compaction resource carries a reusable
// ordered window with a schema-valid compaction item (pinned
// encrypted_content) and no continuation id / store metadata.
func TestCompact_FullPathReturnsSchemaValidCompactionWindow(t *testing.T) {
	t.Parallel()
	const model = "gpt-4o-mini"

	refHandler := complianceOriginScripts(t, model)
	d := conformance.Deploy(t, conformance.DeploymentSpec{
		Frontend:      conformance.FrontendOpenResponses,
		Backend:       conformance.BackendOpenResponses,
		Model:         model,
		OriginHandler: refHandler,
	})
	if d == nil {
		t.Fatal("Deploy(frontend=openresponses, backend=openresponses) failed")
	}
	defer func() { _ = d.Close() }()

	body, err := json.Marshal(map[string]any{
		"model":            model,
		"prompt_cache_key": "openresponses-compact-test",
		"input": []map[string]any{
			{"type": "message", "role": "user", "content": "We agreed to launch on Tuesday and notify support first."},
			{"type": "message", "role": "assistant", "content": "Understood. The launch is Tuesday, with support notified beforehand."},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
		d.Server.URL+"/openresponses/v1/responses/compact", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := d.Server.Client().Do(req)
	if err != nil {
		t.Fatalf("compact request: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("compact status = %d: %s", resp.StatusCode, raw)
	}

	var resource struct {
		Object string `json:"object"`
		ID     string `json:"id"`
		Status string `json:"status"`
		Model  string `json:"model"`
		Output []struct {
			Type             string `json:"type"`
			ID               string `json:"id"`
			EncryptedContent string `json:"encrypted_content"`
		} `json:"output"`
		Usage struct {
			TotalTokens int `json:"total_tokens"`
		} `json:"usage"`
		PreviousResponseID *string `json:"previous_response_id"`
		Store              *bool   `json:"store"`
	}
	if err := json.Unmarshal(raw, &resource); err != nil {
		t.Fatalf("compact resource JSON invalid: %v\n%s", err, raw)
	}
	if resource.Object != "response.compaction" {
		t.Fatalf("object = %q, want response.compaction", resource.Object)
	}
	if len(resource.Output) == 0 {
		t.Fatalf("compact resource has no output items: %s", raw)
	}
	var hasCompaction bool
	for _, item := range resource.Output {
		if item.Type != "compaction" {
			t.Fatalf("output item type = %q, want compaction (ordered window preserved): %s", item.Type, raw)
		}
		if item.ID == "" {
			t.Fatalf("compaction item id is empty: %s", raw)
		}
		if item.EncryptedContent == "" {
			t.Fatalf("compaction item missing pinned encrypted_content: %s", raw)
		}
		if item.ID == "compaction_compliance" {
			hasCompaction = true
		}
	}
	if !hasCompaction {
		t.Fatalf("refbackend compaction item (compaction_compliance) not preserved in output: %s", raw)
	}
	if strings.Contains(string(raw), `"previous_response_id"`) {
		t.Fatalf("compact resource must not carry continuation id: %s", raw)
	}
	if strings.Contains(string(raw), `"store"`) {
		t.Fatalf("compact resource must not carry store metadata: %s", raw)
	}
}
