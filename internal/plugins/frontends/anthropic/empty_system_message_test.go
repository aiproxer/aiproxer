package anthropic_test

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/frontends/anthropic"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"gopkg.in/yaml.v3"
)

// secretSentinel is a value that must never appear in an operator notice. It is
// deliberately distinctive so a substring assertion is meaningful.
const secretSentinel = "sk-ant-SENTINEL-DO-NOT-LEAK-7631"

// bareCarrier is the exact Claude Code 2.1.322 `--bare` shape: a per-turn
// control message with role "system", content [], and a per-message
// output_config the vendor SDK documents as a per-turn control.
const bareCarrier = `{
  "model": "claude-sonnet-5-5",
  "max_tokens": 128000,
  "output_config": {"effort": "medium"},
  "messages": [
    {"role":"user","content":[{"type":"text","text":"PROBE hi","cache_control":{"type":"ephemeral"}}]},
    {"role":"system","content":[],"output_config":{"effort":"medium"}}
  ]
}`

// expectedCounts is the exact reduction a decode must report.
type expectedCounts struct {
	empty   int
	effort  int
	fields  int
	classes []string
}

func assertReduction(t *testing.T, got anthropic.EmptySystemCarrierReduction, want expectedCounts) {
	t.Helper()
	if got.EmptyContent != want.empty || got.PerTurnEffort != want.effort || got.MessageFields != want.fields {
		t.Fatalf("reduction = {empty:%d effort:%d fields:%d}, want {empty:%d effort:%d fields:%d}",
			got.EmptyContent, got.PerTurnEffort, got.MessageFields, want.empty, want.effort, want.fields)
	}
	if !equalClasses(got.Classes(), want.classes) {
		t.Fatalf("classes = %v, want %v", got.Classes(), want.classes)
	}
	// ClassCounts must agree with the counts, class by class.
	counts := got.ClassCounts()
	if len(counts) != len(want.classes) {
		t.Fatalf("class counts = %+v, want %d entries", counts, len(want.classes))
	}
	wantCount := map[string]int{
		"empty_content":                 want.empty,
		"per_turn_effort_not_forwarded": want.effort,
		"message_fields_not_forwarded":  want.fields,
	}
	for _, cc := range counts {
		if wantCount[cc.Class] != cc.Count {
			t.Fatalf("class %q count = %d, want %d", cc.Class, cc.Count, wantCount[cc.Class])
		}
	}
}

func TestDecodeMessage_emptyInlineSystemCarrier(t *testing.T) {
	t.Parallel()
	d, err := anthropic.DecodeMessageRequest([]byte(bareCarrier), anthropic.DecodeOptions{RouteSelector: "stub:x"})
	if err != nil {
		t.Fatalf("native --bare carrier rejected: %v", err)
	}
	// The carrier declares a per-message effort. It is reported even though the
	// request-level effort happens to match, because the frontend forwards only
	// the request-level value and must never imply the carrier's was preserved.
	assertReduction(t, d.EmptySystemCarrier, expectedCounts{
		empty: 1, effort: 1, fields: 0,
		classes: []string{"empty_content", "per_turn_effort_not_forwarded"},
	})
	if d.Call.Options.ReasoningEffort != "medium" {
		t.Fatalf("request-level effort = %q, want %q", d.Call.Options.ReasoningEffort, "medium")
	}
	if len(d.Call.Messages) != 1 || d.Call.Messages[0].Role != aipapi.RoleUser ||
		d.Call.Messages[0].Parts[0].Text != "PROBE hi" {
		t.Fatalf("client content changed: %+v", d.Call.Messages)
	}
	if err := d.Call.Validate(); err != nil {
		t.Fatal(err)
	}
	if _, err := anthropic.DecodeMessageRequest([]byte(bareCarrier), anthropic.DecodeOptions{
		RouteSelector: "stub:x", EmptySystemMessages: anthropic.EmptySystemMessagesReject,
	}); err == nil || !strings.Contains(err.Error(), "content blocks are empty") {
		t.Fatalf("reject policy must keep the previous refusal, got %v", err)
	}
}

