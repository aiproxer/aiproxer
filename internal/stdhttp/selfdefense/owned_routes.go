package selfdefense

import (
	"net/http"
	"net/url"
	"slices"
	"strings"

	httpcontract "github.com/aiproxer/aiproxer/internal/stdhttp/contract"
)

// ownedRouteProbeSegment is the concrete token substituted for a wildcard segment and
// appended to a literal candidate path to discover whether the router also registered
// that path as a subtree. Several protected operator mounts register BOTH the bare
// path and the path with a trailing separator, so probing the path alone would
// discover only the exact registration and silently leave the subtree it also owns
// uncarved.
const ownedRouteProbeSegment = "x"

// probePathsFor derives the concrete request paths used to ask the router about a
// candidate path.
//
// A literal candidate is probed with itself, which reveals an exact registration, and
// with a synthetic descendant, which the router answers with its own trailing-slash
// pattern for a subtree registration.
//
// A candidate containing pattern syntax cannot be probed with its own text: "{id}" in
// a request path is a literal segment, while in a pattern it matches one segment, so
// probing the pattern text would ask the wrong question. Instead the pattern is
// turned into a request the pattern actually matches: every single-segment wildcard
// becomes a concrete segment, and a trailing rest or end wildcard is dropped, because
// both match the empty remainder. That makes a wildcard claim -- such as the bundled
// openai-responses /v1/responses/{id}/cancel -- resolvable, which it otherwise is not.
func probePathsFor(path string) []string {
	if !strings.ContainsRune(path, '{') {
		return []string{path, path + "/" + ownedRouteProbeSegment}
	}
	segments := strings.Split(strings.Trim(path, "/"), "/")
	concrete := make([]string, 0, len(segments))
	for _, segment := range segments {
		switch {
		case strings.HasSuffix(segment, "...}") && strings.HasPrefix(segment, "{"),
			segment == "{$}":
			// A rest or end wildcard matches the empty remainder, so the concrete
			// request simply stops before it.
		case strings.HasPrefix(segment, "{") && strings.HasSuffix(segment, "}"):
			concrete = append(concrete, ownedRouteProbeSegment)
		default:
			concrete = append(concrete, segment)
		}
	}
	probe := "/" + strings.Join(concrete, "/")
	if probe == "/" {
		probe = "/"
	}
	return []string{probe, probe + "/" + ownedRouteProbeSegment}
}

// methodlessProbeMethod is an HTTP method token no route is ever registered for. It
// exists so the resolver can ask the router a question about methodless registrations
// that no ordinary method can answer: ServeMux lets a methodless registration coexist
// with method-specific ones for the same path, and the method-specific one wins for its
// own method. Probing only GET would therefore find "GET /x" and never learn that "/x"
// also exists, so a request the router routes to the methodless handler would look
// unowned. A token that cannot collide with a real registration falls through to the
// methodless one.
const methodlessProbeMethod = "AIPMETHODLESSPROBE"

// routeMethodMatches reports whether a registration carrying registered owns a
// request carrying candidate.
//
// It reproduces the router's own rule that a GET registration also answers HEAD,
// which a plain equality test gets wrong and would then let the gate refuse a request
// the router routes.
func routeMethodMatches(registered, candidate string) bool {
	if registered == "" || registered == candidate {
		return true
	}
	return registered == http.MethodGet && candidate == http.MethodHead
}

