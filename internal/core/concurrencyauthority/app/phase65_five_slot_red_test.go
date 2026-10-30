package app_test

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/concurrencyauthority/app"
	"github.com/aiproxer/aiproxer/internal/core/concurrencyauthority/domain"
)

func TestPhase65_FiveSlotMultiInstanceContention(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 18, 15, 0, 0, 0, time.UTC)
	store := newMemoryStore()
	rules := []domain.Rule{
		func() domain.Rule {
			r := strictRule(5)
			r.ID = "max-active"
			r.RenewBefore = 15 * time.Second
			return r
		}(),
		func() domain.Rule {
			r := strictRule(5)
			r.ID = "max-active-b"
			r.RenewBefore = 15 * time.Second
			return r
		}(),
	}
	svcA := newService(t, rules, store, now)
	svcB := newService(t, rules, store, now)

	var allowed atomic.Int32
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			svc := svcA
			if i%2 == 1 {
				svc = svcB
			}
			res, err := svc.Admit(context.Background(), app.AdmitInput{
				RequestID: fmt.Sprintf("req-%02d", i),
				Scope:     principalScope("alice"),
				Namespace: "default",
			})
			if err != nil {
				t.Errorf("admit: %v", err)
				return
			}
			if res.Kind == domain.DecisionAllow {
				allowed.Add(1)
			}
		}(i)
	}
	wg.Wait()
	if got := allowed.Load(); got != 5 {
		t.Fatalf("allowed=%d want 5 across instances", got)
	}

	q, err := store.QuerySets(context.Background(), app.QuerySetsCommand{Now: now, Limit: 10})
	if err != nil || len(q.Sets) == 0 {
		t.Fatalf("query sets: err=%v n=%d", err, len(q.Sets))
	}
	_, err = store.ReleaseSet(context.Background(), app.ReleaseSetCommand{
		SetID: q.Sets[0].SetID, RequestID: q.Sets[0].RequestID, Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := svcA.Admit(context.Background(), app.AdmitInput{
		RequestID: "req-recover", Scope: principalScope("alice"), Namespace: "default",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Kind != domain.DecisionAllow {
		t.Fatalf("capacity must recover after set release: %+v", res)
	}
}
