package metrics

import (
	"net/http"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	oteltrace "go.opentelemetry.io/otel/trace"
)

// UpstreamProm holds outbound HTTP round-trip latency (shared upstream client).
type UpstreamProm struct {
	requestDuration *prometheus.HistogramVec
	exemplars       bool
}

var upstreamBuckets = []float64{
	0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 25, 60, 120, 300,
}

// RegisterUpstreamProm registers aip_upstream_request_duration_seconds on reg.
func RegisterUpstreamProm(reg prometheus.Registerer, exemplars bool) *UpstreamProm {
	m := &UpstreamProm{
		requestDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: namespace,
				Name:      "upstream_request_duration_seconds",
				Help:      "Outbound upstream HTTP request duration in seconds (bounded host_bucket + method labels).",
				Buckets:   upstreamBuckets,
			},
			[]string{"host_bucket", "method"},
		),
		exemplars: exemplars,
	}
	reg.MustRegister(m.requestDuration)
	return m
}

// WrapUpstreamRoundTripper records latency for each RoundTrip on the inner transport.
func (u *UpstreamProm) WrapUpstreamRoundTripper(rt http.RoundTripper) http.RoundTripper {
	if u == nil || rt == nil {
		return rt
	}
	return &upstreamRT{inner: rt, m: u}
}

type upstreamRT struct {
	inner http.RoundTripper
	m     *UpstreamProm
}

func (t *upstreamRT) RoundTrip(req *http.Request) (*http.Response, error) {
	if req == nil {
		return t.inner.RoundTrip(req)
	}
	start := time.Now()
	resp, err := t.inner.RoundTrip(req)
	d := time.Since(start).Seconds()
	host := bucketHost(req.URL.Host)
	method := req.Method
	if method == "" {
		method = "UNKNOWN"
	}
	h := t.m.requestDuration.WithLabelValues(host, method)
	if t.m.exemplars {
		if obs, ok := h.(prometheus.ExemplarObserver); ok {
			if span := oteltrace.SpanFromContext(req.Context()); span.SpanContext().IsValid() {
				traceID := span.SpanContext().TraceID().String()
				obs.ObserveWithExemplar(d, prometheus.Labels{"trace_id": traceID})
				return resp, err
			}
		}
	}
	h.Observe(d)
	return resp, err
}

func bucketHost(host string) string {
	h := strings.ToLower(strings.TrimSpace(host))
	if h == "" {
		return "unknown"
	}
	// Strip default ports for stable buckets.
	h, _, _ = strings.Cut(h, ":")
	switch {
	case strings.Contains(h, "openai"):
		return "openai"
	case strings.Contains(h, "anthropic"):
		return "anthropic"
	case strings.Contains(h, "googleapis") || strings.Contains(h, "generativelanguage"):
		return "google"
	case strings.Contains(h, "amazonaws.com") || strings.Contains(h, "bedrock"):
		return "aws"
	default:
		return "other"
	}
}
