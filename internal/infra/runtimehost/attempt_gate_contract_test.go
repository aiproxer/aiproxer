package runtimehost

import (
	"context"

	sdkreload "github.com/aiproxer/aiproxer/pkg/aipsdk/configreload"
)

// Task 6.2 keeps the AttemptGate contract vocabulary as a compile-time assert
// against the single production owner. These interfaces must not back a second
// ownership state machine.

// attemptGateContract is the exclusive owner of reload admission, coalescing,
// active-attempt cancellation, shutdown rejection, idle wait, and atomic
// pending-follow-up claim (requirements 6.1, 6.5-6.9).
type attemptGateContract interface {
	// TryStart admits one attempt or returns an immutable busy/reject outcome
	// with no live lease. On admissionAdmitted, registration of busy/active
	// state and the completion lease become visible in one lock transition
	// (req 6.5, 6.6): busy is never observable without an armed completion signal.
	TryStart(ctx context.Context, trigger sdkreload.Trigger) attemptAdmissionOutcome

	// BeginShutdown atomically clears pending follow-up work, rejects future
	// TryStart admissions, and cancels any active lease context (req 6.1, 6.7).
	// Final Complete/abandon of the canceled lease still wakes idle waiters.
	BeginShutdown()

	// WaitForIdle waits on completion notifications until no attempt is active.
	// It must not poll, sleep, or use periodic timers (req 6.5). Cancellation
	// or deadline of ctx cancels only this wait — never the active lease.
	WaitForIdle(ctx context.Context) error
}

// attemptLeaseContract is one admitted attempt token. The gate owns internal
// cancellation; consumers see only the host-owned attempt context and atomic
// Complete/Abandon transitions (no separate Cancel authority).
type attemptLeaseContract interface {
	// Context is the host-owned attempt context. Trigger-caller cancel/deadline
	// after admission must not cancel this context; context values may remain
	// available. Gate/host shutdown cancels it.
	Context() context.Context

	// Complete atomically finishes or advances the active lease exactly once:
	// either final release (wake idle waiters) or claim exactly one pending
	// HUP follow-up without an idle/admission gap (req 6.7, 6.8).
	Complete() attemptFinishOutcome

	// Abandon releases this lease without claiming pending follow-up work.
	// If this lease is still the active unfinished token, it atomically marks
	// finished, discards pending HUP/coalesced work, clears active state, closes
	// the idle notification exactly once, and cancels the host-owned context
	// (req 6.7). Concurrent/sequential Abandon/Complete races are panic-free;
	// only one transition wins. After Complete or a prior Abandon, Abandon is
	// an idempotent no-op and never claims follow-up work.
	Abandon()
}

// Compile-assert the production owner satisfies the Task 6.1/6.2 contract.
var (
	_ attemptGateContract  = (*attemptGate)(nil)
	_ attemptLeaseContract = (*attemptLease)(nil)
)

// attemptGateSemanticEvidenceKind labels how a semantic row is locked.
type attemptGateSemanticEvidenceKind string

const (
	evidenceCharacterization attemptGateSemanticEvidenceKind = "characterization"
	evidenceASTRED           attemptGateSemanticEvidenceKind = "ast_red"
	evidenceTask62Contract   attemptGateSemanticEvidenceKind = "task62_contract"
)

