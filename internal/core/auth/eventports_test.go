package auth

import (
	"context"
	"testing"

	sdkauth "github.com/aiproxer/aiproxer/pkg/aipsdk/auth"
)

type stubEventSink struct{}

func (stubEventSink) OnAuthDecision(_ context.Context, _ sdkauth.AuthDecisionEvent) error {
	return nil
}

func (stubEventSink) OnSessionStart(_ context.Context, _ sdkauth.SessionStartEvent) error {
	return nil
}

func TestEventSink_interface(t *testing.T) {
	t.Parallel()
	var _ EventSink = stubEventSink{}
}

func TestEventFailurePolicy_stringValues(t *testing.T) {
	t.Parallel()
	if e, w := string(EventFailureBestEffort), "best_effort"; e != w {
		t.Fatalf("EventFailureBestEffort: got %q", e)
	}
	if e, w := string(EventFailureFailClosed), "fail_closed"; e != w {
		t.Fatalf("EventFailureFailClosed: got %q", e)
	}
}
