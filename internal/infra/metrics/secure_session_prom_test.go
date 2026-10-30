package metrics

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestSecureSessionMetricsSink_denialAndStorage(t *testing.T) {
	t.Parallel()
	r := NewRegistry()
	p := RegisterSecureSessionProm(r)
	sink := SecureSessionMetricsSink(p)
	if sink == nil {
		t.Fatal("sink")
	}
	sink.ObserveBeginTurnDenied(string(aipapi.SessionDeniedStorageUnavailable))
	sink.ObserveStorageUnavailable()
	sink.ObserveBeginTurnNew()
	sink.ObserveBeginTurnResume()
	sink.ObserveActivityTouch(0.001)
	sink.ObserveRecorderClientTurnFailed(true)
	sink.ObserveRecorderStreamEventFailed(true, true)

	if got := testutil.ToFloat64(p.denied.WithLabelValues(string(aipapi.SessionDeniedStorageUnavailable))); got != 1 {
		t.Fatalf("denied: %v", got)
	}
	if got := testutil.ToFloat64(p.storageFail); got != 1 {
		t.Fatalf("storage: %v", got)
	}
}

func TestNewBundle_includesSecureSession(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{Observability: config.ObservabilityConfig{Metrics: config.MetricsConfig{Enabled: true}}}
	b := NewBundle(cfg, nil, nil)
	if b.SecureSession == nil {
		t.Fatal("expected SecureSession prom")
	}
	if b.SecureSessionMetricsSink() == nil {
		t.Fatal("expected sink")
	}
}
