package openairesponses_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openairesponses"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// defaultCodexNamespaceTools is the observed default Codex namespace shape:
// one top-level function tool plus one namespace declaration grouping five
// function members. The description text is deliberately distinctive so a
// fold test can prove it reaches every member.
const defaultCodexNamespaceTools = `[
	{"type":"function","name":"exec_command","description":"RUN","parameters":{"type":"object","properties":{"cmd":{"type":"string"}}}},
	{"type":"namespace","name":"multi_agent_v1","description":"NS-DESC","tools":[
		{"type":"function","name":"close_agent","description":"CLOSE-DESC","parameters":{"type":"object","properties":{"target":{"type":"string"}}}},
		{"type":"function","name":"resume_agent","description":"RESUME-DESC","parameters":{"type":"object"}}
	]},
	{"type":"function","name":"lookup","description":"LOOK","parameters":{"type":"object"}}
]`

func decodeNamespaceRequest(t *testing.T, body string, opts openairesponses.DecodeOptions) (*openairesponses.DecodedCreate, error) {
	t.Helper()
	if opts.RouteSelector == "" {
		opts.RouteSelector = "stub:gpt-4o-mini"
	}
	return openairesponses.DecodeCreateRequest([]byte(body), opts)
}

func TestDecodeCreate_NamespaceToolsProjectMembers(t *testing.T) {
	t.Parallel()
	body := `{"model":"gpt-4o-mini","input":"hi","tools":` + defaultCodexNamespaceTools + `,"tool_choice":{"type":"function","function":{"name":"close_agent"}}}`
	d, err := decodeNamespaceRequest(t, body, openairesponses.DecodeOptions{})
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	wantNames := []string{"exec_command", "multi_agent_v1__close_agent", "multi_agent_v1__resume_agent", "lookup"}
	gotNames := make([]string, len(d.Call.Tools))
	for i, tool := range d.Call.Tools {
		gotNames[i] = tool.Name
	}
	if !slices.Equal(gotNames, wantNames) {
		t.Fatalf("projected tool names = %v, want %v", gotNames, wantNames)
	}
	if got, want := d.Call.Tools[1].Description, "NS-DESC\n\nCLOSE-DESC"; got != want {
		t.Fatalf("folded namespace member description = %q, want %q", got, want)
	}
	if got, want := string(d.Call.Tools[1].Parameters), `{"type":"object","properties":{"target":{"type":"string"}}}`; got != want {
		t.Fatalf("member parameters = %s, want %s", got, want)
	}
	if d.Call.ToolChoice.Mode != aipapi.ToolChoiceRequired || d.Call.ToolChoice.Name != "multi_agent_v1__close_agent" {
		t.Fatalf("tool choice = %+v, want required projected member", d.Call.ToolChoice)
	}
	if err := d.Call.Validate(); err != nil {
		t.Fatalf("the decoded call is not canonical: %v", err)
	}
}

