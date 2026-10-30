package tokenizers

import (
	"fmt"
	"strings"

	accountingapp "github.com/aiproxer/aiproxer/internal/core/tokenaccounting/app"
	tiktokenlocal "github.com/aiproxer/aiproxer/internal/infra/tokenizers/tiktoken"
)

// Supported compatible-mode tokenizer identifiers (requirement 7.2).
var supportedCompatibleTokenizerIDs = map[string]struct{}{
	"cl100k_base": {},
	"o200k_base":  {},
}

// ResolveCompatibleID maps an optional compatible-mode tokenizer identifier to a
// local counter. Empty input preserves current default behavior by returning nil.
func ResolveCompatibleID(id string) (accountingapp.LocalCounter, string, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, "", nil
	}
	normalized, ok := normalizeCompatibleTokenizerID(id)
	if !ok {
		return nil, "", fmt.Errorf("unknown compatible tokenizer %q (supported: cl100k_base, o200k_base)", id)
	}
	counter, err := tiktokenlocal.NewCounter(tiktokenlocal.Config{DefaultEncoding: normalized})
	if err != nil {
		return nil, "", fmt.Errorf("compatible tokenizer %q: %w", normalized, err)
	}
	return counter, normalized, nil
}

func normalizeCompatibleTokenizerID(id string) (string, bool) {
	id = strings.ToLower(strings.TrimSpace(id))
	if _, ok := supportedCompatibleTokenizerIDs[id]; ok {
		return id, true
	}
	return "", false
}
