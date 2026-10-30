package stdhttp

import (
	"bytes"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
)

// Task 4.2 generic protected admin mounting. stdhttp no longer composes the
// billing admin surface; it serves every registered [AdminMount] behind the
// existing diagnostics shared-secret protection at literal paths below
// /admin/ only. These tests preserve the former billing-mount assertions at
// the generic layer: mounting, protection, empty-secret refusal, path
// isolation from frontend routes, response/error pass-through, and the mount
// contract inventory. The real billing handler/reader/HealthSink chain lives
// in the runtimebundle handoff tests.

const adminMountsSecret = "adm-mounted-secret"

type adminMountsProbe struct {
	methods []string
	paths   []string
	status  int
	body    string
}

func (p *adminMountsProbe) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.methods = append(p.methods, r.Method)
	p.paths = append(p.paths, r.URL.Path)
	status := p.status
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	_, _ = w.Write([]byte(p.body))
}

func adminMountsConfig(secret string) *config.Config {
	return &config.Config{Diagnostics: config.DiagnosticsConfig{SharedSecret: secret}}
}

func adminMountsMux(t *testing.T, cfg *config.Config, mounts ...AdminMount) *http.ServeMux {
	t.Helper()
	mux := http.NewServeMux()
	if err := mountAdminMounts(adminMountsInput{Mux: mux, Cfg: cfg, Mounts: mounts}); err != nil {
		t.Fatalf("mountAdminMounts: %v", err)
	}
	return mux
}

