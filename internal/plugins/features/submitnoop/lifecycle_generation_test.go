package submitnoop_test

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/submitnoop"
	aipplugin "github.com/aiproxer/aiproxer/pkg/aipsdk/plugin"
)

func TestLifecycleProbe_LifecycleStartStopOnce(t *testing.T) {
	t.Parallel()
	p := &submitnoop.LifecycleProbe{}
	if !p.SafeUnderCandidateOverlap() {
		t.Fatal("submit-noop probe must be overlap-safe for generation replacement")
	}
	if err := p.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if p.StartCount() != 1 || !p.WasStarted() {
		t.Fatalf("starts=%d started=%v", p.StartCount(), p.WasStarted())
	}
	if err := p.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if p.StopCount() != 1 || !p.WasStopped() {
		t.Fatalf("stops=%d stopped=%v", p.StopCount(), p.WasStopped())
	}
}

func TestLifecycleProbe_FactoryOverrideSharesInstanceAcrossGenerationMerges(t *testing.T) {
	t.Parallel()
	shared := &submitnoop.LifecycleProbe{}
	submitnoop.SetLifecycleProbeFactoryForTest(func() aipplugin.Lifecycle { return shared })
	t.Cleanup(func() { submitnoop.SetLifecycleProbeFactoryForTest(nil) })

	a := submitnoop.NewLifecycleProbeForConfig()
	b := submitnoop.NewLifecycleProbeForConfig()
	if a != shared || b != shared {
		t.Fatal("factory override must return the shared lifecycle instance")
	}
}
