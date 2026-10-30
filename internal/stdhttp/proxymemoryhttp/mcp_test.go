package proxymemoryhttp_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/aiproxer/aiproxer/internal/plugins/features/proxymemory"
	proxymemoryhttp "github.com/aiproxer/aiproxer/internal/stdhttp/proxymemoryhttp"
)

const mcpTestPath = "/v1/aip/memory/mcp"

// mcpTestRolloutName is the one seeded rollout summary document.
const mcpTestRolloutName = "2026-01-01T00-00-00-012345-alpha.md"

// newMCPStore seeds alice's bank with its readable documents.
func newMCPStore() *fakeStore {
	store := newFakeStore()
	store.seedBank(httpTestPrincipal, httpTestScope, 3)
	store.summaries[testOwner{principal: httpTestPrincipal, scope: httpTestScope}] = "the summary"
	store.documents[testOwner{principal: httpTestPrincipal, scope: httpTestScope}] = proxymemory.Documents{
		MemoryMD:      "alpha line\nneedle in the handbook\n",
		MemorySummary: "the summary\n",
		RolloutSummaries: map[string]string{
			mcpTestRolloutName: "rollout needle\n",
		},
		ProjectLabel: "/w/alice",
	}
	return store
}

// rpcEnvelope is the JSON-RPC 2.0 response shape of design §11.3.
type rpcEnvelope struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// toolResult is the tools/call result shape of design §11.3.
type toolResult struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	IsError bool `json:"isError"`
}

func (r toolResult) text(t *testing.T) string {
	t.Helper()
	if len(r.Content) != 1 || r.Content[0].Type != "text" {
		t.Fatalf("tool result content = %+v, want exactly one text item", r.Content)
	}
	return r.Content[0].Text
}

func mcpPost(h http.Handler, body, principal string) *httptest.ResponseRecorder {
	return serve(h, scopedRequest(http.MethodPost, mcpTestPath, strings.NewReader(body), principal))
}

// requireRPCResponse requires a 200 JSON-RPC response envelope and returns it.
func requireRPCResponse(t *testing.T, rec *httptest.ResponseRecorder) rpcEnvelope {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}
	var env rpcEnvelope
	decodeBody(t, rec, &env)
	if env.JSONRPC != "2.0" {
		t.Fatalf("jsonrpc = %q, want \"2.0\"", env.JSONRPC)
	}
	return env
}

// requireRPCCode requires one JSON-RPC error response code.
func requireRPCCode(t *testing.T, rec *httptest.ResponseRecorder, code int) {
	t.Helper()
	env := requireRPCResponse(t, rec)
	if env.Error == nil {
		t.Fatalf("response %s carries no error", rec.Body.String())
	}
	if env.Error.Code != code {
		t.Fatalf("error code = %d, want %d (%s)", env.Error.Code, code, rec.Body.String())
	}
}

func requireToolText(t *testing.T, rec *httptest.ResponseRecorder, text string, isError bool) {
	t.Helper()
	env := requireRPCResponse(t, rec)
	if env.Error != nil {
		t.Fatalf("tool call returned JSON-RPC error %+v", env.Error)
	}
	var result toolResult
	if err := json.Unmarshal(env.Result, &result); err != nil {
		t.Fatalf("result %s is not a tool result: %v", env.Result, err)
	}
	if result.IsError != isError {
		t.Fatalf("isError = %v, want %v", result.IsError, isError)
	}
	if got := result.text(t); got != text {
		t.Fatalf("text = %q, want %q", got, text)
	}
}

