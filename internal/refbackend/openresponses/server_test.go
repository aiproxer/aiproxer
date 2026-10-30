package openresponses

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func postJSON(t *testing.T, url string, body string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer sk-test")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	return resp, raw
}

func TestServer_JSONCreateServesCompleteResource(t *testing.T) {
	t.Parallel()
	_, ts := startServer(t, Options{}, &Script{
		ID: "scenario-json-text", Description: "JSON text create", Mode: ModeJSON,
		Expected: ExpectedRequest{Model: "gpt-openresponses-1", MinInputItems: 1},
		Resource: NewResource("resp_json_1", "gpt-openresponses-1", 1719900000, []Item{
			NewMessageItem("assistant", "output_text", "json ok"),
		}),
	})
	resp, raw := postJSON(t, ts.URL+"/v1/responses", `{"model":"gpt-openresponses-1","input":[{"type":"message","role":"user","content":"hi"}]}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d body: %s", resp.StatusCode, raw)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("content-type: %q", ct)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if string(m["id"]) != `"resp_json_1"` || string(m["status"]) != `"completed"` {
		t.Fatalf("resource: %s", raw)
	}
	for _, f := range requiredResponseFields {
		if _, ok := m[f]; !ok {
			t.Errorf("required field %q missing", f)
		}
	}
}

func TestServer_StrictRequestAssertions(t *testing.T) {
	t.Parallel()
	stream := false
	_, ts := startServer(t, Options{}, &Script{
		ID: "scenario-strict", Description: "strict request capture", Mode: ModeJSON,
		Expected: ExpectedRequest{
			Model: "m", Stream: &stream, MinInputItems: 1, MaxInputItems: 1,
			RequireTools: 1, ContentType: "application/json",
			Contains: []string{`"tool_choice"`, `"instructions"`},
			MustOmit: []string{"previous_response_id"},
		},
		Resource: NewResource("r", "m", 1, nil),
	})
	body := `{"model":"m","input":[{"type":"message","role":"user","content":"hi"}],"tools":[{"type":"function","name":"f"}],"tool_choice":"auto","instructions":"be brief"}`
	resp, raw := postJSON(t, ts.URL+"/responses", body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d body: %s", resp.StatusCode, raw)
	}
}

func TestServer_ExpectationMismatchCounted(t *testing.T) {
	t.Parallel()
	srv, ts := startServer(t, Options{}, &Script{
		ID: "scenario-mismatch", Description: "mismatch on purpose", Mode: ModeJSON,
		Expected: ExpectedRequest{Model: "expected-model", MinInputItems: 1},
		Resource: NewResource("r", "m", 1, nil),
	})
	resp, raw := postJSON(t, ts.URL+"/responses", `{"model":"other-model","input":[{"type":"message","role":"user","content":"hi"}]}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	if !strings.Contains(string(raw), "expectation_mismatch") {
		t.Fatalf("body: %s", raw)
	}
	if srv.MismatchCount() != 1 {
		t.Fatalf("mismatch count: %d", srv.MismatchCount())
	}
	if obs, ok := srv.Capture().Last(); !ok || !strings.Contains(string(obs.Body), "other-model") {
		t.Fatalf("capture: %+v", obs)
	}
}

func TestServer_SSECreateServesLifecycle(t *testing.T) {
	t.Parallel()
	stream := true
	_, ts := startServer(t, Options{}, &Script{
		ID: "scenario-sse-text", Description: "SSE text create", Mode: ModeSSE,
		Expected: ExpectedRequest{Model: "gpt-openresponses-1", Stream: &stream, MinInputItems: 1},
		Resource: NewResource("resp_sse_1", "gpt-openresponses-1", 1719900600, []Item{
			NewMessagePartsItem("assistant", "", NewTextPart("stream ok")),
		}),
	})
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/v1/responses", strings.NewReader(`{"model":"gpt-openresponses-1","input":[{"type":"message","role":"user","content":"hi"}],"stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer sk-test")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content-type: %q", ct)
	}
	raw, _ := io.ReadAll(resp.Body)
	s := string(raw)
	for _, want := range []string{
		"event: response.created",
		"event: response.output_item.added",
		"event: response.content_part.added",
		"event: response.output_text.delta",
		"event: response.output_text.done",
		"event: response.output_item.done",
		"event: response.completed",
		"data: [DONE]",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in stream", want)
		}
	}
	if strings.Count(s, "event: response.completed") != 1 {
		t.Fatalf("expected exactly one terminal, got %d", strings.Count(s, "event: response.completed"))
	}
}

