package acp

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// preparedProcess is a fake child carrying the two prepared-process seams the
// Claude Agent connector installs on its spawned process: an immutable
// root-scoped fingerprint and a per-turn reuse verdict. Both replace the
// client-derived process config as the reuse authority.
type preparedProcess struct {
	*fakeProcess
	fingerprint string
	reuse       func() (bool, error)
}

func (p *preparedProcess) RuntimeFingerprint() string { return p.fingerprint }

// ValidateReuse reports the current verdict; the test owns it and only changes
// it between turns, on the same goroutine that drives Open.
func (p *preparedProcess) ValidateReuse(context.Context) (bool, error) {
	if p.reuse == nil {
		return true, nil
	}
	return p.reuse()
}

func (p *preparedProcess) wasKilled() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.killed
}

// preparedStarter spawns one fresh prepared child per Start and records them, so
// a test counts real spawns instead of trusting the pool's bookkeeping.
type preparedStarter struct {
	t     *testing.T
	procs []*preparedProcess
}

func (s *preparedStarter) Start(_ []string, _ string, _ []string) (Process, error) {
	proc := &preparedProcess{fakeProcess: newFakeProcess(s.t), fingerprint: "policy-fingerprint"}
	s.procs = append(s.procs, proc)
	return proc, nil
}

func (s *preparedStarter) last() *preparedProcess {
	return s.procs[len(s.procs)-1]
}

