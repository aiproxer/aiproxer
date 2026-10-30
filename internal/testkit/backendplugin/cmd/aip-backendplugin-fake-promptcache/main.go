// Command aip-backendplugin-fake-promptcache is the test capability variant of
// the deterministic fake backendplugin that advertises prompt-cache residency
// and, on a successful execute, emits one renewable prompt-cache observation
// plus a finished OS-command tool event. It lets an Enterprise integration test
// drive real keep-warm provider-maintenance accounting through the ordinary
// runtime path (discovered connector -> host backend -> runtime commit -> keep-
// warm hook) instead of wiring featurehost by hand.
package main

import (
	fakebp "github.com/aiproxer/aiproxer/internal/testkit/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

func main() {
	backendplugin.Main("aip-backendplugin-fake-promptcache", &fakebp.FakeService{
		Mode:                  fakebp.ModeValid,
		PromptCache:           true,
		PromptCacheActivation: true,
		CanonicalModelID:      "fake/fake-model",
		NativeModelID:         "fake-model",
	})
}
