package backendplugin_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

func TestToolChoiceBridge_roundTripAllModes(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   aipapi.ToolChoice
		want string
	}{
		{name: "auto absent", in: aipapi.ToolChoice{Mode: aipapi.ToolChoiceAuto}, want: ""},
		{name: "none", in: aipapi.ToolChoice{Mode: aipapi.ToolChoiceNone}, want: "none"},
		{name: "any", in: aipapi.ToolChoice{Mode: aipapi.ToolChoiceAny}, want: "any"},
		{name: "required named", in: aipapi.ToolChoice{Mode: aipapi.ToolChoiceRequired, Name: "weather"}, want: "required:weather"},
		{name: "allowed auto", in: aipapi.ToolChoice{Mode: aipapi.ToolChoiceAuto, AllowedTools: []string{"get_a", "get_b"}}, want: "allowed:auto:get_a,get_b"},
		{name: "allowed none", in: aipapi.ToolChoice{Mode: aipapi.ToolChoiceNone, AllowedTools: []string{"get_a"}}, want: "allowed:none:get_a"},
		{name: "allowed any", in: aipapi.ToolChoice{Mode: aipapi.ToolChoiceAny, AllowedTools: []string{"get_a"}}, want: "allowed:any:get_a"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			wire := backendplugin.ToolChoiceToWire(tc.in)
			if tc.want == "" {
				if wire != nil {
					t.Fatalf("wire=%q want absent", *wire)
				}
			} else if wire == nil || *wire != tc.want {
				got := ""
				if wire != nil {
					got = *wire
				}
				t.Fatalf("wire=%q want %q", got, tc.want)
			}
			var ptr *string
			if wire != nil {
				ptr = wire
			}
			back, err := backendplugin.ToolChoiceFromWire(ptr)
			if err != nil {
				t.Fatal(err)
			}
			if back.Mode != tc.in.Mode || back.Name != tc.in.Name {
				t.Fatalf("back=%+v want=%+v", back, tc.in)
			}
			if !slices.Equal(back.AllowedTools, tc.in.AllowedTools) {
				t.Fatalf("allowed subset back=%v want=%v", back.AllowedTools, tc.in.AllowedTools)
			}
		})
	}
}

func TestToolChoiceBridge_CallFromInvocationPreservesChoice(t *testing.T) {
	t.Parallel()

	required := "required:get_weather"
	inv := backendplugin.Invocation{
		RequestID: "r", AttemptID: "a", ALegID: "aleg", BLegID: "bleg", CanonicalModelID: "m",
		Messages: []backendplugin.Message{{
			Role:  backendplugin.RoleUser,
			Parts: []backendplugin.Part{{Kind: backendplugin.PartKindText, Text: strPtr("hi")}},
		}},
		Tools: []backendplugin.ToolDef{{
			Name: "get_weather", ParametersJSON: backendplugin.RawJSONFromBytes([]byte(`{"type":"object"}`)),
		}},
		ToolChoice: &required,
		Options:    backendplugin.GenerationOptions{ResponseSchemaJSON: backendplugin.RawJSONAbsentValue()},
	}
	call, err := backendplugin.CallFromInvocation(inv)
	if err != nil {
		t.Fatal(err)
	}
	if call.ToolChoice.Mode != aipapi.ToolChoiceRequired || call.ToolChoice.Name != "get_weather" {
		t.Fatalf("choice=%+v", call.ToolChoice)
	}
}

func TestToolChoiceBridge_rejectsNamedNonRequiredModes(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   aipapi.ToolChoice
	}{
		{name: "auto named", in: aipapi.ToolChoice{Mode: aipapi.ToolChoiceAuto, Name: "weather"}},
		{name: "any named", in: aipapi.ToolChoice{Mode: aipapi.ToolChoiceAny, Name: "weather"}},
		{name: "none named", in: aipapi.ToolChoice{Mode: aipapi.ToolChoiceNone, Name: "weather"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tools := []aipapi.ToolDef{{Name: "weather", Parameters: []byte(`{"type":"object"}`)}}
			if tc.in.Mode == aipapi.ToolChoiceNone {
				tools = nil
			}
			call := aipapi.Call{
				Messages: []aipapi.Message{{
					Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")},
				}},
				Tools:      tools,
				ToolChoice: tc.in,
			}
			if err := call.Validate(); err == nil {
				t.Fatal("expected canonical validation error")
			}
		})
	}
}

func TestToolChoiceBridge_rejectsAutoNamedWire(t *testing.T) {
	t.Parallel()

	autoNamed := "auto:weather"
	_, err := backendplugin.ToolChoiceFromWire(&autoNamed)
	if err == nil {
		t.Fatal("expected wire rejection")
	}
	if !errors.Is(err, backendplugin.ErrInvalidInvocation) {
		t.Fatalf("err=%v", err)
	}
}

