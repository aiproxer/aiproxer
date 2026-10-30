// Package replay provides the source-event replay boundary shared by metering
// reducers. It validates and deduplicates exact observation revisions before
// any graph or financial projection code sees them.
package replay

import (
	"errors"
	"fmt"
	"sort"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

// ErrIdentityConflict means one source-event revision was delivered with
// more than one semantic payload.
var ErrIdentityConflict = errors.New("metering/replay: source event identity conflict")

// IdentityConflict identifies the source event whose immutable revision was
// delivered with conflicting payloads.
type IdentityConflict struct {
	Identity     string
	ExistingHash string
	IncomingHash string
}

func (e *IdentityConflict) Error() string {
	if e == nil {
		return ErrIdentityConflict.Error()
	}
	return fmt.Sprintf("%v: %s (%s vs %s)", ErrIdentityConflict, e.Identity, e.ExistingHash, e.IncomingHash)
}

func (e *IdentityConflict) Unwrap() error { return ErrIdentityConflict }

// Result is the canonical, exact-replay-reduced input for a domain reducer.
// Observations contains one copy per source-event identity. Replayed counts
// exact duplicate deliveries skipped from the batch.
type Result struct {
	Observations []metering.Observation
	Replayed     int
}

// Deduplicate validates each observation and removes exact replay deliveries.
// The identity is source-event plus revision; quantity equality alone never
// deduplicates an event. Receipt time is deliberately excluded from payload
// equality because it is transport/store metadata, not source evidence.
func Deduplicate(observations []metering.Observation) (Result, error) {
	entries := make(map[string]entry, len(observations))
	out := make([]metering.Observation, 0, len(observations))
	result := Result{}
	for i, original := range observations {
		canonical, err := original.Canonical()
		if err != nil {
			return Result{}, fmt.Errorf("metering/replay: observation[%d]: %w", i, err)
		}
		identity := canonical.IdentityKey()
		hash, err := payloadFingerprint(canonical)
		if err != nil {
			return Result{}, fmt.Errorf("metering/replay: observation[%d] payload: %w", i, err)
		}
		if previous, exists := entries[identity]; exists {
			if previous.hash != hash {
				return Result{}, &IdentityConflict{Identity: identity, ExistingHash: previous.hash, IncomingHash: hash}
			}
			result.Replayed++
			continue
		}
		entries[identity] = entry{hash: hash}
		out = append(out, canonical)
	}
	result.Observations = out
	return result, nil
}

// Sort orders replay survivors by declared stream sequence and revision. It
// is stable for additive events and uses source identity only as a final
// deterministic tie-breaker; reducers still reject ambiguous cumulative ties.
func Sort(observations []metering.Observation) []metering.Observation {
	out := append([]metering.Observation(nil), observations...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Sequence != out[j].Sequence {
			return out[i].Sequence < out[j].Sequence
		}
		if out[i].Revision != out[j].Revision {
			return out[i].Revision < out[j].Revision
		}
		if out[i].SourceEventKey != out[j].SourceEventKey {
			return out[i].SourceEventKey < out[j].SourceEventKey
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// PayloadFingerprint returns the canonical semantic payload hash used for
// replay comparison. ReceivedAt is replaced by ObservedAt because a late
// transport receipt must not turn the same source event into a conflict.
func PayloadFingerprint(observation metering.Observation) (string, error) {
	canonical, err := observation.Canonical()
	if err != nil {
		return "", err
	}
	return payloadFingerprint(canonical)
}

type entry struct{ hash string }

func payloadFingerprint(observation metering.Observation) (string, error) {
	return observation.ReplayFingerprint()
}
