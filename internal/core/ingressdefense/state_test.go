package ingressdefense

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"math"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// stateBase is the fixed fake clock origin for every state contract below. No
// adaptive-state test may sleep or read the wall clock: the state receives `now`
// from its caller so the whole backoff contract is deterministic.
var stateBase = time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)

func mustAddr(t *testing.T, raw string) netip.Addr {
	t.Helper()
	addr, err := netip.ParseAddr(raw)
	if err != nil {
		t.Fatalf("netip.ParseAddr(%q) = %v", raw, err)
	}
	return addr
}

func mustState(t *testing.T, limits StateLimits) *State {
	t.Helper()
	s, err := NewState(limits)
	if err != nil {
		t.Fatalf("NewState(%+v) = %v, want success", limits, err)
	}
	return s
}

func assertUntil(t *testing.T, label string, tr Transition, want time.Time) {
	t.Helper()
	if !tr.QuarantineUntil.Equal(want) {
		t.Fatalf("%s: QuarantineUntil = %s, want %s", label, tr.QuarantineUntil, want)
	}
}

// -- 2.1 transition algorithm ------------------------------------------------

func TestNewStateRejectsUnboundedLimits(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		limits StateLimits
	}{
		{name: "zero capacity", limits: StateLimits{StateTTL: time.Hour}},
		{name: "negative capacity", limits: StateLimits{MaxEntries: -1, StateTTL: time.Hour}},
		{name: "zero ttl", limits: StateLimits{MaxEntries: 8}},
		{name: "negative ttl", limits: StateLimits{MaxEntries: 8, StateTTL: -time.Hour}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			s, err := NewState(tc.limits)
			if err == nil {
				t.Fatalf("NewState(%+v) succeeded, want domain invariant error", tc.limits)
			}
			if s != nil {
				t.Fatalf("NewState(%+v) returned a state alongside error %v", tc.limits, err)
			}
		})
	}
	if got := mustState(t, validStateLimits()).Len(); got != 0 {
		t.Fatalf("fresh state Len() = %d, want 0", got)
	}
}

func TestIsolatedAuthFailuresBelowThresholdNeverQuarantine(t *testing.T) {
	t.Parallel()

	s := mustState(t, validStateLimits())
	policy := validPolicy()
	addr := mustAddr(t, "203.0.113.10")

	now := stateBase
	for i := 0; i < policy.AuthFailures-1; i++ {
		tr := s.RecordAuthFailure(addr, now, policy)
		if tr.QuarantineStarted {
			t.Fatalf("failure %d/%d started a quarantine: %+v", i+1, policy.AuthFailures-1, tr)
		}
		if tr.Reason != "" {
			t.Fatalf("failure %d below threshold reported reason %q; the reason vocabulary stays closed to quarantine transitions", i+1, tr.Reason)
		}
		if !tr.QuarantineUntil.IsZero() {
			t.Fatalf("failure %d below threshold set QuarantineUntil = %s, want zero", i+1, tr.QuarantineUntil)
		}
		if tr.EntryCount != 1 {
			t.Fatalf("failure %d EntryCount = %d, want 1", i+1, tr.EntryCount)
		}
		now = now.Add(time.Second)
	}
	if s.IsQuarantined(addr, now) {
		t.Fatal("isolated failures below the threshold must not quarantine the source")
	}
	if got := s.Len(); got != 1 {
		t.Fatalf("Len() = %d, want 1 tracked entry for the counted failures", got)
	}
}

func TestAuthFailureThresholdStartsQuarantine(t *testing.T) {
	t.Parallel()

	s := mustState(t, validStateLimits())
	policy := validPolicy()
	addr := mustAddr(t, "203.0.113.11")

	now := stateBase
	var tr Transition
	for i := 0; i < policy.AuthFailures; i++ {
		tr = s.RecordAuthFailure(addr, now, policy)
		now = now.Add(time.Second)
	}
	if !tr.QuarantineStarted {
		t.Fatalf("failure %d did not start a quarantine: %+v", policy.AuthFailures, tr)
	}
	if tr.Reason != ReasonAuthFailureThreshold {
		t.Fatalf("threshold reason = %q, want %q", tr.Reason, ReasonAuthFailureThreshold)
	}
	assertUntil(t, "threshold", tr, now.Add(-time.Second).Add(policy.InitialQuarantine))
	if !s.IsQuarantined(addr, now) {
		t.Fatal("source must be quarantined from the threshold crossing instant")
	}
	if s.IsQuarantined(addr, tr.QuarantineUntil) {
		t.Fatal("quarantine must lapse once the deadline passes without operator action")
	}
}

func TestAuthFailureWindowBeginsWithFirstCountedFailure(t *testing.T) {
	t.Parallel()

	policy := validPolicy()
	// The window starts at the first counted failure, so a failure landing one
	// whole window later opens a fresh window instead of extending the old one.
	offsets := []time.Duration{0, 59 * time.Second, 60 * time.Second, 61 * time.Second, 62 * time.Second, 63 * time.Second, 64 * time.Second}
	s := mustState(t, validStateLimits())
	addr := mustAddr(t, "203.0.113.12")

	var crossing int
	for i, offset := range offsets {
		tr := s.RecordAuthFailure(addr, stateBase.Add(offset), policy)
		if !tr.QuarantineStarted {
			continue
		}
		crossing++
		assertUntil(t, fmt.Sprintf("failure %d", i+1), tr, stateBase.Add(offset).Add(policy.InitialQuarantine))
	}
	if crossing != 1 {
		t.Fatalf("threshold crossings = %d, want exactly 1: the window must restart at the %s boundary", crossing, policy.FailureWindow)
	}
	if got := s.Len(); got != 1 {
		t.Fatalf("Len() = %d, want 1", got)
	}
}

func TestAuthFailureWindowResetsAfterThreshold(t *testing.T) {
	t.Parallel()

	s := mustState(t, validStateLimits())
	policy := validPolicy()
	addr := mustAddr(t, "203.0.113.13")

	now := stateBase
	for i := 0; i < policy.AuthFailures; i++ {
		s.RecordAuthFailure(addr, now, policy)
		now = now.Add(time.Second)
	}
	if !s.IsQuarantined(addr, now) {
		t.Fatal("expected an active quarantine after the first threshold crossing")
	}

	// The threshold resets the auth-window counter, so a further full window of
	// counted failures is required again.
	now = now.Add(time.Hour)
	for i := 0; i < policy.AuthFailures-1; i++ {
		if tr := s.RecordAuthFailure(addr, now, policy); tr.QuarantineStarted {
			t.Fatalf("post-threshold failure %d escalated without a full new window: %+v", i+1, tr)
		}
		now = now.Add(time.Second)
	}
	tr := s.RecordAuthFailure(addr, now, policy)
	if !tr.QuarantineStarted {
		t.Fatalf("post-threshold window did not reach the threshold: %+v", tr)
	}
	assertUntil(t, "second threshold", tr, now.Add(2*policy.InitialQuarantine))
}

// TestAuthFailureWindowCounterResetsInsideTheStillOpenWindow pins design rule 2's
// explicit post-threshold auth-window counter reset. Every counted failure here
// lands inside the still-open failure window, so a window-expiry restart cannot
// stand in for the reset: only clearing the counter and the window start makes
// the following failures count from one again.
func TestAuthFailureWindowCounterResetsInsideTheStillOpenWindow(t *testing.T) {
	t.Parallel()

	s := mustState(t, validStateLimits())
	policy := validPolicy()
	addr := mustAddr(t, "203.0.113.26")

	now := stateBase
	for i := range policy.AuthFailures {
		tr := s.RecordAuthFailure(addr, now, policy)
		now = now.Add(time.Second)
		if i == policy.AuthFailures-1 {
			if !tr.QuarantineStarted {
				t.Fatalf("failure %d did not cross the threshold: %+v", i+1, tr)
			}
			break
		}
		if tr.QuarantineStarted {
			t.Fatalf("failure %d crossed the threshold before %d counted failures: %+v", i+1, policy.AuthFailures, tr)
		}
	}

	// The first threshold crossing consumed only a few seconds of the window, so
	// the failures below stay inside it. Consuming the whole window here would
	// let the expiry branch restart the counter and hide a missing reset.
	for i := range policy.AuthFailures - 1 {
		tr := s.RecordAuthFailure(addr, now, policy)
		now = now.Add(time.Second)
		if tr.QuarantineStarted || tr.Reason != "" {
			t.Fatalf("post-threshold failure %d re-crossed inside the same open window: %+v", i+1, tr)
		}
		if elapsed := now.Sub(stateBase); elapsed >= policy.FailureWindow {
			t.Fatalf("post-threshold failure %d ran the window out at +%s, want every counted failure inside the %s window", i+1, elapsed, policy.FailureWindow)
		}
	}

	tr := s.RecordAuthFailure(addr, now, policy)
	if !tr.QuarantineStarted {
		t.Fatalf("the reset window did not reach the threshold again: %+v", tr)
	}
	if tr.Reason != ReasonAuthFailureThreshold {
		t.Fatalf("second crossing reason = %q, want %q", tr.Reason, ReasonAuthFailureThreshold)
	}
	assertUntil(t, "second in-window threshold", tr, now.Add(2*policy.InitialQuarantine))
}

