package routing

import (
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/interleavedstate"
)

// InitialCandidates extracts all possible attempt candidates from a compiled
// selector AST, preserving exact sequential/fallback, weighted, and race candidate
// order and membership (Requirements 7.3, 8.3, 8.4, 10.3, 10.4).
// A nil selector or one with no alternatives returns nil.
func InitialCandidates(sel *Selector) []AttemptCandidate {
	if sel == nil || len(sel.Alternatives) == 0 {
		return nil
	}
	var out []AttemptCandidate
	for _, alt := range sel.Alternatives {
		switch {
		case alt.Primary != nil:
			c := AttemptCandidate{
				Primary: *alt.Primary,
				Key:     alt.Primary.String(),
			}
			out = append(out, c)
		case alt.Weighted != nil:
			// Thinker-aware groups tag every member exactly as canonical planning
			// does (stickyCandidate/pickThinkerCycle): thinker branches carry
			// RoleThinker, all other members RoleExecutor, plus the shared
			// selector key. Non-thinker groups leave both at zero values.
			thinkerAware := false
			for _, b := range alt.Weighted.Branches {
				if b.IsThinker {
					thinkerAware = true
					break
				}
			}
			var selKey string
			if thinkerAware {
				keys := make([]string, 0, len(alt.Weighted.Branches))
				for _, b := range alt.Weighted.Branches {
					keys = append(keys, branchKey(b))
				}
				selKey = strings.Join(keys, "^")
			}
			for _, b := range alt.Weighted.Branches {
				if b.Parallel != nil {
					for _, leg := range b.Parallel.Branches {
						c := AttemptCandidate{
							Primary:    leg.Target,
							Key:        leg.Target.String(),
							IsParallel: true,
							Handicap:   leg.Handicap,
						}
						if b.IsFirst {
							c.MarkedFirst = true
						}
						if thinkerAware {
							c.InterleavedRole = interleavedstate.RoleExecutor
							c.SelectorKey = selKey
						}
						out = append(out, c)
					}
					continue
				}
				c := AttemptCandidate{
					Primary:     b.Target,
					Key:         b.Target.String(),
					MarkedFirst: b.IsFirst,
				}
				if thinkerAware {
					c.SelectorKey = selKey
					if b.IsThinker {
						c.InterleavedRole = interleavedstate.RoleThinker
					} else {
						c.InterleavedRole = interleavedstate.RoleExecutor
					}
				}
				out = append(out, c)
			}
		case alt.Parallel != nil:
			for _, b := range alt.Parallel.Branches {
				c := AttemptCandidate{
					Primary:    b.Target,
					Key:        b.Target.String(),
					IsParallel: true,
					Handicap:   b.Handicap,
				}
				out = append(out, c)
			}
		}
	}
	return out
}

// CandidateEqual reports whether two AttemptCandidates have identical routing identity,
// including backend, model, native model, isParallel, handicap, markedFirst, role, and selector key.
func CandidateEqual(a, b AttemptCandidate) bool {
	return a.Primary.Backend == b.Primary.Backend &&
		a.Primary.Model == b.Primary.Model &&
		a.Primary.NativeModel == b.Primary.NativeModel &&
		a.Key == b.Key &&
		a.IsParallel == b.IsParallel &&
		a.Handicap == b.Handicap &&
		a.MarkedFirst == b.MarkedFirst &&
		a.InterleavedRole == b.InterleavedRole &&
		a.SelectorKey == b.SelectorKey
}

// CandidateSetsEqual reports whether two candidate slices match in exact order and membership.
func CandidateSetsEqual(a, b []AttemptCandidate) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !CandidateEqual(a[i], b[i]) {
			return false
		}
	}
	return true
}

// PrepareSelector compiles the selector, validates execution composition under the
// active policy, and binds native model IDs if a resolver is configured.
// It is the shared pure preparation logic for both canonical route planning
// (Executor.buildRoutePlan) and wire assessment (ComposeInitialCandidates,
// InitialRouteAssessmentGate) to eliminate any risk of routing drift (Requirements 7, 8, 19).
func PrepareSelector(
	raw string,
	aliases *AliasResolver,
	defaultBackend string,
	classes BackendExecutionResolver,
	policy config.ExecutionCompositionPolicy,
	resolver NativeModelResolver,
) (*Selector, error) {
	sel, err := CompileSelector(raw, aliases, defaultBackend)
	if err != nil {
		return nil, err
	}
	if err := ValidateExecutionComposition(sel, classes, policy); err != nil {
		return nil, err
	}
	if resolver != nil {
		if err := BindNativeModelIDs(sel, resolver); err != nil {
			return nil, err
		}
	}
	return sel, nil
}

// ComposeInitialCandidates runs the full canonical routing composition sequence:
// 1. Selector compile (trim, alias resolution, parse, model-only defaulting, and unresolved-model-only rejection).
// 2. Execution composition validation under the active generation policy.
// 3. Request-bound native model binding (fail-closed on wrong-backend canonical).
// 4. Initial candidate extraction in exact order and membership.
//
// It is pure and side-effect-free: no B-legs, no I/O, no live store mutation.
func ComposeInitialCandidates(
	raw string,
	aliases *AliasResolver,
	defaultBackend string,
	classes BackendExecutionResolver,
	policy config.ExecutionCompositionPolicy,
	resolver NativeModelResolver,
) ([]AttemptCandidate, *Selector, error) {
	sel, err := PrepareSelector(raw, aliases, defaultBackend, classes, policy, resolver)
	if err != nil {
		return nil, nil, err
	}
	cands := InitialCandidates(sel)
	return cands, sel, nil
}
