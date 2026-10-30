package runtime_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/policy"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func cbTestNow() time.Time {
	return time.Unix(1715620001, 0).UTC()
}

func TestExecutor_circuitBreakerSkipsAfterFailures(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	cb := policy.NewCircuitBreaker(policy.CircuitBreakerOptions{
		FailureThreshold: 1,
		OpenDuration:     time.Hour,
		Now:              cbTestNow,
	})
	var badOpens int
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(3)
	ex.Now = cbTestNow
	ex.CandidateHealth = cb
	ex.Backends = map[string]execbackend.Backend{
		"bad": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				badOpens++
				return nil, aipapi.RecoverablePreOutputError(errors.New("boom"))
			},
		},
		"ok": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventResponseFinished}}), nil
			},
		},
	}
	call := &aipapi.Call{
		Session: aipapi.SessionRef{ContinuityKey: "cb-skip"},
		Route:   aipapi.RouteIntent{Selector: "bad:m|ok:m"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	s, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	if badOpens != 1 {
		t.Fatalf("want one bad open on first request, got %d", badOpens)
	}
	_, _ = aipapi.Collect(context.Background(), s)

	call2 := &aipapi.Call{
		Session: aipapi.SessionRef{ContinuityKey: "cb-skip-2"},
		Route:   aipapi.RouteIntent{Selector: "bad:m|ok:m"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	s2, err := ex.Execute(context.Background(), call2)
	if err != nil {
		t.Fatal(err)
	}
	if badOpens != 1 {
		t.Fatalf("expected bad skipped by circuit on second request, badOpens=%d", badOpens)
	}
	_, _ = aipapi.Collect(context.Background(), s2)
}
