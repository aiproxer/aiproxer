package backendplugin_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

func TestInvocationValidate_doesNotMutateInstructionsSlice(t *testing.T) {
	t.Parallel()
	instructions := []backendplugin.Message{
		{Role: backendplugin.RoleSystem, Parts: []backendplugin.Part{{Kind: backendplugin.PartKindText, Text: new("sys")}}},
	}
	beforeCap := cap(instructions)
	beforeLen := len(instructions)
	inv := backendplugin.Invocation{
		RequestID:        "req-1",
		AttemptID:        "att-1",
		ALegID:           "a-1",
		BLegID:           "b-1",
		CanonicalModelID: "model-1",
		Instructions:     instructions,
		Messages: []backendplugin.Message{
			{Role: backendplugin.RoleUser, Parts: []backendplugin.Part{{Kind: backendplugin.PartKindText, Text: new("hi")}}},
		},
	}
	if err := inv.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if len(instructions) != beforeLen {
		t.Fatalf("instructions len changed: got %d want %d", len(instructions), beforeLen)
	}
	if cap(instructions) != beforeCap {
		t.Fatalf("instructions cap changed: got %d want %d", cap(instructions), beforeCap)
	}
	if &instructions[0] != &inv.Instructions[0] {
		t.Fatalf("Validate must not reallocate or append into caller Instructions slice")
	}
}
