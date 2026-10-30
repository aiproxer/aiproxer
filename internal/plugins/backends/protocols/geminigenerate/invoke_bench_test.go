package geminigenerate

import (
	"encoding/json"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func BenchmarkBuildContents(b *testing.B) {
	call := &aipapi.Call{
		Messages: make([]aipapi.Message, 1000),
	}
	for i := range 1000 {
		call.Messages[i] = aipapi.Message{
			Role: aipapi.RoleUser,
			Parts: []aipapi.Part{
				aipapi.TextPart("hello world"),
			},
		}
	}

	b.ReportAllocs()
	for b.Loop() {
		_, _ = buildContents(call)
	}
}

func BenchmarkUserPartsToGenaiParts(b *testing.B) {
	parts := make([]aipapi.Part, 1000)
	for i := range 1000 {
		parts[i] = aipapi.TextPart("hello world")
	}

	b.ReportAllocs()
	for b.Loop() {
		_, _ = userPartsToGenaiParts(parts)
	}
}

func BenchmarkAssistantPartsToGenaiParts(b *testing.B) {
	parts := make([]aipapi.Part, 1000)
	for i := range 1000 {
		parts[i] = aipapi.TextPart("hello world")
	}

	b.ReportAllocs()
	for b.Loop() {
		_, _ = assistantPartsToGenaiParts(parts)
	}
}

func BenchmarkBuildTools(b *testing.B) {
	tools := make([]aipapi.ToolDef, 1000)
	for i := range 1000 {
		tools[i] = aipapi.ToolDef{
			Name:        "test",
			Description: "desc",
			Parameters:  json.RawMessage(`{"type": "object"}`),
		}
	}

	b.ReportAllocs()
	for b.Loop() {
		_, _ = buildTools(tools)
	}
}
