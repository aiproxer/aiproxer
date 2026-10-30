package stdhttp

import (
	"net/http"
)

// listenAndServe is the [http.Server.ListenAndServe] implementation (overridable in tests).
var listenAndServe = func(srv *http.Server) error { return srv.ListenAndServe() }
