package runtimehost

// Task 6.4: direct white-box tests against the unexported ReloadState. These
// instantiate ReloadState directly with NO Manager, Source, Loader, Compiler,
// AttemptRunner, AttemptGate, Coordinator, or ReloadObserver (req 6.3-6.4,
// 7.1-7.8). Concurrency tests use barriers/channels, never wall-clock sleeps.

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/configreload"
	"github.com/aiproxer/aiproxer/internal/infra/configsource"
	sdkreload "github.com/aiproxer/aiproxer/pkg/aipsdk/configreload"
)

func stateEffective(fp string, digest byte) *config.EffectiveConfig {
	var d [32]byte
	d[0] = digest
	return &config.EffectiveConfig{
		Config: &config.Config{},
		Identity: config.EffectiveIdentity{
			PrivateDigest:     d,
			PublicFingerprint: fp,
		},
		LoadedAt: time.Now().UTC(),
	}
}

func stateActiveSource(opaque byte) *configsource.ActiveSourceVersion {
	return &configsource.ActiveSourceVersion{
		HandleIdentity: configsource.FileIdentity{Platform: "windows", Scheme: "win-fileid", Opaque: [32]byte{opaque}},
		PrivateDigest:  [32]byte{opaque},
	}
}

// 1. Initial empty and active state.

func TestReloadState_InitialEmptyState(t *testing.T) {
	t.Parallel()
	s := newReloadState(reloadStateInitial{})
	st := s.Snapshot(reloadStatusInput{ActiveGeneration: 0})
	if st.LastResult.Category != "" || st.LastSuccess.Category != "" || st.LastFailure.Category != "" {
		t.Fatalf("expected empty last/success/failure, got %+v", st)
	}
	if st.SourceIntegrity != "ok" {
		t.Fatalf("SourceIntegrity=%q want ok", st.SourceIntegrity)
	}
	if st.ModelGeneration != "" {
		t.Fatalf("ModelGeneration=%q want empty", st.ModelGeneration)
	}
	if len(st.History) != 0 {
		t.Fatalf("expected no invented history, got %v", st.History)
	}
	if st.ControlDegraded {
		t.Fatal("empty state must not be control degraded")
	}
}

func TestReloadState_InitialActiveState(t *testing.T) {
	t.Parallel()
	eff := stateEffective("fp-boot", 1)
	src := stateActiveSource(2)
	s := newReloadState(reloadStateInitial{
		ActiveEffective: eff,
		ActiveSource:    src,
		InitialResult:   sdkreload.Result{Category: sdkreload.ResultPublished, ActiveGeneration: 1},
		ModelGeneration: "fp-boot",
	})
	st := s.Snapshot(reloadStatusInput{ActiveGeneration: 1})
	if st.LastResult.Category != sdkreload.ResultPublished || st.LastResult.ActiveGeneration != 1 {
		t.Fatalf("LastResult=%+v", st.LastResult)
	}
	if st.LastSuccess.Category != sdkreload.ResultPublished {
		t.Fatalf("LastSuccess=%+v", st.LastSuccess)
	}
	if st.ModelGeneration != "fp-boot" {
		t.Fatalf("ModelGeneration=%q", st.ModelGeneration)
	}
	// Observable completion: initial active generation does not invent a
	// reload history attempt.
	if len(st.History) != 0 {
		t.Fatalf("expected no invented history at construction, got %v", st.History)
	}

	in := s.ActiveInput(sdkreload.Trigger{Kind: sdkreload.TriggerAPI}, 1, 1)
	if in.ActiveEffective != eff {
		t.Fatal("expected initial active effective to be visible via ActiveInput")
	}
	if in.ActiveSource == nil || in.ActiveSource.PrivateDigest != src.PrivateDigest {
		t.Fatalf("ActiveSource=%+v", in.ActiveSource)
	}
}

// 2. ActiveInput attempt/trigger/generation + cloned active source.

func TestReloadState_ActiveInputClonesSource(t *testing.T) {
	t.Parallel()
	src := stateActiveSource(9)
	s := newReloadState(reloadStateInitial{ActiveSource: src})

	in := s.ActiveInput(sdkreload.Trigger{Kind: sdkreload.TriggerSIGHUP}, 42, 7)
	if in.AttemptID != 42 || in.ActiveGeneration != 7 || in.Trigger.Kind != sdkreload.TriggerSIGHUP {
		t.Fatalf("attemptInput=%+v", in)
	}
	if in.ActiveSource == src {
		t.Fatal("ActiveInput must clone ActiveSource, not alias it")
	}
	if in.ActiveSource == nil || *in.ActiveSource != *src {
		t.Fatalf("cloned source mismatch: %+v want %+v", in.ActiveSource, src)
	}

	// Mutating the returned clone must not affect a subsequent snapshot.
	in.ActiveSource.PrivateDigest[0] = 0xFF
	in2 := s.ActiveInput(sdkreload.Trigger{Kind: sdkreload.TriggerSIGHUP}, 43, 7)
	if in2.ActiveSource.PrivateDigest[0] != 9 {
		t.Fatalf("mutation of returned ActiveSource leaked into state: %v", in2.ActiveSource.PrivateDigest[0])
	}
}

// 3. Publish Apply updates effective/source, last success, posture, model
// fingerprint, history.

