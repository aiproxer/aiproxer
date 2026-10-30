//go:build productsmoke

package productsmoke

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// evidenceEnv names the directory that receives one subdirectory per journey.
// The design allows a fallback when it is unset; a fallback directory is
// logged so the artifacts remain discoverable.
const evidenceEnv = "AIP_PRODUCT_SMOKE_EVIDENCE"

// redactedMarker replaces a canary wherever evidence or a failure message would
// otherwise carry it.
const redactedMarker = "[REDACTED:canary]"

// Diagnostic cuts. Raw strings are always redacted before one of these limits
// bounds them: truncating first can split a credential into a fragment that
// exact-match redaction no longer recognizes.
const (
	wireRequestBodyExcerpt = 4096
	wireErrorExcerpt       = 1024
	wireHeaderExcerpt      = 512
	wireBodyExcerpt        = 16384
	wireReportExcerpt      = 4096
	providerExcerpt        = 4096
	proxyTailExcerpt       = 8 << 10

	// Assertion messages excerpt observed wire content too; those cuts apply
	// the same redact-then-bound order as every other excerpt.
	wireFailureBodyExcerpt   = 1024
	wireFailureDetailExcerpt = 200

	// logFileMaxBytes caps the on-disk proxy log. The in-memory tail keeps
	// receiving every byte; logFileTruncationMarker is written once at the cut
	// so the file itself says that it is incomplete.
	logFileMaxBytes         = 4 << 20
	logFileTruncationMarker = "\n... [proxy log file truncated at the capture bound; later output is only in the bounded in-memory tail]\n"
)

// redactor replaces every configured credential in one diagnostic string.
type redactor func(string) string

// excerpt redacts raw first and only then bounds it.
func excerpt(redact redactor, raw string, limit int) string {
	return truncate(redact(raw), limit)
}

// evidence owns one journey's artifact directory.
type evidence struct {
	t      *testing.T
	dir    string
	canary string
}

// newEvidence prepares the journey's evidence directory. The directory is
// created eagerly so a later failure cannot lose the artifacts.
func newEvidence(t *testing.T, canary string) *evidence {
	t.Helper()
	dir := ""
	if root := strings.TrimSpace(os.Getenv(evidenceEnv)); root != "" {
		dir = filepath.Join(root, journeySlug(t.Name()))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("product smoke: create evidence directory %q: %v", dir, err)
		}
	} else {
		fallback, err := os.MkdirTemp("", "product-smoke-evidence-")
		if err != nil {
			t.Fatalf("product smoke: create fallback evidence directory: %v", err)
		}
		dir = fallback
		t.Logf("product smoke: %s is unset; evidence written to %s", evidenceEnv, dir)
	}
	return &evidence{t: t, dir: dir, canary: canary}
}

// journeySlug maps a test name to a single path element.
func journeySlug(name string) string {
	replacer := strings.NewReplacer("/", "_", "\\", "_", " ", "_", ":", "_")
	return replacer.Replace(name)
}

func (e *evidence) path(name string) string {
	return filepath.Join(e.dir, name)
}

// redact removes the canary from s.
func (e *evidence) redact(s string) string {
	if e == nil || e.canary == "" {
		return s
	}
	return strings.ReplaceAll(s, e.canary, redactedMarker)
}

// write stores redacted content under name and returns its path. Writing
// failures are logged rather than fatal so evidence collection never hides the
// original journey failure.
func (e *evidence) write(name, content string) string {
	path := e.path(name)
	content = e.redact(content)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		e.t.Logf("product smoke: write evidence %q: %v", path, err)
	}
	return path
}

// logSink redirects proxy stdout and stderr into a bounded, redacted evidence
// file and an in-memory tail used by failure messages. It counts redactions so
// the canary journey can assert the proxy log never contained the credential.
type logSink struct {
	mu        sync.Mutex
	secret    string
	hold      []byte
	file      *os.File
	tail      []byte
	tailMax   int
	fileMax   int
	written   int
	fileDone  bool
	writeErr  error
	leaks     int
	firstLeak string
	closed    bool
}

