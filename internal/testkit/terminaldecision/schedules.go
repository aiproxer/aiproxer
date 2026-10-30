// Package terminaldecision contains deterministic coordination fixtures for
// terminal-decision platform tests. Fixtures only expose barriers; callers
// advance them and own any goroutines used by the test.
package terminaldecision

import (
	"fmt"
	"sync"
)

// EventKind identifies one bounded outcome category recorded by a fixture.
type EventKind uint8

const (
	TerminalEvent EventKind = iota
	SettlementEvent
	CleanupEvent
	PolicyEvent
	StepEvent
)

const (
	TerminalAllowStop = "allow_stop"
	TerminalCancelled = "cancelled"
	TerminalContinued = "continued"
	TerminalClosed    = "closed"
	TerminalDrained   = "drained"

	SettlementB1            = "b1_settled"
	SettlementB1AfterB2     = "b1_settled_after_b2"
	SettlementB1Unsettled   = "b1_unsettled"
	SettlementDrained       = "request_drained"
	SettlementNotApplicable = "not_applicable"

	CleanupNone               = "none"
	CleanupCancelled          = "cancelled"
	CleanupOverlayDeactivated = "overlay_deactivated"
	CleanupGenerationClosed   = "generation_closed"

	PolicySnapshotUnchanged = "snapshot_unchanged"
	PolicyOldSnapshot       = "old_snapshot"
	PolicyNewSnapshot       = "new_snapshot"

	StepB1Settlement = "b1_settlement"
)

// Event is one terminal, settlement, cleanup, or policy result.
type Event struct {
	Kind  EventKind
	Value string
}

// Outcome is the expected one-event result of a named schedule.
type Outcome struct {
	Terminal   string
	Settlement string
	Cleanup    string
	Policy     string
}

// Recorder rejects duplicate outcome categories, making exactly-once checks
// deterministic without polling or timeouts.
type Recorder struct {
	mu     sync.Mutex
	events []Event
}

// NewRecorder returns an empty event recorder.
func NewRecorder() *Recorder { return &Recorder{} }

// Record appends one outcome, rejecting empty values and duplicate categories.
func (r *Recorder) Record(kind EventKind, value string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if value == "" {
		return fmt.Errorf("empty event value for kind %d", kind)
	}
	if kind > StepEvent {
		return fmt.Errorf("unknown event kind %d", kind)
	}
	if kind != StepEvent {
		for _, event := range r.events {
			if event.Kind == kind {
				return fmt.Errorf("duplicate event kind %d", kind)
			}
		}
	}
	r.events = append(r.events, Event{Kind: kind, Value: value})
	return nil
}

// Events returns a copy in recording order so callers can assert
// linearization, such as publication before settlement.
func (r *Recorder) Events() []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Event(nil), r.events...)
}

// Outcome returns the four recorded categories. Missing categories are an
// error because every schedule declares one explicit result for each class.
func (r *Recorder) Outcome() (Outcome, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var outcome Outcome
	for _, event := range r.events {
		switch event.Kind {
		case TerminalEvent:
			outcome.Terminal = event.Value
		case SettlementEvent:
			outcome.Settlement = event.Value
		case CleanupEvent:
			outcome.Cleanup = event.Value
		case PolicyEvent:
			outcome.Policy = event.Value
		}
	}
	if outcome.Terminal == "" || outcome.Settlement == "" || outcome.Cleanup == "" || outcome.Policy == "" {
		return Outcome{}, fmt.Errorf("incomplete outcome: %+v", outcome)
	}
	return outcome, nil
}

// Matches compares recorded results with the schedule's expected outcome.
func (r *Recorder) Matches(want Outcome) error {
	got, err := r.Outcome()
	if err != nil {
		return err
	}
	if got != want {
		return fmt.Errorf("outcome mismatch: want %+v, got %+v", want, got)
	}
	return nil
}

// Barrier is a caller-driven two-phase gate. Arrive and Release are
// idempotent, so a replayed schedule step cannot panic by closing a channel.
type Barrier struct {
	arrived  chan struct{}
	released chan struct{}
	arrive   sync.Once
	release  sync.Once
}

// NewBarrier returns a closed-only coordination gate.
func NewBarrier() *Barrier {
	return &Barrier{arrived: make(chan struct{}), released: make(chan struct{})}
}

// Arrive publishes that the guarded operation reached the gate.
func (b *Barrier) Arrive() { b.arrive.Do(func() { close(b.arrived) }) }

// Arrived is closed after Arrive is called.
func (b *Barrier) Arrived() <-chan struct{} { return b.arrived }

// Release permits the guarded operation to continue.
func (b *Barrier) Release() { b.release.Do(func() { close(b.released) }) }

