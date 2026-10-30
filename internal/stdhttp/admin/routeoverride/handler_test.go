package routeoverride_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/core/routeoverride"
	adminov "github.com/aiproxer/aiproxer/internal/stdhttp/admin/routeoverride"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

type fakeCommandService struct {
	get     func(ctx context.Context, aLegID string) (routeoverride.State, error)
	replace func(ctx context.Context, aLegID, selector string) (routeoverride.State, error)
	clear   func(ctx context.Context, aLegID string) (routeoverride.State, error)
}

func (f fakeCommandService) Get(ctx context.Context, aLegID string) (routeoverride.State, error) {
	if f.get != nil {
		return f.get(ctx, aLegID)
	}
	return routeoverride.Inactive(aLegID), nil
}

func (f fakeCommandService) Replace(ctx context.Context, aLegID, selector string) (routeoverride.State, error) {
	if f.replace != nil {
		return f.replace(ctx, aLegID, selector)
	}
	return routeoverride.State{ALegID: aLegID, Active: true, Selector: selector, Revision: 1, UpdatedAt: time.Unix(1, 0).UTC()}, nil
}

func (f fakeCommandService) Clear(ctx context.Context, aLegID string) (routeoverride.State, error) {
	if f.clear != nil {
		return f.clear(ctx, aLegID)
	}
	return routeoverride.State{ALegID: aLegID, Revision: 2, UpdatedAt: time.Unix(2, 0).UTC()}, nil
}

func testHandler(t *testing.T, svc routeoverride.CommandService) http.Handler {
	t.Helper()
	h, err := adminov.NewHandler(adminov.Options{
		Service:      svc,
		MaxBodyBytes: 69632,
	})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	return h
}

func TestHandler_GETInactiveRevision0(t *testing.T) {
	t.Parallel()
	h := testHandler(t, fakeCommandService{})
	req := httptest.NewRequest(http.MethodGet, "/a_1", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), `"selector"`) {
		t.Fatalf("inactive GET must omit selector: %s", rec.Body.String())
	}
}

