package agentloopguard

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/controltool"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
)

func task72ValidMeta() controltool.Meta {
	return controltool.Meta{
		TraceID:      "trace-72",
		ALegID:       "a-leg-72",
		BLegID:       "b-leg-72",
		CandidateKey: "candidate-72",
		AttemptSeq:   1,
		Scope:        scope.PrincipalScopeView{PrincipalID: scope.Known("principal-72")},
		Session:      session.SessionView{ALegID: "a-leg-72"},
		Workspace:    workspace.WorkspaceView{ID: "workspace-72"},
	}
}

func task72Call(args string) controltool.CompletedCall {
	return controltool.CompletedCall{
		ToolCallID: "call-72",
		ToolName:   "attempt_completion",
		ArgsJSON:   []byte(args),
	}
}

func TestCompletionToolProviderIDAndSpec(t *testing.T) {
	t.Parallel()
	p := NewCompletionToolProvider()
	id := p.ID()
	if err := controltool.ValidateProviderID(id); err != nil {
		t.Fatalf("provider ID %q rejected: %v", id, err)
	}
	if id == "" || len(id) > controltool.MaxProviderIDBytes {
		t.Fatalf("provider ID %q is not stable bounded", id)
	}
	if err := controltool.ValidateProvider(p); err != nil {
		t.Fatalf("ValidateProvider: %v", err)
	}
	spec := p.Spec()
	if err := spec.Validate(); err != nil {
		t.Fatalf("Spec.Validate: %v", err)
	}
	frozen := completionToolSpec()
	if spec.Tool.Name != frozen.Tool.Name ||
		spec.Tool.Description != frozen.Tool.Description ||
		string(spec.Tool.Parameters) != string(frozen.Tool.Parameters) ||
		spec.Instruction != frozen.Instruction ||
		spec.MaxArgsBytes != frozen.MaxArgsBytes {
		t.Fatal("provider Spec does not reuse completionToolSpec exactly")
	}
	if spec.Tool.Name != "attempt_completion" {
		t.Fatalf("tool name = %q, want attempt_completion", spec.Tool.Name)
	}
	if spec.MaxArgsBytes != controltool.DefaultMaxArgsBytes {
		t.Fatalf("MaxArgsBytes = %d, want %d", spec.MaxArgsBytes, controltool.DefaultMaxArgsBytes)
	}
	// Owned schema bytes: mutating one Spec must not affect the next.
	first := p.Spec()
	first.Tool.Parameters[0] = '['
	second := p.Spec()
	if string(second.Tool.Parameters) != string(frozen.Tool.Parameters) {
		t.Fatal("Spec schema bytes are not owned per call")
	}
	// Zero-state: two constructions expose identical contracts.
	other := NewCompletionToolProvider()
	if other.ID() != id {
		t.Fatalf("provider ID not stable: %q vs %q", id, other.ID())
	}
}

func TestCompletionHandleValid(t *testing.T) {
	t.Parallel()
	p := NewCompletionToolProvider()
	cases := map[string]struct {
		args string
		want string
	}{
		"exact object":     {args: `{"result":"done"}`, want: "done"},
		"unicode":          {args: `{"result":"héllo 🌍 done"}`, want: "héllo 🌍 done"},
		"escaped quotes":   {args: `{"result":"say \"hi\" ok"}`, want: `say "hi" ok`},
		"trailing ws":      {args: "{\"result\":\"done\"}   \n\t  ", want: "done"},
		"padded preserved": {args: `{"result":"  padded  "}`, want: "  padded  "},
		"spaced json":      {args: `{ "result" : "spaced" }`, want: "spaced"},
		"escaped unicode":  {args: `{"result":"A"}`, want: "A"},
		"escaped key":      {args: "{\"\\u0072esult\":\"escaped-key-ok\"}", want: "escaped-key-ok"},
		"newline in value": {args: `{"result":"line1\nline2 ok"}`, want: "line1\nline2 ok"},
		"leading ws":       {args: "  \n\t " + `{"result":"led ok"}` + "  ", want: "led ok"},
		"slash escaped":    {args: `{"result":"a\/b ok"}`, want: "a/b ok"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			call := task72Call(tc.args)
			rawCopy := string(call.ArgsJSON)
			out, err := p.Handle(context.Background(), call, task72ValidMeta())
			if err != nil {
				t.Fatalf("Handle error = %v, want nil", err)
			}
			if out.Kind != controltool.OutcomeComplete {
				t.Fatalf("Kind = %v, want OutcomeComplete", out.Kind)
			}
			if out.ResultText != tc.want {
				t.Fatalf("ResultText = %q, want %q", out.ResultText, tc.want)
			}
			if err := out.Validate(); err != nil {
				t.Fatalf("Outcome.Validate: %v", err)
			}
			if string(call.ArgsJSON) != rawCopy {
				t.Fatal("Handle mutated input ArgsJSON")
			}
		})
	}
}

