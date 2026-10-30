package metrics

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/usage"
	"github.com/prometheus/client_golang/prometheus"
)

type switchTestIdentity struct {
	core            [32]byte
	cacheTarget     [32]byte
	openCredential  [32]byte
	providerAccount [32]byte
}

func switchDigest(label string) [32]byte {
	return sha256.Sum256([]byte(label))
}

func switchSample(session, leg string, attemptSeq int, launchSeq uint64, identity switchTestIdentity, healthy bool) usage.Event {
	ev := usage.Event{
		SessionID:                session,
		BackendID:                "backend",
		Model:                    "model",
		BLegID:                   leg,
		AttemptSeq:               attemptSeq,
		PromptCacheLaunchSeq:     launchSeq,
		InputTokens:              8000,
		CacheIdentityDigest:      identity.core,
		CacheTargetDigest:        identity.cacheTarget,
		CredentialIdentityDigest: identity.openCredential,
		ProviderAccountDigest:    identity.providerAccount,
	}
	if healthy {
		ev.CacheReadTokens = 8000
	}
	return ev
}

func switchDetector(t *testing.T, logs *bytes.Buffer, now *time.Time) (*PromptCacheCollapse, *prometheus.Registry) {
	t.Helper()
	reg := prometheus.NewRegistry()
	return NewPromptCacheCollapseWithSettings(
		RegisterPromptCacheCollapseProm(reg),
		slog.New(slog.NewJSONHandler(logs, nil)),
		func() time.Time { return *now },
		PromptCacheCollapseSettings{},
	), reg
}

func assertCollapseCause(t *testing.T, logs *bytes.Buffer, reg *prometheus.Registry, want string) {
	t.Helper()
	if got := collapseCount(t, reg); got != 1 {
		t.Fatalf("trips=%v want 1", got)
	}
	if text := logs.String(); !strings.Contains(text, `"cause":"`+want+`"`) {
		t.Fatalf("missing cause %q in %s", want, text)
	}
}

func TestSwitchIdentity_TemporaryTargetChangeRevertingIsNotBlamed(t *testing.T) {
	t.Parallel()
	var logs bytes.Buffer
	now := time.Unix(1000, 0)
	d, reg := switchDetector(t, &logs, &now)
	core := switchDigest("core")
	a, b := switchDigest("target-a"), switchDigest("target-b")
	observeCache(t, d, switchSample("session", "leg-0", 1, 10, switchTestIdentity{core: core, cacheTarget: a}, true))
	now = now.Add(time.Second)
	observeCache(t, d, switchSample("session", "leg-1", 2, 11, switchTestIdentity{core: core, cacheTarget: b}, false))
	for i := range 4 {
		now = now.Add(time.Second)
		observeCache(t, d, switchSample("session", fmt.Sprintf("leg-%d", i+2), i+3, uint64(12+i), switchTestIdentity{core: core, cacheTarget: a}, false))
	}
	assertCollapseCause(t, &logs, reg, string(CollapseCauseUnexplained))
}

func TestSwitchIdentity_TemporaryCredentialChangeRevertingIsNotBlamed(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		id   func([32]byte) switchTestIdentity
	}{
		{"provider_account", func(value [32]byte) switchTestIdentity {
			return switchTestIdentity{core: switchDigest("core"), providerAccount: value}
		}},
		{"open_credential", func(value [32]byte) switchTestIdentity {
			return switchTestIdentity{core: switchDigest("core"), openCredential: value}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var logs bytes.Buffer
			now := time.Unix(1000, 0)
			d, reg := switchDetector(t, &logs, &now)
			a, b := switchDigest("cred-a"), switchDigest("cred-b")
			observeCache(t, d, switchSample("session", "leg-0", 1, 10, tc.id(a), true))
			now = now.Add(time.Second)
			observeCache(t, d, switchSample("session", "leg-1", 2, 11, tc.id(b), false))
			for i := range 4 {
				now = now.Add(time.Second)
				observeCache(t, d, switchSample("session", fmt.Sprintf("leg-%d", i+2), i+3, uint64(12+i), tc.id(a), false))
			}
			assertCollapseCause(t, &logs, reg, string(CollapseCauseUnexplained))
		})
	}
}

