package protocolstate

import (
	"errors"
	"strings"
	"testing"
)

func TestObserveFirstBaselineHasZeroNoProgress(t *testing.T) {
	t.Parallel()

	fp := Fingerprint(fingerprintInput())
	next, err := Observe(State{}, fp)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if next.LastFingerprint != fp {
		t.Fatalf("baseline fingerprint=%q, want %q", next.LastFingerprint, fp)
	}
	if next.ConsecutiveNoProgress != 0 {
		t.Fatalf("first baseline consecutive=%d, want 0", next.ConsecutiveNoProgress)
	}
	if next.Reprompts != 0 || next.Terminal {
		t.Fatalf("first baseline changed reprompts/terminal: %+v", next)
	}
}

func TestObserveEqualFingerprintIncrementsNoProgress(t *testing.T) {
	t.Parallel()

	fp := Fingerprint(fingerprintInput())
	next, err := Observe(State{LastFingerprint: fp, Reprompts: 1}, fp)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if next.ConsecutiveNoProgress != 1 {
		t.Fatalf("consecutive=%d, want 1", next.ConsecutiveNoProgress)
	}
	if next.Reprompts != 1 {
		t.Fatalf("observation changed reprompts to %d", next.Reprompts)
	}
}

func TestObserveChangedFingerprintResetsOnlyConsecutive(t *testing.T) {
	t.Parallel()

	fp := Fingerprint(fingerprintInput())
	prior := State{Reprompts: 2, LastFingerprint: fp, ConsecutiveNoProgress: 2}
	next, err := Observe(prior, "sha256:"+repeatHex("ab", 32))
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if next.ConsecutiveNoProgress != 0 {
		t.Fatalf("progress did not reset consecutive: %+v", next)
	}
	if next.Reprompts != prior.Reprompts {
		t.Fatalf("progress changed reprompts: %d -> %d", prior.Reprompts, next.Reprompts)
	}
	if next.LastFingerprint == prior.LastFingerprint {
		t.Fatal("progress did not adopt the new baseline fingerprint")
	}
}

func TestObserveSaturatesConsecutiveCount(t *testing.T) {
	t.Parallel()

	fp := Fingerprint(fingerprintInput())
	prior := State{Reprompts: MaxReprompts, LastFingerprint: fp, ConsecutiveNoProgress: MaxConsecutiveNoProgress}
	next, err := Observe(prior, fp)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if next.ConsecutiveNoProgress != MaxConsecutiveNoProgress {
		t.Fatalf("consecutive=%d, want saturation at %d", next.ConsecutiveNoProgress, MaxConsecutiveNoProgress)
	}
	if _, err := Encode(next); err != nil {
		t.Fatalf("saturated state must remain encodable: %v", err)
	}
}

func TestObserveTerminalIsAbsorbing(t *testing.T) {
	t.Parallel()

	fp := Fingerprint(fingerprintInput())
	prior := State{Reprompts: 1, LastFingerprint: fp, ConsecutiveNoProgress: 3, Terminal: true}
	next, err := Observe(prior, Fingerprint(fingerprintInput()))
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if next != prior {
		t.Fatalf("terminal state changed: %+v -> %+v", prior, next)
	}
}

func TestObserveRejectsInvalidStateWithoutRepairingIt(t *testing.T) {
	t.Parallel()

	fp := Fingerprint(fingerprintInput())
	for name, prior := range map[string]State{
		"negative_reprompts":   {Reprompts: -1, LastFingerprint: fp},
		"reprompts_over_cap":   {Reprompts: MaxReprompts + 1, LastFingerprint: fp},
		"negative_consecutive": {LastFingerprint: fp, ConsecutiveNoProgress: -1},
		"consecutive_over":     {LastFingerprint: fp, ConsecutiveNoProgress: MaxConsecutiveNoProgress + 1},
		"malformed_baseline":   {LastFingerprint: "sha256:zz"},
		"no_progress_unbased":  {ConsecutiveNoProgress: 2},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			next, err := Observe(prior, fp)
			if !errors.Is(err, ErrInvalidState) {
				t.Fatalf("Observe err=%v, want ErrInvalidState", err)
			}
			if next != prior {
				t.Fatalf("Observe repaired invalid state: %+v -> %+v", prior, next)
			}
		})
	}
}

