package memory_test

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/securesession/adapters/memory"
	"github.com/aiproxer/aiproxer/internal/core/securesession/domain"
)

// TestMemoryStore_concurrentCreateLoadTouchTranscriptUsage stresses Create, fingerprint/A-leg
// lookups, activity touch, transcript append, and usage under many goroutines (race detector).
func TestMemoryStore_concurrentCreateLoadTouchTranscriptUsage(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := memory.New(memory.Options{})

	const workers = 96
	const rounds = 8
	var wg sync.WaitGroup
	wg.Add(workers)
	for w := range workers {
		go func(w int) {
			defer wg.Done()
			for r := range rounds {
				fp := domain.TokenFingerprint{}
				fp[0] = byte(w)
				fp[1] = byte(r)
				fp[2] = byte(w >> 8)
				sid := domain.SessionID(fmt.Sprintf("race-sess-%d-%d", w, r))
				aLeg := fmt.Sprintf("race-aleg-%d-%d", w, r)
				_, err := s.Create(ctx, domain.CreateRecord{
					SessionID:         sid,
					ResumeFingerprint: fp,
					Owner:             domain.PrincipalRef{ID: "owner-" + strconv.Itoa(w)},
					Workspace:         domain.WorkspaceRef{ID: "ws"},
					Policy: domain.PolicyMetadata{
						PolicyVersion: "v", TranscriptEnabled: true, AuditMode: "optional",
					},
					ALegID:         aLeg,
					ResumeEligible: true,
					CreatedAt:      time.Unix(1, int64(w*rounds+r)),
				})
				if err != nil {
					t.Errorf("create w=%d r=%d: %v", w, r, err)
					return
				}
				if _, err := s.LoadByResumeFingerprint(ctx, fp); err != nil {
					t.Errorf("LoadByResumeFingerprint w=%d r=%d: %v", w, r, err)
					return
				}
				if _, err := s.LoadByALegID(ctx, aLeg); err != nil {
					t.Errorf("LoadByALegID w=%d r=%d: %v", w, r, err)
					return
				}
				touchAt := time.Unix(1000, int64(w*1000000+r))
				if err := s.TouchActivity(ctx, sid, touchAt, domain.ActivityClientRequest); err != nil {
					t.Errorf("TouchActivity w=%d r=%d: %v", w, r, err)
					return
				}
				seq, err := s.NextTranscriptSeq(ctx, sid)
				if err != nil {
					t.Errorf("NextTranscriptSeq w=%d r=%d: %v", w, r, err)
					return
				}
				if err := s.AppendTranscript(ctx, domain.TranscriptItem{
					SessionID: sid, TurnID: "t1", Seq: seq, EventKind: "user",
					PayloadRef: "p", CreatedAt: touchAt,
				}); err != nil {
					t.Errorf("AppendTranscript w=%d r=%d: %v", w, r, err)
					return
				}
				if err := s.AddUsage(ctx, domain.UsageDelta{
					SessionID: sid, TurnID: "t1", BLegID: "b1",
					InputTokens: 1, OutputTokens: 2,
				}); err != nil {
					t.Errorf("AddUsage w=%d r=%d: %v", w, r, err)
					return
				}
			}
		}(w)
	}
	wg.Wait()

	if _, err := s.Summary(ctx, domain.SummaryQuery{Limit: 500}); err != nil {
		t.Fatal(err)
	}
}
