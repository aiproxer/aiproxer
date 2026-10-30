package capabilities

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestMapResolver_dispatchesByBackend(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	call := aipapi.Call{}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Backend: "be1", Model: "m"}}
	m := MapResolver{
		"be1": func(_ context.Context, c routing.AttemptCandidate, _ aipapi.Call) aipapi.BackendCaps {
			if c.Primary.Model != "m" {
				t.Fatalf("model %q", c.Primary.Model)
			}
			return aipapi.NewBackendCaps(aipapi.CapabilityStreaming)
		},
	}
	got := m.DescribeCandidate(ctx, cand, call)
	if _, ok := got[aipapi.CapabilityStreaming]; !ok {
		t.Fatalf("caps %+v", got)
	}
}