func TestDecodeCreate_NamespaceDescriptionFoldOnlyWhenPresent(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name          string
		namespaceDesc string
		memberDesc    string
		want          string
	}{
		{name: "both", namespaceDesc: "NS", memberDesc: "M", want: "NS\n\nM"},
		{name: "namespace only", namespaceDesc: "NS", memberDesc: "", want: "NS"},
		{name: "member only", namespaceDesc: "", memberDesc: "M", want: "M"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tools := `[{"type":"namespace","name":"a","description":"` + tc.namespaceDesc + `","tools":[{"type":"function","name":"f","description":"` + tc.memberDesc + `","parameters":{"type":"object"}}]}]`
			d, err := decodeNamespaceRequest(t, `{"model":"gpt-4o-mini","input":"hi","tools":`+tools+`}`, openairesponses.DecodeOptions{})
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if got := d.Call.Tools[0].Description; got != tc.want {
				t.Fatalf("description = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDecodeCreate_NamespaceHistoryPreserved(t *testing.T) {
	t.Parallel()
	body := `{
		"model":"gpt-4o-mini",
		"tools":[{"type":"namespace","name":"multi_agent_v1","description":"NS","tools":[{"type":"function","name":"close_agent","parameters":{"type":"object"}}]}],
		"input":[
			{"type":"message","role":"user","content":"hi"},
			{"type":"function_call","id":"fc_1","call_id":"c1","name":"close_agent","namespace":"multi_agent_v1","arguments":"{\"target\":\"nope\"}"},
			{"type":"function_call_output","call_id":"c1","name":"close_agent","namespace":"multi_agent_v1","output":"err"},
			{"type":"message","role":"user","content":"again"}
		]
	}`
	d, err := decodeNamespaceRequest(t, body, openairesponses.DecodeOptions{})
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(d.Call.Messages) != 4 {
		t.Fatalf("history messages = %d, want 4 (no item lost or reordered)", len(d.Call.Messages))
	}
	callPart := d.Call.Messages[1].Parts[0]
	if d.Call.Messages[1].Role != aipapi.RoleAssistant || callPart.Kind != aipapi.PartJSON {
		t.Fatalf("history call message = %+v", d.Call.Messages[1])
	}
	if callPart.ToolCallID != "c1" || callPart.ToolName != "multi_agent_v1__close_agent" {
		t.Fatalf("history call identity = id %q name %q", callPart.ToolCallID, callPart.ToolName)
	}
	if string(callPart.Content) != `{"target":"nope"}` {
		t.Fatalf("history call arguments = %s", callPart.Content)
	}
	resultPart := d.Call.Messages[2].Parts[0]
	if d.Call.Messages[2].Role != aipapi.RoleTool || resultPart.Kind != aipapi.PartToolResult || resultPart.ToolCallID != "c1" {
		t.Fatalf("history result = %+v / %+v", d.Call.Messages[2], resultPart)
	}
	if string(resultPart.Content) != `"err"` {
		t.Fatalf("history result content = %s", resultPart.Content)
	}
}

func TestDecodeCreate_NamespaceHistoryUndeclaredPreserved(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		tools string
		want  string
	}{
		{name: "no namespace declaration", tools: `[{"type":"function","name":"x","parameters":{"type":"object"}}]`, want: "multi_agent_v1__close_agent"},
		{name: "different member", tools: `[{"type":"namespace","name":"multi_agent_v1","tools":[{"type":"function","name":"resume_agent","parameters":{"type":"object"}}]}]`, want: "multi_agent_v1__close_agent"},
		{name: "different namespace", tools: `[{"type":"namespace","name":"other","tools":[{"type":"function","name":"close_agent","parameters":{"type":"object"}}]}]`, want: "multi_agent_v1__close_agent"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			body := `{"model":"gpt-4o-mini","tools":` + tc.tools + `,"input":[{"type":"function_call","call_id":"c1","name":"close_agent","namespace":"multi_agent_v1","arguments":"{}"}]}`
			d, err := decodeNamespaceRequest(t, body, openairesponses.DecodeOptions{})
			if err != nil {
				t.Fatalf("undeclared namespaced history must be preserved, got %v", err)
			}
			part := d.Call.Messages[0].Parts[0]
			if part.ToolCallID != "c1" || part.ToolName != tc.want {
				t.Fatalf("history call = id %q name %q, want id c1 name %q", part.ToolCallID, part.ToolName, tc.want)
			}
		})
	}
}

func TestDecodeCreate_NamespaceHistoryCollisionRejected(t *testing.T) {
	t.Parallel()
	body := `{
		"model":"gpt-4o-mini",
		"tools":[{"type":"function","name":"multi_agent_v1__close_agent","parameters":{"type":"object"}}],
		"input":[{"type":"function_call","call_id":"c1","name":"close_agent","namespace":"multi_agent_v1","arguments":"{}"}]
	}`
	_, err := decodeNamespaceRequest(t, body, openairesponses.DecodeOptions{})
	if err == nil {
		t.Fatal("an undeclared history projection that collides with a declared name must be refused")
	}
	if !strings.Contains(err.Error(), "already declared") {
		t.Fatalf("collision refusal must name the conflict: %v", err)
	}
}

func TestDecodeCreate_NamespaceHistoryWithoutNamespaceStaysFlat(t *testing.T) {
	t.Parallel()
	body := `{
		"model":"gpt-4o-mini",
		"tools":[{"type":"namespace","name":"multi_agent_v1","tools":[{"type":"function","name":"close_agent","parameters":{"type":"object"}}]}],
		"input":[
			{"type":"function_call","call_id":"c1","name":"ordinary","arguments":"{}"},
			{"type":"function_call_output","call_id":"c1","output":"ok"}
		]
	}`
	d, err := decodeNamespaceRequest(t, body, openairesponses.DecodeOptions{})
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	part := d.Call.Messages[0].Parts[0]
	if part.ToolName != "ordinary" {
		t.Fatalf("a flat history call must keep its authored name, got %q", part.ToolName)
	}
}

func TestDecodeCreate_NamespaceCollisionsRejected(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		tools string
	}{
		{
			name: "top-level before namespace member",
			tools: `[
				{"type":"function","name":"multi_agent_v1__close_agent","parameters":{"type":"object"}},
				{"type":"namespace","name":"multi_agent_v1","tools":[{"type":"function","name":"close_agent","parameters":{"type":"object"}}]}
			]`,
		},
		{
			name: "top-level after namespace member",
			tools: `[
				{"type":"namespace","name":"multi_agent_v1","tools":[{"type":"function","name":"close_agent","parameters":{"type":"object"}}]},
				{"type":"function","name":"multi_agent_v1__close_agent","parameters":{"type":"object"}}
			]`,
		},
		{
			name: "two namespaces project to the same name",
			tools: `[
				{"type":"namespace","name":"a__b","tools":[{"type":"function","name":"c","parameters":{"type":"object"}}]},
				{"type":"namespace","name":"a","tools":[{"type":"function","name":"b__c","parameters":{"type":"object"}}]}
			]`,
		},
		{
			name: "duplicate namespace declaration",
			tools: `[
				{"type":"namespace","name":"a","tools":[{"type":"function","name":"one","parameters":{"type":"object"}}]},
				{"type":"namespace","name":"a","tools":[{"type":"function","name":"two","parameters":{"type":"object"}}]}
			]`,
		},
		{
			name: "duplicate member in one namespace",
			tools: `[
				{"type":"namespace","name":"a","tools":[
					{"type":"function","name":"one","parameters":{"type":"object"}},
					{"type":"function","name":"one","parameters":{"type":"object"}}
				]}
			]`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := decodeNamespaceRequest(t, `{"model":"gpt-4o-mini","input":"hi","tools":`+tc.tools+`}`, openairesponses.DecodeOptions{})
			if err == nil {
				t.Fatal("a projected-name collision must be refused")
			}
			if !strings.Contains(err.Error(), "already declared") {
				t.Fatalf("collision refusal must name the conflict: %v", err)
			}
		})
	}
}

