package controltool

import (
	"context"
	"encoding/json"
	"errors"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
)

func TestProviderSurfaceIsNarrow(t *testing.T) {
	t.Parallel()
	providerType := reflect.TypeFor[Provider]()
	if providerType.NumMethod() != 3 {
		t.Fatalf("Provider exposes %d methods, want exactly ID, Spec, and Handle", providerType.NumMethod())
	}
	for _, methodName := range []string{"ID", "Spec", "Handle"} {
		if _, ok := providerType.MethodByName(methodName); !ok {
			t.Fatalf("Provider missing %s method", methodName)
		}
	}
	handle, _ := providerType.MethodByName("Handle")
	if handle.Type.NumIn() != 3 {
		t.Fatalf("Handle arity = %d, want context, CompletedCall, Meta", handle.Type.NumIn())
	}
	if handle.Type.In(0) != reflect.TypeFor[context.Context]() {
		t.Fatalf("Handle first argument type = %s, want context.Context", handle.Type.In(0))
	}
	if handle.Type.In(1) != reflect.TypeFor[CompletedCall]() {
		t.Fatalf("Handle second argument type = %s, want CompletedCall", handle.Type.In(1))
	}
	if handle.Type.In(2) != reflect.TypeFor[Meta]() {
		t.Fatalf("Handle third argument type = %s, want Meta", handle.Type.In(2))
	}
	if handle.Type.NumOut() != 2 {
		t.Fatalf("Handle results = %d, want Outcome and error", handle.Type.NumOut())
	}
	if handle.Type.Out(0) != reflect.TypeFor[Outcome]() {
		t.Fatalf("Handle first result type = %s, want Outcome", handle.Type.Out(0))
	}
	if handle.Type.Out(1) != reflect.TypeFor[error]() {
		t.Fatalf("Handle second result type = %s, want error", handle.Type.Out(1))
	}
}

func TestDefaultMaxArgsBytesMatchesExistingToolCallEnvelope(t *testing.T) {
	t.Parallel()
	const envelope = 64 * 1024
	if DefaultMaxArgsBytes != envelope {
		t.Fatalf("DefaultMaxArgsBytes = %d, want existing tool-call envelope %d", DefaultMaxArgsBytes, envelope)
	}
	if DefaultMaxArgsBytes > aipapi.MaxEventDeltaBytes {
		t.Fatalf("DefaultMaxArgsBytes = %d exceeds aipapi.MaxEventDeltaBytes = %d", DefaultMaxArgsBytes, aipapi.MaxEventDeltaBytes)
	}
}

func TestProviderIdentityIsStableAndBounded(t *testing.T) {
	t.Parallel()
	for _, id := range []string{"", "   ", strings.Repeat("p", MaxProviderIDBytes+1)} {
		if err := ValidateProviderID(id); !errors.Is(err, ErrInvalidProvider) {
			t.Fatalf("ValidateProviderID(%q) error = %v, want ErrInvalidProvider", id, err)
		}
	}
	if err := ValidateProviderID(strings.Repeat("p", MaxProviderIDBytes)); err != nil {
		t.Fatalf("maximum-sized provider identity rejected: %v", err)
	}
	if err := ValidateProviderID("proxy.control.example"); err != nil {
		t.Fatalf("bounded provider identity rejected: %v", err)
	}

	id, err := ProviderIdentity(stubProvider{id: "proxy.control.example", spec: validSpec()})
	if err != nil {
		t.Fatalf("ProviderIdentity() error = %v", err)
	}
	if id != "proxy.control.example" {
		t.Fatalf("ProviderIdentity() = %q, want proxy.control.example", id)
	}
}

func TestProviderIdentityRejectsTypedNilAndPanics(t *testing.T) {
	t.Parallel()
	var typedNil *stubProvider
	if _, err := ProviderIdentity(typedNil); !errors.Is(err, ErrInvalidProvider) {
		t.Fatalf("typed-nil ProviderIdentity error = %v, want ErrInvalidProvider", err)
	}
	if err := ValidateProvider(typedNil); !errors.Is(err, ErrInvalidProvider) {
		t.Fatalf("typed-nil ValidateProvider error = %v, want ErrInvalidProvider", err)
	}
	if _, err := ProviderIdentity(nil); !errors.Is(err, ErrInvalidProvider) {
		t.Fatalf("nil ProviderIdentity error = %v, want ErrInvalidProvider", err)
	}

	if _, err := ProviderIdentity(panicIDProvider{}); !errors.Is(err, ErrInvalidProvider) {
		t.Fatalf("panic ID ProviderIdentity error = %v, want ErrInvalidProvider", err)
	}
	if err := ValidateProvider(panicSpecProvider{id: "proxy.control.example"}); !errors.Is(err, ErrInvalidProvider) {
		t.Fatalf("panic Spec ValidateProvider error = %v, want ErrInvalidProvider", err)
	}
}