func TestSwitchIdentity_DuplicateRejectedLegDoesNotShiftHealthyBaseline(t *testing.T) {
	t.Parallel()
	var logs bytes.Buffer
	now := time.Unix(1000, 0)
	d, reg := switchDetector(t, &logs, &now)
	core := switchDigest("core")
	a, b := switchDigest("target-a"), switchDigest("target-b")
	observeCache(t, d, switchSample("session", "leg-a", 1, 1, switchTestIdentity{core: core, cacheTarget: a}, true))
	now = now.Add(time.Second)
	observeCache(t, d, switchSample("session", "leg-b", 2, 2, switchTestIdentity{core: core, cacheTarget: b}, true))
	now = now.Add(time.Second)
	// A repeated positive usage delta for the already-recorded leg-a is
	// rejected by the detector. It must not change the healthy identity.
	observeCache(t, d, switchSample("session", "leg-a", 1, 1, switchTestIdentity{core: core, cacheTarget: a}, true))
	for i := range 5 {
		now = now.Add(time.Second)
		observeCache(t, d, switchSample("session", fmt.Sprintf("cold-b-%d", i), 3+i, uint64(3+i), switchTestIdentity{core: core, cacheTarget: b}, false))
	}
	assertCollapseCause(t, &logs, reg, string(CollapseCauseUnexplained))
}

func TestSwitchIdentity_DuplicateRejectedLegDoesNotHideARealSwitch(t *testing.T) {
	t.Parallel()
	var logs bytes.Buffer
	now := time.Unix(1000, 0)
	d, reg := switchDetector(t, &logs, &now)
	core := switchDigest("core")
	a, b, c := switchDigest("target-a"), switchDigest("target-b"), switchDigest("target-c")
	observeCache(t, d, switchSample("session", "leg-a", 1, 1, switchTestIdentity{core: core, cacheTarget: a}, true))
	now = now.Add(time.Second)
	observeCache(t, d, switchSample("session", "leg-b", 2, 2, switchTestIdentity{core: core, cacheTarget: b}, true))
	now = now.Add(time.Second)
	observeCache(t, d, switchSample("session", "leg-a", 1, 1, switchTestIdentity{core: core, cacheTarget: a}, true))
	for i := range 5 {
		now = now.Add(time.Second)
		observeCache(t, d, switchSample("session", fmt.Sprintf("cold-c-%d", i), 3+i, uint64(3+i), switchTestIdentity{core: core, cacheTarget: c}, false))
	}
	assertCollapseCause(t, &logs, reg, string(CollapseCauseAffinitySwitch))
}

func TestSwitchIdentity_DuplicateRejectedLegCredentialParity(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		trigger [32]byte
		want    string
	}{
		{"unchanged_account", switchDigest("account-b"), string(CollapseCauseUnexplained)},
		{"new_account", switchDigest("account-c"), string(CollapseCauseCredentialSwitch)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var logs bytes.Buffer
			now := time.Unix(1000, 0)
			d, reg := switchDetector(t, &logs, &now)
			core := switchDigest("core")
			accountA, accountB := switchDigest("account-a"), switchDigest("account-b")
			observeCache(t, d, switchSample("session", "leg-a", 1, 1, switchTestIdentity{core: core, providerAccount: accountA}, true))
			now = now.Add(time.Second)
			observeCache(t, d, switchSample("session", "leg-b", 2, 2, switchTestIdentity{core: core, providerAccount: accountB}, true))
			now = now.Add(time.Second)
			observeCache(t, d, switchSample("session", "leg-a", 1, 1, switchTestIdentity{core: core, providerAccount: accountA}, true))
			for i := range 5 {
				now = now.Add(time.Second)
				observeCache(t, d, switchSample("session", fmt.Sprintf("cold-%d", i), 3+i, uint64(3+i), switchTestIdentity{core: core, providerAccount: tc.trigger}, false))
			}
			assertCollapseCause(t, &logs, reg, tc.want)
		})
	}
}

