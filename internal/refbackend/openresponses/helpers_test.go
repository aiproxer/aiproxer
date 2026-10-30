package openresponses

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// fakeRequest builds a request for expectation checks without a real server.
func fakeRequest(method, path, contentType, auth string) *http.Request {
	req := httptest.NewRequest(method, "http://emulator.invalid"+path, nil)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	return req
}

// startServer spins up a scripted emulator server over httptest, selecting the
// first script. Default options require a bearer token (as the reference
// provider does); tests that need open auth pass Options{AllowMissingBearer:true}.
func startServer(t *testing.T, opts Options, scripts ...*Script) (*Server, *httptest.Server) {
	t.Helper()
	srv := NewServer(opts)
	if err := srv.Register(scripts...); err != nil {
		t.Fatalf("register scripts: %v", err)
	}
	if err := srv.Select(scripts[0].ID); err != nil {
		t.Fatalf("select %s: %v", scripts[0].ID, err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return srv, ts
}
