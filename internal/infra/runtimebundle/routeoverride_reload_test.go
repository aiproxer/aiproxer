package runtimebundle_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/infra/runtimehost"
	"github.com/aiproxer/aiproxer/internal/stdhttp"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"gopkg.in/yaml.v3"
)

type reloadRequestPlane struct {
	h http.Handler
}

func (p reloadRequestPlane) Handler() http.Handler         { return p.h }
func (p reloadRequestPlane) Close() error                  { return nil }
func (p reloadRequestPlane) Quiesce(context.Context) error { return nil }

func TestCompileGeneration_putAfterPublishUsesNewGenerationValidator(t *testing.T) {
	t.Parallel()
	const secret = "override-secret-12"
	oldCfg := routeOverrideAdminConfig(secret)
	withRouteOverrideOpenAIBackend(t, oldCfg)
	oldCfg.ModelAliases = []config.ModelAliasConfig{{Pattern: `^cheap\|$`, Replacement: "openai:gpt-4"}}
	ps, err := runtimebundle.NewProcessServices(context.Background(), runtimebundle.ProcessServicesInput{
		Cfg:  oldCfg,
		Log:  testkit.DiscardLogger(),
		Opts: &runtimebundle.BuildOptions{PluginRegistry: generationRegistry(t)},
		Tracing: runtimebundle.ProcessTracing{
			Shutdown: func(context.Context) error { return nil },
		},
	})
	if err != nil {
		t.Fatalf("NewProcessServices: %v", err)
	}
	t.Cleanup(func() { _ = ps.Close() })

	oldGen, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process:   ps,
		Candidate: oldCfg,
		Compose:   stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatalf("CompileGeneration old: %v", err)
	}
	t.Cleanup(func() { _ = oldGen.Close() })

	newCfg := routeOverrideAdminConfig(secret)
	withRouteOverrideOpenAIBackend(t, newCfg)
	newCfg.ModelAliases = []config.ModelAliasConfig{{Pattern: `^promo\|$`, Replacement: "openai:gpt-4"}}
	newGen, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process:   ps,
		Candidate: newCfg,
		Compose:   stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatalf("CompileGeneration new: %v", err)
	}
	t.Cleanup(func() { _ = newGen.Close() })

	leg, err := ps.Continuity.CreateALeg(context.Background(), "ov-put-after-publish")
	if err != nil {
		t.Fatalf("CreateALeg: %v", err)
	}

	m := runtimehost.NewManager(4, nil)
	d := runtimehost.NewGenerationDispatcher(m)
	oldPrepared := m.PrepareRequestPlane("old", reloadRequestPlane{h: oldGen.Handler()})
	if err := m.Publish(oldPrepared); err != nil {
		t.Fatalf("publish old: %v", err)
	}

	oldPromo := putRoutingOverride(t, d, secret, leg.ALegID, "promo|")
	if oldPromo.Code != http.StatusBadRequest {
		t.Fatalf("PUT promo| on old generation status=%d body=%s want 400", oldPromo.Code, oldPromo.Body)
	}
	oldCheap := putRoutingOverride(t, d, secret, leg.ALegID, "cheap|")
	if oldCheap.Code != http.StatusOK {
		t.Fatalf("PUT cheap| on old generation status=%d body=%s want 200", oldCheap.Code, oldCheap.Body)
	}

	newPrepared := m.PrepareRequestPlane("new", reloadRequestPlane{h: newGen.Handler()})
	if err := m.Publish(newPrepared); err != nil {
		t.Fatalf("publish new: %v", err)
	}

	afterPromo := putRoutingOverride(t, d, secret, leg.ALegID, "promo|")
	if afterPromo.Code != http.StatusOK {
		t.Fatalf("PUT promo| after publish status=%d body=%s want 200 from new generation validator", afterPromo.Code, afterPromo.Body)
	}
	var dto struct {
		Active   bool   `json:"active"`
		Selector string `json:"selector"`
		Revision int64  `json:"revision"`
	}
	if err := json.Unmarshal([]byte(afterPromo.Body), &dto); err != nil {
		t.Fatalf("decode PUT body: %v", err)
	}
	if !dto.Active || dto.Selector != "promo|" || dto.Revision < 1 {
		t.Fatalf("new-generation PUT result: %+v", dto)
	}
	afterCheap := putRoutingOverride(t, d, secret, leg.ALegID, "cheap|")
	if afterCheap.Code != http.StatusBadRequest {
		t.Fatalf("PUT cheap| after publish status=%d body=%s want 400 from new validator", afterCheap.Code, afterCheap.Body)
	}
}

