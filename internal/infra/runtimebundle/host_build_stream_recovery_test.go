package runtimebundle_test

import (
	"context"
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/stdhttp"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	sdkreload "github.com/aiproxer/aiproxer/pkg/aipsdk/configreload"
)

func TestBuildHost_UsesStartupFixedStreamRecoverySnapshot(t *testing.T) {
	t.Setenv("AIP_AUTO_RESUME", "true")
	t.Setenv("AIP_AUTO_RESUME_IDLE_TIMEOUT", "12s")

	cfgPath := runtimebundle.MaterializeExampleConfigForTest(t, filepath.Join("..", "..", "..", "config", "examples", "dogfood-local-stub.yaml"))
	compose := stdhttp.ComposeStandardHTTP
	host, err := runtimebundle.BuildHost(context.Background(), runtimebundle.BuildHostInput{
		ConfigPath:      cfgPath,
		Mandatory:       aipsdk.StandardDistributionRequirements(),
		LogWriter:       io.Discard,
		HandlerComposer: compose,
	})
	if err != nil {
		t.Fatalf("BuildHost: %v", err)
	}
	t.Cleanup(func() { _ = host.Close(context.Background()) })

	if runtimebundle.HostFixedStreamRecovery(host).EnvIdleTimeout != 12*time.Second {
		t.Fatalf("BuildHost snapshot idle=%v want 12s", runtimebundle.HostFixedStreamRecovery(host).EnvIdleTimeout)
	}
	enabled := runtimebundle.HostFixedStreamRecovery(host).EnvEnabled
	if enabled == nil || !*enabled {
		t.Fatalf("BuildHost snapshot enabled=%v want true", enabled)
	}
	// Mutate process env after BuildHost. Reload must not reread these; it
	// reuses the startup-fixed snapshot captured exactly once at BuildHost.
	t.Setenv("AIP_AUTO_RESUME", "not-a-bool")
	t.Setenv("AIP_AUTO_RESUME_IDLE_TIMEOUT", "99s")

	// No-op reload still exercises the fixed effective loader with the startup snapshot.
	result := host.Reload(context.Background(), sdkreload.Trigger{
		Kind:       sdkreload.TriggerAPI,
		AcceptedAt: time.Now().UTC(),
		SafeActor:  "test",
	})
	if result.Category == sdkreload.ResultInvalid || result.Category == sdkreload.ResultInternalFailed {
		t.Fatalf("reload failed under mutated env: category=%s reason=%s", result.Category, result.ReasonCategory)
	}

	// Snapshot retained on the Host must remain the startup-fixed values.
	if runtimebundle.HostFixedStreamRecovery(host).EnvIdleTimeout != 12*time.Second {
		t.Fatalf("snapshot mutated: idle=%v", runtimebundle.HostFixedStreamRecovery(host).EnvIdleTimeout)
	}
	// Prove a fresh env read would differ / fail — BuildHost must not have depended on it.
	if _, err := config.StreamRecoveryOverridesFromEnv(); err == nil {
		t.Fatal("expected env parse failure after mutation; test setup broken")
	}
}
