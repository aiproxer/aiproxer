package openresponses

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestCompactResource_Build(t *testing.T) {
	t.Parallel()
	env := EnvelopeMetadata{
		ResponseID: "comp_123",
		CreatedAt:  time.Now(),
		Model:      "gpt-4o",
		Status:     "completed",
	}

	trajectory := []aipapi.Item{
		{
			ID:     "comp_item_1",
			Kind:   aipapi.ItemKindCompaction,
			Status: aipapi.ItemStatusCompleted,
			Compaction: &aipapi.CompactionItem{
				EncapsulatedID: "enc_100",
				Dialect:        "standard",
				Implementor:    "proxy",
			},
		},
	}

	usage := UsageStats{
		InputTokens:  100,
		OutputTokens: 10,
		TotalTokens:  110,
	}

	wireCompact, jsonBytes, err := BuildCompactResource(env, trajectory, usage)
	if err != nil {
		t.Fatalf("BuildCompactResource failed: %v", err)
	}

	if wireCompact.ID != env.ResponseID {
		t.Fatalf("expected ID %q, got %q", env.ResponseID, wireCompact.ID)
	}
	if wireCompact.Object != "response.compaction" {
		t.Fatalf("expected object response.compaction, got %q", wireCompact.Object)
	}
	if len(wireCompact.Output) != 1 {
		t.Fatalf("expected 1 output item, got %d", len(wireCompact.Output))
	}

	var rawMap map[string]any
	if err := json.Unmarshal(jsonBytes, &rawMap); err != nil {
		t.Fatalf("failed to unmarshal compact JSON: %v", err)
	}

	if rawMap["object"] != "response.compaction" {
		t.Fatalf("unexpected object in JSON: %v", rawMap["object"])
	}
}

func TestCompactResource_BuildFailures(t *testing.T) {
	t.Parallel()
	env := EnvelopeMetadata{
		ResponseID: "", // empty ID
		CreatedAt:  time.Now(),
		Model:      "gpt-4o",
	}

	_, _, err := BuildCompactResource(env, nil, UsageStats{})
	if err == nil {
		t.Fatalf("expected error for empty response ID, got nil")
	}
}
