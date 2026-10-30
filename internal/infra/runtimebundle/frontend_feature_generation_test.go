package runtimebundle_test

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/infra/runtimehost"
	"github.com/aiproxer/aiproxer/internal/stdhttp"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	aipplugin "github.com/aiproxer/aiproxer/pkg/aipsdk/plugin"
)

type failingLifecycle struct {
	starts, stops atomic.Int32
	err           error
}

func (f *failingLifecycle) Start(context.Context) error {
	f.starts.Add(1)
	return f.err
}

func (f *failingLifecycle) Stop(context.Context) error {
	f.stops.Add(1)
	return nil
}
func (f *failingLifecycle) SafeUnderCandidateOverlap() bool { return true }

func TestCompileGeneration_RouteConflictRollsBackBeforePublication(t *testing.T) {
	t.Parallel()
	ps := newProcessForGeneration(t)
	cand := stubCandidateConfig(t, "rc", "route-conflict", "rc:stub-default", []config.PluginConfig{
		{ID: "openai-responses", Enabled: true},
		// Same factory mounts /v1/responses again → ServeMux conflict.
		{ID: "responses-dup", Kind: "openai-responses", Enabled: true},
	})

	_, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process:   ps,
		Candidate: cand,
		Compose:   stdhttp.ComposeStandardHTTP,
	})
	if err == nil {
		t.Fatal("expected route conflict rejection")
	}
	if !errors.Is(err, stdhttp.ErrRouteConflict) && !strings.Contains(err.Error(), "route conflict") {
		t.Fatalf("want route conflict, got %v", err)
	}
	if ps.Closed() {
		t.Fatal("process services must remain open after candidate route conflict")
	}

	ok, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps,
		Candidate: stubCandidateConfig(t, "rc-ok", "ok-text", "rc-ok:stub-default", []config.PluginConfig{
			{ID: "openai-responses", Enabled: true},
		}),
		Compose: stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatalf("recompile after conflict rollback: %v", err)
	}
	t.Cleanup(func() { _ = ok.Close() })
	body := postResponses(t, ok.Handler(), "stub-default")
	if !strings.Contains(body, "ok-text") {
		t.Fatalf("body=%s", body)
	}
}

func TestCompileGeneration_FeatureUniquenessRejectsSecretsGuardDuplicates(t *testing.T) {
	t.Parallel()
	ps := newProcessForGeneration(t)
	cand := stubCandidateConfig(t, "uniq", "u", "uniq:stub-default", []config.PluginConfig{
		{ID: "openai-responses", Enabled: true},
	})
	cand.Plugins.Features = []config.PluginConfig{
		{ID: "sg-a", Kind: "secrets-guard", Enabled: true, Config: genYAMLNode(t, "action: log\n")},
		{ID: "sg-b", Kind: "secrets-guard", Enabled: true, Config: genYAMLNode(t, "action: redact\n")},
	}
	if err := config.Validate(cand); err != nil {
		t.Fatalf("validate: %v", err)
	}

	_, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process:   ps,
		Candidate: cand,
		Compose:   stdhttp.ComposeStandardHTTP,
	})
	if err == nil {
		t.Fatal("expected feature uniqueness rejection")
	}
	if !strings.Contains(err.Error(), "secrets-guard") {
		t.Fatalf("err=%v", err)
	}
	for _, leak := range []string{"action", "log", "redact"} {
		if strings.Contains(err.Error(), leak) {
			t.Fatalf("error leaked %q: %v", leak, err)
		}
	}
	if ps.Closed() {
		t.Fatal("process closed on feature uniqueness failure")
	}
}

func TestCompileGeneration_LifecycleStartFailureRollsBackOnce(t *testing.T) {
	t.Parallel()
	life := &failingLifecycle{err: errors.New("lifecycle start boom")}
	ps := newProcessForGeneration(t)

	_, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps,
		Candidate: stubCandidateConfig(t, "life-fail", "x", "life-fail:stub-default", []config.PluginConfig{
			{ID: "openai-responses", Enabled: true},
		}),
		CandidateOpts: &runtimebundle.BuildOptions{
			FeatureLifecycles: []aipplugin.Lifecycle{life},
		},
		Compose: stdhttp.ComposeStandardHTTP,
	})
	if err == nil {
		t.Fatal("expected lifecycle start failure")
	}
	if !strings.Contains(err.Error(), "ledger start failed") || strings.Contains(err.Error(), "lifecycle start boom") {
		t.Fatalf("err=%v", err)
	}
	if got := life.starts.Load(); got != 1 {
		t.Fatalf("starts=%d want 1", got)
	}
	if got := life.stops.Load(); got != 1 {
		t.Fatalf("stops=%d want 1 (rollback exactly once)", got)
	}
	if ps.Closed() {
		t.Fatal("process closed on lifecycle start failure")
	}

	okLife := &overlapLife{}
	ok, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps,
		Candidate: stubCandidateConfig(t, "life-ok", "ok", "life-ok:stub-default", []config.PluginConfig{
			{ID: "openai-responses", Enabled: true},
		}),
		CandidateOpts: &runtimebundle.BuildOptions{
			FeatureLifecycles: []aipplugin.Lifecycle{okLife},
		},
		Compose: stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatalf("recompile after lifecycle rollback: %v", err)
	}
	t.Cleanup(func() { _ = ok.Close() })
	if okLife.starts.Load() != 1 {
		t.Fatalf("ok lifecycle starts=%d", okLife.starts.Load())
	}
}

