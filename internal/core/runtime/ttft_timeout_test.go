package runtime

import (
	"context"
	"errors"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

type ttftBlockingOpenBackend struct {
	openCanceled atomic.Bool
}

func (b *ttftBlockingOpenBackend) backend() execbackend.Backend {
	return execbackend.Backend{
		Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
		Open: func(ctx context.Context, _ aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			<-ctx.Done()
			b.openCanceled.Store(true)
			return nil, ctx.Err()
		},
	}
}

type ttftImmediateBackend struct{}

func (b ttftImmediateBackend) backend() execbackend.Backend {
	return execbackend.Backend{
		Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
		Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			return aipapi.NewFixedEventStream([]aipapi.Event{
				{Kind: aipapi.EventTextDelta, Delta: "ok"},
				{Kind: aipapi.EventResponseFinished},
			}), nil
		},
	}
}

type ttftBlockingRecvStream struct {
	closed atomic.Bool
}

func (s *ttftBlockingRecvStream) Recv(ctx context.Context) (aipapi.Event, error) {
	<-ctx.Done()
	return aipapi.Event{}, ctx.Err()
}

func (s *ttftBlockingRecvStream) Close() error {
	s.closed.Store(true)
	return nil
}

func (s *ttftBlockingRecvStream) Cancel(context.Context, aipapi.CancelCause) aipapi.CancelResult {
	return aipapi.CancelResult{Mode: aipapi.CancelModeCloseOnly}
}

type ttftBlockingRecvBackend struct {
	stream *ttftBlockingRecvStream
}

func (b *ttftBlockingRecvBackend) backend() execbackend.Backend {
	b.stream = &ttftBlockingRecvStream{}
	return execbackend.Backend{
		Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
		Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			return b.stream, nil
		},
	}
}

func ttftTestExecutor(t *testing.T, backends map[string]execbackend.Backend) *Executor {
	t.Helper()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ex := TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Backends = backends
	ex.Rand = routing.NewSeededRng(1)
	return ex
}

func ttftTestCall(selector string) *aipapi.Call {
	return &aipapi.Call{
		Session:  aipapi.SessionRef{ContinuityKey: "ttft-test"},
		Route:    aipapi.RouteIntent{Selector: selector},
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}}},
	}
}

// ttftRemainingClock returns an executor clock backdated so a ttft_timeout=1s
// budget has only `remaining` of real time left. The selector grammar accepts
// whole seconds only, so tests shrink the remaining budget via the clock
// instead of the configured value; the exercised path (real context deadline,
// cancellation, TTFT classification, failover) is unchanged.
func ttftRemainingClock(remaining time.Duration) func() time.Time {
	base := time.Now()
	return func() time.Time { return base.Add(remaining - time.Second) }
}

func TestExecutor_TTFTLeafOpenTimeoutFailsOver(t *testing.T) {
	t.Parallel()
	slow := &ttftBlockingOpenBackend{}
	ex := ttftTestExecutor(t, map[string]execbackend.Backend{
		"slow": slow.backend(),
		"fast": ttftImmediateBackend{}.backend(),
	})
	ex.Now = ttftRemainingClock(100 * time.Millisecond)
	stream, err := ex.Execute(context.Background(), ttftTestCall("[ttft_timeout=1]slow:m|fast:m"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stream.Close() })
	ev, err := stream.Recv(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ev.Kind != aipapi.EventTextDelta || ev.Delta != "ok" {
		t.Fatalf("first event: %#v", ev)
	}
	if !slow.openCanceled.Load() {
		t.Fatal("expected slow backend open context to be canceled")
	}
}

func TestRetryRecvStream_TTFTLeafRecvTimeoutFailsOver(t *testing.T) {
	t.Parallel()
	slow := &ttftBlockingRecvBackend{}
	ex := ttftTestExecutor(t, map[string]execbackend.Backend{
		"slow": slow.backend(),
		"fast": ttftImmediateBackend{}.backend(),
	})
	ex.Now = ttftRemainingClock(100 * time.Millisecond)
	stream, err := ex.Execute(context.Background(), ttftTestCall("[ttft_timeout=1]slow:m|fast:m"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stream.Close() })
	ev, err := stream.Recv(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ev.Kind != aipapi.EventTextDelta || ev.Delta != "ok" {
		t.Fatalf("first event: %#v", ev)
	}
	if slow.stream == nil || !slow.stream.closed.Load() {
		t.Fatal("expected slow stream to be closed after leaf TTFT timeout")
	}
}

func TestExecutor_TTFTGlobalOpenTimeoutStopsAttempts(t *testing.T) {
	t.Parallel()
	slow := &ttftBlockingOpenBackend{}
	ex := ttftTestExecutor(t, map[string]execbackend.Backend{"slow": slow.backend(), "fast": ttftImmediateBackend{}.backend()})
	ex.Now = ttftRemainingClock(100 * time.Millisecond)
	_, err := ex.Execute(context.Background(), ttftTestCall("{ttft_timeout=1}slow:m|fast:m"))
	if !errors.Is(err, aipapi.ErrTTFTTimeout) {
		t.Fatalf("expected ErrTTFTTimeout, got %v", err)
	}
	if !slow.openCanceled.Load() {
		t.Fatal("expected slow backend open context to be canceled")
	}
}

func TestRetryRecvStream_TTFTStopsAfterCommittedOutput(t *testing.T) {
	t.Parallel()
	stream := aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventTextDelta, Delta: "ok"}})
	sel, err := routing.Parse("[ttft_timeout=1]slow:m")
	if err != nil {
		t.Fatal(err)
	}
	ttft := newTTFTBudget(time.Now(), sel)
	s := &retryRecvStream{
		facts: testRecvTurnFacts(recvTurnFacts{
			baseline: *ttftTestCall("[ttft_timeout=1]slow:m"),
			aLegID:   "a1",
			traceID:  "t1",
		}),
		recovery: &recoveryController{budget: &attemptBudget{max: 1}, ttft: ttft, sel: sel, session: &routing.SessionRoutingState{}, excluded: map[string]struct{}{}}, attempt: testAttemptSlot(b2bua.BLegRecord{BLegID: "b1", Seq: 1}, routing.AttemptCandidate{Key: "slow:m", Primary: *sel.Alternatives[0].Primary}, authorityLifecycle{}),
		responsePipeline: newResponsePipeline(),
	}
	testStoreInner(s, stream)
	ev, err := s.Recv(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ev.Kind != aipapi.EventTextDelta {
		t.Fatalf("event: %#v", ev)
	}
	if !ttft.done {
		t.Fatal("expected committed output to mark TTFT budget done")
	}
	_, err = s.Recv(context.Background())
	if !errors.Is(err, io.EOF) {
		t.Fatalf("expected EOF after committed output, got %v", err)
	}
}

