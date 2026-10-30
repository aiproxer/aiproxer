package stdhttp

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/config"
	corediag "github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/core/securesession/adapters/memory"
	"github.com/aiproxer/aiproxer/internal/core/securesession/domain"
	"github.com/aiproxer/aiproxer/internal/infra/metrics"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/usage"
	"github.com/prometheus/client_golang/prometheus"
)

func collapseTestDetector(now *time.Time) *metrics.PromptCacheCollapse {
	return metrics.NewPromptCacheCollapse(
		metrics.RegisterPromptCacheCollapseProm(prometheus.NewRegistry()),
		slog.New(slog.DiscardHandler),
		func() time.Time { return *now },
	)
}

func collapseUsage(session, leg string, input, cacheRead int) usage.Event {
	return usage.Event{
		SessionID: session, ALegID: "a-leg", BLegID: leg,
		BackendID: "backend", Model: "model",
		InputTokens: input, CacheReadTokens: cacheRead,
	}
}

func mountCacheHealthMux(t *testing.T, m *metrics.Bundle) (*http.ServeMux, string) {
	t.Helper()
	ctx := t.Context()
	st := memory.New(memory.Options{})
	var fp domain.TokenFingerprint
	fp[2] = 2
	if _, err := st.Create(ctx, domain.CreateRecord{
		SessionID: "sess-mount", ResumeFingerprint: fp,
		Owner: domain.PrincipalRef{ID: "op-user"}, Policy: domain.PolicyMetadata{},
		ALegID: "a-mount", ResumeEligible: true, CreatedAt: time.Unix(1, 0),
	}); err != nil {
		t.Fatal(err)
	}
	const secret = "s3cr3t-12chars-minimum-secret-value-here-ok"
	cfg := &config.Config{}
	cfg.Diagnostics.SharedSecret = secret
	cfg.SecureSession.DiagnosticsExposeSummaries = true
	cfg.SecureSession.DiagnosticsPathPrefix = "/debug/sessions"
	mux := http.NewServeMux()
	if err := mountSecureSessionDiagnostics(mountSecureSessionDiagnosticsInput{
		LogCtx: ctx, Mux: mux, Cfg: cfg,
		Log:      slog.New(slog.DiscardHandler),
		Security: HTTPSecurityInput{SecureSessionStore: st},
		Metrics:  m,
	}); err != nil {
		t.Fatal(err)
	}
	return mux, secret
}

func TestSecureSessionDiagnosticsMount_cacheHealthWired(t *testing.T) {
	t.Parallel()
	now := time.Unix(600, 0)
	d := collapseTestDetector(&now)
	if err := d.OnUsage(t.Context(), collapseUsage("sess-mount", "warm", 8000, 8000)); err != nil {
		t.Fatal(err)
	}
	mux, secret := mountCacheHealthMux(t, &metrics.Bundle{PromptCacheCollapse: d})

	req := httptest.NewRequest(http.MethodGet, "/debug/sessions/sess-mount", nil)
	req.Header.Set(corediag.HeaderDiagnosticsSecret, secret)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	var env struct {
		Session map[string]any `json:"session"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	health, ok := env.Session["cache_health"].(map[string]any)
	if !ok || health["status"] != "healthy" || health["stale"] != false {
		t.Fatalf("cache_health=%#v want healthy fresh", env.Session["cache_health"])
	}
}

func TestSecureSessionDiagnosticsMount_cacheHealthAbsentWithoutMetrics(t *testing.T) {
	t.Parallel()
	mux, secret := mountCacheHealthMux(t, nil)

	for _, path := range []string{"/debug/sessions/sess-mount", "/debug/sessions"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set(corediag.HeaderDiagnosticsSecret, secret)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status %d", path, rec.Code)
		}
		if strings.Contains(rec.Body.String(), "cache_health") {
			t.Fatalf("%s: unwired mount must not emit cache_health: %s", path, rec.Body.String())
		}
	}
}