func TestCompletionHandleInvalidModelMistakes(t *testing.T) {
	t.Parallel()
	p := NewCompletionToolProvider()
	invalid := map[string]string{
		"missing":            `{}`,
		"empty object extra": `{"other":"x"}`,
		"empty result":       `{"result":""}`,
		"whitespace result":  `{"result":"   "}`,
		"null result":        `{"result":null}`,
		"bool result":        `{"result":true}`,
		"number result":      `{"result":123}`,
		"array result":       `{"result":[]}`,
		"object result":      `{"result":{}}`,
		"nested result":      `{"result":{"nested":"x"}}`,
		"wrong root array":   `[]`,
		"wrong root string":  `"hello"`,
		"wrong root number":  `123`,
		"wrong root null":    `null`,
		"wrong root bool":    `true`,
		"duplicate direct":   `{"result":"a","result":"b"}`,
		"duplicate escaped":  `{"result":"a","` + `\u0072esult":"b"}`,
		"unknown key":        `{"unknown":"x"}`,
		"case variant":       `{"Result":"a"}`,
		"upper variant":      `{"RESULT":"a"}`,
		"command key":        `{"command":"x","result":"a"}`,
		"command trailing":   `{"result":"a","command":"x"}`,
		"status key":         `{"result":"a","status":"ok"}`,
		"approval key":       `{"result":"a","approval":"yes"}`,
		"metadata key":       `{"result":"a","metadata":{}}`,
		"trailing value":     `{"result":"a"} {"result":"b"}`,
		"trailing garbage":   `{"result":"a"} garbage`,
		"trailing bracket":   `{"result":"a"}}`,
		"incomplete":         `{"result":"a"`,
		"incomplete value":   `{"result":`,
		"truncated open":     `{`,
		"empty bytes":        ``,
		"whitespace only":    "   \n\t  ",
		"decoded NUL":        `{"result":"a` + `\u0000` + `b"}`,
		"empty key":          `{"":"a"}`,
		"null key value":     `{"result":null,"extra":1}`,
		"two keys one valid": `{"result":"a","result2":"b"}`,
	}
	for name, args := range invalid {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			call := task72Call(args)
			rawCopy := string(call.ArgsJSON)
			out, err := p.Handle(context.Background(), call, task72ValidMeta())
			if err != nil {
				t.Fatalf("Handle error = %v, want nil for model mistake", err)
			}
			if out.Kind != controltool.OutcomeInvalid {
				t.Fatalf("Kind = %v, want OutcomeInvalid", out.Kind)
			}
			if out.ResultText != "" {
				t.Fatalf("ResultText = %q, want empty on invalid", out.ResultText)
			}
			if err := out.Validate(); err != nil {
				t.Fatalf("Outcome.Validate: %v", err)
			}
			if string(call.ArgsJSON) != rawCopy {
				t.Fatal("Handle mutated input ArgsJSON on invalid")
			}
		})
	}
}

