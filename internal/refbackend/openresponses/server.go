package openresponses

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// DefaultMaxBodyBytes bounds request bodies served by the emulator.
const DefaultMaxBodyBytes = 8 << 20

// Options tunes the independent server emulator.
type Options struct {
	// AllowMissingBearer, when true, skips the bearer requirement. When false,
	// requests without an Authorization: Bearer header are rejected with 401.
	AllowMissingBearer bool
	// RequiredBearer, when non-empty, requires the exact bearer secret.
	RequiredBearer string
	// MaxBodyBytes bounds request body reads; zero uses DefaultMaxBodyBytes.
	MaxBodyBytes int64
	// RedactBodies replaces captured request bodies with a redaction marker.
	RedactBodies bool
	// Clock supplies deterministic timestamps for observations.
	Clock Clock
	// CaptureMax bounds the stored observation list.
	CaptureMax int
	// Logger, when set, receives emulator warnings.
	Logger *slog.Logger
}

// Server is an independent OpenResponses remote backend emulator. A test selects
// one script at a time; every request is captured (atomic counters + redacted
// bounded capture) and validated against the active script's expectations.
type Server struct {
	mu       sync.Mutex
	scripts  map[string]*Script
	activeID string
	opts     Options
	clock    Clock
	cap      *Capture
	logger   *slog.Logger

	mismatches atomic.Int64
	cancelled  atomic.Int64
	writeErrs  atomic.Int64
}

// NewServer constructs an independent emulator server.
func NewServer(opts Options) *Server {
	if opts.MaxBodyBytes <= 0 {
		opts.MaxBodyBytes = DefaultMaxBodyBytes
	}
	clock := opts.Clock
	if clock == nil {
		clock = NewClock(time.Time{})
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	redactHeaders := []string{"authorization", "x-api-key", "cookie", "x-stripe-apikey"}
	return &Server{
		scripts: map[string]*Script{},
		opts:    opts,
		clock:   clock,
		cap:     NewCapture(opts.CaptureMax, redactHeaders, opts.RedactBodies),
		logger:  logger,
	}
}

// Register validates and registers one or more scripts. Duplicate IDs fail.
func (s *Server) Register(sd ...*Script) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sc := range sd {
		if err := sc.Validate(); err != nil {
			return fmt.Errorf("refbackend/openresponses: register %q: %w", sc.ID, err)
		}
		if _, dup := s.scripts[sc.ID]; dup {
			return fmt.Errorf("refbackend/openresponses: duplicate script %q", sc.ID)
		}
		s.scripts[sc.ID] = sc
	}
	return nil
}

// Select makes id the active script for subsequent requests.
func (s *Server) Select(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.scripts[id]; !ok {
		return ErrUnknownScript
	}
	s.activeID = id
	return nil
}

// ActiveScript returns the currently selected script.
func (s *Server) ActiveScript() (*Script, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.activeID == "" {
		return nil, ErrNoScriptSelected
	}
	sc, ok := s.scripts[s.activeID]
	if !ok {
		return nil, ErrUnknownScript
	}
	return sc, nil
}

// Capture exposes the atomic counters and redacted bounded capture.
func (s *Server) Capture() *Capture { return s.cap }

// MismatchCount returns how many requests violated the active script's expectations.
func (s *Server) MismatchCount() int64 { return s.mismatches.Load() }

// CancelCount returns how many times the server observed request cancellation.
func (s *Server) CancelCount() int64 { return s.cancelled.Load() }

// WriteErrorCount returns how many times the server observed a writer failure.
func (s *Server) WriteErrorCount() int64 { return s.writeErrs.Load() }

// Clock exposes the server's deterministic clock.
func (s *Server) Clock() Clock { return s.clock }

// Handler returns the HTTP surface: POST /responses (JSON/SSE), POST
// /responses/compact (JSON), GET /responses (WebSocket).
func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/responses"):
			s.serveWS(w, r)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/responses/compact"):
			s.serveCompact(w, r)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/responses"):
			s.serveCreate(w, r)
		default:
			http.NotFound(w, r)
		}
	})
}