func TestValidateSpecAcceptsBoundedFrozenToolContract(t *testing.T) {
	t.Parallel()
	spec := validSpec()
	if err := spec.Validate(); err != nil {
		t.Fatalf("valid spec rejected: %v", err)
	}
	if spec.Tool.Name != "proxy_control" {
		t.Fatalf("fixture tool name = %q, want generic proxy_control", spec.Tool.Name)
	}
	if spec.MaxArgsBytes != DefaultMaxArgsBytes {
		t.Fatalf("fixture MaxArgsBytes = %d, want DefaultMaxArgsBytes", spec.MaxArgsBytes)
	}
}

func TestValidateSpecRejectsToolIdentityAndSchemaProblems(t *testing.T) {
	t.Parallel()
	cases := map[string]func(*Spec){
		"empty tool name":      func(s *Spec) { s.Tool.Name = "" },
		"whitespace tool name": func(s *Spec) { s.Tool.Name = "  proxy_control  " },
		"oversized tool name": func(s *Spec) {
			s.Tool.Name = strings.Repeat("n", aipapi.MaxToolNameBytes+1)
		},
		"oversized description": func(s *Spec) {
			s.Tool.Description = strings.Repeat("d", aipapi.MaxToolDescriptionBytes+1)
		},
		"missing parameters": func(s *Spec) { s.Tool.Parameters = nil },
		"empty parameters":   func(s *Spec) { s.Tool.Parameters = []byte{} },
		"non-json parameters": func(s *Spec) {
			s.Tool.Parameters = []byte("not-json")
		},
		"array schema": func(s *Spec) {
			s.Tool.Parameters = json.RawMessage(`[]`)
		},
		"string schema": func(s *Spec) {
			s.Tool.Parameters = json.RawMessage(`"object"`)
		},
		"unknown schema type": func(s *Spec) {
			s.Tool.Parameters = json.RawMessage(`{"type":"widget"}`)
		},
		"non-object schema type": func(s *Spec) {
			s.Tool.Parameters = json.RawMessage(`{"type":"string"}`)
		},
		"remote ref": func(s *Spec) {
			s.Tool.Parameters = json.RawMessage(`{"$ref":"https://example.invalid/schema"}`)
		},
		"local ref": func(s *Spec) {
			s.Tool.Parameters = json.RawMessage(`{"$ref":"#"}`)
		},
		"oversized schema": func(s *Spec) {
			s.Tool.Parameters = json.RawMessage(`{"type":"object","description":"` + strings.Repeat("x", aipapi.MaxToolParametersBytes) + `"}`)
		},
		"trailing json": func(s *Spec) {
			s.Tool.Parameters = json.RawMessage(`{"type":"object"}{"type":"object"}`)
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			spec := validSpec()
			mutate(&spec)
			if err := spec.Validate(); !errors.Is(err, ErrInvalidSpec) {
				t.Fatalf("Validate() error = %v, want ErrInvalidSpec", err)
			}
		})
	}
}

func TestValidateSpecEnforcesArgsBudgetEnvelope(t *testing.T) {
	t.Parallel()
	spec := validSpec()
	spec.MaxArgsBytes = 0
	if err := spec.Validate(); !errors.Is(err, ErrInvalidSpec) {
		t.Fatalf("zero MaxArgsBytes error = %v, want ErrInvalidSpec", err)
	}
	spec = validSpec()
	spec.MaxArgsBytes = -1
	if err := spec.Validate(); !errors.Is(err, ErrInvalidSpec) {
		t.Fatalf("negative MaxArgsBytes error = %v, want ErrInvalidSpec", err)
	}
	spec = validSpec()
	spec.MaxArgsBytes = DefaultMaxArgsBytes + 1
	if err := spec.Validate(); !errors.Is(err, ErrInvalidSpec) {
		t.Fatalf("over-envelope MaxArgsBytes error = %v, want ErrInvalidSpec", err)
	}
	spec = validSpec()
	spec.MaxArgsBytes = 1
	if err := spec.Validate(); err != nil {
		t.Fatalf("minimum MaxArgsBytes rejected: %v", err)
	}
}

