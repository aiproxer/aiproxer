package runtime

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aiproxer/aiproxer/internal/core/affinity"
	"github.com/aiproxer/aiproxer/internal/core/affinity/memorystore"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/streamrecovery"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
)

// TestDefect1_ReplacementOpen_BlocksOnContextDone_CanceledByClose proves Defect 1:
// Replacement open must be owned by the stream/lifecycle context. When Backend B's Open
// blocks on ctx.Done() ONLY (no manual release channel), Close() on the stream must
// promptly cancel the in-flight replacement open without hanging.
func TestDefect1_ReplacementOpen_BlocksOnContextDone_CanceledByClose(t *testing.T) {
	ex, _, _ := setupTestExecutor(t)

	respStarted := aipapi.Event{Kind: aipapi.EventResponseStarted}
	recErr := aipapi.RecoverablePreOutputError(errors.New("connection reset by peer before output"))

	openBStarted := make(chan struct{})
	bOpenObservedCtxDone := make(chan struct{})
	var opensA, opensB atomic.Int32

	ex.Backends = map[string]execbackend.Backend{
		"backend-a": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			OpenWire: func(ctx context.Context, req largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
				opensA.Add(1)
				return &failAfterEventsStream{
					events: []aipapi.Event{respStarted},
					fail:   recErr,
				}, nil
			},
		},
		"backend-b": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			OpenWire: func(ctx context.Context, req largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
				opensB.Add(1)
				close(openBStarted)
				// Backend B blocks on ctx.Done() ONLY (no manual release channel)
				<-ctx.Done()
				close(bOpenObservedCtxDone)
				return nil, ctx.Err()
			},
		},
	}

	src := newTestSource(`{"model":"gpt-4o","prompt":"wire-defect-1"}`)
	acc := makeTestAcceptedAssessment(t, "gen-1", "openai-chat", src, false, "backend-a:gpt-4o", "backend-b:gpt-4o")
	acc.WireRequest.CandidateModel = "backend-a:gpt-4o|backend-b:gpt-4o"

	principal := execview.PrincipalView{ID: "usr-defect-1"}
	ctx := execview.WithPrincipal(context.Background(), principal)
	ctx = largebody.WithWireIdentity(ctx, "req-defect-1", "trace-defect-1")

	res, err := ex.ExecuteLargeBody(ctx, acc, src)
	require.NoError(t, err)
	require.NotNil(t, res.Stream)

	recvErrCh := make(chan error, 1)
	go func() {
		ev, rerr := res.Stream.Recv(context.Background())
		if rerr != nil {
			recvErrCh <- rerr
			return
		}
		assert.Equal(t, aipapi.EventResponseStarted, ev.Kind)

		// Next Recv encounters recoverable error on A, triggering replacement open for B
		_, rerr = res.Stream.Recv(context.Background())
		recvErrCh <- rerr
	}()

	select {
	case <-openBStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for backend B open to start")
	}

	// Client Close fires while backend B open is blocked on ctx.Done()
	err = res.Stream.Close()
	require.NoError(t, err)

	// In defect 1, replacement open ran under wireIn.outCtx (not canceled by Close),
	// so backend B would hang forever on <-ctx.Done().
	// In the collapsed architecture, replacement open is canceled promptly by Close.
	select {
	case <-bOpenObservedCtxDone:
		// Succeeded: backend B observed cancellation promptly
	case <-time.After(2 * time.Second):
		t.Fatal("backend B open did NOT observe cancellation after stream.Close() (hung on <-ctx.Done())")
	}

	select {
	case rerr := <-recvErrCh:
		assert.True(t, errors.Is(rerr, io.EOF) || errors.Is(rerr, context.Canceled), "expected Recv to return EOF or Canceled on closed stream, got %v", rerr)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for Recv to return after stream.Close()")
	}
}