func (s *Server) serveCreate(w http.ResponseWriter, r *http.Request) {
	script, body, ok := s.captureAndAuthorize(w, r)
	if !ok {
		return
	}
	req, perr := parseCreateRequest(body)
	if perr != nil {
		s.writeError(w, http.StatusBadRequest, ErrorObject{Type: "invalid_request", Code: "malformed_body", Message: perr.Error()})
		return
	}
	s.dispatch(w, r, script, func() []string {
		return checkExpected(script.Expected, r, req, body)
	})
}

func (s *Server) serveCompact(w http.ResponseWriter, r *http.Request) {
	script, body, ok := s.captureAndAuthorize(w, r)
	if !ok {
		return
	}
	req, perr := parseCompactRequest(body)
	if perr != nil {
		s.writeError(w, http.StatusBadRequest, ErrorObject{Type: "invalid_request", Code: "malformed_body", Message: perr.Error()})
		return
	}
	s.dispatch(w, r, script, func() []string {
		return checkCompactExpected(script.Expected, r, req, body)
	})
}

// captureAndAuthorize reads the body, captures it (redacted), and applies the
// server credential policy. It returns ok=false when a response was already written.
func (s *Server) captureAndAuthorize(w http.ResponseWriter, r *http.Request) (*Script, []byte, bool) {
	script, err := s.ActiveScript()
	if err != nil {
		s.writeError(w, http.StatusServiceUnavailable, ErrorObject{Type: "server_error", Code: "no_active_script", Message: "no script selected"})
		return nil, nil, false
	}
	body, rerr := readBounded(r.Body, s.opts.MaxBodyBytes)
	if rerr != nil {
		s.writeError(w, http.StatusRequestEntityTooLarge, ErrorObject{Type: "invalid_request", Code: "body_too_large", Message: "request body exceeds bound"})
		return nil, nil, false
	}
	s.cap.Record(r, body)
	if code, eo := s.authorize(r); eo != nil {
		s.writeError(w, code, *eo)
		return nil, nil, false
	}
	return script, body, true
}

// dispatch applies error/malformed/mode handling after expected checks pass.
func (s *Server) dispatch(w http.ResponseWriter, r *http.Request, script *Script, check func() []string) {
	if fails := check(); len(fails) > 0 {
		s.mismatches.Add(1)
		s.writeError(w, http.StatusBadRequest, ErrorObject{
			Type:    "invalid_request",
			Code:    "expectation_mismatch",
			Message: strings.Join(fails, "; "),
		})
		return
	}
	if script.Error != nil {
		s.writeScriptError(w, script.Error)
		return
	}
	// SSE malformed modes (event header/type/terminal/content-type) are applied
	// inside serveSSE; JSON/compact malformed resource/body modes run after.
	if script.Mode == ModeSSE {
		s.serveSSE(w, r, script)
		return
	}
	if script.Malformed != "" {
		s.serveMalformed(w, r, script)
		return
	}
	s.serveJSON(w, r, script)
}

func (s *Server) serveJSON(w http.ResponseWriter, r *http.Request, script *Script) {
	var body []byte
	var err error
	switch {
	case len(script.RawBody) > 0:
		body = script.RawBody
	case script.Resource != nil:
		body, err = json.Marshal(script.Resource)
	case script.CompactResource != nil:
		body, err = json.Marshal(script.CompactResource)
	}
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, ErrorObject{Type: "server_error", Code: "build_failed", Message: "failed to build resource"})
		return
	}
	if err := s.delay(r.Context(), script, 0); err != nil {
		s.cancelled.Add(1)
		return
	}
	ct := "application/json"
	if script.Malformed == MalformedContentType {
		ct = "text/event-stream"
	}
	w.Header().Set("Content-Type", ct)
	status := script.Status
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	if _, err := w.Write(body); err != nil {
		s.writeErrs.Add(1)
	}
}