// TestOpen_PreparedProcessFingerprintDecidesReuse proves the runtime's published
// fingerprint, not the client-derived process config, decides whether a live
// child is compatible: two turns on the same prepared child must reuse it.
// Mutant: drop the fingerprint override in Open (keep ResolveProcessConfig) ->
// the second turn sees a mismatch, kills the child and respawns.
func TestOpen_PreparedProcessFingerprintDecidesReuse(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	proto := &fakeTurnProtocol{model: "agent", procConfig: "client-derived"}
	starter := &preparedStarter{t: t}
	backend := NewSubprocessBackend(SubprocessBackendConfig{
		Protocol:       proto,
		Workspace:      WorkspacePolicy{DefaultDir: workspace},
		ProcessStarter: starter,
		Log:            slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	t.Cleanup(func() { _ = backend.Close() })

	for turn := range 2 {
		stream, err := backend.Open(t.Context(), newTurnCall("prepared-reuse"))
		if err != nil {
			t.Fatalf("turn %d Open: %v", turn, err)
		}
		if err := stream.Close(); err != nil {
			t.Fatalf("turn %d Close: %v", turn, err)
		}
	}
	if len(starter.procs) != 1 {
		t.Fatalf("spawns = %d, want 1: the published fingerprint must keep the compatible child", len(starter.procs))
	}
	if starter.procs[0].wasKilled() {
		t.Fatal("compatible prepared child must not be killed")
	}
}

// TestOpen_WithdrawsRefusedReuseBeforeNextTurn proves the pre-turn reuse gate:
// a prepared child that reports it must not be reused never serves the next
// turn. A refusal replaces the runtime with a fresh spawn; a failed recheck
// fails the turn while releasing the claim so a later compatible turn works.
// Mutant: ignore the ValidateReuse verdict -> the refused child keeps serving.
func TestOpen_WithdrawsRefusedReuseBeforeNextTurn(t *testing.T) {
	t.Parallel()
	newBackend := func(t *testing.T, workspace string) (SubprocessEngine, *preparedStarter) {
		t.Helper()
		starter := &preparedStarter{t: t}
		backend := NewSubprocessBackend(SubprocessBackendConfig{
			Protocol:       &fakeTurnProtocol{model: "agent", procConfig: "client-derived"},
			Workspace:      WorkspacePolicy{DefaultDir: workspace},
			ProcessStarter: starter,
			Log:            slog.New(slog.NewTextHandler(io.Discard, nil)),
		})
		t.Cleanup(func() { _ = backend.Close() })
		return backend, starter
	}
	openTurn := func(t *testing.T, backend SubprocessEngine, session string) {
		t.Helper()
		stream, err := backend.Open(t.Context(), newTurnCall(session))
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		if err := stream.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	}

	t.Run("refusal replaces the runtime", func(t *testing.T) {
		backend, starter := newBackend(t, t.TempDir())
		openTurn(t, backend, "reuse-refused")
		starter.last().reuse = func() (bool, error) { return false, nil }
		openTurn(t, backend, "reuse-refused")
		if len(starter.procs) != 2 {
			t.Fatalf("spawns = %d, want 2: a refused runtime must be replaced", len(starter.procs))
		}
		if !starter.procs[0].wasKilled() {
			t.Fatal("the refused child must be killed")
		}
	})

	t.Run("error fails the turn and releases the claim", func(t *testing.T) {
		backend, starter := newBackend(t, t.TempDir())
		openTurn(t, backend, "reuse-error")
		recheck := errors.New("policy recheck failed")
		starter.last().reuse = func() (bool, error) { return false, recheck }
		if _, err := backend.Open(t.Context(), newTurnCall("reuse-error")); !errors.Is(err, recheck) {
			t.Fatalf("reuse error = %v, want the turn to fail with the recheck error", err)
		}
		if len(starter.procs) != 1 {
			t.Fatalf("spawns = %d, want 1: a failed recheck must not spawn", len(starter.procs))
		}
		starter.last().reuse = nil
		openTurn(t, backend, "reuse-error")
	})
}

// TestOpen_RejectsNilContextBeforeAnyWork proves a nil context is a typed
// caller error instead of a panic, and that the rejected call spawns nothing.
// Mutant: drop the nil-context guard in Open -> nil-context panic inside the pool.
func TestOpen_RejectsNilContextBeforeAnyWork(t *testing.T) {
	t.Parallel()
	starter := &preparedStarter{t: t}
	backend := NewSubprocessBackend(SubprocessBackendConfig{
		Protocol:       &fakeTurnProtocol{model: "agent"},
		Workspace:      WorkspacePolicy{DefaultDir: t.TempDir()},
		ProcessStarter: starter,
		Log:            slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	t.Cleanup(func() { _ = backend.Close() })

	// Typed nil: the guard under test is the callee's nil-context rejection.
	var nilCtx context.Context
	if _, err := backend.Open(nilCtx, newTurnCall("nil-context")); !errors.Is(err, aipapi.ErrNilContext) {
		t.Fatalf("Open(nil, call) = %v, want aipapi.ErrNilContext", err)
	}
	if len(starter.procs) != 0 {
		t.Fatalf("spawns = %d, want 0: a rejected call must not spawn", len(starter.procs))
	}
}

// TestPoolManagedStream_CancelledAdmissionTurnWithdrawsRuntime proves a
// cancelled admission-guarded turn never counts as completed, so Close still
// withdraws the runtime whose prompt state the cancellation left ambiguous.
// Mutant: let a terminal frame mark a cancelled admission turn completed ->
// Close releases the ambiguous runtime for reuse.
func TestPoolManagedStream_CancelledAdmissionTurnWithdrawsRuntime(t *testing.T) {
	t.Parallel()
	key := RuntimeKey{Workspace: t.TempDir(), Model: "m", ClientSession: "c"}
	pool := NewRuntimePool(RuntimePoolConfig{})
	t.Cleanup(func() { _ = pool.Close() })
	pool.SetProcess(key, newFakeProcess(t), nil, "sid", "", "acp")
	if _, busy := pool.ClaimForTurn(t.Context(), key, ""); busy {
		t.Fatal("runtime must be claimable")
	}
	body := "{\"jsonrpc\":\"2.0\",\"id\":9,\"result\":{\"stopReason\":\"end_turn\"}}\n"
	inner := newPromptNDJSONStream(
		context.Background(), io.NopCloser(strings.NewReader(body)), &client{t: &unarySpyTransport{}},
		"sid", 9, "mid", SessionUpdateMapperOptions{}, nil, CancelProfile{}, 0,
	)
	stream := &poolManagedStream{inner: inner, pool: pool, key: key, admission: true}
	if res := stream.Cancel(context.Background(), aipapi.CancelCause{Kind: aipapi.CancelExplicit}); res.Mode != aipapi.CancelModeProvider {
		t.Fatalf("cancel mode = %s", res.Mode)
	}
	// The adapter answers the cancel with a terminal frame; it must not be read
	// as a clean completion of the admission-guarded turn.
	for {
		_, err := stream.Recv(context.Background())
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Recv: %v", err)
		}
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	if rt := pool.Get(key); rt == nil || rt.HasProcess() {
		t.Fatal("cancelled admission turn left a reusable runtime")
	}
}
