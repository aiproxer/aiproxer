package httpclient

import (
	"net"
	"net/http"
	"time"
)

// Outbound tuning for shared upstream calls (LLM APIs). Go's default MaxIdleConnsPerHost is 2,
// which causes connection churn under concurrency to a single host; we raise it explicitly.
const (
	defaultMaxIdleConnsPerHost = 64
	defaultMaxIdleConns        = 100
	// ResponseHeaderTimeout bounds time waiting for response headers (distinct from Client.Timeout,
	// which covers the full round trip including body). Slightly below Standard's client timeout.
	defaultResponseHeaderTimeout = 60 * time.Second
	defaultDialTimeout           = 30 * time.Second
	defaultKeepAlive             = 30 * time.Second
	defaultIdleConnTimeout       = 90 * time.Second
	defaultTLSHandshakeTimeout   = 10 * time.Second
	defaultExpectContinueTimeout = 1 * time.Second
	defaultClientTimeout         = 120 * time.Second
)

// TransportTune holds outbound [http.Transport] and [http.Client] timeouts and pool sizes.
// Use [DefaultTransportTune] for the same defaults as historical Standard outbound behavior.
type TransportTune struct {
	MaxIdleConns          int
	MaxIdleConnsPerHost   int
	IdleConnTimeout       time.Duration
	ResponseHeaderTimeout time.Duration
	DialTimeout           time.Duration
	KeepAlive             time.Duration
	TLSHandshakeTimeout   time.Duration
	ExpectContinueTimeout time.Duration
	ClientTimeout         time.Duration
}

// DefaultTransportTune matches the defaults used by [Standard] before optional YAML overrides.
func DefaultTransportTune() TransportTune {
	return TransportTune{
		MaxIdleConns:          defaultMaxIdleConns,
		MaxIdleConnsPerHost:   defaultMaxIdleConnsPerHost,
		IdleConnTimeout:       defaultIdleConnTimeout,
		ResponseHeaderTimeout: defaultResponseHeaderTimeout,
		DialTimeout:           defaultDialTimeout,
		KeepAlive:             defaultKeepAlive,
		TLSHandshakeTimeout:   defaultTLSHandshakeTimeout,
		ExpectContinueTimeout: defaultExpectContinueTimeout,
		ClientTimeout:         defaultClientTimeout,
	}
}

// Standard returns an HTTP client suitable for upstream provider calls from the standard bundle:
// explicit transport (timeouts, pooling), not package-global DefaultClient.
// It honors HTTP_PROXY / HTTPS_PROXY from the process environment.
func Standard() *http.Client {
	return StandardWithTune(true, DefaultTransportTune())
}

// StandardWithTrustEnvironment is like [Standard] but allows disabling use of proxy-related
// environment variables (sets Transport.Proxy to nil when trustEnv is false).
func StandardWithTrustEnvironment(trustEnv bool) *http.Client {
	return StandardWithTune(trustEnv, DefaultTransportTune())
}

// StandardWithTune builds a client using the given pooling and timeout tuning.
func StandardWithTune(trustEnv bool, tune TransportTune) *http.Client {
	return &http.Client{
		Transport: NewTransportTune(trustEnv, tune),
		Timeout:   tune.ClientTimeout,
	}
}

// DefaultTransport builds a shared transport policy for outbound HTTP from the composition root.
// It honors HTTP_PROXY / HTTPS_PROXY from the environment.
func DefaultTransport() *http.Transport {
	return NewTransportTune(true, DefaultTransportTune())
}

// NewTransport returns a new transport with the same tuning as [DefaultTransport].
// When trustEnvironmentProxy is false, Proxy is nil so HTTP_PROXY and related env vars are ignored.
func NewTransport(trustEnvironmentProxy bool) *http.Transport {
	return NewTransportTune(trustEnvironmentProxy, DefaultTransportTune())
}

// NewTransportTune builds a transport from explicit tuning (see [TransportTuneFromConfig] for YAML-backed values).
func NewTransportTune(trustEnvironmentProxy bool, tune TransportTune) *http.Transport {
	t := &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   tune.DialTimeout,
			KeepAlive: tune.KeepAlive,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          tune.MaxIdleConns,
		MaxIdleConnsPerHost:   tune.MaxIdleConnsPerHost,
		IdleConnTimeout:       tune.IdleConnTimeout,
		TLSHandshakeTimeout:   tune.TLSHandshakeTimeout,
		ExpectContinueTimeout: tune.ExpectContinueTimeout,
		ResponseHeaderTimeout: tune.ResponseHeaderTimeout,
	}
	if trustEnvironmentProxy {
		t.Proxy = http.ProxyFromEnvironment
	} else {
		t.Proxy = nil
	}
	return t
}