func (s *Server) serveSSE(w http.ResponseWriter, r *http.Request, script *Script) {
	if len(script.RawBody) > 0 {
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		status := script.Status
		if status == 0 {
			status = http.StatusOK
		}
		w.WriteHeader(status)
		_, _ = w.Write(script.RawBody)
		return
	}
	events := s.eventsFor(script)
	if script.Malformed == MalformedContentType {
		w.Header().Set("Content-Type", "application/json")
	} else {
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	}
	status := script.Status
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	flusher, _ := w.(http.Flusher)

	sw := &sseWriter{w: w}
	malformed := script.Malformed

	if malformed == MalformedDoneBeforeTerminal {
		if err := sw.writeDone(); err != nil {
			s.writeErrs.Add(1)
		}
		if flusher != nil {
			flusher.Flush()
		}
		return
	}

	for i, ev := range events {
		if script.DisconnectAfter > 0 && i >= script.DisconnectAfter {
			hijackAndClose(w)
			return
		}
		if err := s.delay(r.Context(), script, i); err != nil {
			s.cancelled.Add(1)
			return
		}
		header := ev.Type
		if malformed == MalformedEventMismatch && ev.Type == "response.completed" {
			header = "response.failed"
		}
		var werr error
		switch malformed {
		case MalformedEventNoHeader:
			payload, perr := ev.renderPayload()
			if perr != nil {
				werr = perr
				break
			}
			_, werr = fmt.Fprintf(w, "data: %s\n\n", payload)
		case MalformedEventDuplicateTerminal:
			werr = sw.writeEvent(ev, header)
			if werr == nil && ev.Type == "response.completed" {
				werr = sw.writeEvent(ev, header)
			}
		default:
			werr = sw.writeEvent(ev, header)
		}
		if werr != nil {
			s.writeErrs.Add(1)
			s.cancelled.Add(1)
			return
		}
		if flusher != nil {
			flusher.Flush()
		}
		if malformed == MalformedEventAfterTerminal && ev.Type == "response.completed" {
			late := StreamEvent{
				Type: "response.output_item.added",
				Seq:  ev.Seq + 1,
				Fields: map[string]any{
					"output_index": 0,
					"item":         map[string]any{"type": "message", "id": "late", "status": "in_progress", "role": "assistant", "content": []any{}},
				},
			}
			_ = sw.writeEvent(late, late.Type)
			return
		}
	}

	if malformed == MalformedMissingDONE {
		if flusher != nil {
			flusher.Flush()
		}
		return
	}
	if err := sw.writeDone(); err != nil {
		s.writeErrs.Add(1)
		return
	}
	if flusher != nil {
		flusher.Flush()
	}
}

// eventsFor derives the stream events from the script's explicit steps or by
// building them from the configured resource.
func (s *Server) eventsFor(script *Script) []StreamEvent {
	if len(script.SSE) > 0 {
		out := make([]StreamEvent, len(script.SSE))
		for i, ws := range script.SSE {
			var fields map[string]any
			if len(ws.Data) > 0 {
				_ = json.Unmarshal(ws.Data, &fields)
			}
			out[i] = StreamEvent{Type: ws.Type, Seq: ws.Sequence, Fields: fields}
		}
		return out
	}
	return buildStreamEvents(script.Resource)
}

// serveMalformed writes the requested malformed body/resource shapes.
func (s *Server) serveMalformed(w http.ResponseWriter, r *http.Request, script *Script) {
	switch script.Malformed {
	case MalformedBodyNotJSON:
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "this is definitely not json")
		return
	case MalformedOversizedBody:
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, strings.Repeat("x", 6<<20))
		return
	case MalformedResourceMissingField:
		if script.Resource != nil {
			body, err := script.Resource.OmitRequiredField("output")
			if err == nil {
				s.writeRawBody(w, script, body)
				return
			}
		}
	case MalformedResourceBadType:
		if script.Resource != nil {
			body, err := script.Resource.CorruptField("output")
			if err == nil {
				s.writeRawBody(w, script, body)
				return
			}
		}
	case MalformedItemDiscriminator:
		if script.Resource != nil {
			cp := *script.Resource
			cp.Output = append(append([]Item{}, script.Resource.Output...), Item{Type: "mystery_unprefixed_item"})
			if body, err := json.Marshal(&cp); err == nil {
				s.writeRawBody(w, script, body)
				return
			}
		}
	case MalformedContentType:
		if script.Mode == ModeCompact && script.CompactResource != nil {
			if body, err := json.Marshal(script.CompactResource); err == nil {
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(body)
				return
			}
		}
	}
	// Fallback: serve the configured resource unchanged.
	s.serveJSON(w, r, script)
}

