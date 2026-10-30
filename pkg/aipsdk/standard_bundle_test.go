package aipsdk_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

func TestStandardDistributionRequirements_nonEmpty(t *testing.T) {
	t.Parallel()
	req := aipsdk.StandardDistributionRequirements()
	if len(req) < 8 {
		t.Fatalf("expected multiple requirements, got %d", len(req))
	}
}
