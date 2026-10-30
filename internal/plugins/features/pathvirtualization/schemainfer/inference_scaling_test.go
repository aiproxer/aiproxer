package schemainfer_test

// Spec: b-leg-path-virtualization Task 11.1, requirements.md 9.2 and 9.3, and the prior
// finding this feature recorded about a quadratic parse cost in exactly this package:
// a 148-185 KB declared schema chain spent seconds because every declared level
// re-entered the decoder on its own full subtree.
//
// This file is the host-independent half of the standing guard. The existing deep-read
// cost test in this package pins the same property with a wall-clock budget and a
// scale-free deep-versus-wide parity factor; those are the right assertions for a
// one-order-of-magnitude regression, and they are left exactly where they are. What they
// cannot be is a guard that runs identically on a busy CI machine, so this file adds the
// complementary property in a form that has no time in it at all: ALLOCATION COUNTS,
// which are a deterministic function of the code and its input and therefore identical
// on every host and under every timing condition.
//
// The assertion is a RELATIVE one. Comparing the per-member allocation cost at 1000
// declared members against the per-member cost at 10 catches a cost that stopped being
// proportional to the declaration without freezing the absolute number, which would
// break every time the reader's internals legitimately change.
//
// The test does not call t.Parallel, and that is load-bearing rather than an oversight.
// testing.AllocsPerRun pins GOMAXPROCS to one and panics inside a parallel test, and its
// sample counts process-wide allocations, so a concurrent parallel sibling would be
// counted as if it were this test's own. The testing package runs serial top-level tests
// one at a time and releases parallel ones only afterwards, so a serial test here is
// measured on a quiet process.

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization"
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/schemainfer"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// inferenceScalingRuns and inferenceScalingTolerance mirror the guard in
// rewrite/occurrence_scaling_test.go, so the two packages state the same rule with the
// same numbers: a proportional cost passes with a factor near one, and a reintroduced
// quadratic cost would show a factor of about 100 against a bound of two.
const (
	inferenceScalingRuns      = 3
	inferenceScalingTolerance = 2.0
)

// TestInferenceReadAllocationsScaleWithDeclaredMembers pins the requirement 9.2
// bounded-cost claim on the schema read: cost is proportional to the DECLARED SIZE, not
// quadratic in it.
//
// The fixture is one level wide, so every added member is a member the reader has to
// materialize. A reader that re-scanned per node would grow its per-member cost across
// the tiers, which is exactly the shape the quadratic parse cost had. The largest tier
// stays under the step's own node budget so the fixture is refused for its size rather
// than measured for it.
func TestInferenceReadAllocationsScaleWithDeclaredMembers(t *testing.T) {
	inferrer, reject := schemainfer.New(schemainfer.DefaultPathKeys())
	if reject != pathvirtualization.SelectorRejectNone {
		t.Fatalf("New(path keys) reject = %v, want none", reject)
	}

	perMember := make([]float64, 0, len(benchWidthTiers))
	for _, members := range benchWidthTiers {
		tool := aipapi.ToolDef{
			Name:       "synthetic_unclaimed_reader",
			Parameters: benchWideSchema(members),
		}
		result := inferrer.InferArguments(tool)
		if result.Outcome != schemainfer.OutcomeInferred {
			t.Fatalf("declared-members=%d: outcome = %v, want inferred", members, result.Outcome)
		}
		allocs := testing.AllocsPerRun(inferenceScalingRuns, func() {
			benchSinkResult = inferrer.InferArguments(tool)
		})
		t.Logf("declared-members=%4d declared-B=%7d allocs=%7d allocs-per-member=%.3f",
			members, len(tool.Parameters), int(allocs), allocs/float64(members))
		perMember = append(perMember, allocs/float64(members))
	}

	smallest := perMember[0]
	largest := perMember[len(perMember)-1]
	if largest > inferenceScalingTolerance*smallest {
		t.Fatalf("the schema read allocated %.3f objects per declared member at the largest tier and "+
			"%.3f at the smallest: the per-member cost grew by %.1fx, which is not proportional to the "+
			"declared size (requirement 9.2)",
			largest, smallest, largest/smallest)
	}
}
