package frontendpipe_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/plugins/frontends/decodeqos"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/execerr"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/frontendpipe"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

type hookWire struct {
	last string
}

func (w *hookWire) write(rw http.ResponseWriter, status int, msg string) error {
	w.last = msg
	rw.Header().Set("Content-Type", "application/json")
	rw.WriteHeader(status)
	return json.NewEncoder(rw).Encode(map[string]string{"error": msg})
}

func (w *hookWire) WriteBodyTooLarge(rw http.ResponseWriter) error {
	return w.write(rw, http.StatusRequestEntityTooLarge, "too large")
}

func (w *hookWire) WriteReadBodyFailed(rw http.ResponseWriter) error {
	return w.write(rw, http.StatusBadRequest, "read failed")
}

func (w *hookWire) WriteExecutorNotConfigured(rw http.ResponseWriter) error {
	return w.write(rw, http.StatusInternalServerError, "no executor")
}

func (w *hookWire) WritePreflightCanceled(rw http.ResponseWriter) error {
	return w.write(rw, http.StatusServiceUnavailable, "canceled")
}

func (w *hookWire) WriteInvalidJSON(rw http.ResponseWriter) error {
	return w.write(rw, http.StatusBadRequest, "invalid json")
}

func (w *hookWire) WriteAdmissionReject(rw http.ResponseWriter, d decodeqos.Decision) error {
	return w.write(rw, d.Status, d.Message)
}

func (w *hookWire) WriteInvalidRequest(rw http.ResponseWriter) error {
	return w.write(rw, http.StatusBadRequest, "invalid request")
}

func (w *hookWire) WriteExecuteError(rw http.ResponseWriter, out execerr.Outcome) error {
	return w.write(rw, out.Status, out.Message)
}

func (w *hookWire) WriteEncodeFailed(rw http.ResponseWriter) error {
	return w.write(rw, http.StatusInternalServerError, "encode failed")
}

func (w *hookWire) WriteHookError(rw http.ResponseWriter, err error) error {
	if se, ok := errors.AsType[*frontendpipe.StatusError](err); ok {
		return w.write(rw, se.HTTPStatus(), se.Message)
	}
	return w.write(rw, http.StatusBadRequest, err.Error())
}

type hookExec struct {
	err    error
	called bool
	stream aipapi.EventStream
}

func (e *hookExec) Execute(context.Context, *aipapi.Call) (aipapi.EventStream, error) {
	e.called = true
	if e.err != nil {
		return nil, e.err
	}
	if e.stream != nil {
		return e.stream, nil
	}
	return aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventResponseFinished},
	}), nil
}
func (e *hookExec) CancelALeg(context.Context, aipapi.ALegCancelRequest) error { return nil }
func (e *hookExec) WallClock() func() time.Time                                { return nil }

type wrapStream struct {
	aipapi.EventStream
	wrapped bool
}