// TestMCPInitializeNegotiatesProtocolVersion pins the version negotiation and
// the advertised tools capability of design §11.3.
func TestMCPInitializeNegotiatesProtocolVersion(t *testing.T) {
	h := proxymemoryhttp.NewHandler(newTestService(t, newMCPStore(), nil))
	cases := []struct {
		requested string
		want      string
	}{
		{"2025-03-26", "2025-03-26"},
		{"2025-06-18", "2025-06-18"},
		{"1999-01-01", "2025-06-18"},
	}
	for _, tc := range cases {
		t.Run(tc.requested, func(t *testing.T) {
			body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"` +
				tc.requested + `","capabilities":{},"clientInfo":{"name":"codex","version":"1"}}}`
			rec := mcpPost(h, body, httpTestPrincipal)
			env := requireRPCResponse(t, rec)
			if env.Error != nil {
				t.Fatalf("initialize returned error %+v", env.Error)
			}
			if got := strings.TrimSpace(string(env.ID)); got != "1" {
				t.Fatalf("id = %s, want 1", got)
			}
			var result struct {
				ProtocolVersion string                     `json:"protocolVersion"`
				Capabilities    map[string]json.RawMessage `json:"capabilities"`
				ServerInfo      struct {
					Name    string `json:"name"`
					Version string `json:"version"`
				} `json:"serverInfo"`
			}
			if err := json.Unmarshal(env.Result, &result); err != nil {
				t.Fatalf("result %s is not an initialize result: %v", env.Result, err)
			}
			if result.ProtocolVersion != tc.want {
				t.Fatalf("protocolVersion = %q, want %q", result.ProtocolVersion, tc.want)
			}
			if _, ok := result.Capabilities["tools"]; !ok {
				t.Fatalf("capabilities %s lacks tools", env.Result)
			}
			if result.ServerInfo.Name != "aiproxer-memory" || result.ServerInfo.Version != "1" {
				t.Fatalf("serverInfo = %+v, want aiproxer-memory/1", result.ServerInfo)
			}
			if got := rec.Header().Get("Mcp-Session-Id"); got != "" {
				t.Fatalf("Mcp-Session-Id = %q, want none", got)
			}
		})
	}
}

// TestMCPNotificationInitializedIsAccepted pins the 202 empty-body notification
// response of design §11.3.
func TestMCPNotificationInitializedIsAccepted(t *testing.T) {
	h := proxymemoryhttp.NewHandler(newTestService(t, newMCPStore(), nil))
	rec := mcpPost(h, `{"jsonrpc":"2.0","method":"notifications/initialized"}`, httpTestPrincipal)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusAccepted)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("body = %q, want empty", rec.Body.String())
	}
}

// TestMCPPing pins the empty ping result of design §11.3.
func TestMCPPing(t *testing.T) {
	h := proxymemoryhttp.NewHandler(newTestService(t, newMCPStore(), nil))
	env := requireRPCResponse(t, mcpPost(h, `{"jsonrpc":"2.0","id":2,"method":"ping"}`, httpTestPrincipal))
	if env.Error != nil {
		t.Fatalf("ping returned error %+v", env.Error)
	}
	if got := strings.TrimSpace(string(env.Result)); got != "{}" {
		t.Fatalf("ping result = %s, want {}", got)
	}
}

