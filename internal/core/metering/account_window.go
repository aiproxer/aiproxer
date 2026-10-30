package metering

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/metering/replay"
	aipsdkmetering "github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

// AccountWindowQuery identifies a provider allowance-window query. The
// provider account is mandatory: an account-window query without that bound
// would scan unrelated supplier accounts. Pool, window and reset are optional
// refinements; AsOf is an effective observed-at bound, not a receipt-time
// bound.
type AccountWindowQuery struct {
	StoreID            string
	TenantID           string
	ProviderAccountKey string
	PoolID             string
	WindowID           string
	ResetAt            *time.Time
	AsOf               time.Time
	Limit              int
	Cursor             string
}

// AccountWindowObservationPage is a bounded immutable history page.
type AccountWindowObservationPage struct {
	Observations []aipsdkmetering.Observation
	NextCursor   string
}

// AccountWindowProjection is a deterministic current or as-of view for one
// complete provider account/pool/window/reset identity. Measures are merged
// by component key as present-field gauges: an omitted or unavailable field
// never erases a previously known field. ObservationRefs retain the immutable
// source revisions used by this view. No measure is additive and no monetary
// charge can be represented by this projection.
type AccountWindowProjection struct {
	Subject         aipsdkmetering.SubjectRef
	Measures        []aipsdkmetering.Measure
	ObservedAt      time.Time
	ReceivedAt      time.Time
	ObservationRefs []aipsdkmetering.ObservationRef
}

// IdentityKey returns the complete reset-scoped subject identity used for
// deterministic projection ordering and pagination. It intentionally excludes
// request/call lineage: any such association is informational on an
// account-window observation, never a separate gauge subject.
func (p AccountWindowProjection) IdentityKey() string {
	return accountWindowIdentityForSubject(p.Subject)
}

// AccountWindowProjectionPage contains one projection per matching reset
// epoch. Distinct pools, windows and reset epochs are never combined.
type AccountWindowProjectionPage struct {
	Projections []AccountWindowProjection
	NextCursor  string
}

// AccountWindowStore is the domain query contract implemented by the durable
// journal adapter. The contract carries only context and immutable DTOs; SQL
// transaction types remain in infrastructure.
type AccountWindowStore interface {
	ListAccountWindowObservations(context.Context, AccountWindowQuery) (AccountWindowObservationPage, error)
	ProjectAccountWindows(context.Context, AccountWindowQuery) (AccountWindowProjectionPage, error)
}

