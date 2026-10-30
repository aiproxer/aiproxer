package auth_test

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/stdhttp/auth"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/transport/httpauth"
)

func TestMiddleware_noProviders_passthrough(t *testing.T) {
	t.Parallel()
	var saw bool
	h := auth.Middleware(nil, nil, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		saw = true
		w.WriteHeader(http.StatusTeapot)
	}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusTeapot || !saw {
		t.Fatalf("code=%d saw=%v", rec.Code, saw)
	}
}

func TestMiddleware_onlyNilProviders_failClosed500(t *testing.T) {
	t.Parallel()
	var inner bool
	h := auth.Middleware(nil, []httpauth.Provider{nil}, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		inner = true
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if inner {
		t.Fatal("inner handler must not run")
	}
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("want %d, got %d", http.StatusInternalServerError, rec.Code)
	}
}

func TestMiddleware_onlyNilProviders_logsMisconfiguration(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelError}))
	h := auth.Middleware(log, []httpauth.Provider{nil}, http.NotFoundHandler())
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	if !strings.Contains(buf.String(), "all_httpauth_providers_nil") {
		t.Fatalf("expected misconfiguration log, got %q", buf.String())
	}
}

func TestMiddleware_nilRequest_badRequest(t *testing.T) {
	t.Parallel()
	p := stubProvider{res: httpauth.AuthenticationResult{
		Type:      httpauth.TypePrincipal,
		Principal: execview.PrincipalView{ID: "x"},
	}}
	h := auth.Middleware(nil, []httpauth.Provider{p}, http.NotFoundHandler())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want %d, got %d", http.StatusBadRequest, rec.Code)
	}
}

func TestMiddleware_unknownProviderResult_failClosed500(t *testing.T) {
	t.Parallel()
	const unknownAuthType httpauth.AuthenticationType = 99
	p := stubProvider{res: httpauth.AuthenticationResult{Type: unknownAuthType}}
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	rec := httptest.NewRecorder()
	auth.Middleware(log, []httpauth.Provider{p}, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("inner must not run")
	})).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("want %d, got %d", http.StatusInternalServerError, rec.Code)
	}
	if !strings.Contains(buf.String(), "unknown result type") {
		t.Fatalf("expected warn log, got %q", buf.String())
	}
}

func TestMiddleware_nilThenReal_skipsNilRunsReal(t *testing.T) {
	t.Parallel()
	want := execview.PrincipalView{ID: "after-nil"}
	p := stubProvider{res: httpauth.AuthenticationResult{Type: httpauth.TypePrincipal, Principal: want}}
	var got string
	h := auth.Middleware(nil, []httpauth.Provider{nil, p}, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		pg, _ := httpauth.PrincipalFromContext(r.Context())
		got = pg.ID
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("code %d", rec.Code)
	}
	if got != "after-nil" {
		t.Fatalf("principal %q", got)
	}
}

func TestMiddleware_principalPropagates(t *testing.T) {
	t.Parallel()
	want := execview.PrincipalView{ID: "alice"}
	p := stubProvider{res: httpauth.AuthenticationResult{Type: httpauth.TypePrincipal, Principal: want}}
	var gotCtx context.Context
	h := auth.Middleware(nil, []httpauth.Provider{p}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCtx = r.Context()
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code %d", rec.Code)
	}
	pg, ok := httpauth.PrincipalFromContext(gotCtx)
	if !ok || pg.ID != "alice" {
		t.Fatalf("principal %+v ok=%v", pg, ok)
	}
}

func TestMiddleware_frontendIDPropagatesFromPath(t *testing.T) {
	t.Parallel()
	p := stubProvider{res: httpauth.AuthenticationResult{
		Type:      httpauth.TypePrincipal,
		Principal: execview.PrincipalView{ID: "u"},
	}}
	var gotFe string
	var gotOK bool
	h := auth.Middleware(nil, []httpauth.Provider{p}, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		gotFe, gotOK = execview.FrontendIDFromContext(r.Context())
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/messages", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("code %d", rec.Code)
	}
	if !gotOK || gotFe != "anthropic" {
		t.Fatalf("frontend id %q ok=%v", gotFe, gotOK)
	}
}