func TestCompletionHandleRawUTF8AndNUL(t *testing.T) {
	t.Parallel()
	p := NewCompletionToolProvider()
	rawCases := [][]byte{
		{0xff, 0xfe},
		[]byte("{\"result\":\"ok"),
		append([]byte(`{"result":"`), 0xff),
		{0x00},
	}
	for i, raw := range rawCases {
		call := controltool.CompletedCall{ToolCallID: "call-72", ToolName: "attempt_completion", ArgsJSON: raw}
		out, err := p.Handle(context.Background(), call, task72ValidMeta())
		if err != nil {
			t.Fatalf("case %d: Handle error = %v, want nil", i, err)
		}
		if out.Kind != controltool.OutcomeInvalid || out.ResultText != "" {
			t.Fatalf("case %d: got %+v, want invalid empty", i, out)
		}
	}
	// Literal NUL byte inside the JSON string framing is malformed JSON.
	call := controltool.CompletedCall{ToolCallID: "call-72", ToolName: "attempt_completion", ArgsJSON: []byte("{\"result\":\"a\x00b\"}")}
	out, err := p.Handle(context.Background(), call, task72ValidMeta())
	if err != nil {
		t.Fatalf("NUL raw error = %v, want nil", err)
	}
	if out.Kind != controltool.OutcomeInvalid || out.ResultText != "" {
		t.Fatalf("NUL raw got %+v, want invalid empty", out)
	}
}

func TestCompletionHandleBoundaries(t *testing.T) {
	t.Parallel()
	p := NewCompletionToolProvider()
	// Fits exactly in the frozen args envelope.
	overhead := len(`{"result":""}`)
	fitLen := controltool.DefaultMaxArgsBytes - overhead
	fitArgs := `{"result":"` + strings.Repeat("r", fitLen) + `"}`
	if len(fitArgs) != controltool.DefaultMaxArgsBytes {
		t.Fatalf("fixture len = %d, want %d", len(fitArgs), controltool.DefaultMaxArgsBytes)
	}
	out, err := p.Handle(context.Background(), task72Call(fitArgs), task72ValidMeta())
	if err != nil {
		t.Fatalf("boundary fit error = %v", err)
	}
	if out.Kind != controltool.OutcomeComplete || out.ResultText != strings.Repeat("r", fitLen) {
		t.Fatalf("boundary fit: Kind=%v len=%d", out.Kind, len(out.ResultText))
	}
	// One byte over the envelope is a model mistake.
	overArgs := `{"result":"` + strings.Repeat("r", fitLen+1) + `"}`
	if len(overArgs) <= controltool.DefaultMaxArgsBytes {
		t.Fatal("oversized fixture must exceed the envelope")
	}
	out, err = p.Handle(context.Background(), task72Call(overArgs), task72ValidMeta())
	if err != nil {
		t.Fatalf("oversized error = %v, want nil invalid", err)
	}
	if out.Kind != controltool.OutcomeInvalid || out.ResultText != "" {
		t.Fatalf("oversized got %+v, want invalid empty", out)
	}
	// Result at the SDK text bound inside a small envelope is accepted.
	maxOK := `{"result":"` + strings.Repeat("s", 512) + `"}`
	out, err = p.Handle(context.Background(), task72Call(maxOK), task72ValidMeta())
	if err != nil || out.Kind != controltool.OutcomeComplete {
		t.Fatalf("512-byte result: out=%+v err=%v", out, err)
	}
	// Oversized decoded result beyond MaxResultTextBytes is invalid (args bound also trips first).
	huge := strings.Repeat("h", controltool.MaxResultTextBytes+1)
	out, err = p.Handle(context.Background(), task72Call(`{"result":"`+huge+`"}`), task72ValidMeta())
	if err != nil {
		t.Fatalf("huge result error = %v, want nil invalid", err)
	}
	if out.Kind != controltool.OutcomeInvalid || out.ResultText != "" {
		t.Fatalf("huge result got Kind=%v len=%d, want invalid empty", out.Kind, len(out.ResultText))
	}
}