// TestDecodeMessage_carrierPerTurnEffortAlwaysReported pins that the value is
// irrelevant: matching, differing, and carrier-only effort all report, because
// the frontend forwards only the request-level effort, never the carrier's.
func TestDecodeMessage_carrierPerTurnEffortAlwaysReported(t *testing.T) {
	t.Parallel()
	for name, body := range map[string]string{
		"matching": `{"model":"m","max_tokens":64,"output_config":{"effort":"medium"},
			"messages":[{"role":"user","content":"hi"},
			{"role":"system","content":[],"output_config":{"effort":"medium"}}]}`,
		"differing": `{"model":"m","max_tokens":64,"output_config":{"effort":"low"},
			"messages":[{"role":"user","content":"hi"},
			{"role":"system","content":[],"output_config":{"effort":"max"}}]}`,
		"carrier-only": `{"model":"m","max_tokens":64,
			"messages":[{"role":"user","content":"hi"},
			{"role":"system","content":[],"output_config":{"effort":"high"}}]}`,
	} {
		d, err := anthropic.DecodeMessageRequest([]byte(body), anthropic.DecodeOptions{RouteSelector: "stub:m"})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		assertReduction(t, d.EmptySystemCarrier, expectedCounts{
			empty: 1, effort: 1, fields: 0,
			classes: []string{"empty_content", "per_turn_effort_not_forwarded"},
		})
	}
}

// TestDecodeMessage_carrierWithoutPerMessageControls keeps the truly minimal
// carrier honest: only the removed empty content is reported.
func TestDecodeMessage_carrierWithoutPerMessageControls(t *testing.T) {
	t.Parallel()
	body := `{"model":"m","max_tokens":64,"messages":[{"role":"user","content":"hi"},{"role":"system","content":[]}]}`
	d, err := anthropic.DecodeMessageRequest([]byte(body), anthropic.DecodeOptions{RouteSelector: "stub:m"})
	if err != nil {
		t.Fatal(err)
	}
	assertReduction(t, d.EmptySystemCarrier, expectedCounts{empty: 1, classes: []string{"empty_content"}})
}

// TestDecodeMessage_carrierMessageFieldsReported pins each unmodelled carrier
// field class, including a non-string effort that must not be parsed.
func TestDecodeMessage_carrierMessageFieldsReported(t *testing.T) {
	t.Parallel()
	for name, carrier := range map[string]string{
		"clear_at":              `{"role":"system","content":[],"clear_at":"next_user_message"}`,
		"output_config sibling": `{"role":"system","content":[],"output_config":{"effort":"medium","format":{"type":"json"}}}`,
		"unknown message field": `{"role":"system","content":[],"vendor_hint":{"x":1}}`,
		"non-string effort":     `{"role":"system","content":[],"output_config":{"effort":{"nested":true}}}`,
	} {
		body := `{"model":"m","max_tokens":64,"output_config":{"effort":"medium"},
			"messages":[{"role":"user","content":"hi"},` + carrier + `]}`
		d, err := anthropic.DecodeMessageRequest([]byte(body), anthropic.DecodeOptions{RouteSelector: "stub:m"})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		want := expectedCounts{empty: 1, fields: 1, classes: []string{"empty_content", "message_fields_not_forwarded"}}
		if name == "output_config sibling" {
			// A sibling field and a declared effort are independent classes.
			want = expectedCounts{
				empty: 1, effort: 1, fields: 1,
				classes: []string{"empty_content", "per_turn_effort_not_forwarded", "message_fields_not_forwarded"},
			}
		}
		assertReduction(t, d.EmptySystemCarrier, want)
	}
}

// TestDecodeMessage_malformedRequestOutputConfigStillServed is the B2 guard: the
// baseline served a request whose top-level output_config was malformed, so
// decoding must not start rejecting it when no carrier is present. The value is
// now reported as a request-level reduction instead of being dropped silently.
func TestDecodeMessage_malformedRequestOutputConfigStillServed(t *testing.T) {
	t.Parallel()
	for name, raw := range map[string]string{
		"numeric":       `7`,
		"array":         `[{"effort":"medium"}]`,
		"string":        `"medium"`,
		"nested effort": `{"effort":{"nested":true}}`,
	} {
		body := `{"model":"m","max_tokens":64,"output_config":` + raw + `,"messages":[{"role":"user","content":"hi"}]}`
		d, err := anthropic.DecodeMessageRequest([]byte(body), anthropic.DecodeOptions{RouteSelector: "stub:m"})
		if err != nil {
			t.Fatalf("%s: baseline-served request now rejected: %v", name, err)
		}
		if d.EmptySystemCarrier.Applied() {
			t.Fatalf("%s: no carrier was present, reduction = %+v", name, d.EmptySystemCarrier)
		}
		if !d.RequestControls.Applied() {
			t.Fatalf("%s: malformed output_config was not reported", name)
		}
		if d.Call.Options.ReasoningEffort != "" {
			t.Fatalf("%s: malformed effort reached generation options: %q", name, d.Call.Options.ReasoningEffort)
		}
	}
}