func adminMountsServe(handler http.Handler, method, path, secret string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	if secret != "" {
		req.Header.Set("X-AIP-Diagnostics-Secret", secret)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestAdminMountsMountedAndProtected(t *testing.T) {
	t.Parallel()
	probe := &adminMountsProbe{body: `{"status":"ok"}`}
	mux := adminMountsMux(t, adminMountsConfig(adminMountsSecret),
		AdminMount{Path: "/admin/billing", Handler: probe})

	missing := adminMountsServe(mux, http.MethodGet, "/admin/billing/account?account_id=acct", "")
	if missing.Code != http.StatusForbidden {
		t.Fatalf("missing secret status=%d want=%d", missing.Code, http.StatusForbidden)
	}
	missingPOST := adminMountsServe(mux, http.MethodPost, "/admin/billing/account", "")
	if missingPOST.Code != http.StatusForbidden {
		t.Fatalf("missing secret POST status=%d want=%d", missingPOST.Code, http.StatusForbidden)
	}
	if len(probe.paths) != 0 {
		t.Fatalf("handler reached without the diagnostics secret: %v", probe.paths)
	}

	allowed := adminMountsServe(mux, http.MethodGet, "/admin/billing/account?account_id=acct&limit=10", adminMountsSecret)
	if allowed.Code != http.StatusOK || allowed.Body.String() != `{"status":"ok"}` {
		t.Fatalf("allowed response status=%d body=%q", allowed.Code, allowed.Body.String())
	}
	created := adminMountsServe(mux, http.MethodPost, "/admin/billing/account", adminMountsSecret)
	if created.Code != http.StatusOK {
		t.Fatalf("allowed POST status=%d body=%q", created.Code, created.Body.String())
	}
	if !slices.Equal(probe.methods, []string{http.MethodGet, http.MethodPost}) {
		t.Fatalf("handler methods=%v want [GET POST]", probe.methods)
	}
	// The mount strips its prefix: the handler owns its own relative routes.
	if !slices.Equal(probe.paths, []string{"/account", "/account"}) {
		t.Fatalf("handler paths=%v want the stripped mount-relative paths", probe.paths)
	}
}

func TestAdminMountsEmptySecretSkipsAndWarns(t *testing.T) {
	t.Parallel()
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	probe := &adminMountsProbe{body: "provisioned"}
	mux := http.NewServeMux()
	if err := mountAdminMounts(adminMountsInput{
		Mux: mux, Cfg: adminMountsConfig(""), Log: log,
		Mounts: []AdminMount{{Path: "/admin/billing", Handler: probe}},
	}); err != nil {
		t.Fatalf("mountAdminMounts: %v", err)
	}

	for _, path := range []string{"/admin/billing", "/admin/billing/account"} {
		rec := adminMountsServe(mux, http.MethodGet, path, "")
		if rec.Code != http.StatusNotFound {
			t.Fatalf("empty secret GET %s status=%d want=%d body=%q", path, rec.Code, http.StatusNotFound, rec.Body.String())
		}
		rec = adminMountsServe(mux, http.MethodPost, path, "")
		if rec.Code != http.StatusNotFound {
			t.Fatalf("empty secret POST %s status=%d want=%d body=%q", path, rec.Code, http.StatusNotFound, rec.Body.String())
		}
	}
	if len(probe.paths) != 0 {
		t.Fatalf("empty secret served the handler: %v", probe.paths)
	}
	if !strings.Contains(logs.String(), "shared_secret") || !strings.Contains(logs.String(), "/admin/billing") {
		t.Fatalf("empty-secret skip must be logged with the path: %s", logs.String())
	}
}

func TestAdminMountsNotReachableOnFrontendPaths(t *testing.T) {
	t.Parallel()
	probe := &adminMountsProbe{body: "should-not-run"}
	mux := adminMountsMux(t, adminMountsConfig(adminMountsSecret),
		AdminMount{Path: "/admin/billing", Handler: probe})
	frontendHits := 0
	mux.HandleFunc("/v1/responses", func(w http.ResponseWriter, _ *http.Request) {
		frontendHits++
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"output":"ok"}`))
	})

	for _, path := range []string{"/v1/responses", "/v1/account", "/v1/chat/completions"} {
		rec := adminMountsServe(mux, http.MethodPost, path, adminMountsSecret)
		if path == "/v1/responses" {
			if rec.Code != http.StatusOK {
				t.Fatalf("%s status=%d want 200 body=%q", path, rec.Code, rec.Body.String())
			}
			continue
		}
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s status=%d want 404 (admin commands are never frontend routes) body=%q", path, rec.Code, rec.Body.String())
		}
	}
	if frontendHits != 1 {
		t.Fatalf("frontend hits=%d want 1", frontendHits)
	}
	if len(probe.paths) != 0 {
		t.Fatalf("frontend paths reached the admin handler: %v", probe.paths)
	}
}

func TestAdminMountsRejectInvalidPaths(t *testing.T) {
	t.Parallel()
	rejected := []struct {
		name string
		path string
	}{
		{name: "empty", path: ""},
		{name: "bare admin", path: "/admin"},
		{name: "bare admin with slash", path: "/admin/"},
		{name: "not absolute", path: "admin/billing"},
		{name: "method pattern", path: "GET /admin/billing"},
		{name: "host pattern", path: "example.com/admin/billing"},
		{name: "wildcard segment", path: "/admin/{tenant}"},
		{name: "rest wildcard", path: "/admin/{rest...}"},
		{name: "end wildcard", path: "/admin/{$}"},
		{name: "star", path: "/admin/a*b"},
		{name: "backslash", path: `/admin/a\b`},
		{name: "query", path: "/admin/a?b"},
		{name: "fragment", path: "/admin/a#b"},
		{name: "percent escape", path: "/admin/%77p"},
		{name: "space", path: "/admin/a b"},
		{name: "control", path: "/admin/\tx"},
		{name: "dot segment", path: "/admin/./x"},
		{name: "parent segment", path: "/admin/../x"},
		{name: "double slash", path: "/admin//x"},
		{name: "outside admin", path: "/billing"},
	}
	for _, tc := range rejected {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			mux := http.NewServeMux()
			err := mountAdminMounts(adminMountsInput{
				Mux: mux, Cfg: adminMountsConfig(adminMountsSecret),
				Mounts: []AdminMount{{Path: tc.path, Handler: &adminMountsProbe{}}},
			})
			if err == nil {
				t.Fatalf("path %q must be rejected", tc.path)
			}
			if tc.path != "" && !strings.Contains(err.Error(), fmt.Sprintf("%q", tc.path)) {
				t.Fatalf("error %q must name the invalid path %q", err, tc.path)
			}
		})
	}

	accepted := []string{"/admin/billing", "/admin/billing/", "/admin/billing/sub", "/admin/foobar2", "/admin/build-ing_v2"}
	for _, path := range accepted {
		t.Run("accept "+path, func(t *testing.T) {
			t.Parallel()
			mux := http.NewServeMux()
			if err := mountAdminMounts(adminMountsInput{
				Mux: mux, Cfg: adminMountsConfig(adminMountsSecret),
				Mounts: []AdminMount{{Path: path, Handler: &adminMountsProbe{}}},
			}); err != nil {
				t.Fatalf("path %q must be accepted: %v", path, err)
			}
		})
	}
}

func TestAdminMountsRejectOverlaps(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		first   string
		second  string
		cfg     *config.Config
		wantErr bool
	}{
		{name: "duplicate", first: "/admin/foo", second: "/admin/foo", wantErr: true},
		{name: "trailing duplicate", first: "/admin/foo", second: "/admin/foo/", wantErr: true},
		{name: "child", first: "/admin/foo", second: "/admin/foo/bar", wantErr: true},
		{name: "child reversed", first: "/admin/foo/bar", second: "/admin/foo", wantErr: true},
		{name: "segment sibling", first: "/admin/foo", second: "/admin/foobar", wantErr: false},
		{name: "unrelated", first: "/admin/foo", second: "/admin/bar", wantErr: false},
		{name: "keepwarm builtin", first: "/admin/keepwarm", second: "/admin/other", wantErr: true},
		{name: "keepwarm child", first: "/admin/keepwarm/x", second: "/admin/other", wantErr: true},
		{name: "keepwarm sibling", first: "/admin/keepwarmx", second: "/admin/other", wantErr: false},
		{name: "session features builtin", first: "/admin/session-features", second: "/admin/other", wantErr: true},
		{name: "session features child", first: "/admin/session-features/x", second: "/admin/other", wantErr: true},
		{
			name: "configured accounting admin", first: "/admin/ledger", second: "/admin/other", wantErr: true,
			cfg: &config.Config{Accounting: config.AccountingConfig{Admin: config.AccountingAdminConfig{Enabled: true, Path: "/admin/ledger"}}},
		},
		{
			name: "configured accounting admin child", first: "/admin/ledger/x", second: "/admin/other", wantErr: true,
			cfg: &config.Config{Accounting: config.AccountingConfig{Admin: config.AccountingAdminConfig{Enabled: true, Path: "/admin/ledger"}}},
		},
		{
			name: "configured accounting admin sibling", first: "/admin/ledgerx", second: "/admin/other", wantErr: false,
			cfg: &config.Config{Accounting: config.AccountingConfig{Admin: config.AccountingAdminConfig{Enabled: true, Path: "/admin/ledger"}}},
		},
		{
			name: "reports default cross mount", first: "/admin/billing", second: "/admin/billing/health", wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := tc.cfg
			if cfg == nil {
				cfg = adminMountsConfig(adminMountsSecret)
			} else {
				cfg.Diagnostics.SharedSecret = adminMountsSecret
			}
			mux := http.NewServeMux()
			err := mountAdminMounts(adminMountsInput{
				Mux: mux, Cfg: cfg,
				Mounts: []AdminMount{
					{Path: tc.first, Handler: &adminMountsProbe{}},
					{Path: tc.second, Handler: &adminMountsProbe{}},
				},
			})
			if tc.wantErr {
				if err == nil {
					t.Fatalf("mounts %q and %q must be rejected", tc.first, tc.second)
				}
				if !strings.Contains(err.Error(), "overlap") {
					t.Fatalf("error %q must explain the overlap", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("mounts %q and %q must be accepted: %v", tc.first, tc.second, err)
			}
		})
	}
}

func TestAdminMountsConfiguredReportsPathIsItsOwnMount(t *testing.T) {
	t.Parallel()
	// accounting.billing.reports_path names the protected surface the billing
	// composition registers as an admin mount; it must not reject its own
	// registration, while a duplicate registration at that path still fails.
	cfg := adminMountsConfig(adminMountsSecret)
	cfg.Accounting.Billing.ReportsPath = "/admin/billing-custom"
	probe := &adminMountsProbe{body: "custom"}
	mux := http.NewServeMux()
	if err := mountAdminMounts(adminMountsInput{
		Mux: mux, Cfg: cfg,
		Mounts: []AdminMount{{Path: "/admin/billing-custom", Handler: probe}},
	}); err != nil {
		t.Fatalf("configured reports path must mount its own surface: %v", err)
	}
	rec := adminMountsServe(mux, http.MethodGet, "/admin/billing-custom/health", adminMountsSecret)
	if rec.Code != http.StatusOK {
		t.Fatalf("configured reports path status=%d body=%q", rec.Code, rec.Body.String())
	}

	err := mountAdminMounts(adminMountsInput{
		Mux: http.NewServeMux(), Cfg: cfg,
		Mounts: []AdminMount{
			{Path: "/admin/billing-custom", Handler: probe},
			{Path: "/admin/billing-custom", Handler: probe},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "overlap") {
		t.Fatalf("duplicate configured reports registrations must fail: %v", err)
	}
}

func TestAdminMountsRejectNilHandler(t *testing.T) {
	t.Parallel()
	err := mountAdminMounts(adminMountsInput{
		Mux: http.NewServeMux(), Cfg: adminMountsConfig(adminMountsSecret),
		Mounts: []AdminMount{{Path: "/admin/nil-handler"}},
	})
	if err == nil || !strings.Contains(err.Error(), "handler") {
		t.Fatalf("nil handler must fail the build: %v", err)
	}
}

func TestAdminMountsPreserveHandlerResponseAndErrorShape(t *testing.T) {
	t.Parallel()
	mux := adminMountsMux(t, adminMountsConfig(adminMountsSecret),
		AdminMount{Path: "/admin/billing", Handler: &adminMountsProbe{status: http.StatusNotFound, body: `{"error":"not_found"}`}})
	rec := adminMountsServe(mux, http.MethodGet, "/admin/billing/account?account_id=acct&limit=10", adminMountsSecret)
	if rec.Code != http.StatusNotFound || rec.Body.String() != `{"error":"not_found"}` {
		t.Fatalf("handler 404 rewritten: status=%d body=%q", rec.Code, rec.Body.String())
	}

	mux = adminMountsMux(t, adminMountsConfig(adminMountsSecret),
		AdminMount{Path: "/admin/billing", Handler: &adminMountsProbe{status: http.StatusBadRequest, body: `{"error":"invalid_query"}`}})
	rec = adminMountsServe(mux, http.MethodGet, "/admin/billing/account?account_id=acct&limit=bad", adminMountsSecret)
	if rec.Code != http.StatusBadRequest || rec.Body.String() != `{"error":"invalid_query"}` {
		t.Fatalf("handler 400 rewritten: status=%d body=%q", rec.Code, rec.Body.String())
	}
}

func TestAdminMountsMountContractInventory(t *testing.T) {
	t.Parallel()
	for _, row := range mountContractInventory {
		if row.Helper != "mountAdminMounts" {
			continue
		}
		if !slices.Contains(row.BuiltFields, "AdminMounts") {
			t.Fatalf("mountAdminMounts BuiltFields=%v missing AdminMounts", row.BuiltFields)
		}
		return
	}
	t.Fatal("mountAdminMounts inventory row missing")
}
