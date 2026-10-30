package contract

import (
	"fmt"
	"net/http"
	"strings"
	"unicode"
)

// RouteKind is an opaque extension-owned operation identifier.
type RouteKind string

// CanonicalLegacyBasePath is the shared /v1 prefix used by existing frontends.
const CanonicalLegacyBasePath = "/v1"

// RouteClaim registers one normalized HTTP route owner before serving.
type RouteClaim struct {
	OwnerID string
	Method  string
	Path    string
	Kind    RouteKind
}

func (k RouteKind) Validate() error {
	s := string(k)
	if s == "" || len(s) > 96 {
		return fmt.Errorf("route claim: invalid kind")
	}
	for _, r := range s {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && !strings.ContainsRune("._-", r) {
			return fmt.Errorf("route claim: invalid kind %q", s)
		}
	}
	return nil
}

// NormalizeMethod uppercases and trims an HTTP method.
func NormalizeMethod(method string) (string, error) {
	m := strings.TrimSpace(strings.ToUpper(method))
	if m == "" {
		return "", fmt.Errorf("route claim: empty method")
	}
	if strings.ContainsAny(m, "\r\n\t") {
		return "", fmt.Errorf("route claim: method contains control characters")
	}
	if m != http.MethodGet && m != http.MethodPost && m != http.MethodPut &&
		m != http.MethodPatch && m != http.MethodDelete && m != http.MethodOptions {
		return "", fmt.Errorf("route claim: unsupported method %q", method)
	}
	return m, nil
}

// operatorRoutePathMeta lists the characters that carry meaning to the router or to
// the ingress self-defense normalizer rather than being literal path content.
//
//   - "?" and "#" would be split off as query and fragment by a decoder, and the
//     self-defense normalizer deliberately cuts at a decoded "?", so a path
//     containing one is never the same value for the operator, the router and the
//     normalizer.
//   - "*" and "\" are rejected by the existing route-claim policy.
//   - "{" and "}" are Go 1.22+ ServeMux pattern syntax: "{name}" matches one segment,
//     "{name...}" matches the rest, and "{$}" is end-of-path. A value containing one
//     is a pattern, not a literal path, while the owned-route carve compares literals.
//   - "%" is rejected because ServeMux unescapes both a pattern and a request path.
//
// The bundled frontends' own claims may legitimately use wildcards, so this policy
// applies to OPERATOR input only and [NormalizePath] keeps accepting them.
const operatorRoutePathMeta = "?#*\\{}%"

// ValidateOperatorRoutePath rejects an operator-supplied data-plane path that is not
// a literal route.
//
// The policy is closed on purpose rather than extended metacharacter by
// metacharacter: an operator path is owned by the router and by the ingress
// self-defense carve, and both reason about literal paths, so a value that means
// something else to either of them cannot be supported safely. An operator who wants
// a literal character writes the literal character.
func ValidateOperatorRoutePath(field, p string) error {
	if p == "" {
		return nil
	}
	if strings.ContainsAny(p, operatorRoutePathMeta) {
		return fmt.Errorf("%s: must be a literal path and must not contain any of %q "+
			"(ServeMux pattern syntax, a query or fragment separator, a wildcard, a backslash or a percent escape)",
			field, operatorRoutePathMeta)
	}
	if strings.Contains(p, "//") {
		return fmt.Errorf("%s: must not contain an empty path segment", field)
	}
	for segment := range strings.SplitSeq(p, "/") {
		if segment == "." || segment == ".." {
			return fmt.Errorf("%s: must not contain a %q path segment", field, segment)
		}
	}
	for _, r := range p {
		// Control characters and whitespace are rejected because they are not
		// comparable across a log line, a header and a route table.
		if r < 0x20 || r == 0x7f || unicode.IsSpace(r) {
			return fmt.Errorf("%s: must not contain control or whitespace characters", field)
		}
	}
	return nil
}

// NormalizePath canonicalizes a mount path for deterministic ownership checks.
func NormalizePath(path string) (string, error) {
	p := strings.TrimSpace(path)
	if p == "" {
		return "", fmt.Errorf("route claim: empty path")
	}
	if !strings.HasPrefix(p, "/") {
		return "", fmt.Errorf("route claim: path must be absolute")
	}
	if strings.ContainsAny(p, "?#*\\") {
		return "", fmt.Errorf("route claim: path contains invalid characters (query/fragment/wildcard/backslash)")
	}
	// A percent escape is rejected because http.ServeMux unescapes BOTH the
	// registered pattern and the request path. A pattern written with an escape
	// therefore owns requests whose DECODED path is the unescaped form, so the
	// configured path and the path a client actually sends are two different
	// strings. Anything that reasons about routes in the decoded space -- the
	// ingress self-defense owned-route carve above all -- would then be reasoning
	// about a path the router does not own, and a legitimate route could be
	// shadowed. Forbidding the escape keeps the configured path, the registered
	// pattern and the decoded request path the same value.
	if strings.Contains(p, "%") {
		return "", fmt.Errorf("route claim: path must not contain percent escapes (use the literal characters)")
	}
	if strings.Contains(p, "//") {
		return "", fmt.Errorf("route claim: path contains double slash")
	}
	segments := strings.SplitSeq(p, "/")
	for seg := range segments {
		if seg == "." || seg == ".." {
			return "", fmt.Errorf("route claim: path contains traversal segment %q", seg)
		}
	}
	if len(p) > 1 {
		p = strings.TrimRight(p, "/")
	}
	if p == "" {
		p = "/"
	}
	return p, nil
}

// NormalizedClaim returns a copy with normalized method and path.
func (c RouteClaim) NormalizedClaim() (RouteClaim, error) {
	method, err := NormalizeMethod(c.Method)
	if err != nil {
		return RouteClaim{}, err
	}
	path, err := NormalizePath(c.Path)
	if err != nil {
		return RouteClaim{}, err
	}
	if strings.TrimSpace(c.OwnerID) == "" {
		return RouteClaim{}, fmt.Errorf("route claim: empty owner id")
	}
	if err := c.Kind.Validate(); err != nil {
		return RouteClaim{}, err
	}
	out := c
	out.Method = method
	out.Path = path
	out.OwnerID = strings.TrimSpace(c.OwnerID)
	return out, nil
}

// ClaimsForBasePath builds claims for a frontend-owned operation set. The
// contract validates and normalizes generic ownership data; protocol packages
// own operation identifiers and route paths.
func ClaimsForBasePath(ownerID, basePath string, operations ...RouteClaim) ([]RouteClaim, error) {
	base, err := NormalizePath(basePath)
	if err != nil {
		return nil, err
	}
	out := make([]RouteClaim, 0, len(operations))
	for _, operation := range operations {
		operation.OwnerID = ownerID
		operation.Path = strings.TrimSuffix(base, "/") + "/" + strings.TrimPrefix(operation.Path, "/")
		normalized, err := operation.NormalizedClaim()
		if err != nil {
			return nil, err
		}
		out = append(out, normalized)
	}
	return out, nil
}
