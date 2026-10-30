package openresponses

import (
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// unsupportedRequestControls are official request controls the profile
// recognizes. The raw codec accepts their non-null wire shapes (they are valid
// JSON per the pinned schema); admission-time rejection of controls the
// canonical call cannot represent is a frontend concern. instructions is
// deliberately absent here: it maps into a leading canonical system message
// item and forwards (see TestDecodeRequest_InstructionsMapsToLeadingSystemItem).
// Metadata is deliberately excluded from rejection lists: the frontend maps it
// to Call.Session.Metadata.
var unsupportedRequestControls = []struct {
	field string
	value string // JSON value
}{
	{"include", `["reasoning.encrypted_content"]`},
	{"presence_penalty", `0.5`},
	{"frequency_penalty", `0.5`},
	{"stream_options", `{"include_obfuscation":true}`},
	{"top_logprobs", `1`},
	{"truncation", `"auto"`},
	{"service_tier", `"auto"`},
	{"safety_identifier", `"safety_001"`},
	{"prompt_cache_key", `"k1"`},
	{"prompt_cache_retention", `"recent"`},
	{"max_tool_calls", `5`},
}

func TestDecodeRequest_InstructionsMapsToLeadingSystemItem(t *testing.T) {
	t.Parallel()
	body := []byte(`{"model":"gpt-4o","input":"hello","instructions":"Be brief"}`)
	_, call, err := DecodeRequest(body)
	if err != nil {
		t.Fatalf("DecodeRequest failed with instructions: %v", err)
	}
	if !call.HasItemAuthority() || len(call.Items) != 2 {
		t.Fatalf("instructions must map into a leading system item plus input, got %d items: %+v", len(call.Items), call.Items)
	}
	leading := call.Items[0]
	if leading.Kind != aipapi.ItemKindMessage || leading.Role != aipapi.RoleSystem {
		t.Fatalf("leading item must be a system message, got %+v", leading)
	}
	if len(leading.Content) != 1 || leading.Content[0].Kind != aipapi.ContentPartText || leading.Content[0].Text != "Be brief" {
		t.Fatalf("leading system item must preserve the exact instruction text, got %+v", leading.Content)
	}
	if trailing := call.Items[1]; trailing.Role != aipapi.RoleUser {
		t.Fatalf("input must follow the leading system item, got %+v", trailing)
	}
}

func TestDecodeRequest_InstructionsNullOrEmptyTreatedAsAbsent(t *testing.T) {
	t.Parallel()
	for name, instr := range map[string]string{
		"null":  `null`,
		"empty": `""`,
		"blank": `"   "`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			body := []byte(`{"model":"gpt-4o","input":"hello","instructions":` + instr + `}`)
			_, call, err := DecodeRequest(body)
			if err != nil {
				t.Fatalf("DecodeRequest failed for instructions=%s: %v", instr, err)
			}
			if len(call.Items) != 1 {
				t.Fatalf("instructions=%s must be treated as absent (1 item), got %d: %+v", instr, len(call.Items), call.Items)
			}
		})
	}
}

func TestDecodeRequest_UnsupportedControlsRawDecodeAccepted(t *testing.T) {
	t.Parallel()
	for _, tc := range unsupportedRequestControls {
		t.Run(tc.field, func(t *testing.T) {
			t.Parallel()
			body := []byte(`{"model":"gpt-4o","input":"hello","` + tc.field + `":` + tc.value + `}`)
			_, call, err := DecodeRequest(body)
			if err != nil {
				t.Fatalf("raw codec must accept the pinned wire shape for %s=%s, got error: %v", tc.field, tc.value, err)
			}
			if !call.HasItemAuthority() || len(call.Items) != 1 {
				t.Fatalf("unexpected canonical call for %s=%s: %+v", tc.field, tc.value, call)
			}
		})
	}
}