func TestSwitchIdentity_Precedence(t *testing.T) {
	t.Parallel()
	t.Run("request_note_wins", func(t *testing.T) {
		t.Parallel()
		var logs bytes.Buffer
		now := time.Unix(1000, 0)
		d, reg := switchDetector(t, &logs, &now)
		core := switchDigest("core")
		a, b := switchDigest("target-a"), switchDigest("target-b")
		observeCache(t, d, switchSample("session", "leg-0", 1, 1, switchTestIdentity{core: core, cacheTarget: a}, true))
		d.NoteStructuralCause("session", "backend", "model", CollapseCauseCompaction)
		now = now.Add(time.Second)
		for i := range 5 {
			now = now.Add(time.Second)
			observeCache(t, d, switchSample("session", fmt.Sprintf("cold-%d", i), 2+i, uint64(2+i), switchTestIdentity{core: core, cacheTarget: b}, false))
		}
		assertCollapseCause(t, &logs, reg, string(CollapseCauseCompaction))
	})
	t.Run("config_generation_wins", func(t *testing.T) {
		t.Parallel()
		var logs bytes.Buffer
		now := time.Unix(1000, 0)
		d, reg := switchDetector(t, &logs, &now)
		core := switchDigest("core")
		a, b := switchDigest("target-a"), switchDigest("target-b")
		observeCache(t, d, switchSample("session", "leg-0", 1, 1, switchTestIdentity{core: core, cacheTarget: a}, true))
		d.NoteConfigGeneration()
		now = now.Add(time.Second)
		for i := range 5 {
			now = now.Add(time.Second)
			observeCache(t, d, switchSample("session", fmt.Sprintf("cold-%d", i), 2+i, uint64(2+i), switchTestIdentity{core: core, cacheTarget: b}, false))
		}
		assertCollapseCause(t, &logs, reg, string(CollapseCauseConfigGeneration))
	})
	t.Run("switch_wins_over_keep_warm", func(t *testing.T) {
		t.Parallel()
		var logs bytes.Buffer
		now := time.Unix(1000, 0)
		d, reg := switchDetector(t, &logs, &now)
		core := switchDigest("core")
		a, b := switchDigest("target-a"), switchDigest("target-b")
		observeCache(t, d, switchSample("session", "leg-0", 1, 1, switchTestIdentity{core: core, cacheTarget: a}, true))
		now = now.Add(61 * time.Minute)
		for i := range 5 {
			now = now.Add(time.Second)
			observeCache(t, d, switchSample("session", fmt.Sprintf("cold-%d", i), 2+i, uint64(2+i), switchTestIdentity{core: core, cacheTarget: b}, false))
		}
		assertCollapseCause(t, &logs, reg, string(CollapseCauseAffinitySwitch))
	})
	t.Run("unchanged_identity_keeps_keep_warm", func(t *testing.T) {
		t.Parallel()
		var logs bytes.Buffer
		now := time.Unix(1000, 0)
		d, reg := switchDetector(t, &logs, &now)
		core := switchDigest("core")
		a := switchDigest("target-a")
		observeCache(t, d, switchSample("session", "leg-0", 1, 1, switchTestIdentity{core: core, cacheTarget: a}, true))
		now = now.Add(61 * time.Minute)
		for i := range 5 {
			now = now.Add(time.Second)
			observeCache(t, d, switchSample("session", fmt.Sprintf("cold-%d", i), 2+i, uint64(2+i), switchTestIdentity{core: core, cacheTarget: a}, false))
		}
		assertCollapseCause(t, &logs, reg, string(CollapseCauseKeepWarmExpiry))
	})
}

