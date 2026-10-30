package compatmode

import (
	"fmt"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/infra/tokenizers"
)

// ApplyRuntimePolicy attaches optional per-instance tokenizer metadata.
func ApplyRuntimePolicy(be execbackend.Backend, cfg config.CompatibleModeConfig) (execbackend.Backend, error) {
	counter, id, err := tokenizers.ResolveCompatibleID(cfg.TokenizerID)
	if err != nil {
		return execbackend.Backend{}, fmt.Errorf("compatible runtime policy: %w", err)
	}
	if counter != nil {
		be.LocalCounter = counter
		be.TokenizerID = id
	}
	return be, nil
}
