package routing

import (
	"errors"
	"fmt"
	"strings"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// ErrUnknownBackend reports a compiled selector that names a backend identity
// not present in the current generation's side-effect-free registry view.
var ErrUnknownBackend = errors.New("routing: unknown backend")

// ErrNoRouteSelector reports that no route selector could be resolved at all:
// the request carried no model with a known route prefix, and the configuration
// defines no default route for the request to fall back to. It is an invalid
// selector with the operator-facing reason attached.
var ErrNoRouteSelector = fmt.Errorf(
	"%w: no route selector: send a model with a route prefix "+
		"(for example \"backend:model\") or set routing.default_route",
	ErrInvalidSelector,
)

// CompileSelector is the shared pure compile path for admin preflight and
// buildRoutePlan: trim, alias resolution, parse, model-only defaulting, and
// unresolved-model-only rejection. It must not allocate B-legs, open backends,
// bind native models, or mutate affinity/weighted-first/interleaved state.
func CompileSelector(raw string, aliases *AliasResolver, defaultBackend string) (*Selector, error) {
	selStr := strings.TrimSpace(raw)
	if selStr == "" {
		// An absent selector stays absent: an alias rule must not turn "no
		// route" into a route to some provider.
		return nil, ErrNoRouteSelector
	}
	if aliases != nil {
		selStr = aliases.Resolve(selStr)
	}
	if selStr == "" {
		return nil, ErrNoRouteSelector
	}
	sel, err := Parse(selStr)
	if err != nil {
		return nil, err
	}
	ApplyModelOnlyBackends(sel, defaultBackend)
	if SelectorHasEmptyBackend(sel) {
		return nil, fmt.Errorf("%w", aipapi.ErrUnresolvedModelOnlySelector)
	}
	return sel, nil
}

// RejectUnknownBackends reports referenced backend IDs absent from known.
// A nil known set means identities were not available without I/O; the check
// is skipped. An empty (non-nil) set rejects every explicit backend ID.
func RejectUnknownBackends(sel *Selector, known map[string]struct{}) error {
	if sel == nil || known == nil {
		return nil
	}
	for _, id := range BackendIDsReferenced(sel) {
		if _, ok := known[id]; !ok {
			return fmt.Errorf("%w %q", ErrUnknownBackend, id)
		}
	}
	return nil
}