// OwnedRoutesFromMux resolves which of the candidates the router actually owns, and
// with which match semantics, by asking the router itself.
//
// Ownership is a property of the router, not of the configuration that produced the
// candidate, so this is resolved against the real *http.ServeMux rather than
// inferred from a path's shape. That is what makes the carve faithful:
//
//   - a configured path whose feature is disabled is not mounted, resolves to no
//     dedicated pattern, and therefore carves nothing;
//   - a literal registration is recorded as exact, and a trailing-slash
//     registration is recorded as a subtree, because that is what the router
//     actually does with it;
//   - the method is carried through, so a route registered for one method is not
//     treated as owning the other methods.
//
// The router's fallback is never ownership: every path it does not route resolves
// to a fallback handler, so a fallback pattern would otherwise carve the entire data
// plane. The bare root pattern and the empty pattern are therefore dropped.
//
// Resolution happens once per generation, at composition, and never on the request
// path.
func OwnedRoutesFromMux(mux *http.ServeMux, candidates []httpcontract.OwnedRouteCandidate) []httpcontract.OwnedRoute {
	if mux == nil || len(candidates) == 0 {
		return nil
	}
	var owned []httpcontract.OwnedRoute
	seen := make(map[httpcontract.OwnedRoute]struct{}, len(candidates)*2)
	record := func(pattern string) {
		method, routePath, subtree := splitDedicatedPattern(pattern)
		if routePath == "" {
			return
		}
		route := httpcontract.OwnedRoute{Method: method, Path: routePath, Subtree: subtree}
		if _, duplicate := seen[route]; duplicate {
			return
		}
		seen[route] = struct{}{}
		owned = append(owned, route)
	}
	for _, candidate := range candidates {
		path := normalizeOwnedPath(candidate.Path)
		if path == "" {
			continue
		}
		// An absent candidate method means the registration is method-less, so it
		// answers every method. The router still needs concrete methods to resolve
		// against, and two probes are needed because a method-less registration can
		// coexist with a method-specific one: the ordinary method reveals the
		// specific registration, and a token that cannot collide with any real
		// registration falls through to the method-less one. Recording whatever
		// dedicated patterns come back is what makes the result the router's full
		// answer for this path rather than one representative request's.
		method := normalizeOwnedMethod(candidate.Method)
		probeMethods := []string{method}
		if method == "" {
			probeMethods = []string{http.MethodGet, methodlessProbeMethod}
		} else {
			probeMethods = append(probeMethods, methodlessProbeMethod)
		}
		// Each method is asked about concrete probe paths, and the router's own
		// answer is recorded. A wildcard candidate cannot be probed with its own
		// text, because "{id}" in a request path is a literal segment while in a
		// pattern it matches one, so the probe paths are derived from the pattern.
		for _, probeMethod := range probeMethods {
			for _, probe := range probePathsFor(path) {
				record(dedicatedPatternFor(mux, probeMethod, probe))
			}
		}
	}
	slices.SortFunc(owned, compareOwnedRoutes)
	return owned
}

// OwnedRoutes is an immutable, pre-compiled set of the routes the running
// generation owns. It is built once when the gate is constructed, so deciding
// whether a path is owned costs a slice comparison and allocates nothing: an
// attacker chooses how often the branch that consults it runs, so the work must not
// be per-request proportional to the inventory size.
type OwnedRoutes struct {
	routes []httpcontract.OwnedRoute
}

// NewOwnedRoutes compiles the inventory into its immutable form. A route that is
// empty, blank, relative, or the root path itself owns nothing and is dropped, so a
// degenerate inventory cannot widen the carve to the whole data plane.
func NewOwnedRoutes(routes []httpcontract.OwnedRoute) OwnedRoutes {
	compiled := make([]httpcontract.OwnedRoute, 0, len(routes))
	for _, route := range routes {
		path := normalizeOwnedPath(route.Path)
		if path == "" || path == "/" {
			continue
		}
		if !route.Subtree {
			path = strings.TrimSuffix(path, "/")
			if path == "" {
				continue
			}
		}
		compiled = append(compiled, httpcontract.OwnedRoute{
			Method:  normalizeOwnedMethod(route.Method),
			Path:    path,
			Subtree: route.Subtree,
		})
	}
	if len(compiled) == 0 {
		return OwnedRoutes{}
	}
	slices.SortFunc(compiled, compareOwnedRoutes)
	return OwnedRoutes{routes: slices.CompactFunc(compiled, sameOwnedRoute)}
}

// Covers reports whether the router owns this exact request target, using the
// router's own semantics: case-sensitive comparison, an exact registration matching
// only its own method and path, and a subtree registration matching everything
// below its path.
//
// Matching is case-SENSITIVE on purpose. http.ServeMux matches literal patterns
// case-sensitively, so /WP-ADMIN/responses is not the route /wp-admin/responses and
// is correctly an impossible-path probe rather than a carved request.
func (o OwnedRoutes) Covers(method, path string) bool {
	if len(o.routes) == 0 {
		return false
	}
	target := normalizeOwnedPath(path)
	if target == "" {
		return false
	}
	for _, route := range o.routes {
		if !routeMethodMatches(route.Method, method) {
			continue
		}
		if route.Subtree {
			// route.Path already ends in a separator, so a prefix match is a
			// descendant. The bare root of the subtree is owned too, because the router
			// answers it with a canonicalizing redirect rather than falling through.
			if strings.HasPrefix(target, route.Path) || target == strings.TrimSuffix(route.Path, "/") {
				return true
			}
			continue
		}
		if target == route.Path || patternMatches(route.Path, target) {
			return true
		}
	}
	return false
}