func TestDecodeCreate_NamespaceToolChoice(t *testing.T) {
	t.Parallel()
	tools := `[
		{"type":"function","name":"run","parameters":{"type":"object"}},
		{"type":"namespace","name":"a","tools":[{"type":"function","name":"run","parameters":{"type":"object"}}]},
		{"type":"namespace","name":"b","tools":[{"type":"function","name":"solo","parameters":{"type":"object"}}]}
	]`
	t.Run("exact top-level name wins", func(t *testing.T) {
		t.Parallel()
		body := `{"model":"gpt-4o-mini","input":"hi","tools":` + tools + `,"tool_choice":{"type":"function","function":{"name":"run"}}}`
		d, err := decodeNamespaceRequest(t, body, openairesponses.DecodeOptions{})
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		if got := d.Call.ToolChoice.Name; got != "run" {
			t.Fatalf("tool choice = %q, want exact top-level run", got)
		}
	})
	t.Run("unique bare member maps", func(t *testing.T) {
		t.Parallel()
		body := `{"model":"gpt-4o-mini","input":"hi","tools":` + tools + `,"tool_choice":{"type":"function","function":{"name":"solo"}}}`
		d, err := decodeNamespaceRequest(t, body, openairesponses.DecodeOptions{})
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		if got := d.Call.ToolChoice.Name; got != "b__solo" {
			t.Fatalf("tool choice = %q, want b__solo", got)
		}
	})
	t.Run("exact projected name maps to itself", func(t *testing.T) {
		t.Parallel()
		body := `{"model":"gpt-4o-mini","input":"hi","tools":` + tools + `,"tool_choice":{"type":"function","function":{"name":"a__run"}}}`
		d, err := decodeNamespaceRequest(t, body, openairesponses.DecodeOptions{})
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		if got := d.Call.ToolChoice.Name; got != "a__run" {
			t.Fatalf("tool choice = %q, want a__run", got)
		}
	})
	t.Run("ambiguous bare member refused", func(t *testing.T) {
		t.Parallel()
		ambiguousTools := `[
			{"type":"namespace","name":"a","tools":[{"type":"function","name":"run","parameters":{"type":"object"}}]},
			{"type":"namespace","name":"b","tools":[{"type":"function","name":"run","parameters":{"type":"object"}}]}
		]`
		body := `{"model":"gpt-4o-mini","input":"hi","tools":` + ambiguousTools + `,"tool_choice":{"type":"function","function":{"name":"run"}}}`
		_, err := decodeNamespaceRequest(t, body, openairesponses.DecodeOptions{})
		if err == nil {
			t.Fatal("an ambiguous bare-member tool choice must be refused")
		}
		if !strings.Contains(err.Error(), "more than one namespace member") {
			t.Fatalf("ambiguity refusal must be explicit: %v", err)
		}
	})
}

