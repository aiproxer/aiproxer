package policy_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/policy"
)

func TestStaticUnhealthy_returnsCopy(t *testing.T) {
	t.Parallel()
	s := policy.StaticUnhealthy{"a": {}, "b": {}}
	m1 := s.UnhealthyCandidateKeys()
	m2 := s.UnhealthyCandidateKeys()
	delete(m1, "a")
	if _, ok := m2["a"]; !ok {
		t.Fatal("second map should be independent copy")
	}
}
