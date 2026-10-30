package reasoninge2e_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/testkit/reasoninge2e"
)

func TestResponsesSmokeCases_deterministic(t *testing.T) {
	t.Parallel()
	a := reasoninge2e.ResponsesSmokeCases(0x52E5017E, 8)
	b := reasoninge2e.ResponsesSmokeCases(0x52E5017E, 8)
	if len(a) != 8 || len(b) != 8 {
		t.Fatalf("len a=%d b=%d", len(a), len(b))
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("case %d nondeterministic: %+v vs %+v", i, a[i], b[i])
		}
		if a[i].Trace == "" {
			t.Fatal("trace required for reproducible failure")
		}
	}
}