func TestRepeatedOffenseEscalatesExponentiallyUpToTheCap(t *testing.T) {
	t.Parallel()

	s := mustState(t, validStateLimits())
	policy := validPolicy()
	addr := mustAddr(t, "203.0.113.14")

	want := []time.Duration{
		time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 16 * time.Minute,
		32 * time.Minute, 64 * time.Minute, policy.MaxQuarantine, policy.MaxQuarantine,
	}
	now := stateBase
	for i, duration := range want {
		tr := s.RecordProbe(addr, now, policy)
		if !tr.QuarantineStarted {
			t.Fatalf("probe %d did not start a quarantine: %+v", i+1, tr)
		}
		if tr.Reason != ReasonImpossiblePath {
			t.Fatalf("probe %d reason = %q, want %q", i+1, tr.Reason, ReasonImpossiblePath)
		}
		assertUntil(t, fmt.Sprintf("probe %d", i+1), tr, now.Add(duration))
		now = now.Add(duration)
	}
}

func TestProbeOffenseIsOneStrongOffenseOnTheSharedLevel(t *testing.T) {
	t.Parallel()

	s := mustState(t, validStateLimits())
	policy := validPolicy()
	addr := mustAddr(t, "203.0.113.15")

	tr := s.RecordProbe(addr, stateBase, policy)
	if !tr.QuarantineStarted {
		t.Fatalf("a single impossible-path hit must record one strong offense: %+v", tr)
	}
	if tr.EntryCount != 1 {
		t.Fatalf("EntryCount = %d, want 1: one source creates exactly one entry", tr.EntryCount)
	}

	// The shared offense level is proven by the next counted auth failure: it
	// escalates to the second level, not back to the first.
	now := stateBase.Add(policy.InitialQuarantine + time.Second)
	for i := 0; i < policy.AuthFailures; i++ {
		tr = s.RecordAuthFailure(addr, now, policy)
		now = now.Add(time.Second)
	}
	if !tr.QuarantineStarted {
		t.Fatalf("auth threshold did not quarantine: %+v", tr)
	}
	assertUntil(t, "auth threshold after one probe", tr, now.Add(-time.Second).Add(2*policy.InitialQuarantine))
}

func TestNonMonotonicClockKeepsTheLaterActiveDeadline(t *testing.T) {
	t.Parallel()

	s := mustState(t, validStateLimits())
	policy := validPolicy()
	addr := mustAddr(t, "203.0.113.16")

	first := s.RecordProbe(addr, stateBase, policy)
	assertUntil(t, "first offense", first, stateBase.Add(policy.InitialQuarantine))

	// A clock correction that moves `now` backwards must not shorten an already
	// active quarantine: the deadline is max(existing, now+duration).
	second := s.RecordProbe(addr, stateBase.Add(-10*time.Minute), policy)
	if !second.QuarantineUntil.Equal(first.QuarantineUntil) {
		t.Fatalf("rewound clock shortened the deadline to %s, want kept %s", second.QuarantineUntil, first.QuarantineUntil)
	}
}

func TestSuccessfulAuthClearsEntryAndOffenseLevel(t *testing.T) {
	t.Parallel()

	s := mustState(t, validStateLimits())
	policy := validPolicy()
	addr := mustAddr(t, "203.0.113.17")

	for i := range 3 {
		s.RecordProbe(addr, stateBase.Add(time.Duration(i)*time.Minute), policy)
	}
	if s.Len() != 1 {
		t.Fatalf("Len() = %d, want 1", s.Len())
	}
	if got := s.Clear(addr); got != 0 {
		t.Fatalf("Clear entry count = %d, want 0", got)
	}
	if s.IsQuarantined(addr, stateBase) {
		t.Fatal("cleared address must not stay quarantined")
	}
	if got := s.Clear(addr); got != 0 {
		t.Fatalf("second Clear entry count = %d, want 0", got)
	}

	// A cleared address starts from a fresh offense level, so a later single
	// probe is a first offense again rather than a re-escalation.
	after := stateBase.Add(time.Hour)
	tr := s.RecordProbe(addr, after, policy)
	assertUntil(t, "probe after clear", tr, after.Add(policy.InitialQuarantine))
}

func TestInactiveStateExpiresEntryAndOffenseLevel(t *testing.T) {
	t.Parallel()

	s := mustState(t, StateLimits{MaxEntries: 64, StateTTL: 10 * time.Minute})
	policy := validPolicy()
	policy.InitialQuarantine = time.Hour
	addr := mustAddr(t, "203.0.113.18")

	for i := range 4 {
		s.RecordProbe(addr, stateBase.Add(time.Duration(i)*time.Minute), policy)
	}
	if !s.IsQuarantined(addr, stateBase.Add(4*time.Minute)) {
		t.Fatal("expected an active quarantine before the state TTL")
	}
	if !s.IsQuarantined(addr, stateBase.Add(12*time.Minute)) {
		t.Fatal("expected the quarantine to still be active just before the state TTL elapses")
	}
	if s.IsQuarantined(addr, stateBase.Add(13*time.Minute)) {
		t.Fatal("state inactive for the state TTL must no longer quarantine the source")
	}
	if got := s.Len(); got != 0 {
		t.Fatalf("Len() = %d, want 0: inactivity must expire the entry", got)
	}

	// The accumulated offense level is gone with the entry: the next probe is a
	// first offense again.
	fresh := stateBase.Add(15 * time.Minute)
	tr := s.RecordProbe(addr, fresh, policy)
	assertUntil(t, "probe after inactivity", tr, fresh.Add(policy.InitialQuarantine))
}

func TestReadsDoNotRefreshHostileInactivityTTL(t *testing.T) {
	t.Parallel()

	s := mustState(t, StateLimits{MaxEntries: 64, StateTTL: 10 * time.Minute})
	policy := validPolicy()
	policy.InitialQuarantine = time.Hour
	addr := mustAddr(t, "203.0.113.19")

	s.RecordProbe(addr, stateBase, policy)
	for offset := time.Minute; offset < 10*time.Minute; offset += time.Minute {
		if !s.IsQuarantined(addr, stateBase.Add(offset)) {
			t.Fatalf("read at +%s lost the active quarantine", offset)
		}
	}
	if s.IsQuarantined(addr, stateBase.Add(10*time.Minute)) {
		t.Fatal("hostile inactivity TTL must not be refreshed by reads")
	}
	if got := s.Len(); got != 0 {
		t.Fatalf("Len() = %d, want 0: the read path must still expire the entry", got)
	}
}