// TestDecodeMessage_multipleCarriersCounted pins per-class counts when one
// request carries several carriers with different declarations.
func TestDecodeMessage_multipleCarriersCounted(t *testing.T) {
	t.Parallel()
	body := `{"model":"m","max_tokens":64,"messages":[
		{"role":"user","content":"hi"},
		{"role":"system","content":[],"output_config":{"effort":"medium"}},
		{"role":"user","content":"again"},
		{"role":"system","content":[],"clear_at":"next_user_message"},
		{"role":"user","content":"more"},
		{"role":"system","content":[]}]}`
	d, err := anthropic.DecodeMessageRequest([]byte(body), anthropic.DecodeOptions{RouteSelector: "stub:m"})
	if err != nil {
		t.Fatal(err)
	}
	assertReduction(t, d.EmptySystemCarrier, expectedCounts{
		empty: 3, effort: 1, fields: 1,
		classes: []string{"empty_content", "per_turn_effort_not_forwarded", "message_fields_not_forwarded"},
	})
	if len(d.Call.Messages) != 3 {
		t.Fatalf("history = %d, want the three surviving user turns", len(d.Call.Messages))
	}
}

func TestDecodeMessage_emptySystemCarrierKeepsSurroundingHistory(t *testing.T) {
	t.Parallel()
	body := []byte(`{"model":"external:model","max_tokens":64,"messages":[
		{"role":"user","content":"first"},
		{"role":"system","content":[]},
		{"role":"system","content":"worker instructions"},
		{"role":"assistant","content":"answer"},
		{"role":"user","content":"next"}]}`)
	d, err := anthropic.DecodeMessageRequest(body, anthropic.DecodeOptions{RouteSelector: "external:model"})
	if err != nil {
		t.Fatal(err)
	}
	wantRoles := []aipapi.Role{aipapi.RoleUser, aipapi.RoleSystem, aipapi.RoleAssistant, aipapi.RoleUser}
	wantText := []string{"first", "worker instructions", "answer", "next"}
	if len(d.Call.Messages) != len(wantRoles) {
		t.Fatalf("history length: %d", len(d.Call.Messages))
	}
	for i, m := range d.Call.Messages {
		if m.Role != wantRoles[i] || len(m.Parts) != 1 || m.Parts[0].Text != wantText[i] {
			t.Fatalf("message %d changed: %+v", i, m)
		}
	}
}

func TestDecodeMessage_emptyConversationTurnStillRejected(t *testing.T) {
	t.Parallel()
	for _, role := range []string{"user", "assistant"} {
		body := []byte(`{"model":"external:model","max_tokens":64,"messages":[{"role":"` + role + `","content":[]},{"role":"user","content":"next"}]}`)
		if _, err := anthropic.DecodeMessageRequest(body, anthropic.DecodeOptions{RouteSelector: "external:model"}); err == nil {
			t.Fatalf("%s: empty conversation turn accepted", role)
		}
	}
	if _, err := anthropic.DecodeMessageRequest([]byte(`{"model":"external:model","max_tokens":64,"messages":[{"role":"system","content":[]}]}`), anthropic.DecodeOptions{RouteSelector: "external:model"}); err == nil {
		t.Fatal("carrier-only history was accepted instead of refused")
	}
	if _, err := anthropic.DecodeMessageRequest([]byte(`{"model":"external:model","max_tokens":64,"messages":[{"role":"user","content":"next"},{"role":"system","content":""}]}`), anthropic.DecodeOptions{RouteSelector: "external:model"}); err == nil {
		t.Fatal("empty string system content was silently accepted")
	}
}

func TestDecodeConfig_emptySystemMessagesPolicy(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ in, want string }{
		{"", anthropic.EmptySystemMessagesServe},
		{"serve", anthropic.EmptySystemMessagesServe},
		{"reject", anthropic.EmptySystemMessagesReject},
	} {
		var n yaml.Node
		if err := yaml.Unmarshal([]byte(`empty_system_messages: `+tc.in), &n); err != nil {
			t.Fatal(err)
		}
		cfg, err := anthropic.DecodeConfig(n)
		if err != nil || cfg.EmptySystemMessages != tc.want {
			t.Fatalf("%q: cfg=%+v err=%v", tc.in, cfg, err)
		}
	}
	var n yaml.Node
	if err := yaml.Unmarshal([]byte(`empty_system_messages: drop`), &n); err != nil {
		t.Fatal(err)
	}
	if _, err := anthropic.DecodeConfig(n); err == nil {
		t.Fatal("an unknown policy value must fail decoding, not silently serve")
	}
}