func TestValidateInstructionAcceptsSystemAndDeveloperOnly(t *testing.T) {
	t.Parallel()
	for _, role := range []aipapi.Role{aipapi.RoleSystem, aipapi.RoleDeveloper} {
		inst := Instruction{Role: role, Text: "Call proxy_control when the assigned work is finished."}
		if err := inst.Validate(); err != nil {
			t.Fatalf("role %q rejected: %v", role, err)
		}
	}
	for _, role := range []aipapi.Role{"", aipapi.RoleUser, aipapi.RoleAssistant, aipapi.RoleTool, aipapi.Role("unknown")} {
		inst := Instruction{Role: role, Text: "Call proxy_control when the assigned work is finished."}
		if err := inst.Validate(); !errors.Is(err, ErrInvalidSpec) {
			t.Fatalf("role %q error = %v, want ErrInvalidSpec", role, err)
		}
	}
}

func TestValidateInstructionBounds(t *testing.T) {
	t.Parallel()
	inst := Instruction{Role: aipapi.RoleSystem, Text: strings.Repeat("i", MaxInstructionBytes)}
	if err := inst.Validate(); err != nil {
		t.Fatalf("maximum instruction rejected: %v", err)
	}
	inst.Text += "x"
	if err := inst.Validate(); !errors.Is(err, ErrInvalidSpec) {
		t.Fatalf("over-bound instruction error = %v, want ErrInvalidSpec", err)
	}
	inst = Instruction{Role: aipapi.RoleSystem, Text: "   "}
	if err := inst.Validate(); !errors.Is(err, ErrInvalidSpec) {
		t.Fatalf("blank instruction error = %v, want ErrInvalidSpec", err)
	}
	inst = Instruction{Role: aipapi.RoleSystem, Text: "ok\x00hidden"}
	if err := inst.Validate(); !errors.Is(err, ErrInvalidSpec) {
		t.Fatalf("NUL instruction error = %v, want ErrInvalidSpec", err)
	}
	inst = Instruction{Role: aipapi.RoleSystem, Text: string([]byte{0xff, 0xfe})}
	if utf8.ValidString(inst.Text) {
		t.Fatal("fixture must be invalid UTF-8")
	}
	if err := inst.Validate(); !errors.Is(err, ErrInvalidSpec) {
		t.Fatalf("invalid UTF-8 instruction error = %v, want ErrInvalidSpec", err)
	}
}

func TestValidateCompletedCallTreatsToolNameAsData(t *testing.T) {
	t.Parallel()
	call := CompletedCall{
		ToolCallID: "call-1",
		ToolName:   "proxy_control",
		ArgsJSON:   []byte(`{"note":"done"}`),
	}
	if err := call.Validate(DefaultMaxArgsBytes); err != nil {
		t.Fatalf("valid call rejected: %v", err)
	}
	// Ownership is not inferred from the name; another generic name remains just data.
	call.ToolName = "session_marker"
	if err := call.Validate(DefaultMaxArgsBytes); err != nil {
		t.Fatalf("generic tool name %q rejected: %v", call.ToolName, err)
	}
}

func TestValidateCompletedCallEnforcesIdentityAndArgsBounds(t *testing.T) {
	t.Parallel()
	cases := map[string]func(*CompletedCall){
		"empty call id": func(c *CompletedCall) { c.ToolCallID = "" },
		"blank call id": func(c *CompletedCall) { c.ToolCallID = "  " },
		"oversized call id": func(c *CompletedCall) {
			c.ToolCallID = strings.Repeat("c", MaxIdentifierBytes+1)
		},
		"empty tool name":      func(c *CompletedCall) { c.ToolName = "" },
		"whitespace tool name": func(c *CompletedCall) { c.ToolName = " proxy_control " },
		"oversized tool name": func(c *CompletedCall) {
			c.ToolName = strings.Repeat("n", aipapi.MaxToolNameBytes+1)
		},
		"oversized args": func(c *CompletedCall) {
			c.ArgsJSON = []byte(strings.Repeat("a", DefaultMaxArgsBytes+1))
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			call := CompletedCall{
				ToolCallID: "call-1",
				ToolName:   "proxy_control",
				ArgsJSON:   []byte(`{"note":"done"}`),
			}
			mutate(&call)
			if err := call.Validate(DefaultMaxArgsBytes); !errors.Is(err, ErrInvalidCall) {
				t.Fatalf("Validate() error = %v, want ErrInvalidCall", err)
			}
		})
	}

	call := CompletedCall{
		ToolCallID: "call-1",
		ToolName:   "proxy_control",
		ArgsJSON:   []byte(strings.Repeat("a", 8)),
	}
	if err := call.Validate(4); !errors.Is(err, ErrInvalidCall) {
		t.Fatalf("spec-capped args error = %v, want ErrInvalidCall", err)
	}
}

