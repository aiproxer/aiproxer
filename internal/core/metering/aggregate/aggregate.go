// Package aggregate applies metering fact kinds onto a stream aggregate without
// mutating journal history (requirements 3.2, 3.3, 3.5, 13.6).
package aggregate

import (
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

// Snapshot is the idempotent result of replaying an ordered fact stream.
type Snapshot struct {
	StreamID      string
	Quantities    map[string]int64 // component -> value (present only)
	MoneyNano     int64
	MoneyCurrency string
	MoneyPresent  bool
	Unavailable   []string // fact IDs marked unavailable / unresolved
	Superseded    map[string]struct{}
	LastSequence  int64
}

// Apply replays facts in Sequence order (stable by FactID on ties). Replaying
// the same ordered set yields the same Snapshot (restart hydration).
func Apply(facts []metering.Fact) (Snapshot, error) {
	ordered := append([]metering.Fact(nil), facts...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].Sequence != ordered[j].Sequence {
			return ordered[i].Sequence < ordered[j].Sequence
		}
		return ordered[i].FactID < ordered[j].FactID
	})
	snap := Snapshot{
		Quantities: make(map[string]int64),
		Superseded: make(map[string]struct{}),
	}
	seen := make(map[string]struct{}) // FactID for idempotent skip of exact replays in input
	for _, f := range ordered {
		if err := f.Validate(); err != nil {
			return Snapshot{}, fmt.Errorf("metering/aggregate: %w", err)
		}
		if snap.StreamID == "" {
			snap.StreamID = f.StreamID
		} else if f.StreamID != snap.StreamID {
			return Snapshot{}, fmt.Errorf("metering/aggregate: mixed stream_id %q and %q", snap.StreamID, f.StreamID)
		}
		idKey := f.FactID
		if _, ok := seen[idKey]; ok {
			continue // duplicate identity in batch is a no-op
		}
		seen[idKey] = struct{}{}
		if f.Sequence > snap.LastSequence {
			snap.LastSequence = f.Sequence
		}
		switch f.Kind {
		case metering.FactKindUnavailable:
			snap.Unavailable = appendUnique(snap.Unavailable, f.FactID)
			continue
		case metering.FactKindReservationEstimate:
			// Estimates do not settle into the metering total; tracked for reconcile only.
			continue
		case metering.FactKindDelta:
			if err := applyDelta(snap.Quantities, f.Quantities, 1); err != nil {
				return Snapshot{}, err
			}
			if err := applyMoneyDelta(&snap, f.Money, 1); err != nil {
				return Snapshot{}, err
			}
		case metering.FactKindCumulative:
			replacePresent(snap.Quantities, f.Quantities)
			if err := applyMoneyReplace(&snap, f.Money); err != nil {
				return Snapshot{}, err
			}
		case metering.FactKindCorrection:
			for _, id := range f.Supersedes {
				snap.Superseded[strings.TrimSpace(id)] = struct{}{}
			}
			if err := applyDelta(snap.Quantities, f.Quantities, 1); err != nil {
				return Snapshot{}, err
			}
			if err := applyMoneyDelta(&snap, f.Money, 1); err != nil {
				return Snapshot{}, err
			}
		case metering.FactKindAuthoritativeReplacement:
			for _, id := range f.Supersedes {
				snap.Superseded[strings.TrimSpace(id)] = struct{}{}
			}
			// Replace only explicitly present components; unrelated components remain
			// (design Corrections; requirement 6.8).
			replacePresent(snap.Quantities, f.Quantities)
			if err := applyMoneyReplace(&snap, f.Money); err != nil {
				return Snapshot{}, err
			}
		default:
			return Snapshot{}, fmt.Errorf("metering/aggregate: unsupported kind %q", f.Kind)
		}
	}
	return snap, nil
}

func applyDelta(dst map[string]int64, qs []metering.Quantity, sign int64) error {
	for _, q := range qs {
		if !q.Present {
			continue
		}
		delta, err := mulInt64Checked(sign, q.Value)
		if err != nil {
			return err
		}
		sum, err := addInt64Checked(dst[q.Component], delta)
		if err != nil {
			return err
		}
		dst[q.Component] = sum
	}
	return nil
}

func replacePresent(dst map[string]int64, qs []metering.Quantity) {
	for _, q := range qs {
		if !q.Present {
			continue
		}
		dst[q.Component] = q.Value
	}
}

func applyMoneyDelta(snap *Snapshot, m *metering.MoneyObservation, sign int64) error {
	if m == nil || !m.Present {
		return nil
	}
	if err := acceptMoneyCurrency(snap, m.Currency); err != nil {
		return err
	}
	delta, err := mulInt64Checked(sign, m.NanoUnits)
	if err != nil {
		return err
	}
	sum, err := addInt64Checked(snap.MoneyNano, delta)
	if err != nil {
		return err
	}
	snap.MoneyPresent = true
	snap.MoneyNano = sum
	return nil
}

func applyMoneyReplace(snap *Snapshot, m *metering.MoneyObservation) error {
	if m == nil || !m.Present {
		return nil
	}
	if err := acceptMoneyCurrency(snap, m.Currency); err != nil {
		return err
	}
	snap.MoneyPresent = true
	snap.MoneyNano = m.NanoUnits
	return nil
}

// acceptMoneyCurrency enforces normalized currency identity for present money
// (requirement 6.7). First present money sets currency; empty currency and
// subsequent mismatches return ErrMixedCurrency.
func acceptMoneyCurrency(snap *Snapshot, currency string) error {
	cur := strings.TrimSpace(currency)
	if cur == "" {
		return ErrMixedCurrency
	}
	if snap.MoneyPresent {
		if strings.TrimSpace(snap.MoneyCurrency) != cur {
			return ErrMixedCurrency
		}
		return nil
	}
	snap.MoneyCurrency = cur
	return nil
}

func addInt64Checked(a, b int64) (int64, error) {
	if b > 0 {
		if a > math.MaxInt64-b {
			return 0, ErrOverflow
		}
	} else if b < 0 {
		if a < math.MinInt64-b {
			return 0, ErrOverflow
		}
	}
	return a + b, nil
}

func mulInt64Checked(a, b int64) (int64, error) {
	if a == 0 || b == 0 {
		return 0, nil
	}
	if a == 1 {
		return b, nil
	}
	if b == 1 {
		return a, nil
	}
	if a == -1 {
		if b == math.MinInt64 {
			return 0, ErrOverflow
		}
		return -b, nil
	}
	if b == -1 {
		if a == math.MinInt64 {
			return 0, ErrOverflow
		}
		return -a, nil
	}
	result := a * b
	if result/a != b {
		return 0, ErrOverflow
	}
	return result, nil
}

func appendUnique(in []string, v string) []string {
	if slices.Contains(in, v) {
		return in
	}
	return append(in, v)
}