func TestReloadState_ApplyPublishUpdatesActiveAndHistory(t *testing.T) {
	t.Parallel()
	s := newReloadState(reloadStateInitial{
		ActiveEffective: stateEffective("fp-old", 1),
		ActiveSource:    stateActiveSource(1),
	})
	newEff := stateEffective("fp-new", 2)
	newSrc := stateActiveSource(2)
	res := s.Apply(attemptOutcome{
		Result:          sdkreload.Result{Category: sdkreload.ResultPublished, AttemptID: 1, ActiveGeneration: 2},
		EffectiveUpdate: newEff,
		SourceUpdate:    newSrc,
	}, reloadTerminalMeta{
		Trigger:    sdkreload.Trigger{Kind: sdkreload.TriggerAPI, SafeActor: "api-actor"},
		Duration:   250 * time.Millisecond,
		RecordedAt: time.Unix(100, 0).UTC(),
	})
	if res.Category != sdkreload.ResultPublished {
		t.Fatalf("Apply result=%+v", res)
	}

	in := s.ActiveInput(sdkreload.Trigger{}, 2, 2)
	if in.ActiveEffective != newEff {
		t.Fatal("expected active effective updated to published EffectiveUpdate")
	}
	if in.ActiveSource == nil || in.ActiveSource.PrivateDigest != newSrc.PrivateDigest {
		t.Fatalf("active source not updated: %+v", in.ActiveSource)
	}

	st := s.Snapshot(reloadStatusInput{ActiveGeneration: 2})
	if st.LastSuccess.Category != sdkreload.ResultPublished || st.LastSuccess.AttemptID != 1 {
		t.Fatalf("LastSuccess=%+v", st.LastSuccess)
	}
	if st.SourceIntegrity != "ok" {
		t.Fatalf("SourceIntegrity=%q", st.SourceIntegrity)
	}
	if st.ModelGeneration != "fp-new" {
		t.Fatalf("ModelGeneration=%q want fp-new", st.ModelGeneration)
	}
	if len(st.History) != 1 {
		t.Fatalf("history len=%d want 1: %v", len(st.History), st.History)
	}
	e := st.History[0]
	if e.AttemptID != 1 || e.Trigger != sdkreload.TriggerAPI || e.Category != sdkreload.ResultPublished {
		t.Fatalf("history entry=%+v", e)
	}
	if e.ActiveGeneration != 2 || e.CandidateGeneration != 2 {
		t.Fatalf("history generations=%+v", e)
	}
	if e.DurationMs != 250 {
		t.Fatalf("DurationMs=%d want 250", e.DurationMs)
	}
	if e.SafeActor != "api-actor" {
		t.Fatalf("SafeActor=%q", e.SafeActor)
	}
	if !e.RecordedAt.Equal(time.Unix(100, 0).UTC()) {
		t.Fatalf("RecordedAt=%v", e.RecordedAt)
	}
}

func TestReloadState_ApplyPublishEmptyFingerprintPreservesPriorModelGen(t *testing.T) {
	t.Parallel()
	s := newReloadState(reloadStateInitial{ModelGeneration: "fp-prior"})
	emptyFPEff := &config.EffectiveConfig{Config: &config.Config{}, Identity: config.EffectiveIdentity{}}
	s.Apply(attemptOutcome{
		Result:          sdkreload.Result{Category: sdkreload.ResultPublished, ActiveGeneration: 2},
		EffectiveUpdate: emptyFPEff,
	}, reloadTerminalMeta{})
	st := s.Snapshot(reloadStatusInput{})
	if st.ModelGeneration != "fp-prior" {
		t.Fatalf("ModelGeneration=%q want preserved fp-prior", st.ModelGeneration)
	}
}

// 4. Source-only effective no-op update.

func TestReloadState_ApplyEffectiveNoopUpdatesSourceOnly(t *testing.T) {
	t.Parallel()
	eff := stateEffective("fp-same", 3)
	s := newReloadState(reloadStateInitial{
		ActiveEffective: eff,
		ActiveSource:    stateActiveSource(1),
	})
	newSrc := stateActiveSource(5)
	s.Apply(attemptOutcome{
		Result:       sdkreload.Result{Category: sdkreload.ResultNoop, AttemptID: 2, ActiveGeneration: 1},
		SourceUpdate: newSrc,
	}, reloadTerminalMeta{Trigger: sdkreload.Trigger{Kind: sdkreload.TriggerAPI}})

	in := s.ActiveInput(sdkreload.Trigger{}, 3, 1)
	if in.ActiveEffective != eff {
		t.Fatal("effective-identity no-op must not change active effective")
	}
	if in.ActiveSource == nil || in.ActiveSource.PrivateDigest != newSrc.PrivateDigest {
		t.Fatalf("expected source baseline advanced: %+v", in.ActiveSource)
	}
	st := s.Snapshot(reloadStatusInput{})
	if st.LastFailure.Category != sdkreload.ResultNoop {
		t.Fatalf("LastFailure=%+v want noop", st.LastFailure)
	}
	if st.SourceIntegrity != "ok" {
		t.Fatalf("SourceIntegrity=%q want ok", st.SourceIntegrity)
	}
}

func TestReloadState_EffectiveNoopReplayPreservesAdoptedOwner(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "linux" {
		t.Skip("retained source owners are Linux-only")
	}
	version, owner := loadLeaseTestBaseline(t, "accepted")
	if version.HandleIdentity.Scheme != "linux-ext4-dev-ino-lease-v1" {
		_ = owner.Close(context.Background())
		t.Skip("test filesystem does not provide ext4 lease evidence")
	}
	state := newReloadState(reloadStateInitial{})
	defer func() { _ = state.closeActiveSource(context.Background()) }()
	outcome := attemptOutcome{
		Result:       sdkreload.Result{Category: sdkreload.ResultNoop, AttemptID: 1},
		SourceUpdate: version, SourceOwnerSlot: owner,
	}
	first := state.Apply(outcome, reloadTerminalMeta{})
	second := state.Apply(outcome, reloadTerminalMeta{})
	if first.Category != sdkreload.ResultNoop || second.Category != sdkreload.ResultNoop {
		t.Fatalf("effective-noop results first=%q second=%q", first.Category, second.Category)
	}
	borrow, err := version.Borrow()
	if err != nil {
		t.Fatalf("replayed outcome closed adopted baseline: %v", err)
	}
	borrow.Release()
}

