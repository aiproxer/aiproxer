package featurehost_test

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/accessmode"
	"github.com/aiproxer/aiproxer/internal/standardplugins/featurehost"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

func nilDiscardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestSecretGuard_ValidateRegistrations(t *testing.T) {
	t.Parallel()

	var regs []aipsdk.Registration
	if err := featurehost.ValidateSecretGuardRegistrations(regs); err != nil {
		t.Fatalf("ValidateSecretGuardRegistrations: %v", err)
	}
}

func TestSecretGuard_BuildRuntime(t *testing.T) {
	t.Parallel()

	rt, err := featurehost.NewProcess(context.Background(), featurehost.ProcessInput{
		Logger: nilDiscardLogger(),
	})
	if err != nil {
		t.Fatalf("NewProcess: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close() })

	sg, err := rt.BuildSecretGuardRuntime(featurehost.SecretGuardBuildInput{
		AccessMode: accessmode.ModeSingleUser,
		Logger:     nilDiscardLogger(),
	})
	if err != nil {
		t.Fatalf("BuildSecretGuardRuntime: %v", err)
	}
	if sg == nil {
		t.Fatal("expected non-nil SecretGuardRuntime")
	}
}