func validCall() *aipapi.Call {
	return &aipapi.Call{
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
}

func testSpec(exec *hookExec, wire *hookWire) frontendpipe.Spec[struct{}] {
	return frontendpipe.Spec[struct{}]{
		Config: frontendpipe.Config{
			Exec:       exec,
			FrontendID: "hooktest",
		},
		Wire: wire,
		MatchPath: func(string) (frontendpipe.PathMatch, bool) {
			return frontendpipe.PathMatch{}, true
		},
		Decode: func(frontendpipe.DecodeContext) (*frontendpipe.Decoded, error) {
			return &frontendpipe.Decoded{Call: validCall(), Stream: false}, nil
		},
		BuildEncodeOpts: func(*frontendpipe.Decoded) struct{} { return struct{}{} },
		WriteStream: func(context.Context, http.ResponseWriter, *aipapi.Call, aipapi.EventStream, struct{}) error {
			return nil
		},
		WriteNonStream: func(_ context.Context, w http.ResponseWriter, _ *aipapi.Call, es aipapi.EventStream, _ struct{}) error {
			if es != nil {
				_ = es.Close()
			}
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{"ok":true}`)
			return nil
		},
	}
}

func TestServeHTTP_AfterDecodeMutatesExtraAndWrapStreamSeesIt(t *testing.T) {
	t.Parallel()
	exec := &hookExec{}
	wire := &hookWire{}
	spec := testSpec(exec, wire)
	var afterDecoded, wrapSaw string
	spec.AfterDecode = func(_ context.Context, decoded *frontendpipe.Decoded) error {
		decoded.Extra = "reserved"
		afterDecoded = "reserved"
		return nil
	}
	spec.WrapStream = func(_ context.Context, decoded *frontendpipe.Decoded, inner aipapi.EventStream) (aipapi.EventStream, error) {
		wrapSaw, _ = decoded.Extra.(string)
		return &wrapStream{EventStream: inner, wrapped: true}, nil
	}
	var wroteWrapped bool
	spec.WriteNonStream = func(_ context.Context, w http.ResponseWriter, _ *aipapi.Call, es aipapi.EventStream, _ struct{}) error {
		ws, ok := es.(*wrapStream)
		wroteWrapped = ok && ws.wrapped
		if es != nil {
			_ = es.Close()
		}
		w.WriteHeader(http.StatusOK)
		return nil
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/x", strings.NewReader(`{"n":1}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	frontendpipe.ServeHTTP(&spec, rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if afterDecoded != "reserved" || wrapSaw != "reserved" {
		t.Fatalf("extra not forwarded: after=%q wrap=%q", afterDecoded, wrapSaw)
	}
	if !wroteWrapped {
		t.Fatal("WriteNonStream did not receive wrapped stream")
	}
	if !exec.called {
		t.Fatal("executor was not called")
	}
}

func TestServeHTTP_AfterDecodeStatusErrorUsesHookWriter(t *testing.T) {
	t.Parallel()
	exec := &hookExec{}
	wire := &hookWire{}
	spec := testSpec(exec, wire)
	spec.AfterDecode = func(context.Context, *frontendpipe.Decoded) error {
		return &frontendpipe.StatusError{
			Status:  http.StatusBadRequest,
			Type:    "invalid_request_error",
			Code:    "previous_response_not_found",
			Message: "Previous response was not found",
		}
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/x", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	frontendpipe.ServeHTTP(&spec, rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if wire.last != "Previous response was not found" {
		t.Fatalf("wire last=%q", wire.last)
	}
	if exec.called {
		t.Fatal("executor ran after AfterDecode error")
	}
}

func TestServeHTTP_OnExecuteErrorRunsOnFailure(t *testing.T) {
	t.Parallel()
	exec := &hookExec{err: errors.New("boom")}
	wire := &hookWire{}
	spec := testSpec(exec, wire)
	var cleaned bool
	spec.OnExecuteError = func(context.Context, *frontendpipe.Decoded, error) {
		cleaned = true
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/x", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	frontendpipe.ServeHTTP(&spec, rec, req)

	if !cleaned {
		t.Fatal("OnExecuteError was not invoked")
	}
	if rec.Code == http.StatusOK {
		t.Fatal("expected execute error status")
	}
}

func TestServeHTTP_AfterDecodeRevalidatesReplacedCall(t *testing.T) {
	t.Parallel()
	exec := &hookExec{}
	wire := &hookWire{}
	spec := testSpec(exec, wire)
	spec.AfterDecode = func(_ context.Context, decoded *frontendpipe.Decoded) error {
		decoded.Call = &aipapi.Call{}
		return nil
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/x", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	frontendpipe.ServeHTTP(&spec, rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if exec.called {
		t.Fatal("executor ran after AfterDecode replaced Call with an invalid value")
	}
}

func TestServeHTTP_AfterDecodeCanceledUsesPreflightWriter(t *testing.T) {
	t.Parallel()
	exec := &hookExec{}
	wire := &hookWire{}
	spec := testSpec(exec, wire)
	spec.AfterDecode = func(context.Context, *frontendpipe.Decoded) error {
		return context.Canceled
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/x", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	frontendpipe.ServeHTTP(&spec, rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if wire.last != "canceled" {
		t.Fatalf("wire last=%q", wire.last)
	}
	if exec.called {
		t.Fatal("executor ran after canceled AfterDecode")
	}
}

func TestServeHTTP_StatusErrorZeroStatusUsesBadRequest(t *testing.T) {
	t.Parallel()
	exec := &hookExec{}
	wire := &hookWire{}
	spec := testSpec(exec, wire)
	spec.AfterDecode = func(context.Context, *frontendpipe.Decoded) error {
		return &frontendpipe.StatusError{Message: "missing status"}
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/x", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	frontendpipe.ServeHTTP(&spec, rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if exec.called {
		t.Fatal("executor ran after zero-status AfterDecode error")
	}
}

type closeCountStream struct {
	aipapi.EventStream
	closes int
}

func (s *closeCountStream) Close() error {
	s.closes++
	if s.EventStream != nil {
		return s.EventStream.Close()
	}
	return nil
}

func TestServeHTTP_WrapStreamErrorClosesInnerStream(t *testing.T) {
	t.Parallel()
	inner := &closeCountStream{
		EventStream: aipapi.NewFixedEventStream([]aipapi.Event{
			{Kind: aipapi.EventResponseStarted},
			{Kind: aipapi.EventResponseFinished},
		}),
	}
	exec := &hookExec{stream: inner}
	wire := &hookWire{}
	spec := testSpec(exec, wire)
	spec.WrapStream = func(context.Context, *frontendpipe.Decoded, aipapi.EventStream) (aipapi.EventStream, error) {
		return nil, errors.New("wrap failed")
	}
	wrote := false
	spec.WriteNonStream = func(context.Context, http.ResponseWriter, *aipapi.Call, aipapi.EventStream, struct{}) error {
		wrote = true
		return nil
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/x", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	frontendpipe.ServeHTTP(&spec, rec, req)

	if rec.Code == http.StatusOK {
		t.Fatal("expected wrap error status")
	}
	if inner.closes != 1 {
		t.Fatalf("inner Close calls=%d want 1", inner.closes)
	}
	if wrote {
		t.Fatal("WriteNonStream ran after WrapStream error")
	}
}

func TestServeHTTP_ClassifyExecuteOverride(t *testing.T) {
	t.Parallel()
	exec := &hookExec{err: errors.New("boom")}
	wire := &hookWire{}
	spec := testSpec(exec, wire)
	spec.ClassifyExecute = func(error) execerr.Outcome {
		return execerr.Outcome{Status: http.StatusConflict, Message: "classified"}
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/x", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	frontendpipe.ServeHTTP(&spec, rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if wire.last != "classified" {
		t.Fatalf("wire last=%q", wire.last)
	}
}

// rejectFirstRecvStream yields one wrapped canonical RejectError from the
// first Recv, then EOF. It models a backend that opens a stream successfully
// (Execute returns no error) but reports a capability rejection — unknown
// model or unsupported effort — before any output event, surfacing through
// Collect on the non-streaming path.
type rejectFirstRecvStream struct {
	err    error
	recvs  int
	closes int
}

func (s *rejectFirstRecvStream) Recv(context.Context) (aipapi.Event, error) {
	s.recvs++
	if s.recvs == 1 {
		return aipapi.Event{}, s.err
	}
	return aipapi.Event{}, io.EOF
}

func (s *rejectFirstRecvStream) Close() error {
	s.closes++
	return nil
}

// countingStreamExec returns a stream successfully and counts invocations, so
// a test can prove the pipeline never retries or fails over after a Collect
// failure (no post-output recovery).
type countingStreamExec struct {
	calls  int
	stream aipapi.EventStream
}

func (e *countingStreamExec) Execute(context.Context, *aipapi.Call) (aipapi.EventStream, error) {
	e.calls++
	return e.stream, nil
}

func (e *countingStreamExec) CancelALeg(context.Context, aipapi.ALegCancelRequest) error {
	return nil
}
func (e *countingStreamExec) WallClock() func() time.Time { return nil }

// collectPassthrough mirrors the stock frontend WriteNonStreamJSON shape:
// Collect the canonical stream first and return its error unwrapped, writing
// headers only after a successful Collect.
func collectPassthrough(ctx context.Context, w http.ResponseWriter, _ *aipapi.Call, es aipapi.EventStream, _ struct{}) error {
	if _, err := aipapi.Collect(ctx, es); err != nil {
		return err
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, `{"ok":true}`)
	return nil
}

func nonStreamRejectSpec(exec *countingStreamExec, wire frontendpipe.WireErrors) frontendpipe.Spec[struct{}] {
	return frontendpipe.Spec[struct{}]{
		Config: frontendpipe.Config{
			Exec:       exec,
			FrontendID: "nonstream-reject",
		},
		Wire: wire,
		MatchPath: func(string) (frontendpipe.PathMatch, bool) {
			return frontendpipe.PathMatch{}, true
		},
		Decode: func(frontendpipe.DecodeContext) (*frontendpipe.Decoded, error) {
			return &frontendpipe.Decoded{Call: validCall(), Stream: false}, nil
		},
		BuildEncodeOpts: func(*frontendpipe.Decoded) struct{} { return struct{}{} },
		WriteStream: func(context.Context, http.ResponseWriter, *aipapi.Call, aipapi.EventStream, struct{}) error {
			return nil
		},
		WriteNonStream: collectPassthrough,
	}
}

// A capability rejection surfacing through Collect on the non-streaming path
// (Execute opened the stream successfully) must keep its 400 client-reject
// wire shape instead of collapsing to a 500 encode failure.
func TestServeHTTP_NonStreamCollectRejectMapsToClientError(t *testing.T) {
	t.Parallel()
	rawReason := strings.Repeat("x", 5000) + "\n\tsecret-token-abc123\x00model=unknown-xyz"
	collectErr := fmt.Errorf("connector build command: %w", &aipapi.RejectError{Reason: rawReason})
	es := &rejectFirstRecvStream{err: collectErr}
	exec := &countingStreamExec{stream: es}
	var onExecuteErrorCalled bool
	spec := nonStreamRejectSpec(exec, frontendpipe.OpenAIWire{})
	spec.OnExecuteError = func(context.Context, *frontendpipe.Decoded, error) {
		onExecuteErrorCalled = true
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/x", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	frontendpipe.ServeHTTP(&spec, rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s want 400", rec.Code, rec.Body.String())
	}
	var body struct {
		Error struct {
			Message string  `json:"message"`
			Type    string  `json:"type"`
			Code    *string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error body: %v (%q)", err, rec.Body.String())
	}
	if body.Error.Type != "invalid_request_error" {
		t.Fatalf("error.type=%q want invalid_request_error", body.Error.Type)
	}
	if body.Error.Code == nil || *body.Error.Code != "unsupported_parameter" {
		t.Fatalf("error.code=%v want unsupported_parameter", body.Error.Code)
	}
	if strings.Contains(body.Error.Message, "secret-token-abc123") {
		t.Fatalf("wire message leaks raw reason detail: %q", body.Error.Message)
	}
	if strings.ContainsAny(body.Error.Message, "\n\r\t\x00") {
		t.Fatalf("wire message carries control characters: %q", body.Error.Message)
	}
	if len(body.Error.Message) > aipapi.MaxClientMessageBytes {
		t.Fatalf("wire message %d bytes exceeds bound %d", len(body.Error.Message), aipapi.MaxClientMessageBytes)
	}
	if strings.Contains(body.Error.Message, strings.Repeat("x", 1024)) {
		t.Fatalf("wire message is not bounded: %d bytes", len(body.Error.Message))
	}
	if exec.calls != 1 {
		t.Fatalf("executor calls=%d want exactly 1 (no retry/failover after Collect failure)", exec.calls)
	}
	if es.recvs != 1 {
		t.Fatalf("stream recvs=%d want 1 (rejection surfaced on first Recv)", es.recvs)
	}
	if es.closes != 1 {
		t.Fatalf("stream closes=%d want 1 (Collect owns closure)", es.closes)
	}
	if onExecuteErrorCalled {
		t.Fatal("OnExecuteError ran for a Collect failure (no new post-output side effects)")
	}
}

// A genuine encode/infrastructure failure after a successful Collect must
// retain the static 500 error, never a client-reject mapping.
func TestServeHTTP_NonStreamEncodeFailureStaysInternalError(t *testing.T) {
	t.Parallel()
	exec := &countingStreamExec{stream: aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventResponseFinished},
	})}
	spec := nonStreamRejectSpec(exec, frontendpipe.OpenAIWire{})
	spec.WriteNonStream = func(ctx context.Context, w http.ResponseWriter, call *aipapi.Call, es aipapi.EventStream, _ struct{}) error {
		if _, err := aipapi.Collect(ctx, es); err != nil {
			return err
		}
		return errors.New("serializer fault: json encode failed")
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/x", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	frontendpipe.ServeHTTP(&spec, rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%s want 500", rec.Code, rec.Body.String())
	}
	var body struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error body: %v (%q)", err, rec.Body.String())
	}
	if body.Error.Message != execerr.InternalWireMessage {
		t.Fatalf("message=%q want static %q", body.Error.Message, execerr.InternalWireMessage)
	}
	if body.Error.Type != "api_error" {
		t.Fatalf("error.type=%q want api_error", body.Error.Type)
	}
	if exec.calls != 1 {
		t.Fatalf("executor calls=%d want exactly 1", exec.calls)
	}
}

// A frontend-owned ClassifyExecute override still owns the Collect-error
// mapping on the non-streaming path.
func TestServeHTTP_NonStreamCollectErrorHonorsClassifyExecute(t *testing.T) {
	t.Parallel()
	collectErr := fmt.Errorf("connector: %w", &aipapi.RejectError{Reason: "effort not supported"})
	es := &rejectFirstRecvStream{err: collectErr}
	exec := &countingStreamExec{stream: es}
	wire := &hookWire{}
	spec := nonStreamRejectSpec(exec, wire)
	spec.ClassifyExecute = func(error) execerr.Outcome {
		return execerr.Outcome{Kind: execerr.KindClientReject, Status: http.StatusConflict, Message: "classified-collect"}
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/x", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	frontendpipe.ServeHTTP(&spec, rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s want custom 409", rec.Code, rec.Body.String())
	}
	if wire.last != "classified-collect" {
		t.Fatalf("wire last=%q want custom outcome", wire.last)
	}
}

// A successful Collect through the shared non-streaming path writes the exact
// success response and nothing else: 200, the success body, and exactly one
// execute. A failure/classification branch that fired after a successful
// Collect could still leave the first 200 in place (WriteHeader is
// first-write-wins) while appending an error payload, so the body is asserted
// byte-for-byte rather than by status alone.
func TestServeHTTP_NonStreamCollectSuccessWritesOnlySuccessResponse(t *testing.T) {
	t.Parallel()
	exec := &countingStreamExec{stream: aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventResponseFinished},
	})}
	spec := nonStreamRejectSpec(exec, frontendpipe.OpenAIWire{})

	req := httptest.NewRequest(http.MethodPost, "/v1/x", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	frontendpipe.ServeHTTP(&spec, rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s want 200", rec.Code, rec.Body.String())
	}
	if got, want := rec.Body.String(), `{"ok":true}`; got != want {
		t.Fatalf("body=%q want exact success body %q (no error payload appended)", got, want)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type=%q want application/json", got)
	}
	if exec.calls != 1 {
		t.Fatalf("executor calls=%d want exactly 1", exec.calls)
	}
}
