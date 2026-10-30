package acp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

type ctxKey struct{}

type unarySpyTransport struct {
	mu          sync.Mutex
	lastCtx     context.Context
	errAtInvoke error
	// invokeCh receives one value after each successful CallUnary state update; optional.
	invokeCh chan struct{}
}

func (u *unarySpyTransport) CallUnary(ctx context.Context, body []byte, expectStatus int) ([]byte, error) {
	u.mu.Lock()
	u.errAtInvoke = ctx.Err()
	u.lastCtx = ctx
	ch := u.invokeCh
	u.mu.Unlock()
	if ch != nil {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if expectStatus == http.StatusNoContent || expectStatus == http.StatusOK {
		return nil, nil
	}
	return nil, nil
}

func (u *unarySpyTransport) CallPromptStream(ctx context.Context, body []byte) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}

func (u *unarySpyTransport) SendJSONRPC(ctx context.Context, body []byte) error {
	return nil
}

func (u *unarySpyTransport) Close() error { return nil }

func (u *unarySpyTransport) last() context.Context {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.lastCtx
}

func (u *unarySpyTransport) invokeErr() error {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.errAtInvoke
}

var errToolUpdateSink = errors.New("tool update sink fail")

type failToolUpdateSink struct{}

func (failToolUpdateSink) HandleToolUpdate(context.Context, string, map[string]any) ([]aipapi.Event, error) {
	return nil, errToolUpdateSink
}

func (failToolUpdateSink) FlushIncomplete() []aipapi.Event { return nil }

var errTestHandler = errors.New("inbound method handler fail")

type errServerRequestHandler struct{}

func (errServerRequestHandler) HandleServerRequest(context.Context, string, json.RawMessage, json.RawMessage) (any, error) {
	return nil, errTestHandler
}

type okServerRequestHandler struct{}

func (okServerRequestHandler) HandleServerRequest(context.Context, string, json.RawMessage, json.RawMessage) (any, error) {
	return map[string]any{}, nil
}

var errSendJSONRPC = errors.New("send json-rpc fail")

type sendErrTransport struct{}

func (sendErrTransport) CallUnary(context.Context, []byte, int) ([]byte, error) { return nil, nil }

func (sendErrTransport) CallPromptStream(context.Context, []byte) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}

func (sendErrTransport) SendJSONRPC(context.Context, []byte) error { return errSendJSONRPC }

func (sendErrTransport) Close() error { return nil }

// captureRPCTransport records the last JSON-RPC body sent via SendJSONRPC so tests
// can verify the error response shape without a live subprocess.
type captureRPCTransport struct {
	mu       sync.Mutex
	lastBody []byte
}

func (c *captureRPCTransport) CallUnary(context.Context, []byte, int) ([]byte, error) {
	return nil, nil
}

func (c *captureRPCTransport) CallPromptStream(context.Context, []byte) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}

func (c *captureRPCTransport) SendJSONRPC(_ context.Context, body []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastBody = body
	return nil
}

func (c *captureRPCTransport) Close() error { return nil }

func (c *captureRPCTransport) lastSentBody() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastBody
}

type readOnceThenErr struct {
	data []byte
	sent bool
	err  error
}

func (r *readOnceThenErr) Read(p []byte) (int, error) {
	if !r.sent {
		r.sent = true
		return copy(p, r.data), nil
	}
	return 0, r.err
}

func TestParseNDJSONLine_planEmitsReasoning(t *testing.T) {
	t.Parallel()
	line := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s","update":{"sessionUpdate":"plan","entries":[]}}}`
	evs, err := parseNDJSONLine(context.Background(), mergeMapperOptions(Config{}), line, 99)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 || evs[0].Kind != aipapi.EventReasoningDelta {
		t.Fatalf("got %#v", evs)
	}
}

func TestParseNDJSONLine_planDisabledYieldsWarning(t *testing.T) {
	t.Parallel()
	line := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s","update":{"sessionUpdate":"plan","entries":[]}}}`
	mapper := mergeMapperOptions(Config{SessionUpdate: SessionUpdateMapperOptions{DisablePlanReasoning: true}})
	evs, err := parseNDJSONLine(context.Background(), mapper, line, 99)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 || evs[0].Kind != aipapi.EventWarning {
		t.Fatalf("got %#v", evs)
	}
}

