package configreload

import (
	"net/http"
)

// browserGuard enforces the non-browser administrative Origin/Fetch Metadata
// posture before coordinator invocation (req 12.7). statusRead permits
// Sec-Fetch-Site: none for direct user-agent navigation on read-only status.
func (h *Handler) browserGuard(w http.ResponseWriter, r *http.Request, statusRead bool) bool {
	if origin := r.Header.Get("Origin"); origin != "" {
		if _, ok := h.opts.AllowOrigins[origin]; !ok {
			writeCategory(w, http.StatusForbidden, "browser-origin-rejected")
			return false
		}
	}
	switch site := r.Header.Get("Sec-Fetch-Site"); site {
	case "cross-site", "same-site":
		writeCategory(w, http.StatusForbidden, "fetch-metadata-rejected")
		return false
	case "same-origin":
		if r.Header.Get("Origin") == "" {
			writeCategory(w, http.StatusForbidden, "fetch-metadata-rejected")
			return false
		}
		if _, ok := h.opts.AllowOrigins[r.Header.Get("Origin")]; !ok {
			writeCategory(w, http.StatusForbidden, "browser-origin-rejected")
			return false
		}
	case "none":
		if !statusRead {
			writeCategory(w, http.StatusForbidden, "fetch-metadata-rejected")
			return false
		}
	}
	// Explicitly strip any CORS headers; never reflect Origin (req 12.7).
	w.Header().Del("Access-Control-Allow-Origin")
	w.Header().Del("Access-Control-Allow-Credentials")
	w.Header().Del("Access-Control-Allow-Headers")
	w.Header().Del("Access-Control-Allow-Methods")
	return true
}
