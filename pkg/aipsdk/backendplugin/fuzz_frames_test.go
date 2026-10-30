package backendplugin_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

func FuzzServerFrame(f *testing.F) {
	f.Add([]byte(`{"kind":"accepted"}`))
	f.Add([]byte(`{"kind":"event","event":{"kind":"text_delta","delta":"x"}}`))
	f.Add([]byte(`{"kind":"event","event":{"kind":"evil_kind"}}`))
	f.Add([]byte(`{"kind":"diagnostic","diagnostic":"` + string(make([]byte, 4096)) + `"}`))
	f.Add(make([]byte, 0))
	f.Add([]byte{0xff, 0x00, 0x01})
	f.Fuzz(func(t *testing.T, in []byte) {
		// Treat arbitrary bytes as diagnostic/event payload candidates and
		// ensure validators never panic and always fail closed on garbage.
		_ = backendplugin.ValidateServerFrameBounds(backendplugin.ServerFrame{
			Kind:       backendplugin.ServerFrameDiagnostic,
			Diagnostic: string(in),
		})
		_ = backendplugin.ValidateServerFrameBounds(backendplugin.ServerFrame{
			Kind:  backendplugin.ServerFrameEvent,
			Event: &backendplugin.CanonicalEvent{Kind: backendplugin.EventKind(in), Delta: new(string(in))},
		})
		_ = (backendplugin.ServerFrame{
			Kind:  backendplugin.ServerFrameEvent,
			Event: &backendplugin.CanonicalEvent{Kind: backendplugin.EventKind(in)},
		}).ValidateShape()
		_ = backendplugin.ValidateEventKind(backendplugin.EventKind(string(in)))
	})
}

func ptrStr(s string) *string { return new(s) }
