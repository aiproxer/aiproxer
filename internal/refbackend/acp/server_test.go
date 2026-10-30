package acp_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	refbackend "github.com/aiproxer/aiproxer/internal/refbackend/acp"
)

func TestHandler_initializeAdvertisesSubset(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{}))
	t.Cleanup(srv.Close)

	body := `{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":1,"clientCapabilities":{},"clientInfo":{"name":"t","version":"1"}}}`
	resp := postACP(t, srv.URL, body)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	var m map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		t.Fatal(err)
	}
	if m["error"] != nil {
		t.Fatalf("unexpected error: %v", m["error"])
	}
	res, _ := m["result"].(map[string]any)
	if res == nil {
		t.Fatal("missing result")
	}
	pv, ok := res["protocolVersion"].(float64)
	if !ok || int(pv) != 1 {
		t.Fatalf("protocolVersion: %#v", res["protocolVersion"])
	}
	acaps, _ := res["agentCapabilities"].(map[string]any)
	if acaps == nil {
		t.Fatal("missing agentCapabilities")
	}
	if acaps["loadSession"] != false {
		t.Fatalf("loadSession: %#v", acaps["loadSession"])
	}
	pc, _ := acaps["promptCapabilities"].(map[string]any)
	if pc == nil || pc["resource"] != true {
		t.Fatalf("promptCapabilities.resource: %#v", pc)
	}
}

func TestHandler_sessionNewRequiresInitialize(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{}))
	t.Cleanup(srv.Close)

	body := `{"jsonrpc":"2.0","id":1,"method":"session/new","params":{"cwd":"/tmp","mcpServers":[]}}`
	resp := postACP(t, srv.URL, body)
	defer func() { _ = resp.Body.Close() }()
	var m map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		t.Fatal(err)
	}
	if m["error"] == nil {
		t.Fatal("expected error before initialize")
	}
}

func TestHandler_sessionNewReturnsUniqueIDs(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{}))
	t.Cleanup(srv.Close)

	mustInit(t, srv.URL)
	id1 := mustSessionNew(t, srv.URL)
	id2 := mustSessionNew(t, srv.URL)
	if id1 == "" || id2 == "" || id1 == id2 {
		t.Fatalf("session ids: %q %q", id1, id2)
	}
}

func TestHandler_sessionPromptStreamsUpdatesThenEndTurn(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{}))
	t.Cleanup(srv.Close)

	mustInit(t, srv.URL)
	sid := mustSessionNew(t, srv.URL)

	prompt := `{"jsonrpc":"2.0","id":10,"method":"session/prompt","params":{"sessionId":` + jsonQuote(sid) + `,"prompt":[{"type":"text","text":"hi"}]}}`
	resp := postACP(t, srv.URL, prompt)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "ndjson") {
		t.Fatalf("content-type: %q", ct)
	}
	lines, err := scanNDJSONLines(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) < 2 {
		t.Fatalf("expected ≥2 ndjson lines, got %d", len(lines))
	}
	if !strings.Contains(lines[0], `"session/update"`) || !strings.Contains(lines[0], `"plan"`) {
		t.Fatalf("first line plan update: %s", lines[0])
	}
	last := lines[len(lines)-1]
	if !strings.Contains(last, `"stopReason"`) || !strings.Contains(last, `"end_turn"`) {
		t.Fatalf("final line: %s", last)
	}
}

func TestHandler_sessionPromptResourceEchoInChunk(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{}))
	t.Cleanup(srv.Close)

	mustInit(t, srv.URL)
	sid := mustSessionNew(t, srv.URL)

	prompt := `{"jsonrpc":"2.0","id":11,"method":"session/prompt","params":{"sessionId":` + jsonQuote(sid) + `,"prompt":[{"type":"text","text":"x"},{"type":"resource","resource":{"uri":"file:///tmp/a.py","mimeType":"text/x-python","text":"print(1)"}}]}}`
	resp := postACP(t, srv.URL, prompt)
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(body, []byte("saw resource:")) || !bytes.Contains(body, []byte("file:///tmp/a.py")) {
		t.Fatalf("body: %s", string(body))
	}
}

