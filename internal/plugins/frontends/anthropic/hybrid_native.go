package anthropic

import (
	"bytes"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

const hybridExchangeTimeout = 5 * time.Minute

// ClaudeCodeHybridMinimumWriteTimeout leaves downstream ingress/drain headroom
// around the five-minute hybrid exchange; standard composition enforces it.
const ClaudeCodeHybridMinimumWriteTimeout = hybridExchangeTimeout + time.Minute

func newHybridNativeClient() *http.Client {
	// No environment proxy, caller-supplied origin or redirects. Disable
	// keepalive so no idle connection needs a second generation cleanup owner.
	return &http.Client{
		Transport:     &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 15 * time.Second}).DialContext, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, TLSHandshakeTimeout: 15 * time.Second, ResponseHeaderTimeout: 60 * time.Second, DisableKeepAlives: true, MaxResponseHeaderBytes: 64 << 10},
		Timeout:       hybridExchangeTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func (h *hybridHandler) serveNative(w http.ResponseWriter, r *http.Request, body []byte) {
	endpoint := "https://api.anthropic.com/v1/messages"
	if r.URL.RawQuery == "beta=true" {
		endpoint += "?beta=true"
	}
	up, _ := http.NewRequestWithContext(r.Context(), http.MethodPost, endpoint, bytes.NewReader(body))
	up.Header.Set("Content-Type", "application/json")
	// Copy only protocol headers known to be needed by the pinned CLI, plus
	// exactly its original first-party credential. Connection-nominated headers
	// are rejected rather than allowing hop-by-hop stripping to remove auth.
	for _, name := range []string{"Authorization", "x-api-key", "anthropic-version", "anthropic-beta", "anthropic-dangerous-direct-browser-access", "User-Agent", "x-app"} {
		for _, v := range r.Header.Values(name) {
			up.Header.Add(name, v)
		}
	}
	w.Header().Set("X-AIP-Canonical-Processing", "false")
	h.log.InfoContext(r.Context(), "claude_code_hybrid_native", "canonical_processing", false)
	resp, err := h.native.Do(up)
	if err != nil {
		hybridError(w, 502, "native Anthropic transport failed")
		return
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		hybridError(w, 502, "native Anthropic redirect refused")
		return
	}
	// No arbitrary upstream headers (Set-Cookie, Location, proxy/control or
	// connection-nominated fields) cross back to the CLI.
	blocked := map[string]bool{}
	for _, v := range resp.Header.Values("Connection") {
		for name := range strings.SplitSeq(v, ",") {
			blocked[strings.ToLower(strings.TrimSpace(name))] = true
		}
	}
	for _, name := range []string{"Content-Type", "Request-Id", "Retry-After", "anthropic-ratelimit-requests-limit", "anthropic-ratelimit-requests-remaining", "anthropic-ratelimit-requests-reset", "anthropic-ratelimit-tokens-limit", "anthropic-ratelimit-tokens-remaining", "anthropic-ratelimit-tokens-reset"} {
		if !blocked[strings.ToLower(name)] {
			for _, v := range resp.Header.Values(name) {
				w.Header().Add(name, v)
			}
		}
	}
	w.WriteHeader(resp.StatusCode)
	buf := make([]byte, 32<<10)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, err := w.Write(buf[:n]); err != nil {
				return
			}
			if err := http.NewResponseController(w).Flush(); err != nil {
				return
			}
		}
		if readErr != nil {
			if readErr != io.EOF {
				h.log.WarnContext(r.Context(), "claude_code_hybrid_native_stream_failed", "canonical_processing", false)
			}
			return
		}
	}
}
