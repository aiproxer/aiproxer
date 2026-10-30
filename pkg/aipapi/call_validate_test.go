package aipapi_test

import (
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestCallValidate_requiresMessages(t *testing.T) {
	t.Parallel()

	call := aipapi.Call{}
	err := call.Validate()
	if err == nil {
		t.Fatal("expected error")
	}
	if _, ok := errors.AsType[*aipapi.ValidationError](err); !ok {
		t.Fatalf("expected ValidationError, got %T", err)
	}
	if !errors.Is(err, aipapi.ErrInvalidCall) {
		t.Fatalf("expected wrap of ErrInvalidCall: %v", err)
	}
}

func TestCallValidate_messageRoleAndParts(t *testing.T) {
	t.Parallel()

	call := aipapi.Call{
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{}},
		},
	}
	if err := call.Validate(); err == nil {
		t.Fatal("expected error for empty parts")
	}

	call = aipapi.Call{
		Messages: []aipapi.Message{
			{Role: "", Parts: []aipapi.Part{aipapi.TextPart("hi")}},
		},
	}
	if err := call.Validate(); err == nil {
		t.Fatal("expected error for missing role")
	}
}

func TestCallValidate_textPartInvariant(t *testing.T) {
	t.Parallel()

	call := aipapi.Call{
		Messages: []aipapi.Message{{
			Role: aipapi.RoleUser,
			Parts: []aipapi.Part{{
				Kind: aipapi.PartText,
				Text: "",
			}},
		}},
	}
	if err := call.Validate(); err == nil {
		t.Fatal("expected error for empty text")
	}
}

func TestCallValidate_toolChoiceNoneWithToolsRejected(t *testing.T) {
	t.Parallel()

	call := aipapi.Call{
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
		Tools: []aipapi.ToolDef{{Name: "x"}},
		ToolChoice: aipapi.ToolChoice{
			Mode: aipapi.ToolChoiceNone,
		},
	}
	if err := call.Validate(); err == nil {
		t.Fatal("expected unsupported combination error")
	}
}

func TestCallValidate_toolChoiceRequiredMissingTool(t *testing.T) {
	t.Parallel()

	call := aipapi.Call{
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
		Tools: []aipapi.ToolDef{{Name: "alpha"}},
		ToolChoice: aipapi.ToolChoice{
			Mode: aipapi.ToolChoiceRequired,
			Name: "beta",
		},
	}
	if err := call.Validate(); err == nil {
		t.Fatal("expected error for unknown tool name")
	}
}

func TestCallValidate_generationOptionBounds(t *testing.T) {
	t.Parallel()

	temp := 2.1
	call := aipapi.Call{
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
		Options: aipapi.GenerationOptions{Temperature: &temp},
	}
	if err := call.Validate(); err == nil {
		t.Fatal("expected temperature bound error")
	}

	temp = 0.5
	topP := 1.1
	call.Options = aipapi.GenerationOptions{Temperature: &temp, TopP: &topP}
	if err := call.Validate(); err == nil {
		t.Fatal("expected top_p bound error")
	}

	tooBig := math.MaxInt32 + 1
	call.Options = aipapi.GenerationOptions{MaxOutputTokens: &tooBig}
	if err := call.Validate(); err == nil {
		t.Fatal("expected max_output_tokens overflow error")
	}
}

func TestCallValidate_jsonPartMustBeValidJSON(t *testing.T) {
	t.Parallel()

	call := aipapi.Call{
		Messages: []aipapi.Message{{
			Role: aipapi.RoleUser,
			Parts: []aipapi.Part{{
				Kind:    aipapi.PartJSON,
				Content: json.RawMessage(`{`),
			}},
		}},
	}
	if err := call.Validate(); err == nil {
		t.Fatal("expected invalid json error")
	}
}

