package runtime

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	sdktraffic "github.com/aiproxer/aiproxer/pkg/aipsdk/traffic"
)

// expiringRequestPartHook lets the call's deadline pass while the request is
// still being prepared, which is the window between the caller's refusal and
// the backend open.
type expiringRequestPartHook struct{ delay time.Duration }

func (h *expiringRequestPartHook) ID() string                        { return "expiring-request-parts" }
func (h *expiringRequestPartHook) Order() int                        { return 0 }
func (h *expiringRequestPartHook) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }
func (h *expiringRequestPartHook) HandleRequestParts(context.Context, *aipapi.Call, sdkhooks.PartMeta) error {
	time.Sleep(h.delay)
	return nil
}

// countingTrafficObserver records proxy-to-backend traffic observations. A
// leg refused before the observation must leave no record behind.
type countingTrafficObserver struct{ count atomic.Int32 }

func (o *countingTrafficObserver) OnObservation(context.Context, sdktraffic.Observation) error {
	o.count.Add(1)
	return nil
}

// TestExecutor_ExpiredDeadlineDoesNotOpenBackend pins the first backend
// boundary: a call whose deadline expired before the provider was contacted
// must not start provider work or leave a traffic record. The backend is never
// opened, and the caller sees the expired bound instead of a provider attempt.
func TestExecutor_ExpiredDeadlineDoesNotOpenBackend(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	observer := &countingTrafficObserver{}
	ex := TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{RequestPartHooks: []sdkhooks.RequestPartHook{
		&expiringRequestPartHook{delay: 200 * time.Millisecond},
	}})
	ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(ex.Bus, extensions.SnapshotOptions{TrafficObserver: observer})
	ex.Rand = routing.NewSeededRng(1)
	var opens atomic.Int32
	ex.Backends = map[string]execbackend.Backend{
		"expired": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				opens.Add(1)
				return aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventResponseFinished}}), nil
			},
		},
	}

	ctx, cancel := context.WithTimeout(execDetachedCtx(context.Background()), 20*time.Millisecond)
	defer cancel()
	_, err = ex.Execute(ctx, &aipapi.Call{
		Session: aipapi.SessionRef{ContinuityKey: "expired-deadline"},
		Route:   aipapi.RouteIntent{Selector: "expired:m"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Execute err = %v, want context.DeadlineExceeded", err)
	}
	if got := opens.Load(); got != 0 {
		t.Fatalf("backend opens = %d, want 0 for an expired deadline", got)
	}
	if got := observer.count.Load(); got != 0 {
		t.Fatalf("traffic observations = %d, want 0 for an already-expired deadline", got)
	}
}

// blockingTrafficObserver holds the proxy-to-backend observation open, which is
// the synchronous work the executor performs between the pre-observation
// deadline check and the provider call.
type blockingTrafficObserver struct {
	entered chan struct{}
	release chan struct{}
}

func (o *blockingTrafficObserver) OnObservation(_ context.Context, ev sdktraffic.Observation) error {
	if ev.Leg != sdktraffic.LegPTB {
		return nil
	}
	select {
	case o.entered <- struct{}{}:
	default:
	}
	<-o.release
	return nil
}

// TestExecutor_ExpiredTTFTBudgetDoesNotOpenBackend pins the effective-context
// boundary: the provider receives the TTFT-scoped context, which can be
// narrower than the caller's, so a TTFT budget that expires during the
// synchronous preparation must not open the backend either. The failure keeps
// its established TTFT classification instead of surfacing as an anonymous
// deadline error.
func TestExecutor_ExpiredTTFTBudgetDoesNotOpenBackend(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	observer := &blockingTrafficObserver{entered: make(chan struct{}, 1), release: make(chan struct{})}
	ex := TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(1)
	ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(ex.Bus, extensions.SnapshotOptions{TrafficObserver: observer})
	var opens atomic.Int32
	ex.Backends = map[string]execbackend.Backend{
		"ttft": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				opens.Add(1)
				return aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventResponseFinished}}), nil
			},
		},
	}

	// The caller's context stays live for far longer than the global TTFT
	// budget, so only the TTFT scope expires while the observation blocks.
	ctx, cancel := context.WithTimeout(execDetachedCtx(context.Background()), 30*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, execErr := ex.Execute(ctx, &aipapi.Call{
			Session: aipapi.SessionRef{ContinuityKey: "expired-ttft-budget"},
			Route:   aipapi.RouteIntent{Selector: "{ttft_timeout=1}ttft:m"},
			Messages: []aipapi.Message{{
				Role:  aipapi.RoleUser,
				Parts: []aipapi.Part{aipapi.TextPart("hi")},
			}},
		})
		done <- execErr
	}()

	select {
	case <-observer.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("traffic observation never started")
	}
	// Hold the observation past the one-second TTFT budget, then release it.
	time.Sleep(1300 * time.Millisecond)
	close(observer.release)

	err = <-done
	if !errors.Is(err, aipapi.ErrTTFTTimeout) {
		t.Fatalf("Execute err = %v, want aipapi.ErrTTFTTimeout", err)
	}
	if got := opens.Load(); got != 0 {
		t.Fatalf("backend opens = %d, want 0 when the TTFT budget expires during preparation", got)
	}
}

// the last practical boundary: the traffic observation is synchronous and
// unbounded, so the deadline can expire after the pre-observation check while
// the observation runs. The provider must not be opened in that window either.
func TestExecutor_ExpiredDeadlineDuringTrafficObservationDoesNotOpenBackend(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	observer := &blockingTrafficObserver{entered: make(chan struct{}, 1), release: make(chan struct{})}
	ex := TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(1)
	ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(ex.Bus, extensions.SnapshotOptions{TrafficObserver: observer})
	var opens atomic.Int32
	ex.Backends = map[string]execbackend.Backend{
		"expired": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				opens.Add(1)
				return aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventResponseFinished}}), nil
			},
		},
	}

	ctx, cancel := context.WithTimeout(execDetachedCtx(context.Background()), 200*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, execErr := ex.Execute(ctx, &aipapi.Call{
			Session: aipapi.SessionRef{ContinuityKey: "expired-during-observation"},
			Route:   aipapi.RouteIntent{Selector: "expired:m"},
			Messages: []aipapi.Message{{
				Role:  aipapi.RoleUser,
				Parts: []aipapi.Part{aipapi.TextPart("hi")},
			}},
		})
		done <- execErr
	}()

	select {
	case <-observer.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("traffic observation never started")
	}
	// Hold the observation past the deadline, then release it: the bound
	// expired while the observation ran.
	time.Sleep(400 * time.Millisecond)
	close(observer.release)

	err = <-done
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Execute err = %v, want context.DeadlineExceeded", err)
	}
	if got := opens.Load(); got != 0 {
		t.Fatalf("backend opens = %d, want 0 when the deadline expires during the traffic observation", got)
	}
}
