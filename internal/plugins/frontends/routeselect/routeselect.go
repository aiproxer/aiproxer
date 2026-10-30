// Package routeselect derives explicit route selectors from model identifiers.
// Route selectors use "backend-prefix:model" to route requests to a backend family.
package routeselect

import (
	"encoding/json"
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/routing"
)

type PrefixSet map[string]struct{}

func NewPrefixSet(prefixes []string) PrefixSet {
	filtered := routing.FilterRoutePrefixes(prefixes)
	out := make(PrefixSet, len(filtered))
	for _, prefix := range filtered {
		out[prefix] = struct{}{}
	}
	return out
}

// InlineOrDefault returns model when it has a known backend prefix before the colon delimiter.
// Otherwise it returns defaultRoute with surrounding whitespace removed.
func (p PrefixSet) InlineOrDefault(model, defaultRoute string) string {
	return p.ResolveInlineOrDefault(model, defaultRoute).Selector
}

// ResolveInlineOrDefault returns the route selector for a client model and the
// origin that produced it.
//
// The origin is the route's provenance: it is the default origin only when the
// client named no route, so the proxy chose its default route. A client that
// named a route keeps it, even when it names the route the default would have.
func (p PrefixSet) ResolveInlineOrDefault(model, defaultRoute string) routing.ResolvedRoute {
	model = strings.TrimSpace(model)
	prefix, _, ok := strings.Cut(model, ":")
	if ok {
		if _, known := p[strings.TrimSpace(prefix)]; known {
			return routing.ExplicitRoute(model)
		}
	}
	return routing.DefaultRoute(defaultRoute)
}

// FromModelOrDefault parses body for a model field and returns it when it carries a known inline route prefix.
// If decoding fails or the model has no known prefix, it returns defaultRoute with surrounding whitespace removed.
func (p PrefixSet) FromModelOrDefault(body []byte, defaultRoute string) string {
	return p.ResolveFromModelOrDefault(body, defaultRoute).Selector
}

// ResolveFromModelOrDefault parses body for a model field and returns the route
// selector with the origin that produced it. See
// [PrefixSet.ResolveInlineOrDefault] for the provenance.
func (p PrefixSet) ResolveFromModelOrDefault(body []byte, defaultRoute string) routing.ResolvedRoute {
	var req struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &req); err == nil {
		return p.ResolveInlineOrDefault(req.Model, defaultRoute)
	}
	return routing.DefaultRoute(defaultRoute)
}
