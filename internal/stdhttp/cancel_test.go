package stdhttp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/leglifecycle"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/core/securesession/domain"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestALegIDFromCancelPath(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		path string
		id   string
		ok   bool
	}{
		{name: "cancel path", path: "/aip/v1/a-legs/a-1/cancel", id: "a-1", ok: true},
		{name: "missing cancel suffix", path: "/aip/v1/a-legs/a-1", ok: false},
		{name: "extra path", path: "/aip/v1/a-legs/a-1/cancel/extra", ok: false},
		{name: "empty id", path: "/aip/v1/a-legs//cancel", ok: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := aLegIDFromCancelPath(tt.path)
			if ok != tt.ok || got != tt.id {
				t.Fatalf("aLegIDFromCancelPath(%q) = (%q, %v), want (%q, %v)", tt.path, got, ok, tt.id, tt.ok)
			}
		})
	}
}

func TestCancelErrorWire(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		err        error
		wantStatus int
		wantType   string
	}{
		{name: "missing principal", err: domain.ErrMissingPrincipal, wantStatus: http.StatusUnauthorized, wantType: "authentication_error"},
		{name: "owner mismatch", err: domain.ErrOwnerMismatch, wantStatus: http.StatusForbidden, wantType: "invalid_request_error"},
		{name: "session not found", err: domain.ErrSessionNotFound, wantStatus: http.StatusNotFound, wantType: "invalid_request_error"},
		{name: "internal", err: errors.New("database down"), wantStatus: http.StatusInternalServerError, wantType: "api_error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			gotStatus, _, gotType := cancelErrorWire(tc.err)
			if gotStatus != tc.wantStatus || gotType != tc.wantType {
				t.Fatalf("cancelErrorWire = (%d, %q), want (%d, %q)", gotStatus, gotType, tc.wantStatus, tc.wantType)
			}
		})
	}
}

func TestMountALegCancel_cancelsRuntimeBLeg(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	inner := &stdCancelStream{closed: make(chan struct{})}
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
	call := &aipapi.Call{
		Session: aipapi.SessionRef{ContinuityKey: "std-cancel"},
		Route:   aipapi.RouteIntent{Selector: "managed:m"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stream.Close() })
	if call.Session.ALegID == "" {
		t.Fatal("executor did not assign A-leg id")
	}
	mux := http.NewServeMux()
	mountALegCancel(mux, HTTPFrontendInput{Executor: ex})
	req := httptest.NewRequest(http.MethodPost, "/aip/v1/a-legs/"+call.Session.ALegID+"/cancel", nil)
	rr := httptest.NewRecorder()

	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var body struct {
		ALegID string `json:"a_leg_id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.ALegID != call.Session.ALegID || body.Status != "cancelled" {
		t.Fatalf("body = %+v", body)
	}
	if got, want := inner.calls(), []string{"cancel:explicit", "close"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("backend calls = %v want %v", got, want)
	}
}

type stdCancelStream struct {
	closed chan struct{}
	once   sync.Once
	mu     sync.Mutex
	log    []string
}

func (s *stdCancelStream) Recv(ctx context.Context) (aipapi.Event, error) {
	select {
	case <-ctx.Done():
		return aipapi.Event{}, ctx.Err()
	case <-s.closed:
		return aipapi.Event{}, leglifecycle.ErrALegCanceled
	}
}

func (s *stdCancelStream) Cancel(_ context.Context, cause aipapi.CancelCause) aipapi.CancelResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.log = append(s.log, "cancel:"+string(cause.Kind))
	return aipapi.CancelResult{Mode: aipapi.CancelModeProvider}
}

func (s *stdCancelStream) Close() error {
	s.once.Do(func() { close(s.closed) })
	s.mu.Lock()
	defer s.mu.Unlock()
	s.log = append(s.log, "close")
	return nil
}

func (s *stdCancelStream) calls() []string {
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

var _ aipapi.ManagedEventStream = (*stdCancelStream)(nil)
