package metrics

// Claim table for the strict-provider quirk application counter.
//
// | Test | Claim | Source | Mutant -> result |
// | --- | --- | --- | --- |
// | TestQuirkAppliedProm_CountsBoundedIDs | one Observe per applied quirk increments aip_provider_quirk_applied_total{quirk} for exactly the two counted quirk IDs | task deliverable (pre-approved metric name and quirk label) | delete the Inc -> FAIL |
// | TestQuirkAppliedProm_DropsUnknownIDs | any other ID (including another valid profile quirk such as openai.prompt_cache_key) creates no series, keeping the quirk label bounded | task deliverable 2 (label values must be the bounded quirk-ID set only) | record arbitrary IDs -> FAIL |
// | TestQuirkAppliedProm_NilSafe | a nil prom is a silent no-op, so adapters without metrics stay metric-silent | task deliverable 1 (zero value means no observation) | dereference without guard -> FAIL (panic) |

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/backends/openaicompat"
	"github.com/aiproxer/aiproxer/internal/providerprofiles"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// The Prometheus handle structurally satisfies the adapters' observer seam
// without the metrics package importing provider-specific packages.
var _ openaicompat.QuirkObserver = (*QuirkAppliedProm)(nil)

func TestQuirkAppliedProm_CountsBoundedIDs(t *testing.T) {
	t.Parallel()
	prom := RegisterQuirkAppliedProm(prometheus.NewRegistry())
	prom.ObserveQuirkApplied(providerprofiles.QuirkOpenAIChatReasoningContent)
	prom.ObserveQuirkApplied(providerprofiles.QuirkOpenAIChatReasoningContent)
	prom.ObserveQuirkApplied(providerprofiles.QuirkOpenAIResponsesToolAdjacency)
	if got := testutil.ToFloat64(prom.applied.WithLabelValues(string(providerprofiles.QuirkOpenAIChatReasoningContent))); got != 2 {
		t.Fatalf("chat reasoning quirk count = %v, want 2", got)
	}
	if got := testutil.ToFloat64(prom.applied.WithLabelValues(string(providerprofiles.QuirkOpenAIResponsesToolAdjacency))); got != 1 {
		t.Fatalf("tool adjacency quirk count = %v, want 1", got)
	}
}

func TestQuirkAppliedProm_DropsUnknownIDs(t *testing.T) {
	t.Parallel()
	prom := RegisterQuirkAppliedProm(prometheus.NewRegistry())
	// A valid profile quirk that is not a counted normalization must not mint a series.
	prom.ObserveQuirkApplied(providerprofiles.QuirkOpenAIPromptCacheKey)
	prom.ObserveQuirkApplied(providerprofiles.QuirkID("bogus.quirk"))
	if got := testutil.CollectAndCount(prom.applied, "aip_provider_quirk_applied_total"); got != 0 {
		t.Fatalf("unknown IDs must not create series, got %d", got)
	}
}

func TestQuirkAppliedProm_NilSafe(t *testing.T) {
	t.Parallel()
	var prom *QuirkAppliedProm
	prom.ObserveQuirkApplied(providerprofiles.QuirkOpenAIChatReasoningContent)
}