func TestDecodeCreate_NamespaceStrictRefuses(t *testing.T) {
	t.Parallel()
	strict := openairesponses.DecodeOptions{NamespaceTools: "reject"}
	t.Run("declaration", func(t *testing.T) {
		t.Parallel()
		body := `{"model":"gpt-4o-mini","input":"hi","tools":[{"type":"namespace","name":"multi_agent_v1","tools":[]}]}`
		_, err := decodeNamespaceRequest(t, body, strict)
		if err == nil {
			t.Fatal("strict mode must refuse namespace declarations")
		}
		if !strings.Contains(err.Error(), "namespace_tools=reject") {
			t.Fatalf("refusal must name the strict setting: %v", err)
		}
	})
	t.Run("history", func(t *testing.T) {
		t.Parallel()
		body := `{"model":"gpt-4o-mini","input":[{"type":"function_call","call_id":"c1","name":"close_agent","namespace":"multi_agent_v1","arguments":"{}"}],"tools":[{"type":"function","name":"x","parameters":{"type":"object"}}]}`
		_, err := decodeNamespaceRequest(t, body, strict)
		if err == nil {
			t.Fatal("strict mode must refuse namespaced history")
		}
		if !strings.Contains(err.Error(), "namespace_tools=reject") {
			t.Fatalf("refusal must name the strict setting: %v", err)
		}
	})
	t.Run("history output", func(t *testing.T) {
		t.Parallel()
		body := `{"model":"gpt-4o-mini","input":[{"type":"function_call_output","call_id":"c1","name":"close_agent","namespace":"multi_agent_v1","output":"x"}],"tools":[{"type":"function","name":"x","parameters":{"type":"object"}}]}`
		_, err := decodeNamespaceRequest(t, body, strict)
		if err == nil {
			t.Fatal("strict mode must refuse namespaced history output")
		}
		if !strings.Contains(err.Error(), "namespace_tools=reject") {
			t.Fatalf("refusal must name the strict setting: %v", err)
		}
	})
}

func TestDecodeCreate_NamespaceUnsupportedMemberRejected(t *testing.T) {
	t.Parallel()
	cases := []string{
		`{"type":"namespace","name":"a","tools":[{"type":"custom","name":"x"}]}`,
		`{"type":"namespace","name":"a","tools":[{"type":"web_search"}]}`,
		`{"type":"namespace","name":"a","tools":[{"type":"namespace","name":"nested","tools":[]}]}`,
	}
	for _, tools := range cases {
		tools := tools
		t.Run(tools, func(t *testing.T) {
			t.Parallel()
			_, err := decodeNamespaceRequest(t, `{"model":"gpt-4o-mini","input":"hi","tools":[`+tools+`]}`, openairesponses.DecodeOptions{})
			if err == nil {
				t.Fatal("an unsupported namespace member type must be refused")
			}
			if !strings.Contains(err.Error(), "unsupported namespace member type") {
				t.Fatalf("refusal must name the member type: %v", err)
			}
		})
	}
}