func TestSwitchIdentity_UnknownAdapterTargetDoesNotFabricate(t *testing.T) {
	t.Parallel()
	core := switchDigest("core")
	a, b := switchDigest("target-a"), switchDigest("target-b")
	for _, tc := range []struct {
		name    string
		healthy [32]byte
		trigger [32]byte
		want    string
	}{
		{"unknown_to_known", [32]byte{}, a, string(CollapseCauseUnexplained)},
		{"known_to_unknown", a, [32]byte{}, string(CollapseCauseUnexplained)},
		{"known_to_known_change", a, b, string(CollapseCauseAffinitySwitch)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var logs bytes.Buffer
			now := time.Unix(1000, 0)
			d, reg := switchDetector(t, &logs, &now)
			observeCache(t, d, switchSample("session", "leg-0", 1, 1, switchTestIdentity{core: core, cacheTarget: tc.healthy}, true))
			for i := range 5 {
				now = now.Add(time.Second)
				observeCache(t, d, switchSample("session", fmt.Sprintf("cold-%d", i), 2+i, uint64(2+i), switchTestIdentity{core: core, cacheTarget: tc.trigger}, false))
			}
			assertCollapseCause(t, &logs, reg, tc.want)
		})
	}
}

func TestSwitchIdentity_CoreChangeIsComparableWithoutAdapterTarget(t *testing.T) {
	t.Parallel()
	var logs bytes.Buffer
	now := time.Unix(1000, 0)
	d, reg := switchDetector(t, &logs, &now)
	coreA, coreB := switchDigest("core-a"), switchDigest("core-b")
	observeCache(t, d, switchSample("session", "leg-0", 1, 1, switchTestIdentity{core: coreA}, true))
	for i := range 5 {
		now = now.Add(time.Second)
		observeCache(t, d, switchSample("session", fmt.Sprintf("cold-%d", i), 2+i, uint64(2+i), switchTestIdentity{core: coreB, cacheTarget: switchDigest("target-b")}, false))
	}
	assertCollapseCause(t, &logs, reg, string(CollapseCauseAffinitySwitch))
}

func TestSwitchIdentity_ProviderAccountRotation(t *testing.T) {
	t.Parallel()
	core := switchDigest("core")
	accountA, accountB := switchDigest("account-a"), switchDigest("account-b")
	for _, tc := range []struct {
		name    string
		healthy switchTestIdentity
		trigger switchTestIdentity
		want    string
	}{
		{
			name:    "no_open_credential_rotates",
			healthy: switchTestIdentity{core: core, providerAccount: accountA},
			trigger: switchTestIdentity{core: core, providerAccount: accountB},
			want:    string(CollapseCauseCredentialSwitch),
		},
		{
			name:    "no_open_credential_unchanged",
			healthy: switchTestIdentity{core: core, providerAccount: accountA},
			trigger: switchTestIdentity{core: core, providerAccount: accountA},
			want:    string(CollapseCauseUnexplained),
		},
		{
			name:    "account_unknown_does_not_fabricate",
			healthy: switchTestIdentity{core: core},
			trigger: switchTestIdentity{core: core, providerAccount: accountB},
			want:    string(CollapseCauseUnexplained),
		},
		{
			name:    "open_credential_and_account_both_present",
			healthy: switchTestIdentity{core: core, openCredential: switchDigest("cred-a"), providerAccount: accountA},
			trigger: switchTestIdentity{core: core, openCredential: switchDigest("cred-a"), providerAccount: accountB},
			want:    string(CollapseCauseCredentialSwitch),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var logs bytes.Buffer
			now := time.Unix(1000, 0)
			d, reg := switchDetector(t, &logs, &now)
			observeCache(t, d, switchSample("session", "leg-0", 1, 1, tc.healthy, true))
			for i := range 5 {
				now = now.Add(time.Second)
				observeCache(t, d, switchSample("session", fmt.Sprintf("cold-%d", i), 2+i, uint64(2+i), tc.trigger, false))
			}
			assertCollapseCause(t, &logs, reg, tc.want)
		})
	}
}

func TestSwitchIdentity_MissingChronologyDoesNotAttribute(t *testing.T) {
	t.Parallel()
	core := switchDigest("core")
	a, b := switchDigest("target-a"), switchDigest("target-b")
	for _, tc := range []struct {
		name       string
		triggerSeq uint64
	}{
		{"missing_ordinal", 0},
		{"older_ordinal", 10},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var logs bytes.Buffer
			now := time.Unix(1000, 0)
			d, reg := switchDetector(t, &logs, &now)
			observeCache(t, d, switchSample("session", "leg-0", 1, 20, switchTestIdentity{core: core, cacheTarget: b}, true))
			for i := range 5 {
				now = now.Add(time.Second)
				observeCache(t, d, switchSample("session", fmt.Sprintf("cold-%d", i), 2+i, tc.triggerSeq, switchTestIdentity{core: core, cacheTarget: a}, false))
			}
			assertCollapseCause(t, &logs, reg, string(CollapseCauseUnexplained))
		})
	}
}