func TestCallValidate_routeSelectorMaxSize(t *testing.T) {
	t.Parallel()
	sel := strings.Repeat("a", aipapi.MaxRouteSelectorBytes+1)
	call := aipapi.Call{
		Route: aipapi.RouteIntent{Selector: sel},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	if err := call.Validate(); err == nil {
		t.Fatal("expected error for oversized selector")
	}
}

func TestCallValidate_toolParametersMustBeValidJSONWhenSet(t *testing.T) {
	t.Parallel()

	call := aipapi.Call{
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
		Tools: []aipapi.ToolDef{{
			Name:       "x",
			Parameters: json.RawMessage(`not-json`),
		}},
	}
	if err := call.Validate(); err == nil {
		t.Fatal("expected invalid tool parameters JSON")
	}
}

func TestSessionRef_CorrelationID_prefersAuthoritative(t *testing.T) {
	t.Parallel()
	if got := (aipapi.SessionRef{ClientSessionID: "c", AuthoritativeSessionID: "p"}).CorrelationID(); got != "p" {
		t.Fatalf("got %q", got)
	}
	if got := (aipapi.SessionRef{ClientSessionID: "c"}).CorrelationID(); got != "c" {
		t.Fatalf("got %q", got)
	}
}

func TestCallValidate_sessionLegacyFieldsCompatible(t *testing.T) {
	t.Parallel()
	call := aipapi.Call{
		Session: aipapi.SessionRef{
			ClientSessionID: "client-hint",
			ContinuityKey:   "continuity",
			ALegID:          "aleg-1",
		},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hello")},
		}},
	}
	if err := call.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCallValidate_sessionSecureFieldsSizeLimits(t *testing.T) {
	t.Parallel()
	base := aipapi.Call{
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hello")},
		}},
	}
	t.Run("oversized session id", func(t *testing.T) {
		t.Parallel()
		c := base
		c.Session.AuthoritativeSessionID = strings.Repeat("x", aipapi.MaxAuthoritativeSessionIDBytes+1)
		if err := c.Validate(); err == nil {
			t.Fatal("expected error")
		}
	})
	t.Run("oversized resume token", func(t *testing.T) {
		t.Parallel()
		c := base
		c.Session.ResumeToken = strings.Repeat("t", aipapi.MaxResumeTokenBytes+1)
		if err := c.Validate(); err == nil {
			t.Fatal("expected error")
		}
	})
	t.Run("secure fields ok at limits", func(t *testing.T) {
		t.Parallel()
		c := base
		c.Session.AuthoritativeSessionID = strings.Repeat("s", aipapi.MaxAuthoritativeSessionIDBytes)
		c.Session.ResumeToken = strings.Repeat("r", aipapi.MaxResumeTokenBytes)
		if err := c.Validate(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

func TestCallValidate_validMinimalCall(t *testing.T) {
	t.Parallel()

	call := aipapi.Call{
		ID: "req-1",
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hello")},
		}},
		Tools: []aipapi.ToolDef{{
			Name:        "do",
			Description: "d",
			Parameters:  json.RawMessage(`{"type":"object"}`),
		}},
		ToolChoice: aipapi.ToolChoice{Mode: aipapi.ToolChoiceRequired, Name: "do"},
	}
	if err := call.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCallValidate_toolChoiceNameOnlyAllowedWithRequired(t *testing.T) {
	t.Parallel()

	tools := []aipapi.ToolDef{{Name: "fn", Parameters: json.RawMessage(`{"type":"object"}`)}}
	messages := []aipapi.Message{{
		Role:  aipapi.RoleUser,
		Parts: []aipapi.Part{aipapi.TextPart("hi")},
	}}

	cases := []struct {
		name  string
		tc    aipapi.ToolChoice
		tools []aipapi.ToolDef
		field string
	}{
		{name: "auto named", tc: aipapi.ToolChoice{Mode: aipapi.ToolChoiceAuto, Name: "fn"}, tools: tools, field: "ToolChoice.Name"},
		{name: "any named", tc: aipapi.ToolChoice{Mode: aipapi.ToolChoiceAny, Name: "fn"}, tools: tools, field: "ToolChoice.Name"},
		{name: "none named", tc: aipapi.ToolChoice{Mode: aipapi.ToolChoiceNone, Name: "fn"}, tools: nil, field: "ToolChoice.Name"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			call := aipapi.Call{Messages: messages, Tools: tc.tools, ToolChoice: tc.tc}
			err := call.Validate()
			if err == nil {
				t.Fatal("expected validation error")
			}
			var v *aipapi.ValidationError
			if !errors.As(err, &v) || v.Field != tc.field {
				t.Fatalf("err=%v", err)
			}
		})
	}

	required := aipapi.Call{Messages: messages, Tools: tools, ToolChoice: aipapi.ToolChoice{
		Mode: aipapi.ToolChoiceRequired, Name: "fn",
	}}
	if err := required.Validate(); err != nil {
		t.Fatalf("required named should validate: %v", err)
	}
}

func TestCallValidate_allowedToolsCommaNameAcceptedInProcess(t *testing.T) {
	t.Parallel()

	// The comma limitation is an artifact of the plugin ABI comma-joined
	// encoding, not of the canonical model or the JSON protocol. A protocol Call
	// executing in-process (no plugin ABI) must keep accepting comma-containing
	// tool names, so canonical validation must not reject them.
	messages := []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}}}
	tools := []aipapi.ToolDef{{Name: "get,a", Parameters: []byte(`{"type":"object"}`)}}
	call := aipapi.Call{
		Messages:   messages,
		Tools:      tools,
		ToolChoice: aipapi.ToolChoice{Mode: aipapi.ToolChoiceAuto, AllowedTools: []string{"get,a"}},
	}
	if err := call.Validate(); err != nil {
		t.Fatalf("canonical validation must accept comma tool names for in-process use: %v", err)
	}
}

