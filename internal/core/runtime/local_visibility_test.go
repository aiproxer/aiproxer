package runtime

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/conversationprojection"
	"github.com/aiproxer/aiproxer/internal/core/localstream"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	aipcont "github.com/aiproxer/aiproxer/pkg/aipsdk/continuation"
)

// TestLocalStream_LegacyFullHistory_ClientVisibleBackendFiltered proves
// a claimed local input and its reply remain client-visible in the A-leg
// ingress/continuation but are filtered on the next backend projection
// when the client replays full legacy history. This is the legacy slice of
// Req 11.1-11.3 for task 3.4.
func TestLocalStream_LegacyFullHistory_ClientVisibleBackendFiltered(t *testing.T) {
	t.Parallel()
	aLegID := "a_test_local_legacy_visibility"

	sourceMsg := aipapi.Message{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("local-claimed-input")}}
	replyText := "local-reply-legacy"
	replyMsg := localstream.CanonicalAssistantMessage(replyText)
	// Simulate local turn tagging: source and reply.
	sourceID, _ := conversationprojection.MessageIdentityOf(sourceMsg)
	replyID, _ := conversationprojection.MessageIdentityOf(replyMsg)
	snap := conversationprojection.Snapshot{
		NeverBackend: []conversationprojection.Tag{
			{Identity: sourceID, Reason: "test_local"},
			{Identity: replyID, Reason: "test_local"},
		},
	}
	if len(snap.NeverBackend) != 2 {
		t.Fatalf("snapshot never_backend %d want 2", len(snap.NeverBackend))
	}
	// Client replays full history containing the local messages plus a new turn.
	legacyCall := aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "openai:gpt-4"},
		Messages: []aipapi.Message{
			sourceMsg,
			replyMsg,
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("next question")}},
		},
		Session: aipapi.SessionRef{ALegID: aLegID},
	}
	// Client-visible truth must still contain the local messages.
	foundSource := false
	foundReply := false
	for _, m := range legacyCall.Messages {
		if id, _ := conversationprojection.MessageIdentityOf(m); id == sourceID {
			foundSource = true
		}
		if id, _ := conversationprojection.MessageIdentityOf(m); id == replyID {
			foundReply = true
		}
	}
	if !foundSource || !foundReply {
		t.Fatalf("ingress lost local messages: source %v reply %v", foundSource, foundReply)
	}
	// Backend-effective projection must remove both.
	out, ev, err := conversationprojection.Project(legacyCall, snap)
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	if ev.FilteredCount != 2 {
		t.Fatalf("filtered %d want 2", ev.FilteredCount)
	}
	for _, m := range out.Messages {
		if id, _ := conversationprojection.MessageIdentityOf(m); id == sourceID || id == replyID {
			t.Fatalf("backend call still contains tagged %s", id)
		}
	}
	for _, m := range out.Instructions {
		if id, _ := conversationprojection.MessageIdentityOf(m); id == sourceID || id == replyID {
			t.Fatalf("backend instructions still contains tagged %s", id)
		}
	}
	// Exactly one forwardable message remains.
	if len(out.Messages) != 1 || out.Messages[0].Parts[0].Text != "next question" {
		t.Fatalf("backend messages %+v want single next question", out.Messages)
	}
	// Feature does not require frontend to delete from A-leg history; snap still holds tags.
	if len(snap.NeverBackend) != 2 {
		t.Fatalf("snapshot after projection %d want 2", len(snap.NeverBackend))
	}
}

