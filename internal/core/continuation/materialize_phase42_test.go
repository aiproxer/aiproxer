package continuation_test

import (
	"context"
	"errors"
	"testing"
	"time"

	corecont "github.com/aiproxer/aiproxer/internal/core/continuation"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	aipcont "github.com/aiproxer/aiproxer/pkg/aipsdk/continuation"
)

func phase42Item(id string, role aipapi.Role) aipapi.Item {
	return aipapi.Item{ID: id, Kind: aipapi.ItemKindMessage, Role: role, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: id}}}
}

func TestMaterializeExactTrajectoryOrderAndIsolation(t *testing.T) {
	t.Parallel()
	store := corecont.NewMemoryStore()
	scope := aipcont.Scope{PrincipalID: "p", SessionID: "s"}
	ctx := context.Background()
	reserve := func(previous aipcont.ResponseID, input, output []aipapi.Item) aipcont.ResponseID {
		id, err := store.Reserve(ctx, scope, aipcont.StoragePolicy{TTL: time.Hour})
		if err != nil {
			t.Fatal(err)
		}
		if err := store.PutTerminal(ctx, aipcont.ContinuationRecord{
			ID: id, Scope: scope, PreviousID: previous, InputItems: input, OutputItems: output,
			Terminal: true, ProfileID: "p", Lineage: aipcont.Lineage{ProfileID: "p"},
		}); err != nil {
			t.Fatal(err)
		}
		return id
	}
	first := reserve("", []aipapi.Item{phase42Item("in-1", aipapi.RoleUser)}, []aipapi.Item{phase42Item("out-1", aipapi.RoleAssistant)})
	second := reserve(first, []aipapi.Item{phase42Item("in-2", aipapi.RoleUser)}, []aipapi.Item{phase42Item("out-2", aipapi.RoleAssistant)})
	newInput := []aipapi.Item{phase42Item("in-3", aipapi.RoleUser)}
	got, err := aipcont.Materialize(ctx, aipcont.MaterializeInput{Store: store, Scope: scope, StartID: second, NewInput: newInput, Bounds: aipcont.Bounds{MaxChainDepth: 4, MaxMaterializedItems: 10, MaxMaterializedBytes: 1 << 20}})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"in-1", "out-1", "in-2", "out-2", "in-3"}
	if len(got.Items) != len(want) {
		t.Fatalf("items=%d want %d", len(got.Items), len(want))
	}
	for i, id := range want {
		if got.Items[i].ID != id {
			t.Fatalf("item[%d]=%q want %q", i, got.Items[i].ID, id)
		}
	}
	got.Items[0].ID = "mutated"
	newInput[0].ID = "mutated-input"
	again, err := aipcont.Materialize(ctx, aipcont.MaterializeInput{Store: store, Scope: scope, StartID: second, NewInput: []aipapi.Item{phase42Item("in-3", aipapi.RoleUser)}, Bounds: aipcont.Bounds{MaxChainDepth: 4, MaxMaterializedItems: 10, MaxMaterializedBytes: 1 << 20}})
	if err != nil || again.Items[0].ID != "in-1" {
		t.Fatalf("materialized state aliased: first=%q err=%v", again.Items[0].ID, err)
	}

	call, _, err := corecont.MaterializeCall(ctx, aipcont.MaterializeInput{Store: store, Scope: scope, StartID: second, NewInput: []aipapi.Item{phase42Item("in-3", aipapi.RoleUser)}, Bounds: aipcont.Bounds{MaxChainDepth: 4, MaxMaterializedItems: 10, MaxMaterializedBytes: 1 << 20}}, aipapi.Call{Session: aipapi.SessionRef{ClientSessionID: "client-only"}})
	if err != nil || call.Session.ClientSessionID != "" || len(call.Items) != 5 {
		t.Fatalf("call materialization leaked client state: call=%+v err=%v", call, err)
	}
}

