//go:build integration

package runtimebundle_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	coreruntime "github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/infra/runtimehost"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
	"github.com/aiproxer/aiproxer/internal/stdhttp"
	"github.com/aiproxer/aiproxer/internal/testkit/compatibleparity"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

func TestCompatibleMultiInstance_runtimeBundleBuildPreservesProvenance(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	base, err := os.ReadFile(filepath.Join("..", "..", "..", "config", "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[{"id":"m1"}]}`)
	}))
	t.Cleanup(srv.Close)

	rows := fmt.Sprintf(`    - id: compat-a
      kind: custom-openai-legacy-compatible
      enabled: true
      config:
        backend_prefix: compat-a
        base_url: %s/v1
        tokenizer: cl100k_base
        max_concurrent_requests: 1
    - id: compat-b
      kind: custom-openai-legacy-compatible
      enabled: true
      config:
        backend_prefix: compat-b
        base_url: %s/v1
        tokenizer: o200k_base
        max_concurrent_requests: 2
`, srv.URL, srv.URL)
	text := strings.Replace(string(base), "  features:\n", rows+"  features:\n", 1)
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}

	host, err := runtimebundle.BuildHost(ctx, runtimebundle.BuildHostInput{
		ConfigPath:      path,
		Mandatory:       aipsdk.StandardDistributionRequirements(),
		HandlerComposer: stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = host.Close(context.Background()) })

	backends := hostActiveCompatibleBackends(t, host)
	beA, okA := backends["compat-a"]
	beB, okB := backends["compat-b"]
	if !okA || !okB {
		t.Fatalf("backends missing compat-a=%v compat-b=%v", okA, okB)
	}
	if beA.TokenizerID == beB.TokenizerID {
		t.Fatalf("tokenizer provenance collapsed: %q", beA.TokenizerID)
	}
	if beA.TokenizerID != "cl100k_base" || beB.TokenizerID != "o200k_base" {
		t.Fatalf("tokenizer ids A=%q B=%q", beA.TokenizerID, beB.TokenizerID)
	}
}

func hostActiveCompatibleBackends(t *testing.T, host *runtimebundle.Host) map[string]execbackend.Backend {
	t.Helper()
	active := runtimebundle.HostManager(host).Active()
	if active == nil {
		t.Fatal("nil active generation")
	}
	provider, ok := active.RequestPlane().(runtimehost.ExecutorProvider)
	if !ok || provider == nil {
		t.Fatal("active generation missing ExecutorProvider")
	}
	ex, ok := provider.ExecutorView().(*coreruntime.Executor)
	if !ok || ex == nil {
		t.Fatal("expected *runtime.Executor from active generation")
	}
	return ex.Backends
}

func multiInstanceCall(family compatibleparity.Family) aipapi.Call {
	op := aipapi.OperationOpenAIChatCompletions
	if family == compatibleparity.FamilyOpenAIResponses {
		op = aipapi.OperationOpenAIResponses
	}
	if family == compatibleparity.FamilyAnthropic {
		op = ""
	}
	return aipapi.Call{
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("iso")}}},
		Invocation: aipapi.Invocation{
			Operation:     op,
			DeliveryMode:  aipapi.DeliveryModeNonStreaming,
			TransportMode: aipapi.TransportModeNonStreaming,
		},
	}
}

func factoryForMulti(family compatibleparity.Family) string {
	switch family {
	case compatibleparity.FamilyOpenAILegacy:
		return standardplugins.CustomOpenAILegacyCompatibleID
	case compatibleparity.FamilyOpenAIResponses:
		return standardplugins.CustomOpenAIResponsesCompatibleID
	case compatibleparity.FamilyAnthropic:
		return standardplugins.CustomAnthropicCompatibleID
	default:
		return ""
	}
}

