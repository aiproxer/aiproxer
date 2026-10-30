package stdhttp_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/frontends/gemini"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/sessionwire"
	"github.com/aiproxer/aiproxer/internal/stdhttp"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

// These tests guard against drift between the local stdhttp constants and
// the canonical values defined inside the concrete frontend plugin packages.
// Test-only imports of plugin packages are acceptable for boundary alignment.

func TestGeminiFrontendID_matchesCanonical(t *testing.T) {
	t.Parallel()
	if stdhttp.ExportGeminiFrontendID() != gemini.ID {
		t.Fatalf("stdhttp Gemini frontend ID = %q, want gemini.ID = %q", stdhttp.ExportGeminiFrontendID(), gemini.ID)
	}
}

func TestSessionHeaders_matchCanonical(t *testing.T) {
	t.Parallel()
	if stdhttp.ExportHeaderAuthoritativeSessionID() != sessionwire.HeaderAuthoritativeSessionID {
		t.Fatalf("stdhttp session ID header = %q, want sessionwire.HeaderAuthoritativeSessionID = %q",
			stdhttp.ExportHeaderAuthoritativeSessionID(), sessionwire.HeaderAuthoritativeSessionID)
	}
	if stdhttp.ExportHeaderResumeToken() != sessionwire.HeaderResumeToken {
		t.Fatalf("stdhttp resume token header = %q, want sessionwire.HeaderResumeToken = %q",
			stdhttp.ExportHeaderResumeToken(), sessionwire.HeaderResumeToken)
	}
}

func TestDefaultHTTPHeaders_matchSessionCarriers(t *testing.T) {
	t.Parallel()
	h := aipsdk.DefaultHTTPHeaders()
	if h.SessionID[0] != sessionwire.HeaderAuthoritativeSessionID {
		t.Fatalf("session id default %q", h.SessionID[0])
	}
	if h.ResumeToken[0] != sessionwire.HeaderResumeToken {
		t.Fatalf("resume token default %q", h.ResumeToken[0])
	}
	if h.ALegID[0] != sessionwire.HeaderALegID {
		t.Fatalf("a-leg id default %q", h.ALegID[0])
	}
	if h.SessionHint[0] != sessionwire.HeaderSessionHint {
		t.Fatalf("session hint default %q", h.SessionHint[0])
	}
	if h.Route[0] != gemini.HeaderRouteSelector {
		t.Fatalf("route default %q, want gemini/routeselect %q", h.Route[0], gemini.HeaderRouteSelector)
	}
}