func TestCompletionHandleContractBoundsFailClosed(t *testing.T) {
	t.Parallel()
	p := NewCompletionToolProvider()
	meta := task72ValidMeta()
	// Invalid identities fail closed with typed SDK errors, never OutcomeInvalid.
	badCalls := map[string]controltool.CompletedCall{
		"empty call id":  {ToolCallID: "", ToolName: "attempt_completion", ArgsJSON: []byte(`{"result":"ok"}`)},
		"blank call id":  {ToolCallID: "  ", ToolName: "attempt_completion", ArgsJSON: []byte(`{"result":"ok"}`)},
		"empty tool":     {ToolCallID: "call-72", ToolName: "", ArgsJSON: []byte(`{"result":"ok"}`)},
		"padded tool":    {ToolCallID: "call-72", ToolName: " attempt_completion ", ArgsJSON: []byte(`{"result":"ok"}`)},
		"oversized id":   {ToolCallID: strings.Repeat("c", controltool.MaxIdentifierBytes+1), ToolName: "attempt_completion", ArgsJSON: []byte(`{"result":"ok"}`)},
		"oversized name": {ToolCallID: "call-72", ToolName: strings.Repeat("n", 300), ArgsJSON: []byte(`{"result":"ok"}`)},
	}
	for name, call := range badCalls {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			out, err := p.Handle(context.Background(), call, meta)
			if !errors.Is(err, controltool.ErrInvalidCall) {
				t.Fatalf("err = %v, want ErrInvalidCall", err)
			}
			if out.ResultText != "" {
				t.Fatalf("ResultText = %q on contract failure, want empty", out.ResultText)
			}
			if strings.Contains(err.Error(), "private completion fixture marker") {
				t.Fatal("contract error echoes raw content")
			}
		})
	}
	badMeta := meta
	badMeta.AttemptSeq = -1
	if _, err := p.Handle(context.Background(), task72Call(`{"result":"ok"}`), badMeta); !errors.Is(err, controltool.ErrInvalidMeta) {
		t.Fatalf("meta err = %v, want ErrInvalidMeta", badMeta)
	}
	// Tool name never confers ownership: any non-empty name with valid args completes.
	for _, toolName := range []string{"attempt_completion", "other-tool", "session_marker"} {
		call := controltool.CompletedCall{ToolCallID: "call-72", ToolName: toolName, ArgsJSON: []byte(`{"result":"name-agnostic ok"}`)}
		out, err := p.Handle(context.Background(), call, meta)
		if err != nil || out.Kind != controltool.OutcomeComplete || out.ResultText != "name-agnostic ok" {
			t.Fatalf("tool %q: out=%+v err=%v", toolName, out, err)
		}
	}
}

func TestCompletionHandleContext(t *testing.T) {
	t.Parallel()
	p := NewCompletionToolProvider()
	call := task72Call(`{"result":"ctx ok"}`)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.Handle(canceled, call, task72ValidMeta()); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled err = %v, want context.Canceled", err)
	}
	deadline, cancel2 := context.WithDeadline(context.Background(), time.Now().Add(-1))
	defer cancel2()
	if _, err := p.Handle(deadline, call, task72ValidMeta()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline err = %v, want DeadlineExceeded", err)
	}
	// Nil context is tolerated without panic.
	out, err := p.Handle(nil, call, task72ValidMeta()) //nolint:staticcheck // The nil-context case is the behavior under test.
	if err != nil || out.Kind != controltool.OutcomeComplete {
		t.Fatalf("nil ctx: out=%+v err=%v", out, err)
	}
}

func TestCompletionHandlePrivacy(t *testing.T) {
	t.Parallel()
	p := NewCompletionToolProvider()
	privateResultText := "private completion fixture marker 72"
	args := `{"result":"` + privateResultText + `"}`
	out, err := p.Handle(context.Background(), task72Call(args), task72ValidMeta())
	if err != nil || out.Kind != controltool.OutcomeComplete {
		t.Fatalf("marker valid: out=%+v err=%v", out, err)
	}
	if out.ReasonCode == "" || strings.Contains(out.ReasonCode, privateResultText) {
		t.Fatalf("reason %q leaks or empty", out.ReasonCode)
	}
	if len(out.ReasonCode) > controltool.MaxReasonCodeBytes {
		t.Fatalf("reason %d bytes exceeds bound", len(out.ReasonCode))
	}
	// Invalid path carries no text and static reason.
	badArgs := `{"result":"` + privateResultText + `","extra":1}`
	out, err = p.Handle(context.Background(), task72Call(badArgs), task72ValidMeta())
	if err != nil {
		t.Fatalf("invalid err = %v, want nil", err)
	}
	if out.ResultText != "" {
		t.Fatal("invalid carries result text")
	}
	if strings.Contains(out.ReasonCode, privateResultText) || strings.Contains(out.ReasonCode, "extra") {
		t.Fatalf("invalid reason %q leaks content", out.ReasonCode)
	}
	// Contract error carries no raw args/result.
	_, err = p.Handle(context.Background(), controltool.CompletedCall{ToolCallID: "", ToolName: "attempt_completion", ArgsJSON: []byte(args)}, task72ValidMeta())
	if err == nil || strings.Contains(err.Error(), privateResultText) || strings.Contains(err.Error(), args) {
		t.Fatalf("contract error leaks: %v", err)
	}
	// Context error carries no raw result.
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = p.Handle(canceled, task72Call(args), task72ValidMeta())
	if err == nil || strings.Contains(err.Error(), privateResultText) {
		t.Fatalf("ctx error leaks: %v", err)
	}
}