func TestQuarantineDurationSaturatesWithoutOverflow(t *testing.T) {
	t.Parallel()

	const maxDuration = time.Duration(math.MaxInt64)
	for _, tc := range []struct {
		name                 string
		level                int
		initial, limit, want time.Duration
	}{
		// A non-positive level is unreachable: every call site raises the offense
		// level first, yet the ladder must not shorten the first-offense duration.
		{name: "non-positive level performs no doublings", level: 0, initial: time.Minute, limit: 2 * time.Hour, want: time.Minute},
		{name: "negative level performs no doublings", level: -1, initial: time.Minute, limit: 2 * time.Hour, want: time.Minute},
		{name: "first offense", level: 1, initial: time.Minute, limit: 2 * time.Hour, want: time.Minute},
		{name: "second offense doubles", level: 2, initial: time.Minute, limit: 2 * time.Hour, want: 2 * time.Minute},
		{name: "third offense quadruples", level: 3, initial: time.Minute, limit: 2 * time.Hour, want: 4 * time.Minute},
		{name: "cap reached", level: 8, initial: time.Minute, limit: 2 * time.Hour, want: 2 * time.Hour},
		{name: "huge level saturates at the ceiling", level: 1 << 20, initial: time.Minute, limit: 2 * time.Hour, want: 2 * time.Hour},
		{name: "nanosecond ladder stays exact", level: 30, initial: time.Nanosecond, limit: maxDuration, want: time.Duration(1) << 29},
		{name: "overflow guard returns the ceiling", level: 64, initial: time.Nanosecond, limit: maxDuration, want: maxDuration},
		{name: "saturated level returns the ceiling", level: 1 << 20, initial: time.Nanosecond, limit: maxDuration, want: maxDuration},
		{name: "zero initial quarantines at the ceiling", level: 5, initial: 0, limit: time.Hour, want: time.Hour},
		{name: "negative initial quarantines at the ceiling", level: 5, initial: -time.Minute, limit: time.Hour, want: time.Hour},
		// A non-positive ceiling is returned as-is, so a policy that skipped
		// Policy.Validate can land the deadline in the past. Unreachable from
		// config; pinned so the documented fallback stays true.
		{name: "zero initial with zero ceiling returns the ceiling", level: 5, initial: 0, limit: 0, want: 0},
		{name: "negative initial with negative ceiling returns the ceiling", level: 5, initial: -time.Minute, limit: -time.Hour, want: -time.Hour},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := quarantineDuration(tc.level, tc.initial, tc.limit); got != tc.want {
				t.Fatalf("quarantineDuration(%d, %s, %s) = %s, want %s", tc.level, tc.initial, tc.limit, got, tc.want)
			}
		})
	}
}

func TestSaturatedOffenseLevelNeverOverflowsTheStateDeadline(t *testing.T) {
	t.Parallel()

	const maxDuration = time.Duration(math.MaxInt64)
	s := mustState(t, validStateLimits())
	policy := Policy{
		Enabled:           true,
		AuthFailures:      1,
		FailureWindow:     time.Minute,
		InitialQuarantine: time.Nanosecond,
		MaxQuarantine:     maxDuration,
	}
	addr := mustAddr(t, "203.0.113.20")

	var tr Transition
	for range maxOffenseLevel * 2 {
		tr = s.RecordProbe(addr, stateBase, policy)
	}
	if got := tr.QuarantineUntil.Sub(stateBase); got != maxDuration {
		t.Fatalf("saturated deadline offset = %s, want %s", got, maxDuration)
	}
	if got := nextOffenseLevel(1 << 30); got != maxOffenseLevel {
		t.Fatalf("offense level = %d, want the saturating ceiling %d", got, maxOffenseLevel)
	}
}

func TestStateRecordsNothingForDisabledPolicyOrUnknownSource(t *testing.T) {
	t.Parallel()

	s := mustState(t, validStateLimits())
	disabled := validPolicy()
	disabled.Enabled = false
	addr := mustAddr(t, "203.0.113.21")

	if tr := s.RecordProbe(addr, stateBase, disabled); tr.QuarantineStarted || tr.EntryCount != 0 {
		t.Fatalf("disabled policy recorded a probe: %+v", tr)
	}
	if tr := s.RecordAuthFailure(addr, stateBase, disabled); tr.QuarantineStarted || tr.EntryCount != 0 {
		t.Fatalf("disabled policy recorded an auth failure: %+v", tr)
	}
	if s.IsQuarantined(addr, stateBase) {
		t.Fatal("a disabled policy must leave the source unquarantined")
	}
	if got := s.Len(); got != 0 {
		t.Fatalf("Len() = %d, want 0: a disabled policy creates no state", got)
	}

	enabled := validPolicy()
	if tr := s.RecordProbe(netip.Addr{}, stateBase, enabled); tr.QuarantineStarted || tr.EntryCount != 0 {
		t.Fatalf("unidentifiable source recorded a probe: %+v", tr)
	}
	if tr := s.RecordAuthFailure(netip.Addr{}, stateBase, enabled); tr.QuarantineStarted || tr.EntryCount != 0 {
		t.Fatalf("unidentifiable source recorded an auth failure: %+v", tr)
	}
	if got := s.Clear(netip.Addr{}); got != 0 {
		t.Fatalf("Clear of an unidentifiable source reported %d entries", got)
	}
	if got := s.Len(); got != 0 {
		t.Fatalf("Len() = %d, want 0: an unidentifiable source must never be tracked", got)
	}
}

func TestStateLeavesAdaptiveExemptionToTheAdapter(t *testing.T) {
	t.Parallel()

	s := mustState(t, validStateLimits())
	policy := validPolicy()
	policy.AdaptiveExemptCIDRs = []netip.Prefix{netip.MustParsePrefix("203.0.113.0/24")}
	addr := mustAddr(t, "203.0.113.22")

	if !policy.AdaptiveExempt(addr) {
		t.Fatal("fixture address must be inside the exemption allowlist")
	}
	if tr := s.RecordProbe(addr, stateBase, policy); !tr.QuarantineStarted {
		t.Fatalf("state must not evaluate the exemption allowlist itself: %+v", tr)
	}
}

func TestStateExposesOnlyOffenseRecordersAndNoStatusTakingAPI(t *testing.T) {
	t.Parallel()

	typ := reflect.TypeFor[*State]()
	want := []string{"Clear", "IsQuarantined", "Len", "RecordAuthFailure", "RecordProbe"}
	var got []string
	for method := range typ.Methods() {
		got = append(got, method.Name)
	}
	sort.Strings(got)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("State method set = %v, want exactly %v; a recorder taking a response status would let 403 count as hostile evidence", got, want)
	}
}

// -- 2.2 capacity, eviction, isolation, concurrency ----------------------------

func TestEntryShapeCarriesOnlyBoundedExactAddressData(t *testing.T) {
	t.Parallel()

	typ := reflect.TypeFor[entry]()
	if typ.NumField() != len(boundedEntryFields) {
		t.Fatalf("adaptive entry has %d fields, want exactly %d", typ.NumField(), len(boundedEntryFields))
	}
	for field := range typ.Fields() {
		if err := checkBoundedEntryField(typ, field); err != nil {
			t.Error(err)
		}
	}
}

func TestEntryShapeGateRejectsRetainedRequestData(t *testing.T) {
	t.Parallel()

	type rawPathEntry struct {
		offenseLevel  int
		lastHostileAt time.Time
		rawPath       string
	}
	type aggregateEntry struct {
		offenseLevel int
		bucket       netip.Prefix
	}
	type admittedEntry struct {
		offenseLevel int
	}
	for _, value := range []any{rawPathEntry{}, aggregateEntry{}} {
		typ := reflect.TypeOf(value)
		var rejected bool
		for field := range typ.Fields() {
			if err := checkBoundedEntryField(typ, field); err != nil {
				rejected = true
			}
		}
		if !rejected {
			t.Fatalf("%s passed the bounded entry gate; the gate is vacuous", typ)
		}
	}
	typ := reflect.TypeFor[admittedEntry]()
	if err := checkBoundedEntryField(typ, typ.Field(0)); err != nil {
		t.Fatalf("bounded entry field rejected: %v", err)
	}
}

var boundedEntryFields = map[string]reflect.Type{
	"windowStartedAt": reflect.TypeFor[time.Time](),
	"failures":        reflect.TypeFor[int](),
	"offenseLevel":    reflect.TypeFor[int](),
	"quarantineUntil": reflect.TypeFor[time.Time](),
	"lastHostileAt":   reflect.TypeFor[time.Time](),
	// admission is internal ring bookkeeping, not adaptive state: a bounded
	// monotonic counter that lets a ring slot prove it still owns the live entry
	// it names, after Clear or a TTL expiry left that same address re-admitted
	// into a second slot. It carries no request data, influences no offense
	// decision, and is never read outside the shard lock.
	"admission": reflect.TypeFor[uint64](),
}

// checkBoundedEntryField keeps one source entry limited to the normative
// adaptive-entry shape: bounded counters, penalty state and timestamps only.
func checkBoundedEntryField(owner reflect.Type, field reflect.StructField) error {
	want, ok := boundedEntryFields[field.Name]
	if !ok {
		return fmt.Errorf("%s.%s: an entry may retain only bounded counters, penalty state and timestamps", owner, field.Name)
	}
	if field.Type != want {
		return fmt.Errorf("%s.%s: type %s, want %s", owner, field.Name, field.Type, want)
	}
	return nil
}

