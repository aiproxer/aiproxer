package bedrock

import (
	"context"
	"testing"
)

func TestNewWithContext_exposesInventoryPrefix(t *testing.T) {
	t.Parallel()

	// Use a canceled context to exercise the construction error path and ensure
	// BackendPrefixes is set even when the Bedrock runtime client is unavailable.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	be := NewWithContext(ctx, Config{Region: "us-east-1"})
	if len(be.BackendPrefixes) != 1 || be.BackendPrefixes[0] != ID {
		t.Fatalf("BackendPrefixes = %#v, want [%q]", be.BackendPrefixes, ID)
	}
}