func TestCompletionHandleTask71ABIUnchanged(t *testing.T) {
	t.Parallel()
	p := NewCompletionToolProvider()
	spec := p.Spec()
	if spec.Tool.Name != "attempt_completion" {
		t.Fatalf("tool name = %q", spec.Tool.Name)
	}
	if strings.Contains(string(spec.Tool.Parameters), "command") {
		t.Fatal("schema must not expose command")
	}
	if !utf8.ValidString(spec.Instruction.Text) {
		t.Fatal("instruction not valid UTF-8")
	}
	if !strings.HasSuffix(spec.Instruction.Text, "</task-completion-protocol>\n") {
		t.Fatal("instruction must keep final LF fence")
	}
	if err := controltool.ValidateProvider(p); err != nil {
		t.Fatalf("ValidateProvider: %v", err)
	}
}

func FuzzCompletionArgs(f *testing.F) {
	seeds := []string{
		`{"result":"done"}`,
		`{"result":"héllo 🌍"}`,
		`{"result":"say \"hi\""}`,
		`{"result":"done"}  `,
		`{}`,
		`{"result":""}`,
		`{"result":"   "}`,
		`{"result":null}`,
		`{"result":true}`,
		`{"result":123}`,
		`{"result":[]}`,
		`{"result":{}}`,
		`[]`,
		`{"result":"a","result":"b"}`,
		"{\"result\":\"a\",\"\\u0072esult\":\"b\"}",
		`{"Result":"a"}`,
		`{"result":"a","command":"x"}`,
		`{"result":"a"} {"result":"b"}`,
		`{"result":"a"} garbage`,
		`{"result":"a"`,
		``,
		"   ",
		`{"result":"a` + "`\u0000" + `b"}`,
		`{"unknown":"x"}`,
		"{\"\\u0072esult\":\"escaped-single-ok\"}",
		"{\"result\":\"\\ud800\"}",
		"{\"result\":\"\\ud83d\\ude00\"}",
		"[1,2]",
		`null`,
		`{"result":  "spaced"}`,
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	f.Add([]byte{0xff, 0xfe})
	f.Add([]byte(strings.Repeat("r", controltool.DefaultMaxArgsBytes+8)))
	f.Fuzz(func(t *testing.T, raw []byte) {
		result, ok := parseCompletionArgs(raw)
		if !ok {
			if result != "" {
				t.Fatalf("invalid must carry no text, got %q", result)
			}
			return
		}
		if !utf8.ValidString(result) {
			t.Fatal("valid result is not valid UTF-8")
		}
		if strings.ContainsRune(result, 0) {
			t.Fatal("valid result contains NUL")
		}
		if strings.TrimSpace(result) == "" {
			t.Fatal("valid result is blank")
		}
		if len(result) > controltool.MaxResultTextBytes {
			t.Fatalf("valid result %d bytes exceeds bound", len(result))
		}
		out := controltool.Outcome{Kind: controltool.OutcomeComplete, ResultText: result, ReasonCode: completionCompleteReason}
		if err := out.Validate(); err != nil {
			t.Fatalf("valid outcome rejected: %v", err)
		}
		if out.ReasonCode != completionCompleteReason {
			t.Fatalf("reason = %q, want static %q", out.ReasonCode, completionCompleteReason)
		}
	})
}