// TestMCPToolsList pins the four tools and their scope-requiring schemas.
func TestMCPToolsList(t *testing.T) {
	h := proxymemoryhttp.NewHandler(newTestService(t, newMCPStore(), nil))
	env := requireRPCResponse(t, mcpPost(h, `{"jsonrpc":"2.0","id":3,"method":"tools/list"}`, httpTestPrincipal))
	if env.Error != nil {
		t.Fatalf("tools/list returned error %+v", env.Error)
	}
	var result struct {
		Tools []struct {
			Name        string `json:"name"`
			Description string `json:"description"`
			InputSchema struct {
				Type       string                     `json:"type"`
				Properties map[string]json.RawMessage `json:"properties"`
				Required   []string                   `json:"required"`
			} `json:"inputSchema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(env.Result, &result); err != nil {
		t.Fatalf("result %s is not a tools/list result: %v", env.Result, err)
	}
	names := make([]string, 0, len(result.Tools))
	for _, tool := range result.Tools {
		names = append(names, tool.Name)
	}
	want := []string{"memory_list", "memory_read", "memory_search", "memory_add_note"}
	if !slices.Equal(names, want) {
		t.Fatalf("tool names = %v, want %v", names, want)
	}
	toolRequired := map[string]string{
		"memory_list":     "scope",
		"memory_read":     "path",
		"memory_search":   "query",
		"memory_add_note": "note",
	}
	for _, tool := range result.Tools {
		if strings.TrimSpace(tool.Description) == "" {
			t.Fatalf("tool %s has no description", tool.Name)
		}
		if tool.InputSchema.Type != "object" {
			t.Fatalf("tool %s schema type = %q, want object", tool.Name, tool.InputSchema.Type)
		}
		if !slices.Contains(tool.InputSchema.Required, "scope") {
			t.Fatalf("tool %s schema does not require scope: %+v", tool.Name, tool.InputSchema.Required)
		}
		if !slices.Contains(tool.InputSchema.Required, toolRequired[tool.Name]) {
			t.Fatalf("tool %s schema does not require %s: %+v", tool.Name, toolRequired[tool.Name], tool.InputSchema.Required)
		}
		raw, ok := tool.InputSchema.Properties["scope"]
		if !ok {
			t.Fatalf("tool %s schema has no scope property", tool.Name)
		}
		var prop struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(raw, &prop); err != nil || prop.Type != "string" {
			t.Fatalf("tool %s scope property = %s, want type string", tool.Name, raw)
		}
	}
}

// TestMCPToolsCallHappyPaths pins every tool's happy path, the exact result
// shape and the tool_call counter of design §11.3.
func TestMCPToolsCallHappyPaths(t *testing.T) {
	store := newMCPStore()
	metrics := proxymemory.NewPrometheusCollector()
	h := proxymemoryhttp.NewHandler(newTestService(t, store, metrics), metrics)

	cases := []struct {
		name string
		tool string
		args string
		want string
	}{
		{
			name: "memory_list",
			tool: "memory_list",
			args: `{"scope":"` + httpTestScope + `"}`,
			want: "MEMORY.md\nmemory_summary.md\nrollout_summaries/" + mcpTestRolloutName,
		},
		{
			name: "memory_read without a range",
			tool: "memory_read",
			args: `{"scope":"` + httpTestScope + `","path":"memory_summary.md"}`,
			want: "1: the summary\n",
		},
		{
			name: "memory_read with a range",
			tool: "memory_read",
			args: `{"scope":"` + httpTestScope + `","path":"MEMORY.md","start_line":2,"end_line":2}`,
			want: "2: needle in the handbook\n",
		},
		{
			name: "memory_search",
			tool: "memory_search",
			args: `{"scope":"` + httpTestScope + `","query":"needle"}`,
			want: "MEMORY.md:2: needle in the handbook\nrollout_summaries/" + mcpTestRolloutName + ":1: rollout needle",
		},
		{
			name: "memory_search no matches",
			tool: "memory_search",
			args: `{"scope":"` + httpTestScope + `","query":"absent"}`,
			want: "no matches",
		},
		{
			name: "memory_add_note",
			tool: "memory_add_note",
			args: `{"scope":"` + httpTestScope + `","note":"keep this"}`,
			want: "saved",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":` +
				strconvQuote(tc.tool) + `,"arguments":` + tc.args + `}}`
			requireToolText(t, mcpPost(h, body, httpTestPrincipal), tc.want, false)
		})
	}
	requireEventCount(t, metrics, float64(len(cases)))
	if len(store.addedNotes) != 1 {
		t.Fatalf("added notes = %d, want 1", len(store.addedNotes))
	}
	note := store.addedNotes[0]
	if note.owner != (testOwner{principal: httpTestPrincipal, scope: httpTestScope}) || note.note.Text != "keep this" {
		t.Fatalf("added note = %+v, want alice's bank with text %q", note, "keep this")
	}
}

