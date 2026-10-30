package openairesponses_test

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/leglifecycle"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openairesponses"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/sessionwire"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestHandler_cancelResponseEndToEndCancelsRuntimeBLeg(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	inner := newHTTPBlockingBLeg()
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(1)
	ex.ALegLifecycle = leglifecycle.NewCoordinator(leglifecycle.CoordinatorConfig{CancelTimeout: time.Second})
	ex.Backends = map[string]execbackend.Backend{
		"managed": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return inner, nil
			},
		},
	}
	testkit.WireConformanceExecutorSecureSession(t, ex)
	exec := &capturingRuntimeExec{Executor: ex}
	srv := httptest.NewServer(&openairesponses.Handler{
		Exec:                 exec,
		DefaultRouteSelector: "managed:gpt-4o-mini",
	})
	t.Cleanup(srv.Close)

	req, err := http.NewRequest(http.MethodPost, srv.URL+"/v1/responses", strings.NewReader(`{"model":"gpt-4o-mini","input":"hi","stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("stream status=%d body=%s executeErr=%v", resp.StatusCode, b, exec.err())
	}
	aLegID := strings.TrimSpace(resp.Header.Get(sessionwire.HeaderALegID))
	if aLegID == "" {
		t.Fatal("stream response did not expose A-leg carrier")
	}
	reader := bufio.NewReader(resp.Body)
	line, err := reader.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(line, "response.created") {
		t.Fatalf("first SSE line = %q", line)
	}
	responseID, err := readResponseCreatedID(reader)
	if err != nil {
		t.Fatal(err)
	}
	if responseID == "" {
		t.Fatal("response.created event did not expose response id")
	}

	cancelReq, err := http.NewRequest(http.MethodPost, srv.URL+"/v1/responses/"+responseID+"/cancel", nil)
	if err != nil {
		t.Fatal(err)
	}
	cancelResp, err := http.DefaultClient.Do(cancelReq)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cancelResp.Body.Close() }()
	if cancelResp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(cancelResp.Body)
		t.Fatalf("cancel status=%d body=%s", cancelResp.StatusCode, b)
	}
	if got, want := inner.calls(), []string{"cancel:explicit", "close"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("backend B-leg calls=%v want %v", got, want)
	}
}

func readResponseCreatedID(reader *bufio.Reader) (string, error) {
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return "", err
		}
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		var payload struct {
			Response struct {
				ID string `json:"id"`
			} `json:"response"`
		}
		if err := json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &payload); err != nil {
			return "", err
		}
		return payload.Response.ID, nil
	}
}

type capturingRuntimeExec struct {
	*runtime.Executor
	mu      sync.Mutex
	lastErr error
}

func (e *capturingRuntimeExec) Execute(ctx context.Context, call *aipapi.Call) (aipapi.EventStream, error) {
	es, err := e.Executor.Execute(ctx, call)
	e.mu.Lock()
	e.lastErr = err
	e.mu.Unlock()
	return es, err
}

func (e *capturingRuntimeExec) err() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.lastErr
}

type httpBlockingBLeg struct {
	closed chan struct{}
	once   sync.Once
	mu     sync.Mutex
	log    []string
}

func newHTTPBlockingBLeg() *httpBlockingBLeg {
	return &httpBlockingBLeg{closed: make(chan struct{})}
}

func (s *httpBlockingBLeg) Recv(ctx context.Context) (aipapi.Event, error) {
	if ctx == nil {
		return aipapi.Event{}, aipapi.ErrNilContext
	}
	select {
	case <-ctx.Done():
		return aipapi.Event{}, ctx.Err()
	case <-s.closed:
		return aipapi.Event{}, leglifecycle.ErrALegCanceled
	}
}

func (s *httpBlockingBLeg) Cancel(_ context.Context, cause leglifecycle.CancelCause) leglifecycle.CancelResult {
	s.mu.Lock()
	s.log = append(s.log, "cancel:"+string(cause.Kind))
	s.mu.Unlock()
	return leglifecycle.CancelResult{Mode: leglifecycle.CancelModeProvider}
}

func (s *httpBlockingBLeg) Close() error {
	s.once.Do(func() { close(s.closed) })
	s.mu.Lock()
	s.log = append(s.log, "close")
	s.mu.Unlock()
	return nil
}

func (s *httpBlockingBLeg) calls() []string {
	deadline := time.After(time.Second)
	for {
		s.mu.Lock()
		out := append([]string(nil), s.log...)
		s.mu.Unlock()
		if len(out) >= 2 {
			return out
		}
		select {
		case <-deadline:
			return out
		case <-time.After(time.Millisecond):
		}
	}
}

var (
	_ aipapi.EventStream       = (*httpBlockingBLeg)(nil)
	_ leglifecycle.BLegAttempt = (*httpBlockingBLeg)(nil)
)
