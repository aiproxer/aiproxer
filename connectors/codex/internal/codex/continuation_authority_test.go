package codex

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestWSContinuation_UsesIDOnlyForExactIncrementalExtension(t *testing.T) {
	t.Parallel()
	store := newWSContinuationStore(0, 8)
	call := aipapi.Call{Session: aipapi.SessionRef{AuthoritativeSessionID: "session-1"}}
	base := Payload{Model: "model", PromptCacheKey: "cache", Input: []inputItem{textMessageItem{Type: "message", Role: "user", Content: "first"}}}
	output := textMessageItem{Type: "message", Role: "assistant", Content: "answer"}
	store.record(nil, call, base, "response-1", output)

	incremental := base
	incremental.Input = append(cloneInputItems(base.Input), output, textMessageItem{Type: "message", Role: "user", Content: "next"})
	if !store.prepare(context.Background(), nil, call, &incremental) {
		t.Fatal("exact incremental extension did not use continuation")
	}
	if incremental.PreviousResponseID != "response-1" || len(incremental.Input) != 1 {
		t.Fatalf("continuation payload = %+v", incremental)
	}

	drift := base
	drift.Input = []inputItem{textMessageItem{Type: "message", Role: "user", Content: "edited"}}
	if store.prepare(context.Background(), nil, call, &drift) {
		t.Fatal("edited history used a response ID")
	}
	if drift.PreviousResponseID != "" || len(drift.Input) != 1 {
		t.Fatalf("drift payload = %+v", drift)
	}
}

func TestWSContinuation_CheckpointCommitInvalidatesOldBaseline(t *testing.T) {
	t.Parallel()
	store := newWSContinuationStore(0, 8)
	call := aipapi.Call{Session: aipapi.SessionRef{AuthoritativeSessionID: "session-1"}}
	payload := Payload{Model: "model", PromptCacheKey: "cache", Input: []inputItem{textMessageItem{Type: "message", Role: "user", Content: "first"}}}
	store.record(nil, call, payload, "old-response")
	store.invalidateLineage(nil, call, &payload)

	next := payload
	next.Input = append(cloneInputItems(payload.Input), textMessageItem{Type: "message", Role: "user", Content: "next"})
	if store.prepare(context.Background(), nil, call, &next) {
		t.Fatal("first request after checkpoint reused old response ID")
	}
	if next.PreviousResponseID != "" || len(next.Input) != 2 {
		t.Fatalf("post-checkpoint payload = %+v", next)
	}
}

func TestWSContinuation_SuccessEstablishesNewBaselineAndHTTPHasNoChain(t *testing.T) {
	t.Parallel()
	store := newWSContinuationStore(0, 8)
	call := aipapi.Call{Session: aipapi.SessionRef{AuthoritativeSessionID: "session-1"}}
	payload := Payload{Model: "model", PromptCacheKey: "cache", Input: []inputItem{textMessageItem{Type: "message", Role: "user", Content: "turn"}}}
	store.record(nil, call, payload, "new-response")
	if got := payload.PreviousResponseID; got != "" {
		t.Fatalf("record mutated HTTP-like payload: %q", got)
	}
	next := payload
	next.Input = append(cloneInputItems(payload.Input), textMessageItem{Type: "message", Role: "user", Content: "next"})
	if !store.prepare(context.Background(), nil, call, &next) || next.PreviousResponseID != "new-response" {
		t.Fatalf("new successful baseline was not usable: %+v", next)
	}

	otherTurn := call
	otherTurn.Session.AuthoritativeSessionID = "session-2"
	other := payload
	other.Input = append(cloneInputItems(payload.Input), textMessageItem{Type: "message", Role: "user", Content: "next"})
	if store.prepare(context.Background(), nil, otherTurn, &other) {
		t.Fatal("continuation crossed logical turns")
	}
}

func TestWSContinuation_ClientOnlySessionHintCannotPartitionResponseID(t *testing.T) {
	t.Parallel()
	store := newWSContinuationStore(0, 8)
	call := aipapi.Call{Session: aipapi.SessionRef{ContinuityKey: "client-hint"}}
	payload := Payload{Model: "model", PromptCacheKey: "cache", Input: []inputItem{textMessageItem{Type: "message", Role: "user", Content: "turn"}}}
	store.record(nil, call, payload, "response-1")
	next := payload
	next.Input = append(cloneInputItems(payload.Input), textMessageItem{Type: "message", Role: "user", Content: "next"})
	if store.prepare(context.Background(), nil, call, &next) {
		t.Fatal("client-only session hint enabled response-id continuation")
	}
}