func TestValidateMetaBoundsIdentifiers(t *testing.T) {
	t.Parallel()
	meta := validMeta()
	if err := meta.Validate(); err != nil {
		t.Fatalf("valid meta rejected: %v", err)
	}
	cases := map[string]func(*Meta){
		"oversized trace": func(m *Meta) { m.TraceID = strings.Repeat("t", MaxIdentifierBytes+1) },
		"oversized a-leg": func(m *Meta) { m.ALegID = strings.Repeat("a", MaxIdentifierBytes+1) },
		"oversized b-leg": func(m *Meta) { m.BLegID = strings.Repeat("b", MaxIdentifierBytes+1) },
		"oversized candidate": func(m *Meta) {
			m.CandidateKey = strings.Repeat("k", MaxIdentifierBytes+1)
		},
		"negative attempt":   func(m *Meta) { m.AttemptSeq = -1 },
		"invalid utf8 trace": func(m *Meta) { m.TraceID = string([]byte{0xff}) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			meta := validMeta()
			mutate(&meta)
			if err := meta.Validate(); !errors.Is(err, ErrInvalidMeta) {
				t.Fatalf("Validate() error = %v, want ErrInvalidMeta", err)
			}
		})
	}
}

func TestValidateOutcomeCompleteRequiresBoundedResultText(t *testing.T) {
	t.Parallel()
	out := Outcome{Kind: OutcomeComplete, ResultText: "work finished", ReasonCode: "complete"}
	if err := out.Validate(); err != nil {
		t.Fatalf("valid complete outcome rejected: %v", err)
	}
	out.ResultText = strings.Repeat("r", MaxResultTextBytes)
	if err := out.Validate(); err != nil {
		t.Fatalf("maximum result text rejected: %v", err)
	}
	cases := map[string]Outcome{
		"empty result": {Kind: OutcomeComplete, ResultText: "", ReasonCode: "complete"},
		"blank result": {Kind: OutcomeComplete, ResultText: "  ", ReasonCode: "complete"},
		"nul result":   {Kind: OutcomeComplete, ResultText: "ok\x00", ReasonCode: "complete"},
		"oversized result": {
			Kind:       OutcomeComplete,
			ResultText: strings.Repeat("r", MaxResultTextBytes+1),
			ReasonCode: "complete",
		},
		"missing reason": {Kind: OutcomeComplete, ResultText: "work finished"},
	}
	for name, outcome := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if err := outcome.Validate(); !errors.Is(err, ErrInvalidOutcome) {
				t.Fatalf("Validate() error = %v, want ErrInvalidOutcome", err)
			}
		})
	}
}

func TestValidateOutcomeInvalidCarriesNoClientOutput(t *testing.T) {
	t.Parallel()
	out := Outcome{Kind: OutcomeInvalid, ReasonCode: "args_too_large"}
	if err := out.Validate(); err != nil {
		t.Fatalf("valid invalid outcome rejected: %v", err)
	}
	out.ResultText = "should not leak to the client"
	if err := out.Validate(); !errors.Is(err, ErrInvalidOutcome) {
		t.Fatalf("client output on invalid error = %v, want ErrInvalidOutcome", err)
	}
	out = Outcome{Kind: OutcomeInvalid, ResultText: " ", ReasonCode: "malformed"}
	if err := out.Validate(); !errors.Is(err, ErrInvalidOutcome) {
		t.Fatalf("whitespace client output error = %v, want ErrInvalidOutcome", err)
	}
}

func TestValidateOutcomeReasonCodesBoundedAndContentFree(t *testing.T) {
	t.Parallel()
	ok := Outcome{Kind: OutcomeInvalid, ReasonCode: "malformed_json"}
	if err := ok.Validate(); err != nil {
		t.Fatalf("content-free reason rejected: %v", err)
	}
	ok.ReasonCode = strings.Repeat("a", MaxReasonCodeBytes)
	if err := ok.Validate(); err != nil {
		t.Fatalf("maximum reason code rejected: %v", err)
	}
	for _, reason := range []string{"", " ", "has space", "user said hello", "not/a/token", "résumé", strings.Repeat("a", MaxReasonCodeBytes+1)} {
		out := Outcome{Kind: OutcomeInvalid, ReasonCode: reason}
		if err := out.Validate(); !errors.Is(err, ErrInvalidOutcome) {
			t.Fatalf("reason %q error = %v, want ErrInvalidOutcome", reason, err)
		}
	}
}

