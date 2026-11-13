package anthropic_test

import (
	"bytes"
	"net/http"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/frontends/anthropic"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"gopkg.in/yaml.v3"
)

// TestDecodeMessage_requestOutputConfigEffort pins the request-level control:
// a documented effort value reaches the canonical generation options, and
// everything this frontend cannot represent is reported instead of dropped.
func TestDecodeMessage_requestOutputConfigEffort(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		outputConfig string // raw JSON, empty means absent
		wantEffort   string
		wantClasses  []string
	}{
		"absent":            {},
		"null":              {outputConfig: `null`},
		"empty object":      {outputConfig: `{}`},
		"low":               {outputConfig: `{"effort":"low"}`, wantEffort: "low"},
		"medium":            {outputConfig: `{"effort":"medium"}`, wantEffort: "medium"},
		"high":              {outputConfig: `{"effort":"high"}`, wantEffort: "high"},
		"xhigh":             {outputConfig: `{"effort":"xhigh"}`, wantEffort: "xhigh"},
		"max":               {outputConfig: `{"effort":"max"}`, wantEffort: "max"},
		"effort null":       {outputConfig: `{"effort":null}`},
		"empty effort":      {outputConfig: `{"effort":""}`, wantClasses: []string{"request_effort_not_forwarded"}},
		"non-string effort": {outputConfig: `{"effort":{"nested":true}}`, wantClasses: []string{"request_effort_not_forwarded"}},
		"format only":       {outputConfig: `{"format":{"type":"json_schema"}}`, wantClasses: []string{"request_output_config_fields_not_forwarded"}},
		"effort and format": {outputConfig: `{"effort":"high","format":{"type":"json_schema"}}`, wantEffort: "high", wantClasses: []string{"request_output_config_fields_not_forwarded"}},
		"unknown sibling":   {outputConfig: `{"verbosity":"low"}`, wantClasses: []string{"request_output_config_fields_not_forwarded"}},
		"non-object":        {outputConfig: `7`, wantClasses: []string{"request_output_config_fields_not_forwarded"}},
		"array":             {outputConfig: `[{"effort":"low"}]`, wantClasses: []string{"request_output_config_fields_not_forwarded"}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			body := `{"model":"m","max_tokens":64`
			if tc.outputConfig != "" {
				body += `,"output_config":` + tc.outputConfig
			}
			body += `,"messages":[{"role":"user","content":"hi"}]}`
			d, err := anthropic.DecodeMessageRequest([]byte(body), anthropic.DecodeOptions{RouteSelector: "stub:m"})
			if err != nil {
				t.Fatalf("served request rejected: %v", err)
			}
			if got := d.Call.Options.ReasoningEffort; got != tc.wantEffort {
				t.Fatalf("ReasoningEffort = %q, want %q", got, tc.wantEffort)
			}
			if got := d.RequestControls.Classes(); !equalClasses(got, tc.wantClasses) {
				t.Fatalf("classes = %v, want %v", got, tc.wantClasses)
			}
			if err := d.Call.Validate(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// TestDecodeMessage_contentSystemControlsReported pins the second proven loss:
// a content-bearing inline system message keeps its content but its per-message
// controls are counted, never forwarded.
func TestDecodeMessage_contentSystemControlsReported(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		message      string
		wantEffort   int
		wantFields   int
		wantClasses  []string
		wantMsgParts int
	}{
		"no controls": {
			message:      `{"role":"system","content":"worker instructions"}`,
			wantMsgParts: 1,
		},
		"effort": {
			message:      `{"role":"system","content":"worker instructions","output_config":{"effort":"medium"}}`,
			wantEffort:   1,
			wantClasses:  []string{"content_system_per_turn_effort_not_forwarded"},
			wantMsgParts: 1,
		},
		"clear_at": {
			message:      `{"role":"system","content":"worker instructions","clear_at":"next_user_message"}`,
			wantFields:   1,
			wantClasses:  []string{"content_system_fields_not_forwarded"},
			wantMsgParts: 1,
		},
		"effort and sibling": {
			message:      `{"role":"system","content":"worker instructions","output_config":{"effort":"max","format":{"type":"json"}}}`,
			wantEffort:   1,
			wantFields:   1,
			wantClasses:  []string{"content_system_per_turn_effort_not_forwarded", "content_system_fields_not_forwarded"},
			wantMsgParts: 1,
		},
		"malformed output_config is served": {
			message:      `{"role":"system","content":"worker instructions","output_config":7}`,
			wantFields:   1,
			wantClasses:  []string{"content_system_fields_not_forwarded"},
			wantMsgParts: 1,
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			body := `{"model":"m","max_tokens":64,"messages":[{"role":"user","content":"hi"},` + tc.message + `]}`
			d, err := anthropic.DecodeMessageRequest([]byte(body), anthropic.DecodeOptions{RouteSelector: "stub:m"})
			if err != nil {
				t.Fatalf("content-bearing system message rejected: %v", err)
			}
			if d.ContentSystemControls.PerTurnEffort != tc.wantEffort || d.ContentSystemControls.MessageFields != tc.wantFields {
				t.Fatalf("content controls = %+v, want effort:%d fields:%d", d.ContentSystemControls, tc.wantEffort, tc.wantFields)
			}
			if got := d.ContentSystemControls.Classes(); !equalClasses(got, tc.wantClasses) {
				t.Fatalf("classes = %v, want %v", got, tc.wantClasses)
			}
			if len(d.Call.Messages) != 2 || len(d.Call.Messages[1].Parts) != tc.wantMsgParts {
				t.Fatalf("history changed: %+v", d.Call.Messages)
			}
			if d.Call.Options.ReasoningEffort != "" {
				t.Fatalf("per-message effort leaked into generation options: %q", d.Call.Options.ReasoningEffort)
			}
		})
	}
}

// TestDecodeMessage_perMessageControlsReject refuses any inline system message
// that declares a control this frontend cannot forward, including a contentless
// carrier that the serve policy would otherwise reduce.
func TestDecodeMessage_perMessageControlsReject(t *testing.T) {
	t.Parallel()
	carrierWithEffort := `{"role":"system","content":[],"output_config":{"effort":"medium"}}`
	bodies := map[string]string{
		"content-bearing effort":   `{"model":"m","max_tokens":64,"messages":[{"role":"user","content":"hi"},{"role":"system","content":"worker instructions","output_config":{"effort":"medium"}}]}`,
		"content-bearing clear_at": `{"model":"m","max_tokens":64,"messages":[{"role":"user","content":"hi"},{"role":"system","content":"worker instructions","clear_at":"next_user_message"}]}`,
		"empty carrier":            `{"model":"m","max_tokens":64,"messages":[{"role":"user","content":"hi"},` + carrierWithEffort + `]}`,
	}
	for name, body := range bodies {
		if _, err := anthropic.DecodeMessageRequest([]byte(body), anthropic.DecodeOptions{
			RouteSelector:       "stub:m",
			PerMessageControls:  anthropic.PerMessageControlsReject,
			EmptySystemMessages: anthropic.EmptySystemMessagesServe,
		}); err == nil {
			t.Fatalf("%s: control-bearing message accepted under reject", name)
		}
	}
	// A contentless carrier without controls is still governed by the empty
	// policy and is served under the default serve value.
	d, err := anthropic.DecodeMessageRequest([]byte(`{"model":"m","max_tokens":64,"messages":[{"role":"user","content":"hi"},{"role":"system","content":[]}]}`), anthropic.DecodeOptions{
		RouteSelector:      "stub:m",
		PerMessageControls: anthropic.PerMessageControlsReject,
	})
	if err != nil {
		t.Fatalf("control-free carrier rejected: %v", err)
	}
	if !d.EmptySystemCarrier.Applied() {
		t.Fatal("control-free carrier must still report its removal")
	}
}

// TestDecodeConfig_perMessageControls pins the operator policy vocabulary.
func TestDecodeConfig_perMessageControls(t *testing.T) {
	t.Parallel()
	for raw, want := range map[string]string{
		// An empty document keeps the zero value; decode-time normalization
		// applies the serve default when the handler builds DecodeOptions.
		``:                             ``,
		`per_message_controls: serve`:  anthropic.PerMessageControlsServe,
		`per_message_controls: reject`: anthropic.PerMessageControlsReject,
	} {
		var n yaml.Node
		if err := yaml.Unmarshal([]byte(raw), &n); err != nil {
			t.Fatal(err)
		}
		cfg, err := anthropic.DecodeConfig(n)
		if err != nil {
			t.Fatalf("%q: %v", raw, err)
		}
		if cfg.PerMessageControls != want {
			t.Fatalf("%q: policy = %q, want %q", raw, cfg.PerMessageControls, want)
		}
	}
	var n yaml.Node
	if err := yaml.Unmarshal([]byte(`per_message_controls: silently-drop`), &n); err != nil {
		t.Fatal(err)
	}
	if _, err := anthropic.DecodeConfig(n); err == nil {
		t.Fatal("an unknown policy value must fail decoding")
	}
}

// TestHTTP_requestAndContentSystemNotices pins the two new operator notices:
// bounded classes and counts, the strict policy name where one exists, and no
// client value in any line.
func TestHTTP_requestAndContentSystemNotices(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		body    string
		marker  string
		notices []noticeLine
	}{
		"request effort malformed": {
			body:    `{"model":"m","max_tokens":64,"output_config":{"effort":{"secret":"` + secretSentinel + `"},"format":{"type":"json"}},"messages":[{"role":"user","content":[{"type":"text","text":"PROBE hi"}]}]}`,
			marker:  "request output_config reduced",
			notices: []noticeLine{{class: "request_effort_not_forwarded", count: 1}, {class: "request_output_config_fields_not_forwarded", count: 1}},
		},
		"content system effort": {
			body:    `{"model":"m","max_tokens":64,"messages":[{"role":"user","content":[{"type":"text","text":"PROBE hi"}]},{"role":"system","content":"worker","output_config":{"effort":"` + secretSentinel + `"}}]}`,
			marker:  "inline system message controls not forwarded",
			notices: []noticeLine{{class: "content_system_per_turn_effort_not_forwarded", count: 1}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var logs bytes.Buffer
			_, srv, capture := newCarrierHandler(t, &logs, anthropic.Config{})
			status, body := postBody(t, srv, tc.body)
			if status != http.StatusOK {
				t.Fatalf("status = %d, body = %s", status, body)
			}
			got := noticeLinesWith(t, logs.String(), tc.marker)
			if len(got) != len(tc.notices) {
				t.Fatalf("notice lines = %d, want %d; logs:\n%s", len(got), len(tc.notices), logs.String())
			}
			for i, want := range tc.notices {
				if got[i].class != want.class || got[i].count != want.count {
					t.Fatalf("notice %d = %+v, want %+v; logs:\n%s", i, got[i], want, logs.String())
				}
				if got[i].scope != "first_request_with_this_class_in_this_handler_generation" {
					t.Fatalf("notice %d scope = %q", i, got[i].scope)
				}
				if strings.Contains(got[i].raw, secretSentinel) || strings.Contains(got[i].raw, "PROBE") {
					t.Fatalf("notice leaked client data: %s", got[i].raw)
				}
			}
			if v, ok := capture.Load("last"); ok {
				call := testkit.MustAIPCall(t, v)
				if call.Options.ReasoningEffort != "" {
					t.Fatalf("unrepresentable effort reached generation options: %q", call.Options.ReasoningEffort)
				}
			}
		})
	}
}

func noticeLinesWith(t *testing.T, logs, marker string) []noticeLine {
	t.Helper()
	out := make([]noticeLine, 0, 2)
	for line := range strings.SplitSeq(logs, "\n") {
		if !strings.Contains(line, marker) {
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