func TestSwitchIdentity_MissingOrdinalHealthySampleDoesNotOverwrite(t *testing.T) {
	t.Parallel()
	core := switchDigest("core")
	a, b := switchDigest("target-a"), switchDigest("target-b")
	t.Run("conflicting_unsequenced_does_not_fabricate", func(t *testing.T) {
		t.Parallel()
		var logs bytes.Buffer
		now := time.Unix(1000, 0)
		d, reg := switchDetector(t, &logs, &now)
		observeCache(t, d, switchSample("session", "leg-b", 1, 20, switchTestIdentity{core: core, cacheTarget: b}, true))
		now = now.Add(time.Second)
		// An unsequenced healthy completion cannot be ordered against the
		// numbered reference. Because it conflicts, it invalidates the
		// comparison instead of replacing the reference or inheriting its
		// ordinal.
		observeCache(t, d, switchSample("session", "leg-a-late", 2, 0, switchTestIdentity{core: core, cacheTarget: a}, true))
		for i := range 5 {
			now = now.Add(time.Second)
			observeCache(t, d, switchSample("session", fmt.Sprintf("cold-b-%d", i), 3+i, uint64(21+i), switchTestIdentity{core: core, cacheTarget: b}, false))
		}
		assertCollapseCause(t, &logs, reg, string(CollapseCauseUnexplained))
	})
	t.Run("conflicting_unsequenced_evidence_is_not_reused", func(t *testing.T) {
		t.Parallel()
		var logs bytes.Buffer
		now := time.Unix(1000, 0)
		d, reg := switchDetector(t, &logs, &now)
		observeCache(t, d, switchSample("session", "leg-b", 1, 20, switchTestIdentity{core: core, cacheTarget: b}, true))
		now = now.Add(time.Second)
		observeCache(t, d, switchSample("session", "leg-a-late", 2, 0, switchTestIdentity{core: core, cacheTarget: a}, true))
		for i := range 5 {
			now = now.Add(time.Second)
			observeCache(t, d, switchSample("session", fmt.Sprintf("cold-a-%d", i), 3+i, uint64(21+i), switchTestIdentity{core: core, cacheTarget: a}, false))
		}
		// The unsequenced A conflicts with B, so the comparison is invalidated;
		// A cannot be attributed from the older B reference.
		assertCollapseCause(t, &logs, reg, string(CollapseCauseUnexplained))
	})
}

// TestSwitchIdentity_UnsequencedHealthyEvidenceInvalidatesReference proves
// R4-F1: a healthy observation without a launch ordinal cannot be ordered
// against a numbered reference, so if it differs (including incomplete
// evidence), the comparison is invalidated. Retaining the old identity would
// let a later cold leg that matches the unsequenced healthy observation be
// falsely attributed to a switch.
func TestSwitchIdentity_UnsequencedHealthyEvidenceInvalidatesReference(t *testing.T) {
	t.Parallel()
	core := switchDigest("core")
	targetA, targetB := switchDigest("target-a"), switchDigest("target-b")
	accountA, accountB := switchDigest("account-a"), switchDigest("account-b")
	for _, tc := range []struct {
		name        string
		healthy     switchTestIdentity
		unsequenced switchTestIdentity
		trigger     switchTestIdentity
	}{
		{
			name:        "cache_target_newer_unsequenced",
			healthy:     switchTestIdentity{core: core, cacheTarget: targetA},
			unsequenced: switchTestIdentity{core: core, cacheTarget: targetB},
			trigger:     switchTestIdentity{core: core, cacheTarget: targetB},
		},
		{
			name:        "provider_account_newer_unsequenced",
			healthy:     switchTestIdentity{core: core, providerAccount: accountA},
			unsequenced: switchTestIdentity{core: core, providerAccount: accountB},
			trigger:     switchTestIdentity{core: core, providerAccount: accountB},
		},
		{
			name:        "incomplete_unsequenced_evidence",
			healthy:     switchTestIdentity{core: core, cacheTarget: targetA, providerAccount: accountA},
			unsequenced: switchTestIdentity{core: core},
			trigger:     switchTestIdentity{core: core, cacheTarget: targetB, providerAccount: accountB},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var logs bytes.Buffer
			now := time.Unix(1000, 0)
			d, reg := switchDetector(t, &logs, &now)
			observeCache(t, d, switchSample("session", "healthy-numbered", 1, 10, tc.healthy, true))
			now = now.Add(time.Second)
			// The unsequenced healthy sample is accepted but cannot be ordered
			// against ordinal 10. It differs, so the comparison must be
			// invalidated rather than retaining the numbered A reference.
			observeCache(t, d, switchSample("session", "healthy-unsequenced", 2, 0, tc.unsequenced, true))
			for i := range 5 {
				now = now.Add(time.Second)
				observeCache(t, d, switchSample("session", fmt.Sprintf("cold-%d", i), 3+i, uint64(11+i), tc.trigger, false))
			}
			assertCollapseCause(t, &logs, reg, string(CollapseCauseUnexplained))
		})
	}
}