func TestServer_CompactServesCompactResource(t *testing.T) {
	t.Parallel()
	_, ts := startServer(t, Options{}, &Script{
		ID: "scenario-compact", Description: "standalone compact", Mode: ModeCompact,
		Expected: ExpectedRequest{Model: "m", MinInputItems: 1},
		CompactResource: NewCompactResource("resp_compact_1", "m", 1719900000, []Item{
			NewCompactionItem("cmp_1", ""),
		}),
	})
	resp, raw := postJSON(t, ts.URL+"/v1/responses/compact", `{"model":"m","input":[{"type":"message","role":"user","content":"compress"}]}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d body: %s", resp.StatusCode, raw)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if string(m["object"]) != `"response.compaction"` || string(m["id"]) != `"resp_compact_1"` {
		t.Fatalf("compact: %s", raw)
	}
}

func TestServer_StatusOverride(t *testing.T) {
	t.Parallel()
	_, ts := startServer(t, Options{}, &Script{
		ID: "scenario-202", Description: "status override", Mode: ModeJSON,
		Resource: NewResource("r", "m", 1, nil),
		Status:   http.StatusAccepted,
	})
	resp, _ := postJSON(t, ts.URL+"/responses", `{"model":"m","input":"hi"}`)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status: %d", resp.StatusCode)
	}
}

func TestServer_WrongRoute404(t *testing.T) {
	t.Parallel()
	_, ts := startServer(t, Options{}, &Script{
		ID: "scenario-x", Description: "x", Mode: ModeJSON, Resource: NewResource("r", "m", 1, nil),
	})
	resp, err := http.Get(ts.URL + "/v1/other")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status: %d", resp.StatusCode)
	}
}

func TestServer_NoActiveScript503(t *testing.T) {
	t.Parallel()
	srv := NewServer(Options{AllowMissingBearer: true})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	resp, raw := postJSON(t, ts.URL+"/responses", `{"model":"m","input":"hi"}`)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status: %d body: %s", resp.StatusCode, raw)
	}
	if !strings.Contains(string(raw), "no_active_script") {
		t.Fatalf("body: %s", raw)
	}
}

func TestServer_CaptureRedactsAuthorization(t *testing.T) {
	t.Parallel()
	srv, ts := startServer(t, Options{}, &Script{
		ID: "scenario-redact", Description: "redaction", Mode: ModeJSON,
		Resource: NewResource("r", "m", 1, nil),
	})
	postJSON(t, ts.URL+"/responses", `{"model":"m","input":"hi"}`)
	if srv.Capture().Total() != 1 {
		t.Fatalf("total: %d", srv.Capture().Total())
	}
	obs, ok := srv.Capture().Last()
	if !ok {
		t.Fatal("no observation")
	}
	if !obs.Redacted || obs.Headers.Get("Authorization") != RedactedAuthorization {
		t.Fatalf("authorization not redacted: %+v", obs.Headers)
	}
	if srv.Capture().Count("/responses") != 1 {
		t.Fatalf("per-path count: %d", srv.Capture().Count("/responses"))
	}
}

func TestServer_MalformedRequestBodyRejected(t *testing.T) {
	t.Parallel()
	_, ts := startServer(t, Options{}, &Script{
		ID: "scenario-x", Description: "x", Mode: ModeJSON, Resource: NewResource("r", "m", 1, nil),
	})
	resp, raw := postJSON(t, ts.URL+"/responses", `{"input":123}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status: %d body: %s", resp.StatusCode, raw)
	}
	if !strings.Contains(string(raw), "malformed_body") {
		t.Fatalf("body: %s", raw)
	}
}

func TestServer_OversizedRequestBodyRejected(t *testing.T) {
	t.Parallel()
	_, ts := startServer(t, Options{MaxBodyBytes: 1024}, &Script{
		ID: "scenario-x", Description: "x", Mode: ModeJSON, Resource: NewResource("r", "m", 1, nil),
	})
	big := `{"model":"m","input":"` + strings.Repeat("a", 4096) + `"}`
	resp, raw := postJSON(t, ts.URL+"/responses", big)
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status: %d body: %s", resp.StatusCode, raw)
	}
}

func TestServer_RedactBodiesOption(t *testing.T) {
	t.Parallel()
	srv, ts := startServer(t, Options{RedactBodies: true}, &Script{
		ID: "scenario-x", Description: "x", Mode: ModeJSON, Resource: NewResource("r", "m", 1, nil),
	})
	postJSON(t, ts.URL+"/responses", `{"model":"m","input":"secret-body"}`)
	obs, ok := srv.Capture().Last()
	if !ok || string(obs.Body) != RedactedAuthorization {
		t.Fatalf("body not redacted: %+v", obs)
	}
}

// TestServer_ConcurrentCreates exercises concurrent scripted requests with atomic
// counters under the race detector.
func TestServer_ConcurrentCreates(t *testing.T) {
	t.Parallel()
	srv, ts := startServer(t, Options{}, &Script{
		ID: "scenario-concurrent", Description: "concurrent", Mode: ModeJSON,
		Expected: ExpectedRequest{Model: "m"},
		Resource: NewResource("r", "m", 1, nil),
	})
	const n = 40
	errs := make(chan error, n)
	for range n {
		go func() {
			resp, raw := postJSON(t, ts.URL+"/responses", `{"model":"m","input":"hi"}`)
			if resp.StatusCode != http.StatusOK {
				errs <- &httpError{code: resp.StatusCode, body: string(raw)}
				return
			}
			errs <- nil
		}()
	}
	for range n {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent create: %v", err)
		}
	}
	if srv.Capture().Total() != n {
		t.Fatalf("total: %d want %d", srv.Capture().Total(), n)
	}
	if srv.Capture().Count("/responses") != n {
		t.Fatalf("per-path: %d", srv.Capture().Count("/responses"))
	}
}

type httpError struct {
	code int
	body string
}

func (e *httpError) Error() string {
	return "unexpected status " + string(rune(e.code)) + ": " + e.body
}

var _ = context.Background
