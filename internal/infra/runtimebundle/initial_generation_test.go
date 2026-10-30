package runtimebundle_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/infra/runtimehost"
	"github.com/aiproxer/aiproxer/internal/plugins/features/submitnoop"
	"github.com/aiproxer/aiproxer/internal/stdhttp"
	bpkit "github.com/aiproxer/aiproxer/internal/testkit/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	aipplugin "github.com/aiproxer/aiproxer/pkg/aipsdk/plugin"
)

func TestInitialGeneration_BuildHostPublishesGenerationOne(t *testing.T) {
	t.Parallel()
	cfgPath := bpkit.WriteDogfoodLocalStubConfig(t)
	host, err := runtimebundle.BuildHost(context.Background(), runtimebundle.BuildHostInput{
		ConfigPath:      cfgPath,
		Mandatory:       aipsdk.StandardDistributionRequirements(),
		LogWriter:       io.Discard,
		HandlerComposer: stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatalf("BuildHost: %v", err)
	}
	hostServeCleanup(t, host)

	if runtimebundle.HostProcess(host) == nil || runtimebundle.HostManager(host) == nil || runtimebundle.HostManager(host).Active() == nil {
		t.Fatal("expected process services, manager, and initial generation")
	}
	if runtimebundle.HostManager(host).Active().ID() != 1 {
		t.Fatalf("id=%d want 1", runtimebundle.HostManager(host).Active().ID())
	}
	st := runtimebundle.HostManager(host).Active().Status()
	if st.Meta.Label != "startup" || st.Meta.TriggerKind != "startup" {
		t.Fatalf("meta=%+v", st.Meta)
	}
	if st.Meta.PublicFingerprint == "" {
		t.Fatal("expected public fingerprint")
	}
	if st.Lifecycle != runtimehost.GenActive {
		t.Fatalf("lifecycle=%v", st.Lifecycle)
	}

	lease, ok := runtimebundle.HostManager(host).Acquire()
	if !ok || lease.Handler() == nil {
		t.Fatal("expected acquireable generation handler")
	}
	st = lease.Status()
	if st.Meta.ID != 1 || st.Meta.PublicFingerprint == "" {
		lease.Release()
		t.Fatalf("lease status=%+v", st)
	}
	lease.Release()

	d := runtimehost.NewGenerationDispatcher(runtimebundle.HostManager(host))
	rr := httptest.NewRecorder()
	d.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/not-a-route", nil))
	if rr.Code == http.StatusServiceUnavailable {
		t.Fatalf("dispatcher unavailable: %d", rr.Code)
	}
}

func TestInitialGeneration_CompileFailureRollsBackProcessServices(t *testing.T) {
	t.Parallel()
	assertBuildHostPartialCleanupOnComposeFailure(t)
}

func TestBootstrapPartialCleanup_ComposeFailureClosesOwnersOnce(t *testing.T) {
	t.Parallel()
	assertBuildHostPartialCleanupOnComposeFailure(t)
}

func TestInitialGeneration_BuildHostRequiresHandlerComposer(t *testing.T) {
	t.Parallel()
	cfgPath := bpkit.WriteDogfoodLocalStubConfig(t)
	host, err := runtimebundle.BuildHost(context.Background(), runtimebundle.BuildHostInput{
		ConfigPath: cfgPath,
		Mandatory:  aipsdk.StandardDistributionRequirements(),
		LogWriter:  io.Discard,
	})
	if err == nil {
		hostServeCleanup(t, host)
		t.Fatal("expected nil HandlerComposer failure")
	}
	if !strings.Contains(err.Error(), "HandlerComposer") {
		t.Fatalf("error=%v want HandlerComposer requirement", err)
	}
}

func assertBuildHostPartialCleanupOnComposeFailure(t *testing.T) {
	t.Helper()
	cfgPath := bpkit.WriteDogfoodLocalStubConfig(t)
	host, err := runtimebundle.BuildHost(context.Background(), runtimebundle.BuildHostInput{
		ConfigPath: cfgPath,
		Mandatory:  aipsdk.StandardDistributionRequirements(),
		LogWriter:  io.Discard,
		HandlerComposer: func(context.Context, *config.Config, *slog.Logger, stdhttp.StandardHTTPInput) (http.Handler, error) {
			return nil, errors.New("compose boom")
		},
	})
	if err == nil {
		hostServeCleanup(t, host)
		t.Fatal("expected compose failure")
	}
	if host != nil {
		t.Fatal("failed BuildHost must not leave partial ownership (nil Host expected)")
	}
}

func TestInitialGeneration_FeatureLifecycleStartStopOnceNoAppOwnership(t *testing.T) {
	probe := &submitnoop.LifecycleProbe{}
	submitnoop.SetLifecycleProbeFactoryForTest(func() aipplugin.Lifecycle { return probe })
	t.Cleanup(func() { submitnoop.SetLifecycleProbeFactoryForTest(nil) })
	cfgPath := writeLifecycleProbeConfig(t)

	host, err := runtimebundle.BuildHost(context.Background(), runtimebundle.BuildHostInput{
		ConfigPath:      cfgPath,
		Mandatory:       aipsdk.StandardDistributionRequirements(),
		LogWriter:       io.Discard,
		HandlerComposer: stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatalf("BuildHost: %v", err)
	}
	if probe.StartCount() != 1 {
		t.Fatalf("lifecycle starts=%d want 1 (double-register would be 2)", probe.StartCount())
	}
	if probe.StopCount() != 0 {
		t.Fatalf("lifecycle stops=%d want 0 before retire", probe.StopCount())
	}

	if err := host.Close(context.Background()); err != nil {
		t.Fatalf("Host.Close: %v", err)
	}

	if probe.StartCount() != 1 || probe.StopCount() != 1 {
		t.Fatalf("after generation retire: starts=%d stops=%d want 1/1", probe.StartCount(), probe.StopCount())
	}
}

func writeLifecycleProbeConfig(t *testing.T) string {
	t.Helper()
	src, err := os.ReadFile(bpkit.WriteDogfoodLocalStubConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Replace(string(src),
		"    - id: submit-noop\n      enabled: true\n      config: {}",
		"    - id: submit-noop\n      enabled: true\n      config:\n        lifecycle_probe: true",
		1)
	if text == string(src) {
		t.Fatal("failed to inject lifecycle_probe into dogfood config")
	}
	path := filepath.Join(t.TempDir(), "lifecycle-probe.yaml")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