func TestCallValidate_rejectsWhitespacePaddedToolNames(t *testing.T) {
	t.Parallel()

	messages := []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}}}
	cases := []struct {
		name  string
		tools []aipapi.ToolDef
		field string
	}{
		{
			name:  "leading whitespace",
			tools: []aipapi.ToolDef{{Name: " fn", Parameters: []byte(`{"type":"object"}`)}},
			field: "Tools[0].Name",
		},
		{
			name:  "trailing whitespace",
			tools: []aipapi.ToolDef{{Name: "fn ", Parameters: []byte(`{"type":"object"}`)}},
			field: "Tools[0].Name",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			call := aipapi.Call{Messages: messages, Tools: tc.tools}
			err := call.Validate()
			if err == nil {
				t.Fatal("expected validation error")
			}
			var v *aipapi.ValidationError
			if !errors.As(err, &v) || v.Field != tc.field {
				t.Fatalf("err=%v", err)
			}
			if !strings.Contains(v.Message, "whitespace") {
				t.Fatalf("message=%q", v.Message)
			}
		})
	}
}

func TestCallValidate_rejectsWhitespacePaddedToolChoiceName(t *testing.T) {
	t.Parallel()

	messages := []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}}}
	tools := []aipapi.ToolDef{{Name: "fn", Parameters: []byte(`{"type":"object"}`)}}
	cases := []struct {
		name string
		tc   aipapi.ToolChoice
	}{
		{name: "leading whitespace", tc: aipapi.ToolChoice{Mode: aipapi.ToolChoiceRequired, Name: " fn"}},
		{name: "trailing whitespace", tc: aipapi.ToolChoice{Mode: aipapi.ToolChoiceRequired, Name: "fn "}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			call := aipapi.Call{Messages: messages, Tools: tools, ToolChoice: tc.tc}
			err := call.Validate()
			if err == nil {
				t.Fatal("expected validation error")
			}
			var v *aipapi.ValidationError
			if !errors.As(err, &v) || v.Field != "ToolChoice.Name" {
				t.Fatalf("err=%v", err)
			}
			if !strings.Contains(v.Message, "whitespace") {
				t.Fatalf("message=%q", v.Message)
			}
		})
	}
}

func TestCallValidate_allowedToolsSubset(t *testing.T) {
	t.Parallel()

	messages := []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}}}
	tools := []aipapi.ToolDef{{Name: "fn1", Parameters: []byte(`{"type":"object"}`)}, {Name: "fn2", Parameters: []byte(`{"type":"object"}`)}}

	valid := []aipapi.ToolChoice{
		{Mode: aipapi.ToolChoiceAuto, AllowedTools: []string{"fn1"}},
		{Mode: aipapi.ToolChoiceAny, AllowedTools: []string{"fn1", "fn2"}},
		// mode none + subset is schema-legal (the subset is vacuous) and must not
		// trip the plain "none with declared tools" incompatibility rule.
		{Mode: aipapi.ToolChoiceNone, AllowedTools: []string{"fn1"}},
	}
	for i, tc := range valid {
		call := aipapi.Call{Messages: messages, Tools: tools, ToolChoice: tc}
		if err := call.Validate(); err != nil {
			t.Fatalf("valid case %d (%+v) rejected: %v", i, tc, err)
		}
	}

	invalid := []struct {
		name  string
		tc    aipapi.ToolChoice
		field string
	}{
		{name: "required mode with subset", tc: aipapi.ToolChoice{Mode: aipapi.ToolChoiceRequired, AllowedTools: []string{"fn1"}}, field: "ToolChoice.Mode"},
		{name: "name with subset", tc: aipapi.ToolChoice{Mode: aipapi.ToolChoiceAuto, Name: "fn1", AllowedTools: []string{"fn1"}}, field: "ToolChoice.Name"},
		{name: "unknown tool not declared", tc: aipapi.ToolChoice{Mode: aipapi.ToolChoiceAuto, AllowedTools: []string{"fn_missing"}}, field: "ToolChoice.AllowedTools"},
		{name: "empty entry", tc: aipapi.ToolChoice{Mode: aipapi.ToolChoiceAuto, AllowedTools: []string{""}}, field: "ToolChoice.AllowedTools"},
		{name: "whitespace entry", tc: aipapi.ToolChoice{Mode: aipapi.ToolChoiceAuto, AllowedTools: []string{" fn1"}}, field: "ToolChoice.AllowedTools"},
		{name: "duplicate entries", tc: aipapi.ToolChoice{Mode: aipapi.ToolChoiceAuto, AllowedTools: []string{"fn1", "fn1"}}, field: "ToolChoice.AllowedTools"},
		{name: "subset with no tools declared", tc: aipapi.ToolChoice{Mode: aipapi.ToolChoiceAuto, AllowedTools: []string{"fn1"}}, field: "ToolChoice.AllowedTools"},
	}
	for _, tc := range invalid {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			callTools := tools
			if tc.name == "subset with no tools declared" {
				callTools = nil
			}
			call := aipapi.Call{Messages: messages, Tools: callTools, ToolChoice: tc.tc}
			err := call.Validate()
			if err == nil {
				t.Fatal("expected validation error")
			}
			var v *aipapi.ValidationError
			if !errors.As(err, &v) || v.Field != tc.field {
				t.Fatalf("err=%v", err)
			}
		})
	}
}
