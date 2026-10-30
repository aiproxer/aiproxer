package backend

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/promptcache"
)

// PromptCacheResidencySubject is the small setup/observation surface used by
// the residency TCK. Provider implementations keep their own setup semantics;
// the TCK only observes the stable promptcache.Controller contract.
type PromptCacheResidencySubject interface {
	promptcache.Controller
	Issue(targetID promptcache.TargetID, generationID promptcache.GenerationID, renewable bool) (promptcache.Observation, error)
	Observations() []promptcache.Observation
	Evict(handle promptcache.Handle) error
	SetAffinityAvailable(bool)
	Close() error
	UpstreamResident(handle promptcache.Handle) bool
}

// RunPromptCacheResidencyTCK certifies lifecycle, identity, affinity, release,
// generation-close, accounting, and multi-target behavior without a provider.
func RunPromptCacheResidencyTCK(t *testing.T, factory func() PromptCacheResidencySubject) {
	t.Helper()
	t.Run("observation_only_and_multiple_targets", func(t *testing.T) {
		t.Helper()
		subject := factory()
		first, err := subject.Issue("target-a", "generation-a", false)
		if err != nil {
			t.Fatal(err)
		}
		second, err := subject.Issue("target-b", "generation-b", true)
		if err != nil {
			t.Fatal(err)
		}
		if first.TargetID == second.TargetID || first.GenerationID == second.GenerationID {
			t.Fatalf("identities were merged: first=%+v second=%+v", first, second)
		}
		if len(first.Handle) != 0 || len(second.Handle) == 0 {
			t.Fatalf("observation/control identity mismatch: first=%+v second=%+v", first, second)
		}
		if got := subject.Observations(); len(got) != 2 {
			t.Fatalf("observations=%d, want 2", len(got))
		}
	})

	t.Run("renewal_accounting_and_cold_recreation", func(t *testing.T) {
		t.Helper()
		subject := factory()
		observation, err := subject.Issue("renewable", "generation", true)
		if err != nil {
			t.Fatal(err)
		}
		response, err := subject.Renew(context.Background(), promptcache.RenewRequest{Handle: observation.Handle, OperationID: "op-renew"})
		if err != nil {
			t.Fatal(err)
		}
		if response.Result.Status != promptcache.Renewed || response.Accounting == nil {
			t.Fatalf("response=%+v, want renewed result with accounting", response)
		}
		if err := response.Validate(); err != nil {
			t.Fatal(err)
		}
		if err := subject.Evict(observation.Handle); err != nil {
			t.Fatal(err)
		}
		response, err = subject.Renew(context.Background(), promptcache.RenewRequest{Handle: observation.Handle, OperationID: "op-cold"})
		if err != nil {
			t.Fatal(err)
		}
		if response.Result.Status != promptcache.ColdRecreated {
			t.Fatalf("status=%q, want cold_recreated", response.Result.Status)
		}
	})

	t.Run("affinity_release_and_close_are_fail_closed", func(t *testing.T) {
		t.Helper()
		subject := factory()
		observation, err := subject.Issue("target", "generation", true)
		if err != nil {
			t.Fatal(err)
		}
		subject.SetAffinityAvailable(false)
		response, err := subject.Renew(context.Background(), promptcache.RenewRequest{Handle: observation.Handle, OperationID: "op-affinity"})
		if err != nil {
			t.Fatal(err)
		}
		if response.Result.Status != promptcache.Stale {
			t.Fatalf("status=%q, want stale on affinity loss", response.Result.Status)
		}
		subject.SetAffinityAvailable(true)
		if err := subject.Release(context.Background(), promptcache.ReleaseRequest{Handle: observation.Handle}); err != nil {
			t.Fatal(err)
		}
		if err := subject.Release(context.Background(), promptcache.ReleaseRequest{Handle: observation.Handle}); err != nil {
			t.Fatal(err)
		}
		if !subject.UpstreamResident(observation.Handle) {
			t.Fatal("local release deleted simulated upstream residency")
		}
		response, err = subject.Renew(context.Background(), promptcache.RenewRequest{Handle: observation.Handle, OperationID: "op-stale"})
		if err != nil {
			t.Fatal(err)
		}
		if response.Result.Status != promptcache.Stale {
			t.Fatalf("status=%q, want stale after release", response.Result.Status)
		}

		closed := factory()
		fresh, err := closed.Issue("close-target", "generation", true)
		if err != nil {
			t.Fatal(err)
		}
		if err := closed.Close(); err != nil {
			t.Fatal(err)
		}
		response, err = closed.Renew(context.Background(), promptcache.RenewRequest{Handle: fresh.Handle, OperationID: "op-closed"})
		if err != nil {
			t.Fatal(err)
		}
		if response.Result.Status != promptcache.Stale {
			t.Fatalf("status=%q, want stale after close", response.Result.Status)
		}
	})
}

