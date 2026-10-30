package identitywire_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/identity"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/identitywire"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func FuzzCaptureClientUserAgent(f *testing.F) {
	f.Add("Cursor/1.2")
	f.Add("")
	f.Add("bad\r\nagent")
	f.Add(strings.Repeat("a", identity.MaxUserAgentBytes+1))
	f.Add("  spaced  ")
	f.Fuzz(func(t *testing.T, ua string) {
		if len(ua) > identity.MaxUserAgentBytes+128 {
			return
		}
		h := make(http.Header)
		if ua != "" {
			h.Set("User-Agent", ua)
		}
		h.Set("Authorization", "Bearer secret")
		h.Set("Cookie", "session=abc")
		var inv aipapi.Invocation
		identitywire.CaptureClientUserAgent(&inv, h)
		// Must never copy credential material into ClientUserAgent.
		if strings.Contains(inv.ClientUserAgent, "Bearer") || strings.Contains(inv.ClientUserAgent, "session=") {
			t.Fatalf("credential leak into ClientUserAgent: %q", inv.ClientUserAgent)
		}
		if inv.ClientUserAgent != "" {
			if len(inv.ClientUserAgent) > identity.MaxUserAgentBytes {
				t.Fatalf("captured overlong UA len=%d", len(inv.ClientUserAgent))
			}
			if strings.ContainsAny(inv.ClientUserAgent, "\r\n\x00") {
				t.Fatalf("captured control chars: %q", inv.ClientUserAgent)
			}
		}
		identitywire.CaptureClientUserAgent(nil, h)
	})
}
