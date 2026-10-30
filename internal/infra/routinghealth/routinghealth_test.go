package routinghealth_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/infra/routinghealth"
)

func TestEmpty_nonNil(t *testing.T) {
	t.Parallel()
	h := routinghealth.Empty()
	if h == nil {
		t.Fatal("expected non-nil")
	}
	if h.UnhealthyCandidateKeys() != nil {
		t.Fatalf("expected no unhealthy keys, got %v", h.UnhealthyCandidateKeys())
	}
}

func TestStaticKeys_excludesEmpty(t *testing.T) {
	t.Parallel()
	h := routinghealth.StaticKeys("", "  ", "x:y")
	m := h.UnhealthyCandidateKeys()
	if len(m) != 1 {
		t.Fatalf("want 1 key, got %v", m)
	}
	if _, ok := m["x:y"]; !ok {
		t.Fatal("missing x:y")
	}
}