// bannedStatePackages are package qualifiers whose mere use breaks the contract,
// whatever they select. Banning the qualifier rather than one call is what
// closes the errgroup.Group plus g.Go and aliased-handle evasions a literal
// substring list cannot see.
var bannedStatePackages = map[string]string{
	"context":  "adaptive state owns no cancellable I/O boundary",
	"os":       "adaptive state owns no process or filesystem state",
	"errgroup": "adaptive state owns no goroutine",
	"sql":      "adaptive state is process-local and owns no database",
	"bun":      "adaptive state is process-local and owns no database",
}

// bannedStateQualified are exact qualified names the state must never touch: every
// clock read, timer, ticker, sleep and scheduler call, plus the completion
// barrier whose only purpose is joining goroutines.
var bannedStateQualified = map[string]string{
	"time.Now":        "the state takes the current instant from its caller",
	"time.Since":      "the state takes the current instant from its caller",
	"time.After":      "the state takes the current instant from its caller",
	"time.AfterFunc":  "the state owns no timer",
	"time.NewTimer":   "the state owns no timer",
	"time.NewTicker":  "the state owns no ticker",
	"time.Tick":       "the state owns no ticker",
	"time.Sleep":      "the state owns no goroutine to sleep in",
	"runtime.Gosched": "the state owns no goroutine to yield",
	"sync.WaitGroup":  "the state owns no completion barrier",
}

// bannedStateImports are import paths the state must not pull in at all.
var bannedStateImports = map[string]string{
	"database/sql": "adaptive state is process-local and owns no database",
}

// stateResourceViolation returns the first reason src breaks the no-clock,
// no-goroutine, no-timer, no-I/O, no-persistence contract of the adaptive state,
// or "" when src honours it.
//
// The rule is structural, not a substring list:
//
//   - no `go` statement at all: the state is driven only by its callers;
//   - a `defer` is allowed only of a mutex release (Unlock/RLock/RUnlock), because
//     the state opens no file, socket, timer or lock handle to close;
//   - the clock, timer, sleep, scheduler and barrier names in
//     bannedStateQualified, the package qualifiers in bannedStatePackages, any
//     net.Dial* call, and the import paths in bannedStateImports;
//   - a dot or blank import, which would leave a banned dependency unnameable.
func stateResourceViolation(name, src string) string {
	file, err := parser.ParseFile(token.NewFileSet(), name, src, parser.SkipObjectResolution)
	if err != nil {
		return "cannot be parsed: " + err.Error()
	}
	violation := ""
	ast.Inspect(file, func(node ast.Node) bool {
		if violation != "" {
			return false
		}
		switch typed := node.(type) {
		case *ast.GoStmt:
			violation = "starts a goroutine; the state is driven only by its callers"
		case *ast.DeferStmt:
			violation = deferredCallViolation(typed.Call)
		case *ast.ImportSpec:
			violation = importViolation(typed)
		case *ast.SelectorExpr:
			violation = qualifiedNameViolation(typed)
		}
		return violation == ""
	})
	return violation
}

// deferredCallViolation reports why a deferred call is not a plainly safe mutex
// release. Shard locking is the only thing the state defers, and it defers it
// exactly once per mutation, so any other deferred call is unowned work.
func deferredCallViolation(call *ast.CallExpr) string {
	if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
		switch sel.Sel.Name {
		case "Unlock", "RLock", "RUnlock":
			return ""
		}
	}
	return "defers " + callName(call.Fun) + "(); only a mutex release may be deferred, the state owns no resource to close"
}

// importViolation reports why an import spec breaks the contract.
func importViolation(spec *ast.ImportSpec) string {
	path := strings.Trim(spec.Path.Value, `"`)
	if reason, banned := bannedStateImports[path]; banned {
		return "imports " + path + "; " + reason
	}
	if spec.Name != nil && (spec.Name.Name == "_" || spec.Name.Name == ".") {
		return "imports " + path + " as " + spec.Name.Name + "; a blank or dot import leaves a banned dependency unnameable"
	}
	return ""
}

// qualifiedNameViolation reports why one pkg.Sel selector breaks the contract.
func qualifiedNameViolation(sel *ast.SelectorExpr) string {
	pkg, ok := sel.X.(*ast.Ident)
	if !ok {
		return ""
	}
	qualified := pkg.Name + "." + sel.Sel.Name
	if reason, banned := bannedStatePackages[pkg.Name]; banned {
		return "uses " + qualified + "; " + reason
	}
	if reason, banned := bannedStateQualified[qualified]; banned {
		return "uses " + qualified + "; " + reason
	}
	if strings.HasPrefix(qualified, "net.Dial") {
		return "uses " + qualified + "; adaptive state owns no network resource"
	}
	return ""
}

// callName renders a callee as pkg.Sel for a package-qualified function or a
// method, and as the bare identifier for a function name.
func callName(fun ast.Expr) string {
	switch typed := fun.(type) {
	case *ast.SelectorExpr:
		if pkg, ok := typed.X.(*ast.Ident); ok {
			return pkg.Name + "." + typed.Sel.Name
		}
		return typed.Sel.Name
	case *ast.Ident:
		return typed.Name
	}
	return ""
}

func TestStateOwnsNoClockGoroutineOrTimer(t *testing.T) {
	t.Parallel()

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	scanned := 0
	for _, file := range entries {
		name := file.Name()
		if file.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(filepath.Join(".", name))
		if err != nil {
			t.Fatal(err)
		}
		scanned++
		if violation := stateResourceViolation(name, string(src)); violation != "" {
			t.Errorf("%s %s; adaptive state owns no clock, goroutine, timer, I/O or persistence", name, violation)
		}
	}
	if scanned == 0 {
		t.Fatal("no production source scanned; the resource gate is vacuous")
	}
	if err := runtimeResourceViolation(reflect.TypeFor[State](), map[reflect.Type]bool{}); err != nil {
		t.Errorf("State: %v", err)
	}
}

