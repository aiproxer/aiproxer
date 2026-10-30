package routing

import (
	"errors"
	"fmt"
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

// ErrUnsafeExecutionComposition is the root sentinel for unsafe selector composition.
var ErrUnsafeExecutionComposition = errors.New("routing: unsafe backend execution composition")

// BackendExecutionResolver resolves the execution class of a configured backend instance.
// If the backend instance is not configured in the active generation, ok is false.
type BackendExecutionResolver interface {
	ResolveBackendExecution(backendID string) (class aipsdk.BackendExecutionClass, ok bool)
}

// BackendExecutionResolverFunc allows using an ordinary function as a BackendExecutionResolver.
type BackendExecutionResolverFunc func(backendID string) (aipsdk.BackendExecutionClass, bool)

func (f BackendExecutionResolverFunc) ResolveBackendExecution(backendID string) (aipsdk.BackendExecutionClass, bool) {
	if f == nil {
		return aipsdk.BackendExecutionUnknown, false
	}
	return f(backendID)
}

// UnsafeExecutionCompositionError records details of an unsafe composition rejection.
type UnsafeExecutionCompositionError struct {
	Composition string
	BackendID   string
	Class       aipsdk.BackendExecutionClass
	Policy      config.ExecutionCompositionPolicy
}

func (e *UnsafeExecutionCompositionError) Error() string {
	if e == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("routing: unsafe backend execution composition: ")
	if e.Composition != "" {
		b.WriteString(e.Composition)
		b.WriteString(" contains ")
	}
	if e.BackendID != "" {
		b.WriteString("backend ")
		fmt.Fprintf(&b, "%q", e.BackendID)
	}
	if e.Class != "" {
		b.WriteString(" with execution class ")
		fmt.Fprintf(&b, "%q", e.Class)
	} else {
		b.WriteString(" with unclassified execution metadata")
	}
	b.WriteString("; direct routing is supported; policy is ")
	if e.Policy != "" {
		fmt.Fprintf(&b, "%q", e.Policy)
	} else {
		fmt.Fprintf(&b, "%q", config.ExecutionCompositionSafe)
	}
	return b.String()
}

func (e *UnsafeExecutionCompositionError) Unwrap() error {
	return ErrUnsafeExecutionComposition
}

// IsDirectPrimary returns true if the selector contains exactly one failover alternative
// and that alternative is a single direct Primary (not weighted or parallel).
func IsDirectPrimary(sel *Selector) bool {
	return sel != nil &&
		len(sel.Alternatives) == 1 &&
		sel.Alternatives[0].Primary != nil
}

// ValidateExecutionComposition checks whether a compiled selector AST conforms to the
// generation's execution composition policy.
// Under safe policy:
//   - A direct primary is always permitted (regardless of backend execution class).
//   - Composed selectors (weighted, parallel, thinker, failover) require every reachable
//     configured backend to be explicitly classified as inference.
//   - Reachable backends that are absent from the resolver are ignored here (preserving
//     existing missing-backend authority).
//   - If classes resolver is nil when evaluating a composite under safe policy, it fails closed.
//
// Under unrestricted policy:
// - All compositions are allowed.
func ValidateExecutionComposition(
	sel *Selector,
	classes BackendExecutionResolver,
	policy config.ExecutionCompositionPolicy,
) error {
	if sel == nil {
		return nil
	}
	effectivePolicy := policy
	if effectivePolicy == "" {
		effectivePolicy = config.ExecutionCompositionSafe
	}
	if effectivePolicy == config.ExecutionCompositionUnrestricted {
		return nil
	}
	if IsDirectPrimary(sel) {
		return nil
	}
	if classes == nil {
		return fmt.Errorf("%w: execution resolver unavailable", ErrUnsafeExecutionComposition)
	}

	for _, alt := range sel.Alternatives {
		if err := validateAltExecutionComposition(alt, len(sel.Alternatives) > 1, classes, effectivePolicy); err != nil {
			return err
		}
	}
	return nil
}

func validateAltExecutionComposition(
	alt FailoverAlt,
	inFailover bool,
	classes BackendExecutionResolver,
	policy config.ExecutionCompositionPolicy,
) error {
	switch {
	case alt.Primary != nil:
		if inFailover {
			return checkPrimaryClass(*alt.Primary, "failover selector", classes, policy)
		}
		return nil

	case alt.Weighted != nil:
		compName := "weighted selector"
		hasThinker := false
		for _, b := range alt.Weighted.Branches {
			if b.IsThinker {
				hasThinker = true
				break
			}
		}
		if hasThinker {
			compName = "thinker hybrid selector"
		}
		for _, b := range alt.Weighted.Branches {
			if b.Parallel != nil {
				for _, leg := range b.Parallel.Branches {
					if err := checkPrimaryClass(leg.Target, compName, classes, policy); err != nil {
						return err
					}
				}
				continue
			}
			if err := checkPrimaryClass(b.Target, compName, classes, policy); err != nil {
				return err
			}
		}
		return nil

	case alt.Parallel != nil:
		compName := "parallel selector"
		if inFailover {
			compName = "failover parallel selector"
		}
		for _, b := range alt.Parallel.Branches {
			if err := checkPrimaryClass(b.Target, compName, classes, policy); err != nil {
				return err
			}
		}
		return nil
	}
	return nil
}

func checkPrimaryClass(
	p Primary,
	comp string,
	classes BackendExecutionResolver,
	policy config.ExecutionCompositionPolicy,
) error {
	backendID := strings.TrimSpace(p.Backend)
	if backendID == "" {
		return nil
	}
	class, ok := classes.ResolveBackendExecution(backendID)
	if !ok {
		// Backend is absent from the generation; defer to missing-backend authority
		return nil
	}
	if class != aipsdk.BackendExecutionInference {
		return &UnsafeExecutionCompositionError{
			Composition: comp,
			BackendID:   backendID,
			Class:       class,
			Policy:      policy,
		}
	}
	return nil
}
