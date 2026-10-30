package routeoverride_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/routeoverride"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

type recordingValidator struct {
	calls []string
	err   error
}

func (v *recordingValidator) ValidateSelector(_ context.Context, raw string) error {
	v.calls = append(v.calls, raw)
	return v.err
}

type recordingStore struct {
	gets     int
	replaces int
	clears   int
	lastID   string
	lastSel  string
	state    routeoverride.State
	getErr   error
	mutErr   error
}

func (s *recordingStore) Snapshot(ctx context.Context, aLegID string) (routeoverride.State, error) {
	return s.Get(ctx, aLegID)
}

func (s *recordingStore) Get(_ context.Context, aLegID string) (routeoverride.State, error) {
	s.gets++
	s.lastID = aLegID
	if s.getErr != nil {
		return routeoverride.State{}, s.getErr
	}
	out := s.state
	if out.ALegID == "" {
		out.ALegID = aLegID
	}
	return out.Clone(), nil
}

func (s *recordingStore) Replace(_ context.Context, aLegID, selector string, _ time.Time) (routeoverride.State, error) {
	s.replaces++
	s.lastID = aLegID
	s.lastSel = selector
	if s.mutErr != nil {
		return routeoverride.State{}, s.mutErr
	}
	s.state = routeoverride.State{
		ALegID:    aLegID,
		Active:    true,
		Selector:  selector,
		Revision:  s.state.Revision + 1,
		UpdatedAt: time.Unix(1, 0).UTC(),
	}
	if s.state.Revision < 1 {
		s.state.Revision = 1
	}
	return s.state.Clone(), nil
}

func (s *recordingStore) Clear(_ context.Context, aLegID string, now time.Time) (routeoverride.State, error) {
	s.clears++
	s.lastID = aLegID
	if s.mutErr != nil {
		return routeoverride.State{}, s.mutErr
	}
	if !s.state.Active {
		out := s.state
		out.ALegID = aLegID
		return out.Clone(), nil
	}
	s.state = routeoverride.State{ALegID: aLegID, Revision: s.state.Revision + 1, UpdatedAt: now.UTC()}
	return s.state.Clone(), nil
}

func testService(t *testing.T, store routeoverride.Store, v routeoverride.SelectorValidator) *routeoverride.Service {
	t.Helper()
	svc, err := routeoverride.NewService(store, v, func() time.Time { return time.Unix(10, 0).UTC() })
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return svc
}

func TestNewService_requiresDeps(t *testing.T) {
	t.Parallel()
	v := &recordingValidator{}
	st := &recordingStore{}
	if _, err := routeoverride.NewService(nil, v, nil); err == nil {
		t.Fatal("expected nil store error")
	}
	if _, err := routeoverride.NewService(st, nil, nil); err == nil {
		t.Fatal("expected nil validator error")
	}
}

func TestService_GetNormalizesALegAndMapsNotFound(t *testing.T) {
	t.Parallel()
	st := &recordingStore{getErr: routeoverride.ErrNotFound}
	svc := testService(t, st, &recordingValidator{})
	_, err := svc.Get(context.Background(), "  a_1  ")
	if !errors.Is(err, routeoverride.ErrNotFound) {
		t.Fatalf("got %v want ErrNotFound", err)
	}
	if st.lastID != "a_1" {
		t.Fatalf("a-leg id passed to store: %q", st.lastID)
	}
}

func TestService_emptyALegDoesNotTouchStore(t *testing.T) {
	t.Parallel()
	st := &recordingStore{}
	svc := testService(t, st, &recordingValidator{})
	if _, err := svc.Get(context.Background(), "  \t"); !errors.Is(err, routeoverride.ErrNotFound) {
		t.Fatalf("Get: %v", err)
	}
	if _, err := svc.Replace(context.Background(), "", "openai:gpt-4"); !errors.Is(err, routeoverride.ErrNotFound) {
		t.Fatalf("Replace: %v", err)
	}
	if _, err := svc.Clear(context.Background(), " "); !errors.Is(err, routeoverride.ErrNotFound) {
		t.Fatalf("Clear: %v", err)
	}
	if st.gets != 0 || st.replaces != 0 || st.clears != 0 {
		t.Fatalf("empty a-leg must not call store: %+v", st)
	}
}

func TestService_ReplaceValidatesBeforeMutate(t *testing.T) {
	t.Parallel()
	st := &recordingStore{}
	v := &recordingValidator{err: errors.New("parse failed")}
	svc := testService(t, st, v)
	_, err := svc.Replace(context.Background(), "a_1", "  openai:gpt-4  ")
	if !errors.Is(err, routeoverride.ErrInvalidSelector) {
		t.Fatalf("got %v want ErrInvalidSelector", err)
	}
	if st.replaces != 0 {
		t.Fatal("store.Replace must not run after failed preflight")
	}
	if len(v.calls) != 1 || v.calls[0] != "openai:gpt-4" {
		t.Fatalf("validator calls: %#v", v.calls)
	}
}