// strconvQuote quotes one tool name for embedding into a JSON body.
func strconvQuote(s string) string {
	raw, _ := json.Marshal(s)
	return string(raw)
}

// TestMCPToolCallNotOwnedScope pins the not-found tool result: another
// principal's bank answers exactly like an unknown scope.
func TestMCPToolCallNotOwnedScope(t *testing.T) {
	store := newMCPStore()
	store.seedBank(httpTestOtherPrincipal, httpTestOtherScope, 1)
	h := proxymemoryhttp.NewHandler(newTestService(t, store, nil))
	for _, scopeID := range []string{httpTestOtherScope, httpTestUnknownScope} {
		t.Run(scopeID, func(t *testing.T) {
			body := `{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"memory_list","arguments":{"scope":"` + scopeID + `"}}}`
			requireToolText(t, mcpPost(h, body, httpTestPrincipal), "not found", true)
		})
	}
}

// TestMCPToolCallInvalidInput pins the invalid-input tool result for a service
// ErrInvalid.
func TestMCPToolCallInvalidInput(t *testing.T) {
	h := proxymemoryhttp.NewHandler(newTestService(t, newMCPStore(), nil))
	body := `{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"memory_search","arguments":{"scope":"` +
		httpTestScope + `","query":"   "}}}`
	requireToolText(t, mcpPost(h, body, httpTestPrincipal), "invalid input", true)
}

// TestMCPToolCallStoreError pins the content-free unavailable result and its
// counted store_unavailable.
func TestMCPToolCallStoreError(t *testing.T) {
	store := newMCPStore()
	store.documentsErr = errors.New("boom: connection dropped")
	metrics := proxymemory.NewPrometheusCollector()
	h := proxymemoryhttp.NewHandler(newTestService(t, store, metrics), metrics)
	body := `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"memory_list","arguments":{"scope":"` +
		httpTestScope + `"}}}`
	requireToolText(t, mcpPost(h, body, httpTestPrincipal), "unavailable", true)
	requireEventCount(t, metrics, 1)
}

// TestMCPToolCallUnknownTool pins the invalid-params code of design §11.3.
func TestMCPToolCallUnknownTool(t *testing.T) {
	h := proxymemoryhttp.NewHandler(newTestService(t, newMCPStore(), nil))
	body := `{"jsonrpc":"2.0","id":8,"method":"tools/call","params":{"name":"memory_nope","arguments":{}}}`
	requireRPCCode(t, mcpPost(h, body, httpTestPrincipal), -32602)
}

// TestMCPUnknownMethod pins the method-not-found code of design §11.3.
func TestMCPUnknownMethod(t *testing.T) {
	h := proxymemoryhttp.NewHandler(newTestService(t, newMCPStore(), nil))
	requireRPCCode(t, mcpPost(h, `{"jsonrpc":"2.0","id":9,"method":"memory/boom"}`, httpTestPrincipal), -32601)
}

// TestMCPBatchRejected pins the invalid-request code for a batch array.
func TestMCPBatchRejected(t *testing.T) {
	h := proxymemoryhttp.NewHandler(newTestService(t, newMCPStore(), nil))
	body := `[{"jsonrpc":"2.0","id":10,"method":"tools/list"},{"jsonrpc":"2.0","id":11,"method":"ping"}]`
	requireRPCCode(t, mcpPost(h, body, httpTestPrincipal), -32600)
}

