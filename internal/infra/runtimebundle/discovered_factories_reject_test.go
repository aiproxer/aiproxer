package runtimebundle

import (
	"context"
	"testing"
)

// A failed dial must hand back a true nil session, not a typed nil.
// buildDiscoveredPhysical disposes the rejected session through a
// nil-interface comparison; a typed-nil session passes that comparison and
// panics on Close, turning a fail-closed Configure rejection (for example a
// forbidden inline api_key field) into a host crash.
func TestDefaultDialSession_FailureYieldsNilSession(t *testing.T) {
	t.Parallel()
	sess, _, err := defaultDialSession(context.Background(), DialSessionRequest{})
	if err == nil {
		t.Fatal("expected an error dialing a nil connection")
	}
	if sess != nil {
		t.Fatalf("failed dial returned a non-nil session %T; the rejection path would panic on Close", sess)
	}
}