func TestObserveRejectsMalformedFingerprint(t *testing.T) {
	t.Parallel()

	if _, err := Observe(State{}, "not-a-digest"); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("Observe err=%v, want ErrInvalidState", err)
	}
}

func TestAdvanceRepromptCountsOnlyEmittedIntents(t *testing.T) {
	t.Parallel()

	fp := Fingerprint(fingerprintInput())
	next, err := Advance(State{LastFingerprint: fp, ConsecutiveNoProgress: 2}, Limits{MaxReprompts: MaxReprompts, NoProgressLimit: 2})
	if err != nil {
		t.Fatalf("Advance: %v", err)
	}
	if next.Reprompts != 1 {
		t.Fatalf("reprompts=%d, want 1", next.Reprompts)
	}
	if next.ConsecutiveNoProgress != 2 || next.LastFingerprint != fp || next.Terminal {
		t.Fatalf("Advance mutated unrelated fields: %+v", next)
	}
	if _, err := Encode(next); err != nil {
		t.Fatalf("proposed state must be encodable before commit: %v", err)
	}
}

// TestAdvanceRejectsUnobservedBaseline pins the proposed-state invariant: a
// public pure helper must never report success while returning a state its own
// Validate and Encode reject. An emitted intent implies an observed baseline,
// so advancing the valid zero State cannot produce a valid proposal.
func TestAdvanceRejectsUnobservedBaseline(t *testing.T) {
	t.Parallel()

	prior := State{}
	limits := Limits{MaxReprompts: 1, NoProgressLimit: 1}
	next, err := Advance(prior, limits)
	if !errors.Is(err, ErrInvalidState) {
		t.Fatalf("Advance(%+v, %+v) err=%v, want ErrInvalidState", prior, limits, err)
	}
	if next != prior {
		t.Fatalf("rejected Advance altered prior state: %+v -> %+v", prior, next)
	}
	if err := next.Validate(); err != nil {
		t.Fatalf("returned state must stay valid: %v", err)
	}
	if _, err := Encode(next); err != nil {
		t.Fatalf("returned state must stay encodable: %v", err)
	}
}

// TestAdvanceBaselineProposalStaysValidAndEncodable pins that the remediation
// rejects only an unobserved baseline: an observed baseline still advances,
// and the proposal is valid, encodable, and survives a decode round trip.
func TestAdvanceBaselineProposalStaysValidAndEncodable(t *testing.T) {
	t.Parallel()

	fp := Fingerprint(fingerprintInput())
	prior := State{LastFingerprint: fp}
	next, err := Advance(prior, Limits{MaxReprompts: 1, NoProgressLimit: 1})
	if err != nil {
		t.Fatalf("Advance on observed baseline: %v", err)
	}
	if next.Reprompts != 1 || next.LastFingerprint != fp || next.ConsecutiveNoProgress != 0 || next.Terminal {
		t.Fatalf("unexpected proposal %+v", next)
	}
	if err := next.Validate(); err != nil {
		t.Fatalf("proposed state must satisfy its own invariants: %v", err)
	}
	token, err := Encode(next)
	if err != nil {
		t.Fatalf("Encode proposal: %v", err)
	}
	decoded, err := Decode(token)
	if err != nil {
		t.Fatalf("Decode proposal: %v", err)
	}
	if decoded != next {
		t.Fatalf("round trip changed the proposal: %+v -> %+v", next, decoded)
	}
}