func TestMiddleware_reject(t *testing.T) {
	t.Parallel()
	p := stubProvider{res: httpauth.AuthenticationResult{
		Type:       httpauth.TypeReject,
		HTTPStatus: http.StatusForbidden,
		Body:       []byte("nope"),
	}}
	var inner bool
	h := auth.Middleware(nil, []httpauth.Provider{p}, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		inner = true
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusForbidden || rec.Body.String() != "nope" || inner {
		t.Fatalf("code=%d body=%q inner=%v", rec.Code, rec.Body.String(), inner)
	}
}

func TestMiddleware_annotate_allowList(t *testing.T) {
	t.Parallel()
	rh := http.Header{}
	rh.Set("Vary", "Accept-Encoding")
	rh.Set("Set-Cookie", "session=evil; Path=/")
	p := stubProvider{res: httpauth.AuthenticationResult{
		Type:            httpauth.TypeAnnotate,
		ResponseHeaders: rh,
	}}
	rec := httptest.NewRecorder()
	auth.Middleware(nil, []httpauth.Provider{p}, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if got := rec.Header().Values("Vary"); len(got) != 1 || got[0] != "Accept-Encoding" {
		t.Fatalf("Vary: %q", got)
	}
	if rec.Header().Get("Set-Cookie") != "" {
		t.Fatalf("Set-Cookie should be stripped, got %q", rec.Header().Get("Set-Cookie"))
	}
}

func TestMiddleware_annotate_allowList_logsDropped(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	rh := http.Header{}
	rh.Set("Set-Cookie", "x=1")
	p := stubProvider{res: httpauth.AuthenticationResult{
		Type:            httpauth.TypeAnnotate,
		ResponseHeaders: rh,
	}}
	auth.Middleware(log, []httpauth.Provider{p}, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	if !strings.Contains(buf.String(), "Set-Cookie") {
		t.Fatalf("expected warn log about dropped header, got %q", buf.String())
	}
}

func TestMiddleware_reject_contentType(t *testing.T) {
	t.Parallel()
	p := stubProvider{res: httpauth.AuthenticationResult{
		Type:        httpauth.TypeReject,
		HTTPStatus:  http.StatusUnauthorized,
		Body:        []byte(`{"err":1}`),
		ContentType: "application/json; charset=utf-8",
	}}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	mw := auth.Middleware(nil, []httpauth.Provider{p}, http.NotFoundHandler())
	mw.ServeHTTP(rec, req)
	if got := rec.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Fatalf("content-type: %q", got)
	}
}

func TestMiddleware_challenge(t *testing.T) {
	t.Parallel()
	hd := http.Header{}
	hd.Set("WWW-Authenticate", `Bearer realm="aip"`)
	p := stubProvider{res: httpauth.AuthenticationResult{
		Type:       httpauth.TypeChallenge,
		HTTPStatus: http.StatusUnauthorized,
		Headers:    hd,
	}}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	mw := auth.Middleware(nil, []httpauth.Provider{p}, http.NotFoundHandler())
	mw.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("code %d", rec.Code)
	}
	if got := rec.Header().Get("WWW-Authenticate"); got == "" {
		t.Fatal("missing challenge header")
	}
}

func TestMiddleware_termination_headerAllowList(t *testing.T) {
	t.Parallel()
	hd := http.Header{}
	hd.Set("WWW-Authenticate", `Bearer realm="aip"`)
	hd.Set("Cache-Control", "no-store")
	hd.Set("Set-Cookie", "sid=evil; Path=/")
	hd.Set("Location", "https://evil.example/phish")
	p := stubProvider{res: httpauth.AuthenticationResult{
		Type:       httpauth.TypeReject,
		HTTPStatus: http.StatusForbidden,
		Headers:    hd,
		Body:       []byte("no"),
	}}
	rec := httptest.NewRecorder()
	auth.Middleware(nil, []httpauth.Provider{p}, http.NotFoundHandler()).
		ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Header().Get("Set-Cookie") != "" {
		t.Fatalf("Set-Cookie must be stripped, got %q", rec.Header().Get("Set-Cookie"))
	}
	if rec.Header().Get("Location") != "" {
		t.Fatalf("Location must be stripped, got %q", rec.Header().Get("Location"))
	}
	if got := rec.Header().Get("WWW-Authenticate"); got == "" {
		t.Fatal("expected WWW-Authenticate")
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control: %q", got)
	}
}

func TestMiddleware_termination_logsDroppedHeaders(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	hd := http.Header{}
	hd.Set("Set-Cookie", "x=1")
	hd.Set("Location", "/elsewhere")
	p := stubProvider{res: httpauth.AuthenticationResult{
		Type:       httpauth.TypeChallenge,
		HTTPStatus: http.StatusUnauthorized,
		Headers:    hd,
	}}
	auth.Middleware(log, []httpauth.Provider{p}, http.NotFoundHandler()).
		ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	out := buf.String()
	if !strings.Contains(out, "Set-Cookie") || !strings.Contains(out, "Location") {
		t.Fatalf("expected warn logs for dropped headers, got %q", out)
	}
}

func TestMiddleware_providerError_failClosed(t *testing.T) {
	t.Parallel()
	p := errProvider{}
	rec := httptest.NewRecorder()
	auth.Middleware(nil, []httpauth.Provider{p}, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("inner should not run")
	})).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("code %d", rec.Code)
	}
}

