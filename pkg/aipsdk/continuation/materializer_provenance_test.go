package continuation_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	aipcont "github.com/aiproxer/aiproxer/pkg/aipsdk/continuation"
)

// The materializer provenance marker is the ONLY evidence that a call's item
// slice is a complete parent trajectory. These proofs bind the marker to the
// real materializers, so a consumer cannot mistake "nonempty items" for
// completeness.

func provenanceFixture(t *testing.T) (aipcont.Store, aipcont.Scope, aipcont.ResponseID) {
	t.Helper()
	ctx := context.Background()
	scope := aipcont.Scope{TenantID: "tenant", PrincipalID: "principal", SessionID: "session"}
	store := aipcont.NewMemoryStore()
	id, err := store.Reserve(ctx, scope, aipcont.StoragePolicy{TTL: time.Hour})
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	if err := store.PutTerminal(ctx, aipcont.ContinuationRecord{
		ID: id, Scope: scope, Terminal: true,
		InputItems:  []aipapi.Item{provenanceItem("first", aipapi.RoleUser)},
		OutputItems: []aipapi.Item{provenanceItem("answer", aipapi.RoleAssistant)},
	}); err != nil {
		t.Fatalf("store the parent: %v", err)
	}
	return store, scope, id
}

func provenanceItem(id string, role aipapi.Role) aipapi.Item {
	return aipapi.Item{
		ID: id, Kind: aipapi.ItemKindMessage, Role: role,
		Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: id}},
	}
}

func TestMaterializeCallMarksTheProvenanceExtension(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, scope, id := provenanceFixture(t)
	base := aipapi.Call{PreviousResponseID: id.String()}
	out, _, err := aipcont.MaterializeCall(ctx, aipcont.MaterializeInput{
		Store: store, Scope: scope, StartID: id,
		NewInput: []aipapi.Item{provenanceItem("second", aipapi.RoleUser)},
	}, base)
	if err != nil {
		t.Fatalf("MaterializeCall: %v", err)
	}
	if !aipcont.Materialized(out) {
		t.Fatal("a materialized call must carry the provenance marker")
	}
	if aipcont.Materialized(base) {
		t.Fatal("the un-materialized base call must not carry the marker")
	}
	if len(out.Items) != 3 {
		t.Fatalf("materialized items=%d, want 3", len(out.Items))
	}
}

func TestResolveParentMarksTheProvenanceExtension(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, scope, id := provenanceFixture(t)
	resolver := aipcont.NewResolver(store, aipcont.Bounds{})
	out, _, err := resolver.ResolveParent(ctx, scope, id.String(), aipapi.Call{
		PreviousResponseID: id.String(),
		Items:              []aipapi.Item{provenanceItem("second", aipapi.RoleUser)},
	})
	if err != nil {
		t.Fatalf("ResolveParent: %v", err)
	}
	if !aipcont.Materialized(out) {
		t.Fatal("a ResolveParent result must carry the provenance marker")
	}
}

func TestMaterializedRejectsAnythingButAnExplicitTrue(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		raw  json.RawMessage
		want bool
	}{
		{name: "absent", want: false},
		{name: "true", raw: json.RawMessage(`true`), want: true},
		{name: "true with whitespace", raw: json.RawMessage(" \n true \t"), want: true},
		{name: "false", raw: json.RawMessage(`false`), want: false},
		{name: "numeric one", raw: json.RawMessage(`1`), want: false},
		{name: "string true", raw: json.RawMessage(`"true"`), want: false},
		{name: "object", raw: json.RawMessage(`{"v":1}`), want: false},
		{name: "empty", raw: json.RawMessage(``), want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			call := aipapi.Call{}
			if tc.raw != nil {
				call.Extensions = map[string]json.RawMessage{aipcont.MaterializerProvenanceExtension: tc.raw}
			}
			if got := aipcont.Materialized(call); got != tc.want {
				t.Fatalf("Materialized=%v, want %v", got, tc.want)
			}
		})
	}
}