// TestStateResourceGateRejectsEvadingShapes proves the resource gate is not
// vacuous. Every shape the package contract forbids must be reported, including
// the ones a literal substring list misses, while the shapes the real production
// code legitimately uses must stay clean.
func TestStateResourceGateRejectsEvadingShapes(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		src  string
		want string
	}{
		{name: "timer callback", src: "package p\n\nfunc f() { time.AfterFunc(time.Minute, work) }\n", want: "time.AfterFunc"},
		{name: "channel wait", src: "package p\n\nfunc f() { <-time.After(time.Minute) }\n", want: "time.After"},
		{name: "sleep", src: "package p\n\nfunc f() { time.Sleep(time.Minute) }\n", want: "time.Sleep"},
		{name: "wall clock", src: "package p\n\nfunc f() { _ = time.Now() }\n", want: "time.Now"},
		{name: "ticker", src: "package p\n\nfunc f() { time.NewTicker(time.Minute) }\n", want: "time.NewTicker"},
		{name: "errgroup with g.Go", src: "package p\n\nfunc f() {\n\tvar g errgroup.Group\n\tg.Go(work)\n}\n", want: "errgroup"},
		{name: "wait group with wg.Go", src: "package p\n\nfunc f() {\n\tvar wg sync.WaitGroup\n\twg.Go(work)\n}\n", want: "sync.WaitGroup"},
		{name: "network dial", src: "package p\n\nfunc f() { net.Dial(\"tcp\", addr) }\n", want: "net.Dial"},
		{name: "dialer value", src: "package p\n\nfunc f() { d := net.Dialer{}; _ = d }\n", want: "net.Dial"},
		{name: "database handle", src: "package p\n\nimport \"database/sql\"\n", want: "database/sql"},
		{name: "process environment", src: "package p\n\nimport \"os\"\n\nfunc f() { os.Getenv(\"AIProxer\") }\n", want: "os"},
		{name: "request context", src: "package p\n\nimport \"context\"\n\nfunc f(ctx context.Context) {}\n", want: "context"},
		{name: "scheduler yield", src: "package p\n\nfunc f() { runtime.Gosched() }\n", want: "runtime.Gosched"},
		{name: "bare goroutine", src: "package p\n\nfunc f() { go work() }\n", want: "goroutine"},
		{name: "deferred close", src: "package p\n\nfunc f() { defer close(done) }\n", want: "only a mutex release"},
		{name: "deferred function value", src: "package p\n\nfunc f() { defer release() }\n", want: "only a mutex release"},
		{name: "dot import", src: "package p\n\nimport . \"time\"\n", want: "dot import"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if violation := stateResourceViolation("synthetic.go", tc.src); !strings.Contains(violation, tc.want) {
				t.Fatalf("stateResourceViolation = %q, want it to report %q", violation, tc.want)
			}
		})
	}

	for _, tc := range []struct{ name, src string }{
		{name: "clock values are not clock reads", src: "package p\n\nimport \"time\"\n\ntype e struct {\n\tat  time.Time\n\tttl time.Duration\n}\n"},
		{name: "sharded state with one deferred release", src: "package p\n\nimport (\n\t\"net/netip\"\n\t\"sync\"\n)\n\ntype shard struct {\n\tmu      sync.Mutex\n\tentries map[netip.Addr]int\n}\n\nfunc f(s *shard) {\n\ts.mu.Lock()\n\tdefer s.mu.Unlock()\n\ts.entries[netip.Addr{}] = 1\n}\n"},
		{name: "arithmetic and counters", src: "package p\n\nimport (\n\t\"math\"\n\t\"sync/atomic\"\n)\n\nconst ceiling = 64\n\nvar total atomic.Int64\n\nfunc f() int { return min(math.MaxInt64, ceiling) + int(total.Load()) }\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if violation := stateResourceViolation("synthetic.go", tc.src); violation != "" {
				t.Fatalf("legitimate production shape rejected: %s", violation)
			}
		})
	}

	for _, tc := range []struct {
		name string
		typ  any
	}{
		{name: "void callback field", typ: struct{ onDone func() }{}},
		{name: "argument callback field", typ: struct{ weight func(int) int }{}},
		{name: "result callback field", typ: struct{ lookup func(string) (int, error) }{}},
		{name: "variadic callback field", typ: struct{ log func(string, ...any) }{}},
		{name: "channel field", typ: struct{ wake chan struct{} }{}},
		{name: "buffered channel field", typ: struct{ events chan int }{}},
		{name: "timer field", typ: struct{ deadline *time.Timer }{}},
		{name: "ticker field", typ: struct{ tick *time.Ticker }{}},
		{name: "completion barrier field", typ: struct{ wg sync.WaitGroup }{}},
	} {
		t.Run("reflection rejects "+tc.name, func(t *testing.T) {
			t.Parallel()

			if err := runtimeResourceViolation(reflect.TypeOf(tc.typ), map[reflect.Type]bool{}); err == nil {
				t.Fatalf("%s passed the state resource gate; the gate is vacuous", tc.name)
			}
		})
	}
	for _, tc := range []struct {
		name string
		typ  any
	}{
		{name: "State", typ: State{}},
		{name: "shard", typ: shard{}},
		{name: "entry", typ: entry{}},
	} {
		t.Run("reflection admits "+tc.name, func(t *testing.T) {
			t.Parallel()

			if err := runtimeResourceViolation(reflect.TypeOf(tc.typ), map[reflect.Type]bool{}); err != nil {
				t.Fatalf("production shape rejected: %v", err)
			}
		})
	}
}

// runtimeResourceViolation rejects any runtime-capable shape reachable from the
// state: a function value whatever its signature (a func(int) int callback is as
// much an owned hook as a func()), a channel, a timer, a ticker, or a completion
// barrier that only has meaning next to a goroutine.
func runtimeResourceViolation(typ reflect.Type, seen map[reflect.Type]bool) error {
	if typ == nil || seen[typ] {
		return nil
	}
	seen[typ] = true
	switch typ.Kind() {
	case reflect.Func:
		return fmt.Errorf("field of type %s introduces a callback; adaptive state must be driven only by its callers", typ)
	case reflect.Chan:
		return fmt.Errorf("field of type %s introduces a channel", typ)
	}
	switch typ {
	case reflect.TypeFor[time.Timer](), reflect.TypeFor[time.Ticker](), reflect.TypeFor[sync.WaitGroup]():
		return fmt.Errorf("field of type %s introduces a timer, ticker or completion barrier", typ)
	}
	switch typ.Kind() {
	case reflect.Struct:
		for field := range typ.Fields() {
			if err := runtimeResourceViolation(field.Type, seen); err != nil {
				return err
			}
		}
	case reflect.Slice, reflect.Array, reflect.Pointer, reflect.Map:
		return runtimeResourceViolation(typ.Elem(), seen)
	}
	return nil
}

func TestHardCapacityAndDeterministicEvictionUnderUniqueAddressChurn(t *testing.T) {
	t.Parallel()

	const capacity = 8
	limits := StateLimits{MaxEntries: capacity, StateTTL: 24 * time.Hour}
	policy := validPolicy()

	// The eviction rule is least recently ADMITTED inside the shard, and with
	// MaxEntries == capacity the capacity splits into `capacity` one-entry
	// shards. No entry is cleared and none expires during the churn, so every
	// slot holds a live key and the least recently admitted live key of a
	// one-entry shard is exactly its previous occupant. The survivors are
	// therefore the newest address of every shard, and this is an assertion
	// about which addresses survive, not only about how many.
	run := func(t *testing.T) (expected, survivors map[int]string) {
		t.Helper()
		s := mustState(t, limits)
		expected = make(map[int]string, capacity)
		for i := range 64 {
			addr := netip.AddrFrom4([4]byte{198, 51, 100, byte(i)})
			expected[s.shardIndex(addr)] = addr.String()
			s.RecordProbe(addr, stateBase, policy)
			if got := s.Len(); got > capacity {
				t.Fatalf("Len() = %d after %d insertions, want at most the configured capacity %d", got, i+1, capacity)
			}
		}
		if got := s.Len(); got != capacity {
			t.Fatalf("saturated Len() = %d, want %d", got, capacity)
		}
		survivors = make(map[int]string, capacity)
		for i := range 64 {
			addr := netip.AddrFrom4([4]byte{198, 51, 100, byte(i)})
			if !s.IsQuarantined(addr, stateBase) {
				continue
			}
			shard := s.shardIndex(addr)
			if _, duplicate := survivors[shard]; duplicate {
				t.Fatalf("shard %d holds more than one survivor; every entry must be exact-address keyed", shard)
			}
			survivors[shard] = addr.String()
		}
		return expected, survivors
	}

	first, firstSurvivors := run(t)
	_, replaySurvivors := run(t)
	if len(first) != capacity {
		t.Fatalf("shards holding state = %d, want the capacity %d", len(first), capacity)
	}
	if !reflect.DeepEqual(firstSurvivors, first) {
		t.Fatalf("eviction kept %v, want the least-recently-admitted rule survivors %v", firstSurvivors, first)
	}
	if !reflect.DeepEqual(firstSurvivors, replaySurvivors) {
		t.Fatalf("eviction is not deterministic: %v then %v", firstSurvivors, replaySurvivors)
	}
}

// TestEvictionCursorAdvancesPastAStaleRingSlotWithoutEvictingALiveKey pins the
// real which-key rule of the bounded ring. Clear removes an entry from the entry
// map but leaves its admission slot, so the next admission overwrites that stale
// slot and advances the cursor while evicting no live key. An idealized
// "always evicts the oldest live key" model would have dropped the still-live
// neighbour here instead.
func TestEvictionCursorAdvancesPastAStaleRingSlotWithoutEvictingALiveKey(t *testing.T) {
	t.Parallel()

	// 32 shards of capacity 2, so exactly one shard holds a two-entry ring.
	s := mustState(t, StateLimits{MaxEntries: 64, StateTTL: 24 * time.Hour})
	policy := validPolicy()
	ring := addressesInSameShard(t, s, 0, 4)
	first, second, third, fourth := ring[0], ring[1], ring[2], ring[3]

	s.RecordProbe(first, stateBase, policy)
	s.RecordProbe(second, stateBase, policy)
	// The ring is now full: [first, second], cursor 0.
	s.Clear(first)
	if s.IsQuarantined(first, stateBase) {
		t.Fatal("Clear must drop the entry so its admission slot becomes stale")
	}

	s.RecordProbe(third, stateBase, policy)
	if !s.IsQuarantined(second, stateBase) {
		t.Fatal("a stale ring slot must advance the cursor without evicting the live neighbour")
	}
	if !s.IsQuarantined(third, stateBase) {
		t.Fatal("the newly admitted address must keep its own state")
	}
	if got := s.Len(); got != 2 {
		t.Fatalf("Len() = %d, want 2: the stale slot evicts nothing", got)
	}

	// The cursor now points at the second slot, so the next admission evicts the
	// live key that slot still holds and the ring keeps exactly two entries.
	s.RecordProbe(fourth, stateBase, policy)
	if s.IsQuarantined(second, stateBase) {
		t.Fatal("the live key under the cursor must be the one evicted")
	}
	if !s.IsQuarantined(third, stateBase) || !s.IsQuarantined(fourth, stateBase) {
		t.Fatal("the two most recently admitted keys must survive their own shard")
	}
	if got := s.Len(); got != 2 {
		t.Fatalf("Len() = %d, want 2: eviction replaces, it never grows", got)
	}
}

