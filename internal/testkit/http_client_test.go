package testkit

import (
	"net/http"
	"testing"

	"github.com/aiproxer/aiproxer/internal/infra/httpclient"
)

func TestIntegrationHTTPClient_nonNilPassthrough(t *testing.T) {
	t.Parallel()
	c := &http.Client{}
	if got := IntegrationHTTPClient(c); got != c {
		t.Fatalf("want same client pointer")
	}
}

func TestIntegrationHTTPClient_nilUsesHTTPClientStandard(t *testing.T) {
	t.Parallel()
	got := IntegrationHTTPClient(nil)
	want := httpclient.Standard()
	if got.Timeout != want.Timeout {
		t.Fatalf("timeout: got %v want %v", got.Timeout, want.Timeout)
	}
	gotT, ok := got.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("Transport: got %T want *http.Transport", got.Transport)
	}
	wantT, ok := want.Transport.(*http.Transport)
	if !ok {
		t.Fatal("httpclient.Standard Transport is not *http.Transport")
	}
	if gotT.MaxIdleConnsPerHost != wantT.MaxIdleConnsPerHost {
		t.Fatalf("MaxIdleConnsPerHost: got %d want %d", gotT.MaxIdleConnsPerHost, wantT.MaxIdleConnsPerHost)
	}
}

func TestLocalTestServerHTTPClient_hasTimeout(t *testing.T) {
	t.Parallel()
	c := LocalTestServerHTTPClient()
	if c == nil {
		t.Fatal("nil client")
		return
	}
	if c.Timeout != localTestServerHTTPTimeout {
		t.Fatalf("timeout: got %v want %v", c.Timeout, localTestServerHTTPTimeout)
	}
}