// Released is closed after Release is called.
func (b *Barrier) Released() <-chan struct{} { return b.released }

// Fixture is shared bookkeeping for named schedules, not a scheduler.
type Fixture struct {
	Name           string
	Recorder       *Recorder
	Expected       Outcome
	ExpectedEvents []Event
}

func newFixture(name string, expected Outcome, events ...Event) Fixture {
	return Fixture{
		Name:           name,
		Recorder:       NewRecorder(),
		Expected:       expected,
		ExpectedEvents: append([]Event(nil), events...),
	}
}

func scheduleEvents(outcome Outcome, steps ...string) []Event {
	events := make([]Event, 0, len(steps)+4)
	for _, step := range steps {
		events = append(events, Event{StepEvent, step})
	}
	return append(events,
		Event{TerminalEvent, outcome.Terminal}, Event{SettlementEvent, outcome.Settlement},
		Event{CleanupEvent, outcome.Cleanup}, Event{PolicyEvent, outcome.Policy})
}

func publishedEvents(outcome Outcome) []Event {
	events := []Event{{StepEvent, "b2_admission"}, {StepEvent, "b2_publication"}}
	events = append(events, Event{SettlementEvent, outcome.Settlement}, Event{TerminalEvent, outcome.Terminal})
	return append(events, Event{CleanupEvent, outcome.Cleanup}, Event{PolicyEvent, outcome.Policy})
}

// Record records an outcome on the fixture.
func (f *Fixture) Record(kind EventKind, value string) error {
	return f.Recorder.Record(kind, value)
}

// Verify checks the fixture's expected outcome and exact event order.
func (f *Fixture) Verify() error {
	if err := f.Recorder.Matches(f.Expected); err != nil {
		return err
	}
	got := f.Recorder.Events()
	if len(got) != len(f.ExpectedEvents) {
		return fmt.Errorf("event count mismatch: want %d, got %d", len(f.ExpectedEvents), len(got))
	}
	for i := range got {
		if got[i] != f.ExpectedEvents[i] {
			return fmt.Errorf("event %d mismatch: want %+v, got %+v", i, f.ExpectedEvents[i], got[i])
		}
	}
	return nil
}

// ProviderFailureKind identifies a failure at the provider boundary.
type ProviderFailureKind string

const (
	ProviderTimeout   ProviderFailureKind = "timeout"
	ProviderError     ProviderFailureKind = "error"
	ProviderPanic     ProviderFailureKind = "panic"
	ProviderMalformed ProviderFailureKind = "malformed"
)

// ProviderFailureSchedule gates provider timeout, error, panic, and malformed
// result paths. All normalize to one safe terminal and no continuation.
type ProviderFailureSchedule struct {
	Fixture
	Kind     ProviderFailureKind
	Provider *Barrier
}

// ProviderFailureSchedules returns one independent schedule per boundary
// failure mode.
func ProviderFailureSchedules() []ProviderFailureSchedule {
	want := Outcome{TerminalAllowStop, SettlementB1, CleanupNone, PolicySnapshotUnchanged}
	return []ProviderFailureSchedule{
		{Fixture: newFixture("provider-timeout", want, scheduleEvents(want, "provider_failure")...), Kind: ProviderTimeout, Provider: NewBarrier()},
		{Fixture: newFixture("provider-error", want, scheduleEvents(want, "provider_failure")...), Kind: ProviderError, Provider: NewBarrier()},
		{Fixture: newFixture("provider-panic", want, scheduleEvents(want, "provider_failure")...), Kind: ProviderPanic, Provider: NewBarrier()},
		{Fixture: newFixture("provider-malformed", want, scheduleEvents(want, "provider_failure")...), Kind: ProviderMalformed, Provider: NewBarrier()},
	}
}

// CancellationSchedule exposes the provider, cancellation, and continuation
// gates for a cancellation-wins race.
type CancellationSchedule struct {
	Fixture
	Provider, Cancel, Continuation *Barrier
}

// CancellationRaceSchedule returns the cancellation-wins schedule.
func CancellationRaceSchedule() CancellationSchedule {
	want := Outcome{TerminalCancelled, SettlementB1, CleanupCancelled, PolicySnapshotUnchanged}
	return CancellationSchedule{Fixture: newFixture("cancellation-wins", want, scheduleEvents(want, "cancellation")...), Provider: NewBarrier(), Cancel: NewBarrier(), Continuation: NewBarrier()}
}

// ContinuationSchedule exposes B2 admission/publication and B1 settlement
// gates for both pre-publication and post-publication failure paths.
type ContinuationSchedule struct {
	Fixture
	B2Admission, B2Publication, B1Settlement *Barrier
}

