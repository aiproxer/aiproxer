package testkit

import (
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/modelcatalog"
	"github.com/aiproxer/aiproxer/internal/infra/modelcatalog/modelsdev"
)

// ModelsDevCatalogSnapshot parses models.dev-shaped catalog JSON into a core [modelcatalog.Snapshot]
// for tests that need a realistic index without importing the adapter from core packages.
func ModelsDevCatalogSnapshot(t *testing.T, rawJSON string, fetchedAt time.Time) modelcatalog.Snapshot {
	t.Helper()
	s, err := modelsdev.ParseSnapshot([]byte(rawJSON), fetchedAt)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
