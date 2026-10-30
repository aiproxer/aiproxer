package backendplugin_test

import (
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

func TestServerFrameSize_OpaqueToolDiagnostic(t *testing.T) {
	t.Parallel()
	limit := uint64(128)
	opaque := []byte(strings.Repeat("o", 200))
	if err := backendplugin.ValidateServerFrameSize(backendplugin.ServerFrame{
		Kind:     backendplugin.ServerFrameEvent,
		Sequence: 1,
		Event:    &backendplugin.CanonicalEvent{Kind: backendplugin.EventReasoningOpaqueDelta, Opaque: opaque},
	}, limit); err == nil {
		t.Fatal("expected oversized opaque")
	}
	args := strings.Repeat("a", 200)
	id := "call-1"
	name := "tool"
	if err := backendplugin.ValidateServerFrameSize(backendplugin.ServerFrame{
		Kind:     backendplugin.ServerFrameEvent,
		Sequence: 1,
		Event: &backendplugin.CanonicalEvent{
			Kind: backendplugin.EventToolCallArgsDelta, ToolCallID: &id, ToolName: &name, Delta: &args,
		},
	}, limit); err == nil {
		t.Fatal("expected oversized tool args")
	}
	if err := backendplugin.ValidateServerFrameSize(backendplugin.ServerFrame{
		Kind:       backendplugin.ServerFrameDiagnostic,
		Sequence:   1,
		Diagnostic: strings.Repeat("d", 200),
	}, limit); err == nil {
		t.Fatal("expected oversized diagnostic")
	}
	n := backendplugin.ServerFrameSizeBytes(backendplugin.ServerFrame{
		Kind:       backendplugin.ServerFrameDiagnostic,
		Sequence:   1,
		Diagnostic: "ok",
	})
	if n == 0 {
		t.Fatal("expected non-zero frame size")
	}
	if backendplugin.ServerFrameConservativeBytes(backendplugin.ServerFrame{
		Kind: backendplugin.ServerFrameAccepted,
	}) == 0 {
		t.Fatal("envelope must be counted")
	}
}
