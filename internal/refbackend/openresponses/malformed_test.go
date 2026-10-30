package openresponses

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestMalformed_ResourceMissingField(t *testing.T) {
	t.Parallel()
	_, ts := startServer(t, Options{}, &Script{
		ID: "scenario-miss-field", Description: "missing required field", Mode: ModeJSON,
		Resource:  NewResource("r", "m", 1, nil),
		Malformed: MalformedResourceMissingField,
	})
	resp, raw := postJSON(t, ts.URL+"/responses", `{"model":"m","input":"hi"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if _, ok := m["output"]; ok {
		t.Fatal("output must be omitted by malformed mode")
	}
}

func TestMalformed_ResourceBadType(t *testing.T) {
	t.Parallel()
	_, ts := startServer(t, Options{}, &Script{
		ID: "scenario-bad-type", Description: "bad field type", Mode: ModeJSON,
		Resource:  NewResource("r", "m", 1, nil),
		Malformed: MalformedResourceBadType,
	})
	resp, raw := postJSON(t, ts.URL+"/responses", `{"model":"m","input":"hi"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	if !strings.Contains(string(raw), `"output":"not-the-right-type"`) {
		t.Fatalf("body: %s", raw)
	}
}

func TestMalformed_ItemDiscriminator(t *testing.T) {
	t.Parallel()
	_, ts := startServer(t, Options{}, &Script{
		ID: "scenario-item", Description: "unknown unprefixed item", Mode: ModeJSON,
		Resource:  NewResource("r", "m", 1, nil),
		Malformed: MalformedItemDiscriminator,
	})
	resp, raw := postJSON(t, ts.URL+"/responses", `{"model":"m","input":"hi"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	if !strings.Contains(string(raw), "mystery_unprefixed_item") {
		t.Fatalf("body: %s", raw)
	}
}

func TestMalformed_BodyNotJSON(t *testing.T) {
	t.Parallel()
	_, ts := startServer(t, Options{}, &Script{
		ID: "scenario-notjson", Description: "non-json body", Mode: ModeJSON,
		Malformed: MalformedBodyNotJSON,
	})
	resp, raw := postJSON(t, ts.URL+"/responses", `{"model":"m","input":"hi"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	if string(raw) != "this is definitely not json" {
		t.Fatalf("body: %q", raw)
	}
}

func TestMalformed_OversizedBody(t *testing.T) {
	t.Parallel()
	_, ts := startServer(t, Options{}, &Script{
		ID: "scenario-oversize", Description: "oversized body", Mode: ModeJSON,
		Malformed: MalformedOversizedBody,
	})
	resp, raw := postJSON(t, ts.URL+"/responses", `{"model":"m","input":"hi"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	if len(raw) < 5<<20 {
		t.Fatalf("body must exceed client parse bound, got %d bytes", len(raw))
	}
}

func sseFetch(t *testing.T, url string) (string, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, url, strings.NewReader(`{"model":"m","input":"hi","stream":true}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer sk-test")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	return resp.Header.Get("Content-Type"), string(raw)
}

func TestMalformed_SSEEventNoHeader(t *testing.T) {
	t.Parallel()
	_, ts := startServer(t, Options{}, &Script{
		ID: "scenario-nohdr", Description: "data without event header", Mode: ModeSSE,
		Resource:  NewResource("r", "m", 1, []Item{NewMessagePartsItem("assistant", "", NewTextPart("x"))}),
		Malformed: MalformedEventNoHeader,
	})
	_, s := sseFetch(t, ts.URL+"/responses")
	if strings.Contains(s, "event: response.created") {
		t.Fatalf("event header must be omitted: %q", s)
	}
	if !strings.Contains(s, `"type":"response.created"`) {
		t.Fatalf("data payload missing: %q", s)
	}
}

func TestMalformed_SSEEventMismatch(t *testing.T) {
	t.Parallel()
	_, ts := startServer(t, Options{}, &Script{
		ID: "scenario-mismatch", Description: "header/type mismatch", Mode: ModeSSE,
		Resource:  NewResource("r", "m", 1, []Item{NewMessagePartsItem("assistant", "", NewTextPart("x"))}),
		Malformed: MalformedEventMismatch,
	})
	_, s := sseFetch(t, ts.URL+"/responses")
	if !strings.Contains(s, "event: response.failed\n") || !strings.Contains(s, `"type":"response.completed"`) {
		t.Fatalf("mismatch not served: %q", s)
	}
}

func TestMalformed_SSEDuplicateTerminal(t *testing.T) {
	t.Parallel()
	_, ts := startServer(t, Options{}, &Script{
		ID: "scenario-dupterm", Description: "duplicate terminal", Mode: ModeSSE,
		Resource:  NewResource("r", "m", 1, []Item{NewMessagePartsItem("assistant", "", NewTextPart("x"))}),
		Malformed: MalformedEventDuplicateTerminal,
	})
	_, s := sseFetch(t, ts.URL+"/responses")
	if strings.Count(s, "event: response.completed") != 2 {
		t.Fatalf("expected duplicate terminal, got: %q", s)
	}
}

func TestMalformed_SSEAfterTerminal(t *testing.T) {
	t.Parallel()
	_, ts := startServer(t, Options{}, &Script{
		ID: "scenario-afterterm", Description: "event after terminal", Mode: ModeSSE,
		Resource:  NewResource("r", "m", 1, []Item{NewMessagePartsItem("assistant", "", NewTextPart("x"))}),
		Malformed: MalformedEventAfterTerminal,
	})
	_, s := sseFetch(t, ts.URL+"/responses")
	idx := strings.Index(s, "event: response.completed")
	if idx < 0 {
		t.Fatalf("terminal missing: %q", s)
	}
	if !strings.Contains(s[idx:], "response.output_item.added") {
		t.Fatalf("event after terminal missing: %q", s)
	}
}

func TestMalformed_SSEDoneBeforeTerminal(t *testing.T) {
	t.Parallel()
	_, ts := startServer(t, Options{}, &Script{
		ID: "scenario-done-early", Description: "done before terminal", Mode: ModeSSE,
		Resource:  NewResource("r", "m", 1, []Item{NewMessagePartsItem("assistant", "", NewTextPart("x"))}),
		Malformed: MalformedDoneBeforeTerminal,
	})
	_, s := sseFetch(t, ts.URL+"/responses")
	if !strings.HasPrefix(s, "data: [DONE]") {
		t.Fatalf("stream must start with [DONE]: %q", s)
	}
	if strings.Contains(s, "response.completed") {
		t.Fatalf("terminal must not follow early done: %q", s)
	}
}

func TestMalformed_SSEMissingDONE(t *testing.T) {
	t.Parallel()
	_, ts := startServer(t, Options{}, &Script{
		ID: "scenario-nodone", Description: "missing done", Mode: ModeSSE,
		Resource:  NewResource("r", "m", 1, []Item{NewMessagePartsItem("assistant", "", NewTextPart("x"))}),
		Malformed: MalformedMissingDONE,
	})
	_, s := sseFetch(t, ts.URL+"/responses")
	if strings.Contains(s, "data: [DONE]") {
		t.Fatal("stream must omit [DONE]")
	}
	if !strings.Contains(s, "response.completed") {
		t.Fatalf("terminal must be present: %q", s)
	}
}

func TestMalformed_ContentType(t *testing.T) {
	t.Parallel()
	_, ts := startServer(t, Options{}, &Script{
		ID: "scenario-wrongct", Description: "wrong content type", Mode: ModeSSE,
		Resource:  NewResource("r", "m", 1, []Item{NewMessagePartsItem("assistant", "", NewTextPart("x"))}),
		Malformed: MalformedContentType,
	})
	ct, _ := sseFetch(t, ts.URL+"/responses")
	if !strings.Contains(ct, "application/json") {
		t.Fatalf("content-type must be wrong: %q", ct)
	}
}

func TestMalformed_CompactContentType(t *testing.T) {
	t.Parallel()
	_, ts := startServer(t, Options{}, &Script{
		ID: "scenario-wrongct-c", Description: "wrong compact content type", Mode: ModeCompact,
		CompactResource: NewCompactResource("c", "m", 1, nil),
		Malformed:       MalformedContentType,
	})
	resp, _ := postJSON(t, ts.URL+"/responses/compact", `{"model":"m","input":[{"type":"message","role":"user","content":"x"}]}`)
	if !strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("compact content-type: %q", resp.Header.Get("Content-Type"))
	}
}