func TestParseNDJSONLine_chunkYieldsDelta(t *testing.T) {
	t.Parallel()
	line := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"hi"}}}}`
	evs, err := parseNDJSONLine(context.Background(), mergeMapperOptions(Config{}), line, 99)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 || evs[0].Kind != aipapi.EventTextDelta || evs[0].Delta != "hi" {
		t.Fatalf("got %#v", evs)
	}
}

func TestParseNDJSONLine_textDelta(t *testing.T) {
	t.Parallel()
	line := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s","update":{"sessionUpdate":"agent_message_chunk","content":{"textDelta":"x"}}}}`
	evs, err := parseNDJSONLine(context.Background(), mergeMapperOptions(Config{}), line, 99)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 || evs[0].Delta != "x" {
		t.Fatalf("got %#v", evs)
	}
}

func TestParseNDJSONLine_terminal(t *testing.T) {
	t.Parallel()
	line := `{"jsonrpc":"2.0","id":10,"result":{"stopReason":"end_turn"}}`
	evs, err := parseNDJSONLine(context.Background(), mergeMapperOptions(Config{}), line, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 || evs[0].Kind != aipapi.EventResponseFinished {
		t.Fatalf("got %#v", evs)
	}
}

func TestPromptStream_signalCancelContextNotCanceledWithConsumer(t *testing.T) {
	t.Parallel()
	spy := &unarySpyTransport{invokeCh: make(chan struct{}, 4)}
	cli := &client{t: spy}
	parent := context.WithValue(context.Background(), ctxKey{}, "trace")
	body := io.NopCloser(strings.NewReader(""))
	s := newPromptNDJSONStream(parent, body, cli, "sid", 1, "mid", mergeMapperOptions(Config{}), nil, mergeCancelProfile(Config{}), 0)

	cctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Recv(cctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("recv err: %v", err)
	}

	select {
	case <-spy.invokeCh:
	case <-time.After(3 * time.Second):
		t.Fatal("cancelSession was not invoked")
	}
	last := spy.last()
	if spy.invokeErr() != nil {
		t.Fatalf("cancel ctx should not inherit consumer cancellation at RPC start: %v", spy.invokeErr())
	}
	if _, ok := last.Deadline(); !ok {
		t.Fatal("expected bounded deadline on cancel RPC context")
	}
	if got, _ := last.Value(ctxKey{}).(string); got != "trace" {
		t.Fatalf("expected stream context value propagated, got %q", got)
	}
}

func TestPromptStream_CancelInvokesProviderCancel(t *testing.T) {
	t.Parallel()
	spy := &unarySpyTransport{invokeCh: make(chan struct{}, 4)}
	cli := &client{t: spy}
	body := io.NopCloser(strings.NewReader(""))
	s := newPromptNDJSONStream(context.Background(), body, cli, "sid", 1, "mid", mergeMapperOptions(Config{}), nil, mergeCancelProfile(Config{}), 0)

	res := s.Cancel(context.Background(), aipapi.CancelCause{Kind: aipapi.CancelExplicit})
	if res.Mode != aipapi.CancelModeProvider {
		t.Fatalf("cancel mode = %s", res.Mode)
	}
	select {
	case <-spy.invokeCh:
	case <-time.After(3 * time.Second):
		t.Fatal("cancelSession was not invoked")
	}
}

func TestPromptStream_decodeInboundLineMalformedJSON(t *testing.T) {
	t.Parallel()
	body := io.NopCloser(strings.NewReader("{not json"))
	cli := &client{t: &unarySpyTransport{}}
	s := newPromptNDJSONStream(context.Background(), body, cli, "sid", 1, "mid", mergeMapperOptions(Config{}), nil, mergeCancelProfile(Config{}), 0)
	_, err := s.Recv(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "acp: decode inbound line") {
		t.Fatalf("got %v", err)
	}
	if _, ok := errors.AsType[*json.SyntaxError](err); !ok {
		t.Fatalf("expected *json.SyntaxError in chain, got %v", err)
	}
}

func TestPromptStream_handleInboundServerRequestHandlerFail(t *testing.T) {
	t.Parallel()
	line := `{"jsonrpc":"2.0","id":5,"method":"vendor/extra","params":{}}` + "\n"
	cap := &captureRPCTransport{}
	cli := &client{t: cap}
	s := newPromptNDJSONStream(context.Background(), io.NopCloser(strings.NewReader(line)), cli, "sid", 1, "mid", mergeMapperOptions(Config{}), errServerRequestHandler{}, mergeCancelProfile(Config{}), 0)

	// The handler returns an error, but the stream should NOT terminate. Instead a
	// JSON-RPC -32601 error response is sent back to the agent and the stream continues.
	// Since no content events were emitted, the scanner reaches EOF and returns
	// io.ErrUnexpectedEOF (not a handler error).
	_, err := s.Recv(context.Background())
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("expected io.ErrUnexpectedEOF (stream continues after error response), got %v", err)
	}

	// Verify the -32601 error response was sent.
	sent := cap.lastSentBody()
	if sent == nil {
		t.Fatal("expected a JSON-RPC error response to be sent")
	}
	var resp struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Error   *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
		Result any `json:"result"`
	}
	if err := json.Unmarshal(sent, &resp); err != nil {
		t.Fatalf("unmarshal sent response: %v", err)
	}
	if resp.Error == nil {
		t.Fatalf("expected error field in response, got %s", sent)
	}
	if resp.Error.Code != -32601 {
		t.Fatalf("error code = %d, want -32601", resp.Error.Code)
	}
	if !strings.Contains(resp.Error.Message, errTestHandler.Error()) {
		t.Fatalf("error message = %q, want it to contain %q", resp.Error.Message, errTestHandler.Error())
	}
	if resp.Result != nil {
		t.Fatalf("expected no result field in error response, got %v", resp.Result)
	}
}