func newLogSink(t *testing.T, path, secret string) *logSink {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatalf("product smoke: open proxy log %q: %v", path, err)
	}
	return &logSink{secret: secret, file: f, tailMax: 256 << 10, fileMax: logFileMaxBytes}
}

// Write buffers enough trailing bytes to never split the canary across write
// boundaries, redacts the safe prefix and mirrors it to the evidence file.
func (s *logSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return len(p), nil
	}
	s.hold = append(s.hold, p...)
	s.flushHeld()
	return len(p), nil
}

// flushHeld emits every held byte that cannot be part of a canary match that
// still needs more input. A credential that spans a write boundary, or whose
// match crosses the flush cut, stays in the hold until it can be redacted.
func (s *logSink) flushHeld() {
	if s.secret == "" {
		s.emit(s.hold)
		s.hold = nil
		return
	}
	keep := len(s.secret) - 1
	cut := len(s.hold) - keep
	if cut <= 0 {
		return
	}
	for {
		moved := false
		for i := 0; i < cut; i++ {
			if s.hold[i] != s.secret[0] {
				continue
			}
			n := len(s.hold) - i
			if n > len(s.secret) {
				n = len(s.secret)
			}
			if string(s.hold[i:i+n]) == s.secret[:n] && i+n > cut {
				cut = i
				moved = true
				break
			}
		}
		if !moved {
			break
		}
	}
	if cut <= 0 {
		return
	}
	chunk := s.redact(s.hold[:cut])
	s.emit(chunk)
	s.hold = append(s.hold[:0], s.hold[cut:]...)
}

// Close flushes the held tail and closes the evidence file.
func (s *logSink) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return s.writeErr
	}
	s.closed = true
	if len(s.hold) > 0 {
		s.emit(s.redact(s.hold))
		s.hold = nil
	}
	if err := s.file.Close(); err != nil && s.writeErr == nil {
		s.writeErr = err
	}
	return s.writeErr
}

// Flush emits the held tail so leak checks see the whole captured log.
func (s *logSink) Flush() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || len(s.hold) == 0 {
		return
	}
	s.emit(s.redact(s.hold))
	s.hold = nil
}

// Tail returns the last n redacted bytes captured from the proxy.
func (s *logSink) Tail(n int) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n <= 0 || n > len(s.tail) {
		n = len(s.tail)
	}
	return string(s.tail[len(s.tail)-n:])
}

// Redactions reports how often the canary was replaced in the proxy log. A
// non-zero value means the proxy log contained the credential.
func (s *logSink) Redactions() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.leaks
}

// FirstLeak returns a redacted excerpt of the first log region that contained
// the canary, or "" when the log never did.
func (s *logSink) FirstLeak() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.firstLeak
}

func (s *logSink) emit(chunk []byte) {
	if len(chunk) == 0 {
		return
	}
	s.mirror(chunk)
	s.tail = append(s.tail, chunk...)
	if over := len(s.tail) - s.tailMax; over > 0 {
		s.tail = append(s.tail[:0], s.tail[over:]...)
	}
}

// mirror writes redacted chunk to the evidence file, capping the file at
// fileMax bytes and marking the cut once. The in-memory tail is unaffected.
func (s *logSink) mirror(chunk []byte) {
	if s.fileDone {
		return
	}
	room := s.fileMax - s.written
	if room <= 0 {
		s.markFileTruncated()
		return
	}
	if len(chunk) > room {
		s.writeFile(chunk[:room])
		s.markFileTruncated()
		return
	}
	s.writeFile(chunk)
}

func (s *logSink) writeFile(chunk []byte) {
	if len(chunk) == 0 {
		return
	}
	if _, err := s.file.Write(chunk); err != nil && s.writeErr == nil {
		s.writeErr = err
	}
	s.written += len(chunk)
}

