package openairesponses_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/securesession/domain"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openairesponses"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/sessionwire"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

func TestDecodeCreateRequest_sessionMetadataAndHeaders(t *testing.T) {
	t.Parallel()
	body := `{
	  "model": "gpt-4o-mini",
	  "input": "hello",
	  "metadata": {
	    "` + sessionwire.MetaKeyAuthoritativeSessionID + `": "meta-sid",
	    "` + sessionwire.MetaKeyResumeToken + `": "meta-tok"
	  }
	}`
	h := http.Header{}
	h.Set(sessionwire.HeaderAuthoritativeSessionID, "hdr-sid")
	h.Set(sessionwire.HeaderResumeToken, "hdr-tok")
	d, err := openairesponses.DecodeCreateRequest([]byte(body), openairesponses.DecodeOptions{
		RouteSelector: "stub:x",
		Headers:       h,
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.Call.Session.AuthoritativeSessionID != "hdr-sid" {
		t.Fatalf("AuthoritativeSessionID: %q", d.Call.Session.AuthoritativeSessionID)
	}
	if d.Call.Session.ResumeToken != "hdr-tok" {
		t.Fatalf("ResumeToken: %q", d.Call.Session.ResumeToken)
	}
}

func TestDecodeCreateRequest_sessionMetadataOnly(t *testing.T) {
	t.Parallel()
	body := `{
	  "model": "gpt-4o-mini",
	  "input": "hello",
	  "metadata": {
	    "` + sessionwire.MetaKeyAuthoritativeSessionID + `": "only-sid",
	    "` + sessionwire.MetaKeyResumeToken + `": "only-tok"
	  }
	}`
	d, err := openairesponses.DecodeCreateRequest([]byte(body), openairesponses.DecodeOptions{
		RouteSelector: "stub:x",
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.Call.Session.AuthoritativeSessionID != "only-sid" || d.Call.Session.ResumeToken != "only-tok" {
		t.Fatalf("session: %+v", d.Call.Session)
	}
}

type sessionDenyExecutor struct{}

func (sessionDenyExecutor) Execute(ctx context.Context, call *aipapi.Call) (aipapi.EventStream, error) {
	_ = ctx
	_ = call
	return nil, aipapi.NewSessionDenialInvalidAuthority("super-secret-internal")
}

func (sessionDenyExecutor) WallClock() func() time.Time { return nil }

func (sessionDenyExecutor) CancelALeg(context.Context, aipapi.ALegCancelRequest) error { return nil }

type cancelExecutor struct {
	canceled aipapi.ALegCancelRequest
	err      error
}

func (e *cancelExecutor) Execute(ctx context.Context, call *aipapi.Call) (aipapi.EventStream, error) {
	_ = ctx
	_ = call
	return aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventResponseFinished}}), nil
}

func (e *cancelExecutor) WallClock() func() time.Time { return nil }

func (e *cancelExecutor) CancelALeg(ctx context.Context, req aipapi.ALegCancelRequest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if e.err != nil {
		return e.err
	}
	e.canceled = req
	return nil
}

func TestHandler_sessionDenial_nonEnumeratingJSON(t *testing.T) {
	t.Parallel()
	h := &openairesponses.Handler{
		Exec:                 sessionDenyExecutor{},
		DefaultRouteSelector: "stub:x",
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	req, err := http.NewRequest(http.MethodPost, srv.URL+"/v1/responses", strings.NewReader(`{"model":"gpt-4o-mini","input":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	var payload struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(payload.Error.Message, "super-secret") {
		t.Fatalf("message leaked internal: %q", payload.Error.Message)
	}
	if payload.Error.Type != "invalid_request_error" {
		t.Fatalf("error.type: %q", payload.Error.Type)
	}
}

func TestHandler_cancelResponseCancelsALegFromCarrier(t *testing.T) {
	t.Parallel()
	ex := &cancelExecutor{}
	h := &openairesponses.Handler{Exec: ex}
	req := httptest.NewRequest(http.MethodPost, "/v1/responses/resp_abc/cancel", nil)
	req.Header.Set(sessionwire.HeaderALegID, "a-leg-1")
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	if ex.canceled.ALegID != "a-leg-1" {
		t.Fatalf("canceled A-leg = %q", ex.canceled.ALegID)
	}
	if !strings.Contains(rr.Body.String(), `"status":"cancelled"`) {
		t.Fatalf("cancel response body = %s", rr.Body.String())
	}
}

func TestHandler_cancelResponseRejectsMissingResponseID(t *testing.T) {
	t.Parallel()
	ex := &cancelExecutor{}
	h := &openairesponses.Handler{Exec: ex}
	req := httptest.NewRequest(http.MethodPost, "/v1/responses/cancel", nil)
	req.Header.Set(sessionwire.HeaderALegID, "a-leg-1")
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	if ex.canceled.ALegID != "" {
		t.Fatalf("unexpected cancellation request: %+v", ex.canceled)
	}
}

func TestHandler_cancelResponseErrorStatusMapping(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		err        error
		wantStatus int
		wantType   string
	}{
		{name: "missing principal", err: domain.ErrMissingPrincipal, wantStatus: http.StatusUnauthorized, wantType: "authentication_error"},
		{name: "owner mismatch", err: domain.ErrOwnerMismatch, wantStatus: http.StatusForbidden, wantType: "invalid_request_error"},
		{name: "session not found", err: domain.ErrSessionNotFound, wantStatus: http.StatusNotFound, wantType: "invalid_request_error"},
		{name: "internal", err: errors.New("database down"), wantStatus: http.StatusInternalServerError, wantType: "api_error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := &openairesponses.Handler{Exec: &cancelExecutor{err: tc.err}}
			req := httptest.NewRequest(http.MethodPost, "/v1/responses/resp_abc/cancel", nil)
			req.Header.Set(sessionwire.HeaderALegID, "a-leg-1")
			rr := httptest.NewRecorder()

			h.ServeHTTP(rr, req)

			if rr.Code != tc.wantStatus {
				t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
			}
			var payload struct {
				Error struct {
					Type string `json:"type"`
				} `json:"error"`
			}
			if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
				t.Fatal(err)
			}
			if payload.Error.Type != tc.wantType {
				t.Fatalf("error.type = %q want %q", payload.Error.Type, tc.wantType)
			}
		})
	}
}

var _ aipsdk.ExecutorView = sessionDenyExecutor{}