func TestHandler_sessionCancelEndsPromptWithCancelled(t *testing.T) {
	t.Parallel()
	planReached := make(chan struct{}, 1)
	continuePrompt := make(chan struct{})
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{
		OnPromptUpdate: func(stage, sessionID string) {
			if stage != "plan" {
				return
			}
			select {
			case planReached <- struct{}{}:
			default:
			}
			<-continuePrompt
		},
	}))
	t.Cleanup(srv.Close)

	mustInit(t, srv.URL)
	sid := mustSessionNew(t, srv.URL)

	prompt := `{"jsonrpc":"2.0","id":20,"method":"session/prompt","params":{"sessionId":` + jsonQuote(sid) + `,"prompt":[{"type":"text","text":"slow"}]}}`

	type result struct {
		lines []string
		err   error
	}
	ch := make(chan result, 1)
	go func() {
		resp, err := http.Post(srv.URL+"/v1/acp", "application/json", strings.NewReader(prompt))
		if err != nil {
			ch <- result{err: err}
			return
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			ch <- result{err: fmt.Errorf("prompt status %d", resp.StatusCode)}
			return
		}
		lines, err := scanNDJSONLines(resp.Body)
		ch <- result{lines: lines, err: err}
	}()

	<-planReached
	cancel := `{"jsonrpc":"2.0","method":"session/cancel","params":{"sessionId":` + jsonQuote(sid) + `}}`
	resp2 := postACP(t, srv.URL, cancel)
	_ = resp2.Body.Close()
	if resp2.StatusCode != http.StatusNoContent {
		t.Fatalf("cancel status %d", resp2.StatusCode)
	}
	close(continuePrompt)

	r := <-ch
	if r.err != nil {
		t.Fatal(r.err)
	}
	if len(r.lines) < 2 {
		t.Fatalf("lines: %v", r.lines)
	}
	last := r.lines[len(r.lines)-1]
	if !strings.Contains(last, `"cancelled"`) {
		t.Fatalf("expected cancelled stopReason, last=%s", last)
	}
}

func TestHandler_sessionLoadRejected(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{}))
	t.Cleanup(srv.Close)
	mustInit(t, srv.URL)

	body := `{"jsonrpc":"2.0","id":3,"method":"session/load","params":{"sessionId":"sess_ref_1","cwd":"/tmp","mcpServers":[]}}`
	resp := postACP(t, srv.URL, body)
	defer func() { _ = resp.Body.Close() }()
	var m map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		t.Fatal(err)
	}
	if m["error"] == nil {
		t.Fatal("expected error for session/load")
	}
}

func postACP(t *testing.T, baseURL, body string) *http.Response {
	t.Helper()
	resp, err := http.Post(baseURL+"/v1/acp", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func mustInit(t *testing.T, baseURL string) {
	t.Helper()
	body := `{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":1,"clientCapabilities":{},"clientInfo":{"name":"t","version":"1"}}}`
	resp := postACP(t, baseURL, body)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("init status %d", resp.StatusCode)
	}
}

func mustSessionNew(t *testing.T, baseURL string) string {
	t.Helper()
	body := `{"jsonrpc":"2.0","id":1,"method":"session/new","params":{"cwd":"/tmp","mcpServers":[]}}`
	resp := postACP(t, baseURL, body)
	defer func() { _ = resp.Body.Close() }()
	var m map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		t.Fatal(err)
	}
	res, _ := m["result"].(map[string]any)
	if res == nil {
		t.Fatalf("no result: %v", m)
	}
	sid, _ := res["sessionId"].(string)
	return sid
}

func jsonQuote(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func scanNDJSONLines(r io.Reader) ([]string, error) {
	sc := bufio.NewScanner(r)
	buf := make([]byte, 0, 64*1024)
	sc.Buffer(buf, 1024*1024)
	var lines []string
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line != "" {
			lines = append(lines, line)
		}
	}
	if err := sc.Err(); err != nil {
		return lines, err
	}
	return lines, nil
}
