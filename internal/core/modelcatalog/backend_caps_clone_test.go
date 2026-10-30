package modelcatalog_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/modelcatalog"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestCloneBackendCaps_nilAndCopy(t *testing.T) {
	t.Parallel()
	if modelcatalog.CloneBackendCaps(nil) != nil {
		t.Fatal("nil in should be nil out")
	}
	orig := aipapi.BackendCaps{
		aipapi.CapabilityTools: {},
	}
	cp := modelcatalog.CloneBackendCaps(orig)
	delete(cp, aipapi.CapabilityTools)
	if _, ok := orig[aipapi.CapabilityTools]; !ok {
		t.Fatal("mutating clone should not affect original map")
	}
}