// TestDefect2_TTFT_KeptAliveUntilOutputCommitment_FailsOver proves Defect 2:
// In PR #630, progress.ttft.markCommitted() was called immediately at open, disabling TTFT.
// When Backend A emits started (non-committing) and stalls past leaf TTFT timeout,
// TTFT must remain armed until true output commitment, causing leaf-failover to Backend B.
func TestDefect2_TTFT_KeptAliveUntilOutputCommitment_FailsOver(t *testing.T) {
	respStarted := aipapi.Event{Kind: aipapi.EventResponseStarted}
	textDelta := aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "recovered-via-ttft"}
	respFinished := aipapi.Event{Kind: aipapi.EventResponseFinished}

	runTest := func(t *testing.T, isWire bool) {
		t.Helper()
		ex, _, _ := setupTestExecutor(t)
		ex.Now = time.Now

		var opensA, opensB atomic.Int32
		aStallBlocker := make(chan struct{})
		defer close(aStallBlocker)

		streamA := &stallingAfterEventsStream{
			events:       []aipapi.Event{respStarted},
			stallBlocker: aStallBlocker,
		}

		backendA := execbackend.Backend{
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(ctx context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				opensA.Add(1)
				return streamA, nil
			},
			OpenWire: func(ctx context.Context, req largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
				opensA.Add(1)
				return streamA, nil
			},
		}

		backendB := execbackend.Backend{
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(ctx context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				opensB.Add(1)
				return &failAfterEventsStream{
					events: []aipapi.Event{respStarted, textDelta, respFinished},
				}, nil
			},
			OpenWire: func(ctx context.Context, req largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
				opensB.Add(1)
				return &failAfterEventsStream{
					events: []aipapi.Event{respStarted, textDelta, respFinished},
				}, nil
			},
		}

		ex.Backends = map[string]execbackend.Backend{
			"backend-a": backendA,
			"backend-b": backendB,
		}

		selector := "[ttft_timeout=1]backend-a:gpt-4o|backend-b:gpt-4o"
		principal := execview.PrincipalView{ID: "usr-defect-2"}

		var stream aipapi.EventStream
		if isWire {
			src := newTestSource(`{"model":"gpt-4o","prompt":"wire-ttft"}`)
			acc := makeTestAcceptedAssessment(t, "gen-1", "openai-chat", src, false, "backend-a:gpt-4o", "backend-b:gpt-4o")
			acc.WireRequest.CandidateModel = selector

			ctx := execview.WithPrincipal(context.Background(), principal)
			ctx = largebody.WithWireIdentity(ctx, "req-wire-ttft", "trace-wire-ttft")

			res, err := ex.ExecuteLargeBody(ctx, acc, src)
			require.NoError(t, err)
			stream = res.Stream
		} else {
			call := &aipapi.Call{
				Route: aipapi.RouteIntent{Selector: selector},
				Messages: []aipapi.Message{{
					Role:  aipapi.RoleUser,
					Parts: []aipapi.Part{aipapi.TextPart("hello")},
				}},
			}
			ctx := execview.WithPrincipal(context.Background(), principal)
			s, err := ex.Execute(ctx, call)
			require.NoError(t, err)
			stream = s
		}
		defer func() { _ = stream.Close() }()

		recvCtx, cancel := context.WithTimeout(context.Background(), 2500*time.Millisecond)
		defer cancel()

		var sawTextDelta bool
		for {
			ev, err := stream.Recv(recvCtx)
			if err != nil {
				if errors.Is(err, io.EOF) {
					break
				}
				require.NoError(t, err)
			}
			if ev.Kind == aipapi.EventTextDelta && ev.Delta == "recovered-via-ttft" {
				sawTextDelta = true
			}
		}

		assert.True(t, sawTextDelta, "must receive text from backend B after backend A TTFT timeout")
		assert.Equal(t, int32(1), opensA.Load(), "backend A must have been opened")
		assert.Equal(t, int32(1), opensB.Load(), "backend B must have been opened via TTFT failover")
	}

	t.Run("Canonical_Baseline", func(t *testing.T) {
		runTest(t, false)
	})

	t.Run("Wire_Differential", func(t *testing.T) {
		runTest(t, true)
	})
}