// noticeLine is one decoded operator notice.
type noticeLine struct {
	class string
	count int
	scope string
	raw   string
}

func notices(t *testing.T, logs string) []noticeLine {
	t.Helper()
	out := make([]noticeLine, 0, 3)
	for line := range strings.SplitSeq(logs, "\n") {
		if !strings.Contains(line, "empty inline system carrier reduced") {
			continue
		}
		out = append(out, noticeLine{
			class: fieldValue(line, "class"),
			count: atoi(fieldValue(line, "count")),
			scope: fieldValue(line, "scope"),
			raw:   line,
		})
	}
	return out
}

func fieldValue(line, key string) string {
	for part := range strings.FieldsSeq(line) {
		if v, ok := strings.CutPrefix(part, key+"="); ok {
			return strings.Trim(v, `"`)
		}
	}
	return ""
}

func atoi(s string) int {
	var n int
	for _, r := range s {
		if r < '0' || r > '9' {
			return -1
		}
		n = n*10 + int(r-'0')
	}
	return n
}

func newCarrierHandler(t *testing.T, logs *bytes.Buffer, cfg anthropic.Config) (*anthropic.Handler, *httptest.Server, *sync.Map) {
	t.Helper()
	var capture sync.Map
	ex := testkit.NewStubExecutor(t, aipapi.NewBackendCaps(aipapi.CapabilityStreaming), "served", &capture)
	h := &anthropic.Handler{
		Exec:                 ex,
		DefaultRouteSelector: "stub:claude-sonnet-5-5",
		Log:                  slog.New(slog.NewTextHandler(logs, nil)),
		Config:               cfg,
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return h, srv, &capture
}

func postBody(t *testing.T, srv *httptest.Server, body string) (int, string) {
	t.Helper()
	resp, err := http.Post(srv.URL+"/v1/messages", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw)
}

// TestHTTP_emptySystemCarrierNoticesExactClassesAndCounts pins each class line
// exactly (label and count), not by substring, plus the absence of client values
// including a secret sentinel.
func TestHTTP_emptySystemCarrierNoticesExactClassesAndCounts(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		body    string
		history int
		notices []noticeLine // class/count pairs, in order
	}{
		"effort only": {
			body:    `{"model":"m","max_tokens":64,"messages":[{"role":"user","content":[{"type":"text","text":"PROBE hi"}]},{"role":"system","content":[],"output_config":{"effort":"` + secretSentinel + `"}}]}`,
			history: 1,
			notices: []noticeLine{{class: "empty_content", count: 1}, {class: "per_turn_effort_not_forwarded", count: 1}},
		},
		"clear_at": {
			body:    `{"model":"m","max_tokens":64,"messages":[{"role":"user","content":[{"type":"text","text":"PROBE hi"}]},{"role":"system","content":[],"clear_at":"` + secretSentinel + `"}]}`,
			history: 1,
			notices: []noticeLine{{class: "empty_content", count: 1}, {class: "message_fields_not_forwarded", count: 1}},
		},
		"multi carrier": {
			body: `{"model":"m","max_tokens":64,"messages":[{"role":"user","content":[{"type":"text","text":"PROBE hi"}]},
				{"role":"system","content":[],"output_config":{"effort":"medium"}},
				{"role":"user","content":"again"},
				{"role":"system","content":[],"clear_at":"next_user_message"}]}`,
			history: 2,
			notices: []noticeLine{{class: "empty_content", count: 2}, {class: "per_turn_effort_not_forwarded", count: 1}, {class: "message_fields_not_forwarded", count: 1}},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var logs bytes.Buffer
			_, srv, capture := newCarrierHandler(t, &logs, anthropic.Config{})
			status, body := postBody(t, srv, tc.body)
			if status != http.StatusOK {
				t.Fatalf("status = %d, body = %s", status, body)
			}
			got := notices(t, logs.String())
			if len(got) != len(tc.notices) {
				t.Fatalf("notice lines = %d, want %d; logs:\n%s", len(got), len(tc.notices), logs.String())
			}
			for i, want := range tc.notices {
				if got[i].class != want.class || got[i].count != want.count {
					t.Fatalf("notice %d = {class:%q count:%d}, want {class:%q count:%d}; logs:\n%s",
						i, got[i].class, got[i].count, want.class, want.count, logs.String())
				}
				if got[i].scope != "first_request_with_this_class_in_this_handler_generation" {
					t.Fatalf("notice %d scope = %q", i, got[i].scope)
				}
				// The notice must never echo a client value, including the sentinel.
				for _, forbidden := range []string{secretSentinel, "PROBE", "next_user_message", "medium"} {
					if strings.Contains(got[i].raw, forbidden) {
						t.Fatalf("notice %d leaked %q: %s", i, forbidden, got[i].raw)
					}
				}
				if !strings.Contains(got[i].raw, "empty_system_messages=reject") {
					t.Fatalf("notice %d must name the strict policy: %s", i, got[i].raw)
				}
			}
			if v, ok := capture.Load("last"); ok {
				call := testkit.MustAIPCall(t, v)
				if len(call.Messages) != tc.history {
					t.Fatalf("canonical history = %d, want %d: %+v", len(call.Messages), tc.history, call.Messages)
				}
				if call.Options.ReasoningEffort != "" {
					t.Fatalf("per-message effort leaked into generation options: %q", call.Options.ReasoningEffort)
				}
			}
			// A second request must not re-emit already reported classes.
			status, body = postBody(t, srv, tc.body)
			if status != http.StatusOK {
				t.Fatalf("second status = %d, body = %s", status, body)
			}
			if got2 := notices(t, logs.String()); len(got2) != len(tc.notices) {
				t.Fatalf("repeat request added notices: %d, want %d; logs:\n%s", len(got2), len(tc.notices), logs.String())
			}
		})
	}
}