func TestReloadState_MismatchedSourceOwnerPairDoesNotMutateState(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "linux" {
		t.Skip("retained source owners are Linux-only")
	}
	versionA, ownerA := loadLeaseTestBaseline(t, "source-a")
	versionB, ownerB := loadLeaseTestBaseline(t, "source-b")
	if versionA.HandleIdentity.Scheme != "linux-ext4-dev-ino-lease-v1" || versionB.HandleIdentity.Scheme != "linux-ext4-dev-ino-lease-v1" {
		_ = ownerA.Close(context.Background())
		_ = ownerB.Close(context.Background())
		t.Skip("test filesystem does not provide ext4 lease evidence")
	}
	defer func() {
		_ = ownerA.Close(context.Background())
		_ = ownerB.Close(context.Background())
	}()
	state := newReloadState(reloadStateInitial{})
	res := state.Apply(attemptOutcome{
		Result:       sdkreload.Result{Category: sdkreload.ResultNoop, AttemptID: 2},
		SourceUpdate: versionB, SourceOwnerSlot: ownerA,
	}, reloadTerminalMeta{})
	if res.Category != sdkreload.ResultInternalFailed {
		t.Fatalf("mismatched owner/source pair category=%q want internal-failed", res.Category)
	}
	if got := state.ActiveInput(sdkreload.Trigger{}, 3, 0).ActiveSource; got != nil {
		t.Fatalf("invalid pair mutated active source: %+v", got)
	}
	if ownerA.IsEmpty() || ownerB.IsEmpty() {
		t.Fatal("invalid pairing must leave both source owners unmoved")
	}
}

func loadLeaseTestBaseline(t *testing.T, body string) (*configsource.ActiveSourceVersion, *configsource.SourceOwnerSlot) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	source, err := configsource.NewFixedSource(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, _, err := source.ReadStable(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot.TakeBaseline()
}

// 5. Atomic no-op without source update.

func TestReloadState_ApplyAtomicNoopNoActiveUpdateStillRecordsHistory(t *testing.T) {
	t.Parallel()
	eff := stateEffective("fp-x", 1)
	src := stateActiveSource(1)
	s := newReloadState(reloadStateInitial{ActiveEffective: eff, ActiveSource: src})

	s.Apply(attemptOutcome{
		Result: sdkreload.Result{Category: sdkreload.ResultNoop, AttemptID: 4, ActiveGeneration: 1},
	}, reloadTerminalMeta{Trigger: sdkreload.Trigger{Kind: sdkreload.TriggerAPI}})

	in := s.ActiveInput(sdkreload.Trigger{}, 5, 1)
	if in.ActiveEffective != eff {
		t.Fatal("atomic no-op must not change active effective")
	}
	if in.ActiveSource == nil || in.ActiveSource.PrivateDigest != src.PrivateDigest {
		t.Fatalf("atomic no-op must not change active source: %+v", in.ActiveSource)
	}
	st := s.Snapshot(reloadStatusInput{})
	if len(st.History) != 1 || st.History[0].Category != sdkreload.ResultNoop {
		t.Fatalf("expected recorded no-op history entry, got %v", st.History)
	}
	if st.LastFailure.Category != sdkreload.ResultNoop {
		t.Fatalf("LastFailure=%+v", st.LastFailure)
	}
}

// 6. Every failure class, especially source integrity.

func TestReloadState_ApplyFailureClasses(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name         string
		category     sdkreload.ResultCategory
		wantPosture  string
		posturePrior string
	}{
		{name: "source_integrity", category: sdkreload.ResultSourceIntegrity, wantPosture: "failed"},
		{name: "invalid", category: sdkreload.ResultInvalid, wantPosture: "ok", posturePrior: "ok"},
		{name: "restart_required", category: sdkreload.ResultRestartRequired, wantPosture: "ok", posturePrior: "ok"},
		{name: "preparation_failed", category: sdkreload.ResultPreparationFailed, wantPosture: "ok", posturePrior: "ok"},
		{name: "retention_blocked", category: sdkreload.ResultRetentionBlocked, wantPosture: "ok", posturePrior: "ok"},
		{name: "canceled", category: sdkreload.ResultCanceled, wantPosture: "ok", posturePrior: "ok"},
		{name: "internal_failed", category: sdkreload.ResultInternalFailed, wantPosture: "ok", posturePrior: "ok"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			eff := stateEffective("fp-keep", 1)
			src := stateActiveSource(1)
			s := newReloadState(reloadStateInitial{ActiveEffective: eff, ActiveSource: src})
			s.Apply(attemptOutcome{
				Result: sdkreload.Result{Category: tc.category, AttemptID: 9, ActiveGeneration: 1},
			}, reloadTerminalMeta{Trigger: sdkreload.Trigger{Kind: sdkreload.TriggerAPI}})

			in := s.ActiveInput(sdkreload.Trigger{}, 10, 1)
			if in.ActiveEffective != eff || in.ActiveSource.PrivateDigest != src.PrivateDigest {
				t.Fatal("failure classes must not mutate active effective/source")
			}
			st := s.Snapshot(reloadStatusInput{})
			if st.LastFailure.Category != tc.category {
				t.Fatalf("LastFailure=%+v want %q", st.LastFailure, tc.category)
			}
			if st.SourceIntegrity != tc.wantPosture {
				t.Fatalf("SourceIntegrity=%q want %q", st.SourceIntegrity, tc.wantPosture)
			}
		})
	}
}

