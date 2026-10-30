package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	coreauth "github.com/aiproxer/aiproxer/internal/core/auth"
	frontanthropic "github.com/aiproxer/aiproxer/internal/plugins/frontends/anthropic"
	frontgemini "github.com/aiproxer/aiproxer/internal/plugins/frontends/gemini"
	frontopenailegacy "github.com/aiproxer/aiproxer/internal/plugins/frontends/openailegacy"
	frontopenairesponses "github.com/aiproxer/aiproxer/internal/plugins/frontends/openairesponses"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auth"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/transport/httpauth"
)

type panicExecutorView struct{}

func (panicExecutorView) Execute(context.Context, *aipapi.Call) (aipapi.EventStream, error) {
	panic("inner_must_not_run")
}

func (panicExecutorView) WallClock() func() time.Time { return nil }

func (panicExecutorView) CancelALeg(context.Context, aipapi.ALegCancelRequest) error {
	panic("inner_must_not_run")
}

func TestBundledFrontends_authRequired_missingBearer_terminatesWithJSONAndSkipsInner(t *testing.T) {
	t.Parallel()
	ak, err := coreauth.NewLocalAPIKeyAuthenticator([]coreauth.LocalAPIKeyRecord{
		{KeyID: "kid", PrincipalID: "p1", Key: "test-local-api-key-16"},
	})
	if err != nil {
		t.Fatal(err)
	}
	pa := coreauth.PolicyAuthenticator{
		Handler:  auth.HandlerLocalAPIKey,
		Required: auth.LevelAPIKey,
		APIKey:   ak,
	}
	disp := coreauth.NewEventDispatcher(&orderSink{}, coreauth.EventFailureBestEffort)
	prov := NewPolicyProvider(&pa, disp, PolicySnapshot{
		AccessMode: auth.AccessMultiUser, HandlerKind: auth.HandlerLocalAPIKey, RequiredLevel: auth.LevelAPIKey,
	}, nil)

	inner := panicExecutorView{}
	route := "stub:gpt-4o-mini"
	cases := []struct {
		name string
		path string
	}{
		{"openai_responses", "/v1/responses"},
		{"openai_legacy", "/v1/chat/completions"},
		{"anthropic", "/v1/messages"},
		{"gemini", "/v1beta/models/gemini-2.0-flash:generateContent"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			mux := http.NewServeMux()
			switch tc.name {
			case "openai_responses":
				mux.Handle("/v1/responses", &frontopenairesponses.Handler{
					Exec:                 inner,
					DefaultRouteSelector: route,
				})
			case "openai_legacy":
				mux.Handle("/v1/chat/completions", &frontopenailegacy.Handler{
					Exec:                 inner,
					DefaultRouteSelector: route,
				})
			case "anthropic":
				mux.Handle("/v1/messages", &frontanthropic.Handler{
					Exec:                 inner,
					DefaultRouteSelector: route,
				})
			case "gemini":
				mux.Handle("/v1beta/", &frontgemini.Handler{
					Exec:                 inner,
					DefaultRouteSelector: route,
				})
			default:
				t.Fatalf("unknown case %q", tc.name)
			}
			h := Middleware(nil, []httpauth.Provider{prov}, mux)
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusUnauthorized && rec.Code != http.StatusForbidden {
				t.Fatalf("want 401/403, got %d body=%q", rec.Code, rec.Body.String())
			}
			ct := rec.Header().Get("Content-Type")
			if ct == "" || !json.Valid(rec.Body.Bytes()) {
				t.Fatalf("want JSON content-type and body, ct=%q body=%q", ct, rec.Body.String())
			}
			var wrap struct {
				Error *struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &wrap); err != nil || wrap.Error == nil || wrap.Error.Code == "" {
				t.Fatalf("expected error object with code, got body=%q err=%v", rec.Body.String(), err)
			}
		})
	}
}

func TestBundledFrontends_authRequired_invalidBearer_skipsInner(t *testing.T) {
	t.Parallel()
	ak, err := coreauth.NewLocalAPIKeyAuthenticator([]coreauth.LocalAPIKeyRecord{
		{KeyID: "kid", PrincipalID: "p1", Key: "test-local-api-key-16"},
	})
	if err != nil {
		t.Fatal(err)
	}
	pa := coreauth.PolicyAuthenticator{Handler: auth.HandlerLocalAPIKey, Required: auth.LevelAPIKey, APIKey: ak}
	disp := coreauth.NewEventDispatcher(&orderSink{}, coreauth.EventFailureBestEffort)
	prov := NewPolicyProvider(&pa, disp, PolicySnapshot{
		AccessMode: auth.AccessMultiUser, HandlerKind: auth.HandlerLocalAPIKey, RequiredLevel: auth.LevelAPIKey,
	}, nil)
	mux := http.NewServeMux()
	mux.Handle("/v1/responses", &frontopenairesponses.Handler{Exec: panicExecutorView{}, DefaultRouteSelector: "stub:m"})
	h := Middleware(nil, []httpauth.Provider{prov}, mux)
	req := httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code == http.StatusOK {
		t.Fatalf("unexpected 200")
	}
}
