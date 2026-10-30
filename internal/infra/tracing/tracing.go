// Package tracing wires OpenTelemetry tracers, propagators, and optional OTLP export.
package tracing

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/config"
	corehttp "github.com/aiproxer/aiproxer/internal/core/http"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

const defaultServiceName = "aipstd"

// Result carries shutdown and whether tracing is active (SDK registered).
type Result struct {
	Shutdown func(context.Context) error
	Active   bool
}

// Init configures global propagators and, when cfg.Tracing.Enabled, an OTLP HTTP exporter and tracer provider.
// Export uses standard OTEL environment variables (OTEL_EXPORTER_OTLP_ENDPOINT, OTEL_TRACES_EXPORTER, etc.).
func Init(ctx context.Context, cfg *config.Config) (Result, error) {
	if cfg == nil || !cfg.Observability.Tracing.Enabled {
		return Result{Shutdown: func(context.Context) error { return nil }, Active: false}, nil
	}

	serviceName := strings.TrimSpace(cfg.Observability.Tracing.ServiceName)
	if serviceName == "" {
		serviceName = strings.TrimSpace(os.Getenv("OTEL_SERVICE_NAME"))
	}
	if serviceName == "" {
		serviceName = defaultServiceName
	}

	res, err := resource.New(
		ctx,
		resource.WithFromEnv(),
		resource.WithTelemetrySDK(),
		resource.WithHost(),
		resource.WithAttributes(semconv.ServiceName(serviceName)),
	)
	if err != nil {
		return Result{}, fmt.Errorf("tracing: resource: %w", err)
	}

	exp, err := otlptracehttp.New(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("tracing: otlp http exporter: %w", err)
	}

	tpOpts := []sdktrace.TracerProviderOption{
		sdktrace.WithBatcher(exp),
		sdktrace.WithResource(res),
	}
	if sr := cfg.Observability.Tracing.SampleRatio; sr != nil {
		r := *sr
		if r > 0 && r < 1 {
			tpOpts = append(tpOpts, sdktrace.WithSampler(
				sdktrace.ParentBased(sdktrace.TraceIDRatioBased(r)),
			))
		}
	}
	tp := sdktrace.NewTracerProvider(tpOpts...)

	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	shutdown := func(shutdownCtx context.Context) error {
		ctxTO, cancel := context.WithTimeout(shutdownCtx, 10*time.Second)
		defer cancel()
		if err := tp.Shutdown(ctxTO); err != nil {
			return fmt.Errorf("tracing: shutdown tracer provider: %w", err)
		}
		return nil
	}

	return Result{Shutdown: shutdown, Active: true}, nil
}

// HTTPMiddleware wraps the handler with otelhttp when tracing is active; otherwise returns next unchanged.
func HTTPMiddleware(active bool, next http.Handler) http.Handler {
	if !active || next == nil {
		return next
	}
	return otelhttp.NewHandler(
		next, "",
		otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string {
			return spanName(r)
		}),
	)
}

func spanName(r *http.Request) string {
	if r == nil {
		return "HTTP"
	}
	p := corehttp.CoarsePathGroup(r.URL.Path)
	if len(p) > 64 {
		p = p[:64]
	}
	return r.Method + " " + p
}

// WrapTransport wraps the round tripper with otelhttp.NewTransport when active.
func WrapTransport(active bool, rt http.RoundTripper) http.RoundTripper {
	if !active || rt == nil {
		return rt
	}
	return otelhttp.NewTransport(rt)
}
