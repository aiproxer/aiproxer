package contract

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// ErrRouteConflict indicates two enabled owners claim the same normalized route.
var ErrRouteConflict = errors.New("route registry: conflict")

// RouteConflictError names both owners for deterministic composition failure.
type RouteConflictError struct {
	Method        string
	Path          string
	ExistingOwner string
	NewOwner      string
	ExistingKind  RouteKind
	NewKind       RouteKind
}

func (d RouteConflictError) Error() string {
	return fmt.Sprintf("%s: %s %s owned by %q (%s) conflicts with %q (%s)",
		ErrRouteConflict, d.Method, d.Path, d.ExistingOwner, d.ExistingKind, d.NewOwner, d.NewKind)
}

// RouteRegistry validates normalized route ownership before serving.
type RouteRegistry struct {
	claims []RouteClaim
	index  map[string]RouteClaim
}

// NewRouteRegistry returns an empty registry.
func NewRouteRegistry() *RouteRegistry {
	return &RouteRegistry{index: make(map[string]RouteClaim)}
}

// Register adds one normalized claim or returns a deterministic conflict.
func (r *RouteRegistry) Register(claim RouteClaim) error {
	norm, err := claim.NormalizedClaim()
	if err != nil {
		return err
	}
	key := routeKey(norm.Method, norm.Path)
	existing, occupied := r.index[key]
	if occupied && (existing.OwnerID != norm.OwnerID || existing.Kind != norm.Kind) {
		return RouteConflictError{
			Method:        norm.Method,
			Path:          norm.Path,
			ExistingOwner: existing.OwnerID,
			NewOwner:      norm.OwnerID,
			ExistingKind:  existing.Kind,
			NewKind:       norm.Kind,
		}
	}
	if !occupied {
		r.claims = append(r.claims, norm)
	}
	r.index[key] = norm
	return nil
}

// RegisterAll registers claims in order, failing on the first conflict. The
// operation is atomic at the candidate level: every claim is validated against
// a staged copy before the registry is mutated, so a conflict leaves the
// registry unchanged (no partial registration).
func (r *RouteRegistry) RegisterAll(claims []RouteClaim) error {
	if len(claims) == 0 {
		return nil
	}
	staged := NewRouteRegistry()
	for _, c := range r.claims {
		if err := staged.Register(c); err != nil {
			return err
		}
	}
	for _, c := range claims {
		if err := staged.Register(c); err != nil {
			return err
		}
	}
	for _, c := range claims {
		if err := r.Register(c); err != nil {
			return err
		}
	}
	return nil
}

// ValidateCanonicalPathTakeover rejects base_path=/v1 when any registered claim
// would collide with an already-owned method/path pair.
func (r *RouteRegistry) ValidateCanonicalPathTakeover(basePath string, proposed []RouteClaim) error {
	normBase, err := NormalizePath(basePath)
	if err != nil {
		return err
	}
	if normBase != CanonicalLegacyBasePath {
		return nil
	}
	for _, claim := range proposed {
		norm, err := claim.NormalizedClaim()
		if err != nil {
			return err
		}
		key := routeKey(norm.Method, norm.Path)
		if existing, ok := r.index[key]; ok && existing.OwnerID != norm.OwnerID {
			return RouteConflictError{
				Method:        norm.Method,
				Path:          norm.Path,
				ExistingOwner: existing.OwnerID,
				NewOwner:      norm.OwnerID,
				ExistingKind:  existing.Kind,
				NewKind:       norm.Kind,
			}
		}
	}
	return nil
}

// Claims returns a sorted copy of registered claims.
func (r *RouteRegistry) Claims() []RouteClaim {
	out := append([]RouteClaim(nil), r.claims...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		if out[i].Method != out[j].Method {
			return out[i].Method < out[j].Method
		}
		return out[i].OwnerID < out[j].OwnerID
	})
	return out
}

// OwnerOf returns the registered owner for a normalized method/path pair.
func (r *RouteRegistry) OwnerOf(method, path string) (RouteClaim, bool) {
	m, err := NormalizeMethod(method)
	if err != nil {
		return RouteClaim{}, false
	}
	p, err := NormalizePath(path)
	if err != nil {
		return RouteClaim{}, false
	}
	claim, ok := r.index[routeKey(m, p)]
	return claim, ok
}

func routeKey(method, path string) string {
	return method + "\x00" + path
}

// RemapBasePath rewrites OpenResponses default claims onto another base path.
func RemapBasePath(claims []RouteClaim, fromBase, toBase string) ([]RouteClaim, error) {
	from, err := NormalizePath(fromBase)
	if err != nil {
		return nil, err
	}
	to, err := NormalizePath(toBase)
	if err != nil {
		return nil, err
	}
	out := make([]RouteClaim, 0, len(claims))
	for _, c := range claims {
		if !strings.HasPrefix(c.Path, from) {
			out = append(out, c)
			continue
		}
		suffix := strings.TrimPrefix(c.Path, from)
		if suffix == "" {
			suffix = "/"
		}
		nc := c
		nc.Path = to + suffix
		if nc.Path != to && strings.HasSuffix(nc.Path, "/") {
			nc.Path = strings.TrimRight(nc.Path, "/")
		}
		norm, err := nc.NormalizedClaim()
		if err != nil {
			return nil, err
		}
		out = append(out, norm)
	}
	return out, nil
}
