// Package storecontract holds reusable contract tests for [app.Store] implementations.
package storecontract

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/securesession/app"
	"github.com/aiproxer/aiproxer/internal/core/securesession/domain"
)

// RunAll exercises the secure-session store port (memory, SQLite, etc.).
// newStore is called from each subtest with that subtest's *testing.T so construction failures
// and [testing.T.Cleanup] hooks are attributed to the correct subtest.
func RunAll(t *testing.T, newStore func(*testing.T) app.Store) {
	t.Helper()

	t.Run("Create_uniqueness_sessionID", func(t *testing.T) {
		t.Parallel()
		testCreateUniquenessSessionid(t, newStore(t))
	})

	t.Run("Create_uniqueness_fingerprint", func(t *testing.T) {
		t.Parallel()
		testCreateUniquenessFingerprint(t, newStore(t))
	})

	t.Run("LoadByID_owner_workspace", func(t *testing.T) {
		t.Parallel()
		testLoadByIDOwnerWorkspace(t, newStore(t))
	})

	t.Run("LoadByALegID_roundTrip", func(t *testing.T) {
		t.Parallel()
		testLoadByALegIDRoundTrip(t, newStore(t))
	})

	t.Run("AttemptTrace_outcome_by_BLeg", func(t *testing.T) {
		t.Parallel()
		testAttemptTraceOutcomeByBLeg(t, newStore(t))
	})

	t.Run("Transcript_disabled_explicit", func(t *testing.T) {
		t.Parallel()
		testTranscriptDisabledExplicit(t, newStore(t))
	})

	t.Run("AddUsage_summary_rollups", func(t *testing.T) {
		t.Parallel()
		testAddUsageSummaryRollups(t, newStore(t))
	})

	t.Run("ListAttemptEvidence_trace_usage_outcome", func(t *testing.T) {
		t.Parallel()
		testListAttemptEvidenceTraceUsageOutcome(t, newStore(t))
	})

	t.Run("FailedOpen_attempt_round_trip", func(t *testing.T) {
		t.Parallel()
		testFailedOpenAttemptRoundTrip(t, newStore(t))
	})

	t.Run("ListAttemptEvidence_window_is_newest_attempts", func(t *testing.T) {
		t.Parallel()
		testListAttemptEvidenceWindowIsNewest(t, newStore(t))
	})

	t.Run("AddUsage_finish_timing_preserves_latest_usage", func(t *testing.T) {
		t.Parallel()
		testAddUsageFinishTimingPreservesLatestUsage(t, newStore(t))
	})

	t.Run("AddUsage_total_tokens_uses_latest_provider_total", func(t *testing.T) {
		t.Parallel()
		testAddUsageTotalTokensUsesLatestProviderTotal(t, newStore(t))
	})

	t.Run("missing_lookups_non_enumerating", func(t *testing.T) {
		t.Parallel()
		testMissingLookupsNonEnumerating(t, newStore(t))
	})

	t.Run("TouchActivity_updates_record", func(t *testing.T) {
		t.Parallel()
		testTouchActivityUpdatesRecord(t, newStore(t))
	})

	t.Run("TouchActivity_olderTimestampIgnored", func(t *testing.T) {
		t.Parallel()
		testTouchActivityOlderTimestampIgnored(t, newStore(t))
	})

	t.Run("AppendTranscript_Audit_ordering", func(t *testing.T) {
		t.Parallel()
		testAppendTranscriptAuditOrdering(t, newStore(t))
	})

	t.Run("NextAuditSeq", func(t *testing.T) {
		t.Parallel()
		testNextAuditSeq(t, newStore(t))
	})

	t.Run("NextTranscriptSeq", func(t *testing.T) {
		t.Parallel()
		testNextTranscriptSeq(t, newStore(t))
	})
}

