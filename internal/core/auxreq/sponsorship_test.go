package auxreq

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/execctx"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
)

// sponsorshipObserver records the funding decision the worker context carried.
type sponsorshipObserver struct {
	mu   sync.Mutex
	seen []execctx.Sponsorship
}

func (o *sponsorshipObserver) record(decl execctx.Sponsorship) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.seen = append(o.seen, decl)
}

func (o *sponsorshipObserver) snapshot() []execctx.Sponsorship {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]execctx.Sponsorship(nil), o.seen...)
}

// TestBackgroundSponsorshipTravelsWithTheJob proves that the operator's
// funding decision reaches the background execution unchanged.
//
// The worker context is derived from the process root, so an inherited
// context value would never reach the execution: the decision is captured at
// submission and restored for the worker.
func TestBackgroundSponsorshipTravelsWithTheJob(t *testing.T) {
	observer := &sponsorshipObserver{}
	runner := executorRunnerFunc(func(ctx context.Context, _ *aipapi.Call) (aipapi.EventStream, error) {
		decl, ok := execctx.SponsorshipFromContext(ctx)
		if !ok {
			t.Error("the background execution carried no funding decision")
			return nil, aipapi.ErrInvalidCall
		}
		observer.record(decl)
		return aipapi.NewFixedEventStream([]aipapi.Event{
			{Kind: aipapi.EventResponseStarted},
			{Kind: aipapi.EventResponseFinished},
		}), nil
	})
	scheduler, err := NewBackgroundScheduler(context.Background(), func() ExecutorRunner { return runner },
		SchedulerConfig{Workers: 1, QueueCapacity: 2, MaxResults: 2, JobTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = scheduler.Close() }()

	decl := execctx.Sponsorship{
		Mode:                execctx.SponsorshipModeOperatorSponsored,
		SponsorKey:          "memory-team",
		DeclaredSponsorKeys: []string{"memory-team"},
	}
	client := WithSponsorship(scheduler, decl)
	if client == nil {
		t.Fatal("the sponsored background client is nil")
	}
	id, err := client.SubmitCollect(context.Background(), auxiliary.Request{
		Role:        "proxy_memory_phase1",
		Visibility:  "private",
		SessionMode: auxiliary.SessionModeDetached,
		Call: &aipapi.Call{
			Route:    aipapi.RouteIntent{Selector: "backend:aux-model"},
			Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("auxiliary request")}}},
		},
	}, auxiliary.SubmitOptions{CoalesceKey: "proxy-memory/funding/job", Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Await(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	seen := observer.snapshot()
	if len(seen) != 1 {
		t.Fatalf("the funding decision was observed %d times, want exactly one", len(seen))
	}
	if seen[0].Mode != execctx.SponsorshipModeOperatorSponsored {
		t.Fatalf("the funding mode changed in transit: got %q", seen[0].Mode)
	}
	if seen[0].SponsorKey != "memory-team" {
		t.Fatalf("the sponsor key changed in transit: got %q", seen[0].SponsorKey)
	}
	if len(seen[0].DeclaredSponsorKeys) != 1 || seen[0].DeclaredSponsorKeys[0] != "memory-team" {
		t.Fatalf("the declared sponsor keys changed in transit: got %v", seen[0].DeclaredSponsorKeys)
	}
}

// TestBackgroundSponsorshipIsAbsentWithoutADeclaration proves that work
// submitted without an operator declaration carries none: the funding
// decision is never invented by the scheduler.
func TestBackgroundSponsorshipIsAbsentWithoutADeclaration(t *testing.T) {
	observer := &sponsorshipObserver{}
	runner := executorRunnerFunc(func(ctx context.Context, _ *aipapi.Call) (aipapi.EventStream, error) {
		if decl, ok := execctx.SponsorshipFromContext(ctx); ok {
			observer.record(decl)
		}
		return aipapi.NewFixedEventStream([]aipapi.Event{
			{Kind: aipapi.EventResponseStarted},
			{Kind: aipapi.EventResponseFinished},
		}), nil
	})
	scheduler, err := NewBackgroundScheduler(context.Background(), func() ExecutorRunner { return runner },
		SchedulerConfig{Workers: 1, QueueCapacity: 2, MaxResults: 2, JobTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = scheduler.Close() }()

	id, err := scheduler.SubmitCollect(context.Background(), auxiliary.Request{
		Role:        "proxy_memory_phase1",
		Visibility:  "private",
		SessionMode: auxiliary.SessionModeDetached,
		Call: &aipapi.Call{
			Route:    aipapi.RouteIntent{Selector: "backend:aux-model"},
			Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("auxiliary request")}}},
		},
	}, auxiliary.SubmitOptions{CoalesceKey: "proxy-memory/funding/undeclared", Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := scheduler.Await(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if seen := observer.snapshot(); len(seen) != 0 {
		t.Fatalf("the scheduler invented a funding decision: %v", seen)
	}
}

// executorRunnerFunc adapts a function to the ExecutorRunner interface.
type executorRunnerFunc func(context.Context, *aipapi.Call) (aipapi.EventStream, error)

func (f executorRunnerFunc) Execute(ctx context.Context, call *aipapi.Call) (aipapi.EventStream, error) {
	return f(ctx, call)
}

// TestBackgroundSponsorshipCaptureOwnsItsDeclaredKeys proves that the job the
// scheduler captured owns its declared key set. A caller that mutates the set
// it passed in after the submission must not change what the queued job will
// enforce: the captured declaration is the authority the worker consults.
func TestBackgroundSponsorshipCaptureOwnsItsDeclaredKeys(t *testing.T) {
	runner := executorRunnerFunc(func(ctx context.Context, _ *aipapi.Call) (aipapi.EventStream, error) {
		return aipapi.NewFixedEventStream([]aipapi.Event{
			{Kind: aipapi.EventResponseStarted},
			{Kind: aipapi.EventResponseFinished},
		}), nil
	})
	scheduler, err := NewBackgroundScheduler(context.Background(), func() ExecutorRunner { return runner },
		SchedulerConfig{Workers: 1, QueueCapacity: 2, MaxResults: 2, JobTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = scheduler.Close() }()

	declared := []string{"memory-team"}
	decl := execctx.Sponsorship{
		Mode:                execctx.SponsorshipModeOperatorSponsored,
		SponsorKey:          "outside",
		DeclaredSponsorKeys: declared,
	}
	// A reader of the trusted context mutates the declared key set it received
	// before the job is submitted.
	ctx := execctx.WithSponsorship(context.Background(), decl)
	read, ok := execctx.SponsorshipFromContext(ctx)
	if !ok {
		t.Fatal("the declaration is missing from the context")
	}
	read.DeclaredSponsorKeys[0] = "outside"
	id, err := scheduler.SubmitCollect(ctx, auxiliary.Request{
		Role:        "proxy_memory_phase1",
		Visibility:  "private",
		SessionMode: auxiliary.SessionModeDetached,
		Call: &aipapi.Call{
			Route:    aipapi.RouteIntent{Selector: "backend:aux-model"},
			Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("auxiliary request")}}},
		},
	}, auxiliary.SubmitOptions{CoalesceKey: "proxy-memory/funding/capture", Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	scheduler.mu.Lock()
	job := scheduler.jobs[id]
	scheduler.mu.Unlock()
	if job == nil {
		t.Fatal("the captured job is missing")
	}
	if job.sponsorship.SponsorKeyDeclared() {
		t.Fatal("mutating the caller's key set changed the captured declaration from refused to admitted")
	}
	if len(job.sponsorship.DeclaredSponsorKeys) != 1 || job.sponsorship.DeclaredSponsorKeys[0] != "memory-team" {
		t.Fatalf("the captured declared key set = %v, want the submitted one", job.sponsorship.DeclaredSponsorKeys)
	}
}