func TestMaterializeItemBound(t *testing.T) {
	t.Parallel()
	store := corecont.NewMemoryStore()
	scope := aipcont.Scope{PrincipalID: "p", SessionID: "s"}
	id, err := store.Reserve(context.Background(), scope, aipcont.StoragePolicy{TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutTerminal(context.Background(), aipcont.ContinuationRecord{ID: id, Scope: scope, Terminal: true, InputItems: []aipapi.Item{phase42Item("in", aipapi.RoleUser)}, OutputItems: []aipapi.Item{phase42Item("out", aipapi.RoleAssistant)}}); err != nil {
		t.Fatal(err)
	}
	_, err = aipcont.Materialize(context.Background(), aipcont.MaterializeInput{Store: store, Scope: scope, StartID: id, NewInput: []aipapi.Item{phase42Item("new", aipapi.RoleUser)}, Bounds: aipcont.Bounds{MaxChainDepth: 2, MaxMaterializedItems: 2}})
	if !errors.Is(err, aipcont.ErrMaterializedItemsExceeded) {
		t.Fatalf("err=%v", err)
	}
}

func TestMaterializeCarriesNewInputRequirementsAndNativeLineage(t *testing.T) {
	t.Parallel()
	store := corecont.NewMemoryStore()
	scope := aipcont.Scope{PrincipalID: "p", SessionID: "s"}
	id, err := store.Reserve(context.Background(), scope, aipcont.StoragePolicy{TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	record := aipcont.ContinuationRecord{
		ID: id, Scope: scope, Terminal: true,
		InputItems:         []aipapi.Item{phase42Item("old", aipapi.RoleUser)},
		NativeRequirements: []aipcont.NativeRequirement{{BackendID: "provider-a", Model: "model-a", Kind: "reasoning", Dialect: "dialect-a", Implementor: "impl-a"}},
	}
	if err := store.PutTerminal(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	newInput := []aipapi.Item{{Kind: aipapi.ItemKindReasoning, Reasoning: &aipapi.ReasoningItem{Reasoning: &aipapi.ReasoningPart{Dialect: "dialect-b", Text: "history"}}}}
	got, err := aipcont.Materialize(context.Background(), aipcont.MaterializeInput{
		Store: store, Scope: scope, StartID: id, NewInput: newInput,
		Bounds: aipcont.Bounds{MaxChainDepth: 2, MaxMaterializedItems: 8, MaxMaterializedBytes: 1 << 20},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.NativeRequirements) != 1 || got.NativeRequirements[0].Dialect != "dialect-a" {
		t.Fatalf("native requirements=%+v", got.NativeRequirements)
	}
	if len(got.Requirements.ReasoningDialects) != 1 || got.Requirements.ReasoningDialects[0].Dialect != "dialect-b" {
		t.Fatalf("requirements=%+v", got.Requirements)
	}
}

func TestMaterializeCallPreservesCoreSessionUntilWireSanitization(t *testing.T) {
	t.Parallel()
	store := corecont.NewMemoryStore()
	scope := aipcont.Scope{PrincipalID: "p", SessionID: "s"}
	id, err := store.Reserve(context.Background(), scope, aipcont.StoragePolicy{TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutTerminal(context.Background(), aipcont.ContinuationRecord{
		ID: id, Scope: scope, Terminal: true, InputItems: []aipapi.Item{phase42Item("old", aipapi.RoleUser)},
	}); err != nil {
		t.Fatal(err)
	}
	call, _, err := corecont.MaterializeCall(context.Background(), aipcont.MaterializeInput{
		Store: store, Scope: scope, StartID: id, NewInput: []aipapi.Item{phase42Item("new", aipapi.RoleUser)},
		Bounds: aipcont.Bounds{MaxChainDepth: 2, MaxMaterializedItems: 8, MaxMaterializedBytes: 1 << 20},
	}, aipapi.Call{Session: aipapi.SessionRef{
		ClientSessionID: "client", ContinuityKey: "key", ALegID: "a-leg",
		AuthoritativeSessionID: "proxy", ResumeToken: "token",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if call.Session.ALegID != "a-leg" || call.Session.ClientSessionID != "" || call.Session.ContinuityKey != "" || call.Session.AuthoritativeSessionID != "" || call.Session.ResumeToken != "" {
		t.Fatalf("session=%+v", call.Session)
	}
	if !aipcont.Materialized(call) {
		t.Fatal("the core materializer must mark its provider-facing call")
	}
}