// TestHTTP_laterReductionClassStillReported is the B3 guard: a class that first
// appears on a later request must still produce its own notice, so no
// degradation is hidden behind an earlier one.
func TestHTTP_laterReductionClassStillReported(t *testing.T) {
	t.Parallel()
	var logs bytes.Buffer
	_, srv, _ := newCarrierHandler(t, &logs, anthropic.Config{})

	first := `{"model":"m","max_tokens":64,"messages":[{"role":"user","content":"one"},
		{"role":"system","content":[]}]}`
	status, body := postBody(t, srv, first)
	if status != http.StatusOK {
		t.Fatalf("first status = %d, body = %s", status, body)
	}
	got := notices(t, logs.String())
	if len(got) != 1 || got[0].class != "empty_content" || got[0].count != 1 {
		t.Fatalf("first notices = %+v, want exactly empty_content/1; logs:\n%s", got, logs.String())
	}

	second := `{"model":"m","max_tokens":64,"messages":[{"role":"user","content":"two"},
		{"role":"system","content":[],"output_config":{"effort":"medium"}}]}`
	status, body = postBody(t, srv, second)
	if status != http.StatusOK {
		t.Fatalf("second status = %d, body = %s", status, body)
	}
	got = notices(t, logs.String())
	if len(got) != 2 {
		t.Fatalf("notices after later class = %d, want 2; logs:\n%s", len(got), logs.String())
	}
	if got[1].class != "per_turn_effort_not_forwarded" || got[1].count != 1 {
		t.Fatalf("later notice = %+v, want per_turn_effort_not_forwarded/1", got[1])
	}

	// A class reported once must not repeat on a third request.
	status, body = postBody(t, srv, second)
	if status != http.StatusOK {
		t.Fatalf("third status = %d, body = %s", status, body)
	}
	if got := notices(t, logs.String()); len(got) != 2 {
		t.Fatalf("third request re-emitted a class; notices = %d; logs:\n%s", len(got), logs.String())
	}
}

// TestHTTP_emptySystemCarrierRejectedByConfig proves the handler passes the
// operator policy through, not only the decoder.
func TestHTTP_emptySystemCarrierRejectedByConfig(t *testing.T) {
	t.Parallel()
	var logs bytes.Buffer
	_, srv, capture := newCarrierHandler(t, &logs, anthropic.Config{EmptySystemMessages: anthropic.EmptySystemMessagesReject})
	status, body := postBody(t, srv, bareCarrier)
	if status != http.StatusBadRequest || !strings.Contains(body, "invalid request JSON") {
		t.Fatalf("reject policy status = %d, body = %s", status, body)
	}
	if _, ok := capture.Load("last"); ok {
		t.Fatal("rejected request must not reach the executor")
	}
	if got := notices(t, logs.String()); len(got) != 0 {
		t.Fatalf("rejected request must not emit a reduction notice, got %+v:\n%s", got, logs.String())
	}
}

func equalClasses(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range want {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
