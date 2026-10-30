package codexclientcompat

import (
	"encoding/json"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// TestIgnoresClampViaCodexUnsupportedGenParams characterizes the canonical-call
// extension decoder that backend adapters install as
// execbackend.Backend.IgnoresAuthorityMaxOutputTokensClamp: it reports true only
// when the codex extension key is present and its JSON value decodes to true.
// This is the pre-move behavior of internal/core/execbackend preserved verbatim
// after the decoder moved to this feature package.
func TestIgnoresClampViaCodexUnsupportedGenParams(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		extensions map[string]json.RawMessage
		want       bool
	}{
		{
			name:       "nil extensions",
			extensions: nil,
			want:       false,
		},
		{
			name:       "empty extensions map",
			extensions: map[string]json.RawMessage{},
			want:       false,
		},
		{
			name:       "unrelated codex extension only",
			extensions: map[string]json.RawMessage{"openai_codex.tool_strict": json.RawMessage("true")},
			want:       false,
		},
		{
			name:       "key present and true",
			extensions: map[string]json.RawMessage{"openai_codex.ignore_unsupported_gen_params": json.RawMessage("true")},
			want:       true,
		},
		{
			name:       "key present and false",
			extensions: map[string]json.RawMessage{"openai_codex.ignore_unsupported_gen_params": json.RawMessage("false")},
			want:       false,
		},
		{
			name:       "key present as quoted string",
			extensions: map[string]json.RawMessage{"openai_codex.ignore_unsupported_gen_params": json.RawMessage(`"true"`)},
			want:       false,
		},
		{
			name:       "key present as number one",
			extensions: map[string]json.RawMessage{"openai_codex.ignore_unsupported_gen_params": json.RawMessage("1")},
			want:       false,
		},
		{
			name:       "key present as json null",
			extensions: map[string]json.RawMessage{"openai_codex.ignore_unsupported_gen_params": json.RawMessage("null")},
			want:       false,
		},
		{
			name:       "key present but not valid json",
			extensions: map[string]json.RawMessage{"openai_codex.ignore_unsupported_gen_params": json.RawMessage("tru")},
			want:       false,
		},
		{
			name: "key present and true alongside unrelated extensions",
			extensions: map[string]json.RawMessage{
				"openai_codex.tool_strict":                   json.RawMessage("true"),
				"openai_codex.ignore_unsupported_gen_params": json.RawMessage("true"),
			},
			want: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := IgnoresClampViaCodexUnsupportedGenParams(aipapi.Call{Extensions: tc.extensions})
			if got != tc.want {
				t.Fatalf("IgnoresClampViaCodexUnsupportedGenParams(%s) = %v, want %v", describeExtensions(tc.extensions), got, tc.want)
			}
		})
	}
}

// TestCodexIgnoreUnsupportedGenParamsExtWireKey pins the canonical-call extension
// key byte-for-byte: the writer in this package, the backend-adapter decoders,
// and the openai_codex connector all address the same wire name.
func TestCodexIgnoreUnsupportedGenParamsExtWireKey(t *testing.T) {
	t.Parallel()

	const wantKey = "openai_codex.ignore_unsupported_gen_params"
	if CodexIgnoreUnsupportedGenParamsExt != wantKey {
		t.Fatalf("CodexIgnoreUnsupportedGenParamsExt = %q, want %q", CodexIgnoreUnsupportedGenParamsExt, wantKey)
	}
}

// TestApplyIgnoreUnsupportedGenParamsRoundTripsThroughDecoder keeps the writer and
// the reader of the extension on one key: a call marked by this feature is
// reported by the decoder that backend adapters install as their clamp callback.
func TestApplyIgnoreUnsupportedGenParamsRoundTripsThroughDecoder(t *testing.T) {
	t.Parallel()

	call := aipapi.Call{Options: aipapi.GenerationOptions{MaxOutputTokens: new(int)}}
	applyIgnoreUnsupportedGenParams(&call)

	if got := IgnoresClampViaCodexUnsupportedGenParams(call); !got {
		t.Fatal("decoder must report true for a call this feature marked")
	}
}

func describeExtensions(ext map[string]json.RawMessage) string {
	if len(ext) == 0 {
		return "{}"
	}
	b, err := json.Marshal(map[string]json.RawMessage(ext))
	if err != nil {
		return "<unencodable>"
	}
	return string(b)
}