// 7. Unknown category normalization.

func TestReloadState_ApplyUnknownCategoryNormalizesToInternalFailed(t *testing.T) {
	t.Parallel()
	s := newReloadState(reloadStateInitial{})
	res := s.Apply(attemptOutcome{
		Result: sdkreload.Result{Category: sdkreload.ResultCategory("totally-unknown"), AttemptID: 1, ActiveGeneration: 1},
	}, reloadTerminalMeta{Trigger: sdkreload.Trigger{Kind: sdkreload.TriggerAPI}})
	if res.Category != sdkreload.ResultInternalFailed {
		t.Fatalf("normalized category=%q want internal-failed", res.Category)
	}
	st := s.Snapshot(reloadStatusInput{})
	if st.LastFailure.Category != sdkreload.ResultInternalFailed {
		t.Fatalf("LastFailure=%+v", st.LastFailure)
	}
	if len(st.History) != 1 || st.History[0].Category != sdkreload.ResultInternalFailed {
		t.Fatalf("history=%v", st.History)
	}
}

func TestReloadState_ApplyEmptyCategoryPreservesUnset(t *testing.T) {
	t.Parallel()
	s := newReloadState(reloadStateInitial{})
	res := s.Apply(attemptOutcome{Result: sdkreload.Result{AttemptID: 1}}, reloadTerminalMeta{})
	if res.Category != "" {
		t.Fatalf("empty category must not be normalized: %q", res.Category)
	}
}

// seedTerminalState seeds active/terminal/history primitives so non-terminal
// Apply outcomes can prove they mutate nothing.
func seedTerminalState(t *testing.T) *ReloadState {
	t.Helper()
	eff := stateEffective("fp-seed", 3)
	src := stateActiveSource(3)
	s := newReloadState(reloadStateInitial{
		ActiveEffective: eff,
		ActiveSource:    src,
		InitialResult:   sdkreload.Result{Category: sdkreload.ResultPublished, AttemptID: 1, ActiveGeneration: 1},
		ModelGeneration: "fp-seed",
		HistoryCapacity: 8,
	})
	s.Apply(attemptOutcome{
		Result: sdkreload.Result{
			Category:         sdkreload.ResultRestartRequired,
			AttemptID:        2,
			ActiveGeneration: 1,
			ReasonCategory:   "classify",
			RestartFields:    []string{"server.address"},
		},
	}, reloadTerminalMeta{
		Trigger:    sdkreload.Trigger{Kind: sdkreload.TriggerAPI, SafeActor: "seed-actor"},
		Duration:   5 * time.Millisecond,
		RecordedAt: time.Unix(50, 0).UTC(),
	})
	return s
}

func captureStateFingerprint(t *testing.T, s *ReloadState) sdkreload.Status {
	t.Helper()
	return s.Snapshot(reloadStatusInput{
		ActiveGeneration:    1,
		Busy:                true,
		PendingSignal:       true,
		CoalescedSignals:    2,
		RetainedGenerations: 1,
		RetentionPressure:   true,
	})
}

func assertStateUnchanged(t *testing.T, before, after sdkreload.Status, s *ReloadState, seededEff *config.EffectiveConfig, seededDigest [32]byte) {
	t.Helper()
	if after.LastResult.Category != before.LastResult.Category || after.LastResult.AttemptID != before.LastResult.AttemptID {
		t.Fatalf("LastResult mutated: before=%+v after=%+v", before.LastResult, after.LastResult)
	}
	if after.LastSuccess.Category != before.LastSuccess.Category || after.LastSuccess.AttemptID != before.LastSuccess.AttemptID {
		t.Fatalf("LastSuccess mutated: before=%+v after=%+v", before.LastSuccess, after.LastSuccess)
	}
	if after.LastFailure.Category != before.LastFailure.Category || after.LastFailure.AttemptID != before.LastFailure.AttemptID {
		t.Fatalf("LastFailure mutated: before=%+v after=%+v", before.LastFailure, after.LastFailure)
	}
	if after.SourceIntegrity != before.SourceIntegrity {
		t.Fatalf("SourceIntegrity mutated: %q -> %q", before.SourceIntegrity, after.SourceIntegrity)
	}
	if after.ModelGeneration != before.ModelGeneration {
		t.Fatalf("ModelGeneration mutated: %q -> %q", before.ModelGeneration, after.ModelGeneration)
	}
	if len(after.History) != len(before.History) {
		t.Fatalf("history length mutated: %d -> %d", len(before.History), len(after.History))
	}
	for i := range before.History {
		if after.History[i] != before.History[i] {
			t.Fatalf("history[%d] mutated: before=%+v after=%+v", i, before.History[i], after.History[i])
		}
	}
	if after.CurrentAttempt == nil || before.CurrentAttempt == nil ||
		after.CurrentAttempt.AttemptID != before.CurrentAttempt.AttemptID ||
		after.CurrentAttempt.Category != before.CurrentAttempt.Category {
		t.Fatalf("CurrentAttempt mutated: before=%+v after=%+v", before.CurrentAttempt, after.CurrentAttempt)
	}
	in := s.ActiveInput(sdkreload.Trigger{}, 99, 1)
	if in.ActiveEffective != seededEff {
		t.Fatal("active effective pointer mutated by non-terminal Apply")
	}
	if in.ActiveSource == nil || in.ActiveSource.PrivateDigest != seededDigest {
		t.Fatalf("active source mutated: %+v", in.ActiveSource)
	}
}

