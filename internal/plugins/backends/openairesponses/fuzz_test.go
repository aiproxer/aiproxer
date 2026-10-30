package openairesponses

import (
	"encoding/json"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func FuzzParamsForCall(f *testing.F) {
	rawModel, err := json.Marshal("gpt-4o-mini")
	if err != nil {
		f.Fatal(err)
	}
	ext := map[string]json.RawMessage{extModelJSONKey: rawModel}
	base := &aipapi.Call{
		Route:      aipapi.RouteIntent{Selector: "stub:gpt-4o-mini"},
		Messages:   []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}}},
		Extensions: ext,
	}
	cand := routing.AttemptCandidate{
		Primary: routing.Primary{Backend: "openai", Model: "gpt-4o-mini"},
		Key:     "openai:gpt-4o-mini",
	}
	f.Add([]byte(""))
	f.Add([]byte("x"))
	f.Add([]byte(`{"tool":"calls"}`))

	f.Fuzz(func(t *testing.T, suffix []byte) {
		txt := string(suffix)
		if len(txt) > 1<<16 {
			txt = txt[:1<<16]
		}
		if txt == "" {
			txt = "."
		}
		c := *base
		c.Messages = []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart(txt)}}}
		_, _ = ParamsForCall(&c, cand)
	})
}