func TestToolChoiceBridge_invalidWireRejectedBeforeExecute(t *testing.T) {
	t.Parallel()

	bad := "not-a-mode"
	inv := backendplugin.Invocation{
		RequestID: "r", AttemptID: "a", ALegID: "aleg", BLegID: "bleg", CanonicalModelID: "m",
		Messages: []backendplugin.Message{{
			Role:  backendplugin.RoleUser,
			Parts: []backendplugin.Part{{Kind: backendplugin.PartKindText, Text: strPtr("hi")}},
		}},
		ToolChoice: &bad,
		Options:    backendplugin.GenerationOptions{ResponseSchemaJSON: backendplugin.RawJSONAbsentValue()},
	}
	if err := inv.Validate(); err == nil {
		t.Fatal("expected invalid tool choice")
	}
	_, err := backendplugin.CallFromInvocation(inv)
	if err == nil {
		t.Fatal("expected CallFromInvocation failure")
	}
	if !errors.Is(err, backendplugin.ErrInvalidInvocation) {
		t.Fatalf("err=%v", err)
	}
}

func TestToolChoiceBridge_rejectsWhitespacePaddedWire(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		wire string
	}{
		{name: "padded mode", wire: " none"},
		{name: "padded required prefix", wire: " required:fn"},
		{name: "padded required name", wire: "required: fn"},
		{name: "padded required name trailing", wire: "required:fn "},
		{name: "padded whole wire", wire: " required:fn "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			w := tc.wire
			_, err := backendplugin.ToolChoiceFromWire(&w)
			if err == nil {
				t.Fatal("expected wire rejection")
			}
			if !errors.Is(err, backendplugin.ErrInvalidInvocation) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestToolChoiceBridge_requiredWithAllowedToolsRejectedByABI(t *testing.T) {
	t.Parallel()

	tc := aipapi.ToolChoice{Mode: aipapi.ToolChoiceRequired, AllowedTools: []string{"get_a"}}
	err := backendplugin.ValidateToolChoiceABI(tc)
	if err == nil {
		t.Fatal("expected ABI rejection for required mode with allowed tools")
	}
	if !errors.Is(err, backendplugin.ErrInvalidInvocation) {
		t.Fatalf("err=%v", err)
	}

	// The public encoder has no error channel, so it cannot signal the invalid
	// combination. Document that it still emits an encoding the ABI decoder
	// rejects: callers must validate via ValidateToolChoiceABI (or canonical
	// ValidateToolChoice) before encoding so invalid Required+AllowedTools never
	// reaches the ABI.
	wire := backendplugin.ToolChoiceToWire(tc)
	if wire == nil || *wire != "allowed:required:get_a" {
		t.Fatalf("wire=%v", wire)
	}
	if _, err := backendplugin.ToolChoiceFromWire(wire); err == nil {
		t.Fatal("expected the emitted allowed:required wire to be rejected by the decoder")
	}
}

func TestToolChoiceBridge_rejectsCommaToolNameInABI(t *testing.T) {
	t.Parallel()

	cases := []aipapi.ToolChoice{
		{Mode: aipapi.ToolChoiceAuto, AllowedTools: []string{"get,a"}},
		{Mode: aipapi.ToolChoiceAny, AllowedTools: []string{"get_a,b"}},
	}
	for _, tc := range cases {
		if err := backendplugin.ValidateToolChoiceABI(tc); err == nil {
			t.Fatalf("expected ABI rejection for comma-containing allowed tool name %v", tc.AllowedTools)
		} else if !errors.Is(err, backendplugin.ErrInvalidInvocation) {
			t.Fatalf("err=%v", err)
		}
	}
}

func TestToolChoiceBridge_whitespacePaddedCanonicalRejectedBeforeRoundTrip(t *testing.T) {
	t.Parallel()

	tools := []aipapi.ToolDef{{Name: "fn", Parameters: []byte(`{"type":"object"}`)}}
	call := aipapi.Call{
		Messages: []aipapi.Message{{
			Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
		Tools:      tools,
		ToolChoice: aipapi.ToolChoice{Mode: aipapi.ToolChoiceRequired, Name: " fn"},
	}
	if err := call.Validate(); err == nil {
		t.Fatal("expected canonical rejection before ABI encode")
	}

	wire := "required: fn"
	inv := backendplugin.Invocation{
		RequestID: "r", AttemptID: "a", ALegID: "aleg", BLegID: "bleg", CanonicalModelID: "m",
		Messages: []backendplugin.Message{{
			Role:  backendplugin.RoleUser,
			Parts: []backendplugin.Part{{Kind: backendplugin.PartKindText, Text: strPtr("hi")}},
		}},
		Tools: []backendplugin.ToolDef{{
			Name: "fn", ParametersJSON: backendplugin.RawJSONFromBytes([]byte(`{"type":"object"}`)),
		}},
		ToolChoice: &wire,
		Options:    backendplugin.GenerationOptions{ResponseSchemaJSON: backendplugin.RawJSONAbsentValue()},
	}
	if err := inv.Validate(); err == nil {
		t.Fatal("expected ABI validation rejection")
	}
}