func TestReloadState_ApplyBusyDoesNotMutateStateOrHistory(t *testing.T) {
	t.Parallel()
	s := seedTerminalState(t)
	seededEff := s.ActiveInput(sdkreload.Trigger{}, 0, 1).ActiveEffective
	seededSrc := s.ActiveInput(sdkreload.Trigger{}, 0, 1).ActiveSource
	before := captureStateFingerprint(t, s)

	callerFields := []string{"hostile.field"}
	got := s.Apply(attemptOutcome{Result: sdkreload.Result{
		Category:       sdkreload.ResultBusy,
		AttemptID:      99,
		ReasonCategory: configreload.StageBusy,
		RestartFields:  callerFields,
	}}, reloadTerminalMeta{Trigger: sdkreload.Trigger{Kind: sdkreload.TriggerAPI, SafeActor: "busy-actor"}})
	if got.Category != sdkreload.ResultBusy {
		t.Fatalf("Busy Apply must return Busy unchanged, got %q", got.Category)
	}
	if got.AttemptID != 99 {
		t.Fatalf("Busy Apply must return defensive clone of outcome, AttemptID=%d", got.AttemptID)
	}
	callerFields[0] = "mutated-caller"
	got.RestartFields[0] = "mutated-return"
	got.Category = sdkreload.ResultPublished

	after := captureStateFingerprint(t, s)
	assertStateUnchanged(t, before, after, s, seededEff, seededSrc.PrivateDigest)
	if after.LastResult.Category == sdkreload.ResultBusy {
		t.Fatal("Busy must not become LastResult/terminal state")
	}
	if len(after.History) > 0 {
		for _, e := range after.History {
			if e.Category == sdkreload.ResultBusy {
				t.Fatal("Busy must not append a history entry")
			}
		}
	}
}

func TestReloadState_ApplyEmptyCategoryDoesNotMutateStateOrHistory(t *testing.T) {
	t.Parallel()
	s := seedTerminalState(t)
	seededEff := s.ActiveInput(sdkreload.Trigger{}, 0, 1).ActiveEffective
	seededSrc := s.ActiveInput(sdkreload.Trigger{}, 0, 1).ActiveSource
	before := captureStateFingerprint(t, s)

	callerFields := []string{"hostile.field"}
	got := s.Apply(attemptOutcome{Result: sdkreload.Result{
		AttemptID:     77,
		RestartFields: callerFields,
	}}, reloadTerminalMeta{Trigger: sdkreload.Trigger{Kind: sdkreload.TriggerAPI}})
	if got.Category != "" {
		t.Fatalf("empty category must stay unset, got %q", got.Category)
	}
	callerFields[0] = "mutated-caller"
	if len(got.RestartFields) > 0 {
		got.RestartFields[0] = "mutated-return"
	}

	after := captureStateFingerprint(t, s)
	assertStateUnchanged(t, before, after, s, seededEff, seededSrc.PrivateDigest)
	if after.LastResult.Category == "" && after.LastResult.AttemptID == 77 {
		t.Fatal("empty category must not replace LastResult")
	}
	if after.LastFailure.AttemptID == 77 && after.LastFailure.Category == "" {
		t.Fatal("empty category must not become LastFailure")
	}
	for _, e := range after.History {
		if e.AttemptID == 77 && e.Category == "" {
			t.Fatal("empty category must not append an unset history event")
		}
	}
}

// History sanitization parity with prior configreload.StatusHistory policy:
// safe actor → RedactedPlaceholder; stage/reason → "other"; 64-byte bound.
func TestReloadState_HistorySanitizationPolicyParity(t *testing.T) {
	t.Parallel()

	const (
		fakeSecretActor  = "password-placeholder-actor"
		fakeSecretReason = "token-placeholder-reason"
	)

	t.Run("actor_trim_and_bound", func(t *testing.T) {
		t.Parallel()
		s := newReloadState(reloadStateInitial{HistoryCapacity: 4})
		padded := "  actor-ok  "
		s.Apply(attemptOutcome{Result: sdkreload.Result{Category: sdkreload.ResultNoop, AttemptID: 1}},
			reloadTerminalMeta{Trigger: sdkreload.Trigger{SafeActor: padded}})
		st := s.Snapshot(reloadStatusInput{})
		if st.History[0].SafeActor != "actor-ok" {
			t.Fatalf("SafeActor trim failed: %q", st.History[0].SafeActor)
		}
		long := strings.Repeat("a", 80)
		s.Apply(attemptOutcome{Result: sdkreload.Result{Category: sdkreload.ResultNoop, AttemptID: 2}},
			reloadTerminalMeta{Trigger: sdkreload.Trigger{SafeActor: long}})
		st = s.Snapshot(reloadStatusInput{})
		if got := st.History[1].SafeActor; len(got) != 64 || got != long[:64] {
			t.Fatalf("SafeActor bound want 64-byte prefix, got len=%d", len(got))
		}
	})

	t.Run("actor_secret_looking_redacted", func(t *testing.T) {
		t.Parallel()
		s := newReloadState(reloadStateInitial{HistoryCapacity: 2})
		s.Apply(attemptOutcome{Result: sdkreload.Result{Category: sdkreload.ResultNoop, AttemptID: 1}},
			reloadTerminalMeta{Trigger: sdkreload.Trigger{SafeActor: fakeSecretActor}})
		st := s.Snapshot(reloadStatusInput{})
		if st.History[0].SafeActor != configreload.RedactedPlaceholder {
			t.Fatalf("secret-looking SafeActor must become %q, got %q", configreload.RedactedPlaceholder, st.History[0].SafeActor)
		}
	})

	t.Run("reason_trim_bound_and_secret_other", func(t *testing.T) {
		t.Parallel()
		s := newReloadState(reloadStateInitial{HistoryCapacity: 4})
		s.Apply(attemptOutcome{Result: sdkreload.Result{
			Category: sdkreload.ResultNoop, AttemptID: 1, ReasonCategory: "  classify  ",
		}}, reloadTerminalMeta{})
		st := s.Snapshot(reloadStatusInput{})
		if st.History[0].ReasonCategory != "classify" {
			t.Fatalf("ReasonCategory trim failed: %q", st.History[0].ReasonCategory)
		}
		long := strings.Repeat("r", 80)
		s.Apply(attemptOutcome{Result: sdkreload.Result{
			Category: sdkreload.ResultNoop, AttemptID: 2, ReasonCategory: long,
		}}, reloadTerminalMeta{})
		st = s.Snapshot(reloadStatusInput{})
		if got := st.History[1].ReasonCategory; len(got) != 64 || got != long[:64] {
			t.Fatalf("ReasonCategory bound want 64-byte prefix, got len=%d", len(got))
		}
		s.Apply(attemptOutcome{Result: sdkreload.Result{
			Category: sdkreload.ResultNoop, AttemptID: 3, ReasonCategory: fakeSecretReason,
		}}, reloadTerminalMeta{})
		st = s.Snapshot(reloadStatusInput{})
		if st.History[2].ReasonCategory != "other" {
			t.Fatalf("secret-looking ReasonCategory must become %q, got %q", "other", st.History[2].ReasonCategory)
		}
		if st.History[2].ReasonCategory == configreload.RedactedPlaceholder {
			t.Fatal("ReasonCategory must not use actor redaction placeholder")
		}
	})
}

