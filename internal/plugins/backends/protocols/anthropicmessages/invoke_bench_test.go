package anthropicmessages

import (
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func BenchmarkBuildSystemBlocks(b *testing.B) {
	call := &aipapi.Call{
		Instructions: []aipapi.Message{
			{Role: aipapi.RoleSystem, Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: "Instruction 1"}}},
			{Role: aipapi.RoleSystem, Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: "Instruction 2"}}},
		},
		Messages: []aipapi.Message{
			{Role: aipapi.RoleSystem, Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: "Sys 1"}, {Kind: aipapi.PartText, Text: "Sys 2"}}},
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: "User 1"}}},
			{Role: aipapi.RoleSystem, Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: "Sys 3"}, {Kind: aipapi.PartText, Text: "Sys 4"}, {Kind: aipapi.PartText, Text: "Sys 5"}}},
		},
	}

	for b.Loop() {
		_ = buildSystemBlocks(call, false)
	}
}

func BenchmarkBuildAnthropicMessages(b *testing.B) {
	call := &aipapi.Call{
		Messages: []aipapi.Message{
			{Role: aipapi.RoleSystem, Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: "Sys 1"}}},
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: "User 1"}}},
			{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: "Assistant 1"}}},
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: "User 2"}}},
			{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: "Assistant 2"}}},
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: "User 3"}}},
		},
	}

	for b.Loop() {
		_, _ = buildAnthropicMessages(call)
	}
}

func BenchmarkUserPartsToBlocks(b *testing.B) {
	parts := make([]aipapi.Part, 100)
	for i := range 100 {
		parts[i] = aipapi.Part{
			Kind: aipapi.PartText,
			Text: "Hello, world!",
		}
	}

	for b.Loop() {
		_, _ = userPartsToBlocks(parts)
	}
}
