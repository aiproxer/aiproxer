package openresponses

import (
	"encoding/json"
	"testing"
)

func fuzzSeed(b []byte) []byte {
	// A complete create request body used to seed parsers.
	return b
}

// FuzzParseCreateRequest fuzzes the independent create-request parser for
// panics/leaks on arbitrary bytes.
func FuzzParseCreateRequest(f *testing.F) {
	f.Add([]byte(`{"model":"m","input":"hi","stream":true}`))
	f.Add([]byte(`{"model":"m","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"x"}]}]}`))
	f.Add([]byte(`{"model":"m","input":[{"type":"acme:telemetry","id":"t1"}]}`))
	f.Add(fuzzSeed([]byte(`{"model":"m","input":"hi"}`)))
	f.Add([]byte{0xff, 0x00, 0xfe})
	f.Add([]byte(`{"input":123}`))
	f.Add([]byte(`[]`))
	f.Add([]byte(`{"model":"m","tools":[{"type":"function","parameters":{"type":"object"}}]}`))

	f.Fuzz(func(t *testing.T, data []byte) {
		req, err := parseCreateRequest(data)
		if err != nil {
			return
		}
		_ = req.Items()
		_ = req.ExtensionItemTypes()
		for _, it := range req.Items() {
			_, _ = json.Marshal(it)
		}
	})
}

// FuzzParseCompactRequest fuzzes the independent compact-request parser.
func FuzzParseCompactRequest(f *testing.F) {
	f.Add([]byte(`{"model":"m","input":[{"type":"message","role":"user","content":"c"}]}`))
	f.Add([]byte(`{"input":[]}`))
	f.Add([]byte(`{"model":"m","acme:mode":"fast"}`))

	f.Fuzz(func(t *testing.T, data []byte) {
		req, err := parseCompactRequest(data)
		if err != nil {
			return
		}
		_ = req.Items()
		_, _ = json.Marshal(req)
	})
}

// FuzzParseItem fuzzes the independent wire-item parser.
func FuzzParseItem(f *testing.F) {
	f.Add([]byte(`{"type":"message","role":"user","content":"hi"}`))
	f.Add([]byte(`{"type":"acme:telemetry","id":"t1"}`))
	f.Add([]byte(`{"type":"function_call","arguments":"{}"}`))
	f.Add([]byte(`{"type":"reasoning","encrypted_content":null}`))
	f.Add([]byte(`{"type":"mystery"}`))

	f.Fuzz(func(t *testing.T, data []byte) {
		var it Item
		if err := it.UnmarshalJSON(data); err != nil {
			return
		}
		if it.Type == "" {
			t.Fatal("parsed item with empty type")
		}
		_, _ = json.Marshal(it)
	})
}

// FuzzParseContentPart fuzzes the independent content-part parser.
func FuzzParseContentPart(f *testing.F) {
	f.Add([]byte(`{"type":"input_text","text":"hi"}`))
	f.Add([]byte(`{"type":"acme:part","k":1}`))
	f.Add([]byte(`{"type":"input_image","image_url":{"url":"x"}}`))
	f.Add([]byte(`{"type":"bogus"}`))

	f.Fuzz(func(t *testing.T, data []byte) {
		var p ContentPart
		if err := p.UnmarshalJSON(data); err != nil {
			return
		}
		if p.Type == "" {
			t.Fatal("parsed part with empty type")
		}
		_, _ = json.Marshal(p)
	})
}

// FuzzBuildStream fuzzes the independent stream builder over arbitrary resources
// to prove it never panics and always emits exactly one terminal.
func FuzzBuildStream(f *testing.F) {
	f.Add([]byte(`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"x"}]}`))
	f.Add([]byte(`{"type":"function_call","name":"f","arguments":"{}"}`))
	f.Add([]byte(`{"type":"reasoning","summary":[{"type":"summary_text","text":"s"}]}`))
	f.Add([]byte(`{"type":"acme:telemetry"}`))
	f.Add([]byte(`{"type":"mystery"}`))

	f.Fuzz(func(t *testing.T, data []byte) {
		var it Item
		if err := it.UnmarshalJSON(data); err != nil {
			return
		}
		res := NewResource("r", "m", 1, []Item{it})
		events := buildStreamEvents(res)
		terminals := 0
		for _, ev := range events {
			if ev.Type == "response.completed" {
				terminals++
			}
			if _, err := ev.renderPayload(); err != nil {
				t.Fatalf("render payload: %v", err)
			}
		}
		if terminals != 1 {
			t.Fatalf("expected exactly one terminal, got %d", terminals)
		}
	})
}

// FuzzParseWSTurn fuzzes the WebSocket turn-envelope parser.
func FuzzParseWSTurn(f *testing.F) {
	f.Add([]byte(`{"type":"response.create","model":"m","input":"hi"}`))
	f.Add([]byte(`{"model":"m"}`))
	f.Add([]byte(`{"type":"response.create","input":[{"type":"message","role":"user","content":"x"}]}`))
	f.Add([]byte(`not json`))

	f.Fuzz(func(t *testing.T, data []byte) {
		req, err := parseWSTurn(data)
		if err != nil {
			return
		}
		_ = req.Model
		_ = req.Items()
	})
}