// attemptGateContractSemantics documents exact Task 6.2 behavior. Evidence must
// be characterization, AST architecture assertion, or a Task 6.2 contract case —
// prose alone is not coverage.
var attemptGateContractSemantics = []struct {
	Name      string
	Rule      string
	Evidence  []attemptGateSemanticEvidenceKind
	CoveredBy string
}{
	{
		Name: "atomic_arm",
		Rule: "TryStart(admissionAdmitted) makes attempt registration and completion lease visible in one lock transition; busy is never observable without an armed completion signal.",
		Evidence: []attemptGateSemanticEvidenceKind{
			evidenceTask62Contract,
			evidenceASTRED,
		},
		CoveredBy: "TestAttemptGate_AtomicArmNoBusyWithoutLease;TestAttemptGate_ArchitectureOwnerAssertions",
	},
	{
		Name: "api_busy_no_queue",
		Rule: "API trigger while active returns admissionBusyAPI with canonical ResultBusy/StageBusy metadata, CoalescedSignals=0, no live lease, and does not set pending follow-up.",
		Evidence: []attemptGateSemanticEvidenceKind{
			evidenceTask62Contract,
		},
		CoveredBy: "TestAttemptGate_BusyAPIConflictNoPending",
	},
	{
		Name: "sighup_pending_once",
		Rule: "First SIGHUP while active returns admissionPendingHUP with CoalescedSignals=0 on the outcome, sets at most one pending follow-up, and issues no live lease / second attempt.",
		Evidence: []attemptGateSemanticEvidenceKind{
			evidenceTask62Contract,
		},
		CoveredBy: "TestAttemptGate_CoalesceBoundedPending",
	},
	{
		Name: "sighup_coalesce_count",
		Rule: "Additional SIGHUPs while pending already set return admissionCoalescedHUP carrying the incremented CoalescedSignals on the immutable outcome; no further queued work.",
		Evidence: []attemptGateSemanticEvidenceKind{
			evidenceTask62Contract,
		},
		CoveredBy: "TestAttemptGate_CoalesceBoundedPending",
	},
	{
		Name: "finish_claim_pending_followup",
		Rule: "Complete() either (a) finishReleasedIdle: releases the final lease and wakes idle waiters, or (b) finishFollowUpClaimed: atomically claims exactly one pending HUP, clears/resets coalesced, returns FollowUpLease+CoalescedSignals, and keeps the gate non-idle with no admission window.",
		Evidence: []attemptGateSemanticEvidenceKind{
			evidenceTask62Contract,
			evidenceASTRED,
		},
		CoveredBy: "TestAttemptGate_FinishClaimsPendingFollowUp;TestAttemptGate_ArchitectureOwnerAssertions",
	},
	{
		Name: "shutdown_atomic",
		Rule: "BeginShutdown clears pending follow-up, rejects future starts with admissionRejectedShutdown (no live lease), cancels the active lease context, and allows final Complete/abandon to wake WaitForIdle waiters.",
		Evidence: []attemptGateSemanticEvidenceKind{
			evidenceTask62Contract,
		},
		CoveredBy: "TestAttemptGate_ShutdownRejectsAndCancels;TestAttemptGate_ShutdownCancelsIdleWaiters",
	},
	{
		Name: "finish_exactly_once",
		Rule: "Complete is panic-free under concurrent duplicate calls, advances/finishes at most once (finishAlreadyCompleted on duplicates), and wakes every WaitForIdle waiter on the releasing transition.",
		Evidence: []attemptGateSemanticEvidenceKind{
			evidenceTask62Contract,
			evidenceASTRED,
		},
		CoveredBy: "TestAttemptGate_ExactFinishSequentialDuplicate;TestAttemptGate_ConcurrentDuplicateComplete;TestAttemptGate_ArchitectureOwnerAssertions",
	},
	{
		Name: "abandon_release_no_followup",
		Rule: "Abandon releases the active unfinished lease without claiming pending follow-up: discards pending/coalesced, closes idle notify once, cancels host context. Complete vs Abandon and duplicate Abandon races are panic-free with exactly one winning transition; deferred Abandon after final Complete is an idempotent no-op. Coordinator.Reload defers current-lease Abandon after admission.",
		Evidence: []attemptGateSemanticEvidenceKind{
			evidenceTask62Contract,
			evidenceASTRED,
		},
		CoveredBy: "TestAttemptGate_AbandonReleasesWithoutFollowUp;TestAttemptGate_CompleteVsAbandonRace;TestAttemptGate_DuplicateAbandonRace;TestAttemptGate_PanicCleanupAbandon;TestAttemptGate_ArchitectureOwnerAssertions",
	},
	{
		Name: "caller_cancel_detach",
		Rule: "After admission, trigger-caller cancel/deadline must not cancel or leak the host-owned active attempt/follow-up slot (context values may remain). Gate/host shutdown cancels the active lease. WaitForIdle ctx cancel/deadline cancels only that wait. Non-admitted outcomes return no live lease.",
		Evidence: []attemptGateSemanticEvidenceKind{
			evidenceTask62Contract,
			evidenceCharacterization,
			evidenceASTRED,
		},
		CoveredBy: "TestAttemptGate_CallerCancelDetachment;TestAttemptGate_CallerCancelDetachmentEvidence;TestCoordinator_HostTimeoutIndependentOfClientCancel",
	},
	{
		Name: "idle_wait_cases",
		Rule: "WaitForIdle covers already-idle, active→finish (blocked waiters awakened), concurrent waiters, canceled context, already-expired deadline (DeadlineExceeded), and shutdown+finish wake without polling.",
		Evidence: []attemptGateSemanticEvidenceKind{
			evidenceTask62Contract,
			evidenceASTRED,
		},
		CoveredBy: "TestAttemptGate_IdleAlreadyIdle;TestAttemptGate_IdleAfterActiveFinish;TestAttemptGate_IdleWaitersWake;TestAttemptGate_IdleCanceledContext;TestAttemptGate_IdleDeadlineExceeded;TestAttemptGate_ShutdownCancelsIdleWaiters;TestAttemptGate_NoPollPolicyInConcurrencySuite",
	},
	{
		Name: "interleave_one_complete_vs_wait_shutdown",
		Rule: "Race coverage exercises TryStart/Complete/WaitForIdle/BeginShutdown interleavings, including concurrent duplicate Complete under race.",
		Evidence: []attemptGateSemanticEvidenceKind{
			evidenceTask62Contract,
		},
		CoveredBy: "TestAttemptGate_InterleavingsOneCompleteVsWaitShutdown;TestAttemptGate_ConcurrentDuplicateComplete",
	},
}
