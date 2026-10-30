package extractor

const (
	// DefaultMaxInputTokens mirrors the validated extractor.max_input_tokens
	// policy captured by the feature composition layer.
	DefaultMaxInputTokens = 12_000

	// hardMaxInputBytes is an independent provider-neutral safety ceiling. The
	// configured token-equivalent bound is checked first for normal inputs; this
	// ceiling still protects a permissive configuration from giant prompts.
	hardMaxInputBytes = 4 << 20
)