func testCreateUniquenessSessionid(t *testing.T, s app.Store) {
	t.Helper()
	ctx := context.Background()
	fp1, fp2 := twoFingerprints()
	base := sampleCreate("owner-a", "ws-1", fp1, "a-leg-1", "sid-1")
	_, err := s.Create(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	dup := base
	dup.ResumeFingerprint = fp2
	dup.ALegID = "a-leg-2"
	_, err = s.Create(ctx, dup)
	if !errors.Is(err, domain.ErrDuplicateSessionID) {
		t.Fatalf("Create duplicate session id: got %v want %v", err, domain.ErrDuplicateSessionID)
	}
}

func testCreateUniquenessFingerprint(t *testing.T, s app.Store) {
	t.Helper()
	ctx := context.Background()
	fp, _ := twoFingerprints()
	r1 := sampleCreate("o1", "w1", fp, "aleg-1", "session-1")
	_, err := s.Create(ctx, r1)
	if err != nil {
		t.Fatal(err)
	}
	r2 := sampleCreate("o1", "w1", fp, "aleg-2", "session-2")
	_, err = s.Create(ctx, r2)
	if !errors.Is(err, domain.ErrDuplicateFingerprint) {
		t.Fatalf("Create duplicate fingerprint: got %v want %v", err, domain.ErrDuplicateFingerprint)
	}
}

func testLoadByIDOwnerWorkspace(t *testing.T, s app.Store) {
	t.Helper()
	ctx := context.Background()
	fp, _ := twoFingerprints()
	cr := sampleCreate("principal-x", "workspace-y", fp, "a-leg-z", "sess-own")
	created, err := s.Create(ctx, cr)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.LoadByID(ctx, created.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Owner.ID != "principal-x" || got.Workspace.ID != "workspace-y" {
		t.Fatalf("owner/workspace: got %+v", got)
	}
}

func testLoadByALegIDRoundTrip(t *testing.T, s app.Store) {
	t.Helper()
	ctx := context.Background()
	fp, _ := twoFingerprints()
	cr := sampleCreate("o", "w", fp, "a-leg-lookup", "sess-aleg")
	created, err := s.Create(ctx, cr)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.LoadByALegID(ctx, "a-leg-lookup")
	if err != nil {
		t.Fatal(err)
	}
	if got.SessionID != created.SessionID {
		t.Fatalf("session id mismatch")
	}
}

func testAttemptTraceOutcomeByBLeg(t *testing.T, s app.Store) {
	t.Helper()
	ctx := context.Background()
	fp, _ := twoFingerprints()
	cr := sampleCreate("o", "w", fp, "a-main", "sess-trace")
	rec, err := s.Create(ctx, cr)
	if err != nil {
		t.Fatal(err)
	}
	tr := domain.AttemptTrace{
		SessionID: rec.SessionID, TurnID: "t1", ALegID: "a-main", BLegID: "b-1",
		AttemptSeq: 1, RequestedModel: "m", ResolvedBackend: "be", ResolvedModel: "rm",
		RouteSource: "rs", RouteReason: "rr", StartedAt: time.Unix(10, 0),
	}
	if err := s.AppendAttemptTrace(ctx, tr); err != nil {
		t.Fatal(err)
	}
	out := domain.AttemptOutcome{
		SessionID: rec.SessionID, TurnID: "t1", BLegID: "b-1", Success: true,
		SurfaceState: domain.SurfaceSurfaced, EndedAt: time.Unix(11, 0),
	}
	if err := s.UpdateAttemptOutcome(ctx, out); err != nil {
		t.Fatal(err)
	}
	loaded, err := s.LoadByID(ctx, rec.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.LatestAttemptTrace.BLegID != "b-1" || loaded.LatestAttemptOutcome.BLegID != "b-1" || !loaded.LatestAttemptOutcome.Success {
		t.Fatalf("trace/outcome: trace=%+v outcome=%+v", loaded.LatestAttemptTrace, loaded.LatestAttemptOutcome)
	}
}

// testFailedOpenAttemptRoundTrip pins that an attempt whose backend opened no
// stream persists beside a stream-acquiring one, keeps its failure outcome and
// pre-output classification, and is never read back as an acquisition.
func testFailedOpenAttemptRoundTrip(t *testing.T, s app.Store) {
	t.Helper()
	ctx := context.Background()
	fp, _ := twoFingerprints()
	cr := sampleCreate("o", "w", fp, "a-main", "sess-failed-open")
	rec, err := s.Create(ctx, cr)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AppendAttemptTrace(ctx, domain.AttemptTrace{
		SessionID: rec.SessionID, TurnID: "t1", ALegID: "a-main", BLegID: "b-fail",
		AttemptSeq: 1, ResolvedBackend: "be-nil", ResolvedModel: "m", StartedAt: time.Unix(10, 0),
		OpenFailed: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateAttemptOutcome(ctx, domain.AttemptOutcome{
		SessionID: rec.SessionID, TurnID: "t1", BLegID: "b-fail",
		SurfaceState: domain.SurfaceSwallowed, ProviderStatus: "pre_output",
		ErrorCode: "be-nil:m", DebugReason: "backend open returned no stream", EndedAt: time.Unix(11, 0),
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendAttemptTrace(ctx, domain.AttemptTrace{
		SessionID: rec.SessionID, TurnID: "t1", ALegID: "a-main", BLegID: "b-ok",
		AttemptSeq: 2, ResolvedBackend: "be-ok", ResolvedModel: "m", StartedAt: time.Unix(12, 0),
	}); err != nil {
		t.Fatal(err)
	}
	ev, err := s.ListAttemptEvidence(ctx, rec.SessionID, domain.ReadOptions{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if len(ev) != 2 {
		t.Fatalf("want 2 attempts, got %d", len(ev))
	}
	failed, opened := ev[0], ev[1]
	if failed.Trace.BLegID != "b-fail" || !failed.Trace.OpenFailed || failed.Trace.ResolvedBackend != "be-nil" {
		t.Fatalf("failed-open trace: %+v", failed.Trace)
	}
	if failed.Outcome.Success || failed.Outcome.ProviderStatus != "pre_output" ||
		failed.Outcome.ErrorCode != "be-nil:m" || failed.Outcome.DebugReason != "backend open returned no stream" {
		t.Fatalf("failed-open outcome: %+v", failed.Outcome)
	}
	if !opened.Outcome.EndedAt.IsZero() {
		t.Fatalf("an attempt without a terminal outcome has no completion time: %v", opened.Outcome.EndedAt)
	}
	if opened.Trace.BLegID != "b-ok" || opened.Trace.OpenFailed {
		t.Fatalf("acquired trace must not read as a failed open: %+v", opened.Trace)
	}
}

// testListAttemptEvidenceWindowIsNewest pins that a limited listing serves the
// newest attempts, oldest first, across turns, so a long session never hides a
// recent (for example failed-open) attempt.
func testListAttemptEvidenceWindowIsNewest(t *testing.T, s app.Store) {
	t.Helper()
	ctx := context.Background()
	fp, _ := twoFingerprints()
	rec, err := s.Create(ctx, sampleCreate("o", "w", fp, "a-main", "sess-window"))
	if err != nil {
		t.Fatal(err)
	}
	legs := []struct {
		turn, leg string
		seq       int
		failed    bool
	}{
		{"t1", "b-1", 1, false},
		{"t1", "b-2", 2, false},
		{"t2", "b-3", 1, false},
		{"t2", "b-4", 2, false},
		{"t3", "b-5", 1, true},
	}
	for i, l := range legs {
		if err := s.AppendAttemptTrace(ctx, domain.AttemptTrace{
			SessionID: rec.SessionID, TurnID: domain.TurnID(l.turn), ALegID: "a-main", BLegID: l.leg,
			AttemptSeq: l.seq, StartedAt: time.Unix(int64(10+i), 0), OpenFailed: l.failed,
		}); err != nil {
			t.Fatal(err)
		}
	}
	ev, err := s.ListAttemptEvidence(ctx, rec.SessionID, domain.ReadOptions{Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(ev) != 3 || ev[0].Trace.BLegID != "b-3" || ev[1].Trace.BLegID != "b-4" ||
		ev[2].Trace.BLegID != "b-5" || !ev[2].Trace.OpenFailed {
		t.Fatalf("want newest three oldest-first ending in the failed open, got %+v", ev)
	}
}

func testTranscriptDisabledExplicit(t *testing.T, s app.Store) {
	t.Helper()
	ctx := context.Background()
	fp, _ := twoFingerprints()
	cr := sampleCreate("o", "w", fp, "a-t", "sess-tx-off")
	cr.Policy.TranscriptEnabled = false
	rec, err := s.Create(ctx, cr)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Policy.TranscriptEnabled {
		t.Fatal("expected transcript disabled on record")
	}
	items, err := s.Transcript(ctx, rec.SessionID, domain.ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	assertNonNilEmptySlice(t, items)
}

func testAddUsageSummaryRollups(t *testing.T, s app.Store) {
	t.Helper()
	ctx := context.Background()
	fp, _ := twoFingerprints()
	cr := sampleCreate("owner-sum", "ws-sum", fp, "a-sum", "sess-sum")
	rec, err := s.Create(ctx, cr)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AppendAttemptTrace(ctx, domain.AttemptTrace{
		SessionID: rec.SessionID, TurnID: "t1", ALegID: cr.ALegID, BLegID: "b1",
		AttemptSeq: 1, StartedAt: time.Unix(1, 0),
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddUsage(ctx, domain.UsageDelta{
		SessionID: rec.SessionID, TurnID: "t1", BLegID: "b1",
		InputTokens: 3, OutputTokens: 5,
	}); err != nil {
		t.Fatal(err)
	}
	sums, err := s.Summary(ctx, domain.SummaryQuery{OwnerID: "owner-sum", WorkspaceID: "ws-sum", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	assertNonNilEmptySlice(t, sums)
	var found *domain.Summary
	for i := range sums {
		if sums[i].SessionID == rec.SessionID {
			found = &sums[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("summary missing session: %#v", sums)
		return
	}
	if found.AttemptCount < 1 {
		t.Fatalf("attempt count: got %d", found.AttemptCount)
	}
	if found.ALegID != "a-sum" || !found.ResumeEligible {
		t.Fatalf("summary lineage: %+v", found)
	}
	if found.PolicyVersion != "v1" || !found.TranscriptEnabled || found.AuditMode != "optional" {
		t.Fatalf("summary policy: %+v", found)
	}
	if found.UsageInputTokens != 3 || found.UsageOutputTokens != 5 {
		t.Fatalf("summary usage: %+v", found)
	}
}

func testListAttemptEvidenceTraceUsageOutcome(t *testing.T, s app.Store) {
	t.Helper()
	ctx := context.Background()
	fp, _ := twoFingerprints()
	cr := sampleCreate("owner-ev", "ws-ev", fp, "a-leg-ev", "sess-evidence")
	rec, err := s.Create(ctx, cr)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AppendAttemptTrace(ctx, domain.AttemptTrace{
		SessionID: rec.SessionID, TurnID: "t1", ALegID: cr.ALegID, BLegID: "b-ev",
		AttemptSeq: 1, RequestedModel: "m1", ResolvedBackend: "be1", ResolvedModel: "r1",
		RouteSource: "src1", StartedAt: time.Unix(10, 0),
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateAttemptOutcome(ctx, domain.AttemptOutcome{
		SessionID: rec.SessionID, TurnID: "t1", BLegID: "b-ev", Success: true,
		SurfaceState: domain.SurfaceSurfaced, EndedAt: time.Unix(11, 0),
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddUsage(ctx, domain.UsageDelta{
		SessionID: rec.SessionID, TurnID: "t1", BLegID: "b-ev",
		InputTokens: 2, OutputTokens: 4, CacheReadTokens: 1, CacheWriteTokens: 3,
		NonCachedInputTokens: 0, ReasoningTokens: 2, NonReasoningOutputTokens: 2,
		TotalTokens: 6, CostNanoUnits: 42, Currency: "USD", CostSource: "estimated",
		RawUsageJSON:     `{"provider":"test"}`,
		RequestStartedAt: time.Unix(100, 0), FirstRemoteEventAt: time.Unix(100, int64(10*time.Millisecond)),
		FirstMeaningfulTokenAt: time.Unix(100, int64(25*time.Millisecond)),
		RemoteCompletedAt:      time.Unix(101, 0), ProxyCompletedAt: time.Unix(101, int64(5*time.Millisecond)),
		TTFTMillis: 25, RemoteDurationMillis: 1000, CompletionDurationMillis: 975, CompletionTPSMilli: 4102,
	}); err != nil {
		t.Fatal(err)
	}
	ev, err := s.ListAttemptEvidence(ctx, rec.SessionID, domain.ReadOptions{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if len(ev) != 1 {
		t.Fatalf("want 1 attempt, got %d", len(ev))
	}
	if ev[0].Trace.BLegID != "b-ev" || ev[0].Trace.RequestedModel != "m1" {
		t.Fatalf("trace: %+v", ev[0].Trace)
	}
	if !ev[0].Outcome.Success || ev[0].Outcome.SurfaceState != domain.SurfaceSurfaced {
		t.Fatalf("outcome: %+v", ev[0].Outcome)
	}
	if ev[0].Accounting.InputTokens != 2 || ev[0].Accounting.OutputTokens != 4 || ev[0].Accounting.CacheReadTokens != 1 {
		t.Fatalf("accounting: %+v", ev[0].Accounting)
	}
	if ev[0].Accounting.CacheWriteTokens != 3 || ev[0].Accounting.ReasoningTokens != 2 ||
		ev[0].Accounting.NonReasoningOutputTokens != 2 || ev[0].Accounting.TotalTokens != 6 ||
		ev[0].Accounting.CostNanoUnits != 42 ||
		ev[0].Accounting.Currency != "USD" || ev[0].Accounting.CostSource != "estimated" ||
		ev[0].Accounting.RawUsageJSON != `{"provider":"test"}` {
		t.Fatalf("accounting: %+v", ev[0].Accounting)
	}
	if ev[0].Accounting.TTFTMillis != 25 || ev[0].Accounting.RemoteDurationMillis != 1000 ||
		ev[0].Accounting.CompletionDurationMillis != 975 || ev[0].Accounting.CompletionTPSMilli != 4102 {
		t.Fatalf("accounting timing: %+v", ev[0].Accounting)
	}
}

func testAddUsageFinishTimingPreservesLatestUsage(t *testing.T, s app.Store) {
	t.Helper()
	ctx := context.Background()
	fp, _ := twoFingerprints()
	cr := sampleCreate("owner-finish", "ws-finish", fp, "a-finish", "sess-finish")
	rec, err := s.Create(ctx, cr)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AppendAttemptTrace(ctx, domain.AttemptTrace{
		SessionID: rec.SessionID, TurnID: "t1", ALegID: cr.ALegID, BLegID: "b-finish",
		AttemptSeq: 1, StartedAt: time.Unix(1, 0),
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddUsage(ctx, domain.UsageDelta{
		SessionID: rec.SessionID, TurnID: "t1", BLegID: "b-finish",
		InputTokens: 7, OutputTokens: 11, CacheReadTokens: 2, CostNanoUnits: 99, Currency: "USD",
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddUsage(ctx, domain.UsageDelta{
		SessionID: rec.SessionID, TurnID: "t1", BLegID: "b-finish",
		RequestStartedAt: time.Unix(10, 0), FirstMeaningfulTokenAt: time.Unix(10, int64(50*time.Millisecond)),
		RemoteCompletedAt: time.Unix(11, 0), TTFTMillis: 50, CompletionTPSMilli: 11000,
	}); err != nil {
		t.Fatal(err)
	}
	got, err := s.LoadByID(ctx, rec.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	ac := got.LatestAttemptAccounting
	if ac.InputTokens != 7 || ac.OutputTokens != 11 || ac.CacheReadTokens != 2 || ac.CostNanoUnits != 99 {
		t.Fatalf("latest usage overwritten by finish row: %+v", ac)
	}
	if ac.TTFTMillis != 50 || ac.CompletionTPSMilli != 11000 {
		t.Fatalf("latest timing missing: %+v", ac)
	}
}

func testAddUsageTotalTokensUsesLatestProviderTotal(t *testing.T, s app.Store) {
	t.Helper()
	ctx := context.Background()
	fp, _ := twoFingerprints()
	cr := sampleCreate("owner-total", "ws-total", fp, "a-total", "sess-total")
	rec, err := s.Create(ctx, cr)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AppendAttemptTrace(ctx, domain.AttemptTrace{
		SessionID: rec.SessionID, TurnID: "t1", ALegID: cr.ALegID, BLegID: "b-total",
		AttemptSeq: 1, StartedAt: time.Unix(1, 0),
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddUsage(ctx, domain.UsageDelta{
		SessionID: rec.SessionID, TurnID: "t1", BLegID: "b-total",
		InputTokens: 10, TotalTokens: 14,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddUsage(ctx, domain.UsageDelta{
		SessionID: rec.SessionID, TurnID: "t1", BLegID: "b-total",
		OutputTokens: 4, TotalTokens: 18,
	}); err != nil {
		t.Fatal(err)
	}
	got, err := s.ListAttemptEvidence(ctx, rec.SessionID, domain.ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("attempt evidence len=%d", len(got))
	}
	ac := got[0].Accounting
	if ac.InputTokens != 10 || ac.OutputTokens != 4 || ac.TotalTokens != 18 {
		t.Fatalf("accounting total should keep latest absolute total, got %+v", ac)
	}
}

func testMissingLookupsNonEnumerating(t *testing.T, s app.Store) {
	t.Helper()
	ctx := context.Background()
	_, err := s.LoadByID(ctx, "no-such-session-id-xxxxxxxx")
	if !errors.Is(err, domain.ErrSessionNotFound) {
		t.Fatalf("LoadByID: %v", err)
	}
	var badFP domain.TokenFingerprint
	badFP[0] = 0xfe
	_, err = s.LoadByResumeFingerprint(ctx, badFP)
	if !errors.Is(err, domain.ErrSessionNotFound) {
		t.Fatalf("LoadByResumeFingerprint: %v", err)
	}
	_, err = s.LoadByALegID(ctx, "no-such-a-leg-zzzzzzzz")
	if !errors.Is(err, domain.ErrSessionNotFound) {
		t.Fatalf("LoadByALegID: %v", err)
	}
}

func testTouchActivityUpdatesRecord(t *testing.T, s app.Store) {
	t.Helper()
	ctx := context.Background()
	fp, _ := twoFingerprints()
	rec, err := s.Create(ctx, sampleCreate("o", "w", fp, "a-touch", "sess-touch"))
	if err != nil {
		t.Fatal(err)
	}
	at := time.Unix(1700, 0)
	if err := s.TouchActivity(ctx, rec.SessionID, at, domain.ActivityRemoteEvent); err != nil {
		t.Fatal(err)
	}
	got, err := s.LoadByID(ctx, rec.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.LastActivityAt.Equal(at) || got.LastActivitySource != domain.ActivityRemoteEvent {
		t.Fatalf("touch: %+v", got)
	}
}

func testTouchActivityOlderTimestampIgnored(t *testing.T, s app.Store) {
	t.Helper()
	ctx := context.Background()
	fp, _ := twoFingerprints()
	rec, err := s.Create(ctx, sampleCreate("o", "w", fp, "a-touch-old", "sess-touch-old"))
	if err != nil {
		t.Fatal(err)
	}
	newer := time.Unix(5000, 0)
	if err := s.TouchActivity(ctx, rec.SessionID, newer, domain.ActivityRemoteEvent); err != nil {
		t.Fatal(err)
	}
	if err := s.TouchActivity(ctx, rec.SessionID, time.Unix(100, 0), domain.ActivityClientRequest); err != nil {
		t.Fatal(err)
	}
	got, err := s.LoadByID(ctx, rec.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.LastActivityAt.Equal(newer) || got.LastActivitySource != domain.ActivityRemoteEvent {
		t.Fatalf("monotonic touch: want %v remote got %+v", newer, got)
	}
}

func testAppendTranscriptAuditOrdering(t *testing.T, s app.Store) {
	t.Helper()
	ctx := context.Background()
	fp, _ := twoFingerprints()
	cr := sampleCreate("o", "w", fp, "a-audit", "sess-audit")
	cr.Policy.TranscriptEnabled = true
	rec, err := s.Create(ctx, cr)
	if err != nil {
		t.Fatal(err)
	}
	t0 := time.Unix(100, 0)
	if err := s.AppendTranscript(ctx, domain.TranscriptItem{
		SessionID: rec.SessionID, TurnID: "t1", Seq: 1, EventKind: "user", PayloadRef: "p1", CreatedAt: t0,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendTranscript(ctx, domain.TranscriptItem{
		SessionID: rec.SessionID, TurnID: "t1", Seq: 2, EventKind: "assistant", PayloadRef: "p2", CreatedAt: t0.Add(time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	tx, err := s.Transcript(ctx, rec.SessionID, domain.ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(tx) != 2 || tx[0].Seq != 1 || tx[1].Seq != 2 {
		t.Fatalf("transcript order: %#v", tx)
	}
	if err := s.AppendAudit(ctx, domain.AuditItem{
		SessionID: rec.SessionID, TurnID: "t1", Seq: 1, Action: "open", Result: "ok", CreatedAt: t0,
	}); err != nil {
		t.Fatal(err)
	}
	aud, err := s.Audit(ctx, rec.SessionID, domain.ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(aud) != 1 || aud[0].Action != "open" {
		t.Fatalf("audit: %#v", aud)
	}
}

func testNextAuditSeq(t *testing.T, s app.Store) {
	t.Helper()
	ctx := context.Background()
	fp, _ := twoFingerprints()
	cr := sampleCreate("o", "w", fp, "a-naudit", "sess-naudit")
	rec, err := s.Create(ctx, cr)
	if err != nil {
		t.Fatal(err)
	}
	n1, err := s.NextAuditSeq(ctx, rec.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if n1 != 1 {
		t.Fatalf("next: got %d want 1", n1)
	}
	if err := s.AppendAudit(ctx, domain.AuditItem{
		SessionID: rec.SessionID, TurnID: "t1", Seq: n1, Action: "a", Result: "r", CreatedAt: time.Unix(1, 0),
	}); err != nil {
		t.Fatal(err)
	}
	n2, err := s.NextAuditSeq(ctx, rec.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if n2 != 2 {
		t.Fatalf("next: got %d want 2", n2)
	}
}

func testNextTranscriptSeq(t *testing.T, s app.Store) {
	t.Helper()
	ctx := context.Background()
	fp, _ := twoFingerprints()
	cr := sampleCreate("o", "w", fp, "a-ntr", "sess-ntr")
	cr.Policy.TranscriptEnabled = true
	rec, err := s.Create(ctx, cr)
	if err != nil {
		t.Fatal(err)
	}
	n1, err := s.NextTranscriptSeq(ctx, rec.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if n1 != 1 {
		t.Fatalf("next transcript: got %d want 1", n1)
	}
	if err := s.AppendTranscript(ctx, domain.TranscriptItem{
		SessionID: rec.SessionID, TurnID: "t1", Seq: n1, EventKind: "x", PayloadRef: "{}", CreatedAt: time.Unix(1, 0),
	}); err != nil {
		t.Fatal(err)
	}
	n2, err := s.NextTranscriptSeq(ctx, rec.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if n2 != 2 {
		t.Fatalf("next transcript: got %d want 2", n2)
	}
}

func assertNonNilEmptySlice[T any](t *testing.T, s []T) {
	t.Helper()
	if s == nil {
		t.Fatal("expected non-nil slice (empty ok)")
	}
}

func twoFingerprints() (domain.TokenFingerprint, domain.TokenFingerprint) {
	var a, b domain.TokenFingerprint
	a[0] = 1
	a[31] = 2
	b[0] = 2
	b[31] = 3
	return a, b
}

func sampleCreate(owner, ws string, fp domain.TokenFingerprint, aLeg string, sid domain.SessionID) domain.CreateRecord {
	return domain.CreateRecord{
		SessionID:         sid,
		ResumeFingerprint: fp,
		Owner:             domain.PrincipalRef{ID: owner},
		Workspace:         domain.WorkspaceRef{ID: ws},
		ClientHints:       domain.ClientHints{ClientSessionID: "hint"},
		Policy: domain.PolicyMetadata{
			PolicyVersion: "v1", TranscriptEnabled: true, AuditMode: "optional",
		},
		ALegID:         aLeg,
		ResumeEligible: true,
		CreatedAt:      time.Unix(1, 0),
	}
}
