package openresponses_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openresponses"
)

// TestDecode_MissingDefaultRouteNamesTheRemedy proves a request whose model
// carries no known route prefix, in a configuration without a default route, is
// refused with the reason and the remedy instead of an unexplained
// "model could not be resolved".
func TestDecode_MissingDefaultRouteNamesTheRemedy(t *testing.T) {
	t.Parallel()

	_, err := openresponses.AuthenticateAndDecodeCreate(context.Background(), []byte(`{
		"model": "plain-model",
		"input": "hello"
	}`), openresponses.DecodeCreateOptions{
		Auth:          &mockAuthorizer{authenticated: true},
		RoutePrefixes: []string{"backend"},
	})
	if err == nil {
		t.Fatal("expected a refusal for the unresolvable route")
	}
	if !strings.Contains(err.Error(), "route selector is required") {
		t.Fatalf("error = %v", err)
	}
	if !strings.Contains(err.Error(), "backend:model") {
		t.Fatalf("error must name the remedy: %v", err)
	}
}

// TestDecode_MissingDefaultRouteRefusesWithoutWork proves the request never
// reaches the executor.
func TestDecode_MissingDefaultRouteRefusesWithoutWork(t *testing.T) {
	t.Parallel()

	auth := &mockAuthorizer{authenticated: true}
	exec := &mockExecutor{}

	handler := openresponses.NewHandler(openresponses.HandlerConfig{
		AllowUnauthenticated: true,
		Authorizer:           auth,
		Executor:             exec,
		RoutePrefixes:        []string{"backend"},
	})

	req := httptest.NewRequest(http.MethodPost, "/openresponses/v1/responses", strings.NewReader(`{"model": "plain-model", "input": "hello"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d want 400", rec.Code)
	}
	if exec.executeCalls != 0 {
		t.Fatalf("executor received %d calls, want 0", exec.executeCalls)
	}
}

// TestDecode_ConfiguredDefaultRouteStillResolves proves the diagnostic does not
// change the configured default route path.
func TestDecode_ConfiguredDefaultRouteStillResolves(t *testing.T) {
	t.Parallel()

	decoded, err := openresponses.AuthenticateAndDecodeCreate(context.Background(), []byte(`{
		"model": "plain-model",
		"input": "hello"
	}`), openresponses.DecodeCreateOptions{
		Auth:                 &mockAuthorizer{authenticated: true},
		DefaultRouteSelector: "backend:default",
		RoutePrefixes:        []string{"backend"},
	})
	if err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if got := decoded.Call.Route.Selector; got != "backend:default" {
		t.Fatalf("route selector = %q want backend:default", got)
	}
}
