package continuation_test

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	aipcont "github.com/aiproxer/aiproxer/pkg/aipsdk/continuation"
)

func TestMaterializeNearMaxIntLimits(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	scope := aipcont.Scope{TenantID: "tenant", PrincipalID: "principal", SessionID: "session"}
	store := aipcont.NewMemoryStore()

	id, err := store.Reserve(ctx, scope, aipcont.StoragePolicy{TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutTerminal(ctx, aipcont.ContinuationRecord{
		ID: id, Scope: scope, Terminal: true,
		InputItems:  []aipapi.Item{overflowItem("in-1", aipapi.RoleUser)},
		OutputItems: []aipapi.Item{overflowItem("out-1", aipapi.RoleAssistant)},
	}); err != nil {
		t.Fatal(err)
	}

	got, err := aipcont.Materialize(ctx, aipcont.MaterializeInput{
		Store: store, Scope: scope, StartID: id,
		NewInput: []aipapi.Item{overflowItem("in-2", aipapi.RoleUser)},
		Bounds: aipcont.Bounds{
			MaxChainDepth:        2,
			MaxMaterializedItems: math.MaxInt,
			MaxMaterializedBytes: math.MaxInt64,
		},
	})
	if err != nil {
		t.Fatalf("materialize with near-MaxInt limits: %v", err)
	}
	want := []string{"in-1", "out-1", "in-2"}
	if len(got.Items) != len(want) {
		t.Fatalf("items=%d want %d", len(got.Items), len(want))
	}
	for i, id := range want {
		if got.Items[i].ID != id {
			t.Fatalf("item[%d]=%q want %q", i, got.Items[i].ID, id)
		}
	}
	if got.ChainDepth != 1 || got.TotalBytes <= 0 {
		t.Fatalf("trajectory=%+v", got)
	}
}

func overflowItem(id string, role aipapi.Role) aipapi.Item {
	return aipapi.Item{ID: id, Kind: aipapi.ItemKindMessage, Role: role, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: id}}}
}
