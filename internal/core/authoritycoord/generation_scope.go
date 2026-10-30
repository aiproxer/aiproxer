package authoritycoord

// generationScopedRequestProvider is an opt-in marker for a request provider
// whose evaluator is compiled from the candidate generation. Such providers
// must be selected from the candidate coordinator even when a process-level
// executable snapshot is also bound to the request.
//
// The marker deliberately has no behavior or data surface. It keeps the
// coordinator generic while allowing candidate-local, nonfinancial policy
// evaluators to follow config reloads without weakening the generation pin for
// existing providers.
type generationScopedRequestProvider interface {
	GenerationScopedRequestProvider()
}

// HasGenerationScopedRequestProviders reports whether c contains an evaluator
// compiled from the candidate generation. The coordinator remains immutable
// after construction, so this read is safe during concurrent request admits.
func (c *RequestCoordinator) HasGenerationScopedRequestProviders() bool {
	if c == nil {
		return false
	}
	for _, slot := range c.Slots {
		if _, ok := slot.Provider.(generationScopedRequestProvider); ok {
			return true
		}
	}
	return false
}
