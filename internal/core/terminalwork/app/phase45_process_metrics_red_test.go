package app_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/terminalwork"
	"github.com/aiproxer/aiproxer/internal/core/terminalwork/app"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
)

type recordingProcessMetrics struct {
	mu          sync.Mutex
	transitions [][3]string
	refreshes   int
}

func (r *recordingProcessMetrics) ObserveTransition(state, kind, providerID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.transitions = append(r.transitions, [3]string{state, kind, providerID})
}

func (r *recordingProcessMetrics) RefreshAfterBatch(context.Context) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.refreshes++
}

func (r *recordingProcessMetrics) snapshot() (transitions [][3]string, refreshes int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	transitions = append([][3]string(nil), r.transitions...)
	return transitions, r.refreshes
}

func TestPhase45_ProcessDueObservesTransitionsAndRefreshes(t *testing.T) {
	t.Parallel()
	clock := &manualClock{t: time.Date(2026, 7, 18, 10, 0, 0, 0, time.UTC)}
	store := openStore(t, clock)
	obs := &recordingProcessMetrics{}
	ok := &fakeProvider{
		id: "prov-ok", kinds: []sdk.WorkKind{sdk.WorkKindSettleRequestProvider}, ver: "1",
	}
	retry := &fakeProvider{
		id: "prov-retry", kinds: []sdk.WorkKind{sdk.WorkKindSettleRequestProvider}, ver: "1",
		fn: func(context.Context, terminalwork.WorkRecord, string) error { return app.ErrProviderTimeout },
	}
	bad := &fakeProvider{
		id: "prov-bad", kinds: []sdk.WorkKind{sdk.WorkKindSettleRequestProvider}, ver: "1",
		fn: func(context.Context, terminalwork.WorkRecord, string) error { return app.ErrInvalidPayload },
	}
	reg := app.NewRegistry()
	_ = reg.Register(ok)
	_ = reg.Register(retry)
	_ = reg.Register(bad)
	seedPending(t, store, clock, sampleRec("w-ok", "sk-ok", "prov-ok", sdk.WorkKindSettleRequestProvider))
	seedPending(t, store, clock, sampleRec("w-retry", "sk-retry", "prov-retry", sdk.WorkKindSettleRequestProvider))
	seedPending(t, store, clock, sampleRec("w-bad", "sk-bad", "prov-bad", sdk.WorkKindSettleRequestProvider))
	p := newProc(t, store, reg, clock, app.Config{Metrics: obs, ClaimLimit: 8})
	if err := p.ProcessDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	transitions, refreshes := obs.snapshot()
	if refreshes != 1 {
		t.Fatalf("RefreshAfterBatch calls=%d want 1", refreshes)
	}
	want := map[string]bool{
		"completed|settle_request_provider|prov-ok":          false,
		"retry|settle_request_provider|prov-retry":           false,
		"quarantined|settle_request_provider|prov-bad":       false,
		"validation_failed|settle_request_provider|prov-bad": false,
	}
	for _, tr := range transitions {
		key := tr[0] + "|" + tr[1] + "|" + tr[2]
		if _, ok := want[key]; ok {
			want[key] = true
		}
	}
	for k, seen := range want {
		if !seen {
			t.Fatalf("missing transition %s in %v", k, transitions)
		}
	}
}

type failingClaimStore struct {
	app.WorkStore
}

func (f failingClaimStore) ClaimDue(context.Context, terminalwork.ClaimDueCommand) ([]terminalwork.WorkRecord, error) {
	return nil, errors.New("claim boom: secret=CLAIM_SECRET")
}

func TestPhase45_ProcessDueAcquireFailureObservesBoundedState(t *testing.T) {
	t.Parallel()
	clock := &manualClock{t: time.Date(2026, 7, 18, 10, 8, 0, 0, time.UTC)}
	store := openStore(t, clock)
	obs := &recordingProcessMetrics{}
	entered := make(chan struct{}, 1)
	block := make(chan struct{})
	prov := &fakeProvider{
		id: "prov-a", kinds: []sdk.WorkKind{sdk.WorkKindSettleRequestProvider}, ver: "1",
		enteredCh: entered,
		blockCh:   block,
	}
	reg := app.NewRegistry()
	_ = reg.Register(prov)
	seedPending(t, store, clock, sampleRec("w-acq-1", "sk-acq-1", "prov-a", sdk.WorkKindSettleRequestProvider))
	seedPending(t, store, clock, sampleRec("w-acq-2", "sk-acq-2", "prov-a", sdk.WorkKindSettleRequestProvider))
	p := newProc(t, store, reg, clock, app.Config{Metrics: obs, GlobalMax: 1, ClaimLimit: 2, PerProviderMax: 1})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.ProcessDue(ctx) }()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("first invoke did not start")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ProcessDue did not return")
	}
	close(block)
	transitions, _ := obs.snapshot()
	found := false
	for _, tr := range transitions {
		if tr[0] == app.TransitionAcquireFailed {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("want %s; got %v", app.TransitionAcquireFailed, transitions)
	}
}

func TestPhase45_ProcessDueClaimFailureObservesAndStillRefreshes(t *testing.T) {
	t.Parallel()
	clock := &manualClock{t: time.Date(2026, 7, 18, 10, 5, 0, 0, time.UTC)}
	base := openStore(t, clock)
	obs := &recordingProcessMetrics{}
	reg := app.NewRegistry()
	_ = reg.Register(&fakeProvider{id: "prov-a", kinds: []sdk.WorkKind{sdk.WorkKindSettleRequestProvider}, ver: "1"})
	p := newProc(t, failingClaimStore{WorkStore: base}, reg, clock, app.Config{Metrics: obs})
	err := p.ProcessDue(context.Background())
	if err == nil {
		t.Fatal("want claim error")
	}
	transitions, refreshes := obs.snapshot()
	if refreshes != 1 {
		t.Fatalf("RefreshAfterBatch calls=%d want 1 even on claim error", refreshes)
	}
	found := false
	for _, tr := range transitions {
		if tr[0] != app.TransitionClaimFailed {
			continue
		}
		found = true
		for _, part := range tr {
			if strings.Contains(part, "CLAIM_SECRET") {
				t.Fatalf("raw claim error leaked into observation: %v", tr)
			}
		}
	}
	if !found {
		t.Fatalf("want %s transition; got %v", app.TransitionClaimFailed, transitions)
	}
}
