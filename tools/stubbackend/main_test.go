package main

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Claim: POSTs receive the scripted responses in order over real HTTP, the
// final response repeats once exhausted, every POST body is logged in order,
// and readiness probes (GET) consume nothing. Mutants: always serve the first
// response, skip logging, or count every method -> FAIL.
func TestStubBackend_ServesSequenceAndLogsBodies(t *testing.T) {
	t.Parallel()
	log, err := os.CreateTemp(t.TempDir(), "requests-*.log")
	if err != nil {
		t.Fatal(err)
	}
	responses := [][]byte{
		[]byte("data: {\"tool\":\"invented_tool\"}\n\ndata: [DONE]\n\n"),
		[]byte("data: {\"tool\":\"get_weather\"}\n\ndata: [DONE]\n\n"),
	}
	srv := httptest.NewServer(newStubServer(responses, log))
	t.Cleanup(srv.Close)

	if code := getStatus(t, srv.URL); code != http.StatusOK {
		t.Fatalf("readiness GET status = %d, want 200", code)
	}
	bodies := []string{`{"n":1}`, `{"n":2}`, `{"n":3}`}
	for i, body := range bodies {
		want := responses[0]
		if i > 0 {
			want = responses[1]
		}
		got, contentType := postBody(t, srv.URL, body)
		if string(got) != string(want) {
			t.Fatalf("POST %d body = %q, want %q", i+1, got, want)
		}
		if contentType != sseContentType {
			t.Fatalf("POST %d content type = %q, want %q", i+1, contentType, sseContentType)
		}
	}
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(log.Name())
	if err != nil {
		t.Fatal(err)
	}
	var wantLines []string
	for i, body := range bodies {
		wantLines = append(wantLines, "REQ"+strconv.Itoa(i+1)+" "+body)
	}
	if string(raw) != strings.Join(wantLines, "\n")+"\n" {
		t.Fatalf("request log = %q, want %q", raw, wantLines)
	}
}

// Claim: a response directory loads one response per file in filename order,
// and an empty directory is an error, not an empty sequence. Mutants: reverse
// order, or no error on empty -> FAIL.
func TestStubBackend_LoadsDirectoryInNameOrder(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "02-second.sse"), []byte("second"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "01-first.sse"), []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := loadResponses(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || string(got[0]) != "first" || string(got[1]) != "second" {
		t.Fatalf("directory responses = %q, want [first second]", got)
	}
	if _, err := loadResponses(t.TempDir()); err == nil {
		t.Fatal("empty directory must be an error")
	}
}

// Claim: a single response file splits on separator lines, and a file without
// the separator serves one response for every request. Mutant: no split ->
// FAIL.
func TestStubBackend_SplitsResponseFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	split := filepath.Join(dir, "two.sse")
	if err := os.WriteFile(split, []byte("first\n"+responseSeparator+"\nsecond\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := loadResponses(split)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || string(got[0]) != "first" || string(got[1]) != "second\n" {
		t.Fatalf("split responses = %q, want [first \"second\\n\"]", got)
	}
	single := filepath.Join(dir, "one.sse")
	if err := os.WriteFile(single, []byte("only"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err = loadResponses(single)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || string(got[0]) != "only" {
		t.Fatalf("single responses = %q, want [only]", got)
	}
	if _, err := loadResponses(filepath.Join(dir, "missing.sse")); err == nil {
		t.Fatal("missing path must be an error")
	}
}

// Claim: -print-free-ports N prints N distinct free loopback ports on one
// line and exits, so scripts can pick per-run ports without Python.
// Mutants: unknown flag, missing output line, repeated port -> FAIL.
func TestStubBackend_PrintFreePorts(t *testing.T) {
	t.Parallel()
	cmd := exec.Command("go", "run", ".", "-print-free-ports", "2")
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("print-free-ports: %v", err)
	}
	fields := strings.Fields(strings.TrimSpace(string(output)))
	if len(fields) != 2 {
		t.Fatalf("print-free-ports output = %q, want two ports on one line", output)
	}
	seen := make(map[string]bool)
	for _, field := range fields {
		port, err := strconv.Atoi(field)
		if err != nil || port < 1 || port > 65535 {
			t.Fatalf("print-free-ports output = %q, want numeric ports", output)
		}
		if seen[field] {
			t.Fatalf("print-free-ports output = %q, want distinct ports", output)
		}
		seen[field] = true
		listener, err := net.Listen("tcp", "127.0.0.1:"+field)
		if err != nil {
			t.Fatalf("printed port %s is not free: %v", field, err)
		}
		_ = listener.Close()
	}
}

func getStatus(t *testing.T, url string) int {
	t.Helper()
	resp, err := http.Get(url) //nolint:gosec // test-only local stub server
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

func postBody(t *testing.T, url, body string) ([]byte, string) {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader(body)) //nolint:gosec // test-only local stub server
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST status = %d, body = %s", resp.StatusCode, raw)
	}
	return raw, resp.Header.Get("Content-Type")
}
