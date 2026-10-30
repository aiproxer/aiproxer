package httpclient

import (
	"net/http"
	"testing"
	"time"
)

func TestDefaultTransport_perHostPoolingAndHeaderTimeout(t *testing.T) {
	t.Parallel()
	tr := DefaultTransport()
	if tr.MaxIdleConnsPerHost != defaultMaxIdleConnsPerHost {
		t.Fatalf("MaxIdleConnsPerHost: got %d want %d", tr.MaxIdleConnsPerHost, defaultMaxIdleConnsPerHost)
	}
	if tr.ResponseHeaderTimeout != defaultResponseHeaderTimeout {
		t.Fatalf("ResponseHeaderTimeout: got %v want %v", tr.ResponseHeaderTimeout, defaultResponseHeaderTimeout)
	}
}

func TestStandard_usesDefaultTransportPolicy(t *testing.T) {
	t.Parallel()
	c := Standard()
	if c.Timeout != 120*time.Second {
		t.Fatalf("Client.Timeout: got %v", c.Timeout)
	}
	tr, ok := c.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("Transport type: got %T", c.Transport)
	}
	if tr.MaxIdleConnsPerHost != defaultMaxIdleConnsPerHost {
		t.Fatalf("transport MaxIdleConnsPerHost: got %d", tr.MaxIdleConnsPerHost)
	}
	if tr.ResponseHeaderTimeout != defaultResponseHeaderTimeout {
		t.Fatalf("transport ResponseHeaderTimeout: got %v", tr.ResponseHeaderTimeout)
	}
}