// 8. Last-success and last-failure across sequences.

func TestReloadState_LastSuccessAndFailureAcrossSequence(t *testing.T) {
	t.Parallel()
	s := newReloadState(reloadStateInitial{})
	s.Apply(attemptOutcome{
		Result:          sdkreload.Result{Category: sdkreload.ResultPublished, AttemptID: 1, ActiveGeneration: 1},
		EffectiveUpdate: stateEffective("fp-1", 1),
	}, reloadTerminalMeta{})
	s.Apply(attemptOutcome{Result: sdkreload.Result{Category: sdkreload.ResultSourceIntegrity, AttemptID: 2, ActiveGeneration: 1}}, reloadTerminalMeta{})
	s.Apply(attemptOutcome{
		Result:          sdkreload.Result{Category: sdkreload.ResultPublished, AttemptID: 3, ActiveGeneration: 2},
		EffectiveUpdate: stateEffective("fp-2", 2),
	}, reloadTerminalMeta{})

	st := s.Snapshot(reloadStatusInput{})
	if st.LastResult.AttemptID != 3 || st.LastResult.Category != sdkreload.ResultPublished {
		t.Fatalf("LastResult=%+v", st.LastResult)
	}
	if st.LastSuccess.AttemptID != 3 {
		t.Fatalf("LastSuccess=%+v want attempt 3", st.LastSuccess)
	}
	if st.LastFailure.AttemptID != 2 || st.LastFailure.Category != sdkreload.ResultSourceIntegrity {
		t.Fatalf("LastFailure=%+v want attempt 2 source-integrity", st.LastFailure)
	}
}

// 9. Bounded history eviction/order and capacity default.

func TestReloadState_HistoryCapacityDefault(t *testing.T) {
	t.Parallel()
	s := newReloadState(reloadStateInitial{HistoryCapacity: 0})
	for i := int64(1); i <= 33; i++ {
		s.Apply(attemptOutcome{Result: sdkreload.Result{Category: sdkreload.ResultNoop, AttemptID: i, ActiveGeneration: 1}}, reloadTerminalMeta{})
	}
	st := s.Snapshot(reloadStatusInput{})
	if len(st.History) != 32 {
		t.Fatalf("history len=%d want default cap 32", len(st.History))
	}
	if st.History[0].AttemptID != 2 {
		t.Fatalf("oldest evicted entry: first AttemptID=%d want 2", st.History[0].AttemptID)
	}
	if st.History[31].AttemptID != 33 {
		t.Fatalf("newest entry: last AttemptID=%d want 33", st.History[31].AttemptID)
	}
}

func TestReloadState_HistoryEvictionOrderCustomCapacity(t *testing.T) {
	t.Parallel()
	s := newReloadState(reloadStateInitial{HistoryCapacity: 3})
	for i := int64(1); i <= 5; i++ {
		s.Apply(attemptOutcome{Result: sdkreload.Result{Category: sdkreload.ResultNoop, AttemptID: i, ActiveGeneration: 1}}, reloadTerminalMeta{})
	}
	st := s.Snapshot(reloadStatusInput{})
	if len(st.History) != 3 {
		t.Fatalf("history len=%d want 3", len(st.History))
	}
	want := []int64{3, 4, 5}
	for i, e := range st.History {
		if e.AttemptID != want[i] {
			t.Fatalf("history[%d].AttemptID=%d want %d (full=%v)", i, e.AttemptID, want[i], st.History)
		}
	}
}

func TestReloadState_HistoryNegativeCapacityDefaults(t *testing.T) {
	t.Parallel()
	s := newReloadState(reloadStateInitial{HistoryCapacity: -5})
	for i := int64(1); i <= 33; i++ {
		s.Apply(attemptOutcome{Result: sdkreload.Result{Category: sdkreload.ResultNoop, AttemptID: i, ActiveGeneration: 1}}, reloadTerminalMeta{})
	}
	st := s.Snapshot(reloadStatusInput{})
	if len(st.History) != 32 {
		t.Fatalf("history len=%d want default cap 32 for negative capacity input", len(st.History))
	}
}

// 10. History stage/candidate/duration/actor metadata.

