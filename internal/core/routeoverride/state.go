package routeoverride

import (
	"fmt"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// State is the revisioned active/inactive routing override for one A-leg.
type State struct {
	ALegID    string
	Active    bool
	Selector  string // empty when inactive
	Revision  int64  // 0 = never mutated
	UpdatedAt time.Time
}

// NormalizeSelector trims surrounding whitespace. Empty after trim is inactive input.
func NormalizeSelector(raw string) string {
	return strings.TrimSpace(raw)
}

// Inactive returns the never-mutated revision-0 state for an existing A-leg.
func Inactive(aLegID string) State {
	return State{ALegID: strings.TrimSpace(aLegID)}
}

// Clone returns a value copy. Selector is a string, so callers cannot alias
// a mutable buffer held by the store.
func (s State) Clone() State {
	return s
}

// Validate enforces D2 invariants for a complete state value.
func (s State) Validate() error {
	if s.Revision < 0 {
		return fmt.Errorf("routeoverride: negative revision")
	}
	if s.Revision == 0 {
		if s.Active {
			return fmt.Errorf("routeoverride: revision 0 must be inactive")
		}
		if s.Selector != "" {
			return fmt.Errorf("routeoverride: revision 0 must not store a selector")
		}
		if !s.UpdatedAt.IsZero() {
			return fmt.Errorf("routeoverride: revision 0 must have zero updated_at")
		}
		return nil
	}
	if s.Active {
		if strings.TrimSpace(s.Selector) == "" {
			return fmt.Errorf("routeoverride: active state requires a selector")
		}
		if len(s.Selector) > aipapi.MaxRouteSelectorBytes {
			return fmt.Errorf("routeoverride: selector exceeds %d bytes", aipapi.MaxRouteSelectorBytes)
		}
	} else if s.Selector != "" {
		return fmt.Errorf("routeoverride: inactive state must not store a selector")
	}
	if s.UpdatedAt.IsZero() {
		return fmt.Errorf("routeoverride: non-zero revision requires updated_at")
	}
	return nil
}
