package acp

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// fakeTurnProtocol is a minimal SubprocessProtocol for Open-level lifecycle
// tests. It does no real JSON-RPC I/O: Handshake returns a fixed session id and
// the session's SendPrompt/BuildStream return inert holders so the turn appears
// in-flight (pool claimed) until the caller closes the stream. procConfig is
// mutable between calls so a test can issue two turns with different
// process-scoped configs against the same runtime key.
type fakeTurnProtocol struct {
	mu         sync.Mutex
	procConfig string
	model      string
}

func (p *fakeTurnProtocol) Label() string                    { return "fake-turn" }
func (p *fakeTurnProtocol) ValidateCall(*aipapi.Call) error  { return nil }
func (p *fakeTurnProtocol) ResolveModel(*aipapi.Call) string { return p.model }
func (p *fakeTurnProtocol) BuildSpawnCommand(model, workspace, _ string) ([]string, string, []string, error) {
	return []string{"fake-bin", "--model", model}, workspace, nil, nil
}

func (p *fakeTurnProtocol) Handshake(context.Context, Transport, string, string) (string, error) {
	return "session-fake", nil
}

func (p *fakeTurnProtocol) BindSession(transport Transport, sessionID string) SubprocessProtocolSession {
	return &fakeTurnSession{transport: transport, sessionID: sessionID}
}

func (p *fakeTurnProtocol) ResolveProcessConfig(*aipapi.Call) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.procConfig
}

func (p *fakeTurnProtocol) setProcessConfig(cfg string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.procConfig = cfg
}

type fakeTurnSession struct {
	transport Transport
	sessionID string
}

func (s *fakeTurnSession) SendPrompt(context.Context, string, string, *aipapi.Call) (io.ReadCloser, int64, error) {
	return io.NopCloser(strings.NewReader("")), 1, nil
}

func (s *fakeTurnSession) BuildStream(_ context.Context, _ io.ReadCloser, _ int64, pool *RuntimePool, key RuntimeKey, _ int) (aipapi.ManagedEventStream, error) {
	return &fakeTurnStream{pool: pool, key: key}, nil
}

// fakeTurnStream is a no-event stream that releases the pool on Close, mirroring
// the real poolManagedStream lifecycle contract.
type fakeTurnStream struct {
	pool   *RuntimePool
	key    RuntimeKey
	mu     sync.Mutex
	closed bool
}

func (s *fakeTurnStream) Recv(context.Context) (aipapi.Event, error) {
	return aipapi.Event{}, io.EOF
}

func (s *fakeTurnStream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	s.pool.Release(s.key)
	return nil
}

func (s *fakeTurnStream) Cancel(context.Context, aipapi.CancelCause) aipapi.CancelResult {
	return aipapi.CancelResult{}
}

// fakeStarter returns a pre-created fakeProcess for every Start call.
type fakeStarter struct {
	proc *fakeProcess
}

func (f *fakeStarter) Start(_ []string, _ string, _ []string) (Process, error) {
	return f.proc, nil
}

func newTurnCall(sessionID string) *aipapi.Call {
	return &aipapi.Call{
		ID:      "t",
		Session: aipapi.SessionRef{ClientSessionID: sessionID},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
}

// TestOpen_RejectsConfigChangeWhileTurnInFlight is the high-severity regression
// test: a second turn requesting a different process-scoped config (Codex
// model_verbosity) must NOT kill the child the first turn is still streaming on.
// The second Open fails explicitly and the first turn's subprocess survives.
func TestOpen_RejectsConfigChangeWhileTurnInFlight(t *testing.T) {
	t.Parallel()
	proto := &fakeTurnProtocol{model: "agent", procConfig: "low"}
	proc := newFakeProcess(t)
	backend := NewSubprocessBackend(SubprocessBackendConfig{
		Protocol:       proto,
		Workspace:      WorkspacePolicy{DefaultDir: os.TempDir()},
		Pool:           RuntimePoolConfig{},
		ProcessStarter: &fakeStarter{proc: proc},
		Log:            slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	t.Cleanup(func() { _ = backend.Close() })

	// First turn: spawns a child with process config "low" and holds the stream
	// open so the runtime stays claimed (inUse).
	first, err := backend.Open(context.Background(), newTurnCall("s1"))
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	t.Cleanup(func() { _ = first.Close() })

	sub, ok := backend.(*subprocessBackend)
	if !ok {
		t.Fatalf("backend is %T, want *subprocessBackend", backend)
	}

	if got := sub.pool.Get(RuntimeKey{Workspace: os.TempDir(), Model: "agent", ClientSession: "s1"}); got == nil || !got.HasProcess() {
		t.Fatal("first turn must leave a live spawned child in the pool")
	}

	// Second turn on the same runtime key but a different process config.
	proto.setProcessConfig("high")
	_, err = backend.Open(context.Background(), newTurnCall("s1"))
	if err == nil {
		t.Fatal("second Open must fail when a turn is in flight on a mismatched child")
	}
	if !strings.Contains(err.Error(), "in flight") {
		t.Fatalf("second Open error must explain the in-flight conflict, got: %v", err)
	}

	// The first turn's child must be untouched: not killed, config unchanged.
	proc.mu.Lock()
	killed := proc.killed
	proc.mu.Unlock()
	if killed {
		t.Fatal("first turn's process must not be killed by the rejected second turn")
	}
	rt := sub.pool.Get(RuntimeKey{Workspace: os.TempDir(), Model: "agent", ClientSession: "s1"})
	if rt == nil || !rt.HasProcess() {
		t.Fatal("first turn's process must still be live after the rejected second turn")
	}
	if got := rt.ProcessConfig(); got != "low" {
		t.Fatalf("process config = %q, want low (unchanged)", got)
	}
}

// TestOpen_ServesSequentialTurnsWithConfigChange verifies that after the first
// turn's stream closes (releasing the runtime), a second turn with a different
// process config can claim the runtime and reset the child normally.
func TestOpen_ServesSequentialTurnsWithConfigChange(t *testing.T) {
	t.Parallel()
	proto := &fakeTurnProtocol{model: "agent", procConfig: "low"}
	proc := newFakeProcess(t)
	backend := NewSubprocessBackend(SubprocessBackendConfig{
		Protocol:       proto,
		Workspace:      WorkspacePolicy{DefaultDir: os.TempDir()},
		Pool:           RuntimePoolConfig{},
		ProcessStarter: &fakeStarter{proc: proc},
		Log:            slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	t.Cleanup(func() { _ = backend.Close() })

	first, err := backend.Open(context.Background(), newTurnCall("s2"))
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}

	// Runtime is released; a different-config turn must now succeed and reset.
	proto.setProcessConfig("high")
	if _, err := backend.Open(context.Background(), newTurnCall("s2")); err != nil {
		t.Fatalf("second Open after release must succeed, got: %v", err)
	}
}
