package execbackend

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/promptcache"
)

func TestEffectivePromptCacheProfile_IsModelAwareAndFailClosed(t *testing.T) {
	t.Parallel()
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "model-b"}}
	be := Backend{ResolvePromptCacheProfile: func(_ context.Context, _ aipapi.Call, c routing.AttemptCandidate) promptcache.Profile {
		if c.Primary.Model == "model-b" {
			return promptcache.Profile{ObservationSupported: true, LifecycleKinds: []promptcache.LifecycleKind{promptcache.LifecycleMinimumResidency}}
		}
		return promptcache.Profile{RenewalSupported: true}
	}}
	got := EffectivePromptCacheProfile(context.Background(), be, aipapi.Call{}, cand)
	if !got.ObservationSupported || got.RenewalSupported || len(got.LifecycleKinds) != 1 {
		t.Fatalf("profile=%+v", got)
	}
	if got := EffectivePromptCacheProfile(context.Background(), Backend{}, aipapi.Call{}, cand); got.ObservationSupported || got.RenewalSupported {
		t.Fatalf("unsupported profile=%+v", got)
	}
}
