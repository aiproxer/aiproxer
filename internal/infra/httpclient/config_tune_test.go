package httpclient

import (
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/config"
)

func TestTransportTuneFromConfig_defaultsMatchStandard(t *testing.T) {
	t.Parallel()
	got := TransportTuneFromConfig(&config.Config{})
	want := DefaultTransportTune()
	if got != want {
		t.Fatalf("TransportTuneFromConfig(empty) = %+v want %+v", got, want)
	}
}

func TestTransportTuneFromConfig_overrides(t *testing.T) {
	t.Parallel()
	n := 200
	perHost := 88
	cfg := &config.Config{
		HTTPClient: config.HTTPClientConfig{
			MaxIdleConns:          &n,
			MaxIdleConnsPerHost:   &perHost,
			IdleConnTimeout:       "45s",
			ResponseHeaderTimeout: "55s",
			ClientTimeout:         "130s",
		},
	}
	got := TransportTuneFromConfig(cfg)
	if got.MaxIdleConns != 200 || got.MaxIdleConnsPerHost != 88 {
		t.Fatalf("pool fields: %+v", got)
	}
	if got.IdleConnTimeout.String() != "45s" || got.ResponseHeaderTimeout.String() != "55s" {
		t.Fatalf("timeouts: %+v", got)
	}
	if got.ClientTimeout != 130*time.Second {
		t.Fatalf("client timeout: %v", got.ClientTimeout)
	}
}