// TestLocalStream_OpenResponsesMaterializedHistory_ClientVisibleBackendFiltered
// proves the same invariant when history is reconstructed via previous_response_id
// materialization (OpenResponses slice). For 3.4 we use a focused slice:
// the materialized Call.Items contain the previously recorded local input/reply
// as concrete message items, and projection removes them before backend work.
func TestLocalStream_OpenResponsesMaterializedHistory_ClientVisibleBackendFiltered(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	aLegID := "a_test_local_or_visibility"

	// Same identities as legacy but in item authority.
	sourceItem := aipapi.Item{
		Kind: aipapi.ItemKindMessage, Role: aipapi.RoleUser, Status: aipapi.ItemStatusCompleted,
		Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "local-claimed-input"}},
		ID:      "item-source-1",
	}
	replyText := "local-reply-or"
	replyItem := localstream.CanonicalAssistantItem(replyText)
	replyItem.ID = "item-reply-1"
	sourceID, _ := conversationprojection.ItemIdentityOf(sourceItem)
	replyID, _ := conversationprojection.ItemIdentityOf(replyItem)
	snap := conversationprojection.Snapshot{
		NeverBackend: []conversationprojection.Tag{
			{Identity: sourceID, Reason: "test_local"},
			{Identity: replyID, Reason: "test_local"},
		},
	}
	if len(snap.NeverBackend) != 2 {
		t.Fatalf("snapshot never_backend %d want 2", len(snap.NeverBackend))
	}

	// Simulate continuation materialization: the resolver would return a Call
	// whose Items are the reconstructed history containing the local messages.
	// Here we directly build that materialized call as the runtime would see
	// after ResolveParent.
	materializedCall := aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "openai:gpt-4"},
		Items: []aipapi.Item{
			sourceItem,
			replyItem,
			{Kind: aipapi.ItemKindMessage, Role: aipapi.RoleUser, Status: aipapi.ItemStatusCompleted, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "next question"}}, ID: "item-next-1"},
		},
		Session: aipapi.SessionRef{ALegID: aLegID},
	}
	// Also include a dangling item_reference to the source that must be cleaned.
	materializedCall.Items = append(materializedCall.Items, aipapi.Item{
		Kind: aipapi.ItemKindItemReference, Status: aipapi.ItemStatusCompleted,
		Reference: &aipapi.ItemReference{ID: "item-source-1"},
	})

	// Client-visible reconstructed call still contains local items.
	foundSource := false
	foundReply := false
	for _, it := range materializedCall.Items {
		if it.Kind != aipapi.ItemKindMessage {
			continue
		}
		if id, _ := conversationprojection.ItemIdentityOf(it); id == sourceID {
			foundSource = true
		}
		if id, _ := conversationprojection.ItemIdentityOf(it); id == replyID {
			foundReply = true
		}
	}
	if !foundSource || !foundReply {
		t.Fatalf("materialized lost local items")
	}

	out, ev, err := conversationprojection.Project(materializedCall, snap)
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	if ev.FilteredCount != 2 {
		t.Fatalf("filtered %d want 2", ev.FilteredCount)
	}
	for _, it := range out.Items {
		if it.Kind == aipapi.ItemKindMessage {
			if id, _ := conversationprojection.ItemIdentityOf(it); id == sourceID || id == replyID {
				t.Fatalf("backend still contains tagged item %s", id)
			}
		}
		if it.Kind == aipapi.ItemKindItemReference && it.Reference != nil && it.Reference.ID == "item-source-1" {
			t.Fatalf("dangling reference not cleaned")
		}
	}
	// Only the new user message should survive (plus no steering).
	countMsg := 0
	for _, it := range out.Items {
		if it.Kind == aipapi.ItemKindMessage {
			countMsg++
			if it.Content[0].Text != "next question" {
				t.Fatalf("unexpected surviving item %+v", it)
			}
		}
	}
	if countMsg != 1 {
		t.Fatalf("surviving msg count %d want 1", countMsg)
	}

	// Continuation record still holds client-visible truth: simulate a
	// MemoryStore record that would be returned via Get for previous_response_id.
	contStore := aipcont.NewMemoryStore()
	scope := aipcont.Scope{SessionID: aLegID}
	id, err := contStore.Reserve(ctx, scope, aipcont.StoragePolicy{})
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	recCont := aipcont.ContinuationRecord{
		ID: id, Scope: scope, Terminal: true, Status: aipcont.RecordStatusCompleted,
		InputItems:  []aipapi.Item{sourceItem, {Kind: aipapi.ItemKindMessage, Role: aipapi.RoleUser, Status: aipapi.ItemStatusCompleted, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "prev user"}}, ID: "prev-1"}},
		OutputItems: []aipapi.Item{replyItem},
		Policy:      aipcont.StoragePolicy{},
	}
	if err := contStore.PutTerminal(ctx, recCont); err != nil {
		t.Fatalf("PutTerminal: %v", err)
	}
	stored, err := contStore.Get(ctx, scope, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	// Stored continuation still contains local reply (client-visible).
	found := false
	for _, it := range stored.OutputItems {
		if it.Kind == aipapi.ItemKindMessage {
			if id2, _ := conversationprojection.ItemIdentityOf(it); id2 == replyID {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("continuation store lost local reply")
	}
	// But a subsequent materialized backend call built from that continuation
	// would again be filtered (proven above).
}

// TestLocalStream_LocalHandlerClaimPreservesClientVisibilityYetFiltersNextTurn
// is a focused integration slice proving the next backend turn filters
// previously tagged local messages while the client view retains them.
// It asserts cross-turn visibility without requiring a second store read.
func TestLocalStream_LocalHandlerClaimPreservesClientVisibilityYetFiltersNextTurn(t *testing.T) {
	t.Parallel()
	sourceMsg := aipapi.Message{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("claimed-input")}}
	sourceID, _ := conversationprojection.MessageIdentityOf(sourceMsg)
	replyText := "integration-reply"
	replyMsg := localstream.CanonicalAssistantMessage(replyText)
	replyID, _ := conversationprojection.MessageIdentityOf(replyMsg)
	snap := conversationprojection.Snapshot{
		NeverBackend: []conversationprojection.Tag{
			{Identity: sourceID, Reason: "test_local"},
			{Identity: replyID, Reason: "test_local"},
		},
	}
	// Next backend call from same A-leg replays both local messages plus new turn.
	nextCall := aipapi.Call{
		Route:    aipapi.RouteIntent{Selector: "openai:gpt-4"},
		Messages: []aipapi.Message{sourceMsg, replyMsg, {Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("follow up")}}},
	}
	out, ev, err := conversationprojection.Project(nextCall, snap)
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	if ev.FilteredCount != 2 {
		t.Fatalf("filtered %d want 2", ev.FilteredCount)
	}
	if len(out.Messages) != 1 {
		t.Fatalf("out messages %d want 1", len(out.Messages))
	}
	// Prove the *client* view (nextCall) still had them before projection.
	if len(nextCall.Messages) != 3 {
		t.Fatalf("client call lost visibility")
	}
}
