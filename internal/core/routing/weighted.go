package routing

import (
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/interleavedstate"
)

// cycleSequenceEqual reports whether two cycle sequences have identical entries
// (key and role) in order. Used to detect a stored cycle whose selector key
// matches the fresh build but whose branch weights, roles, or order changed.
func cycleSequenceEqual(a, b []interleavedstate.CycleEntry) bool {
	return slices.EqualFunc(a, b, func(x, y interleavedstate.CycleEntry) bool {
		return x.Key == y.Key && x.Role == y.Role
	})
}

func effectiveWeight(w int) int64 {
	if w <= 0 {
		return 1
	}
	return int64(w)
}

const maxThinkerCycleWeightRepeats int64 = 100

func thinkerCycleWeight(w int) int64 {
	wt := effectiveWeight(w)
	if wt > maxThinkerCycleWeightRepeats {
		return maxThinkerCycleWeightRepeats
	}
	return wt
}

// sumEligibleWeights returns the sum of effective weights or an error if the sum overflows int64
// or exceeds [math.MaxInt] (so it cannot be passed to [Rng.Intn]).
func sumEligibleWeights(eligible []WeightedBranch) (int64, error) {
	var total int64
	for _, b := range eligible {
		wt := effectiveWeight(b.Weight)
		if wt > 0 && total > math.MaxInt64-wt {
			return 0, ErrWeightedTotalTooLarge
		}
		total += wt
	}
	if total < 1 {
		return 0, ErrNoEligibleCandidate
	}
	if total > int64(math.MaxInt) {
		return 0, ErrWeightedTotalTooLarge
	}
	return total, nil
}

// pickWeighted selects one branch from w using opt (exclusions, session, RNG, retry path).
// The second return is true when the [first] branch was taken and the session should record
// consumption. The third return is the next thinker-aware cycle state when w contains a
// thinker branch, or nil for non-thinker weighted selectors.
func pickWeighted(w *Weighted, opt PlanOptions) ([]AttemptCandidate, bool, *interleavedstate.CycleState, error) {
	if w == nil || len(w.Branches) == 0 {
		return nil, false, nil, ErrNoEligibleCandidate
	}
	thinkerIdx := -1
	for i, b := range w.Branches {
		if b.IsThinker {
			thinkerIdx = i
			break
		}
	}
	if thinkerIdx < 0 {
		c, consumeFirst, err := pickWeightedLegacy(w, opt)
		if err != nil {
			return nil, false, nil, err
		}
		return []AttemptCandidate{c}, consumeFirst, nil, nil
	}
	return pickThinkerCycle(w, opt)
}

// pickWeightedLegacy is the RNG-rolled weighted pick for selectors without a thinker branch.
// Existing weighted behavior (first-request steering, RNG roll, retry-path [first] suppression)
// is preserved unchanged.
func pickWeightedLegacy(w *Weighted, opt PlanOptions) (AttemptCandidate, bool, error) {
	eligible := make([]WeightedBranch, 0, len(w.Branches))
	for _, b := range w.Branches {
		key := b.Target.String()
		if !candidateEligible(b.Target, key, opt) {
			continue
		}
		eligible = append(eligible, b)
	}
	if len(eligible) == 0 {
		return AttemptCandidate{}, false, ErrNoEligibleCandidate
	}

	// Req 7.1 / 7.2 / 7.4: first-request steering only on initial path, not retry/failover path.
	if !opt.IsRetryPath && opt.Session != nil && !opt.Session.FirstRequestConsumed {
		var first *WeightedBranch
		for i := range eligible {
			if eligible[i].IsFirst {
				if first != nil {
					return AttemptCandidate{}, false, fmt.Errorf("internal: multiple [first] in eligible set")
				}
				f := eligible[i]
				first = &f
			}
		}
		if first != nil {
			return AttemptCandidate{
				Primary:     first.Target,
				Key:         first.Target.String(),
				MarkedFirst: true,
			}, true, nil
		}
	}

	rng := opt.Rand
	if rng == nil {
		rng = NewSeededRng(0)
	}
	total, err := sumEligibleWeights(eligible)
	if err != nil {
		return AttemptCandidate{}, false, err
	}
	n := int(total)
	roll := rng.Intn(n)
	var sum int64
	var chosen WeightedBranch
	for _, b := range eligible {
		sum += effectiveWeight(b.Weight)
		if int64(roll) < sum {
			chosen = b
			break
		}
	}
	return AttemptCandidate{
		Primary:     chosen.Target,
		Key:         chosen.Target.String(),
		MarkedFirst: chosen.IsFirst && !opt.IsRetryPath,
	}, false, nil
}