// TestAdvanceObservedStateAboveCapStillReportsExhausted pins that the
// remediation did not reorder the existing terminal/cap precedence: an
// exhausted total budget still reports ErrRepromptsExhausted.
func TestAdvanceObservedStateAboveCapStillReportsExhausted(t *testing.T) {
	t.Parallel()

	fp := Fingerprint(fingerprintInput())
	prior := State{Reprompts: MaxReprompts, LastFingerprint: fp}
	next, err := Advance(prior, Limits{MaxReprompts: MaxReprompts, NoProgressLimit: 1})
	if !errors.Is(err, ErrRepromptsExhausted) {
		t.Fatalf("Advance err=%v, want ErrRepromptsExhausted", err)
	}
	if next != prior {
		t.Fatalf("rejected Advance consumed a slot: %+v", next)
	}
}

func TestAdvanceRepromptRespectsConfiguredCap(t *testing.T) {
	t.Parallel()

	fp := Fingerprint(fingerprintInput())
	prior := State{Reprompts: 2, LastFingerprint: fp}
	next, err := Advance(prior, Limits{MaxReprompts: 2, NoProgressLimit: 2})
	if !errors.Is(err, ErrRepromptsExhausted) {
		t.Fatalf("Advance err=%v, want ErrRepromptsExhausted", err)
	}
	if next != prior {
		t.Fatalf("rejected Advance consumed a slot: %+v", next)
	}
}

func TestAdvanceRepromptRejectsTerminalAndInvalidInput(t *testing.T) {
	t.Parallel()

	fp := Fingerprint(fingerprintInput())
	terminal := State{Terminal: true, LastFingerprint: fp}
	if _, err := Advance(terminal, Limits{MaxReprompts: 1, NoProgressLimit: 1}); !errors.Is(err, ErrRepromptsExhausted) {
		t.Fatalf("Advance on terminal err=%v, want ErrRepromptsExhausted", err)
	}
	if _, err := Advance(State{LastFingerprint: fp}, Limits{MaxReprompts: 0, NoProgressLimit: 1}); !errors.Is(err, ErrInvalidLimits) {
		t.Fatalf("Advance err=%v, want ErrInvalidLimits", err)
	}
	if _, err := Advance(State{LastFingerprint: fp}, Limits{MaxReprompts: MaxReprompts + 1, NoProgressLimit: 1}); !errors.Is(err, ErrInvalidLimits) {
		t.Fatalf("Advance err=%v, want ErrInvalidLimits", err)
	}
}

func TestLimitsValidateBounds(t *testing.T) {
	t.Parallel()

	if err := (Limits{MaxReprompts: 1, NoProgressLimit: 1}).Validate(); err != nil {
		t.Fatalf("minimum limits must be valid: %v", err)
	}
	if err := (Limits{MaxReprompts: MaxReprompts, NoProgressLimit: MaxNoProgressLimit}).Validate(); err != nil {
		t.Fatalf("maximum limits must be valid: %v", err)
	}
	for name, limits := range map[string]Limits{
		"zero_reprompts":       {MaxReprompts: 0},
		"negative_reprompts":   {MaxReprompts: -1},
		"over_reprompts":       {MaxReprompts: MaxReprompts + 1},
		"zero_no_progress":     {NoProgressLimit: 0},
		"negative_no_progress": {NoProgressLimit: -1},
		"over_no_progress":     {NoProgressLimit: MaxNoProgressLimit + 1},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if err := limits.Validate(); !errors.Is(err, ErrInvalidLimits) {
				t.Fatalf("Validate err=%v, want ErrInvalidLimits", err)
			}
		})
	}
}

