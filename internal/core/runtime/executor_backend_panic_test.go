package runtime_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/core/safety"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestExecutor_capabilityNegotiatePanic_excludesCandidateThenOpensNext(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var opened string
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(3)
	ex.Backends = map[string]execbackend.Backend{
		"panicaps": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			ResolveCaps: func(context.Context, aipapi.Call, routing.AttemptCandidate) aipapi.BackendCaps {
				panic("negotiate caps boom")
			},
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				t.Fatal("panicaps Open must not run after capability panic")
				return nil, errors.New("unexpected")
			},
		},
		"good": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(_ context.Context, _ aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				opened = cand.Primary.Backend
				return aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventResponseFinished},
				}), nil
			},
		},
	}
	call := &aipapi.Call{
		Session: aipapi.SessionRef{ContinuityKey: "cap-panic-failover"},
		Route:   aipapi.RouteIntent{Selector: "panicaps:m|good:m"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	s, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	if opened != "good" {
		t.Fatalf("opened backend: %q", opened)
	}
	_, _ = aipapi.Collect(context.Background(), s)
	_ = s.Close()
}

func TestExecutor_openPanic_preOutput_swallowedFailoverToSecondBackend(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var opens []string
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(5)
	ex.Backends = map[string]execbackend.Backend{
		"badopen": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				opens = append(opens, "badopen")
				panic("open boom")
			},
		},
		"good": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				opens = append(opens, "good")
				return aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventResponseFinished},
				}), nil
			},
		},
	}
	call := &aipapi.Call{
		Session: aipapi.SessionRef{ContinuityKey: "open-panic-failover"},
		Route:   aipapi.RouteIntent{Selector: "badopen:m|good:m"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	s, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	if len(opens) != 2 || opens[0] != "badopen" || opens[1] != "good" {
		t.Fatalf("open sequence: %v", opens)
	}
	_, _ = aipapi.Collect(context.Background(), s)
	_ = s.Close()
	alegID := strings.TrimSpace(call.Session.ALegID)
	if alegID == "" {
		t.Fatal("expected aleg id on call after execute")
	}
	leg, err := st.FetchALeg(context.Background(), alegID)
	if err != nil {
		t.Fatal(err)
	}
	atts, err := st.LoadAttempts(context.Background(), leg.ALegID)
	if err != nil {
		t.Fatal(err)
	}
	if len(atts) != 2 {
		t.Fatalf("attempts: want 2 got %d %#v", len(atts), atts)
	}
	if atts[0].Outcome != aipapi.AttemptSwallowedFailure {
		t.Fatalf("attempt1 outcome: %s", atts[0].Outcome)
	}
	if atts[0].Reason != "recoverable pre-output (open)" {
		t.Fatalf("attempt1 reason: %q", atts[0].Reason)
	}
	if atts[1].Outcome != aipapi.AttemptSuccess {
		t.Fatalf("attempt2 outcome: %s", atts[1].Outcome)
	}
}

type recvPanicStream struct{}

func (recvPanicStream) Recv(context.Context) (aipapi.Event, error) {
	panic("recv boom")
}

func (recvPanicStream) Close() error { return nil }

func (recvPanicStream) Cancel(context.Context, aipapi.CancelCause) aipapi.CancelResult {
	return aipapi.CancelResult{Mode: aipapi.CancelModeCloseOnly}
}

func TestExecutor_recvPanic_preOutput_failoverToSecondBackend(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var opens []string
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(9)
	ex.Backends = map[string]execbackend.Backend{
		"badrecv": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				opens = append(opens, "badrecv")
				return recvPanicStream{}, nil
			},
		},
		"good": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				opens = append(opens, "good")
				return aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventResponseFinished},
				}), nil
			},
		},
	}
	call := &aipapi.Call{
		Session: aipapi.SessionRef{ContinuityKey: "recv-panic-failover"},
		Route:   aipapi.RouteIntent{Selector: "badrecv:m|good:m"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	s, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := aipapi.Collect(context.Background(), s); err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(opens) != 2 || opens[0] != "badrecv" || opens[1] != "good" {
		t.Fatalf("open sequence: %v", opens)
	}
	_ = s.Close()
	alegID := strings.TrimSpace(call.Session.ALegID)
	if alegID == "" {
		t.Fatal("expected aleg id on call after execute")
	}
	leg, err := st.FetchALeg(context.Background(), alegID)
	if err != nil {
		t.Fatal(err)
	}
	atts, err := st.LoadAttempts(context.Background(), leg.ALegID)
	if err != nil {
		t.Fatal(err)
	}
	if len(atts) != 2 {
		t.Fatalf("attempts: want 2 got %d %#v", len(atts), atts)
	}
	if atts[0].Outcome != aipapi.AttemptSwallowedFailure {
		t.Fatalf("attempt1 outcome: %s", atts[0].Outcome)
	}
	if atts[0].Reason != "recoverable pre-output (recv)" {
		t.Fatalf("attempt1 reason: %q", atts[0].Reason)
	}
	if atts[1].Outcome != aipapi.AttemptSuccess {
		t.Fatalf("attempt2 outcome: %s", atts[1].Outcome)
	}
}

type deltaThenRecvPanicStream struct {
	sent bool
}

func (d *deltaThenRecvPanicStream) Recv(context.Context) (aipapi.Event, error) {
	if !d.sent {
		d.sent = true
		return aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "x"}, nil
	}
	panic("recv after commit")
}