func TestReloadState_HistoryStageCandidateDurationActorMetadata(t *testing.T) {
	t.Parallel()
	s := newReloadState(reloadStateInitial{})
	// Non-published: candidate generation must be 0 regardless of ActiveGeneration.
	s.Apply(attemptOutcome{
		Result: sdkreload.Result{
			Category:          sdkreload.ResultRestartRequired,
			AttemptID:         1,
			ActiveGeneration:  7,
			ReasonCategory:    "classify",
			RestartFieldCount: 2,
		},
	}, reloadTerminalMeta{
		Trigger:  sdkreload.Trigger{Kind: sdkreload.TriggerSIGHUP, SafeActor: "sighup"},
		Duration: 10 * time.Millisecond,
	})
	st := s.Snapshot(reloadStatusInput{})
	if len(st.History) != 1 {
		t.Fatalf("history=%v", st.History)
	}
	e := st.History[0]
	if e.CandidateGeneration != 0 {
		t.Fatalf("non-published CandidateGeneration=%d want 0", e.CandidateGeneration)
	}
	if e.ActiveGeneration != 7 {
		t.Fatalf("ActiveGeneration=%d want 7", e.ActiveGeneration)
	}
	if e.DurationMs != 10 {
		t.Fatalf("DurationMs=%d want 10", e.DurationMs)
	}
	if e.RestartFieldCount != 2 {
		t.Fatalf("RestartFieldCount=%d want 2", e.RestartFieldCount)
	}
	if e.Trigger != sdkreload.TriggerSIGHUP {
		t.Fatalf("Trigger=%q", e.Trigger)
	}
	if e.SafeActor != "sighup" {
		t.Fatalf("SafeActor=%q", e.SafeActor)
	}
}

// Migrated from observability_test.go TestReloadObservability_CanonicalHistoryEntryCategoryLabels
// (Task 6.4: history policy is now exclusively a ReloadState behavior).
func TestReloadState_CanonicalHistoryEntryCategoryLabels(t *testing.T) {
	t.Parallel()
	s := newReloadState(reloadStateInitial{HistoryCapacity: 4})
	s.Apply(attemptOutcome{
		Result: sdkreload.Result{
			Category:         sdkreload.ResultNoop,
			AttemptID:        9,
			ActiveGeneration: 3,
			ReasonCategory:   configreload.StageNoop,
		},
	}, reloadTerminalMeta{Trigger: sdkreload.Trigger{Kind: sdkreload.TriggerSIGHUP, SafeActor: "sighup"}})

	st := s.Snapshot(reloadStatusInput{})
	if len(st.History) != 1 {
		t.Fatalf("history len=%d want 1", len(st.History))
	}
	e := st.History[0]
	if e.Trigger != sdkreload.TriggerSIGHUP {
		t.Fatalf("trigger=%q", e.Trigger)
	}
	if e.Category != sdkreload.ResultNoop {
		t.Fatalf("category=%q want %q", e.Category, sdkreload.ResultNoop)
	}
	if string(e.Category) != "no-op" {
		t.Fatalf("category label drifted to %q", e.Category)
	}
	if e.Stage != configreload.StageNoop {
		t.Fatalf("stage=%q", e.Stage)
	}
}

// 11. Snapshot dynamic gate/manager fields and control degraded.

func TestReloadState_SnapshotDynamicFieldsPassthrough(t *testing.T) {
	t.Parallel()
	s := newReloadState(reloadStateInitial{})
	st := s.Snapshot(reloadStatusInput{
		ActiveGeneration:    5,
		Busy:                false,
		PendingSignal:       true,
		CoalescedSignals:    3,
		RetainedGenerations: 2,
		RetentionPressure:   true,
	})
	if st.ActiveGeneration != 5 || !st.PendingSignal || st.CoalescedSignals != 3 ||
		st.RetainedGenerations != 2 || !st.RetentionPressure {
		t.Fatalf("dynamic passthrough mismatch: %+v", st)
	}
}

func TestReloadState_SnapshotControlDegraded(t *testing.T) {
	t.Parallel()
	s := newReloadState(reloadStateInitial{})
	if s.Snapshot(reloadStatusInput{}).ControlDegraded {
		t.Fatal("no failure yet: must not be degraded")
	}
	s.Apply(attemptOutcome{Result: sdkreload.Result{Category: sdkreload.ResultNoop, AttemptID: 1}}, reloadTerminalMeta{})
	if s.Snapshot(reloadStatusInput{}).ControlDegraded {
		t.Fatal("no-op last-failure must not be degraded")
	}
	s.Apply(attemptOutcome{Result: sdkreload.Result{Category: sdkreload.ResultInvalid, AttemptID: 2}}, reloadTerminalMeta{})
	if !s.Snapshot(reloadStatusInput{}).ControlDegraded {
		t.Fatal("non-noop/published last-failure must be degraded")
	}
}

// 12. Busy CurrentAttempt and non-busy nil.

func TestReloadState_SnapshotBusyCurrentAttempt(t *testing.T) {
	t.Parallel()
	s := newReloadState(reloadStateInitial{})
	s.Apply(attemptOutcome{Result: sdkreload.Result{
		Category: sdkreload.ResultRestartRequired, AttemptID: 5,
		RestartFields: []string{"a", "b"},
	}}, reloadTerminalMeta{})

	notBusy := s.Snapshot(reloadStatusInput{Busy: false})
	if notBusy.CurrentAttempt != nil {
		t.Fatalf("non-busy snapshot must have nil CurrentAttempt, got %+v", notBusy.CurrentAttempt)
	}
	busy := s.Snapshot(reloadStatusInput{Busy: true})
	if busy.CurrentAttempt == nil || busy.CurrentAttempt.AttemptID != 5 {
		t.Fatalf("busy snapshot CurrentAttempt=%+v", busy.CurrentAttempt)
	}
}

