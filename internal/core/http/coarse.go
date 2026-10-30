package http

import "strings"

// CoarsePathGroup returns the first URL path segment as a bounded route family
// (e.g. "/v1/foo" -> "/v1", "/" -> "/"). Used for metrics, tracing span names, and access logs.
func CoarsePathGroup(path string) string {
	path = strings.TrimSpace(path)
	if path == "" || path == "/" {
		return "/"
	}
	path = strings.TrimSuffix(path, "/")
	segs := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if len(segs) == 0 || segs[0] == "" {
		return "/"
	}
	return "/" + segs[0]
}