// TestSwitchIdentity_UnsequencedConflictChronology pins the invalidation
// boundary: a same-ordinal numbered leg cannot re-establish the invalidated
// reference, while a strictly newer numbered healthy request can.
func TestSwitchIdentity_UnsequencedConflictChronology(t *testing.T) {
	t.Parallel()
	core := switchDigest("core")
	a, b, c := switchDigest("target-a"), switchDigest("target-b"), switchDigest("target-c")
	t.Run("same_ordinal_does_not_restore", func(t *testing.T) {
		t.Parallel()
		var logs bytes.Buffer
		now := time.Unix(1000, 0)
		d, reg := switchDetector(t, &logs, &now)
		observeCache(t, d, switchSample("session", "healthy-a", 1, 10, switchTestIdentity{core: core, cacheTarget: a}, true))
		now = now.Add(time.Second)
		observeCache(t, d, switchSample("session", "healthy-unsequenced", 2, 0, switchTestIdentity{core: core, cacheTarget: b}, true))
		now = now.Add(time.Second)
		// A later leg of the old request confirms A but cannot re-establish the
		// invalidated reference.
		observeCache(t, d, switchSample("session", "healthy-a-same-request", 3, 10, switchTestIdentity{core: core, cacheTarget: a}, true))
		for i := range 5 {
			now = now.Add(time.Second)
			observeCache(t, d, switchSample("session", fmt.Sprintf("cold-b-%d", i), 4+i, uint64(11+i), switchTestIdentity{core: core, cacheTarget: b}, false))
		}
		assertCollapseCause(t, &logs, reg, string(CollapseCauseUnexplained))
	})
	t.Run("late_older_numbered_does_not_restore", func(t *testing.T) {
		t.Parallel()
		var logs bytes.Buffer
		now := time.Unix(1000, 0)
		d, reg := switchDetector(t, &logs, &now)
		observeCache(t, d, switchSample("session", "healthy-a", 1, 10, switchTestIdentity{core: core, cacheTarget: a}, true))
		now = now.Add(time.Second)
		observeCache(t, d, switchSample("session", "healthy-unsequenced", 2, 0, switchTestIdentity{core: core, cacheTarget: b}, true))
		now = now.Add(time.Second)
		// A late older numbered completion cannot clear the ambiguity or
		// re-establish the old A reference.
		observeCache(t, d, switchSample("session", "healthy-a-older", 3, 9, switchTestIdentity{core: core, cacheTarget: a}, true))
		for i := range 5 {
			now = now.Add(time.Second)
			observeCache(t, d, switchSample("session", fmt.Sprintf("cold-b-%d", i), 4+i, uint64(11+i), switchTestIdentity{core: core, cacheTarget: b}, false))
		}
		assertCollapseCause(t, &logs, reg, string(CollapseCauseUnexplained))
	})
	t.Run("strictly_newer_numbered_healthy_restores", func(t *testing.T) {
		t.Parallel()
		var logs bytes.Buffer
		now := time.Unix(1000, 0)
		d, reg := switchDetector(t, &logs, &now)
		observeCache(t, d, switchSample("session", "healthy-a", 1, 10, switchTestIdentity{core: core, cacheTarget: a}, true))
		now = now.Add(time.Second)
		observeCache(t, d, switchSample("session", "healthy-unsequenced", 2, 0, switchTestIdentity{core: core, cacheTarget: b}, true))
		now = now.Add(time.Second)
		// A strictly newer numbered healthy request establishes the fresh B
		// reference; a later cold C is then a real switch.
		observeCache(t, d, switchSample("session", "healthy-b-newer", 3, 11, switchTestIdentity{core: core, cacheTarget: b}, true))
		for i := range 5 {
			now = now.Add(time.Second)
			observeCache(t, d, switchSample("session", fmt.Sprintf("cold-c-%d", i), 4+i, uint64(12+i), switchTestIdentity{core: core, cacheTarget: c}, false))
		}
		assertCollapseCause(t, &logs, reg, string(CollapseCauseAffinitySwitch))
	})
}