// TestDefect3_EOFAndIdle_AutoRecovery_Differential proves Defect 3:
// Configured stream auto-recovery (EOF and idle timeout pre-commit) must behave
// differentially identical between canonical Execute and wire ExecuteLargeBody.
func TestDefect3_EOFAndIdle_AutoRecovery_Differential(t *testing.T) {
	respStarted := aipapi.Event{Kind: aipapi.EventResponseStarted}
	textDelta := aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "recovered-via-auto-recovery"}
	respFinished := aipapi.Event{Kind: aipapi.EventResponseFinished}

	t.Run("EOF_PreCommit_AutoRecovery", func(t *testing.T) {
		runEOFTest := func(t *testing.T, isWire bool) {
			t.Helper()
			ex, _, _ := setupTestExecutor(t)
			ex.Now = time.Now
			ex.StreamRecovery = streamrecovery.Config{Enabled: true}

			var opensA, opensB atomic.Int32

			backendA := execbackend.Backend{
				Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
				Open: func(ctx context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
					opensA.Add(1)
					// Emits started then EOF pre-commit
					return &failAfterEventsStream{
						events: []aipapi.Event{respStarted},
					}, nil
				},
				OpenWire: func(ctx context.Context, req largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
					opensA.Add(1)
					return &failAfterEventsStream{
						events: []aipapi.Event{respStarted},
					}, nil
				},
			}

			backendB := execbackend.Backend{
				Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
				Open: func(ctx context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
					opensB.Add(1)
					return &failAfterEventsStream{
						events: []aipapi.Event{respStarted, textDelta, respFinished},
					}, nil
				},
				OpenWire: func(ctx context.Context, req largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
					opensB.Add(1)
					return &failAfterEventsStream{
						events: []aipapi.Event{respStarted, textDelta, respFinished},
					}, nil
				},
			}

			ex.Backends = map[string]execbackend.Backend{
				"backend-a": backendA,
				"backend-b": backendB,
			}

			selector := "backend-a:gpt-4o|backend-b:gpt-4o"
			principal := execview.PrincipalView{ID: "usr-defect-3-eof"}

			var stream aipapi.EventStream
			if isWire {
				src := newTestSource(`{"model":"gpt-4o","prompt":"wire-eof"}`)
				acc := makeTestAcceptedAssessment(t, "gen-1", "openai-chat", src, false, "backend-a:gpt-4o", "backend-b:gpt-4o")
				acc.WireRequest.CandidateModel = selector

				ctx := execview.WithPrincipal(context.Background(), principal)
				ctx = largebody.WithWireIdentity(ctx, "req-wire-eof", "trace-wire-eof")

				res, err := ex.ExecuteLargeBody(ctx, acc, src)
				require.NoError(t, err)
				stream = res.Stream
			} else {
				call := &aipapi.Call{
					Route: aipapi.RouteIntent{Selector: selector},
					Messages: []aipapi.Message{{
						Role:  aipapi.RoleUser,
						Parts: []aipapi.Part{aipapi.TextPart("hello")},
					}},
				}
				ctx := execview.WithPrincipal(context.Background(), principal)
				s, err := ex.Execute(ctx, call)
				require.NoError(t, err)
				stream = s
			}
			defer func() { _ = stream.Close() }()

			recvCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()

			var sawTextDelta bool
			for {
				ev, err := stream.Recv(recvCtx)
				if err != nil {
					if errors.Is(err, io.EOF) {
						break
					}
					require.NoError(t, err)
				}
				if ev.Kind == aipapi.EventTextDelta && ev.Delta == "recovered-via-auto-recovery" {
					sawTextDelta = true
				}
			}

			assert.True(t, sawTextDelta, "must receive text from backend B after backend A pre-commit EOF")
			assert.Equal(t, int32(1), opensA.Load(), "backend A must have been opened")
			assert.Equal(t, int32(1), opensB.Load(), "backend B must have been opened via auto-recovery failover")
		}

		t.Run("Canonical_Baseline", func(t *testing.T) {
			runEOFTest(t, false)
		})

		t.Run("Wire_Differential", func(t *testing.T) {
			runEOFTest(t, true)
		})
	})

	t.Run("Idle_PreCommit_AutoRecovery", func(t *testing.T) {
		runIdleTest := func(t *testing.T, isWire bool) {
			t.Helper()
			ex, _, _ := setupTestExecutor(t)
			ex.Now = time.Now
			ex.StreamRecovery = streamrecovery.Config{Enabled: true, IdleTimeout: 50 * time.Millisecond}

			var opensA, opensB atomic.Int32
			aStallBlocker := make(chan struct{})
			defer close(aStallBlocker)

			streamA := &stallingAfterEventsStream{
				events:       []aipapi.Event{respStarted},
				stallBlocker: aStallBlocker,
			}

			backendA := execbackend.Backend{
				Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
				Open: func(ctx context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
					opensA.Add(1)
					return streamA, nil
				},
				OpenWire: func(ctx context.Context, req largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
					opensA.Add(1)
					return streamA, nil
				},
			}

			backendB := execbackend.Backend{
				Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
				Open: func(ctx context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
					opensB.Add(1)
					return &failAfterEventsStream{
						events: []aipapi.Event{respStarted, textDelta, respFinished},
					}, nil
				},
				OpenWire: func(ctx context.Context, req largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
					opensB.Add(1)
					return &failAfterEventsStream{
						events: []aipapi.Event{respStarted, textDelta, respFinished},
					}, nil
				},
			}

			ex.Backends = map[string]execbackend.Backend{
				"backend-a": backendA,
				"backend-b": backendB,
			}

			selector := "backend-a:gpt-4o|backend-b:gpt-4o"
			principal := execview.PrincipalView{ID: "usr-defect-3-idle"}

			var stream aipapi.EventStream
			if isWire {
				src := newTestSource(`{"model":"gpt-4o","prompt":"wire-idle"}`)
				acc := makeTestAcceptedAssessment(t, "gen-1", "openai-chat", src, false, "backend-a:gpt-4o", "backend-b:gpt-4o")
				acc.WireRequest.CandidateModel = selector

				ctx := execview.WithPrincipal(context.Background(), principal)
				ctx = largebody.WithWireIdentity(ctx, "req-wire-idle", "trace-wire-idle")

				res, err := ex.ExecuteLargeBody(ctx, acc, src)
				require.NoError(t, err)
				stream = res.Stream
			} else {
				call := &aipapi.Call{
					Route: aipapi.RouteIntent{Selector: selector},
					Messages: []aipapi.Message{{
						Role:  aipapi.RoleUser,
						Parts: []aipapi.Part{aipapi.TextPart("hello")},
					}},
				}
				ctx := execview.WithPrincipal(context.Background(), principal)
				s, err := ex.Execute(ctx, call)
				require.NoError(t, err)
				stream = s
			}
			defer func() { _ = stream.Close() }()

			recvCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()

			var sawTextDelta bool
			for {
				ev, err := stream.Recv(recvCtx)
				if err != nil {
					if errors.Is(err, io.EOF) {
						break
					}
					require.NoError(t, err)
				}
				if ev.Kind == aipapi.EventTextDelta && ev.Delta == "recovered-via-auto-recovery" {
					sawTextDelta = true
				}
			}

			assert.True(t, sawTextDelta, "must receive text from backend B after backend A idle timeout failover")
			assert.Equal(t, int32(1), opensA.Load(), "backend A must have been opened")
			assert.Equal(t, int32(1), opensB.Load(), "backend B must have been opened via idle failover")
		}

		t.Run("Canonical_Baseline", func(t *testing.T) {
			runIdleTest(t, false)
		})

		t.Run("Wire_Differential", func(t *testing.T) {
			runIdleTest(t, true)
		})
	})
}

