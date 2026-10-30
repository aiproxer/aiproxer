package app_test

import (
	"context"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/securesession/adapters/memory"
	"github.com/aiproxer/aiproxer/internal/core/securesession/app"
	"github.com/aiproxer/aiproxer/internal/core/securesession/domain"
)

// BenchmarkRecorder_PostHookStream_fullPolicy exercises transcript, usage touch, and audit paths
// (operator audit row per event). Expect bounded allocations: one stream event does store I/O
// (touch, optional transcript seq+append, audit seq+append, optional usage).
func BenchmarkRecorder_PostHookStream_fullPolicy(b *testing.B) {
	ctx := context.Background()
	st := memory.New(memory.Options{})
	sid := domain.SessionID("bench-sess-full")
	fp := domain.TokenFingerprint{}
	fp[5] = 0x11
	_, err := st.Create(ctx, domain.CreateRecord{
		SessionID: sid, ResumeFingerprint: fp,
		Owner: domain.PrincipalRef{ID: "o"}, Workspace: domain.WorkspaceRef{ID: "w"},
		Policy: domain.PolicyMetadata{
			PolicyVersion: "1", TranscriptEnabled: true, AuditMode: "optional",
			RedactionProfile: "standard",
		},
		ALegID: "a-bench", ResumeEligible: true, CreatedAt: time.Unix(1, 0),
	})
	if err != nil {
		b.Fatal(err)
	}
	rec, err := app.NewRecorder(st)
	if err != nil {
		b.Fatal(err)
	}
	pol := domain.PolicyMetadata{
		TranscriptEnabled: true, AuditMode: "optional", RedactionProfile: "standard",
	}
	base := time.Unix(10_000, 0)
	in := app.StreamEventRecordInput{
		SessionID: sid, TurnID: "t-bench", BLegID: "b1", BackendID: "bk",
		Policy: pol, EventKind: "text_delta", EventPayloadJSON: `{"k":"v","n":1}`,
	}
	b.ReportAllocs()

	for i := 0; b.Loop(); i++ {
		in.Now = base.Add(time.Duration(i) * time.Millisecond)
		if err := rec.RecordPostHookStreamEvent(ctx, in); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkRecorder_PostHookStream_minimalPolicy is transcript-disabled, audit-optional light path:
// touch skipped for pure-usage events without EventKind; this variant uses only usage + audit meta.
func BenchmarkRecorder_PostHookStream_minimalPolicy(b *testing.B) {
	ctx := context.Background()
	st := memory.New(memory.Options{})
	sid := domain.SessionID("bench-sess-min")
	fp := domain.TokenFingerprint{}
	fp[6] = 0x22
	_, err := st.Create(ctx, domain.CreateRecord{
		SessionID: sid, ResumeFingerprint: fp,
		Owner: domain.PrincipalRef{ID: "o"}, Workspace: domain.WorkspaceRef{ID: "w"},
		Policy: domain.PolicyMetadata{PolicyVersion: "1", TranscriptEnabled: false, AuditMode: "optional"},
		ALegID: "a-min", ResumeEligible: true, CreatedAt: time.Unix(1, 0),
	})
	if err != nil {
		b.Fatal(err)
	}
	rec, err := app.NewRecorder(st)
	if err != nil {
		b.Fatal(err)
	}
	pol := domain.PolicyMetadata{TranscriptEnabled: false, AuditMode: "optional"}
	base := time.Unix(20_000, 0)
	in := app.StreamEventRecordInput{
		SessionID: sid, TurnID: "t-min", BLegID: "b1", BackendID: "bk",
		Policy: pol, EventKind: "usage_delta", IsUsageEvent: true,
		InputTokens: 2, OutputTokens: 3,
	}
	b.ReportAllocs()

	for i := 0; b.Loop(); i++ {
		in.Now = base.Add(time.Duration(i) * time.Millisecond)
		if err := rec.RecordPostHookStreamEvent(ctx, in); err != nil {
			b.Fatal(err)
		}
	}
}
