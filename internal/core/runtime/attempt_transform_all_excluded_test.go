package runtime_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
)

type excludeAllTransform struct {
	reason string
	calls  *atomic.Int64
}

func (e *excludeAllTransform) ID() string                      { return "exclude-all-xform" }
func (*excludeAllTransform) Order() int                        { return 0 }
func (*excludeAllTransform) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }
func (e *excludeAllTransform) HandleAttempt(_ context.Context, _ *aipapi.Call, _ request.AttemptMeta, _ request.Services) (request.AttemptDecision, error) {
	if e.calls != nil {
		e.calls.Add(1)
	}
	return request.AttemptDecision{Kind: request.AttemptExcludeCandidate, ReasonCode: e.reason}, nil
}

func streamingNoOpenBackend(opens *atomic.Int64) execbackend.Backend {
	return execbackend.Backend{
		Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
		Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			opens.Add(1)
			return nil, errors.New("must not open")
		},
	}
}

func TestCandidateAttemptTransform_allExcluded_sequentialUnrepresentable(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	calls := &atomic.Int64{}
	bundle := contributeAttemptTransformBundle(t, &excludeAllTransform{reason: "unrepresentable_replay", calls: calls})
	bus, snap := wireMergedAttemptSurface(t, bundle)
	var opens atomic.Int64
	ex := runtime.TestExecutor()
	ex.Store, ex.Bus, ex.RuntimeSnapshot = st, bus, snap
	ex.MaxAttempts, ex.Rand = 4, routing.NewSeededRng(3)
	ex.Backends = map[string]execbackend.Backend{
		"a": streamingNoOpenBackend(&opens),
		"b": streamingNoOpenBackend(&opens),
	}
	_, execErr := ex.Execute(t.Context(), attemptTransformBaseCall("a:m|b:m"))
	if !errors.Is(execErr, aipapi.ErrAllCandidatesUnrepresentableReplay) {
		t.Fatalf("want ErrAllCandidatesUnrepresentableReplay, got %v", execErr)
	}
	if opens.Load() != 0 {
		t.Fatalf("opens=%d", opens.Load())
	}
	if calls.Load() < 2 {
		t.Fatalf("transform calls=%d want >=2", calls.Load())
	}
}

func TestCandidateAttemptTransform_allExcluded_sequentialGeneric(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	bundle := contributeAttemptTransformBundle(t, &excludeAllTransform{reason: "plugin_local_reason", calls: &atomic.Int64{}})
	bus, snap := wireMergedAttemptSurface(t, bundle)
	var opens atomic.Int64
	ex := runtime.TestExecutor()
	ex.Store, ex.Bus, ex.RuntimeSnapshot = st, bus, snap
	ex.MaxAttempts, ex.Rand = 4, routing.NewSeededRng(3)
	ex.Backends = map[string]execbackend.Backend{
		"a": streamingNoOpenBackend(&opens),
		"b": streamingNoOpenBackend(&opens),
	}
	_, execErr := ex.Execute(t.Context(), attemptTransformBaseCall("a:m|b:m"))
	if !errors.Is(execErr, aipapi.ErrAllCandidatesExcluded) {
		t.Fatalf("want ErrAllCandidatesExcluded, got %v", execErr)
	}
	if opens.Load() != 0 {
		t.Fatalf("opens=%d", opens.Load())
	}
	if strings.Contains(execErr.Error(), "plugin_local_reason") {
		t.Fatalf("aggregate must not leak participant reason: %v", execErr)
	}
}

func TestCandidateAttemptTransform_allExcluded_weighted(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	bundle := contributeAttemptTransformBundle(t, &excludeAllTransform{reason: "unrepresentable_replay", calls: &atomic.Int64{}})
	bus, snap := wireMergedAttemptSurface(t, bundle)
	var opens atomic.Int64
	ex := runtime.TestExecutor()
	ex.Store, ex.Bus, ex.RuntimeSnapshot = st, bus, snap
	ex.MaxAttempts, ex.Rand = 6, routing.NewSeededRng(7)
	ex.Backends = map[string]execbackend.Backend{
		"a": streamingNoOpenBackend(&opens),
		"b": streamingNoOpenBackend(&opens),
	}
	_, execErr := ex.Execute(t.Context(), attemptTransformBaseCall("[first]a:m^b:m"))
	if !errors.Is(execErr, aipapi.ErrAllCandidatesUnrepresentableReplay) {
		t.Fatalf("want ErrAllCandidatesUnrepresentableReplay, got %v", execErr)
	}
	if opens.Load() != 0 {
		t.Fatalf("opens=%d", opens.Load())
	}
}

func TestCandidateAttemptTransform_allExcluded_parallel(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	bundle := contributeAttemptTransformBundle(t, &excludeAllTransform{reason: "unrepresentable_replay", calls: &atomic.Int64{}})
	bus, snap := wireMergedAttemptSurface(t, bundle)
	var opens atomic.Int64
	ex := runtime.TestExecutor()
	ex.Store, ex.Bus, ex.RuntimeSnapshot = st, bus, snap
	ex.MaxAttempts, ex.Rand = 4, routing.NewSeededRng(5)
	ex.Backends = map[string]execbackend.Backend{
		"a": streamingNoOpenBackend(&opens),
		"b": streamingNoOpenBackend(&opens),
	}
	_, execErr := ex.Execute(t.Context(), attemptTransformBaseCall("a:m!b:m"))
	if !errors.Is(execErr, aipapi.ErrAllCandidatesUnrepresentableReplay) {
		t.Fatalf("want ErrAllCandidatesUnrepresentableReplay, got %v", execErr)
	}
	if opens.Load() != 0 {
		t.Fatalf("opens=%d", opens.Load())
	}
}