func (deltaThenRecvPanicStream) Close() error { return nil }

func (deltaThenRecvPanicStream) Cancel(context.Context, aipapi.CancelCause) aipapi.CancelResult {
	return aipapi.CancelResult{Mode: aipapi.CancelModeCloseOnly}
}

func TestExecutor_recvPanic_postOutput_notRecoverable(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var opens int
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(13)
	ex.Backends = map[string]execbackend.Backend{
		"sole": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				opens++
				return &deltaThenRecvPanicStream{}, nil
			},
		},
	}
	call := &aipapi.Call{
		Session: aipapi.SessionRef{ContinuityKey: "recv-panic-post"},
		Route:   aipapi.RouteIntent{Selector: "sole:m"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	s, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	if opens != 1 {
		t.Fatalf("opens=%d", opens)
	}
	_, err = aipapi.Collect(context.Background(), s)
	if err == nil {
		t.Fatal("expected collect error after post-output recv panic")
	}
	if aipapi.IsRecoverablePreOutput(err) {
		t.Fatal("post-output recv panic must not be recoverable pre-output")
	}
	_ = s.Close()
}

type finishThenClosePanicStream struct {
	i int
}

func (f *finishThenClosePanicStream) Recv(context.Context) (aipapi.Event, error) {
	switch f.i {
	case 0:
		f.i++
		return aipapi.Event{Kind: aipapi.EventResponseStarted}, nil
	case 1:
		f.i++
		return aipapi.Event{Kind: aipapi.EventResponseFinished}, nil
	default:
		return aipapi.Event{}, io.EOF
	}
}

func (*finishThenClosePanicStream) Close() error {
	panic("close boom")
}

func (*finishThenClosePanicStream) Cancel(context.Context, aipapi.CancelCause) aipapi.CancelResult {
	return aipapi.CancelResult{Mode: aipapi.CancelModeCloseOnly}
}

func TestExecutor_streamClosePanic_returnsNil(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(17)
	ex.Backends = map[string]execbackend.Backend{
		"sole": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return &finishThenClosePanicStream{}, nil
			},
		},
	}
	call := &aipapi.Call{
		Session: aipapi.SessionRef{ContinuityKey: "close-panic"},
		Route:   aipapi.RouteIntent{Selector: "sole:m"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	s, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := aipapi.Collect(context.Background(), s); err != nil {
		t.Fatalf("collect: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestExecutor_streamClosePanic_logsIsolatedCrashDiagnosticsAtDebug(t *testing.T) {
	t.Parallel()
	var logBuf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(17)
	ex.Log = log
	ex.Backends = map[string]execbackend.Backend{
		"sole": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return &finishThenClosePanicStream{}, nil
			},
		},
	}
	call := &aipapi.Call{
		Session: aipapi.SessionRef{ContinuityKey: "close-panic-diag"},
		Route:   aipapi.RouteIntent{Selector: "sole:m"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	s, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := aipapi.Collect(context.Background(), s); err != nil {
		t.Fatalf("collect: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	var found bool
	scan := bufio.NewScanner(&logBuf)
	for scan.Scan() {
		line := scan.Bytes()
		var m map[string]any
		if err := json.Unmarshal(line, &m); err != nil {
			t.Fatalf("log line: %s", err)
		}
		if m["msg"] != "isolated_panic_backend_stream_close" {
			continue
		}
		found = true
		if m["panic_boundary"] != string(safety.BoundaryBackend) {
			t.Fatalf("panic_boundary=%v want %q", m["panic_boundary"], safety.BoundaryBackend)
		}
		if m["operation"] != "backend_stream_close" {
			t.Fatalf("operation=%v want backend_stream_close", m["operation"])
		}
	}
	if err := scan.Err(); err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatalf("expected isolated_panic_backend_stream_close error log, got %q", logBuf.String())
	}
	// Bounded attrs must not embed the raw panic string as a dedicated field (only stack may mention it server-side).
	if strings.Contains(logBuf.String(), `"panic_message"`) {
		t.Fatalf("unexpected panic_message field in log output")
	}
}
