package httpclient

import (
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/config"
)

// TransportTuneFromConfig overlays optional [config.Config] http_client fields on [DefaultTransportTune].
// Invalid duration strings are ignored (callers should reject them in [config.Validate] first).
func TransportTuneFromConfig(cfg *config.Config) TransportTune {
	t := DefaultTransportTune()
	if cfg == nil {
		return t
	}
	hc := cfg.HTTPClient
	if hc.MaxIdleConns != nil && *hc.MaxIdleConns > 0 {
		t.MaxIdleConns = *hc.MaxIdleConns
	}
	if hc.MaxIdleConnsPerHost != nil && *hc.MaxIdleConnsPerHost > 0 {
		t.MaxIdleConnsPerHost = *hc.MaxIdleConnsPerHost
	}
	parse := func(s string, dest *time.Duration) {
		s = strings.TrimSpace(s)
		if s == "" {
			return
		}
		d, err := time.ParseDuration(s)
		if err != nil || d <= 0 {
			return
		}
		*dest = d
	}
	parse(hc.IdleConnTimeout, &t.IdleConnTimeout)
	parse(hc.ResponseHeaderTimeout, &t.ResponseHeaderTimeout)
	parse(hc.DialTimeout, &t.DialTimeout)
	parse(hc.KeepAlive, &t.KeepAlive)
	parse(hc.TLSHandshakeTimeout, &t.TLSHandshakeTimeout)
	parse(hc.ExpectContinueTimeout, &t.ExpectContinueTimeout)
	parse(hc.ClientTimeout, &t.ClientTimeout)
	return t
}