func TestCompileGeneration_FrontendFeatureCoexistAcrossPublish(t *testing.T) {
	t.Parallel()
	ps := newProcessForGeneration(t)

	oldCfg := stubCandidateConfig(t, "old-be", "gen-OLD", "old-be:stub-default", []config.PluginConfig{
		{ID: "openai-responses", Enabled: true},
		{ID: "openai-legacy", Enabled: false},
	})
	newCfg := stubCandidateConfig(t, "new-be", "gen-NEW", "new-be:stub-default", []config.PluginConfig{
		{ID: "openai-responses", Enabled: true},
		{ID: "anthropic", Enabled: true},
	})

	oldBundle, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process:   ps,
		Candidate: oldCfg,
		Compose:   stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatalf("compile old: %v", err)
	}
	newBundle, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process:   ps,
		Candidate: newCfg,
		Compose:   stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatalf("compile new: %v", err)
	}

	mgr := runtimehost.NewManager(4, nil)
	disp := runtimehost.NewGenerationDispatcher(mgr)
	oldGen := mgr.PrepareRequestPlane("old", oldBundle)
	if err := mgr.Publish(oldGen); err != nil {
		t.Fatalf("publish old: %v", err)
	}

	// Hold an old-generation lease across the swap so coexistence is observable.
	oldLease, ok := mgr.Acquire()
	if !ok {
		t.Fatal("acquire old")
	}
	defer oldLease.Release()

	newGen := mgr.PrepareRequestPlane("new", newBundle)
	if err := mgr.Publish(newGen); err != nil {
		t.Fatalf("publish new: %v", err)
	}

	bodyOld := postResponses(t, oldLease.Handler(), "stub-default")
	if !strings.Contains(bodyOld, "gen-OLD") || strings.Contains(bodyOld, "gen-NEW") {
		t.Fatalf("old lease leaked new plane: %s", bodyOld)
	}

	bodyNew := postResponses(t, disp, "stub-default")
	if !strings.Contains(bodyNew, "gen-NEW") || strings.Contains(bodyNew, "gen-OLD") {
		t.Fatalf("dispatcher after publish must use new plane: %s", bodyNew)
	}

	// Anthropic path exists only on the new generation handler.
	rr := httptest.NewRecorder()
	disp.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`)))
	if rr.Code == http.StatusNotFound {
		t.Fatal("new generation must expose anthropic route")
	}
	rrOld := httptest.NewRecorder()
	oldLease.Handler().ServeHTTP(rrOld, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`)))
	if rrOld.Code != http.StatusNotFound {
		t.Fatalf("old generation must not expose anthropic route: %d", rrOld.Code)
	}

	if ps.Closed() {
		t.Fatal("process must remain open while generations coexist")
	}
}

func TestCompileGeneration_ManagementRoutesOutsideSwappableGraph(t *testing.T) {
	t.Parallel()
	ps := newProcessForGeneration(t)
	bundle, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps,
		Candidate: stubCandidateConfig(t, "mgmt", "m", "mgmt:stub-default", []config.PluginConfig{
			{ID: "openai-responses", Enabled: true},
		}),
		Compose: stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatalf("CompileGeneration: %v", err)
	}
	t.Cleanup(func() { _ = bundle.Close() })

	// Startup-fixed management paths (req 12.1/12.3) must not appear on the
	// swappable request-plane handler; they stay process-owned.
	for _, path := range []string{"/admin/config/reload", "/admin/config/status"} {
		rr := httptest.NewRecorder()
		bundle.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
		if rr.Code != http.StatusNotFound {
			t.Fatalf("swappable request plane must not own %s: status=%d", path, rr.Code)
		}
	}

	mgr := runtimehost.NewManager(2, nil)
	if err := mgr.Publish(mgr.PrepareRequestPlane("g1", bundle)); err != nil {
		t.Fatalf("publish: %v", err)
	}
	next, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps,
		Candidate: stubCandidateConfig(t, "mgmt2", "m2", "mgmt2:stub-default", []config.PluginConfig{
			{ID: "openai-responses", Enabled: true},
			{ID: "openai-legacy", Enabled: true},
		}),
		Compose: stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatalf("compile next: %v", err)
	}
	t.Cleanup(func() { _ = next.Close() })
	if err := mgr.Publish(mgr.PrepareRequestPlane("g2", next)); err != nil {
		t.Fatalf("publish next: %v", err)
	}

	// After swap, still no management routes on the data-plane dispatcher.
	disp := runtimehost.NewGenerationDispatcher(mgr)
	for _, path := range []string{"/admin/config/reload", "/admin/config/status"} {
		rr := httptest.NewRecorder()
		disp.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
		if rr.Code != http.StatusNotFound {
			t.Fatalf("dispatcher must not own %s after swap: status=%d", path, rr.Code)
		}
	}
}