// staleSlotReAdmission drives one capacity-2 shard into the state where the ring
// holds the same address twice: once in a slot whose entry is gone, and once in
// the slot that owns the address's current live entry. The cursor is then parked
// on the STALE slot, so the next admission reads that stale slot as if it owned
// the live entry.
//
// The sequence, on a shard of capacity 2:
//
//	admit first, second   ring [first, second] cursor 0
//	admit third           ring [third, second] cursor 1, first evicted
//	drop the third entry  ring still [third, second], the third slot is now stale
//	re-admit third        ring [third, third] cursor 0, second evicted
//
// Both slots now name third, and the cursor points at the stale one. A rule that
// decides ownership from the address alone reads the stale slot as owning the live
// entry and evicts it, even though the live entry was admitted more recently than
// every other address in the shard. Admission identity, not address identity, is
// what a slot owns.
func staleSlotReAdmission(t *testing.T, s *State, policy Policy, ring []netip.Addr, dropThird func()) {
	t.Helper()
	first, second, third, fourth := ring[0], ring[1], ring[2], ring[3]

	s.RecordProbe(first, stateBase, policy)
	s.RecordProbe(second, stateBase, policy)
	s.RecordProbe(third, stateBase, policy)
	if s.IsQuarantined(first, stateBase) {
		t.Fatal("the least recently admitted address must have been evicted")
	}
	dropThird()
	if s.IsQuarantined(third, stateBase) {
		t.Fatal("the third entry must be gone, so its ring slot is stale")
	}
	s.RecordProbe(third, stateBase, policy)
	if !s.IsQuarantined(third, stateBase) {
		t.Fatal("the re-admitted address must hold its own live entry")
	}
	if s.IsQuarantined(second, stateBase) {
		t.Fatal("the re-admission must have evicted the live second address")
	}
	if got := s.Len(); got != 1 {
		t.Fatalf("Len() = %d, want 1: the ring is full of slots but only the re-admitted entry is live", got)
	}

	// The cursor is now on the stale third slot while the live third entry is
	// owned by the other slot. Admitting fourth must reuse the stale slot
	// without evicting the live third entry.
	s.RecordProbe(fourth, stateBase, policy)
	if !s.IsQuarantined(third, stateBase) {
		t.Fatal("a stale ring slot must not evict the live entry that re-admission gave the same address")
	}
	if !s.IsQuarantined(fourth, stateBase) {
		t.Fatal("the newly admitted address must keep its own state")
	}
	if got := s.Len(); got != 2 {
		t.Fatalf("Len() = %d, want 2: the stale slot overwrote itself without evicting a live entry", got)
	}
}

// TestStaleAdmissionSlotDoesNotEvictTheLiveEntryOfAReAdmittedAddress is the
// Clear -> re-admit -> stale-slot regression. Clear is an ordinary
// successful-authentication transition, so this sequence is reachable in normal
// operation and must not shorten a re-quarantined source's state.
func TestStaleAdmissionSlotDoesNotEvictTheLiveEntryOfAReAdmittedAddress(t *testing.T) {
	t.Parallel()

	s := mustState(t, StateLimits{MaxEntries: 64, StateTTL: 24 * time.Hour})
	ring := addressesInSameShard(t, s, 0, 4)
	staleSlotReAdmission(t, s, validPolicy(), ring, func() {
		s.Clear(ring[2])
	})
}

// TestStaleAdmissionSlotDoesNotEvictTheLiveEntryAfterTTLExpiry is the same
// regression through the other stale-slot producer. A hostile-inactivity TTL
// expiry drops the entry lazily on the next lookup and leaves exactly the same
// stale ring slot Clear leaves.
func TestStaleAdmissionSlotDoesNotEvictTheLiveEntryAfterTTLExpiry(t *testing.T) {
	t.Parallel()

	ttl := time.Hour
	s := mustState(t, StateLimits{MaxEntries: 64, StateTTL: ttl})
	ring := addressesInSameShard(t, s, 0, 4)
	// The lazy expiry runs inside the state on this read, so the helper's own
	// stateBase reads must see a pre-expiry third entry and a post-expiry one.
	expired := stateBase.Add(2 * ttl)
	staleSlotReAdmission(t, s, validPolicy(), ring, func() {
		if s.IsQuarantined(ring[2], expired) {
			t.Fatal("the entry must expire once the hostile-inactivity TTL has elapsed")
		}
	})
}

// TestAdmissionRingIsDeterministicAcrossClearAndReAdmit guards the ownership
// rule from the other side. Tagging slots with admission identity must not change
// WHICH addresses survive an ordinary fill/clear/re-fill cycle: clearing every
// entry and re-admitting the same addresses in the same order has to reproduce
// the first pass exactly. A fix that compacts the ring, reorders slots, or skips
// the cursor advance on a stale slot changes this result even though it removes
// the double-eviction, so this test keeps the fix honest.
func TestAdmissionRingIsDeterministicAcrossClearAndReAdmit(t *testing.T) {
	t.Parallel()

	s := mustState(t, StateLimits{MaxEntries: 64, StateTTL: 24 * time.Hour})
	policy := validPolicy()
	ring := addressesInSameShard(t, s, 0, 4)

	survivors := func() []netip.Addr {
		t.Helper()
		var live []netip.Addr
		for _, addr := range ring {
			if s.IsQuarantined(addr, stateBase) {
				live = append(live, addr)
			}
		}
		return live
	}

	for _, addr := range ring {
		s.RecordProbe(addr, stateBase, policy)
	}
	firstPass := survivors()
	if len(firstPass) != 2 {
		t.Fatalf("first pass survivors = %v, want the 2 the capacity-2 ring holds", firstPass)
	}
	for _, addr := range ring {
		s.Clear(addr)
	}
	if got := s.Len(); got != 0 {
		t.Fatalf("Len() = %d after clearing every entry, want 0", got)
	}
	for _, addr := range ring {
		s.RecordProbe(addr, stateBase, policy)
	}
	secondPass := survivors()
	if !reflect.DeepEqual(secondPass, firstPass) {
		t.Fatalf("re-admitting after a clear-all kept %v, want the first pass's %v", secondPass, firstPass)
	}
}

func TestEvictionRemovesOnlyTheEvictedAddressItself(t *testing.T) {
	t.Parallel()

	policy := validPolicy()
	s := mustState(t, StateLimits{MaxEntries: 2, StateTTL: 24 * time.Hour})
	order := make([]netip.Addr, 0, 8)
	for i := range 8 {
		addr := netip.AddrFrom4([4]byte{192, 0, 2, byte(i)})
		order = append(order, addr)
		s.RecordProbe(addr, stateBase, policy)
		if got := s.Len(); got > 2 {
			t.Fatalf("Len() = %d, want at most 2", got)
		}
	}

	quarantined := 0
	for _, addr := range order {
		if s.IsQuarantined(addr, stateBase) {
			quarantined++
		}
	}
	if quarantined != 2 {
		t.Fatalf("quarantined addresses after churn = %d, want exactly the capacity 2", quarantined)
	}
	if !s.IsQuarantined(order[len(order)-1], stateBase) {
		t.Fatal("the most recent address must retain its own state")
	}
}