// B2AdmissionFailureSchedule models failure before B2 publication.
func B2AdmissionFailureSchedule() ContinuationSchedule {
	want := Outcome{TerminalAllowStop, SettlementB1Unsettled, CleanupOverlayDeactivated, PolicySnapshotUnchanged}
	return ContinuationSchedule{Fixture: newFixture("b2-admission-failure", want, scheduleEvents(want, "b2_admission")...), B2Admission: NewBarrier(), B2Publication: NewBarrier(), B1Settlement: NewBarrier()}
}

// B2PublishedSettlementSchedule models B2 becoming current before B1
// settlement reports success or loss.
func B2PublishedSettlementSchedule() ContinuationSchedule {
	want := Outcome{TerminalContinued, SettlementB1AfterB2, CleanupOverlayDeactivated, PolicySnapshotUnchanged}
	return ContinuationSchedule{Fixture: newFixture("b2-published-before-b1-settlement", want, publishedEvents(want)...), B2Admission: NewBarrier(), B2Publication: NewBarrier(), B1Settlement: NewBarrier()}
}

// ContinuationSchedules returns both B1/B2 ordering schedules.
func ContinuationSchedules() []ContinuationSchedule {
	return []ContinuationSchedule{B2AdmissionFailureSchedule(), B2PublishedSettlementSchedule()}
}

// WithdrawalSchedule exposes pin, withdrawal, drain, and close barriers.
type WithdrawalSchedule struct {
	Fixture
	PinnedRequest, Withdrawal, Drain, Close *Barrier
}

// GenerationWithdrawalSchedule models an admitted request retaining its
// immutable generation while new admission is withdrawn.
func GenerationWithdrawalSchedule() WithdrawalSchedule {
	want := Outcome{TerminalDrained, SettlementDrained, CleanupGenerationClosed, PolicyOldSnapshot}
	return WithdrawalSchedule{Fixture: newFixture("generation-withdrawal", want, scheduleEvents(want, "request_pinned", "generation_withdrawal", "request_drained", "generation_closed")...), PinnedRequest: NewBarrier(), Withdrawal: NewBarrier(), Drain: NewBarrier(), Close: NewBarrier()}
}

// PolicyRaceSchedule exposes the snapshot/write ordering gates.
type PolicyRaceSchedule struct {
	Fixture
	Snapshot, Write, SnapshotComplete *Barrier
}

// PolicyWriteBeforeSnapshotSchedule models a request observing the new
// complete state.
func PolicyWriteBeforeSnapshotSchedule() PolicyRaceSchedule {
	want := Outcome{TerminalClosed, SettlementB1, CleanupNone, PolicyNewSnapshot}
	return PolicyRaceSchedule{Fixture: newFixture("policy-write-before-snapshot", want, scheduleEvents(want, "policy_write", "policy_snapshot", "snapshot_complete")...), Snapshot: NewBarrier(), Write: NewBarrier(), SnapshotComplete: NewBarrier()}
}

// PolicySnapshotBeforeWriteSchedule models a request retaining the old
// complete state while a later write applies to the next request.
func PolicySnapshotBeforeWriteSchedule() PolicyRaceSchedule {
	want := Outcome{TerminalClosed, SettlementB1, CleanupNone, PolicyOldSnapshot}
	return PolicyRaceSchedule{Fixture: newFixture("policy-snapshot-before-write", want, scheduleEvents(want, "policy_snapshot", "snapshot_complete", "policy_write")...), Snapshot: NewBarrier(), Write: NewBarrier(), SnapshotComplete: NewBarrier()}
}

// PolicySchedules returns both deterministic linearization orders.
func PolicySchedules() []PolicyRaceSchedule {
	return []PolicyRaceSchedule{PolicyWriteBeforeSnapshotSchedule(), PolicySnapshotBeforeWriteSchedule()}
}

// OverlayCleanupSchedule exposes external ingress and fixed-overlay cleanup
// gates for stale state after a crash or restart.
type OverlayCleanupSchedule struct {
	Fixture
	ExternalIngress, OverlayCleanup *Barrier
}

// StaleOverlayCleanupSchedule models idempotent stale overlay deactivation.
func StaleOverlayCleanupSchedule() OverlayCleanupSchedule {
	want := Outcome{TerminalClosed, SettlementNotApplicable, CleanupOverlayDeactivated, PolicySnapshotUnchanged}
	return OverlayCleanupSchedule{Fixture: newFixture("stale-overlay-cleanup", want, scheduleEvents(want, "external_ingress", "overlay_cleanup")...), ExternalIngress: NewBarrier(), OverlayCleanup: NewBarrier()}
}