func TestTTFTContextDeadlineExpiredDoesNotMaskBackendError(t *testing.T) {
	t.Parallel()
	backendErr := errors.New("backend returned a real error")
	parent := context.Background()
	ctx, cancel := context.WithDeadline(parent, time.Now().Add(-time.Second))
	defer cancel()
	d := ttftContextDeadline{
		scope:    ttftTimeoutLeaf,
		deadline: time.Now().Add(-time.Second),
		parent:   parent,
	}
	if d.expired(ctx, backendErr) {
		t.Fatal("non-deadline backend error must not be classified as TTFT timeout")
	}
	if !d.expired(ctx, context.DeadlineExceeded) {
		t.Fatal("backend deadline error should be classified as TTFT timeout")
	}
}

func TestRetryRecvStream_TTFTLeafDoesNotSwallowParentDeadline(t *testing.T) {
	t.Parallel()
	leaf := 60 * time.Second
	stream := &ttftBlockingRecvStream{}
	sel, err := routing.Parse("[ttft_timeout=60]slow:m")
	if err != nil {
		t.Fatal(err)
	}
	s := &retryRecvStream{
		facts: testRecvTurnFacts(recvTurnFacts{
			baseline: *ttftTestCall("[ttft_timeout=60]slow:m"),
			aLegID:   "a1",
			traceID:  "t1",
		}),
		recovery: &recoveryController{budget: &attemptBudget{max: 1}, ttft: &ttftBudget{start: time.Now()}, sel: sel, session: &routing.SessionRoutingState{}, excluded: map[string]struct{}{}}, attempt: testAttemptSlot(b2bua.BLegRecord{BLegID: "b1", Seq: 1}, routing.AttemptCandidate{
			Key:     "slow:m",
			Primary: routing.Primary{Backend: "slow", Model: "m", TTFTTimeout: &leaf},
		}, authorityLifecycle{}),
		responsePipeline: newResponsePipeline(),
	}
	testStoreInner(s, stream)
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	_, err = s.Recv(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected parent deadline error, got %v", err)
	}
	if errors.Is(err, aipapi.ErrTTFTTimeout) {
		t.Fatalf("parent deadline must not surface as TTFT timeout: %v", err)
	}
	if !stream.closed.Load() {
		t.Fatal("expected stream to be closed after parent deadline")
	}
}

func TestTTFTLeafDeadlineAnchoredPerCandidate(t *testing.T) {
	t.Parallel()
	start := time.Unix(100, 0)
	leaf := time.Second
	b := newTTFTBudget(start, nil)

	first, scope, ok := b.deadline(start, "a:b", &leaf)
	if !ok || scope != ttftTimeoutLeaf || !first.Equal(start.Add(leaf)) {
		t.Fatalf("first deadline=%v scope=%q ok=%v", first, scope, ok)
	}
	second, scope, ok := b.deadline(start.Add(500*time.Millisecond), "a:b", &leaf)
	if !ok || scope != ttftTimeoutLeaf || !second.Equal(first) {
		t.Fatalf("second deadline=%v scope=%q ok=%v want %v", second, scope, ok, first)
	}
	other, scope, ok := b.deadline(start.Add(500*time.Millisecond), "c:d", &leaf)
	if !ok || scope != ttftTimeoutLeaf || !other.Equal(start.Add(1500*time.Millisecond)) {
		t.Fatalf("other deadline=%v scope=%q ok=%v", other, scope, ok)
	}
}

func TestTTFTTimeoutDurationIsSeconds(t *testing.T) {
	t.Parallel()
	sel, err := routing.Parse("{ttft_timeout=1}[ttft_timeout=2]a:b")
	if err != nil {
		t.Fatal(err)
	}
	if sel.GlobalTTFTTimeout == nil || *sel.GlobalTTFTTimeout != time.Second {
		t.Fatalf("global duration: %#v", sel.GlobalTTFTTimeout)
	}
	if sel.Alternatives[0].Primary.TTFTTimeout == nil || *sel.Alternatives[0].Primary.TTFTTimeout != 2*time.Second {
		t.Fatalf("leaf duration: %#v", sel.Alternatives[0].Primary.TTFTTimeout)
	}
}