func TestMiddleware_providerError_doesNotLogRawError(t *testing.T) {
	t.Parallel()
	secret := testkit.SyntheticOpenAIAPIKey
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelError}))
	h := auth.Middleware(log, []httpauth.Provider{leakyErrProvider{
		err: fmt.Errorf("backend auth failed for Authorization: Bearer %s", secret),
	}}, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("inner should not run")
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("code %d", rec.Code)
	}
	out := buf.String()
	if strings.Contains(out, secret) || strings.Contains(out, "Authorization: Bearer") {
		t.Fatalf("log leaked raw provider error: %q", out)
	}
	if !strings.Contains(out, "error_kind=") {
		t.Fatalf("expected sanitized error kind in log, got %q", out)
	}
}

func TestMiddleware_chain_ordering(t *testing.T) {
	t.Parallel()
	var order []int
	p1 := orderProvider{n: 1, order: &order, res: httpauth.AuthenticationResult{Type: httpauth.TypeContinue}}
	p2 := orderProvider{n: 2, order: &order, res: httpauth.AuthenticationResult{
		Type:      httpauth.TypePrincipal,
		Principal: execview.PrincipalView{ID: "from-2"},
	}}
	p3 := orderProvider{n: 3, order: &order, res: httpauth.AuthenticationResult{Type: httpauth.TypeContinue}}
	var pid string
	h := auth.Middleware(nil, []httpauth.Provider{p1, p2, p3}, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		p, _ := httpauth.PrincipalFromContext(r.Context())
		pid = p.ID
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	if order[0] != 1 || order[1] != 2 || order[2] != 3 {
		t.Fatalf("order %v", order)
	}
	if pid != "from-2" {
		t.Fatalf("principal id %q", pid)
	}
}

type stubProvider struct {
	res httpauth.AuthenticationResult
}

func (s stubProvider) Authenticate(ctx context.Context, _ http.ResponseWriter, _ *http.Request) (httpauth.AuthenticationResult, error) {
	_ = ctx
	return s.res, nil
}

type errProvider struct{}

func (errProvider) Authenticate(context.Context, http.ResponseWriter, *http.Request) (httpauth.AuthenticationResult, error) {
	return httpauth.AuthenticationResult{}, context.DeadlineExceeded
}

type leakyErrProvider struct {
	err error
}

func (p leakyErrProvider) Authenticate(context.Context, http.ResponseWriter, *http.Request) (httpauth.AuthenticationResult, error) {
	return httpauth.AuthenticationResult{}, p.err
}

type orderProvider struct {
	n     int
	order *[]int
	res   httpauth.AuthenticationResult
}

func (o orderProvider) Authenticate(context.Context, http.ResponseWriter, *http.Request) (httpauth.AuthenticationResult, error) {
	*o.order = append(*o.order, o.n)
	return o.res, nil
}