// patternMatches reports whether a router pattern owns a concrete path.
//
// A published route is not always a literal path: the bundled openai-responses
// frontend claims /v1/responses/{id}/cancel, so a comparison by string equality would
// record the pattern and never match any request the router routes to it. The router's
// own segment grammar is therefore reproduced here:
//
//   - "{name}" matches exactly one non-empty segment;
//   - "{name...}" matches the remaining zero or more segments;
//   - "{$}" matches only the end of the path.
//
// A pattern containing none of these is a literal and never reaches this function, so
// the ordinary request path pays nothing for wildcard support.
func patternMatches(pattern, target string) bool {
	if !strings.ContainsRune(pattern, '{') {
		return false
	}
	patternSegments := dropTrailingEmpty(strings.Split(strings.Trim(pattern, "/"), "/"))
	targetSegments := dropTrailingEmpty(strings.Split(strings.Trim(target, "/"), "/"))
	for i, segment := range patternSegments {
		switch {
		case segment == "{$}":
			// End-of-path marker: it must be last in the pattern and nothing may remain.
			return i == len(patternSegments)-1 && i == len(targetSegments)
		case strings.HasSuffix(segment, "...}") && strings.HasPrefix(segment, "{"):
			// A rest wildcard absorbs everything left, including nothing at all.
			return i == len(patternSegments)-1
		case strings.HasPrefix(segment, "{") && strings.HasSuffix(segment, "}"):
			if i >= len(targetSegments) || targetSegments[i] == "" {
				return false
			}
		default:
			if i >= len(targetSegments) || targetSegments[i] != segment {
				return false
			}
		}
	}
	return len(patternSegments) == len(targetSegments)
}

// dropTrailingEmpty removes one empty trailing segment, because the router treats a
// trailing separator as equivalent to its absence: /b and /b/ are both owned by
// "POST /b/{$}".
func dropTrailingEmpty(segments []string) []string {
	if n := len(segments); n > 0 && segments[n-1] == "" {
		return segments[:n-1]
	}
	return segments
}

// dedicatedPatternFor asks the router which pattern owns this method/path, and
// returns "" when the answer is the router's fallback rather than a real
// registration.
func dedicatedPatternFor(mux *http.ServeMux, method, path string) string {
	req := &http.Request{Method: method, URL: &url.URL{Path: path}}
	_, pattern := mux.Handler(req)
	return pattern
}

// isDedicatedPattern reports whether a resolved pattern is a real registration
// rather than the router's fallback. Both the empty pattern (nothing registered at
// all) and the bare root "/" mean "this path is not routed anywhere specific".
func isDedicatedPattern(pattern string) bool {
	_, path, _ := splitDedicatedPattern(pattern)
	return path != ""
}

// splitDedicatedPattern splits a Go 1.22+ ServeMux pattern of the form
// "METHOD PATH" (or a bare "PATH"), and reports whether the path is a trailing-slash
// subtree registration. It returns an empty path for a fallback pattern.
func splitDedicatedPattern(pattern string) (method, path string, subtree bool) {
	if pattern == "" {
		return "", "", false
	}
	// A method-less registration is a bare "PATH"; a method-scoped one is
	// "METHOD PATH". Defaulting path to the whole pattern is what keeps a
	// method-less registration, which is most of them, from being dropped.
	method, path = "", pattern
	if before, rest, found := strings.Cut(pattern, " "); found {
		method, path = before, strings.TrimSpace(rest)
	}
	if path == "" || path == "/" {
		return "", "", false
	}
	if strings.HasSuffix(path, "/") {
		return method, path, true
	}
	return method, path, false
}

// normalizeOwnedPath reduces a path to the absolute, single-slash-normalized form the
// router compares against. It is the same normalization the frozen families use, so
// the carve and the rules agree on what a path is.
func normalizeOwnedPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if cut := strings.IndexByte(path, '?'); cut >= 0 {
		path = path[:cut]
	}
	if count := leadingSlashCount(path); count > 1 {
		path = path[count-1:]
	}
	if path == "/" {
		return ""
	}
	return path
}

// normalizeOwnedMethod upper-cases a method and reduces an absent one to the empty
// string, which matches every method the way a registration without a method does.
func normalizeOwnedMethod(method string) string {
	return strings.ToUpper(strings.TrimSpace(method))
}

func compareOwnedRoutes(a, b httpcontract.OwnedRoute) int {
	if a.Path != b.Path {
		if a.Path < b.Path {
			return -1
		}
		return 1
	}
	if a.Method != b.Method {
		if a.Method < b.Method {
			return -1
		}
		return 1
	}
	if a.Subtree == b.Subtree {
		return 0
	}
	if a.Subtree {
		return 1
	}
	return -1
}

func sameOwnedRoute(a, b httpcontract.OwnedRoute) bool { return a == b }
