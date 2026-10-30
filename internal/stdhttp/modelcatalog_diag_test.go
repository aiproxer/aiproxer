package stdhttp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	coreconfig "github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/core/modelcatalog"
	"github.com/aiproxer/aiproxer/internal/infra/modelcatalog/modelsdev"
	"github.com/aiproxer/aiproxer/internal/testkit"
)

type memCacheDiag struct{ load modelcatalog.Snapshot }

func (m *memCacheDiag) Load(context.Context) (modelcatalog.Snapshot, error) { return m.load, nil }
func (*memCacheDiag) Save(context.Context, modelcatalog.Snapshot) error     { return nil }

func TestModelCatalogDiagnostics_protectAndRedact(t *testing.T) {
	t.Parallel()
	const path = "/debug/model-catalog"
	const secret = "s3cr3t-12chars-minimum-secret-value-here-ok"

	snap, err := modelsdev.ParseSnapshot([]byte(`{"openai":{"id":"openai","models":[{"id":"m"}]}}`), time.Unix(1700, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	snap.Generation = "gen-z"
	snap.ContentHash = "h1"

	rt := modelcatalog.NewCatalogRuntime(modelcatalog.RuntimeConfig{
		Cache: &memCacheDiag{load: snap},
	})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rt.Close() })

	h := NewCatalogStatusHandler(nil, modelcatalog.CatalogStatusHandlerConfig{
		Runtime:                rt,
		UsageEnabled:           true,
		ExternalUpdatesEnabled: true,
		UpdateInterval:         time.Hour,
		SourceURL:              "https://user:supersecret@api.example.com/models.json",
		Now:                    func() time.Time { return time.Unix(2000, 0).UTC() },
	})
	w := diag.WrapDiagnosticsProtect(secret, h)
	mux := http.NewServeMux()
	mux.Handle(path, w)

	t.Run("forbidden_without_header", func(t *testing.T) {
		t.Parallel()
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		mux.ServeHTTP(rr, req)
		if rr.Code != http.StatusForbidden {
			t.Fatalf("code=%d", rr.Code)
		}
	})

	t.Run("method_not_allowed", func(t *testing.T) {
		t.Parallel()
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, path, nil)
		req.Header.Set(diag.HeaderDiagnosticsSecret, secret)
		mux.ServeHTTP(rr, req)
		if rr.Code != http.StatusMethodNotAllowed {
			t.Fatalf("code=%d", rr.Code)
		}
	})

	t.Run("ok_json", func(t *testing.T) {
		t.Parallel()
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set(diag.HeaderDiagnosticsSecret, secret)
		mux.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
		}
		body := rr.Body.String()
		if strings.Contains(body, "supersecret") || strings.Contains(body, "user:supersecret") {
			t.Fatalf("secret leaked: %s", body)
		}
		var env modelcatalog.CatalogDiagnosticsJSON
		if err := json.Unmarshal(rr.Body.Bytes(), &env); err != nil {
			t.Fatal(err)
		}
		if env.Snapshot == nil || env.Snapshot.Generation != "gen-z" {
			t.Fatalf("snapshot=%v", env.Snapshot)
		}
		if !strings.Contains(env.SourceURLRedacted, "api.example.com") {
			t.Fatalf("source: %q", env.SourceURLRedacted)
		}
	})
}

func TestModelCatalogDiagnostics_disabled(t *testing.T) {
	t.Parallel()
	const path = "/debug/mcat2"
	const secret = "s3cr3t-12chars-minimum-secret-value-here-ok-2"
	h := NewCatalogStatusHandler(nil, modelcatalog.CatalogStatusHandlerConfig{UsageEnabled: false})
	mux := http.NewServeMux()
	mux.Handle(path, diag.WrapDiagnosticsProtect(secret, h))
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set(diag.HeaderDiagnosticsSecret, secret)
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("code %d", rr.Code)
	}
	var env modelcatalog.CatalogDiagnosticsJSON
	if err := json.Unmarshal(rr.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if env.Status != modelcatalog.CatalogDiagDisabled {
		t.Fatalf("status %q", env.Status)
	}
}

func TestModelCatalogDiagnostics_mount_stackHTTPHandler_wiring(t *testing.T) {
	t.Parallel()
	const path = "/debug/cat-status-mount"
	const secret = "s3cr3t-12chars-minimum-secret-value-here-ok"

	snap, err := modelsdev.ParseSnapshot([]byte(`{"openai":{"id":"openai","models":[{"id":"m"}]}}`), time.Unix(1700, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	snap.Generation = "gen-mount"
	snap.ContentHash = "hm"

	rt := modelcatalog.NewCatalogRuntime(modelcatalog.RuntimeConfig{
		Cache: &memCacheDiag{load: snap},
	})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rt.Close() })

	cfg := &coreconfig.Config{
		Logging:       coreconfig.LoggingConfig{AccessLog: false},
		Observability: coreconfig.ObservabilityConfig{Tracing: coreconfig.TracingConfig{Enabled: false}},
		Diagnostics:   coreconfig.DiagnosticsConfig{SharedSecret: secret},
		ModelCatalog: coreconfig.ModelCatalogConfig{
			DiagnosticsPath:        path,
			Enabled:                true,
			ExternalUpdatesEnabled: true,
			UpdateInterval:         "1h",
			SourceURL:              "https://api.example.com/models.json",
		},
	}
	mux := http.NewServeMux()
	mountModelCatalogDiagnostics(context.Background(), diagnosticsMount{
		Mux:    mux,
		Cfg:    cfg,
		Log:    testkit.DiscardLogger(),
		Models: HTTPModelInput{CatalogRuntime: rt},
	})
	outer := stackHTTPHandler(stackHTTPInput{
		Cfg: cfg, Log: testkit.DiscardLogger(), Security: HTTPSecurityInput{}, TraceGen: diag.NewTraceIDGenerator(), Inner: mux, HTTPProm: nil,
	})
	srv := httptest.NewServer(outer)
	t.Cleanup(srv.Close)

	req, err := http.NewRequest(http.MethodGet, srv.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(diag.HeaderDiagnosticsSecret, secret)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = res.Body.Close() })
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", res.StatusCode)
	}
	var env modelcatalog.CatalogDiagnosticsJSON
	if err := json.NewDecoder(res.Body).Decode(&env); err != nil {
		t.Fatal(err)
	}
	if env.Snapshot == nil || env.Snapshot.Generation != "gen-mount" {
		t.Fatalf("snapshot=%v", env.Snapshot)
	}
}
