package metrics

import (
	"net/http"
	"time"

	corehttp "github.com/aiproxer/aiproxer/internal/core/http"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	oteltrace "go.opentelemetry.io/otel/trace"
)

const namespace = "aip"

// httpInboundDurationBuckets are tuned for LLM-proxy tail latency (often >10s); default
// Prometheus buckets top out ~10s and collapse most LLM requests into +Inf.
var httpInboundDurationBuckets = []float64{
	0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5,
	1, 2.5, 5, 10, 25, 60, 120,
}

// NewRegistry builds a dedicated Prometheus registry with Go/process collectors and HTTP RPC metrics.
func NewRegistry() *prometheus.Registry {
	r := prometheus.NewRegistry()
	r.MustRegister(collectors.NewGoCollector())
	r.MustRegister(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	return r
}

// RegisterHTTPMetrics registers histogram and counter for inbound HTTP observations on reg.
// When exemplars is true, histogram observations attach trace_id when the request context carries a valid span.
func RegisterHTTPMetrics(reg prometheus.Registerer, exemplars bool) *HTTPMetrics {
	m := &HTTPMetrics{
		requestDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: namespace,
				Name:      "http_request_duration_seconds",
				Help:      "Inbound HTTP request duration in seconds (labels: method + status_class + route_group).",
				Buckets:   httpInboundDurationBuckets,
			},
			[]string{"method", "status_class", "route_group"},
		),
		requestsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Name:      "http_requests_total",
				Help:      "Inbound HTTP requests (labels: method + status_class + route_group).",
			},
			[]string{"method", "status_class", "route_group"},
		),
		exemplars: exemplars,
	}
	reg.MustRegister(m.requestDuration, m.requestsTotal)
	return m
}

// HTTPMetrics holds references to HTTP RPC metric vectors.
type HTTPMetrics struct {
	requestDuration *prometheus.HistogramVec
	requestsTotal   *prometheus.CounterVec
	exemplars       bool
}

// MetricsHandler exposes the registry for Prometheus scraping.
func MetricsHandler(reg *prometheus.Registry, enableOpenMetrics bool) http.Handler {
	return promhttp.HandlerFor(reg, promhttp.HandlerOpts{
		Registry:          reg,
		EnableOpenMetrics: enableOpenMetrics,
	})
}

// StatusClass maps an HTTP status to a bounded label bucket.
func StatusClass(code int) string {
	switch {
	case code >= 200 && code < 300:
		return "2xx"
	case code >= 300 && code < 400:
		return "3xx"
	case code >= 400 && code < 500:
		return "4xx"
	case code >= 500:
		return "5xx"
	default:
		return "other"
	}
}

// Middleware observes duration and status per request with bounded labels.
func (m *HTTPMetrics) Middleware(next http.Handler) http.Handler {
	if m == nil || next == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rr := &corehttp.ResponseStatusRecorder{ResponseWriter: w}
		next.ServeHTTP(rr, r)
		code := rr.Status
		if code == 0 {
			code = http.StatusOK
		}
		sc := StatusClass(code)
		method := r.Method
		if method == "" {
			method = "UNKNOWN"
		}
		d := time.Since(start).Seconds()
		rg := corehttp.CoarsePathGroup(r.URL.Path)
		if m.exemplars {
			h := m.requestDuration.WithLabelValues(method, sc, rg)
			if obs, ok := h.(prometheus.ExemplarObserver); ok {
				if span := oteltrace.SpanFromContext(r.Context()); span.SpanContext().IsValid() {
					traceID := span.SpanContext().TraceID().String()
					obs.ObserveWithExemplar(d, prometheus.Labels{"trace_id": traceID})
					m.requestsTotal.WithLabelValues(method, sc, rg).Inc()
					return
				}
			}
		}
		m.requestDuration.WithLabelValues(method, sc, rg).Observe(d)
		m.requestsTotal.WithLabelValues(method, sc, rg).Inc()
	})
}