func TestRemainingIntersectsConfiguredAndPlatformCaps(t *testing.T) {
	t.Parallel()

	fp := Fingerprint(fingerprintInput())
	limits := Limits{MaxReprompts: 3, NoProgressLimit: 2}
	baseline := State{LastFingerprint: fp}
	platform := Platform{Cap: 3, Attempt: 1}
	cases := map[string]struct {
		state    State
		limits   Limits
		platform Platform
		want     int
		allowed  bool
	}{
		"initial_attempt_one": {
			state: baseline, platform: platform, want: 2, allowed: true,
		},
		"attempt_at_cap": {
			state: baseline, platform: Platform{Cap: 3, Attempt: 3}, want: 0, allowed: false,
		},
		"attempt_over_cap": {
			state: baseline, platform: Platform{Cap: 3, Attempt: 7}, want: 0, allowed: false,
		},
		"zero_snapshot_cap": {
			state: baseline, platform: Platform{Cap: 0, Attempt: 5}, want: 3, allowed: true,
		},
		"zero_attempt": {
			state: baseline, platform: Platform{Cap: 3, Attempt: 0}, want: 3, allowed: true,
		},
		"configured_cap_one_default": {
			// Configured cap one is the default and the binding constraint;
			// the platform still has two candidate-attempt slots.
			state: baseline, limits: Limits{MaxReprompts: 1, NoProgressLimit: 2}, platform: platform, want: 1, allowed: true,
		},
		"configured_cap_one_at_platform_cap": {
			state: baseline, limits: Limits{MaxReprompts: 1, NoProgressLimit: 2}, platform: Platform{Cap: 3, Attempt: 3}, want: 0, allowed: false,
		},
		"changing_fingerprints_keep_cap": {
			state:    State{Reprompts: 1, LastFingerprint: fp, ConsecutiveNoProgress: 0},
			platform: Platform{Cap: 3, Attempt: 2}, want: 1, allowed: true,
		},
		"progress_never_replenishes": {
			// Progress may reset consecutive no-progress, but the immutable
			// total budget keeps shrinking with the emitted intents.
			state: State{Reprompts: 2, LastFingerprint: fp}, platform: platform, want: 1, allowed: true,
		},
		"progress_at_configured_total_cap": {
			state: State{Reprompts: 3, LastFingerprint: fp}, platform: platform, want: 0, allowed: false,
		},
		"state_above_configured_cap_within_v1": {
			// Valid V1 state, but configured cap two stops it by eligibility.
			state: State{Reprompts: 3, LastFingerprint: fp}, limits: Limits{MaxReprompts: 2, NoProgressLimit: 2},
			platform: platform, want: 0, allowed: false,
		},
		"no_progress_at_limit": {
			state: State{LastFingerprint: fp, ConsecutiveNoProgress: 2}, platform: platform, want: 0, allowed: false,
		},
		"no_progress_below_limit": {
			state: State{LastFingerprint: fp, ConsecutiveNoProgress: 1}, platform: platform, want: 2, allowed: true,
		},
		"terminal_never_repairs": {
			state: State{Terminal: true}, platform: platform, want: 0, allowed: false,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			applied := tc.limits
			if applied == (Limits{}) {
				applied = limits
			}
			got, err := Remaining(tc.state, applied, tc.platform)
			if err != nil {
				t.Fatalf("Remaining: %v", err)
			}
			if got.Remaining != tc.want || got.Allowed != tc.allowed {
				t.Fatalf("Remaining(%+v)=%+v, want remaining=%d allowed=%t", tc.state, got, tc.want, tc.allowed)
			}
			if got.Remaining < 0 {
				t.Fatalf("Remaining underflowed: %+v", got)
			}
		})
	}
}

func TestRemainingRejectsInvalidStateAndLimits(t *testing.T) {
	t.Parallel()

	platform := Platform{Cap: 3, Attempt: 1}
	if _, err := Remaining(State{Reprompts: -1}, Limits{MaxReprompts: 1, NoProgressLimit: 1}, platform); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("Remaining err=%v, want ErrInvalidState", err)
	}
	if _, err := Remaining(State{}, Limits{MaxReprompts: 4, NoProgressLimit: 1}, platform); !errors.Is(err, ErrInvalidLimits) {
		t.Fatalf("Remaining err=%v, want ErrInvalidLimits", err)
	}
	if _, err := Remaining(State{}, Limits{MaxReprompts: 1, NoProgressLimit: 1}, Platform{Cap: -1, Attempt: 1}); !errors.Is(err, ErrInvalidPlatform) {
		t.Fatalf("Remaining err=%v, want ErrInvalidPlatform", err)
	}
}

func repeatHex(unit string, times int) string {
	var out strings.Builder
	for range times {
		out.WriteString(unit)
	}
	return out.String()
}