// 13. Defensive copies: RestartFields, ActiveSource, CurrentAttempt, History.

func TestReloadState_DefensiveCopies(t *testing.T) {
	t.Parallel()
	s := newReloadState(reloadStateInitial{ActiveSource: stateActiveSource(1)})

	origFields := []string{"server.address", "tls.cert"}
	s.Apply(attemptOutcome{Result: sdkreload.Result{
		Category:      sdkreload.ResultRestartRequired,
		AttemptID:     1,
		RestartFields: append([]string(nil), origFields...),
	}}, reloadTerminalMeta{Trigger: sdkreload.Trigger{SafeActor: "actor-1"}})

	st1 := s.Snapshot(reloadStatusInput{Busy: true})
	if len(st1.LastResult.RestartFields) < 2 || len(st1.CurrentAttempt.RestartFields) < 2 || len(st1.History) != 1 {
		t.Fatalf("setup mismatch: %+v", st1)
	}
	st1.LastResult.RestartFields[0] = "mutated"
	st1.CurrentAttempt.RestartFields[0] = "mutated"
	st1.LastResult.Category = sdkreload.ResultPublished
	st1.CurrentAttempt.Category = sdkreload.ResultPublished
	st1.History[0].SafeActor = "mutated-actor"

	st2 := s.Snapshot(reloadStatusInput{Busy: true})
	if st2.LastResult.RestartFields[0] != origFields[0] {
		t.Fatalf("mutating st1.LastResult leaked: %v", st2.LastResult.RestartFields)
	}
	if st2.LastResult.Category != sdkreload.ResultRestartRequired {
		t.Fatalf("mutating st1.LastResult.Category leaked: %q", st2.LastResult.Category)
	}
	if st2.CurrentAttempt.RestartFields[0] != origFields[0] {
		t.Fatalf("mutating st1.CurrentAttempt leaked: %v", st2.CurrentAttempt.RestartFields)
	}
	if st2.CurrentAttempt.Category != sdkreload.ResultRestartRequired {
		t.Fatalf("mutating st1.CurrentAttempt.Category leaked: %q", st2.CurrentAttempt.Category)
	}
	if st2.History[0].SafeActor != "actor-1" {
		t.Fatalf("mutating st1.History leaked: %q", st2.History[0].SafeActor)
	}
	if len(st1.LastResult.RestartFields) > 0 && len(st2.LastResult.RestartFields) > 0 &&
		&st1.LastResult.RestartFields[0] == &st2.LastResult.RestartFields[0] {
		t.Fatal("snapshots must not share RestartFields backing array")
	}

	// Outcome.RestartFields mutation after Apply must not affect stored state.
	s2 := newReloadState(reloadStateInitial{})
	shared := []string{"x", "y"}
	s2.Apply(attemptOutcome{Result: sdkreload.Result{Category: sdkreload.ResultRestartRequired, AttemptID: 9, RestartFields: shared}}, reloadTerminalMeta{})
	shared[0] = "corrupted"
	st3 := s2.Snapshot(reloadStatusInput{})
	if st3.LastResult.RestartFields[0] != "x" {
		t.Fatalf("mutating caller-owned outcome slice after Apply leaked into state: %v", st3.LastResult.RestartFields)
	}

	// SourceUpdate must be cloned before storing.
	s3 := newReloadState(reloadStateInitial{})
	src := stateActiveSource(3)
	s3.Apply(attemptOutcome{Result: sdkreload.Result{Category: sdkreload.ResultNoop, AttemptID: 1}, SourceUpdate: src}, reloadTerminalMeta{})
	src.PrivateDigest[0] = 0xEE
	in := s3.ActiveInput(sdkreload.Trigger{}, 2, 1)
	if in.ActiveSource.PrivateDigest[0] == 0xEE {
		t.Fatal("Apply must clone SourceUpdate before storing")
	}
}

// 14. Concurrent ActiveInput/Apply/Snapshot under race with causal barriers.

func TestReloadState_ConcurrentAccessRace(t *testing.T) {
	t.Parallel()
	s := newReloadState(reloadStateInitial{
		ActiveEffective: stateEffective("fp-0", 0),
		ActiveSource:    stateActiveSource(0),
	})
	const n = 200
	var wg sync.WaitGroup
	wg.Add(3)

	go func() {
		defer wg.Done()
		for i := int64(1); i <= n; i++ {
			_ = s.ActiveInput(sdkreload.Trigger{Kind: sdkreload.TriggerAPI}, i, i)
		}
	}()
	go func() {
		defer wg.Done()
		for i := int64(1); i <= n; i++ {
			cat := sdkreload.ResultNoop
			var eff *config.EffectiveConfig
			if i%2 == 0 {
				cat = sdkreload.ResultPublished
				eff = stateEffective("fp-race", byte(i))
			}
			s.Apply(attemptOutcome{
				Result:          sdkreload.Result{Category: cat, AttemptID: i, ActiveGeneration: i},
				EffectiveUpdate: eff,
				SourceUpdate:    stateActiveSource(byte(i)),
			}, reloadTerminalMeta{Trigger: sdkreload.Trigger{Kind: sdkreload.TriggerAPI}, RecordedAt: time.Now().UTC()})
		}
	}()
	go func() {
		defer wg.Done()
		for i := range n {
			st := s.Snapshot(reloadStatusInput{ActiveGeneration: int64(i), Busy: i%2 == 0})
			if st.History == nil {
				continue
			}
			_ = len(st.History)
		}
	}()
	wg.Wait()
}