func TestSwitchIdentity_SameRequestConflictIsAmbiguous(t *testing.T) {
	t.Parallel()
	core := switchDigest("core")
	a, b := switchDigest("target-a"), switchDigest("target-b")
	for _, tc := range []struct {
		name    string
		trigger [32]byte
	}{
		{"trigger_matches_first_leg", a},
		{"trigger_matches_second_leg", b},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var logs bytes.Buffer
			now := time.Unix(1000, 0)
			d, reg := switchDetector(t, &logs, &now)
			// Two healthy legs of the same request disagree: no cross-request
			// switch reference can be established from that conflict, so neither
			// target may be blamed on a later request.
			observeCache(t, d, switchSample("session", "leg-a", 1, 5, switchTestIdentity{core: core, cacheTarget: a}, true))
			now = now.Add(time.Second)
			observeCache(t, d, switchSample("session", "leg-b", 2, 5, switchTestIdentity{core: core, cacheTarget: b}, true))
			for i := range 5 {
				now = now.Add(time.Second)
				observeCache(t, d, switchSample("session", fmt.Sprintf("cold-%d", i), 3+i, uint64(6+i), switchTestIdentity{core: core, cacheTarget: tc.trigger}, false))
			}
			assertCollapseCause(t, &logs, reg, string(CollapseCauseUnexplained))
		})
	}
}

func TestSwitchIdentity_OutOfOrderHealthySampleDoesNotMoveReference(t *testing.T) {
	t.Parallel()
	var logs bytes.Buffer
	now := time.Unix(1000, 0)
	d, reg := switchDetector(t, &logs, &now)
	core := switchDigest("core")
	a, b, c := switchDigest("target-a"), switchDigest("target-b"), switchDigest("target-c")
	observeCache(t, d, switchSample("session", "leg-a", 1, 1, switchTestIdentity{core: core, cacheTarget: a}, true))
	now = now.Add(time.Second)
	observeCache(t, d, switchSample("session", "leg-b", 2, 2, switchTestIdentity{core: core, cacheTarget: b}, true))
	now = now.Add(time.Second)
	// A late older healthy completion must not move the reference back to A.
	observeCache(t, d, switchSample("session", "leg-a-late", 3, 1, switchTestIdentity{core: core, cacheTarget: a}, true))
	for i := range 5 {
		now = now.Add(time.Second)
		observeCache(t, d, switchSample("session", fmt.Sprintf("cold-b-%d", i), 4+i, uint64(10+i), switchTestIdentity{core: core, cacheTarget: b}, false))
	}
	assertCollapseCause(t, &logs, reg, string(CollapseCauseUnexplained))

	var logs2 bytes.Buffer
	now2 := time.Unix(2000, 0)
	d2, reg2 := switchDetector(t, &logs2, &now2)
	observeCache(t, d2, switchSample("session", "leg-a", 1, 1, switchTestIdentity{core: core, cacheTarget: a}, true))
	now2 = now2.Add(time.Second)
	observeCache(t, d2, switchSample("session", "leg-b", 2, 2, switchTestIdentity{core: core, cacheTarget: b}, true))
	now2 = now2.Add(time.Second)
	observeCache(t, d2, switchSample("session", "leg-a-late", 3, 1, switchTestIdentity{core: core, cacheTarget: a}, true))
	for i := range 5 {
		now2 = now2.Add(time.Second)
		observeCache(t, d2, switchSample("session", fmt.Sprintf("cold-c-%d", i), 4+i, uint64(10+i), switchTestIdentity{core: core, cacheTarget: c}, false))
	}
	assertCollapseCause(t, &logs2, reg2, string(CollapseCauseAffinitySwitch))
}

