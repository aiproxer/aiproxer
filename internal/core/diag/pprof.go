package diag

import (
	"net/http"
	"net/http/pprof"
	"strings"
)

// PprofHandler serves standard library pprof endpoints under prefix (e.g. "/debug/pprof").
// prefix must be non-empty after trimming; trailing slashes are stripped. Returns nil if prefix is empty.
//
// Mount with mux.Handle(prefix+"/", h) so the index page resolves at prefix+"/".
func PprofHandler(prefix string) http.Handler {
	prefix = strings.TrimSpace(prefix)
	prefix = strings.TrimSuffix(prefix, "/")
	if prefix == "" {
		return nil
	}
	m := http.NewServeMux()
	m.HandleFunc("/", pprof.Index)
	m.HandleFunc("/cmdline", pprof.Cmdline)
	m.HandleFunc("/profile", pprof.Profile)
	m.HandleFunc("/symbol", pprof.Symbol)
	m.HandleFunc("/trace", pprof.Trace)
	m.Handle("/allocs", pprof.Handler("allocs"))
	m.Handle("/block", pprof.Handler("block"))
	m.Handle("/goroutine", pprof.Handler("goroutine"))
	m.Handle("/heap", pprof.Handler("heap"))
	m.Handle("/mutex", pprof.Handler("mutex"))
	m.Handle("/threadcreate", pprof.Handler("threadcreate"))
	return http.StripPrefix(prefix, m)
}
