package metrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStatusClass_buckets(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		code int
		want string
	}{
		{"2xx", 200, "2xx"},
		{"3xx", 301, "3xx"},
		{"4xx", 404, "4xx"},
		{"5xx", 500, "5xx"},
		{"other", 99, "other"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := StatusClass(tc.code); got != tc.want {
				t.Fatalf("StatusClass(%d)=%q want %q", tc.code, got, tc.want)
			}
		})
	}
}

func TestHTTPDurationBuckets_coverLLMTail(t *testing.T) {
	t.Parallel()
	var maxBucket float64
	for _, b := range httpInboundDurationBuckets {
		if b > maxBucket {
			maxBucket = b
		}
	}
	if maxBucket < 60 {
		t.Fatalf("largest histogram bucket %g must be >= 60s for LLM tail latency observability", maxBucket)
	}
}

func TestHTTPMetricsMiddleware_boundedLabels(t *testing.T) {
	t.Parallel()
	reg := NewRegistry()
	m := RegisterHTTPMetrics(reg, false)
	h := m.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))

	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/any/high-cardinality/path")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := resp.Body.Close(); err != nil {
			t.Errorf("response body close: %v", err)
		}
	})

	mfs, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	var dump strings.Builder
	for _, mf := range mfs {
		dump.WriteString(mf.String())
	}
	s := dump.String()
	if strings.Contains(s, "/any/high-cardinality") || strings.Contains(s, "high-cardinality") {
		t.Fatalf("metrics exposition must not echo URL path:\n%s", s)
	}
	if !strings.Contains(s, `method`) || !strings.Contains(s, `status_class`) || !strings.Contains(s, `route_group`) {
		t.Fatalf("expected bounded labels in exposition:\n%s", s)
	}
	if resp.StatusCode != http.StatusTeapot {
		t.Fatalf("status=%d", resp.StatusCode)
	}
}
