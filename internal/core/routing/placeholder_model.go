package routing

import (
	"fmt"
	"strings"
)

// PlaceholderModelToken is the literal model token the configuration layer used
// to synthesize for a backend kind without a canonical default model. It is a
// placeholder, not a model id: no backend serves it, so a request routed to it
// is refused by the backend as an unknown model.
const PlaceholderModelToken = "model"

// RejectPlaceholderModel refuses a route selector that names the placeholder
// model token. Earlier releases printed such a route as the effective default
// route of a dynamic-inventory backend, so an operator can end up copying it
// into the configuration; the backend refuses the resulting requests, so the
// route is refused with the reason instead.
//
// Every leaf of the selector is inspected, including a parallel group embedded
// in a weighted branch, so no leaf can hide the placeholder.
//
// A selector that does not parse is left to the routing parser: this check only
// reports the placeholder.
func RejectPlaceholderModel(route string) error {
	sel, err := Parse(route)
	if err != nil {
		return nil
	}
	for _, leaf := range routeLeaves(sel) {
		if strings.TrimSpace(leaf.Model) != PlaceholderModelToken {
			continue
		}
		return fmt.Errorf(
			"routing: %q names the placeholder model %q, which no backend serves; "+
				"set routing.default_route to a model the backend serves "+
				"(for example \"backend:model\")",
			route, PlaceholderModelToken,
		)
	}
	return nil
}
