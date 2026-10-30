package storecontract

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/securesession/app"
	"github.com/aiproxer/aiproxer/internal/core/securesession/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ParityFixture abstracts backend-specific store construction and lifecycle
// for canonical database parity testing across SQLite and PostgreSQL.
type ParityFixture interface {
	NewStore(t *testing.T) app.Store
	ReopenStore(t *testing.T) (app.Store, func() app.Store)
}

// RunParitySuite executes the canonical behavioral, capability, and restart parity suite
// for secure session persistence against the provided fixture.
func RunParitySuite(t *testing.T, f ParityFixture) {
	t.Helper()
	ctx := context.Background()

	t.Run("StoreContract", func(t *testing.T) {
		RunAll(t, f.NewStore)
	})

	t.Run("QuarantineContract", func(t *testing.T) {
		RunQuarantineContracts(t, f.NewStore)
	})

	t.Run("DurableCapabilities", func(t *testing.T) {
		t.Run("CheckReadinessMandatoryAudit", func(t *testing.T) {
			st := f.NewStore(t)
			err := st.CheckReadiness(ctx, domain.PolicyMetadata{AuditMode: "mandatory"})
			require.NoError(t, err, "durable store must satisfy mandatory audit readiness")
		})

		t.Run("SessionUsageRollup", func(t *testing.T) {
			st := f.NewStore(t)
			rollup, ok := st.(app.SessionUsageRollup)
			require.True(t, ok, "durable store must implement app.SessionUsageRollup")

			fp, _ := twoFingerprints()
			sid := domain.SessionID(fmt.Sprintf("sess-rollup-%d", time.Now().UnixNano()))
			aleg := fmt.Sprintf("a-rollup-%d", time.Now().UnixNano())
			_, err := st.Create(ctx, domain.CreateRecord{
				SessionID: sid, ResumeFingerprint: fp,
				Owner: domain.PrincipalRef{ID: "o-rollup"}, Workspace: domain.WorkspaceRef{ID: "w-rollup"},
				ClientHints: domain.ClientHints{ClientSessionID: "c-rollup"},
				Policy:      domain.PolicyMetadata{PolicyVersion: "v1", TranscriptEnabled: true, AuditMode: "optional"},
				ALegID:      aleg, ResumeEligible: true, CreatedAt: time.Unix(5, 0),
			})
			require.NoError(t, err)

			err = st.AddUsage(ctx, domain.UsageDelta{
				SessionID: sid, TurnID: "t1", BLegID: "b1",
				InputTokens: 15, OutputTokens: 25,
			})
			require.NoError(t, err)

			in, out, err := rollup.UsageTokenTotals(ctx, sid)
			require.NoError(t, err)
			assert.Equal(t, int64(15), in)
			assert.Equal(t, int64(25), out)
		})
	})

	t.Run("RestartSurvival", func(t *testing.T) {
		t.Run("SessionAndEvidence", func(t *testing.T) {
			s1, reopen := f.ReopenStore(t)
			fp, _ := twoFingerprints()
			sid := domain.SessionID(fmt.Sprintf("sess-restart-%d", time.Now().UnixNano()))
			aleg := fmt.Sprintf("aleg-restart-%d", time.Now().UnixNano())
			now := time.Unix(1_700_000_000, 0).UTC()

			_, err := s1.Create(ctx, domain.CreateRecord{
				SessionID:         sid,
				ResumeFingerprint: fp,
				Owner:             domain.PrincipalRef{ID: "owner-restart", Issuer: "iss", Tenant: "ten"},
				Workspace:         domain.WorkspaceRef{ID: "ws-restart"},
				ClientHints:       domain.ClientHints{ClientSessionID: "hint-restart"},
				Policy: domain.PolicyMetadata{
					PolicyVersion: "v1", TranscriptEnabled: true, AuditMode: "optional",
				},
				ALegID:         aleg,
				ResumeEligible: true,
				CreatedAt:      now,
			})
			require.NoError(t, err)

			err = s1.AppendAttemptTrace(ctx, domain.AttemptTrace{
				SessionID: sid, TurnID: "t1", ALegID: aleg, BLegID: "b-1",
				AttemptSeq: 1, RequestedModel: "req-m", ResolvedBackend: "be-1", ResolvedModel: "res-m",
				RouteSource: "rs-1", StartedAt: now.Add(time.Second),
			})
			require.NoError(t, err)

			err = s1.UpdateAttemptOutcome(ctx, domain.AttemptOutcome{
				SessionID: sid, TurnID: "t1", BLegID: "b-1", Success: true,
				SurfaceState: domain.SurfaceSurfaced, EndedAt: now.Add(2 * time.Second),
			})
			require.NoError(t, err)

			err = s1.AddUsage(ctx, domain.UsageDelta{
				SessionID: sid, TurnID: "t1", BLegID: "b-1",
				InputTokens: 100, OutputTokens: 200, TotalTokens: 300,
				CostNanoUnits: 5000, Currency: "USD", CostSource: "test",
			})
			require.NoError(t, err)

			err = s1.AppendTranscript(ctx, domain.TranscriptItem{
				SessionID: sid, TurnID: "t1", Seq: 1, EventKind: "user_message", PayloadRef: "payload-1", CreatedAt: now.Add(3 * time.Second),
			})
			require.NoError(t, err)

			err = s1.AppendAudit(ctx, domain.AuditItem{
				SessionID: sid, TurnID: "t1", Seq: 1, Action: "session_open", Result: "ok", CreatedAt: now.Add(4 * time.Second),
			})
			require.NoError(t, err)

			err = s1.TouchActivity(ctx, sid, now.Add(10*time.Second), domain.ActivityClientRequest)
			require.NoError(t, err)

			// Reopen store
			s2 := reopen()

			// Verify LoadByID
			got, err := s2.LoadByID(ctx, sid)
			require.NoError(t, err)
			assert.Equal(t, sid, got.SessionID)
			assert.Equal(t, "owner-restart", got.Owner.ID)
			assert.Equal(t, "ws-restart", got.Workspace.ID)
			assert.Equal(t, aleg, got.ALegID)
			assert.True(t, got.ResumeEligible)
			assert.True(t, got.LastActivityAt.Equal(now.Add(10*time.Second)))
			assert.Equal(t, domain.ActivityClientRequest, got.LastActivitySource)
			assert.Equal(t, "b-1", got.LatestAttemptTrace.BLegID)
			assert.True(t, got.LatestAttemptOutcome.Success)
			assert.Equal(t, int64(100), got.LatestAttemptAccounting.InputTokens)
			assert.Equal(t, int64(200), got.LatestAttemptAccounting.OutputTokens)

			// Verify LoadByResumeFingerprint
			gotFP, err := s2.LoadByResumeFingerprint(ctx, fp)
			require.NoError(t, err)
			assert.Equal(t, sid, gotFP.SessionID)

			// Verify LoadByALegID
			gotALeg, err := s2.LoadByALegID(ctx, aleg)
			require.NoError(t, err)
			assert.Equal(t, sid, gotALeg.SessionID)

			// Verify Transcript & sequence
			txs, err := s2.Transcript(ctx, sid, domain.ReadOptions{})
			require.NoError(t, err)
			require.Len(t, txs, 1)
			assert.Equal(t, int64(1), txs[0].Seq)
			assert.Equal(t, "user_message", txs[0].EventKind)

			nextTxSeq, err := s2.NextTranscriptSeq(ctx, sid)
			require.NoError(t, err)
			assert.Equal(t, int64(2), nextTxSeq)

			// Verify Audit & sequence
			audits, err := s2.Audit(ctx, sid, domain.ReadOptions{})
			require.NoError(t, err)
			require.Len(t, audits, 1)
			assert.Equal(t, int64(1), audits[0].Seq)
			assert.Equal(t, "session_open", audits[0].Action)

			nextAudSeq, err := s2.NextAuditSeq(ctx, sid)
			require.NoError(t, err)
			assert.Equal(t, int64(2), nextAudSeq)

			// Verify AttemptEvidence
			evs, err := s2.ListAttemptEvidence(ctx, sid, domain.ReadOptions{})
			require.NoError(t, err)
			require.Len(t, evs, 1)
			assert.Equal(t, "b-1", evs[0].Trace.BLegID)
			assert.True(t, evs[0].Outcome.Success)
			assert.Equal(t, int64(100), evs[0].Accounting.InputTokens)

			// Verify Summary
			sums, err := s2.Summary(ctx, domain.SummaryQuery{OwnerID: "owner-restart", WorkspaceID: "ws-restart"})
			require.NoError(t, err)
			require.Len(t, sums, 1)
			assert.Equal(t, sid, sums[0].SessionID)
			assert.Equal(t, int64(100), sums[0].UsageInputTokens)
			assert.Equal(t, int64(200), sums[0].UsageOutputTokens)

			// Verify UsageTokenTotals
			rollup, ok := s2.(app.SessionUsageRollup)
			require.True(t, ok)
			inTok, outTok, err := rollup.UsageTokenTotals(ctx, sid)
			require.NoError(t, err)
			assert.Equal(t, int64(100), inTok)
			assert.Equal(t, int64(200), outTok)
		})

		t.Run("QuarantineSurvival", func(t *testing.T) {
			s1, reopen := f.ReopenStore(t)
			fp, _ := twoFingerprints()
			sid := domain.SessionID(fmt.Sprintf("sess-restart-q-%d", time.Now().UnixNano()))
			aleg := fmt.Sprintf("aleg-restart-q-%d", time.Now().UnixNano())
			qTime := time.Unix(1_700_000_100, 0).UTC()

			_, err := s1.Create(ctx, domain.CreateRecord{
				SessionID:         sid,
				ResumeFingerprint: fp,
				Owner:             domain.PrincipalRef{ID: "owner-q"},
				Workspace:         domain.WorkspaceRef{ID: "ws-q"},
				Policy:            domain.PolicyMetadata{PolicyVersion: "v1"},
				ALegID:            aleg,
				ResumeEligible:    true,
				CreatedAt:         qTime.Add(-time.Minute),
			})
			require.NoError(t, err)

			err = s1.Quarantine(ctx, domain.QuarantineInput{
				SessionID:  sid,
				TurnID:     "t-q",
				ReasonCode: "secret_guard_block",
				EventID:    "evt-q-1",
				At:         qTime,
			})
			require.NoError(t, err)

			// Reopen store
			s2 := reopen()

			got, err := s2.LoadByID(ctx, sid)
			require.NoError(t, err)
			assert.True(t, got.Status.IsQuarantined())
			assert.False(t, got.ResumeEligible)
			assert.Equal(t, "secret_guard_block", got.QuarantineReasonCode)
			assert.Equal(t, "evt-q-1", got.QuarantineEventID)
			assert.True(t, got.QuarantinedAt.Equal(qTime))

			// Verify audit was persisted across restart
			audits, err := s2.Audit(ctx, sid, domain.ReadOptions{})
			require.NoError(t, err)
			var blockedFound bool
			for _, a := range audits {
				if a.Action == domain.QuarantineAuditActionSecretGuard && a.Result == domain.QuarantineAuditResultBlocked {
					blockedFound = true
					break
				}
			}
			assert.True(t, blockedFound, "expected secret_guard/blocked audit row across restart")

			// Idempotent second quarantine on reopened store
			err = s2.Quarantine(ctx, domain.QuarantineInput{
				SessionID:  sid,
				TurnID:     "t-q",
				ReasonCode: "secret_guard_block",
				EventID:    "evt-q-1",
				At:         qTime,
			})
			require.NoError(t, err)
		})
	})
}
