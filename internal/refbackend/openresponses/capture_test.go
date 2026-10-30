package openresponses

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCapture_Counters(t *testing.T) {
	t.Parallel()
	c := NewCapture(0, nil, false)
	req := httptest.NewRequest(http.MethodPost, "http://x/responses", strings.NewReader(`{"a":1}`))
	c.Record(req, []byte(`{"a":1}`))
	req2 := httptest.NewRequest(http.MethodPost, "http://x/responses", strings.NewReader(`{"a":2}`))
	c.Record(req2, []byte(`{"a":2}`))
	req3 := httptest.NewRequest(http.MethodPost, "http://x/responses/compact", strings.NewReader(`{}`))
	c.Record(req3, []byte(`{}`))

	if c.Total() != 3 {
		t.Fatalf("total: %d", c.Total())
	}
	if c.Count("/responses") != 2 || c.Count("/responses/compact") != 1 {
		t.Fatalf("counts: %d/%d", c.Count("/responses"), c.Count("/responses/compact"))
	}
	if c.Len() != 3 {
		t.Fatalf("len: %d", c.Len())
	}
	last, ok := c.Last()
	if !ok || last.Path != "/responses/compact" || string(last.Body) != `{}` {
		t.Fatalf("last: %+v", last)
	}
}

func TestCapture_Redaction(t *testing.T) {
	t.Parallel()
	c := NewCapture(0, []string{"authorization", "x-api-key"}, false)
	req := httptest.NewRequest(http.MethodPost, "http://x/responses", nil)
	req.Header.Set("Authorization", "Bearer sk-secret")
	req.Header.Set("X-Api-Key", "k-secret")
	req.Header.Set("X-Custom", "visible")
	c.Record(req, []byte(`{"model":"m"}`))

	obs, ok := c.Last()
	if !ok {
		t.Fatal("no observation")
	}
	if !obs.Redacted {
		t.Fatal("must be flagged redacted")
	}
	if obs.Headers.Get("Authorization") != RedactedAuthorization {
		t.Fatalf("authorization: %q", obs.Headers.Get("Authorization"))
	}
	if obs.Headers.Get("X-Api-Key") != RedactedAuthorization {
		t.Fatalf("api key: %q", obs.Headers.Get("X-Api-Key"))
	}
	if obs.Headers.Get("X-Custom") != "visible" {
		t.Fatalf("custom header must stay visible: %q", obs.Headers.Get("X-Custom"))
	}
	if string(obs.Body) != `{"model":"m"}` {
		t.Fatalf("body must be preserved: %q", obs.Body)
	}
}

func TestCapture_BoundedOverflow(t *testing.T) {
	t.Parallel()
	c := NewCapture(2, nil, false)
	for range 5 {
		req := httptest.NewRequest(http.MethodPost, "http://x/responses", nil)
		c.Record(req, []byte(`{}`))
	}
	if c.Total() != 5 {
		t.Fatalf("total must count all: %d", c.Total())
	}
	if c.Len() != 2 {
		t.Fatalf("bounded len: %d", c.Len())
	}
	if c.Overflow() != 3 {
		t.Fatalf("overflow: %d", c.Overflow())
	}
	if len(c.Observations()) != 2 {
		t.Fatalf("observations: %d", len(c.Observations()))
	}
}

func TestCapture_Reset(t *testing.T) {
	t.Parallel()
	c := NewCapture(0, nil, false)
	req := httptest.NewRequest(http.MethodPost, "http://x/responses", nil)
	c.Record(req, []byte(`{}`))
	c.RecordFrame("/responses", nil, []byte(`{"type":"response.create"}`))
	if c.Total() != 2 {
		t.Fatalf("total: %d", c.Total())
	}
	c.Reset()
	if c.Total() != 0 || c.Len() != 0 || c.Overflow() != 0 || c.Count("/responses") != 0 {
		t.Fatalf("reset failed: total=%d len=%d overflow=%d", c.Total(), c.Len(), c.Overflow())
	}
	if _, ok := c.Last(); ok {
		t.Fatal("reset must clear last")
	}
}

func TestCapture_ZeroUpstream(t *testing.T) {
	t.Parallel()
	// No requests issued: the counter must stay at zero so pre-network
	// rejection can be proven with Total() == 0.
	c := NewCapture(0, nil, false)
	if c.Total() != 0 || c.Count("/responses") != 0 {
		t.Fatalf("zero-upstream counters must be 0")
	}
	if c.Len() != 0 {
		t.Fatalf("no observations expected")
	}
}

func TestCapture_ObservationsDefensiveCopy(t *testing.T) {
	t.Parallel()
	c := NewCapture(0, nil, false)
	req := httptest.NewRequest(http.MethodPost, "http://x/responses", strings.NewReader(`{"model":"m"}`))
	c.Record(req, []byte(`{"model":"m"}`))
	obs := c.Observations()
	obs[0].Body[0] = 'X'
	obs[0].Headers.Set("H", "mutated")
	again := c.Observations()
	if again[0].Body[0] == 'X' {
		t.Fatal("observations must be defensive copies")
	}
	if again[0].Headers.Get("H") == "mutated" {
		t.Fatal("header copy must be defensive")
	}
}
