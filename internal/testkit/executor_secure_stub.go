package testkit

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/core/securesession/adapters/aipapidenial"
	"github.com/aiproxer/aiproxer/internal/core/securesession/adapters/b2bualineage"
	"github.com/aiproxer/aiproxer/internal/core/securesession/adapters/memory"
	"github.com/aiproxer/aiproxer/internal/core/securesession/app"
	"github.com/aiproxer/aiproxer/internal/core/workspace"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	aipworkspace "github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
)

// SecureSessionTestFingerprintKey returns a deterministic 32-byte key for secure-session tests.
func SecureSessionTestFingerprintKey() []byte {
	k := make([]byte, 32)
	for i := range k {
		k[i] = byte(i + 1)
	}
	return k
}

// SecureSessionStubExecutorOptions configures [NewStubExecutorWithSecureSession] (task 11.x harness).
type SecureSessionStubExecutorOptions struct {
	Now      func() time.Time
	Rand     routing.Rng
	RandSeed int64 // used when Rand is nil (default 42)

	// Workspace is merged into the runtime snapshot; nil uses empty workspace (void resolver).
	Workspace aipworkspace.Resolver

	// SecureStore when non-nil is used as the secure-session durable store; otherwise a memory
	// store with SimulateDurable=true is constructed.
	SecureStore app.Store

	// SecureSessionRequireWorkspaceID mirrors runtime.Executor.SecureSessionRequireWorkspaceID.
	SecureSessionRequireWorkspaceID bool
	// SecureSessionWorkspaceResolveFailClosed mirrors runtime.Executor.SecureSessionWorkspaceResolveFailClosed.
	SecureSessionWorkspaceResolveFailClosed bool

	// FingerprintKey for resume fingerprinting; nil uses [SecureSessionTestFingerprintKey].
	FingerprintKey []byte

	// ManagerConfig is merged after FingerprintKey/ResumeWindow/StoreDurable defaults are applied.
	ManagerConfig app.ManagerConfig

	// Backends when non-nil replaces the default single-stub map entirely.
	Backends map[string]execbackend.Backend

	// StubText is emitted as one text delta when Backends is nil (same as [NewStubExecutor]).
	StubText string

	SecureSessionRecorder           app.GateRecording
	SecureSessionRecordingMandatory bool
}

// NewStubExecutorWithSecureSession builds an executor with secure-session enabled, aipapi denial
// mapping, B2BUA memory store, and an optional stub backend (task 11 E2E harness).
func NewStubExecutorWithSecureSession(t *testing.T, opts SecureSessionStubExecutorOptions, caps aipapi.BackendCaps, capture *sync.Map) *runtime.Executor {
	t.Helper()
	lineageStore, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	fk := opts.FingerprintKey
	if len(fk) == 0 {
		fk = SecureSessionTestFingerprintKey()
	}
	st := opts.SecureStore
	if st == nil {
		st = memory.New(memory.Options{SimulateDurable: true})
	}
	mc := opts.ManagerConfig
	mc.FingerprintKey = fk
	mc.StoreDurable = true
	mgr, err := app.NewManager(st, app.NewRandGenerator(fk), b2bualineage.New(lineageStore), mc)
	if err != nil {
		t.Fatal(err)
	}
	ws := opts.Workspace
	if ws == nil {
		ws = voidWorkspaceResolver{}
	}
	bus := hooks.New(hooks.Config{})
	snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		Workspace: workspace.NewResolverChain([]aipworkspace.Resolver{ws}),
	})
	nowFn := opts.Now
	if nowFn == nil {
		nowFn = func() time.Time { return time.Unix(3000, 0).UTC() }
	}
	rng := opts.Rand
	if rng == nil {
		seed := opts.RandSeed
		if seed == 0 {
			seed = 42
		}
		rng = routing.NewSeededRng(seed)
	}
	text := opts.StubText
	if text == "" {
		text = "stub-ok"
	}
	be := opts.Backends
	if be == nil {
		be = map[string]execbackend.Backend{
			"stub": {
				Caps: caps,
				Open: func(ctx context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
					if capture != nil {
						capture.Store("last", call)
						var n int
						if v, ok := capture.Load("openCount"); ok {
							n, _ = v.(int)
						}
						capture.Store("openCount", n+1)
					}
					_ = ctx
					_ = cand
					prefix := stubToolPrefixEvents(call)
					evs := make([]aipapi.Event, 0, 2+len(prefix)+2)
					evs = append(
						evs,
						aipapi.Event{Kind: aipapi.EventResponseStarted},
						aipapi.Event{Kind: aipapi.EventMessageStarted},
					)
					evs = append(evs, prefix...)
					evs = append(
						evs,
						aipapi.Event{Kind: aipapi.EventTextDelta, Delta: text},
						aipapi.Event{Kind: aipapi.EventResponseFinished},
					)
					return aipapi.NewFixedEventStream(evs), nil
				},
			},
		}
	}
	ex := runtime.TestExecutor()
	ex.Store = lineageStore
	ex.Bus = bus
	ex.RuntimeSnapshot = snap
	ex.Rand = rng
	ex.Now = nowFn
	ex.Backends = be
	ex.SecureSession = mgr
	ex.SecureSessionRecorder = opts.SecureSessionRecorder
	ex.SecureSessionRecordingMandatory = opts.SecureSessionRecordingMandatory
	ex.SessionDenialMapper = aipapidenial.MapToSessionDenial
	ex.SecureSessionRequireWorkspaceID = opts.SecureSessionRequireWorkspaceID
	ex.SecureSessionWorkspaceResolveFailClosed = opts.SecureSessionWorkspaceResolveFailClosed
	return ex
}

type voidWorkspaceResolver struct{}

func (voidWorkspaceResolver) Resolve(context.Context) (aipworkspace.WorkspaceView, error) {
	return aipworkspace.WorkspaceView{}, nil
}
