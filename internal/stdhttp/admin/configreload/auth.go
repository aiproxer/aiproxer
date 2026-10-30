package configreload

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"strings"
)

func (h *Handler) authorize(w http.ResponseWriter, r *http.Request) bool {
	switch h.opts.AuthMode {
	case AuthModeLocalTrust:
		// Documented local single-user loopback trust (req 12.5).
		// Cookies never authorize; no credential check.
		return true
	case AuthModeBearer:
		want := strings.TrimSpace(h.opts.BearerToken)
		got := bearerFromAuthorization(r.Header.Get("Authorization"))
		// subtle.ConstantTimeCompare short-circuits on length mismatch, leaking
		// the expected token length via timing. Hash both sides first so the
		// compared digests are always fixed-length (cf. internal/core/diag).
		wantHash := sha256.Sum256([]byte(want))
		gotHash := sha256.Sum256([]byte(got))
		if want == "" || subtle.ConstantTimeCompare(gotHash[:], wantHash[:]) != 1 {
			writeCategory(w, http.StatusUnauthorized, "unauthorized")
			return false
		}
		return true
	case AuthModeInjected:
		if h.opts.Authenticator == nil {
			writeCategory(w, http.StatusUnauthorized, "unauthorized")
			return false
		}
		if err := h.opts.Authenticator.Authorize(r); err != nil {
			writeCategory(w, http.StatusUnauthorized, "unauthorized")
			return false
		}
		return true
	default:
		writeCategory(w, http.StatusUnauthorized, "unauthorized")
		return false
	}
}

func bearerFromAuthorization(raw string) string {
	const prefix = "Bearer "
	if len(raw) < len(prefix) || !strings.EqualFold(raw[:len(prefix)], prefix) {
		return ""
	}
	return strings.TrimSpace(raw[len(prefix):])
}