func (s *Server) writeRawBody(w http.ResponseWriter, script *Script, body []byte) {
	w.Header().Set("Content-Type", "application/json")
	status := script.Status
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	if _, err := w.Write(body); err != nil {
		s.writeErrs.Add(1)
	}
}

// authorize enforces the server credential policy.
func (s *Server) authorize(r *http.Request) (int, *ErrorObject) {
	auth := r.Header.Get("Authorization")
	hasBearer := strings.HasPrefix(auth, "Bearer ") && strings.TrimSpace(strings.TrimPrefix(auth, "Bearer ")) != ""
	if !s.opts.AllowMissingBearer && !hasBearer {
		return http.StatusUnauthorized, &ErrorObject{Type: "invalid_request", Code: "invalid_api_key", Message: "incorrect api key", Param: "api_key"}
	}
	if s.opts.RequiredBearer != "" {
		secret := strings.TrimSpace(strings.TrimPrefix(auth, "Bearer "))
		if secret != s.opts.RequiredBearer {
			return http.StatusUnauthorized, &ErrorObject{Type: "invalid_request", Code: "invalid_api_key", Message: "incorrect api key", Param: "api_key"}
		}
	}
	return 0, nil
}

// delay applies the script's delay plan for the given event index, observing
// request cancellation.
func (s *Server) delay(ctx context.Context, script *Script, index int) error {
	d := script.Delay.BeforeFirst
	if index > 0 {
		d = script.Delay.BetweenEvents
	}
	d += script.Delay.SlowWrite
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// writeError writes a structured error envelope with application/json.
func (s *Server) writeError(w http.ResponseWriter, status int, eo ErrorObject) {
	raw, err := json.Marshal(map[string]any{"error": eo})
	if err != nil {
		raw = []byte(`{"error":{"type":"invalid_request","code":"internal","message":"error"}}`)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(raw)
}

// writeScriptError writes a scripted error step, including Retry-After for 429.
func (s *Server) writeScriptError(w http.ResponseWriter, e *ErrorStep) {
	if e.Status == http.StatusTooManyRequests && e.RetryAfter != "" {
		w.Header().Set("Retry-After", e.RetryAfter)
	}
	s.writeError(w, errorStatus(e), *errorObjectFromStep(e))
}

func errorStatus(e *ErrorStep) int {
	if e.Status == 0 {
		return http.StatusBadRequest
	}
	return e.Status
}

func errorObjectFromStep(e *ErrorStep) *ErrorObject {
	typ := e.Type
	if typ == "" {
		typ = "invalid_request"
	}
	return &ErrorObject{Type: typ, Code: e.Code, Message: e.Message, Param: e.Param}
}

// checkExpected verifies a create/SSE request against the script expectations.
func checkExpected(exp ExpectedRequest, r *http.Request, req *CreateRequest, raw []byte) []string {
	var fails []string
	fails = append(fails, commonChecks(exp, r, raw)...)
	if exp.Model != "" && req.Model != exp.Model {
		fails = append(fails, fmt.Sprintf("model: got %q want %q", req.Model, exp.Model))
	}
	if exp.Stream != nil && req.Stream != *exp.Stream {
		fails = append(fails, fmt.Sprintf("stream: got %v want %v", req.Stream, *exp.Stream))
	}
	items := req.Items()
	if exp.MinInputItems > 0 && len(items) < exp.MinInputItems {
		fails = append(fails, fmt.Sprintf("input item count %d below min %d", len(items), exp.MinInputItems))
	}
	if exp.MaxInputItems > 0 && len(items) > exp.MaxInputItems {
		fails = append(fails, fmt.Sprintf("input item count %d above maxBytes %d", len(items), exp.MaxInputItems))
	}
	if exp.RequireTools > 0 && len(req.Tools) < exp.RequireTools {
		fails = append(fails, fmt.Sprintf("tools %d below required %d", len(req.Tools), exp.RequireTools))
	}
	fails = append(fails, extensionChecks(exp, items)...)
	return fails
}

// checkCompactExpected verifies a compact request against the script expectations.
func checkCompactExpected(exp ExpectedRequest, r *http.Request, req *CompactRequest, raw []byte) []string {
	var fails []string
	fails = append(fails, commonChecks(exp, r, raw)...)
	if exp.Model != "" && req.Model != exp.Model {
		fails = append(fails, fmt.Sprintf("model: got %q want %q", req.Model, exp.Model))
	}
	items := req.Items()
	if exp.MinInputItems > 0 && len(items) < exp.MinInputItems {
		fails = append(fails, fmt.Sprintf("input item count %d below min %d", len(items), exp.MinInputItems))
	}
	if exp.MaxInputItems > 0 && len(items) > exp.MaxInputItems {
		fails = append(fails, fmt.Sprintf("input item count %d above maxBytes %d", len(items), exp.MaxInputItems))
	}
	fails = append(fails, extensionChecks(exp, items)...)
	return fails
}

func commonChecks(exp ExpectedRequest, r *http.Request, raw []byte) []string {
	var fails []string
	if exp.Method != "" && r.Method != exp.Method {
		fails = append(fails, fmt.Sprintf("method: got %s want %s", r.Method, exp.Method))
	}
	if exp.PathSuffix != "" && !strings.HasSuffix(r.URL.Path, exp.PathSuffix) {
		fails = append(fails, fmt.Sprintf("path %s does not end with %s", r.URL.Path, exp.PathSuffix))
	}
	if exp.ContentType != "" && r.Header.Get("Content-Type") != exp.ContentType {
		fails = append(fails, fmt.Sprintf("content-type: got %q want %q", r.Header.Get("Content-Type"), exp.ContentType))
	}
	switch exp.Auth {
	case AuthBearer:
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			fails = append(fails, "authorization: bearer required")
		}
	case AuthNone:
		if r.Header.Get("Authorization") != "" {
			fails = append(fails, "authorization: must be absent")
		}
	}
	for _, sub := range exp.Contains {
		if !strings.Contains(string(raw), sub) {
			fails = append(fails, fmt.Sprintf("body missing substring %q", sub))
		}
	}
	for _, sub := range exp.MustOmit {
		if strings.Contains(string(raw), sub) {
			fails = append(fails, fmt.Sprintf("body must omit substring %q", sub))
		}
	}
	return fails
}

func extensionChecks(exp ExpectedRequest, items []Item) []string {
	var fails []string
	for _, et := range exp.RequireExtensionItems {
		found := false
		for _, it := range items {
			if it.Type == et {
				found = true
				break
			}
		}
		if !found {
			fails = append(fails, fmt.Sprintf("input missing extension item %q", et))
		}
	}
	return fails
}

func readBounded(r io.Reader, maxBytes int64) ([]byte, error) {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBodyBytes
	}
	body, err := io.ReadAll(io.LimitReader(r, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > maxBytes {
		return nil, fmt.Errorf("refbackend/openresponses: request body exceeds %d bytes", maxBytes)
	}
	return body, nil
}

func hijackAndClose(w http.ResponseWriter) {
	hj, ok := w.(http.Hijacker)
	if !ok {
		return
	}
	conn, _, err := hj.Hijack()
	if err != nil {
		return
	}
	_ = conn.Close()
}