// ProjectAccountWindows reduces account-window gauge observations into
// deterministic reset-scoped projections. It accepts observations in any
// arrival order and removes exact source-event replays before reducing. A
// conflicting payload for one immutable identity is rejected by the shared
// replay boundary.
func ProjectAccountWindows(observations []aipsdkmetering.Observation, asOf time.Time) ([]AccountWindowProjection, error) {
	deduplicated, err := replay.Deduplicate(observations)
	if err != nil {
		return nil, err
	}
	ordered := append([]aipsdkmetering.Observation(nil), deduplicated.Observations...)
	sort.SliceStable(ordered, func(i, j int) bool {
		return accountWindowObservationLess(ordered[i], ordered[j])
	})

	states := make(map[string]*accountWindowProjectionState)
	for _, observation := range ordered {
		if observation.Subject.Kind != aipsdkmetering.SubjectAccountWindow {
			return nil, fmt.Errorf("metering: account-window projection received subject kind %q", observation.Subject.Kind)
		}
		if !asOf.IsZero() && observation.ObservedAt.After(asOf) {
			continue
		}
		key := accountWindowIdentity(observation)
		state := states[key]
		if state == nil {
			subject := observation.Subject.Clone()
			if subject.TenantID == "" {
				subject.TenantID = strings.TrimSpace(observation.Correlation.TenantID)
			}
			state = &accountWindowProjectionState{
				subject:  subject,
				measures: make(map[string]aipsdkmetering.Measure),
			}
			states[key] = state
		}
		if observation.ObservedAt.After(state.observedAt) || state.observedAt.IsZero() ||
			(observation.ObservedAt.Equal(state.observedAt) && accountWindowObservationLess(state.head, observation)) {
			state.observedAt = observation.ObservedAt
			state.receivedAt = observation.ReceivedAt
			state.head = observation
		}
		ref, err := observation.Ref(observation.Subject.StoreID)
		if err != nil {
			return nil, fmt.Errorf("metering: account-window observation %q reference: %w", observation.ID, err)
		}
		state.refs = append(state.refs, ref)
		for _, measure := range observation.Measures {
			key := measure.Key.CanonicalKey()
			if key == "" {
				return nil, fmt.Errorf("metering: account-window observation %q has empty component key", observation.ID)
			}
			// Gauge absence is not zero. Preserve an explicit unavailable/unknown
			// field only when no usable value exists yet; never let it erase a
			// value from an earlier present-field snapshot.
			if measure.Value == nil || measure.Quality == aipsdkmetering.QualityUnavailable || measure.Quality == aipsdkmetering.QualityNotApplicable {
				if _, exists := state.measures[key]; !exists {
					state.measures[key] = measure.Clone()
				}
				continue
			}
			state.measures[key] = measure.Clone()
		}
	}

	keys := make([]string, 0, len(states))
	for key := range states {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]AccountWindowProjection, 0, len(keys))
	for _, key := range keys {
		state := states[key]
		projection := AccountWindowProjection{
			Subject:         state.subject.Clone(),
			ObservedAt:      state.observedAt,
			ReceivedAt:      state.receivedAt,
			ObservationRefs: append([]aipsdkmetering.ObservationRef(nil), state.refs...),
			Measures:        make([]aipsdkmetering.Measure, 0, len(state.measures)),
		}
		measureKeys := make([]string, 0, len(state.measures))
		for measureKey := range state.measures {
			measureKeys = append(measureKeys, measureKey)
		}
		sort.Strings(measureKeys)
		for _, measureKey := range measureKeys {
			projection.Measures = append(projection.Measures, state.measures[measureKey].Clone())
		}
		result = append(result, projection)
	}
	return result, nil
}

type accountWindowProjectionState struct {
	subject    aipsdkmetering.SubjectRef
	measures   map[string]aipsdkmetering.Measure
	refs       []aipsdkmetering.ObservationRef
	observedAt time.Time
	receivedAt time.Time
	head       aipsdkmetering.Observation
}

func accountWindowIdentity(observation aipsdkmetering.Observation) string {
	subject := observation.Subject
	tenant := strings.TrimSpace(subject.TenantID)
	if tenant == "" {
		tenant = strings.TrimSpace(observation.Correlation.TenantID)
	}
	return accountWindowIdentityForSubjectWithTenant(subject, tenant)
}

func accountWindowIdentityForSubject(subject aipsdkmetering.SubjectRef) string {
	return accountWindowIdentityForSubjectWithTenant(subject, strings.TrimSpace(subject.TenantID))
}

func accountWindowIdentityForSubjectWithTenant(subject aipsdkmetering.SubjectRef, tenant string) string {
	return accountWindowLengthPrefixed(
		subject.StoreID,
		tenant,
		subject.ProviderAccountKey,
		subject.PoolID,
		subject.WindowID,
		strconv.FormatInt(subject.ResetAt.UTC().UnixNano(), 10),
	)
}

func accountWindowLengthPrefixed(values ...string) string {
	var builder strings.Builder
	for _, value := range values {
		builder.WriteString(strconv.Itoa(len(value)))
		builder.WriteByte(':')
		builder.WriteString(value)
	}
	return builder.String()
}

func accountWindowObservationLess(a, b aipsdkmetering.Observation) bool {
	if a.ObservedAt.Before(b.ObservedAt) {
		return true
	}
	if b.ObservedAt.Before(a.ObservedAt) {
		return false
	}
	if a.ReceivedAt.Before(b.ReceivedAt) {
		return true
	}
	if b.ReceivedAt.Before(a.ReceivedAt) {
		return false
	}
	if a.StreamID != b.StreamID {
		return a.StreamID < b.StreamID
	}
	if a.Sequence != b.Sequence {
		return a.Sequence < b.Sequence
	}
	if a.SourceEventKey != b.SourceEventKey {
		return a.SourceEventKey < b.SourceEventKey
	}
	if a.ID != b.ID {
		return a.ID < b.ID
	}
	return a.Revision < b.Revision
}