func TestCompileGeneration_AuthRendererRebuildPerCandidate(t *testing.T) {
	t.Parallel()
	ps := newProcessForGeneration(t)

	var seen int
	composeProbe := func(ctx context.Context, cfg *config.Config, log *slog.Logger, in stdhttp.StandardHTTPInput) (http.Handler, error) {
		seen++
		_ = in.Security.HTTPAuthProviders // auth renderers/providers rebuilt per candidate
		return stdhttp.ComposeStandardHTTP(ctx, cfg, log, in)
	}

	a, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps,
		Candidate: stubCandidateConfig(t, "auth-a", "a", "auth-a:stub-default", []config.PluginConfig{
			{ID: "openai-responses", Enabled: true},
		}),
		Compose: composeProbe,
	})
	if err != nil {
		t.Fatalf("compile A: %v", err)
	}
	t.Cleanup(func() { _ = a.Close() })

	b, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps,
		Candidate: stubCandidateConfig(t, "auth-b", "b", "auth-b:stub-default", []config.PluginConfig{
			{ID: "openai-responses", Enabled: true},
		}),
		Compose: composeProbe,
	})
	if err != nil {
		t.Fatalf("compile B: %v", err)
	}
	t.Cleanup(func() { _ = b.Close() })

	if seen != 2 {
		t.Fatalf("expected two compose probes, got %d", seen)
	}
	bodyA := postResponses(t, a.Handler(), "stub-default")
	bodyB := postResponses(t, b.Handler(), "stub-default")
	if !strings.Contains(bodyA, "a") || !strings.Contains(bodyB, "b") {
		t.Fatalf("handlers not generation-specific: A=%s B=%s", bodyA, bodyB)
	}
	if strings.Contains(bodyA, `"text":"b"`) || strings.Contains(bodyB, `"text":"a"`) {
		t.Fatalf("handler/backend leak across candidates: A=%s B=%s", bodyA, bodyB)
	}
}

func TestCompileGeneration_ComposePanicRollsBackCandidate(t *testing.T) {
	t.Parallel()
	ps := newProcessForGeneration(t)
	_, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps,
		Candidate: stubCandidateConfig(t, "panic", "x", "panic:stub-default", []config.PluginConfig{
			{ID: "openai-responses", Enabled: true},
		}),
		Compose: func(context.Context, *config.Config, *slog.Logger, stdhttp.StandardHTTPInput) (http.Handler, error) {
			panic("compose boom")
		},
	})
	if err == nil {
		t.Fatal("expected compose panic isolation")
	}
	if !strings.Contains(err.Error(), "compose boom") && !strings.Contains(err.Error(), "panic") {
		t.Fatalf("err=%v", err)
	}
	if ps.Closed() {
		t.Fatal("process closed on compose panic")
	}
}

func TestCompileGeneration_CustomFrontendRouteConflictWithDiagnostics(t *testing.T) {
	t.Parallel()
	reg := stdFactoryCatalog(t)
	conflictID := "conflict-frontend-" + strings.ReplaceAll(t.Name(), "/", "-")
	if err := reg.RegisterFrontend(conflictID, func(mux *http.ServeMux, _ aipsdk.FrontendMountOptions) error {
		mux.Handle("/healthz", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	cfg := processBaseConfig()
	if err := config.Validate(cfg); err != nil {
		t.Fatal(err)
	}
	ps, err := runtimebundle.NewProcessServices(context.Background(), runtimebundle.ProcessServicesInput{
		Cfg:  cfg,
		Log:  testkit.DiscardLogger(),
		Opts: &runtimebundle.BuildOptions{PluginRegistry: reg},
		Tracing: runtimebundle.ProcessTracing{
			Shutdown: func(context.Context) error { return nil },
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ps.Close() })

	cand := stubCandidateConfig(t, "cf", "c", "cf:stub-default", []config.PluginConfig{
		{ID: "openai-responses", Enabled: true},
		{ID: conflictID, Enabled: true},
	})
	_, err = runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process:   ps,
		Candidate: cand,
		Compose:   stdhttp.ComposeStandardHTTP,
	})
	if err == nil {
		t.Fatal("expected healthz route conflict with diagnostics")
	}
	if !errors.Is(err, stdhttp.ErrRouteConflict) && !strings.Contains(err.Error(), "route conflict") {
		t.Fatalf("want route conflict, got %v", err)
	}
}