func TestLazyExpiryRunsOnLookupAndMutation(t *testing.T) {
	t.Parallel()

	policy := validPolicy()
	policy.AuthFailures = 1
	later := stateBase.Add(30 * time.Minute)
	for _, tc := range []struct {
		name    string
		expires func(*State, netip.Addr, Policy) Transition
		wantLen int
	}{
		{
			name:    "lookup drops the expired entry",
			expires: func(s *State, a netip.Addr, _ Policy) Transition { s.IsQuarantined(a, later); return Transition{} },
			wantLen: 0,
		},
		{
			name:    "probe replaces the expired entry",
			expires: func(s *State, a netip.Addr, p Policy) Transition { return s.RecordProbe(a, later, p) },
			wantLen: 1,
		},
		{
			name:    "auth failure replaces the expired entry",
			expires: func(s *State, a netip.Addr, p Policy) Transition { return s.RecordAuthFailure(a, later, p) },
			wantLen: 1,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			s := mustState(t, StateLimits{MaxEntries: 8, StateTTL: 10 * time.Minute})
			addr := mustAddr(t, "203.0.113.23")
			s.RecordProbe(addr, stateBase, policy)
			if got := s.Len(); got != 1 {
				t.Fatalf("Len() = %d, want 1", got)
			}

			tr := tc.expires(s, addr, policy)
			if got := s.Len(); got != tc.wantLen {
				t.Fatalf("Len() = %d, want %d", got, tc.wantLen)
			}
			if tc.wantLen != 1 {
				return
			}
			// The entry is replaced, not just counted, so the replacing offense is
			// a fresh first offense. An entry count alone cannot tell the two apart.
			assertUntil(t, tc.name, tr, later.Add(policy.InitialQuarantine))
			if !s.IsQuarantined(addr, later) {
				t.Fatal("the fresh offense at the later instant must quarantine the source again")
			}
		})
	}
}

// TestMutationPathExpiresAnEntryBeforeUseWithoutAnInterveningRead pins state
// transition rule 1 on the two mutation paths. Lazy expiry has to happen inside
// the recorders, not only inside the read path: a source that goes quiet past the
// state TTL and comes back must start a fresh offense level and a fresh
// authentication window. Otherwise a benign client that simply pauses keeps its
// accumulated level and walks itself into a permanent quarantine.
//
// No IsQuarantined and no Clear runs between the last hostile event and the
// post-gap event; the only state touch in between is the entry-count gauge, which
// reads no entry and refreshes no timestamp. Every assertion is on the observable
// deadline or on the threshold crossing, never on the entry count: a replaced entry
// reports Len() == 1 whether the accumulated level was kept or dropped.
func TestMutationPathExpiresAnEntryBeforeUseWithoutAnInterveningRead(t *testing.T) {
	t.Parallel()

	t.Run("probe", func(t *testing.T) {
		t.Parallel()

		policy := validPolicy()
		limits := StateLimits{MaxEntries: 8, StateTTL: 10 * time.Minute}
		addr := mustAddr(t, "203.0.113.28")
		s := mustState(t, limits)

		// Warm the entry up to an escalated level, entirely inside the TTL; the
		// doubling deadlines prove no warm-up probe expired the entry, and the last
		// one leaves lastHostileAt where the post-gap probe below lands exactly on
		// the rule 1 boundary, which is the only instant that pins >= over >.
		const warmUp = 4
		for i := range warmUp {
			at := stateBase.Add(time.Duration(i) * time.Minute)
			tr := s.RecordProbe(addr, at, policy)
			if !tr.QuarantineStarted {
				t.Fatalf("warm-up probe %d did not start a quarantine: %+v", i+1, tr)
			}
			assertUntil(t, fmt.Sprintf("warm-up probe %d", i+1), tr, at.Add(time.Duration(1<<i)*policy.InitialQuarantine))
		}
		if got := s.Len(); got != 1 {
			t.Fatalf("Len() = %d, want 1 entry for the warm-up offenses", got)
		}

		gap := stateBase.Add((warmUp - 1) * time.Minute).Add(limits.StateTTL)
		if elapsed := gap.Sub(stateBase.Add((warmUp - 1) * time.Minute)); elapsed != limits.StateTTL {
			t.Fatalf("inactivity gap = %s, want exactly the %s state TTL, so the post-gap probe sits on the rule 1 boundary", elapsed, limits.StateTTL)
		}
		tr := s.RecordProbe(addr, gap, policy)
		if !tr.QuarantineStarted {
			t.Fatalf("post-gap probe did not start a quarantine: %+v", tr)
		}
		if tr.Reason != ReasonImpossiblePath {
			t.Fatalf("post-gap probe reason = %q, want %q", tr.Reason, ReasonImpossiblePath)
		}
		// A fresh level 1 deadline, not an unexpired entry's level warmUp+1.
		assertUntil(t, "post-gap probe", tr, gap.Add(policy.InitialQuarantine))
		if got := s.Len(); got != 1 {
			t.Fatalf("Len() = %d, want exactly 1: the expired entry is replaced, not duplicated", got)
		}
	})

	t.Run("auth failure", func(t *testing.T) {
		t.Parallel()

		policy := validPolicy()
		// windowStartedAt is never later than lastHostileAt, so a TTL at or above
		// the failure window makes every TTL gap also a window gap and the
		// window-expiry branch would reset the counter on its own: the fresh
		// authentication window below would then prove nothing about the inactivity
		// expiry. The fresh level-1 deadline observes that expiry in any shape.
		limits := StateLimits{MaxEntries: 8, StateTTL: time.Minute}
		policy.FailureWindow = 10 * time.Minute
		if limits.StateTTL >= policy.FailureWindow {
			t.Fatalf("state TTL %s must be shorter than the failure window %s for this case to discriminate", limits.StateTTL, policy.FailureWindow)
		}
		addr := mustAddr(t, "203.0.113.29")
		s := mustState(t, limits)

		// A sub-threshold partial window plus one probe, so the entry carries both a
		// failure count and an offense level before the gap. All of it lands inside
		// the TTL, so nothing expires on the way.
		for i := range policy.AuthFailures - 1 {
			tr := s.RecordAuthFailure(addr, stateBase.Add(time.Duration(i)*time.Second), policy)
			if tr.QuarantineStarted {
				t.Fatalf("warm-up failure %d/%d crossed the threshold: %+v", i+1, policy.AuthFailures-1, tr)
			}
		}
		warmProbe := stateBase.Add(10 * time.Second)
		warm := s.RecordProbe(addr, warmProbe, policy)
		if !warm.QuarantineStarted {
			t.Fatalf("warm-up probe did not start a quarantine: %+v", warm)
		}
		assertUntil(t, "warm-up probe", warm, warmProbe.Add(policy.InitialQuarantine))
		if got := s.Len(); got != 1 {
			t.Fatalf("Len() = %d, want 1 entry for the warm-up offenses", got)
		}

		// The post-gap failure must be exactly on the TTL boundary and still
		// inside the open failure window, the only instant at which the two
		// expiries disagree. lastHostileAt is warmProbe, so that is warmProbe+TTL.
		gap := warmProbe.Add(limits.StateTTL)
		if elapsed := gap.Sub(warmProbe); elapsed != limits.StateTTL {
			t.Fatalf("inactivity gap = %s, want exactly the %s state TTL, so the post-gap failure sits on the rule 1 boundary", elapsed, limits.StateTTL)
		}
		if elapsed := gap.Sub(stateBase); elapsed >= policy.FailureWindow {
			t.Fatalf("post-gap failure at +%s left the %s failure window, so the window-expiry branch would mask the inactivity expiry", elapsed, policy.FailureWindow)
		}

		// The pre-gap count was AuthFailures-1, so carrying it over would cross the
		// threshold on this very first post-gap failure.
		first := s.RecordAuthFailure(addr, gap, policy)
		if first.QuarantineStarted || first.Reason != "" {
			t.Fatalf("post-gap failure carried the pre-gap count of %d: %+v", policy.AuthFailures-1, first)
		}
		now := gap
		var tr Transition
		for i := range policy.AuthFailures - 1 {
			now = now.Add(time.Second)
			tr = s.RecordAuthFailure(addr, now, policy)
			if i < policy.AuthFailures-2 && tr.QuarantineStarted {
				t.Fatalf("post-gap failure %d/%d crossed the threshold early: %+v", i+2, policy.AuthFailures, tr)
			}
		}
		if !tr.QuarantineStarted {
			t.Fatalf("the fresh post-gap window did not reach the threshold: %+v", tr)
		}
		if tr.Reason != ReasonAuthFailureThreshold {
			t.Fatalf("post-gap threshold reason = %q, want %q", tr.Reason, ReasonAuthFailureThreshold)
		}
		// A fresh level 1 deadline: the pre-gap probe's offense level is gone with
		// the expired entry, so the threshold does not escalate to level 2.
		assertUntil(t, "post-gap threshold", tr, now.Add(policy.InitialQuarantine))
		if got := s.Len(); got != 1 {
			t.Fatalf("Len() = %d, want exactly 1: the expired entry is replaced, not duplicated", got)
		}
	})
}

