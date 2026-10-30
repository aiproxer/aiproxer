package openresponses

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestServer_MissingBearer401(t *testing.T) {
	t.Parallel()
	srv, ts := startServer(t, Options{AllowMissingBearer: false}, &Script{
		ID: "scenario-auth", Description: "auth required", Mode: ModeJSON,
		Resource: NewResource("r", "m", 1, nil),
	})
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/responses", strings.NewReader(`{"model":"m","input":"hi"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status: %d body: %s", resp.StatusCode, raw)
	}
	if !strings.Contains(string(raw), "invalid_api_key") {
		t.Fatalf("body: %s", raw)
	}
	// The unauthorized request still arrives and is captured/counted.
	if obs, ok := srv.Capture().Last(); !ok || obs.Method != http.MethodPost {
		t.Fatalf("unauthorized request not captured")
	}
}

func TestServer_WrongBearer401(t *testing.T) {
	t.Parallel()
	_, ts := startServer(t, Options{RequiredBearer: "sk-good"}, &Script{
		ID: "scenario-auth", Description: "auth required", Mode: ModeJSON,
		Resource: NewResource("r", "m", 1, nil),
	})
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/responses", strings.NewReader(`{"model":"m","input":"hi"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer sk-wrong")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status: %d", resp.StatusCode)
	}

	req2, _ := http.NewRequest(http.MethodPost, ts.URL+"/responses", strings.NewReader(`{"model":"m","input":"hi"}`))
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("Authorization", "Bearer sk-good")
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp2.Body.Close() }()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("good bearer status: %d", resp2.StatusCode)
	}
}

func TestServer_RateLimit429RetryAfter(t *testing.T) {
	t.Parallel()
	_, ts := startServer(t, Options{}, &Script{
		ID: "scenario-rate", Description: "rate limited", Mode: ModeJSON,
		Resource: NewResource("r", "m", 1, nil),
		Error: &ErrorStep{
			Status: 429, Type: "requests", Code: "rate_limit_exceeded",
			Message: "Too many requests", Param: "requests", RetryAfter: "42",
		},
	})
	resp, raw := postJSON(t, ts.URL+"/responses", `{"model":"m","input":"hi"}`)
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status: %d body: %s", resp.StatusCode, raw)
	}
	if got := resp.Header.Get("Retry-After"); got != "42" {
		t.Fatalf("retry-after: %q", got)
	}
	if !strings.Contains(string(raw), "rate_limit_exceeded") {
		t.Fatalf("body: %s", raw)
	}
}

func TestServer_ErrorStepMatrix(t *testing.T) {
	t.Parallel()
	cases := []struct {
		status int
		code   string
	}{
		{http.StatusBadRequest, "invalid_request"},
		{http.StatusNotFound, "not_found"},
		{http.StatusUnprocessableEntity, "model_required"},
		{http.StatusInternalServerError, "internal_error"},
		{http.StatusBadGateway, "upstream_error"},
		{http.StatusServiceUnavailable, "overloaded"},
	}
	for _, tc := range cases {
		t.Run(tc.code, func(t *testing.T) {
			t.Parallel()
			_, ts := startServer(t, Options{}, &Script{
				ID: "scenario-err", Description: "forced error", Mode: ModeJSON,
				Resource: NewResource("r", "m", 1, nil),
				Error:    &ErrorStep{Status: tc.status, Type: "server_error", Code: tc.code, Message: "boom"},
			})
			resp, raw := postJSON(t, ts.URL+"/responses", `{"model":"m","input":"hi"}`)
			if resp.StatusCode != tc.status {
				t.Fatalf("status: %d want %d", resp.StatusCode, tc.status)
			}
			if !strings.Contains(string(raw), tc.code) {
				t.Fatalf("body missing %s: %s", tc.code, raw)
			}
		})
	}
}

func TestServer_ErrorStatusDefaults400(t *testing.T) {
	t.Parallel()
	_, ts := startServer(t, Options{}, &Script{
		ID: "scenario-err", Description: "forced error", Mode: ModeJSON,
		Resource: NewResource("r", "m", 1, nil),
		Error:    &ErrorStep{Type: "invalid_request", Code: "bad_request", Message: "bad"},
	})
	resp, _ := postJSON(t, ts.URL+"/responses", `{"model":"m","input":"hi"}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status: %d", resp.StatusCode)
	}
}

func TestServer_AuthNoneRejectsBearer(t *testing.T) {
	t.Parallel()
	srv, ts := startServer(t, Options{}, &Script{
		ID: "scenario-noauth", Description: "no auth allowed", Mode: ModeJSON,
		Expected: ExpectedRequest{Auth: AuthNone},
		Resource: NewResource("r", "m", 1, nil),
	})
	resp, _ := postJSON(t, ts.URL+"/responses", `{"model":"m","input":"hi"}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	if srv.MismatchCount() != 1 {
		t.Fatalf("mismatch count: %d", srv.MismatchCount())
	}
}
