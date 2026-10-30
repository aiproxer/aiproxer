package billing

import (
	"context"
	"fmt"
)

// ExposureAdmissionPreflightInput carries the bounded facts a host can offer
// before it prices anything: the ordered planned-route facts of the route
// plan the call would take.
//
// The input is intentionally free of a quote: a preflight runs before the
// quote exists, so requiring one would force a fabricated offer. The trusted
// customer scope, the credited account, and the call identity are validated
// by the host before the preflight runs, so this input carries only the facts
// a preflight consumer cannot derive on its own.
type ExposureAdmissionPreflightInput struct {
	// Routes carries the ordered planned-route facts: one entry per planned
	// route occurrence, in the shared planned-leaf order, duplicates
	// included. An empty list means the plan named no concrete backend
	// instance.
	Routes []AdmissionRouteFact `json:"routes,omitempty"`
}

// Validate requires a bounded route-fact list whose entries are valid.
func (in ExposureAdmissionPreflightInput) Validate() error {
	if len(in.Routes) > MaxAdmissionRoutes {
		return fmt.Errorf("%w: admission route bound exceeded", ErrInvalidAdmission)
	}
	for i, route := range in.Routes {
		if err := route.Validate(); err != nil {
			return fmt.Errorf("%w: route %d: %v", ErrInvalidAdmission, i, err)
		}
	}
	return nil
}

// Clone returns an independent copy of the input.
func (in ExposureAdmissionPreflightInput) Clone() ExposureAdmissionPreflightInput {
	out := in
	out.Routes = cloneAdmissionRouteFacts(in.Routes)
	return out
}

// ExposureAdmissionPreflighter is the optional pre-quote admission refinement.
// A host that publishes a binding whose admission port also implements this
// interface calls the preflight once per admission, after its own trusted
// input checks and before it asks the quoter for a price.
//
// The preflight has no side effect: it may only refuse the admission. A
// binding whose admission port does not implement it keeps the exact
// pre-quote behavior of the host.
type ExposureAdmissionPreflighter interface {
	PreflightExposureAdmission(ctx context.Context, in ExposureAdmissionPreflightInput) error
}
