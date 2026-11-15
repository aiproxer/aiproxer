package runtime

import (
	"context"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/authoritycoord"
	terminalworkapp "github.com/aiproxer/aiproxer/internal/core/terminalwork/app"
)

// TestLeaseCleanupTimeoutPrecedence pins the single cleanup budget used by
// lease-set release intents, uncertain marking, and heartbeat renewal:
// request-bound coordinator override, then the executor's resolved operator
// setting, then the existing two-second default.
func TestLeaseCleanupTimeoutPrecedence(t *testing.T) {
	t.Parallel()

	const (
		boundOverride = 7 * time.Second
		executorValue = 11 * time.Second
	)

	requestBound := &authoritycoord.RequestCoordinator{CleanupTimeout: boundOverride}
	currentGen := &authoritycoord.RequestCoordinator{CleanupTimeout: 3 * time.Second}

	cases := []struct {
		name string
		ex   *Executor
		st   *requestAuthorityState
		want time.Duration
	}{
		{
			name: "request-bound coordinator wins over a different current-generation coordinator",
			ex: &Executor{AccountingRuntime: AccountingRuntime{
				RequestCoordinator:           currentGen,
				UsageAuthorityCleanupTimeout: executorValue,
			}},
			st:   &requestAuthorityState{Coordinator: requestBound},
			want: boundOverride,
		},
		{
			name: "executor resolved setting wins when the coordinator has no override",
			ex: &Executor{AccountingRuntime: AccountingRuntime{
				RequestCoordinator:           &authoritycoord.RequestCoordinator{},
				UsageAuthorityCleanupTimeout: executorValue,
			}},
			st:   &requestAuthorityState{},
			want: executorValue,
		},
		{
			name: "two-second default when nothing is configured",
			ex:   &Executor{},
			st:   &requestAuthorityState{},
			want: 2 * time.Second,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.ex.leaseCleanupTimeout(tc.st); got != tc.want {
				t.Fatalf("leaseCleanupTimeout=%v want %v", got, tc.want)
			}
		})
	}
}

// TestAcceptLeaseSetReleaseIntentUsesResolvedCleanupTimeout proves the durable
// release intent is bounded by the executor's resolved operator setting rather
// than the hardcoded two-second budget, without detaching cancellation.
func TestAcceptLeaseSetReleaseIntentUsesResolvedCleanupTimeout(t *testing.T) {
	t.Parallel()

	const resolved = 8 * time.Second
	conc := &remediatingSetConc{}
	store := &capturingIntentStore{}
	intents := terminalworkapp.NewIntentService(store, terminalworkapp.IntentServiceConfig{})
	ex := &Executor{AccountingRuntime: AccountingRuntime{
		RequestCoordinator:           &authoritycoord.RequestCoordinator{Concurrency: conc},
		TerminalWork:                 intents,
		UsageAuthorityCleanupTimeout: resolved,
	}}
	st := &requestAuthorityState{
		RequestID:   "req-intent-timeout",
		AttemptID:   "a1",
		TraceID:     "t1",
		LeaseSetID:  "set-intent",
		Coordinator: &authoritycoord.RequestCoordinator{Concurrency: conc},
	}

	parent, cancel := context.WithCancel(context.Background())
	cancel()

	before := time.Now()
	if err := ex.acceptLeaseSetReleaseIntent(parent, st, "test_reason"); err != nil {
		t.Fatalf("acceptLeaseSetReleaseIntent: %v", err)
	}
	if len(store.appended) != 1 {
		t.Fatalf("appended=%d want 1", len(store.appended))
	}
	if store.cancelErr != nil {
		t.Fatalf("release intent context must be uncanceled, got %v", store.cancelErr)
	}
	if !store.hasDeadline {
		t.Fatal("release intent context carries no deadline")
	}
	if earliest := before.Add(resolved - time.Second); store.deadline.Before(earliest) {
		t.Fatalf("deadline %v earlier than %v", store.deadline, earliest)
	}
	if latest := time.Now().Add(resolved + time.Second); store.deadline.After(latest) {
		t.Fatalf("deadline %v later than %v", store.deadline, latest)
	}
}

// deadlineRecordingSetConc extends the remediation spy with the context
// observations MarkLeaseSetUncertain receives.
type deadlineRecordingSetConc struct {
	remediatingSetConc
	markDeadline  time.Time
	markHasDL     bool
	markCancelErr error
	markSetID     string
}

func (s *deadlineRecordingSetConc) MarkLeaseSetUncertain(ctx context.Context, setID string) error {
	s.markDeadline, s.markHasDL = ctx.Deadline()
	s.markCancelErr = ctx.Err()
	s.markSetID = setID
	return s.remediatingSetConc.MarkLeaseSetUncertain(ctx, setID)
}

// TestMarkLeaseSetUncertainUsesResolvedCleanupTimeout proves uncertain marking
// gets the executor's resolved cleanup budget, not a hardcoded two seconds, and
// that a canceled caller context never cancels the marking work itself.
func TestMarkLeaseSetUncertainUsesResolvedCleanupTimeout(t *testing.T) {
	t.Parallel()

	const resolved = 9 * time.Second
	conc := &deadlineRecordingSetConc{}
	ex := &Executor{AccountingRuntime: AccountingRuntime{
		RequestCoordinator:           &authoritycoord.RequestCoordinator{Concurrency: conc},
		UsageAuthorityCleanupTimeout: resolved,
	}}
	st := &requestAuthorityState{
		LeaseSetID:  "set-cleanup",
		Coordinator: &authoritycoord.RequestCoordinator{Concurrency: conc},
	}

	parent, cancel := context.WithCancel(context.Background())
	cancel()

	before := time.Now()
	if err := ex.markLeaseSetUncertain(parent, st); err != nil {
		t.Fatalf("markLeaseSetUncertain: %v", err)
	}
	if conc.uncertain.Load() < 1 {
		t.Fatal("MarkLeaseSetUncertain was not called")
	}
	if conc.markSetID != "set-cleanup" {
		t.Fatalf("set id = %q", conc.markSetID)
	}
	if conc.markCancelErr != nil {
		t.Fatalf("marking context must be uncanceled, got %v", conc.markCancelErr)
	}
	if !conc.markHasDL {
		t.Fatal("marking context carries no deadline")
	}
	if earliest := before.Add(resolved - time.Second); conc.markDeadline.Before(earliest) {
		t.Fatalf("deadline %v earlier than %v", conc.markDeadline, earliest)
	}
	if latest := time.Now().Add(resolved + time.Second); conc.markDeadline.After(latest) {
		t.Fatalf("deadline %v later than %v", conc.markDeadline, latest)
	}
}
