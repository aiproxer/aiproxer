package product

import (
	"testing"
	"time"
)

// TestClaudeSpec_DesignatesItsCurrentModelAsTheDefault proves the connector
// designates its current model as the instance's default, whatever the
// enumeration order is.
//
// The proxy derives the default route from that designation, so a discovery
// that lists another model first must not make that model the default.
func TestClaudeSpec_DesignatesItsCurrentModelAsTheDefault(t *testing.T) {
	t.Parallel()
	spec := claudeTestSpec(t, nil)
	rows := []DiscoveredModel{
		{NativeID: "claude-nebula-17-3-20261013[1m]", DisplayName: "Nebula"},
		{NativeID: "arbitrary-family-9.2", DisplayName: "Arbitrary"},
	}
	// The current model is the second row: the enumeration order is not a
	// preference order.
	if err := spec.PublishModels(ModelDiscovery{
		Version:        1,
		CurrentModelID: rows[1].NativeID,
		Models:         rows,
	}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	snapshot, err := spec.Inventory().LoadModels(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Models) != 2 {
		t.Fatalf("models = %d want 2", len(snapshot.Models))
	}
	if snapshot.Models[0].IsDefault {
		t.Fatal("the first enumerated model must not be published as the default model")
	}
	if !snapshot.Models[1].IsDefault {
		t.Fatal("the current model must be published as the default model")
	}
}
