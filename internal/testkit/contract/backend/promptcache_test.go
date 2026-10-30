package backend

import "testing"

func TestReferencePromptCacheResidencyTCK(t *testing.T) {
	t.Parallel()
	RunPromptCacheResidencyTCK(t, func() PromptCacheResidencySubject {
		return NewReferenceResidencyController("reference-instance", "generation")
	})
}
