package anthropic

import (
	"github.com/aiproxer/aiproxer/internal/plugins/backends/protocols/anthropicmessages"
)

// Re-export the protocol-layer controller so the direct-Anthropic plugin
// surface and its tests keep compiling. The production controller lives in
// the shared anthropicmessages protocol adapter where the backend is built.
type (
	RenewalSnapshot       = anthropicmessages.RenewalSnapshot
	RenewalSystemBlock    = anthropicmessages.RenewalSystemBlock
	RenewalCacheControl   = anthropicmessages.RenewalCacheControl
	RenewalMessage        = anthropicmessages.RenewalMessage
	CacheTarget           = anthropicmessages.CacheTarget
	CacheControllerConfig = anthropicmessages.CacheControllerConfig
	CacheController       = anthropicmessages.CacheController
)

var (
	ErrNoCacheEvidence = anthropicmessages.ErrNoCacheEvidence
	ErrTargetNotFound  = anthropicmessages.ErrTargetNotFound
)

var NewCacheController = anthropicmessages.NewCacheController

// totalPtr is re-exported for the live gate test which builds total tokens
// from input/output evidence.
func totalPtr(a, b *int64) *int64 {
	if a == nil && b == nil {
		return nil
	}
	var total int64
	if a != nil {
		total += *a
	}
	if b != nil {
		total += *b
	}
	return &total
}

type anthropicUsage struct {
	InputTokens              *int64 `json:"input_tokens"`
	OutputTokens             *int64 `json:"output_tokens"`
	CacheCreationInputTokens *int64 `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     *int64 `json:"cache_read_input_tokens"`
}
