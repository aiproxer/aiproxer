package openresponses

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// compactionCarrierEvent returns the canonical EventItem carrier the generic
// backend emits for a provider compaction item in a compacted ordered window.
func compactionCarrierEvent(id, encrypted string) aipapi.Event {
	return aipapi.Event{
		Kind: aipapi.EventItem,
		Item: &aipapi.Item{
			Kind:   aipapi.ItemKindCompaction,
			ID:     id,
			Status: aipapi.ItemStatusCompleted,
			Compaction: &aipapi.CompactionItem{
				EncryptedContent: encrypted,
			},
		},
	}
}

func TestStateMachine_EventItemPreservesCompactionTrajectory(t *testing.T) {
	t.Parallel()
	sm := NewStateMachine(EnvelopeMetadata{
		ResponseID: "comp_proxy_1",
		CreatedAt:  time.Now(),
		Model:      "model-x",
	}, aipapi.GenerationOptions{})

	for _, ev := range []aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		compactionCarrierEvent("cmp_1", "enc:cmp_1"),
		{Kind: aipapi.EventResponseFinished},
	} {
		if err := aipapi.ValidateEventEnvelope(&ev); err != nil {
			t.Fatalf("event envelope invalid: %v", err)
		}
		if _, err := sm.ProcessCanonicalEvent(ev); err != nil {
			t.Fatalf("state machine rejected %s: %v", ev.Kind, err)
		}
	}
	if sm.State() != StateTerminal {
		t.Fatalf("state = %s, want terminal", sm.State())
	}

	traj := sm.Trajectory()
	if len(traj) != 1 {
		t.Fatalf("trajectory items = %d, want 1 compaction item", len(traj))
	}
	got := traj[0]
	if got.Kind != aipapi.ItemKindCompaction {
		t.Fatalf("trajectory item kind = %q, want compaction", got.Kind)
	}
	if got.ID != "cmp_1" {
		t.Fatalf("trajectory compaction id = %q, want cmp_1", got.ID)
	}
	if got.Compaction == nil || got.Compaction.EncryptedContent != "enc:cmp_1" {
		t.Fatalf("compaction encrypted content lost: %+v", got.Compaction)
	}
	if got.Status != aipapi.ItemStatusCompleted {
		t.Fatalf("carried compaction status = %q, want completed", got.Status)
	}
}

func TestStateMachine_EventItemPreservesCompactionOrder(t *testing.T) {
	t.Parallel()
	sm := NewStateMachine(EnvelopeMetadata{
		ResponseID: "comp_proxy_2",
		CreatedAt:  time.Now(),
		Model:      "model-x",
	}, aipapi.GenerationOptions{})

	for _, ev := range []aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventTextDelta, Delta: "window"},
		compactionCarrierEvent("cmp_2", "enc:cmp_2"),
		{Kind: aipapi.EventResponseFinished},
	} {
		if err := aipapi.ValidateEventEnvelope(&ev); err != nil {
			t.Fatalf("event envelope invalid: %v", err)
		}
		if _, err := sm.ProcessCanonicalEvent(ev); err != nil {
			t.Fatalf("state machine rejected %s: %v", ev.Kind, err)
		}
	}

	traj := sm.Trajectory()
	if len(traj) != 2 {
		t.Fatalf("trajectory items = %d, want 2 (message then compaction)", len(traj))
	}
	if traj[0].Kind != aipapi.ItemKindMessage {
		t.Fatalf("trajectory[0] kind = %q, want message", traj[0].Kind)
	}
	if traj[1].Kind != aipapi.ItemKindCompaction || traj[1].ID != "cmp_2" {
		t.Fatalf("trajectory[1] = %+v, want compaction cmp_2", traj[1])
	}
}

func TestCompactResource_OutputCarriesSchemaValidCompactionItem(t *testing.T) {
	t.Parallel()
	env := EnvelopeMetadata{
		ResponseID: "comp_schema_1",
		CreatedAt:  time.Now(),
		Model:      "model-x",
		Status:     "completed",
	}
	sm := NewStateMachine(env, aipapi.GenerationOptions{})
	for _, ev := range []aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		compactionCarrierEvent("cmp_schema", "enc:cmp_schema"),
		{Kind: aipapi.EventResponseFinished},
	} {
		if _, err := sm.ProcessCanonicalEvent(ev); err != nil {
			t.Fatalf("state machine rejected %s: %v", ev.Kind, err)
		}
	}

	_, resourceJSON, err := BuildCompactResource(env, sm.Trajectory(), UsageStats{})
	if err != nil {
		t.Fatalf("BuildCompactResource failed: %v", err)
	}
	var wire struct {
		Object string `json:"object"`
		Output []struct {
			Type             string `json:"type"`
			ID               string `json:"id"`
			EncryptedContent string `json:"encrypted_content"`
		} `json:"output"`
	}
	if err := json.Unmarshal(resourceJSON, &wire); err != nil {
		t.Fatalf("compact resource JSON invalid: %v", err)
	}
	if wire.Object != "response.compaction" {
		t.Fatalf("object = %q", wire.Object)
	}
	if len(wire.Output) != 1 {
		t.Fatalf("output items = %d, want 1", len(wire.Output))
	}
	item := wire.Output[0]
	if item.Type != "compaction" {
		t.Fatalf("output item type = %q, want compaction", item.Type)
	}
	if item.ID != "cmp_schema" {
		t.Fatalf("output item id = %q", item.ID)
	}
	if item.EncryptedContent != "enc:cmp_schema" {
		t.Fatalf("output encrypted_content = %q, want enc:cmp_schema", item.EncryptedContent)
	}
}

func TestCompactResource_NoContinuationMetadataOnCompactionOutput(t *testing.T) {
	t.Parallel()
	env := EnvelopeMetadata{
		ResponseID:         "comp_newchain_1",
		PreviousResponseID: "",
		CreatedAt:          time.Now(),
		Model:              "model-x",
		Status:             "completed",
	}
	_, resourceJSON, err := BuildCompactResource(env, []aipapi.Item{{
		Kind:   aipapi.ItemKindCompaction,
		ID:     "cmp_nc",
		Status: aipapi.ItemStatusCompleted,
		Compaction: &aipapi.CompactionItem{
			EncryptedContent: "enc:nc",
		},
	}}, UsageStats{})
	if err != nil {
		t.Fatalf("BuildCompactResource failed: %v", err)
	}
	for _, forbidden := range []string{"previous_response_id", `"store"`} {
		if jsonBytesContains(resourceJSON, forbidden) {
			t.Fatalf("compact resource must not carry continuation metadata %q: %s", forbidden, resourceJSON)
		}
	}
}

func jsonBytesContains(b []byte, sub string) bool {
	return len(b) >= len(sub) && func() bool {
		for i := 0; i+len(sub) <= len(b); i++ {
			if string(b[i:i+len(sub)]) == sub {
				return true
			}
		}
		return false
	}()
}
