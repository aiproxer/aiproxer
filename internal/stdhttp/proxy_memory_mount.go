package stdhttp

import "net/http"

// mountProxyMemory mounts the projected proxy-memory REST and MCP handler at
// /v1/aip/memory and its subtree. A generation that does not project a handler
// registers nothing, so the mux owns the not-found response for the routes.
func mountProxyMemory(mux *http.ServeMux, h http.Handler) {
	if h == nil {
		return
	}
	mux.Handle("/v1/aip/memory", h)
	mux.Handle("/v1/aip/memory/", h)
}