func TestCompatibleMultiInstance_routingPolicyIndependence(t *testing.T) {
	ctx := context.Background()
	var hitsA, hitsB atomic.Int32
	srvA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitsA.Add(1)
		writeMultiInstanceResponse(w, "A", compatibleparity.FamilyOpenAILegacy, r.URL.Path)
	}))
	srvB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitsB.Add(1)
		writeMultiInstanceResponse(w, "B", compatibleparity.FamilyOpenAILegacy, r.URL.Path)
	}))
	failA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitsA.Add(1)
		http.Error(w, "fail", http.StatusInternalServerError)
	}))
	slowA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitsA.Add(1)
		time.Sleep(250 * time.Millisecond)
		writeMultiInstanceResponse(w, "slow-A", compatibleparity.FamilyOpenAILegacy, r.URL.Path)
	}))
	t.Cleanup(func() {
		srvA.Close()
		srvB.Close()
		failA.Close()
		slowA.Close()
	})

	t.Setenv("COMPAT_ROUTE_A_KEY", "sk-a")
	t.Setenv("COMPAT_ROUTE_B_KEY", "sk-b")

	path := writeCompatibleRoutingConfig(t, map[string]routingInstanceSpec{
		"compat-a": {URL: srvA.URL, Tokenizer: "cl100k_base", MaxConcurrent: 1, EnvRoot: "COMPAT_ROUTE_A_KEY"},
		"compat-b": {URL: srvB.URL, Tokenizer: "o200k_base", MaxConcurrent: 2, EnvRoot: "COMPAT_ROUTE_B_KEY"},
	})

	host, err := runtimebundle.BuildHost(ctx, runtimebundle.BuildHostInput{
		ConfigPath:      path,
		Mandatory:       aipsdk.StandardDistributionRequirements(),
		HandlerComposer: stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = host.Close(context.Background()) })

	inv, err := runtimebundle.InspectInventory(ctx, runtimebundle.InspectInput{
		ConfigPath: path,
		Mandatory:  aipsdk.StandardDistributionRequirements(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(inv.CompatibleBackends) != 2 {
		t.Fatalf("compatible_backends=%d", len(inv.CompatibleBackends))
	}
	byID := map[string]struct{}{}
	for _, row := range inv.CompatibleBackends {
		byID[row.InstanceID] = struct{}{}
		if row.Origin != "built_in_compatible" {
			t.Fatalf("origin=%q instance=%q", row.Origin, row.InstanceID)
		}
		if row.InventoryHealth == nil || row.InventoryHealth.ModelCount != 1 {
			t.Fatalf("inventory health=%+v instance=%q", row.InventoryHealth, row.InstanceID)
		}
	}
	if _, ok := byID["compat-a"]; !ok {
		t.Fatalf("missing compat-a provenance row: %+v", inv.CompatibleBackends)
	}
	if _, ok := byID["compat-b"]; !ok {
		t.Fatalf("missing compat-b provenance row: %+v", inv.CompatibleBackends)
	}

	ex := hostExecutor(t, host)
	ex.Rand = routing.NewSeededRng(1)

	t.Run("sequential", func(t *testing.T) {
		hitsA.Store(0)
		hitsB.Store(0)
		text, err := collectExecutorText(ctx, ex, "compat-a:compat-a/model-a")
		if err != nil {
			t.Fatal(err)
		}
		if text != "iso-A" {
			t.Fatalf("text=%q", text)
		}
		if hitsA.Load() == 0 || hitsB.Load() != 0 {
			t.Fatalf("hits A=%d B=%d", hitsA.Load(), hitsB.Load())
		}
	})

	t.Run("failover", func(t *testing.T) {
		pathFail := writeCompatibleRoutingConfig(t, map[string]routingInstanceSpec{
			"compat-a": {URL: failA.URL, Tokenizer: "cl100k_base", MaxConcurrent: 1, EnvRoot: "COMPAT_ROUTE_A_KEY"},
			"compat-b": {URL: srvB.URL, Tokenizer: "o200k_base", MaxConcurrent: 2, EnvRoot: "COMPAT_ROUTE_B_KEY"},
		})
		hostFail, err := runtimebundle.BuildHost(ctx, runtimebundle.BuildHostInput{
			ConfigPath: pathFail, Mandatory: aipsdk.StandardDistributionRequirements(), HandlerComposer: stdhttp.ComposeStandardHTTP,
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = hostFail.Close(context.Background()) })
		exFail := hostExecutor(t, hostFail)
		text, err := collectExecutorText(ctx, exFail, "compat-a:compat-a/model-a!compat-b:compat-b/model-b")
		if err != nil {
			t.Fatal(err)
		}
		if text != "iso-B" {
			t.Fatalf("text=%q", text)
		}
	})

	t.Run("parallel", func(t *testing.T) {
		pathPar := writeCompatibleRoutingConfig(t, map[string]routingInstanceSpec{
			"compat-a": {URL: slowA.URL, Tokenizer: "cl100k_base", MaxConcurrent: 1, EnvRoot: "COMPAT_ROUTE_A_KEY"},
			"compat-b": {URL: srvB.URL, Tokenizer: "o200k_base", MaxConcurrent: 2, EnvRoot: "COMPAT_ROUTE_B_KEY"},
		})
		hostPar, err := runtimebundle.BuildHost(ctx, runtimebundle.BuildHostInput{
			ConfigPath: pathPar, Mandatory: aipsdk.StandardDistributionRequirements(), HandlerComposer: stdhttp.ComposeStandardHTTP,
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = hostPar.Close(context.Background()) })
		exPar := hostExecutor(t, hostPar)
		exPar.Rand = routing.NewSeededRng(1)
		text, err := collectExecutorText(ctx, exPar, "compat-a:compat-a/model-a!compat-b:compat-b/model-b")
		if err != nil {
			t.Fatal(err)
		}
		if text != "iso-B" {
			t.Fatalf("parallel winner text=%q want iso-B", text)
		}
	})

	t.Run("weighted", func(t *testing.T) {
		hitsA.Store(0)
		hitsB.Store(0)
		text, err := collectExecutorText(ctx, ex, "[weight=1]compat-a:compat-a/model-a^[weight=1]compat-b:compat-b/model-b")
		if err != nil {
			t.Fatal(err)
		}
		if text != "iso-A" && text != "iso-B" {
			t.Fatalf("unexpected weighted text=%q", text)
		}
		if hitsA.Load() == 0 && hitsB.Load() == 0 {
			t.Fatal("expected at least one backend hit")
		}
	})
}

type routingInstanceSpec struct {
	URL           string
	Tokenizer     string
	MaxConcurrent int
	EnvRoot       string
}

func writeCompatibleRoutingConfig(t *testing.T, specs map[string]routingInstanceSpec) string {
	t.Helper()
	base, err := os.ReadFile(filepath.Join("..", "..", "..", "config", "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var rows strings.Builder
	for id, spec := range specs {
		fmt.Fprintf(&rows, `    - id: %s
      kind: custom-openai-legacy-compatible
      enabled: true
      config:
        backend_prefix: %s
        base_url: %s/v1
        api_key_env_var_root: %s
        tokenizer: %s
        max_concurrent_requests: %d
        models:
          source: inline
          items:
            - canonical_id: %s/model-a
              native_id: model-a
`, id, id, spec.URL, spec.EnvRoot, spec.Tokenizer, spec.MaxConcurrent, id)
	}
	text := strings.Replace(string(base), "  features:\n", rows.String()+"  features:\n", 1)
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func hostExecutor(t *testing.T, host *runtimebundle.Host) *coreruntime.Executor {
	t.Helper()
	return hostActiveCompatibleBackendsExecutor(t, host)
}

func hostActiveCompatibleBackendsExecutor(t *testing.T, host *runtimebundle.Host) *coreruntime.Executor {
	t.Helper()
	active := runtimebundle.HostManager(host).Active()
	if active == nil {
		t.Fatal("nil active generation")
	}
	provider, ok := active.RequestPlane().(runtimehost.ExecutorProvider)
	if !ok || provider == nil {
		t.Fatal("missing ExecutorProvider")
	}
	ex, ok := provider.ExecutorView().(*coreruntime.Executor)
	if !ok || ex == nil {
		t.Fatal("expected *runtime.Executor")
	}
	return ex
}

func collectExecutorText(ctx context.Context, ex *coreruntime.Executor, selector string) (string, error) {
	call := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: selector},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("route-test")},
		}},
		Invocation: aipapi.Invocation{
			Operation:     aipapi.OperationOpenAIChatCompletions,
			DeliveryMode:  aipapi.DeliveryModeNonStreaming,
			TransportMode: aipapi.TransportModeNonStreaming,
		},
	}
	s, err := ex.Execute(ctx, call)
	if err != nil {
		return "", err
	}
	col, err := aipapi.Collect(ctx, s)
	if err != nil {
		return "", err
	}
	return col.Text.String(), nil
}