func TestDecodeRequest_UnsupportedControlsNullAccepted(t *testing.T) {
	t.Parallel()
	for _, tc := range unsupportedRequestControls {
		t.Run(tc.field, func(t *testing.T) {
			t.Parallel()
			body := []byte(`{"model":"gpt-4o","input":"hello","` + tc.field + `":null}`)
			_, call, err := DecodeRequest(body)
			if err != nil {
				t.Fatalf("null %s must be accepted like an omitted field, got error: %v", tc.field, err)
			}
			if !call.HasItemAuthority() || len(call.Items) != 1 {
				t.Fatalf("unexpected canonical call for null %s: %+v", tc.field, call)
			}
			if len(call.Extensions) != 0 {
				t.Fatalf("null %s must not become an extension, got %v", tc.field, call.Extensions)
			}
		})
	}
}

func TestDecodeRequest_UnsupportedControlsMalformedFailDeterministically(t *testing.T) {
	t.Parallel()
	cases := []struct {
		field string
		value string
	}{
		{"instructions", `5`},
		{"include", `5`},
		{"presence_penalty", `"x"`},
		{"frequency_penalty", `true`},
		{"top_logprobs", `1.5`},
		{"truncation", `123`},
		{"service_tier", `true`},
		{"max_tool_calls", `"five"`},
	}
	for _, tc := range cases {
		t.Run(tc.field, func(t *testing.T) {
			t.Parallel()
			body := []byte(`{"model":"gpt-4o","input":"hello","` + tc.field + `":` + tc.value + `}`)
			if _, _, err := DecodeRequest(body); err == nil {
				t.Fatalf("malformed %s=%s must fail, got nil", tc.field, tc.value)
			}
		})
	}
}

func TestDecodeRequest_SupportedControlsPreserved(t *testing.T) {
	t.Parallel()
	body := []byte(`{
		"model":"gpt-4o",
		"input":"hello",
		"temperature":0.7,
		"top_p":0.9,
		"max_output_tokens":256,
		"parallel_tool_calls":true,
		"metadata":{"tenant":"acme"}
	}`)
	_, call, err := DecodeRequest(body)
	if err != nil {
		t.Fatalf("supported controls must decode: %v", err)
	}
	if call.Options.Temperature == nil || *call.Options.Temperature != 0.7 {
		t.Fatalf("temperature=%v, want 0.7", call.Options.Temperature)
	}
	if call.Options.TopP == nil || *call.Options.TopP != 0.9 {
		t.Fatalf("top_p=%v, want 0.9", call.Options.TopP)
	}
	if call.Options.MaxOutputTokens == nil || *call.Options.MaxOutputTokens != 256 {
		t.Fatalf("max_output_tokens=%v, want 256", call.Options.MaxOutputTokens)
	}
	if call.Options.ParallelToolCalls == nil || !*call.Options.ParallelToolCalls {
		t.Fatalf("parallel_tool_calls=%v, want true", call.Options.ParallelToolCalls)
	}
}

func TestDecodeRequest_ReasoningEffortSubset(t *testing.T) {
	for name, raw := range map[string]string{
		"valid":       `{"effort":" low "}`,
		"null":        `null`,
		"empty":       `{}`,
		"effort-null": `{"effort":null}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, call, err := DecodeRequest([]byte(`{"model":"gpt-4o","input":"hello","reasoning":` + raw + `}`))
			if err != nil {
				t.Fatal(err)
			}
			want := ""
			if name == "valid" {
				want = "low"
			}
			if call.Options.ReasoningEffort != want {
				t.Fatalf("effort=%q, want %q", call.Options.ReasoningEffort, want)
			}
		})
	}
	for _, raw := range []string{`[]`, `{"effort":1}`, `{"effort":""}`, `{"effort":"minimal"}`, `{"summary":[]}`, `{"effort":"low","summary":[]}`} {
		if _, _, err := DecodeRequest([]byte(`{"model":"gpt-4o","input":"hello","reasoning":` + raw + `}`)); err == nil {
			t.Fatalf("reasoning=%s should be rejected", raw)
		}
	}
}