type referenceTarget struct {
	observation promptcache.Observation
	resident    bool
	upstream    bool
}

// ReferenceResidencyController is a bounded in-process controller used by the
// TCK. It intentionally stores only opaque handles and provider-neutral facts.
type ReferenceResidencyController struct {
	mu                sync.Mutex
	instanceID        string
	generationID      string
	nextHandle        uint64
	targets           map[string]*referenceTarget
	affinityAvailable bool
	closed            bool
}

func NewReferenceResidencyController(instanceID, generationID string) *ReferenceResidencyController {
	return &ReferenceResidencyController{
		instanceID:        instanceID,
		generationID:      generationID,
		targets:           make(map[string]*referenceTarget),
		affinityAvailable: true,
	}
}

func (r *ReferenceResidencyController) Issue(targetID promptcache.TargetID, generationID promptcache.GenerationID, renewable bool) (promptcache.Observation, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return promptcache.Observation{}, promptcache.ErrStaleHandle
	}
	if generationID == "" {
		generationID = promptcache.GenerationID(r.generationID)
	}
	now := time.Unix(1, 0).UTC()
	observation := promptcache.Observation{
		ALegID: "a-leg", BLegID: "b-leg", BackendInstanceID: r.instanceID,
		TargetID: targetID, GenerationID: generationID,
		Lifecycle: promptcache.LifecycleSlidingExpiry,
		Timing:    promptcache.Timing{ObservedAt: now}, Renewable: renewable,
	}
	key := string(targetID) + "\x00" + string(generationID)
	target := &referenceTarget{observation: observation, resident: true, upstream: true}
	if renewable {
		r.nextHandle++
		observation.Handle = promptcache.Handle(fmt.Sprintf("reference-handle-%d", r.nextHandle))
		target.observation = observation
	}
	r.targets[key] = target
	return observation, nil
}

func (r *ReferenceResidencyController) Observations() []promptcache.Observation {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]promptcache.Observation, 0, len(r.targets))
	for _, target := range r.targets {
		out = append(out, target.observation)
	}
	return out
}

func (r *ReferenceResidencyController) Renew(ctx context.Context, req promptcache.RenewRequest) (promptcache.RenewResponse, error) {
	if err := ctx.Err(); err != nil {
		return promptcache.RenewResponse{}, err
	}
	if err := req.Validate(); err != nil {
		return promptcache.RenewResponse{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	var target *referenceTarget
	for _, candidate := range r.targets {
		if string(candidate.observation.Handle) == string(req.Handle) {
			target = candidate
			break
		}
	}
	if target == nil || r.closed || !r.affinityAvailable || target.observation.GenerationID != promptcache.GenerationID(r.generationID) {
		return promptcache.RenewResponse{Result: promptcache.RenewResult{Status: promptcache.Stale}}, nil
	}
	status := promptcache.Renewed
	if !target.resident {
		status = promptcache.ColdRecreated
		target.resident = true
	}
	now := time.Unix(2, 0).UTC()
	observation := target.observation
	observation.Timing.ObservedAt = now
	target.observation = observation
	input := int64(1)
	accounting := &promptcache.AccountingEvidence{InputTokens: &input, Presence: aipapi.UsagePresence{InputTokens: true}, Source: promptcache.AccountingSourceProviderReported, Authority: promptcache.AccountingAuthorityAuthoritative, Plane: promptcache.AccountingPlaneProviderBillable, DedupeKey: "prompt-cache:" + req.OperationID}
	return promptcache.RenewResponse{Result: promptcache.RenewResult{Status: status, Observation: &observation}, Accounting: accounting}, nil
}

func (r *ReferenceResidencyController) Release(ctx context.Context, req promptcache.ReleaseRequest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := req.Validate(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for key, target := range r.targets {
		if string(target.observation.Handle) == string(req.Handle) {
			delete(r.targets, key)
			return nil
		}
	}
	return nil
}

func (r *ReferenceResidencyController) Evict(handle promptcache.Handle) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, target := range r.targets {
		if string(target.observation.Handle) == string(handle) {
			target.resident = false
			return nil
		}
	}
	return promptcache.ErrStaleHandle
}

func (r *ReferenceResidencyController) SetAffinityAvailable(available bool) {
	r.mu.Lock()
	r.affinityAvailable = available
	r.mu.Unlock()
}

func (r *ReferenceResidencyController) Close() error {
	r.mu.Lock()
	r.closed = true
	r.targets = make(map[string]*referenceTarget)
	r.mu.Unlock()
	return nil
}

func (r *ReferenceResidencyController) UpstreamResident(handle promptcache.Handle) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, target := range r.targets {
		if string(target.observation.Handle) == string(handle) {
			return target.upstream
		}
	}
	// Local release removes the volatile target but leaves simulated upstream state.
	return true
}
