package runtime

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/internal/core/routing"
)

// TestItem3_PrepareSelector_DoubleRunDedupe verifies Item 3:
// When wire execution already prepared the selector in ExecuteLargeBody (for billing exposure),
// buildRoutePlan must reuse wp.selector directly instead of running routing.PrepareSelector a second time.
// Both args and outcome are identical before and after.
func TestItem3_PrepareSelector_DoubleRunDedupe(t *testing.T) {
	ex, _, _ := setupTestExecutor(t)

	effectiveModel := "default:gpt-4o"

	// 1. Prepare selector directly with executor's routing configuration (site 1 args)
	directSel, err := routing.PrepareSelector(
		effectiveModel,
		ex.SelectorAliases,
		ex.DefaultBackend,
		ex.BackendExecutionResolver,
		ex.ExecutionCompositionPolicy,
		nil,
	)
	require.NoError(t, err)
	require.NotNil(t, directSel)

	wireFacts := largebody.DefaultTestWireTurnFacts()
	wireFacts.Route.RouteSelector = effectiveModel

	// 2. Build preparedRequest with wp.selector populated (the deduped path)
	wp := &wireAttemptPayload{
		turnFacts: wireFacts,
		selector:  directSel,
	}

	recvFacts := recvTurnFacts{
		traceID:     "trace-dedupe",
		aLegID:      "aleg-dedupe",
		wirePayload: wp,
	}

	prepReq := &preparedRequest{
		recvTurnFacts: recvFacts,
	}

	plan, err := ex.buildRoutePlan(context.Background(), prepReq)
	require.NoError(t, err)
	require.NotNil(t, plan)

	// Invariant 1: buildRoutePlan must reuse the pre-computed selector instance without re-allocating
	assert.Same(t, directSel, plan.sel,
		"buildRoutePlan must reuse wp.selector pointer rather than re-running routing.PrepareSelector")

	// Invariant 2: Outcome parity - selector contents must match PrepareSelector result identically
	assert.Equal(t, directSel.Affinity, plan.sel.Affinity)
	assert.Equal(t, directSel.GlobalTTFTTimeout, plan.sel.GlobalTTFTTimeout)
	require.Equal(t, len(directSel.Alternatives), len(plan.sel.Alternatives))
	assert.Equal(t, *directSel.Alternatives[0].Primary, *plan.routeFacts.sel.Alternatives[0].Primary)

	// 3. Fallback verification: When wp.selector is nil, buildRoutePlan still computes an identical selector
	wpNoSel := &wireAttemptPayload{
		turnFacts: wireFacts,
		selector:  nil,
	}
	prepReqNoSel := &preparedRequest{
		recvTurnFacts: recvTurnFacts{
			traceID:     "trace-dedupe-fallback",
			aLegID:      "aleg-dedupe-fallback",
			wirePayload: wpNoSel,
		},
	}
	planFallback, err := ex.buildRoutePlan(context.Background(), prepReqNoSel)
	require.NoError(t, err)
	require.NotNil(t, planFallback)

	assert.Equal(t, directSel.Affinity, planFallback.sel.Affinity)
	assert.Equal(t, directSel.GlobalTTFTTimeout, planFallback.sel.GlobalTTFTTimeout)
	require.Equal(t, len(directSel.Alternatives), len(planFallback.sel.Alternatives))
	assert.Equal(t, *directSel.Alternatives[0].Primary, *planFallback.routeFacts.sel.Alternatives[0].Primary)
}