func (s *logSink) markFileTruncated() {
	if s.fileDone {
		return
	}
	s.fileDone = true
	if _, err := s.file.WriteString(logFileTruncationMarker); err != nil && s.writeErr == nil {
		s.writeErr = err
	}
}

func (s *logSink) redact(chunk []byte) []byte {
	if s.secret == "" || !bytes.Contains(chunk, []byte(s.secret)) {
		return chunk
	}
	s.leaks++
	if s.firstLeak == "" {
		for _, line := range strings.Split(string(chunk), "\n") {
			if strings.Contains(line, s.secret) {
				redacted := strings.ReplaceAll(line, s.secret, redactedMarker)
				s.firstLeak = truncate(redacted, 400)
				break
			}
		}
	}
	return bytes.ReplaceAll(chunk, []byte(s.secret), []byte(redactedMarker))
}

// errClientCancelled marks a client transcript that ends at a deliberate
// cancellation: the journey stopped reading on purpose after the frame it
// observed, so every later byte was never inspected. A cancelled capture is
// partial by design and must never be presented as a complete observation.
var errClientCancelled = errors.New("intentional client cancellation")

// clientWire is the client-side transcript of one HTTP exchange.
type clientWire struct {
	Method      string
	URL         string
	RequestBody []byte
	Status      int
	Header      http.Header
	Body        []byte
	Err         error
}