func TestExactAddressIsolationNeverWidensToAnAggregate(t *testing.T) {
	t.Parallel()

	policy := validPolicy()
	s := mustState(t, validStateLimits())
	hostile := mustAddr(t, "192.0.2.1")

	s.RecordProbe(hostile, stateBase, policy)
	for _, neighbour := range []string{
		"192.0.2.2", "192.0.2.99", "192.0.2.0", "198.51.100.1", "2001:db8::1", "::1",
	} {
		addr := mustAddr(t, neighbour)
		if s.IsQuarantined(addr, stateBase) {
			t.Errorf("%s is quarantined by the offense of 192.0.2.1; state must never widen to a /24, /64, ASN or country", neighbour)
		}
		if got := s.Clear(addr); got != 1 {
			t.Errorf("Clear(%s) entry count = %d, want 1: a neighbour must own no state", neighbour, got)
		}
	}
	if got := s.Len(); got != 1 {
		t.Fatalf("Len() = %d, want exactly 1: one source must create exactly one entry", got)
	}
	if !s.IsQuarantined(hostile, stateBase) {
		t.Fatal("the offending address must stay quarantined")
	}
}

func TestIPv4MappedNormalizationIsExplicitAndNeverMergesDistinctAddresses(t *testing.T) {
	t.Parallel()

	policy := validPolicy()
	s := mustState(t, validStateLimits())
	mapped := mustAddr(t, "::ffff:192.0.2.1")
	plain := mustAddr(t, "192.0.2.1")

	s.RecordProbe(mapped, stateBase, policy)
	if !s.IsQuarantined(plain, stateBase) {
		t.Fatal("the IPv4-mapped and plain forms of one host must share one entry")
	}
	if got := s.Len(); got != 1 {
		t.Fatalf("Len() = %d, want 1: normalization must not create a second entry for one host", got)
	}
	for _, distinct := range []string{"::ffff:198.51.100.1", "2001:db8::1", "192.0.2.2"} {
		if s.IsQuarantined(mustAddr(t, distinct), stateBase) {
			t.Errorf("unmapping merged the distinct address %s into 192.0.2.1 state", distinct)
		}
	}
}

func TestUnrelatedShardWorkDoesNotWaitOnAnotherShardLock(t *testing.T) {
	t.Parallel()

	s := mustState(t, StateLimits{MaxEntries: 64, StateTTL: time.Hour})
	policy := validPolicy()

	held, other := addressesInDifferentShards(t, s, 0)
	s.RecordProbe(held, stateBase, policy)
	if !s.IsQuarantined(held, stateBase) {
		t.Fatal("the address whose shard will be locked must be quarantined")
	}

	// Holding one shard's lock models a long-held request-path lock. A single
	// global lock would block the unrelated address forever.
	s.shards[0].mu.Lock()
	defer s.shards[0].mu.Unlock()

	acquired := make(chan Transition, 1)
	go func() { acquired <- s.RecordProbe(other, stateBase, policy) }()
	select {
	case tr := <-acquired:
		if !tr.QuarantineStarted {
			t.Fatalf("unrelated address was not recorded: %+v", tr)
		}
	case <-time.After(30 * time.Second):
		t.Fatalf("recording %s serialized behind the unrelated lock on %s", other, held)
	}
}

func TestConcurrentDistinctAddressesAllProgress(t *testing.T) {
	t.Parallel()

	s := mustState(t, StateLimits{MaxEntries: 256, StateTTL: time.Hour})
	policy := validPolicy()

	const workers = 32
	addrs := make([]netip.Addr, workers)
	for i := range workers {
		addrs[i] = netip.AddrFrom4([4]byte{203, 0, byte(i >> 8), byte(i)})
	}
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := range workers {
		go func() {
			defer wg.Done()
			<-start
			for range 32 {
				s.RecordProbe(addrs[i], stateBase, policy)
				if !s.IsQuarantined(addrs[i], stateBase) {
					t.Errorf("address %s lost its own quarantine", addrs[i])
					return
				}
			}
		}()
	}
	close(start)
	wg.Wait()
	if got := s.Len(); got > 256 {
		t.Fatalf("Len() = %d, want at most the configured capacity", got)
	}
}

func TestConcurrentSameAddressRecordingLosesNoOffense(t *testing.T) {
	t.Parallel()

	s := mustState(t, validStateLimits())
	policy := validPolicy()
	policy.InitialQuarantine = time.Second
	policy.MaxQuarantine = time.Hour
	addr := mustAddr(t, "203.0.113.24")

	const offenses = 8
	transitions := make([]Transition, offenses)
	var wg sync.WaitGroup
	wg.Add(offenses)
	for i := range offenses {
		go func() {
			defer wg.Done()
			transitions[i] = s.RecordProbe(addr, stateBase, policy)
		}()
	}
	wg.Wait()
	for i, tr := range transitions {
		if !tr.QuarantineStarted {
			t.Fatalf("concurrent probe %d was lost: %+v", i, tr)
		}
	}
	if got := s.Len(); got != 1 {
		t.Fatalf("Len() = %d, want exactly 1 entry for one address", got)
	}

	// `offenses` recorded offenses must have produced level `offenses`, so the
	// next offense escalates to 2^offenses. A lost update shortens the deadline.
	settled := s.RecordProbe(addr, stateBase, policy)
	assertUntil(t, "probe after concurrent offenses", settled, stateBase.Add(time.Duration(1<<offenses)*time.Second))
}

func TestConcurrentAuthFailuresCrossTheThresholdExactlyOnce(t *testing.T) {
	t.Parallel()

	s := mustState(t, validStateLimits())
	policy := validPolicy()
	addr := mustAddr(t, "203.0.113.25")

	failures := policy.AuthFailures
	transitions := make([]Transition, failures)
	var wg sync.WaitGroup
	wg.Add(failures)
	for i := range failures {
		go func() {
			defer wg.Done()
			transitions[i] = s.RecordAuthFailure(addr, stateBase, policy)
		}()
	}
	wg.Wait()
	var crossings int
	for i, tr := range transitions {
		if tr.QuarantineStarted {
			crossings++
		} else if tr.EntryCount != 1 {
			t.Fatalf("failure %d reported EntryCount %d, want 1", i, tr.EntryCount)
		}
	}
	if crossings != 1 {
		t.Fatalf("threshold crossings = %d, want exactly 1: %d counted failures must reach the threshold once", crossings, failures)
	}
	if !s.IsQuarantined(addr, stateBase) {
		t.Fatal("the counted failures must leave the source quarantined")
	}
}

func addressesInDifferentShards(t *testing.T, s *State, heldShard int) (held, other netip.Addr) {
	t.Helper()
	for i := range 4096 {
		candidate := netip.AddrFrom4([4]byte{100, 64, byte(i >> 8), byte(i)})
		if s.shardIndex(candidate) == heldShard {
			held = candidate
			continue
		}
		if held.IsValid() {
			return held, candidate
		}
	}
	t.Fatal("could not find addresses in two different shards")
	return held, other
}

// addressesInSameShard returns count distinct addresses whose deterministic
// address-to-shard mapping selects shard, so a test can drive one shard's ring
// directly instead of depending on how a churn happens to distribute.
func addressesInSameShard(t *testing.T, s *State, shard, count int) []netip.Addr {
	t.Helper()
	found := make([]netip.Addr, 0, count)
	for i := range 4096 {
		candidate := netip.AddrFrom4([4]byte{100, 64, byte(i >> 8), byte(i)})
		if s.shardIndex(candidate) != shard {
			continue
		}
		found = append(found, candidate)
		if len(found) == count {
			return found
		}
	}
	t.Fatalf("could not find %d addresses in shard %d", count, shard)
	return nil
}
