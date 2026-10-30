package source

import (
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func FuzzPrepareBoundedAndDeterministic(f *testing.F) {
	for _, seed := range []string{
		"I choose the bounded adapter.",
		"Plan: validate the result, then continue.",
		"todo status: pending",
		"\x00\x01\xff",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, text string) {
		cfg := DefaultConfig()
		cfg.MaxBytes = 256
		cfg.MaxEntryBytes = 64
		call := aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart(text)}}}}
		first, err := Prepare(t.Context(), Input{Call: call, Config: cfg})
		if err != nil {
			t.Fatal(err)
		}
		second, err := Prepare(t.Context(), Input{Call: call, Config: cfg})
		if err != nil {
			t.Fatal(err)
		}
		if first.HighWatermark != second.HighWatermark || first.Envelope.Canonical() != second.Envelope.Canonical() {
			t.Fatal("prepare is not deterministic")
		}
		if first.Envelope.Bytes > cfg.MaxBytes {
			t.Fatalf("payload bytes %d exceed bound %d", first.Envelope.Bytes, cfg.MaxBytes)
		}
	})
}