// render renders the exchange for evidence. Every raw field is redacted before
// it is bounded, and the caller redacts the assembled report once more.
//
// A read error keeps the status, headers and partial body the exchange already
// observed, so an aborted stream is still visible in the transcript. A failure
// before any response says so instead of implying the response was empty.
func (w *clientWire) render(redact redactor) string {
	if w == nil {
		return "(no client exchange)"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s\n", w.Method, w.URL)
	fmt.Fprintf(&b, "request body: %s\n", excerpt(redact, string(w.RequestBody), wireRequestBodyExcerpt))
	if w.Err != nil {
		fmt.Fprintf(&b, "error: %s\n", excerpt(redact, w.Err.Error(), wireErrorExcerpt))
	}
	if !w.responded() {
		if w.Err != nil {
			b.WriteString("(no response was received)\n")
		}
		return b.String()
	}
	if w.Err != nil {
		switch {
		case errors.Is(w.Err, errClientCaptureIncomplete):
			b.WriteString("(INCOMPLETE CAPTURE: the response continued past the capture bound; its unread suffix was never inspected)\n")
		case errors.Is(w.Err, errClientCancelled):
			b.WriteString("(PARTIAL CAPTURE: the journey cancelled this exchange deliberately after the first dispatched frame; the rest of the response was never read)\n")
		default:
			b.WriteString("(the response below was received before the error)\n")
		}
	}
	fmt.Fprintf(&b, "status: %d\n", w.Status)
	for _, key := range []string{"Content-Type", "Content-Length", "Transfer-Encoding"} {
		if values := w.Header.Values(key); len(values) > 0 {
			fmt.Fprintf(&b, "%s: %s\n", key, excerpt(redact, strings.Join(values, ", "), wireHeaderExcerpt))
		}
	}
	fmt.Fprintf(&b, "body (%d bytes received):\n%s\n", len(w.Body), excerpt(redact, string(w.Body), wireBodyExcerpt))
	return b.String()
}

// responded reports whether any part of the response was observed.
func (w *clientWire) responded() bool {
	return w.Status != 0 || len(w.Header) > 0 || len(w.Body) > 0
}

// canaryAbsenceError reports whether the captured exchange proves the canary
// never reached the client. Only a complete capture can prove absence: an
// exchange that stopped on a read error or at the capture bound never inspected
// the bytes that follow.
func (w *clientWire) canaryAbsenceError(canary string) error {
	if w == nil {
		return errors.New("no client exchange was captured")
	}
	if w.Err != nil {
		return fmt.Errorf("the client response capture is incomplete, so the canary cannot be ruled out of the unread bytes: %v", w.Err)
	}
	if strings.Contains(string(w.Body), canary) {
		return errors.New("the canary appeared in the client response body")
	}
	for name, values := range w.Header {
		for _, value := range values {
			if strings.Contains(value, canary) {
				return fmt.Errorf("the canary appeared in client response header %s", name)
			}
		}
	}
	return nil
}

// failureContext carries what a journey failure must report.
type failureContext struct {
	sut      *sut
	provider *fakeProvider
	wire     *clientWire
}

// redactor is the credential scrubber for this failure context. It prefers the
// journey's evidence and falls back to the provider's own canary so a context
// can never render diagnostics with nothing to redact.
func (fc failureContext) redactor() redactor {
	if fc.sut != nil && fc.sut.evidence != nil {
		return fc.sut.evidence.redact
	}
	if fc.provider != nil && fc.provider.canary != "" {
		secret := fc.provider.canary
		return func(s string) string { return strings.ReplaceAll(s, secret, redactedMarker) }
	}
	return func(s string) string { return s }
}

// failureReport renders requirement 2.5's witnesses for a failed journey: the
// journey name, the provider request log, the client wire transcript and the
// proxy log tail. A witness the journey never produced is reported as
// unavailable rather than fabricated. Raw strings are redacted before they are
// bounded, and the assembled report is redacted once more as defense in depth.
func failureReport(t *testing.T, fc failureContext, reason string) string {
	var report strings.Builder
	fmt.Fprintf(&report, "product smoke journey %s failed: %s\n", t.Name(), reason)

	var ev *evidence
	if fc.sut != nil {
		ev = fc.sut.evidence
	}
	redact := fc.redactor()
	artifact := func(name, content string) string {
		if ev == nil {
			return "unavailable: no evidence directory"
		}
		return ev.write(name, content)
	}

	if fc.provider == nil {
		report.WriteString("provider requests: unavailable (this journey recorded no fake provider)\n")
	} else {
		raw := fc.provider.RequestLog()
		fmt.Fprintf(&report, "provider requests (%s):\n%s\n", artifact("provider-requests.txt", raw), truncate(redact(raw), providerExcerpt))
	}
	if fc.wire == nil {
		report.WriteString("client wire: unavailable (this journey performed no client exchange)\n")
	} else {
		raw := fc.wire.render(redact)
		fmt.Fprintf(&report, "client wire (%s):\n%s\n", artifact("client-wire.txt", raw), truncate(redact(raw), wireReportExcerpt))
	}
	if fc.sut == nil || fc.sut.logs == nil {
		report.WriteString("proxy log: unavailable (no proxy process was captured)\n")
	} else {
		path := "unavailable: no evidence directory"
		if ev != nil {
			path = ev.path("proxy.log")
		}
		fmt.Fprintf(&report, "proxy log (%s), tail:\n%s\n", path, redact(fc.sut.logs.Tail(proxyTailExcerpt)))
	}

	text := redact(report.String())
	if ev != nil {
		ev.write("failure.txt", text)
	}
	return text
}

// failf reports a journey failure with the client wire, the provider request
// log and the proxy log tail, writing the full artifacts under evidence.
func failf(t *testing.T, fc failureContext, format string, args ...any) {
	t.Helper()
	t.Fatalf("%s", failureReport(t, fc, fmt.Sprintf(format, args...)))
}

// truncate keeps s under limit bytes and marks the cut.
func truncate(s string, limit int) string {
	if limit <= 0 || len(s) <= limit {
		return s
	}
	return s[:limit] + fmt.Sprintf("\n... [truncated %d bytes]", len(s)-limit)
}

var _ io.Writer = (*logSink)(nil)

// TestLogSinkRedactsAcrossWrites proves evidence never stores the canary even
// when a write boundary splits it, and that no surrounding bytes are lost.
func TestLogSinkRedactsAcrossWrites(t *testing.T) {
	t.Parallel()
	const canary = "smoke-canary-0123456789abcdef"
	path := filepath.Join(t.TempDir(), "proxy.log")
	sink := newLogSink(t, path, canary)
	half := len(canary) / 2
	if _, err := sink.Write([]byte("before " + canary[:half])); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if _, err := sink.Write([]byte(canary[half:] + " after")); err != nil {
		t.Fatalf("second write: %v", err)
	}
	if tail := sink.Tail(1 << 10); strings.Contains(tail, canary) {
		t.Fatalf("in-memory tail contains the canary: %q", tail)
	}
	if err := sink.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if got := sink.Redactions(); got != 1 {
		t.Fatalf("redactions %d, want 1", got)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read evidence log: %v", err)
	}
	text := string(raw)
	if strings.Contains(text, canary) {
		t.Fatalf("evidence log contains the canary: %q", text)
	}
	if !strings.Contains(text, redactedMarker) {
		t.Fatalf("evidence log has no redaction marker: %q", text)
	}
	if !strings.Contains(text, "before ") || !strings.Contains(text, " after") {
		t.Fatalf("evidence log lost surrounding bytes: %q", text)
	}
}

// testCanary is the synthetic credential the evidence self-checks use. It has
// the shape newCanary produces.
const testCanary = "smoke-canary-0123456789abcdef0123456789abcdef"

// assertNoCanaryFragment fails when any window of the credential long enough to
// be usable survives in text.
func assertNoCanaryFragment(t *testing.T, text, canary string) {
	t.Helper()
	const window = 8
	for i := 0; i+window <= len(canary); i++ {
		if fragment := canary[i : i+window]; strings.Contains(text, fragment) {
			t.Fatalf("credential fragment %q survived in the evidence:\n%s", fragment, truncate(text, 600))
		}
	}
}

// localEvidence builds an evidence directory without touching
// AIP_PRODUCT_SMOKE_EVIDENCE, so self-checks stay independent of the run.
func localEvidence(t *testing.T, canary string) *evidence {
	t.Helper()
	return &evidence{t: t, dir: t.TempDir(), canary: canary}
}

// TestEvidenceRedactsBeforeTruncation proves a credential crossing a bounded
// output cut cannot survive as a fragment: the client body cut, the provider
// excerpt cut, the assertion-message cut and the proxy tail cut all redact
// before they truncate.
func TestEvidenceRedactsBeforeTruncation(t *testing.T) {
	t.Parallel()

	t.Run("client_body_cut", func(t *testing.T) {
		t.Parallel()
		ev := localEvidence(t, testCanary)
		// The credential starts half its length before the body cut, so
		// truncating the raw body would leave the first half of it behind.
		body := strings.Repeat("x", wireBodyExcerpt-len(testCanary)/2) + testCanary + strings.Repeat("y", 64)
		wire := &clientWire{
			Method:      http.MethodPost,
			URL:         "http://127.0.0.1:1" + openAIChatPath,
			RequestBody: []byte(`{"model":"smoke/chat-a"}`),
			Status:      http.StatusOK,
			Header:      http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:        []byte(body),
		}
		got := wire.render(ev.redact)
		assertNoCanaryFragment(t, got, testCanary)
		if !strings.Contains(got, redactedMarker) {
			t.Fatalf("the redacted body carries no redaction marker:\n%s", truncate(got, 600))
		}
	})

	t.Run("provider_excerpt_cut", func(t *testing.T) {
		t.Parallel()
		ev := localEvidence(t, testCanary)
		// Learn where the credential lands in the rendered provider log, then
		// shift it so the excerpt cut splits it.
		base := providerLogCanaryOffset(t, testCanary)
		pad := providerExcerpt - len(testCanary)/2 - base
		if pad < 0 {
			t.Fatalf("provider log prefix is longer than the excerpt cut (%d bytes)", base)
		}
		provider := scriptedProviderForLog(t, testCanary, strings.Repeat("p", pad)+testCanary)
		sink := newLogSink(t, filepath.Join(ev.dir, "proxy.log"), testCanary)
		t.Cleanup(func() { _ = sink.Close() })
		report := failureReport(t, failureContext{sut: &sut{evidence: ev, logs: sink}, provider: provider},
			"synthetic provider excerpt cut")
		assertNoCanaryFragment(t, report, testCanary)
		if !strings.Contains(report, redactedMarker) {
			t.Fatalf("the provider excerpt carries no redaction marker:\n%s", truncate(report, 600))
		}
		if !strings.Contains(report, "provider requests (") {
			t.Fatalf("the report omits the provider section:\n%s", truncate(report, 600))
		}
	})

	t.Run("assertion_message_cut", func(t *testing.T) {
		t.Parallel()
		ev := localEvidence(t, testCanary)
		// A failed journey reports an excerpt of the observed body inside its
		// assertion message; that cut redacts before it bounds too.
		body := strings.Repeat("x", wireFailureBodyExcerpt-len(testCanary)/2) + testCanary + strings.Repeat("y", 64)
		wire := &clientWire{
			Method: http.MethodPost,
			URL:    "http://127.0.0.1:1" + openAIChatPath,
			Status: http.StatusInternalServerError,
			Header: http.Header{"Content-Type": []string{"application/json"}},
			Body:   []byte(body),
		}
		err := checkOpenAICollect(wire, smokeScript(), ev.redact)
		if err == nil {
			t.Fatal("a non-200 response did not fail the collect check")
		}
		assertNoCanaryFragment(t, err.Error(), testCanary)
		if !strings.Contains(err.Error(), redactedMarker) {
			t.Fatalf("the assertion message carries no redaction marker: %v", err)
		}
	})

	t.Run("proxy_tail_cut", func(t *testing.T) {
		t.Parallel()
		ev := localEvidence(t, testCanary)
		sink := newLogSink(t, ev.path("proxy.log"), testCanary)
		t.Cleanup(func() { _ = sink.Close() })
		// The credential straddles the front of the bounded tail: without
		// redaction before the tail is cut, its last bytes survive.
		trailing := proxyTailExcerpt - 20
		if _, err := sink.Write([]byte(strings.Repeat("z", 256) + testCanary + strings.Repeat("z", trailing))); err != nil {
			t.Fatalf("write proxy log: %v", err)
		}
		report := failureReport(t, failureContext{sut: &sut{evidence: ev, logs: sink}}, "synthetic proxy tail cut")
		assertNoCanaryFragment(t, report, testCanary)
		for _, want := range []string{"provider requests:", "client wire:", "proxy log (", "unavailable"} {
			if !strings.Contains(report, want) {
				t.Fatalf("the report is missing %q:\n%s", want, truncate(report, 600))
			}
		}
	})
}

// providerLogCanaryOffset renders one scripted provider request carrying the
// canary and reports the canary's byte offset in the log.
func providerLogCanaryOffset(t *testing.T, canary string) int {
	t.Helper()
	provider := scriptedProviderForLog(t, canary, canary)
	offset := strings.Index(provider.RequestLog(), canary)
	if offset < 0 {
		t.Fatalf("the scripted provider log does not carry the canary")
	}
	return offset
}

// scriptedProviderForLog records one synthetic request so RequestLog renders
// body.
func scriptedProviderForLog(t *testing.T, canary, body string) *fakeProvider {
	t.Helper()
	provider := newFakeProvider(t, familyOpenAILegacy, canary, fakeScript{Model: "smoke-native-a"})
	provider.record(providerRequest{
		Method: http.MethodPost,
		Path:   openAIChatPath,
		Header: http.Header{},
		Body:   []byte(body),
	})
	return provider
}

// TestProviderBodyCutLeavesNoCredentialFragment proves the fake provider never
// persists a request body it could not read to the end. A credential that
// straddles the provider read cap would otherwise survive as a fragment in the
// retained prefix, and exact-match redaction cannot recognise a fragment. The
// request still fails visibly, safe request metadata stays in the artifact and a
// complete body is still recorded in full.
func TestProviderBodyCutLeavesNoCredentialFragment(t *testing.T) {
	t.Parallel()
	provider := newOpenAILegacyProvider(t, testCanary, fakeScript{Model: "smoke-native-a"})
	// Place the credential so the read cap splits it: half its length before the
	// cap, half after it.
	const prefix = `{"model":"smoke-native-a","stream":false,"messages":[{"role":"user","content":"`
	pad := providerBodyLimit - len(prefix) - len(testCanary)/2
	if pad < 0 {
		t.Fatalf("the synthetic request prefix is longer than the provider read cap")
	}
	oversized := prefix + strings.Repeat("x", pad) + testCanary + `"}]}`
	resp, respBody, err := scriptedPost(t.Context(), provider.OpenAILegacyBaseURL()+openAIChatCompletionsSuffix, oversized, "Authorization", "Bearer "+testCanary)
	if err != nil {
		t.Fatalf("post the oversized body: %v", err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status %d, want 400 for a body beyond the provider read cap; body %s", resp.StatusCode, respBody)
	}
	// A complete body is still recorded, so the omission below is specific to an
	// incomplete read rather than a blanket refusal to keep request bodies.
	if _, _, err := scriptedPost(t.Context(), provider.OpenAILegacyBaseURL()+openAIChatCompletionsSuffix, `{"model":"smoke-native-a","stream":false}`, "Authorization", "Bearer "+testCanary); err != nil {
		t.Fatalf("post the complete body: %v", err)
	}

	ev := localEvidence(t, testCanary)
	sink := newLogSink(t, ev.path("proxy.log"), testCanary)
	t.Cleanup(func() { _ = sink.Close() })
	report := failureReport(t, failureContext{sut: &sut{evidence: ev, logs: sink}, provider: provider}, "synthetic provider body cut")
	assertNoCanaryFragment(t, report, testCanary)
	artifact, err := os.ReadFile(ev.path("provider-requests.txt"))
	if err != nil {
		t.Fatalf("read the persisted provider artifact: %v", err)
	}
	assertNoCanaryFragment(t, string(artifact), testCanary)
	for _, want := range []string{"POST " + openAIChatPath, "body not retained:", "capture bound", "smoke-native-a"} {
		if !strings.Contains(string(artifact), want) {
			t.Fatalf("the persisted provider artifact lost %q:\n%s", want, string(artifact))
		}
	}
	if !strings.Contains(report, "body not retained:") {
		t.Fatalf("the console report hides the omitted body:\n%s", truncate(report, 600))
	}

	requests := provider.Requests()
	if len(requests) != 2 {
		t.Fatalf("provider recorded %d request(s), want 2", len(requests))
	}
	if len(requests[0].Body) != 0 {
		t.Fatalf("the provider retained %d bytes of a body it could not read to the end", len(requests[0].Body))
	}
	if len(requests[1].Body) == 0 {
		t.Fatalf("the provider did not retain the complete body")
	}
}

// TestLogSinkCapsTheEvidenceFile proves the on-disk proxy log stops at its
// bound with a truncation marker while the in-memory tail keeps every byte.
func TestLogSinkCapsTheEvidenceFile(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "proxy.log")
	sink := newLogSink(t, path, "")
	sink.fileMax = 128
	chunk := []byte(strings.Repeat("x", 100))
	for i := 0; i < 4; i++ {
		if _, err := sink.Write(chunk); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	if err := sink.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read evidence log: %v", err)
	}
	if got := len(raw); got > sink.fileMax+len(logFileTruncationMarker) {
		t.Fatalf("evidence file is not capped: %d bytes", got)
	}
	if !strings.Contains(string(raw), "truncated at the capture bound") {
		t.Fatalf("capped evidence file carries no truncation marker: %q", raw)
	}
	if tail := sink.Tail(1 << 20); len(tail) != 4*len(chunk) {
		t.Fatalf("in-memory tail lost bytes: %d, want %d", len(tail), 4*len(chunk))
	}
}

// TestClientWireRendersPartialResponse proves a read error keeps the status,
// headers and partial body the exchange already observed, and that a failure
// before any response stays distinguishable from a partial response.
func TestClientWireRendersPartialResponse(t *testing.T) {
	t.Parallel()
	plain := func(s string) string { return s }

	t.Run("partial_response", func(t *testing.T) {
		t.Parallel()
		wire := &clientWire{
			Method:      http.MethodPost,
			URL:         "http://127.0.0.1:1" + openAIChatPath,
			RequestBody: []byte(`{"model":"smoke/chat-a","stream":true}`),
			Status:      http.StatusOK,
			Header:      http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:        []byte("data: {\"choices\":[{\"delta\":{\"content\":\"Hello \"}}]}\n\ndata: {\"choices\""),
			Err:         fmt.Errorf("read response body: %w", io.ErrUnexpectedEOF),
		}
		got := wire.render(plain)
		for _, want := range []string{
			"error: read response body: unexpected EOF",
			"received before the error",
			"status: 200",
			"Content-Type: text/event-stream",
			`data: {"choices":[{"delta":{"content":"Hello "}}]}`,
		} {
			if !strings.Contains(got, want) {
				t.Fatalf("rendered wire is missing %q:\n%s", want, got)
			}
		}
	})

	t.Run("intentional_cancellation", func(t *testing.T) {
		t.Parallel()
		wire := &clientWire{
			Method:      http.MethodPost,
			URL:         "http://127.0.0.1:1" + openAIChatPath,
			RequestBody: []byte(`{"model":"smoke/chat-a","stream":true}`),
			Status:      http.StatusOK,
			Header:      http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:        []byte("data: {\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n\n"),
			Err:         fmt.Errorf("%w: the journey stopped after the first dispatched frame", errClientCancelled),
		}
		got := wire.render(plain)
		for _, want := range []string{
			"error: intentional client cancellation",
			"PARTIAL CAPTURE",
			"status: 200",
			"Content-Type: text/event-stream",
			`data: {"choices":[{"delta":{"role":"assistant"}}]}`,
		} {
			if !strings.Contains(got, want) {
				t.Fatalf("rendered wire is missing %q:\n%s", want, got)
			}
		}
		if strings.Contains(got, "no response was received") {
			t.Fatalf("a cancelled exchange claims no response was received:\n%s", got)
		}
		// The unread suffix was never inspected, so absence cannot be proven
		// from this capture.
		if err := wire.canaryAbsenceError(testCanary); err == nil {
			t.Fatal("a deliberately cancelled capture cleared the canary absence check")
		}
	})

	t.Run("pre_response_failure", func(t *testing.T) {
		t.Parallel()
		wire := &clientWire{
			Method:      http.MethodPost,
			URL:         "http://127.0.0.1:1" + openAIChatPath,
			RequestBody: []byte(`{"model":"smoke/chat-a"}`),
			Err:         errors.New("dial tcp 127.0.0.1:1: connect: connection refused"),
		}
		got := wire.render(plain)
		if !strings.Contains(got, "error: dial tcp") || !strings.Contains(got, "no response was received") {
			t.Fatalf("pre-response failure is not labelled:\n%s", got)
		}
		if strings.Contains(got, "status:") {
			t.Fatalf("pre-response failure claims a status:\n%s", got)
		}
	})
}