func TestService_ReplaceEnforcesSelectorBoundBeforePreflight(t *testing.T) {
	t.Parallel()
	st := &recordingStore{}
	v := &recordingValidator{}
	svc := testService(t, st, v)
	tooBig := strings.Repeat("a", aipapi.MaxRouteSelectorBytes+1)
	_, err := svc.Replace(context.Background(), "a_1", tooBig)
	if !errors.Is(err, routeoverride.ErrInvalidSelector) {
		t.Fatalf("got %v want ErrInvalidSelector", err)
	}
	if len(v.calls) != 0 {
		t.Fatal("oversized selector must not reach generation preflight")
	}
	if st.replaces != 0 {
		t.Fatal("oversized selector must not mutate store")
	}
}

func TestService_ReplaceRejectsEmptySelector(t *testing.T) {
	t.Parallel()
	st := &recordingStore{}
	v := &recordingValidator{}
	svc := testService(t, st, v)
	_, err := svc.Replace(context.Background(), "a_1", "  ")
	if !errors.Is(err, routeoverride.ErrInvalidSelector) {
		t.Fatalf("got %v want ErrInvalidSelector", err)
	}
	if len(v.calls) != 0 || st.replaces != 0 {
		t.Fatal("empty selector must fail before preflight and mutate")
	}
}

func TestService_ReplaceMapsStoreFailureWithoutMutationClaim(t *testing.T) {
	t.Parallel()
	st := &recordingStore{mutErr: errors.New("disk full")}
	svc := testService(t, st, &recordingValidator{})
	_, err := svc.Replace(context.Background(), "a_1", "openai:gpt-4")
	if !errors.Is(err, routeoverride.ErrUnavailable) {
		t.Fatalf("got %v want ErrUnavailable", err)
	}
}

func TestService_ReplaceMapsRevisionExhausted(t *testing.T) {
	t.Parallel()
	st := &recordingStore{mutErr: routeoverride.ErrRevisionExhausted}
	svc := testService(t, st, &recordingValidator{})
	_, err := svc.Replace(context.Background(), "a_1", "openai:gpt-4")
	if !errors.Is(err, routeoverride.ErrRevisionExhausted) {
		t.Fatalf("got %v want ErrRevisionExhausted", err)
	}
	if errors.Is(err, routeoverride.ErrUnavailable) {
		t.Fatal("revision exhaustion must not be remapped to unavailable")
	}
}

func TestService_GetMapsUnavailable(t *testing.T) {
	t.Parallel()
	st := &recordingStore{getErr: errors.New("db down")}
	svc := testService(t, st, &recordingValidator{})
	_, err := svc.Get(context.Background(), "a_1")
	if !errors.Is(err, routeoverride.ErrUnavailable) {
		t.Fatalf("got %v want ErrUnavailable", err)
	}
}

func TestService_memoryHappyPathIdempotentAndNoBLegs(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	mem, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	leg, err := mem.CreateALeg(ctx, "svc-happy")
	if err != nil {
		t.Fatal(err)
	}
	if err := mem.SetWeightedFirstConsumed(ctx, leg.ALegID, true); err != nil {
		t.Fatal(err)
	}
	clock := time.Unix(50, 0).UTC()
	svc, err := routeoverride.NewService(mem, &recordingValidator{}, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}

	got, err := svc.Get(ctx, leg.ALegID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Active || got.Revision != 0 {
		t.Fatalf("want revision 0 inactive, got %+v", got)
	}

	first, err := svc.Replace(ctx, "  "+leg.ALegID+"  ", "  openai:gpt-4  ")
	if err != nil {
		t.Fatal(err)
	}
	if !first.Active || first.Selector != "openai:gpt-4" || first.Revision != 1 {
		t.Fatalf("first replace: %+v", first)
	}

	again, err := svc.Replace(ctx, leg.ALegID, "openai:gpt-4")
	if err != nil {
		t.Fatal(err)
	}
	if again.Revision != first.Revision || again.UpdatedAt != first.UpdatedAt {
		t.Fatalf("identical replace must be a no-op: first=%+v again=%+v", first, again)
	}

	cleared, err := svc.Clear(ctx, leg.ALegID)
	if err != nil {
		t.Fatal(err)
	}
	if cleared.Active || cleared.Selector != "" || cleared.Revision != 2 {
		t.Fatalf("clear: %+v", cleared)
	}
	if cleared.UpdatedAt.IsZero() {
		t.Fatal("clear tombstone must keep updated_at")
	}

	againClear, err := svc.Clear(ctx, leg.ALegID)
	if err != nil {
		t.Fatal(err)
	}
	if againClear.Revision != cleared.Revision || againClear.UpdatedAt != cleared.UpdatedAt {
		t.Fatalf("repeated clear must be a no-op: %+v vs %+v", againClear, cleared)
	}

	attempts, err := mem.LoadAttempts(ctx, leg.ALegID)
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 0 {
		t.Fatalf("command service must not record attempts: %+v", attempts)
	}
	rec, err := mem.FetchALeg(ctx, leg.ALegID)
	if err != nil {
		t.Fatal(err)
	}
	if !rec.WeightedFirstConsumed {
		t.Fatal("command service must not reset WeightedFirstConsumed")
	}
	interleaved, err := mem.FetchInterleavedState(ctx, leg.ALegID)
	if err != nil {
		t.Fatal(err)
	}
	if !interleaved.Cycle.IsEmpty() {
		t.Fatalf("command service must not write interleaved state: %+v", interleaved)
	}
}