type spyAffinityStore struct {
	affinity.Store
	mu       sync.Mutex
	setCalls []affinity.Binding
}

func (s *spyAffinityStore) Set(ctx context.Context, b affinity.Binding) error {
	s.mu.Lock()
	s.setCalls = append(s.setCalls, b)
	s.mu.Unlock()
	return s.Store.Set(ctx, b)
}

// TestDefect4_AffinityAndAttemptSuccess_BoundAtCommitmentNotOpen proves Defect 4:
// In PR #630, recordAttemptLogged(AttemptSuccess) and AffinityStore.Set("output_committed")
// were invoked at OPEN. When Backend A opens, emits started, fails recoverably,
// and Backend B is unopenable, the overall request fails.
// Assert: NO affinity binding is committed to A (store remains empty / uncommitted)
// and NO AttemptSuccess is logged for A (failure recorded instead).
func TestDefect4_AffinityAndAttemptSuccess_BoundAtCommitmentNotOpen(t *testing.T) {
	ex, _, b2 := setupTestExecutor(t)
	ex.Now = time.Now

	affStore := &spyAffinityStore{Store: memorystore.New()}
	ex.AffinityStore = affStore

	spyStore := &attemptSpyStore{Store: b2}
	ex.Store = spyStore

	respStarted := aipapi.Event{Kind: aipapi.EventResponseStarted}
	recErr := aipapi.RecoverablePreOutputError(errors.New("connection reset by peer before output"))

	var opensA, opensB atomic.Int32

	ex.Backends = map[string]execbackend.Backend{
		"backend-a": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			OpenWire: func(ctx context.Context, req largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
				opensA.Add(1)
				return &failAfterEventsStream{
					events: []aipapi.Event{respStarted},
					fail:   recErr,
				}, nil
			},
		},
		"backend-b": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			OpenWire: func(ctx context.Context, req largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
				opensB.Add(1)
				return nil, errors.New("backend B unopenable")
			},
		},
	}

	src := newTestSource(`{"model":"gpt-4o","prompt":"wire-defect-4"}`)
	acc := makeTestAcceptedAssessment(t, "gen-1", "openai-chat", src, true, "backend-a:gpt-4o", "backend-b:gpt-4o")
	acc.WireRequest.CandidateModel = "{client_sticky}backend-a:gpt-4o|backend-b:gpt-4o"

	principal := execview.PrincipalView{ID: "usr-defect-4"}
	ctx := execview.WithPrincipal(context.Background(), principal)
	ctx = largebody.WithWireIdentity(ctx, "req-defect-4", "trace-defect-4")

	res, err := ex.ExecuteLargeBody(ctx, acc, src)
	require.NoError(t, err)
	require.NotNil(t, res.Stream)
	defer func() { _ = res.Stream.Close() }()

	ev, err := res.Stream.Recv(context.Background())
	if err == nil {
		assert.Equal(t, aipapi.EventResponseStarted, ev.Kind)
		_, err = res.Stream.Recv(context.Background())
	}
	// The request should fail because B is unopenable
	require.Error(t, err, "request must fail when backend B is unopenable")

	// Verify AffinityStore has NO bindings set because output never committed
	affStore.mu.Lock()
	defer affStore.mu.Unlock()
	if len(affStore.setCalls) > 0 {
		t.Fatalf("expected AffinityStore to have NO Set calls for backend-a because output never committed, but got: %+v", affStore.setCalls)
	}

	// Verify no AttemptSuccess was logged for backend A in spyStore
	spyStore.mu.Lock()
	defer spyStore.mu.Unlock()
	for _, rec := range spyStore.records {
		if rec.BackendID == "backend-a" && rec.Outcome == aipapi.AttemptSuccess {
			t.Fatalf("found premature AttemptSuccess logged for backend A at open time: %+v", rec)
		}
	}
}

// stallingAfterEventsStream emits its events and then blocks on stallBlocker until it is closed or ctx canceled.
type stallingAfterEventsStream struct {
	events       []aipapi.Event
	idx          int
	stallBlocker <-chan struct{}
}

func (s *stallingAfterEventsStream) Recv(ctx context.Context) (aipapi.Event, error) {
	if s.idx < len(s.events) {
		ev := s.events[s.idx]
		s.idx++
		return ev, nil
	}
	select {
	case <-s.stallBlocker:
		return aipapi.Event{}, io.EOF
	case <-ctx.Done():
		return aipapi.Event{}, ctx.Err()
	}
}

func (s *stallingAfterEventsStream) Close() error { return nil }

func (s *stallingAfterEventsStream) Cancel(ctx context.Context, cause aipapi.CancelCause) aipapi.CancelResult {
	return aipapi.CancelResult{Mode: aipapi.CancelModeCloseOnly}
}

var _ aipapi.ManagedEventStream = (*stallingAfterEventsStream)(nil)
