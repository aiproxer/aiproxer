package openresponses

import (
	"testing"
)

func fuzzSeedFixture(f *testing.F, name string) []byte {
	f.Helper()
	b := mustReadScenario(f, name)
	return b
}

// FuzzParseResponseResource fuzzes the response resource parser for panics/leaks.
func FuzzParseResponseResource(f *testing.F) {
	f.Add([]byte(`{"id":"r","object":"response","created_at":1,"status":"completed","model":"m","output":[],"parallel_tool_calls":false,"reasoning":null,"store":true,"background":false,"temperature":1,"text":{},"tool_choice":"auto","tools":[],"top_p":1,"truncation":"disabled","usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0},"metadata":{},"service_tier":"default","max_output_tokens":null,"max_tool_calls":null,"instructions":null,"previous_response_id":null,"error":null,"incomplete_details":null}`))
	f.Add([]byte(`{"id":"","object":"response","created_at":1,"status":"completed","model":"m","output":[],"parallel_tool_calls":false,"reasoning":null,"store":true,"background":false,"temperature":1,"text":{},"tool_choice":"auto","tools":[],"top_p":1,"truncation":"disabled","usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0},"metadata":{},"service_tier":"default","max_output_tokens":null,"max_tool_calls":null,"instructions":null,"previous_response_id":null,"error":null,"incomplete_details":null}`))
	f.Add(fuzzSeedFixture(f, "response_text.json"))
	f.Add(fuzzSeedFixture(f, "response_tools.json"))
	f.Add(fuzzSeedFixture(f, "response_reasoning.json"))
	f.Add(fuzzSeedFixture(f, "response_phase.json"))
	f.Add(fuzzSeedFixture(f, "response_extensions.json"))
	f.Add(fuzzSeedFixture(f, "response_error.json"))

	f.Fuzz(func(t *testing.T, data []byte) {
		opts := DefaultParseOptions()
		res, err := ParseResponseResource(data, opts)
		if err == nil {
			if res.ID == "" {
				t.Fatal("parsed response with empty id")
			}
			_ = res.OutputText()
			_ = res.Terminal()
			_ = res.Failed()
		}
	})
}

// FuzzParseCompactResource fuzzes the compact resource parser.
func FuzzParseCompactResource(f *testing.F) {
	f.Add([]byte(`{"id":"c","object":"response.compaction","created_at":1,"status":"completed","model":"m","output":[],"usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0}}`))
	f.Add([]byte(`{"id":"","object":"response.compaction","created_at":1,"status":"completed","model":"m","output":[],"usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0}}`))
	f.Add(fuzzSeedFixture(f, "compact_resource.json"))

	f.Fuzz(func(t *testing.T, data []byte) {
		if res, err := ParseCompactResource(data, DefaultParseOptions()); err == nil {
			if res.ID == "" {
				t.Fatal("parsed compact with empty id")
			}
			_ = res.IsCompact()
		}
	})
}

// FuzzParseEvent fuzzes the single-event JSON parser.
func FuzzParseEvent(f *testing.F) {
	f.Add([]byte(`{"type":"response.completed","sequence_number":1}`))
	f.Add([]byte(`{"type":"response.output_text.delta","delta":"hi"}`))
	f.Add([]byte(`{"type":"acme:telemetry_chunk","sequence_number":2}`))

	f.Fuzz(func(t *testing.T, data []byte) {
		if evt, err := ParseEvent(data, DefaultParseOptions()); err == nil {
			if evt.Type == "" {
				t.Fatal("parsed event with empty type")
			}
			_ = evt.IsTerminal()
			_ = evt.IsError()
		}
	})
}

// FuzzParseSSE fuzzes the SSE framing parser.
func FuzzParseSSE(f *testing.F) {
	f.Add([]byte("event: response.created\ndata: {\"type\":\"response.created\",\"sequence_number\":0}\n\n"))
	f.Add([]byte("event: response.completed\ndata: {\"type\":\"response.completed\",\"sequence_number\":1}\n\ndata: [DONE]\n\n"))
	f.Add(fuzzSeedFixture(f, "stream_text.sse"))

	f.Fuzz(func(t *testing.T, data []byte) {
		events, done, err := ParseSSE(data, DefaultParseOptions())
		if err != nil {
			return
		}
		if !done {
			t.Fatal("ParseSSE accepted stream without [DONE]")
		}
		for i := range events {
			if events[i].Type == "" {
				t.Fatal("event with empty type")
			}
		}
	})
}

// FuzzParseItem fuzzes the discriminated item parser.
func FuzzParseItem(f *testing.F) {
	f.Add([]byte(`{"type":"message","role":"user","content":"hi"}`))
	f.Add([]byte(`{"type":"acme:telemetry_chunk","id":"t1","status":"completed"}`))

	f.Fuzz(func(t *testing.T, data []byte) {
		var it Item
		if err := it.UnmarshalJSON(data); err == nil {
			if it.Type == "" {
				t.Fatal("item with empty type")
			}
		}
	})
}