func TestDecodeCreate_NamespaceDescriptionBoundRejected(t *testing.T) {
	t.Parallel()
	namespaceDescription := strings.Repeat("n", aipapi.MaxToolDescriptionBytes)
	memberDescription := strings.Repeat("m", aipapi.MaxToolDescriptionBytes)
	tools := `[{"type":"namespace","name":"a","description":"` + namespaceDescription + `","tools":[{"type":"function","name":"f","description":"` + memberDescription + `","parameters":{"type":"object"}}]}]`
	_, err := decodeNamespaceRequest(t, `{"model":"gpt-4o-mini","input":"hi","tools":`+tools+`}`, openairesponses.DecodeOptions{})
	if err == nil {
		t.Fatal("a folded description over the canonical bound must be refused")
	}
	if !strings.Contains(err.Error(), "folded description") {
		t.Fatalf("refusal must name the folded description bound: %v", err)
	}
}

func TestDecodeCreate_NamespaceProjectedNameBoundRejected(t *testing.T) {
	t.Parallel()
	tools := `[{"type":"namespace","name":"` + strings.Repeat("n", 200) + `","tools":[{"type":"function","name":"` + strings.Repeat("m", 200) + `","parameters":{"type":"object"}}]}]`
	_, err := decodeNamespaceRequest(t, `{"model":"gpt-4o-mini","input":"hi","tools":`+tools+`}`, openairesponses.DecodeOptions{})
	if err == nil {
		t.Fatal("a projected name over the canonical bound must be refused")
	}
	if !strings.Contains(err.Error(), "projected tool name") {
		t.Fatalf("refusal must name the projected name bound: %v", err)
	}
}

// TestDecodeCreate_NamespaceDoesNotAdmitUnapprovedShapes keeps the explicit
// rejection boundary for custom, hosted, and additional_tools carriers. The
// namespace slice must not widen them by accident.
func TestDecodeCreate_NamespaceDoesNotAdmitUnapprovedShapes(t *testing.T) {
	t.Parallel()
	t.Run("custom declaration", func(t *testing.T) {
		t.Parallel()
		_, err := decodeNamespaceRequest(t, `{"model":"gpt-4o-mini","input":"hi","tools":[{"type":"custom","name":"x"}]}`, openairesponses.DecodeOptions{})
		if err == nil || !strings.Contains(err.Error(), `unsupported type "custom"`) {
			t.Fatalf("custom declarations must stay rejected: %v", err)
		}
	})
	history := []struct {
		name string
		item string
	}{
		{name: "custom call", item: `{"type":"custom_tool_call","call_id":"c1","name":"x","input":"{}"}`},
		{name: "custom call output", item: `{"type":"custom_tool_call_output","call_id":"c1","output":"x"}`},
		{name: "additional tools", item: `{"type":"additional_tools","tools":[]}`},
		{name: "hosted web search call", item: `{"type":"web_search_call","id":"ws_1","status":"completed","action":{"type":"search","query":"x"}}`},
		{name: "hosted web search call output", item: `{"type":"web_search_call_output","call_id":"ws_1","output":"x"}`},
	}
	for _, tc := range history {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			body := `{"model":"gpt-4o-mini","tools":[{"type":"namespace","name":"multi_agent_v1","tools":[{"type":"function","name":"close_agent","parameters":{"type":"object"}}]}],"input":[` + tc.item + `]}`
			_, err := decodeNamespaceRequest(t, body, openairesponses.DecodeOptions{})
			if err == nil {
				t.Fatalf("%s must stay rejected", tc.name)
			}
			if !strings.Contains(err.Error(), "unsupported input item type") {
				t.Fatalf("%s refusal must name the unsupported item type: %v", tc.name, err)
			}
		})
	}
}

// TestDecodeCreate_NamespaceToolsDoNotChangeCanonicalExtensions proves the
// projection is request state, not wire metadata: no new call extension is
// introduced (the client never sees a mapping key on a backend).
func TestDecodeCreate_NamespaceToolsDoNotChangeCanonicalExtensions(t *testing.T) {
	t.Parallel()
	d, err := decodeNamespaceRequest(t, `{"model":"gpt-4o-mini","input":"hi","tools":`+defaultCodexNamespaceTools+`}`, openairesponses.DecodeOptions{})
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	for k := range d.Call.Extensions {
		if strings.Contains(strings.ToLower(k), "namespace") {
			t.Fatalf("namespace projection must not add a call extension, got %q", k)
		}
	}
	encoded, err := json.Marshal(d.Call.Extensions)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(string(encoded)), "multi_agent_v1") {
		t.Fatalf("namespace names must not leak into call extensions: %s", encoded)
	}
}
