package identitywire_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/identity"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/identitywire"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestCaptureClientUserAgent_basic(t *testing.T) {
	t.Parallel()
	var inv aipapi.Invocation
	h := http.Header{}
	h.Set("User-Agent", "Cursor/1.2")
	identitywire.CaptureClientUserAgent(&inv, h)
	if inv.ClientUserAgent != "Cursor/1.2" {
		t.Fatalf("got %q", inv.ClientUserAgent)
	}
}

func TestCaptureClientUserAgent_caseInsensitive(t *testing.T) {
	t.Parallel()
	var inv aipapi.Invocation
	h := http.Header{}
	h.Set("user-agent", "Lower/1")
	identitywire.CaptureClientUserAgent(&inv, h)
	if inv.ClientUserAgent != "Lower/1" {
		t.Fatalf("got %q", inv.ClientUserAgent)
	}
}

func TestCaptureClientUserAgent_missingLeavesUnchanged(t *testing.T) {
	t.Parallel()
	var inv aipapi.Invocation
	inv.ClientUserAgent = "keep"
	identitywire.CaptureClientUserAgent(&inv, http.Header{})
	if inv.ClientUserAgent != "keep" {
		t.Fatalf("got %q", inv.ClientUserAgent)
	}
}

func TestCaptureClientUserAgent_blankLeavesUnchanged(t *testing.T) {
	t.Parallel()
	var inv aipapi.Invocation
	inv.ClientUserAgent = "keep"
	h := http.Header{}
	h.Set("User-Agent", "   ")
	identitywire.CaptureClientUserAgent(&inv, h)
	if inv.ClientUserAgent != "keep" {
		t.Fatalf("got %q", inv.ClientUserAgent)
	}
}

func TestCaptureClientUserAgent_nilSafe(t *testing.T) {
	t.Parallel()
	h := http.Header{}
	h.Set("User-Agent", "x")
	identitywire.CaptureClientUserAgent(nil, h)
	var inv aipapi.Invocation
	identitywire.CaptureClientUserAgent(&inv, nil)
	if inv.ClientUserAgent != "" {
		t.Fatalf("got %q", inv.ClientUserAgent)
	}
}

func TestCaptureClientUserAgent_noHeaderMutation(t *testing.T) {
	t.Parallel()
	var inv aipapi.Invocation
	h := http.Header{}
	h.Set("User-Agent", "Agent/1")
	h.Set("Authorization", "Bearer secret-token")
	beforeUA := h.Get("User-Agent")
	beforeAuth := h.Get("Authorization")
	identitywire.CaptureClientUserAgent(&inv, h)
	if h.Get("User-Agent") != beforeUA {
		t.Fatal("User-Agent mutated")
	}
	if h.Get("Authorization") != beforeAuth {
		t.Fatal("Authorization mutated")
	}
	if inv.ClientUserAgent != "Agent/1" {
		t.Fatalf("got %q", inv.ClientUserAgent)
	}
}

func TestCaptureClientUserAgent_doesNotCaptureCredentials(t *testing.T) {
	t.Parallel()
	var inv aipapi.Invocation
	h := http.Header{}
	h.Set("Authorization", "Bearer secret-token")
	h.Set("X-Api-Key", "sk-secret")
	h.Set("Cookie", "session=abc")
	h.Set("HTTP-Referer", "https://evil.example/")
	h.Set("X-Title", "Evil Title")
	identitywire.CaptureClientUserAgent(&inv, h)
	if inv.ClientUserAgent != "" {
		t.Fatalf("captured non-UA material: %q", inv.ClientUserAgent)
	}
}

func TestCaptureClientUserAgent_rejectsControlCharacters(t *testing.T) {
	t.Parallel()
	bad := []string{
		"agent\rname",
		"agent\nname",
		"agent\x00name",
		"agent\x01name",
	}
	for _, v := range bad {
		t.Run(strings.ReplaceAll(v, "\x00", "NUL"), func(t *testing.T) {
			t.Parallel()
			var inv aipapi.Invocation
			inv.ClientUserAgent = "keep"
			h := http.Header{}
			h.Set("User-Agent", v)
			identitywire.CaptureClientUserAgent(&inv, h)
			if inv.ClientUserAgent != "keep" {
				t.Fatalf("invalid UA should be dropped, got %q", inv.ClientUserAgent)
			}
		})
	}
}

func TestCaptureClientUserAgent_rejectsOverMaxBytes(t *testing.T) {
	t.Parallel()
	var inv aipapi.Invocation
	inv.ClientUserAgent = "keep"
	h := http.Header{}
	h.Set("User-Agent", strings.Repeat("a", identity.MaxUserAgentBytes+1))
	identitywire.CaptureClientUserAgent(&inv, h)
	if inv.ClientUserAgent != "keep" {
		t.Fatalf("overlong UA should be dropped, got %q", inv.ClientUserAgent)
	}
}

func TestCaptureClientUserAgent_acceptsMaxBytes(t *testing.T) {
	t.Parallel()
	var inv aipapi.Invocation
	ua := strings.Repeat("b", identity.MaxUserAgentBytes)
	h := http.Header{}
	h.Set("User-Agent", ua)
	identitywire.CaptureClientUserAgent(&inv, h)
	if inv.ClientUserAgent != ua {
		t.Fatalf("got len=%d want %d", len(inv.ClientUserAgent), len(ua))
	}
}
