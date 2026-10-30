package standardplugins_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/standardplugins"
)

func TestEssentialBackendBundle_NoMigrationKinds(t *testing.T) {
	t.Parallel()
	b := standardplugins.EssentialBackendBundle(standardplugins.UpstreamAPIKeys{})
	if len(b.Backends) != len(standardplugins.EssentialBackendKinds()) {
		t.Fatalf("got %d want %d", len(b.Backends), len(standardplugins.EssentialBackendKinds()))
	}
	for _, e := range b.Backends {
		if !standardplugins.IsEssentialBackendKind(e.ID) {
			t.Fatalf("unexpected %q", e.ID)
		}
	}
}
