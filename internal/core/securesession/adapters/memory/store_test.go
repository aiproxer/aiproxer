package memory_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/securesession/adapters/memory"
	"github.com/aiproxer/aiproxer/internal/core/securesession/app"
	"github.com/aiproxer/aiproxer/internal/core/securesession/domain"
	"github.com/aiproxer/aiproxer/internal/core/securesession/storecontract"
)

func TestMemoryStore_contract(t *testing.T) {
	t.Parallel()
	storecontract.RunAll(t, func(*testing.T) app.Store {
		return memory.New(memory.Options{})
	})
}

func TestMemoryStore_concurrentCreateDistinctIDs(t *testing.T) {
	t.Parallel()
	const n = 128
	s := memory.New(memory.Options{})
	ctx := context.Background()
	var wg sync.WaitGroup
	wg.Add(n)
	for i := range n {
		go func(k int) {
			defer wg.Done()
			var fp domain.TokenFingerprint
			fp[0] = byte(k)
			fp[1] = 0xab
			sid := domain.SessionID(fmt.Sprintf("sess-%d", k))
			_, err := s.Create(ctx, domain.CreateRecord{
				SessionID:         sid,
				ResumeFingerprint: fp,
				Owner:             domain.PrincipalRef{ID: "p"},
				Workspace:         domain.WorkspaceRef{ID: "w"},
				Policy:            domain.PolicyMetadata{PolicyVersion: "1"},
				ALegID:            fmt.Sprintf("a-%d", k),
				ResumeEligible:    true,
				CreatedAt:         time.Unix(1, 0),
			})
			if err != nil {
				t.Errorf("create %d: %v", k, err)
			}
		}(i)
	}
	wg.Wait()
}

func TestMemoryStore_CheckReadiness_mandatoryAuditWithoutSimulateDurable(t *testing.T) {
	t.Parallel()
	s := memory.New(memory.Options{})
	err := s.CheckReadiness(t.Context(), domain.PolicyMetadata{AuditMode: "mandatory"})
	if !errors.Is(err, domain.ErrMandatoryAuditFailure) {
		t.Fatalf("got %v want %v", err, domain.ErrMandatoryAuditFailure)
	}
}

func TestMemoryStore_CheckReadiness_mandatoryAuditWithSimulateDurable(t *testing.T) {
	t.Parallel()
	s := memory.New(memory.Options{SimulateDurable: true})
	if err := s.CheckReadiness(t.Context(), domain.PolicyMetadata{AuditMode: "mandatory"}); err != nil {
		t.Fatal(err)
	}
}

func TestMemoryStore_CheckReadiness_injectedError(t *testing.T) {
	t.Parallel()
	want := domain.ErrStorageUnavailable
	s := memory.New(memory.Options{ReadinessError: want})
	if err := s.CheckReadiness(t.Context(), domain.PolicyMetadata{}); err != want {
		t.Fatalf("got %v want %v", err, want)
	}
}

func TestMemoryStore_twoBLEGOutcomesLatestWins(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := memory.New(memory.Options{})
	fp := domain.TokenFingerprint{}
	fp[0] = 9
	_, err := s.Create(ctx, domain.CreateRecord{
		SessionID: "s2", ResumeFingerprint: fp,
		Owner: domain.PrincipalRef{ID: "o"}, Workspace: domain.WorkspaceRef{},
		ClientHints: domain.ClientHints{}, Policy: domain.PolicyMetadata{PolicyVersion: "1"},
		ALegID: "a", ResumeEligible: true, CreatedAt: time.Unix(1, 0),
	})
	if err != nil {
		t.Fatal(err)
	}
	base := domain.AttemptTrace{SessionID: "s2", TurnID: "t1", ALegID: "a", StartedAt: time.Unix(2, 0)}
	t1 := base
	t1.BLegID, t1.AttemptSeq = "b1", 1
	if err := s.AppendAttemptTrace(ctx, t1); err != nil {
		t.Fatal(err)
	}
	t2 := base
	t2.BLegID, t2.AttemptSeq = "b2", 2
	if err := s.AppendAttemptTrace(ctx, t2); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateAttemptOutcome(ctx, domain.AttemptOutcome{
		SessionID: "s2", TurnID: "t1", BLegID: "b1", Success: false,
		SurfaceState: domain.SurfaceSwallowed, EndedAt: time.Unix(10, 0),
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateAttemptOutcome(ctx, domain.AttemptOutcome{
		SessionID: "s2", TurnID: "t1", BLegID: "b2", Success: true,
		SurfaceState: domain.SurfaceSurfaced, EndedAt: time.Unix(20, 0),
	}); err != nil {
		t.Fatal(err)
	}
	rec, err := s.LoadByID(ctx, "s2")
	if err != nil {
		t.Fatal(err)
	}
	if !rec.LatestAttemptOutcome.Success || rec.LatestAttemptOutcome.BLegID != "b2" {
		t.Fatalf("latest outcome: %#v", rec.LatestAttemptOutcome)
	}
}