func TestPromptStream_handleInboundServerRequestHandlerFail_sendError(t *testing.T) {
	t.Parallel()
	line := `{"jsonrpc":"2.0","id":5,"method":"vendor/extra","params":{}}` + "\n"
	cli := &client{t: sendErrTransport{}}
	s := newPromptNDJSONStream(context.Background(), io.NopCloser(strings.NewReader(line)), cli, "sid", 1, "mid", mergeMapperOptions(Config{}), errServerRequestHandler{}, mergeCancelProfile(Config{}), 0)
	_, err := s.Recv(context.Background())
	if err == nil {
		t.Fatal("expected error when sending error response fails")
	}
	if !strings.Contains(err.Error(), "acp: send inbound server error response") {
		t.Fatalf("got %v", err)
	}
	if !errors.Is(err, errSendJSONRPC) {
		t.Fatalf("expected underlying send error, got %v", err)
	}
}

func TestPromptStream_sendInboundServerResponseFail(t *testing.T) {
	t.Parallel()
	line := `{"jsonrpc":"2.0","id":5,"method":"vendor/extra","params":{}}` + "\n"
	cli := &client{t: sendErrTransport{}}
	s := newPromptNDJSONStream(context.Background(), io.NopCloser(strings.NewReader(line)), cli, "sid", 1, "mid", mergeMapperOptions(Config{}), okServerRequestHandler{}, mergeCancelProfile(Config{}), 0)
	_, err := s.Recv(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "acp: send inbound server response") {
		t.Fatalf("got %v", err)
	}
	if !strings.Contains(err.Error(), "acp: handle inbound server request") {
		t.Fatalf("got %v", err)
	}
	if !errors.Is(err, errSendJSONRPC) {
		t.Fatalf("expected underlying send error, got %v", err)
	}
}

func TestPromptStream_parseNDJSONLineWrapped(t *testing.T) {
	t.Parallel()
	line := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s","update":{"sessionUpdate":"tool_call","toolCallId":"t"}}}` + "\n"
	mapper := mergeMapperOptions(Config{SessionUpdate: SessionUpdateMapperOptions{ToolSink: failToolUpdateSink{}}})
	cli := &client{t: &unarySpyTransport{}}
	s := newPromptNDJSONStream(context.Background(), io.NopCloser(strings.NewReader(line)), cli, "sid", 1, "mid", mapper, nil, mergeCancelProfile(Config{}), 0)
	_, err := s.Recv(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "acp: parse NDJSON line") {
		t.Fatalf("got %v", err)
	}
	if !errors.Is(err, errToolUpdateSink) {
		t.Fatalf("expected tool sink error, got %v", err)
	}
}

func TestPromptStream_scanStreamError(t *testing.T) {
	t.Parallel()
	errRead := errors.New("read stopped after first line")
	chunk := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"x"}}}}` + "\n"
	r := &readOnceThenErr{data: []byte(chunk), err: errRead}
	body := io.NopCloser(r)
	cli := &client{t: &unarySpyTransport{}}
	s := newPromptNDJSONStream(context.Background(), body, cli, "sid", 1, "mid", mergeMapperOptions(Config{}), nil, mergeCancelProfile(Config{}), 0)
	ctx := context.Background()
	for i := range 3 {
		if _, err := s.Recv(ctx); err != nil {
			t.Fatalf("recv %d: %v", i, err)
		}
	}
	_, err := s.Recv(ctx)
	if err == nil {
		t.Fatal("expected scan error")
	}
	if !strings.Contains(err.Error(), "acp: scan stream") {
		t.Fatalf("got %v", err)
	}
	if !errors.Is(err, errRead) {
		t.Fatalf("expected read error, got %v", err)
	}
}