// TestMCPEnvelopeValidation pins the JSON-RPC 2.0 envelope rules of design
// §11.3: the literal protocol version and, when present, a string or number id
// are validated before any method runs, and only a legal id is echoed back.
func TestMCPEnvelopeValidation(t *testing.T) {
	h := proxymemoryhttp.NewHandler(newTestService(t, newMCPStore(), nil))
	cases := []struct {
		name   string
		body   string
		status int
		code   int    // 0 marks a successful result
		id     string // exact response id
	}{
		{
			name:   "missing jsonrpc",
			body:   `{"id":1,"method":"ping"}`,
			status: http.StatusOK,
			code:   -32600,
			id:     "1",
		},
		{
			name:   "wrong jsonrpc version",
			body:   `{"jsonrpc":"1.0","id":1,"method":"ping"}`,
			status: http.StatusOK,
			code:   -32600,
			id:     "1",
		},
		{
			name:   "object id",
			body:   `{"jsonrpc":"2.0","id":{},"method":"ping"}`,
			status: http.StatusOK,
			code:   -32600,
			id:     "null",
		},
		{
			name:   "array id",
			body:   `{"jsonrpc":"2.0","id":[],"method":"ping"}`,
			status: http.StatusOK,
			code:   -32600,
			id:     "null",
		},
		{
			name:   "boolean id",
			body:   `{"jsonrpc":"2.0","id":true,"method":"ping"}`,
			status: http.StatusOK,
			code:   -32600,
			id:     "null",
		},
		{
			name:   "null id",
			body:   `{"jsonrpc":"2.0","id":null,"method":"ping"}`,
			status: http.StatusOK,
			code:   -32600,
			id:     "null",
		},
		{
			name:   "blank method",
			body:   `{"jsonrpc":"2.0","id":14,"method":""}`,
			status: http.StatusOK,
			code:   -32600,
			id:     "14",
		},
		{
			name:   "absent method",
			body:   `{"jsonrpc":"2.0"}`,
			status: http.StatusOK,
			code:   -32600,
			id:     "null",
		},
		{
			name:   "number id",
			body:   `{"jsonrpc":"2.0","id":1,"method":"ping"}`,
			status: http.StatusOK,
			id:     "1",
		},
		{
			name:   "string id",
			body:   `{"jsonrpc":"2.0","id":"req-1","method":"ping"}`,
			status: http.StatusOK,
			id:     `"req-1"`,
		},
		{
			name:   "absent id notification",
			body:   `{"jsonrpc":"2.0","method":"notifications/initialized"}`,
			status: http.StatusAccepted,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := mcpPost(h, tc.body, httpTestPrincipal)
			if tc.status == http.StatusAccepted {
				if rec.Code != http.StatusAccepted || rec.Body.Len() != 0 {
					t.Fatalf("notification status = %d body = %q, want 202 with an empty body", rec.Code, rec.Body.String())
				}
				return
			}
			env := requireRPCResponse(t, rec)
			if got := strings.TrimSpace(string(env.ID)); got != tc.id {
				t.Fatalf("id = %s, want %s (%s)", got, tc.id, rec.Body.String())
			}
			if tc.code == 0 {
				if env.Error != nil {
					t.Fatalf("valid envelope answered error %+v", env.Error)
				}
				return
			}
			if env.Error == nil || env.Error.Code != tc.code {
				t.Fatalf("error = %+v, want code %d (%s)", env.Error, tc.code, rec.Body.String())
			}
		})
	}
}

// TestMCPInvalidEnvelopeDoesNotDispatch pins that a rejected envelope runs no
// service call: the mutating tool inside it must leave the store untouched and
// count no tool_call (design §11.3).
func TestMCPInvalidEnvelopeDoesNotDispatch(t *testing.T) {
	store := newMCPStore()
	metrics := proxymemory.NewPrometheusCollector()
	h := proxymemoryhttp.NewHandler(newTestService(t, store, metrics), metrics)
	const arguments = `{"name":"memory_add_note","arguments":{"scope":"` + httpTestScope + `","note":"should not be saved"}}`
	cases := []struct {
		name string
		body string
		id   string
	}{
		{
			name: "invalid version and object id",
			body: `{"jsonrpc":"1.0","id":{},"method":"tools/call","params":` + arguments + `}`,
			id:   "null",
		},
		{
			name: "missing jsonrpc",
			body: `{"id":16,"method":"tools/call","params":` + arguments + `}`,
			id:   "16",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := mcpPost(h, tc.body, httpTestPrincipal)
			env := requireRPCResponse(t, rec)
			if env.Error == nil || env.Error.Code != -32600 {
				t.Fatalf("response %s = error %+v, want -32600", rec.Body.String(), env.Error)
			}
			if got := strings.TrimSpace(string(env.ID)); got != tc.id {
				t.Fatalf("id = %s, want %s", got, tc.id)
			}
		})
	}
	if len(store.addedNotes) != 0 {
		t.Fatalf("added notes = %d, want 0: a rejected envelope executed memory_add_note", len(store.addedNotes))
	}
	if got := testutil.CollectAndCount(metrics); got != 0 {
		t.Fatalf("metric families = %d, want 0: a rejected envelope counted an event", got)
	}
}