func TestCompileGeneration_disableHTTPKeepsPersistedOverrideEnforced(t *testing.T) {
	t.Parallel()
	const secret = "override-secret-12"
	on := routeOverrideAdminConfig(secret)
	ps, err := runtimebundle.NewProcessServices(context.Background(), runtimebundle.ProcessServicesInput{
		Cfg:  on,
		Log:  testkit.DiscardLogger(),
		Opts: &runtimebundle.BuildOptions{PluginRegistry: generationRegistry(t)},
		Tracing: runtimebundle.ProcessTracing{
			Shutdown: func(context.Context) error { return nil },
		},
	})
	if err != nil {
		t.Fatalf("NewProcessServices: %v", err)
	}
	t.Cleanup(func() { _ = ps.Close() })
	store := ps.RouteOverrideStore
	if store == nil {
		t.Fatal("expected process override store")
	}

	genOn, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process:   ps,
		Candidate: on,
		Compose:   stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatalf("CompileGeneration enabled: %v", err)
	}
	t.Cleanup(func() { _ = genOn.Close() })

	off := routeOverrideBaseConfig()
	off.Diagnostics.SharedSecret = secret
	off.Routing.OverrideAdmin.Enabled = false
	genOff, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process:   ps,
		Candidate: off,
		Compose:   stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatalf("CompileGeneration disabled: %v", err)
	}
	t.Cleanup(func() { _ = genOff.Close() })

	if ps.RouteOverrideStore != store {
		t.Fatal("process override store must stay process-scoped across generation retirement")
	}

	rr := httptest.NewRecorder()
	genOff.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/admin/routing-overrides/missing", nil))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("disabled generation endpoint status=%d want 404", rr.Code)
	}

	offExec := runtimebundle.GenerationExecutorOf(genOff)
	if offExec == nil || offExec.RouteOverrideReader == nil {
		t.Fatal("disabled HTTP must keep runtime reader")
	}
	var opened []string
	okStream := func() aipapi.ManagedEventStream {
		return aipapi.NewFixedEventStream([]aipapi.Event{
			{Kind: aipapi.EventResponseStarted},
			{Kind: aipapi.EventMessageStarted},
			{Kind: aipapi.EventTextDelta, Delta: "ok"},
			{Kind: aipapi.EventResponseFinished},
		})
	}
	offExec.Backends = map[string]execbackend.Backend{
		"clientbe": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(_ context.Context, call aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				opened = append(opened, "clientbe:"+call.Route.Selector)
				return okStream(), nil
			},
		},
		"adminbe": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(_ context.Context, call aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				opened = append(opened, "adminbe:"+call.Route.Selector)
				return okStream(), nil
			},
		},
	}

	seed := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "clientbe:m"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("seed")},
		}},
	}
	stream, err := offExec.Execute(context.Background(), seed)
	if err != nil {
		t.Fatalf("seed turn: %v", err)
	}
	if _, err := aipapi.Collect(context.Background(), stream); err != nil {
		t.Fatalf("seed collect: %v", err)
	}
	if seed.Session.ALegID == "" {
		t.Fatal("seed turn must allocate an A-leg")
	}
	if len(opened) != 1 || opened[0] != "clientbe:clientbe:m" {
		t.Fatalf("seed turn before override: %v", opened)
	}
	if _, err := store.Replace(context.Background(), seed.Session.ALegID, "adminbe:m", time.Now().UTC()); err != nil {
		t.Fatalf("Replace: %v", err)
	}
	opened = nil
	next := &aipapi.Call{
		Session: aipapi.SessionRef{
			ClientSessionID:        seed.Session.ClientSessionID,
			ContinuityKey:          seed.Session.ContinuityKey,
			AuthoritativeSessionID: seed.Session.AuthoritativeSessionID,
			ResumeToken:            seed.Session.ResumeToken,
			ALegID:                 seed.Session.ALegID,
		},
		Route: aipapi.RouteIntent{Selector: "clientbe:m"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("next")},
		}},
	}
	stream, err = offExec.Execute(context.Background(), next)
	if err != nil {
		t.Fatalf("disabled-generation turn: %v", err)
	}
	if _, err := aipapi.Collect(context.Background(), stream); err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(opened) != 1 || opened[0] != "adminbe:adminbe:m" {
		t.Fatalf("persisted override must still be enforced: %v", opened)
	}

	got, err := store.Snapshot(context.Background(), seed.Session.ALegID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Active || got.Selector != "adminbe:m" {
		t.Fatalf("override must survive generation retirement: %+v", got)
	}
}

func routeOverrideAdminConfig(secret string) *config.Config {
	cfg := routeOverrideBaseConfig()
	cfg.Diagnostics.SharedSecret = secret
	cfg.Routing.OverrideAdmin.Enabled = true
	return cfg
}

func withRouteOverrideOpenAIBackend(t *testing.T, cfg *config.Config) {
	t.Helper()
	var empty yaml.Node
	if err := yaml.Unmarshal([]byte(testOpenAIBackendYAML()), &empty); err != nil {
		t.Fatal(err)
	}
	cfg.Plugins.Backends = []config.PluginConfig{
		{Kind: "openai-responses", ID: "openai", Enabled: true, Config: empty},
	}
}

type putOverrideResult struct {
	Code int
	Body string
}

func putRoutingOverride(t *testing.T, h http.Handler, secret, aLegID, selector string) putOverrideResult {
	t.Helper()
	body, err := json.Marshal(map[string]string{"selector": selector})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPut, "/admin/routing-overrides/"+aLegID, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(diag.HeaderDiagnosticsSecret, secret)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	got, _ := io.ReadAll(rr.Body)
	return putOverrideResult{Code: rr.Code, Body: string(got)}
}