func TestValidateOutcomeRejectsUnknownKind(t *testing.T) {
	t.Parallel()
	out := Outcome{Kind: OutcomeKind(99), ResultText: "x", ReasonCode: "complete"}
	if err := out.Validate(); !errors.Is(err, ErrInvalidOutcome) {
		t.Fatalf("unknown kind error = %v, want ErrInvalidOutcome", err)
	}
}

func TestValidateProviderAcceptsCompleteContract(t *testing.T) {
	t.Parallel()
	provider := stubProvider{id: "proxy.control.example", spec: validSpec()}
	if err := ValidateProvider(provider); err != nil {
		t.Fatalf("valid provider rejected: %v", err)
	}
	bad := stubProvider{id: "proxy.control.example", spec: validSpec()}
	bad.spec.Tool.Name = ""
	if err := ValidateProvider(bad); !errors.Is(err, ErrInvalidSpec) {
		t.Fatalf("invalid spec error = %v, want ErrInvalidSpec", err)
	}
}

func TestProductionSourcesStayGeneric(t *testing.T) {
	t.Parallel()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	banned := []string{
		"attempt" + "_" + "completion",
		"agent" + "loopguard",
		"agent" + "-loop-guard",
		"terminal" + "decision",
	}
	fset := token.NewFileSet()
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		body, err := os.ReadFile(filepath.Join(".", entry.Name()))
		if err != nil {
			t.Fatalf("ReadFile %s: %v", entry.Name(), err)
		}
		lower := strings.ToLower(string(body))
		for _, tokenText := range banned {
			if strings.Contains(lower, tokenText) {
				t.Fatalf("%s contains forbidden token %q", entry.Name(), tokenText)
			}
		}
		file, err := parser.ParseFile(fset, entry.Name(), body, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", entry.Name(), err)
		}
		for _, spec := range file.Imports {
			path := strings.Trim(spec.Path.Value, `"`)
			if !allowedProductionImport(path) {
				t.Fatalf("%s imports %s, which is outside the generic contract", entry.Name(), path)
			}
		}
	}
}

func allowedProductionImport(path string) bool {
	switch path {
	case "context", "encoding/json", "errors", "fmt", "io", "reflect", "strings", "unicode", "unicode/utf8", "bytes":
		return true
	case "github.com/aiproxer/aiproxer/pkg/aipapi",
		"github.com/aiproxer/aiproxer/pkg/aipsdk/scope",
		"github.com/aiproxer/aiproxer/pkg/aipsdk/session",
		"github.com/aiproxer/aiproxer/pkg/aipsdk/workspace":
		return true
	default:
		return false
	}
}

func validSpec() Spec {
	return Spec{
		Tool: aipapi.ToolDef{
			Name:        "proxy_control",
			Description: "Call this only when the assigned proxy-local control action is complete.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"note": {
						"type": "string",
						"description": "Bounded control result."
					}
				},
				"required": ["note"],
				"additionalProperties": false
			}`),
		},
		Instruction: Instruction{
			Role: aipapi.RoleSystem,
			Text: "Call proxy_control only when the assigned work is complete.",
		},
		MaxArgsBytes: DefaultMaxArgsBytes,
	}
}

func validMeta() Meta {
	return Meta{
		TraceID:      "trace-1",
		ALegID:       "a-leg-1",
		BLegID:       "b-leg-1",
		CandidateKey: "candidate-1",
		AttemptSeq:   1,
		Scope:        scope.PrincipalScopeView{PrincipalID: scope.Known("principal-1")},
		Session:      session.SessionView{ALegID: "a-leg-1"},
		Workspace:    workspace.WorkspaceView{ID: "workspace-1"},
	}
}

type stubProvider struct {
	id   string
	spec Spec
}

func (p stubProvider) ID() string { return p.id }
func (p stubProvider) Spec() Spec { return p.spec }
func (p stubProvider) Handle(context.Context, CompletedCall, Meta) (Outcome, error) {
	return Outcome{Kind: OutcomeComplete, ResultText: "ok", ReasonCode: "complete"}, nil
}

type panicIDProvider struct{}

func (panicIDProvider) ID() string { panic("id unavailable") }
func (panicIDProvider) Spec() Spec { return Spec{} }
func (panicIDProvider) Handle(context.Context, CompletedCall, Meta) (Outcome, error) {
	return Outcome{}, nil
}

type panicSpecProvider struct{ id string }

func (p panicSpecProvider) ID() string { return p.id }
func (panicSpecProvider) Spec() Spec   { panic("spec unavailable") }
func (panicSpecProvider) Handle(context.Context, CompletedCall, Meta) (Outcome, error) {
	return Outcome{}, nil
}