// TestMCPEnvelopeCaseAliases pins JSON-RPC §2 member-name case sensitivity for
// the envelope: encoding/json resolves struct tags case-insensitively, so a
// differently-cased JSONRPC, Jsonrpc, jsonRPC, METHOD, Method, ID, Id or Params
// member must neither supply a missing exact-case member nor override one. A
// rejected envelope runs no method; an alias-only id is no id, so such an
// envelope stays notification-shaped (202, no dispatch, no side effect) for
// every method rather than being answered as a proxy request.
func TestMCPEnvelopeCaseAliases(t *testing.T) {
	const mutating = `"params":{"name":"memory_add_note","arguments":{"scope":"` + httpTestScope + `","note":"case alias must not save"}}`
	cases := []struct {
		name    string
		body    string
		status  int    // http.StatusAccepted marks the notification shape
		code    int    // 0 marks a dispatched result
		id      string // exact response id, when a body is expected
		effects int    // saved notes, equal to the tool_call metric families
	}{
		{
			name:   "JSONRPC alias cannot supply the version",
			body:   `{"JSONRPC":"2.0","id":20,"method":"tools/call",` + mutating + `}`,
			status: http.StatusOK, code: -32600, id: "20",
		},
		{
			name:   "Jsonrpc alias cannot supply the version",
			body:   `{"Jsonrpc":"2.0","id":21,"method":"tools/call",` + mutating + `}`,
			status: http.StatusOK, code: -32600, id: "21",
		},
		{
			name:   "jsonRPC alias cannot supply the version",
			body:   `{"jsonRPC":"2.0","id":22,"method":"tools/call",` + mutating + `}`,
			status: http.StatusOK, code: -32600, id: "22",
		},
		{
			name:   "fully aliased envelope has no exact-case id to echo",
			body:   `{"JSONRPC":"2.0","ID":23,"Method":"tools/call",` + mutating + `}`,
			status: http.StatusOK, code: -32600, id: "null",
		},
		{
			name:   "alias version cannot shadow the exact invalid one",
			body:   `{"jsonrpc":"1.0","JSONRPC":"2.0","id":24,"method":"tools/call",` + mutating + `}`,
			status: http.StatusOK, code: -32600, id: "24",
		},
		{
			name:   "METHOD alias cannot supply a method",
			body:   `{"jsonrpc":"2.0","id":25,"METHOD":"tools/call",` + mutating + `}`,
			status: http.StatusOK, code: -32600, id: "25",
		},
		{
			name:   "Method alias cannot shadow a blank method",
			body:   `{"jsonrpc":"2.0","id":26,"method":"","Method":"ping"}`,
			status: http.StatusOK, code: -32600, id: "26",
		},
		{
			name:   "Params alias supplies no arguments",
			body:   `{"jsonrpc":"2.0","id":27,"method":"tools/call","Params":{"name":"memory_add_note","arguments":{"scope":"` + httpTestScope + `","note":"case alias must not save"}}}`,
			status: http.StatusOK, code: -32602, id: "27",
		},
		{
			name:   "illegal ID alias leaves the envelope a notification",
			body:   `{"jsonrpc":"2.0","ID":{},"method":"tools/call",` + mutating + `}`,
			status: http.StatusAccepted,
		},
		{
			name:   "legal Id alias leaves the envelope a notification",
			body:   `{"jsonrpc":"2.0","Id":28,"method":"tools/call",` + mutating + `}`,
			status: http.StatusAccepted,
		},
		{
			name:   "illegal Id alias on notifications/initialized",
			body:   `{"jsonrpc":"2.0","Id":{},"method":"notifications/initialized"}`,
			status: http.StatusAccepted,
		},
		{
			name:   "illegal ID alias on ping is no request",
			body:   `{"jsonrpc":"2.0","ID":{},"method":"ping"}`,
			status: http.StatusAccepted,
		},
		{
			name:   "exact-case members win and alias junk is ignored",
			body:   `{"jsonrpc":"2.0","id":29,"method":"tools/call","JSONRPC":"1.0","ID":{},"METHOD":"tools/nope","Params":{},` + mutating + `}`,
			status: http.StatusOK, id: "29", effects: 1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newMCPStore()
			metrics := proxymemory.NewPrometheusCollector()
			h := proxymemoryhttp.NewHandler(newTestService(t, store, metrics), metrics)
			rec := mcpPost(h, tc.body, httpTestPrincipal)
			if tc.status == http.StatusAccepted {
				if rec.Code != http.StatusAccepted || rec.Body.Len() != 0 {
					t.Fatalf("notification shape = %d %q, want 202 with an empty body", rec.Code, rec.Body.String())
				}
			} else {
				env := requireRPCResponse(t, rec)
				if got := strings.TrimSpace(string(env.ID)); got != tc.id {
					t.Fatalf("id = %s, want %s (%s)", got, tc.id, rec.Body.String())
				}
				if tc.code == 0 {
					if env.Error != nil {
						t.Fatalf("valid envelope answered error %+v", env.Error)
					}
				} else if env.Error == nil || env.Error.Code != tc.code {
					t.Fatalf("error = %+v, want code %d (%s)", env.Error, tc.code, rec.Body.String())
				}
			}
			if len(store.addedNotes) != tc.effects {
				t.Fatalf("added notes = %d, want %d: an aliased envelope reached the service", len(store.addedNotes), tc.effects)
			}
			if got := testutil.CollectAndCount(metrics); got != tc.effects {
				t.Fatalf("metric families = %d, want %d: an aliased envelope counted an event", got, tc.effects)
			}
		})
	}
}