// branchKey is the per-branch stable key used in cycle entries and selector keys.
// A parallel-target branch is keyed as "parallel:" plus its legs joined by '!'.
func branchKey(b WeightedBranch) string {
	if b.Parallel != nil {
		legs := make([]string, 0, len(b.Parallel.Branches))
		for _, leg := range b.Parallel.Branches {
			legs = append(legs, leg.Target.String())
		}
		return "parallel:" + strings.Join(legs, "!")
	}
	return b.Target.String()
}

// buildThinkerCycle builds the cycle entries for a thinker-aware weighted selector by repeating
// non-thinker branches by effective weight and appending the thinker branch once, plus the
// selector key formed by joining branch keys in AST order.
func buildThinkerCycle(w *Weighted) ([]interleavedstate.CycleEntry, string) {
	keys := make([]string, 0, len(w.Branches))
	for _, b := range w.Branches {
		keys = append(keys, branchKey(b))
	}
	selKey := strings.Join(keys, "^")
	entries := make([]interleavedstate.CycleEntry, 0, len(w.Branches)+2)
	for _, b := range w.Branches {
		if b.IsThinker {
			continue
		}
		wt := thinkerCycleWeight(b.Weight)
		key := branchKey(b)
		for range wt {
			entries = append(entries, interleavedstate.CycleEntry{Key: key, Role: interleavedstate.RoleExecutor})
		}
	}
	for _, b := range w.Branches {
		if !b.IsThinker {
			continue
		}
		entries = append(entries, interleavedstate.CycleEntry{Key: branchKey(b), Role: interleavedstate.RoleThinker})
		break
	}
	return entries, selKey
}

// pickThinkerCycle advances the thinker-aware weighted cycle cursor and returns the candidates
// at the current position. It honors first-request steering before cycle advancement when no
// valid cycle state exists, suppresses the thinker position on continuation turns, and returns
// ErrNoEligibleCandidate when suppression or exclusions leave no executor candidate.
func pickThinkerCycle(w *Weighted, opt PlanOptions) ([]AttemptCandidate, bool, *interleavedstate.CycleState, error) {
	entries, selKey := buildThinkerCycle(w)
	stateValid := !opt.ThinkerCycle.IsEmpty() &&
		opt.ThinkerCycle.SelectorKey == selKey &&
		cycleSequenceEqual(opt.ThinkerCycle.Sequence, entries)

	// First-request steering before cycle advancement while [first] is still unconsumed.
	if !opt.IsRetryPath && opt.Session != nil && !opt.Session.FirstRequestConsumed {
		for _, b := range w.Branches {
			if !b.IsFirst {
				continue
			}
			key := b.Target.String()
			if !candidateEligible(b.Target, key, opt) {
				continue
			}
			c := AttemptCandidate{
				Primary:         b.Target,
				Key:             key,
				MarkedFirst:     true,
				InterleavedRole: interleavedstate.RoleExecutor,
				SelectorKey:     selKey,
			}
			next := &interleavedstate.CycleState{SelectorKey: selKey, Sequence: entries, NextIndex: 0}
			return []AttemptCandidate{c}, true, next, nil
		}
	}

	var state interleavedstate.CycleState
	if stateValid {
		state = opt.ThinkerCycle
		if state.NextIndex < 0 || state.NextIndex >= len(entries) {
			state.NextIndex = 0
		}
	} else {
		state = interleavedstate.CycleState{SelectorKey: selKey, Sequence: entries, NextIndex: 0}
	}

	n := len(entries)
	for i := range n {
		idx := (state.NextIndex + i) % n
		entry := entries[idx]
		if entry.Role == interleavedstate.RoleThinker && opt.SuppressThinker {
			continue
		}
		cands, ok := cycleCandidates(w, entry, selKey, opt)
		if !ok {
			continue
		}
		next := state
		next.NextIndex = (idx + 1) % n
		return cands, false, &next, nil
	}
	return nil, false, nil, ErrNoEligibleCandidate
}

// cycleCandidates resolves a cycle entry into concrete attempt candidates, applying exclusion,
// health, and request-size eligibility. Parallel executor entries expand to all eligible legs.
func cycleCandidates(w *Weighted, entry interleavedstate.CycleEntry, selKey string, opt PlanOptions) ([]AttemptCandidate, bool) {
	for _, b := range w.Branches {
		if branchKey(b) != entry.Key {
			continue
		}
		if b.Parallel != nil {
			legs := expandParallel(b.Parallel, opt)
			if len(legs) == 0 {
				return nil, false
			}
			for i := range legs {
				legs[i].InterleavedRole = entry.Role
				legs[i].SelectorKey = selKey
			}
			return legs, true
		}
		if !candidateEligible(b.Target, entry.Key, opt) {
			return nil, false
		}
		return []AttemptCandidate{{
			Primary:         b.Target,
			Key:             entry.Key,
			InterleavedRole: entry.Role,
			SelectorKey:     selKey,
		}}, true
	}
	return nil, false
}