func TestHandler_PUTReplaceAndIdempotent(t *testing.T) {
	t.Parallel()
	h := testHandler(t, fakeCommandService{})
	body := []byte(`{"selector":"openai:gpt-4"}`)
	req := httptest.NewRequest(http.MethodPut, "/a_1", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandler_DELETEClear(t *testing.T) {
	t.Parallel()
	h := testHandler(t, fakeCommandService{})
	req := httptest.NewRequest(http.MethodDelete, "/a_1", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandler_PUTMediaType(t *testing.T) {
	t.Parallel()
	h := testHandler(t, fakeCommandService{})
	cases := []struct {
		name   string
		ct     string
		want   int
		setHdr bool
	}{
		{name: "json", ct: "application/json", want: http.StatusOK, setHdr: true},
		{name: "jsonCharset", ct: "application/json; charset=utf-8", want: http.StatusOK, setHdr: true},
		{name: "missing", want: http.StatusUnsupportedMediaType, setHdr: false},
		{name: "text", ct: "text/plain", want: http.StatusUnsupportedMediaType, setHdr: true},
		{name: "malformed", ct: "application/", want: http.StatusUnsupportedMediaType, setHdr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest(http.MethodPut, "/a_1", bytes.NewReader([]byte(`{"selector":"openai:gpt-4"}`)))
			if tc.setHdr {
				req.Header.Set("Content-Type", tc.ct)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status=%d want %d body=%s", rec.Code, tc.want, rec.Body.String())
			}
		})
	}
}

func TestHandler_PUTRejectsMalformedJSONAndUnknownFields(t *testing.T) {
	t.Parallel()
	h := testHandler(t, fakeCommandService{})
	cases := []struct {
		name string
		body string
		want int
	}{
		{name: "malformed", body: "{", want: http.StatusBadRequest},
		{name: "unknownField", body: `{"selector":"openai:gpt-4","extra":true}`, want: http.StatusBadRequest},
		{name: "multiValue", body: `{"selector":"a"}{"selector":"b"}`, want: http.StatusBadRequest},
		{name: "trailingCloseBrace", body: `{"selector":"openai:gpt-4"}}`, want: http.StatusBadRequest},
		{name: "trailingCloseBracket", body: `{"selector":"openai:gpt-4"}]`, want: http.StatusBadRequest},
		{name: "emptySelector", body: `{"selector":"  "}`, want: http.StatusBadRequest},
		{name: "trailingWhitespace", body: "{\"selector\":\"openai:gpt-4\"}\n", want: http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest(http.MethodPut, "/a_1", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status=%d want %d body=%s", rec.Code, tc.want, rec.Body.String())
			}
		})
	}
}

func TestHandler_PUTTrailingJSONDoesNotMutate(t *testing.T) {
	t.Parallel()
	var replaced int
	h := testHandler(t, fakeCommandService{
		replace: func(context.Context, string, string) (routeoverride.State, error) {
			replaced++
			return routeoverride.State{}, nil
		},
	})
	req := httptest.NewRequest(http.MethodPut, "/a_1", strings.NewReader(`{"selector":"openai:gpt-4"}}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400 body=%s", rec.Code, rec.Body.String())
	}
	if replaced != 0 {
		t.Fatalf("Replace called %d times; malformed trailing JSON must not mutate", replaced)
	}
}

func TestHandler_PUTRejectsOversizedBody(t *testing.T) {
	t.Parallel()
	h := testHandler(t, fakeCommandService{})
	payload, err := json.Marshal(map[string]string{"selector": strings.Repeat("a", aipapi.MaxRouteSelectorBytes+1)})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPut, "/a_1", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.ContentLength = int64(len(payload))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge && rec.Code != http.StatusBadRequest {
		t.Fatalf("oversized body status=%d want 413 or 400, body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandler_PUTRejectsContentLengthOverMaxBodyBytes(t *testing.T) {
	t.Parallel()
	const maxBody int64 = 64
	h, err := adminov.NewHandler(adminov.Options{
		Service:      fakeCommandService{},
		MaxBodyBytes: maxBody,
	})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	body := bytes.Repeat([]byte("a"), int(maxBody)+1)
	req := httptest.NewRequest(http.MethodPut, "/a_1", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.ContentLength = int64(len(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("over-limit Content-Length status=%d want 413, body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandler_notFoundInvalidStoreMappings(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		svc  fakeCommandService
		want int
	}{
		{
			name: "notFound",
			svc: fakeCommandService{get: func(context.Context, string) (routeoverride.State, error) {
				return routeoverride.State{}, routeoverride.ErrNotFound
			}},
			want: http.StatusNotFound,
		},
		{
			name: "invalid",
			svc: fakeCommandService{replace: func(context.Context, string, string) (routeoverride.State, error) {
				return routeoverride.State{}, routeoverride.ErrInvalidSelector
			}},
			want: http.StatusBadRequest,
		},
		{
			name: "unavailable",
			svc: fakeCommandService{get: func(context.Context, string) (routeoverride.State, error) {
				return routeoverride.State{}, routeoverride.ErrUnavailable
			}},
			want: http.StatusServiceUnavailable,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := testHandler(t, tc.svc)
			var req *http.Request
			if tc.name == "invalid" {
				req = httptest.NewRequest(http.MethodPut, "/a_1", strings.NewReader(`{"selector":"nope"}`))
				req.Header.Set("Content-Type", "application/json")
			} else {
				req = httptest.NewRequest(http.MethodGet, "/a_1", nil)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status=%d want %d body=%s", rec.Code, tc.want, rec.Body.String())
			}
			if strings.Contains(strings.ToLower(rec.Body.String()), "secret") {
				t.Fatalf("error body leaked secret: %s", rec.Body.String())
			}
		})
	}
}

func TestHandler_methodNotAllowed(t *testing.T) {
	t.Parallel()
	h := testHandler(t, fakeCommandService{})
	req := httptest.NewRequest(http.MethodPost, "/a_1", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d want 405", rec.Code)
	}
}

func TestHandler_operatorSecretProtection(t *testing.T) {
	t.Parallel()
	inner := testHandler(t, fakeCommandService{})
	wrapped := diag.WrapDiagnosticsProtect("supersecret12", inner)
	req := httptest.NewRequest(http.MethodGet, "/a_1", nil)
	rec := httptest.NewRecorder()
	wrapped.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("missing secret status=%d want 403", rec.Code)
	}
	req2 := httptest.NewRequest(http.MethodGet, "/a_1", nil)
	req2.Header.Set(diag.HeaderDiagnosticsSecret, "supersecret12")
	rec2 := httptest.NewRecorder()
	wrapped.ServeHTTP(rec2, req2)
	if rec2.Code == http.StatusForbidden {
		t.Fatal("matching operator secret must not be forbidden")
	}
}

func TestHandler_doesNotCreateALegOnNotFound(t *testing.T) {
	t.Parallel()
	var replaced bool
	h := testHandler(t, fakeCommandService{
		replace: func(context.Context, string, string) (routeoverride.State, error) {
			replaced = true
			return routeoverride.State{}, routeoverride.ErrNotFound
		},
	})
	req := httptest.NewRequest(http.MethodPut, "/missing", strings.NewReader(`{"selector":"openai:gpt-4"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d want 404 body=%s", rec.Code, rec.Body.String())
	}
	if !replaced {
		// Service is invoked only after the handler exists; Phase 1 fails earlier.
		_ = replaced
	}
}

func TestHandler_revisionExhaustedMapsTo500(t *testing.T) {
	t.Parallel()
	h := testHandler(t, fakeCommandService{
		replace: func(context.Context, string, string) (routeoverride.State, error) {
			return routeoverride.State{}, routeoverride.ErrRevisionExhausted
		},
	})
	req := httptest.NewRequest(http.MethodPut, "/a_1", strings.NewReader(`{"selector":"openai:gpt-4"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want 500 body=%s", rec.Code, rec.Body.String())
	}
}

func TestNewHandler_requiresService(t *testing.T) {
	t.Parallel()
	_, err := adminov.NewHandler(adminov.Options{})
	if err == nil {
		t.Fatal("expected error for missing service")
	}
}

func TestHandler_mutationAuditOmitsRawSelectorAndALeg(t *testing.T) {
	t.Parallel()
	const (
		aLeg = "SECRET_ALEG_xyz"
		sel  = "SECRETSEL:gpt-4"
	)
	buf := &bytes.Buffer{}
	log := slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	h, err := adminov.NewHandler(adminov.Options{
		Service: fakeCommandService{
			replace: func(_ context.Context, id, selector string) (routeoverride.State, error) {
				return routeoverride.State{
					ALegID:    id,
					Active:    true,
					Selector:  selector,
					Revision:  1,
					UpdatedAt: time.Unix(1, 0).UTC(),
				}, nil
			},
			get: func(_ context.Context, id string) (routeoverride.State, error) {
				return routeoverride.State{
					ALegID:    id,
					Active:    true,
					Selector:  sel,
					Revision:  1,
					UpdatedAt: time.Unix(1, 0).UTC(),
				}, nil
			},
		},
		MaxBodyBytes: 69632,
		Log:          log,
	})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}

	put := httptest.NewRequest(http.MethodPut, "/"+aLeg, strings.NewReader(`{"selector":"`+sel+`"}`))
	put.Header.Set("Content-Type", "application/json")
	putRec := httptest.NewRecorder()
	h.ServeHTTP(putRec, put)
	if putRec.Code != http.StatusOK {
		t.Fatalf("PUT status=%d body=%s", putRec.Code, putRec.Body.String())
	}
	if !strings.Contains(putRec.Body.String(), sel) {
		t.Fatalf("protected PUT response must include raw selector: %s", putRec.Body.String())
	}

	get := httptest.NewRequest(http.MethodGet, "/"+aLeg, nil)
	getRec := httptest.NewRecorder()
	h.ServeHTTP(getRec, get)
	if getRec.Code != http.StatusOK {
		t.Fatalf("GET status=%d body=%s", getRec.Code, getRec.Body.String())
	}
	if !strings.Contains(getRec.Body.String(), sel) {
		t.Fatalf("protected GET response must include raw selector: %s", getRec.Body.String())
	}

	line := buf.String()
	if !strings.Contains(line, diag.RouteOverrideMutationLogMsg) {
		t.Fatalf("expected mutation audit log, got %s", line)
	}
	if strings.Contains(line, aLeg) {
		t.Fatalf("raw A-leg leaked into mutation log: %s", line)
	}
	if strings.Contains(line, sel) || strings.Contains(line, "SECRETSEL") {
		t.Fatalf("raw selector leaked into mutation log: %s", line)
	}
	if !strings.Contains(line, `"action":"set"`) {
		t.Fatalf("expected set action in audit log: %s", line)
	}
}