// TestMCPTransportMethodNotAllowed pins the GET/DELETE 405 of design §11.3.
func TestMCPTransportMethodNotAllowed(t *testing.T) {
	h := proxymemoryhttp.NewHandler(newTestService(t, newMCPStore(), nil))
	for _, method := range []string{http.MethodGet, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			rec := serve(h, scopedRequest(method, mcpTestPath, nil, httpTestPrincipal))
			if rec.Code != http.StatusMethodNotAllowed {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
			}
			if got := rec.Header().Get("Allow"); got != http.MethodPost {
				t.Fatalf("Allow = %q, want %q", got, http.MethodPost)
			}
		})
	}
}

// TestMCPBodyOverLimitRejected pins the 1 MiB body cap.
func TestMCPBodyOverLimitRejected(t *testing.T) {
	h := proxymemoryhttp.NewHandler(newTestService(t, newMCPStore(), nil))
	body := `{"jsonrpc":"2.0","id":12,"method":"ping","params":{"junk":"` +
		strings.Repeat("x", (1<<20)+1) + `"}}`
	rec := mcpPost(h, body, httpTestPrincipal)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusRequestEntityTooLarge)
	}
	if strings.Contains(rec.Body.String(), "xxx") {
		t.Fatalf("body %q echoes the oversize payload", rec.Body.String())
	}
}