func TestSwitchIdentity_BothChangedAttributesAffinity(t *testing.T) {
	t.Parallel()
	var logs bytes.Buffer
	now := time.Unix(1000, 0)
	d, reg := switchDetector(t, &logs, &now)
	core := switchDigest("core")
	a, b := switchDigest("target-a"), switchDigest("target-b")
	accountA, accountB := switchDigest("account-a"), switchDigest("account-b")
	observeCache(t, d, switchSample("session", "leg-0", 1, 1, switchTestIdentity{core: core, cacheTarget: a, providerAccount: accountA}, true))
	for i := range 5 {
		now = now.Add(time.Second)
		observeCache(t, d, switchSample("session", fmt.Sprintf("cold-%d", i), 2+i, uint64(2+i), switchTestIdentity{core: core, cacheTarget: b, providerAccount: accountB}, false))
	}
	assertCollapseCause(t, &logs, reg, string(CollapseCauseAffinitySwitch))
}

// TestSwitchIdentity_DuplicateLegWithDifferentEvidenceIsRejected proves the
// first-accepted-B-leg boundary itself rejects a duplicate even when the
// duplicate carries different target/account evidence and a newer-looking
// ordinal. Without deduplication the duplicate would replace the healthy
// reference and make the later cold leg look like a switch.
func TestSwitchIdentity_DuplicateLegWithDifferentEvidenceIsRejected(t *testing.T) {
	t.Parallel()
	core := switchDigest("core")
	accountA, accountB, accountC := switchDigest("account-a"), switchDigest("account-b"), switchDigest("account-c")
	for _, tc := range []struct {
		name      string
		healthy   switchTestIdentity
		duplicate switchTestIdentity
		trigger   switchTestIdentity
	}{
		{
			name:      "target",
			healthy:   switchTestIdentity{core: core, cacheTarget: switchDigest("target-b")},
			duplicate: switchTestIdentity{core: core, cacheTarget: switchDigest("target-c")},
			trigger:   switchTestIdentity{core: core, cacheTarget: switchDigest("target-b")},
		},
		{
			name:      "provider_account",
			healthy:   switchTestIdentity{core: core, providerAccount: accountB},
			duplicate: switchTestIdentity{core: core, providerAccount: accountC},
			trigger:   switchTestIdentity{core: core, providerAccount: accountB},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var logs bytes.Buffer
			now := time.Unix(1000, 0)
			d, reg := switchDetector(t, &logs, &now)
			// leg-a is accepted first, then leg-b establishes the healthy
			// reference. The repeated leg-a is rejected before any state
			// mutation even though its evidence differs.
			healthyA := tc.healthy
			if tc.name == "provider_account" {
				healthyA.providerAccount = accountA
			} else {
				healthyA.cacheTarget = switchDigest("target-a")
			}
			observeCache(t, d, switchSample("session", "leg-a", 1, 1, healthyA, true))
			now = now.Add(time.Second)
			observeCache(t, d, switchSample("session", "leg-b", 2, 2, tc.healthy, true))
			now = now.Add(time.Second)
			observeCache(t, d, switchSample("session", "leg-a", 3, 3, tc.duplicate, true))
			for i := range 5 {
				now = now.Add(time.Second)
				observeCache(t, d, switchSample("session", fmt.Sprintf("cold-%d", i), 4+i, uint64(4+i), tc.trigger, false))
			}
			assertCollapseCause(t, &logs, reg, string(CollapseCauseUnexplained))
		})
	}
}
