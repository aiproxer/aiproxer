package catalog

import (
	_ "embed"
	"fmt"
	"os"
	"strings"
)

//go:embed codex_model_catalog.json
var fallbackSnapshot []byte

// FallbackBytes returns the shipped fallback snapshot bytes (the slimmed
// `codex debug models` output). Callers must not mutate the returned slice.
func FallbackBytes() []byte {
	return fallbackSnapshot
}

// LoadFallback parses the shipped fallback snapshot. If overridePath is
// non-empty, the file at that path is read and parsed instead (same `codex
// debug models` JSON format), so operators can ship a custom catalog.
func LoadFallback(overridePath string) (*Catalog, error) {
	raw := fallbackSnapshot
	if path := strings.TrimSpace(overridePath); path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("codexcatalog: read fallback %q: %w", path, err)
		}
		raw = data
	}
	return Parse(raw)
}
