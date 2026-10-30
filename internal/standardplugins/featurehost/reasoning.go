package featurehost

import (
	"github.com/aiproxer/aiproxer/internal/standardplugins/featurehost/reasoning"
)

// ReasoningCompressionOptions is aliased from reasoning for generation options.
type ReasoningCompressionOptions = reasoning.Options

// ReasoningGenerationInput carries generation inputs for reasoning composition.
type ReasoningGenerationInput = reasoning.GenerationInput

// composeReasoningOptions merges production and testing reasoning options.
// It is package-private: only Runtime.CompileGeneration may merge reasoning
// policy, so generic runtimebundle has no separate reasoning entry point
// (Task 2.4, Requirements 8.3/8.4).
func composeReasoningOptions(prod, test ReasoningCompressionOptions) ReasoningCompressionOptions {
	return reasoning.ComposeOptions(prod, test)
}
