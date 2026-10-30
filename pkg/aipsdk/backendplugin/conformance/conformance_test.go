package conformance_test

import (
	"context"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/testkit/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin/conformance"
)

func TestConformance_ValidFakePasses(t *testing.T) {
	t.Parallel()
	svc := &backendplugin.FakeService{Mode: backendplugin.ModeValid}
	rep := conformance.Run(context.Background(), svc)
	if !rep.Ok() {
		t.Fatalf("failures=%v", rep.Failures())
	}
}

func TestConformance_BrokenModesExactModeError(t *testing.T) {
	t.Parallel()
	cases := []struct {
		mode backendplugin.Mode
		code string
	}{
		{backendplugin.ModeUnauthorizedPeer, backendplugin.DiagUnauthorizedPeer},
		{backendplugin.ModeShutdown, backendplugin.DiagShutdown},
		{backendplugin.ModeProcessExit, backendplugin.DiagProcessExit},
		{backendplugin.ModeBlockedCancel, backendplugin.DiagBlockedCancel},
		{backendplugin.ModeDuplicateTerminal, backendplugin.DiagDuplicateTerminal},
		{backendplugin.ModeMalformedFrame, backendplugin.DiagMalformedFrame},
		{backendplugin.ModeOversize, backendplugin.DiagOversize},
	}
	for _, tc := range cases {
		t.Run(string(tc.mode), func(t *testing.T) {
			t.Parallel()
			svc := &backendplugin.FakeService{Mode: tc.mode}
			res := conformance.RunBrokenMode(context.Background(), svc, tc.code)
			if !res.Passed {
				t.Fatalf("%+v", res)
			}
		})
	}
}

func TestConformance_SlowOutputDeadline(t *testing.T) {
	t.Parallel()
	svc := &backendplugin.FakeService{Mode: backendplugin.ModeSlowOutput, SlowWait: 2 * time.Second}
	res := conformance.RunSlowModeDeadline(context.Background(), svc, backendplugin.DiagSlowOutput, 30*time.Millisecond)
	if !res.Passed {
		t.Fatalf("%+v", res)
	}
}
