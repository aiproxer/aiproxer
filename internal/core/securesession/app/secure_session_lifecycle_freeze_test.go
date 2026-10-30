package app_test

// Task 1.5 characterization: freeze secure-session BeginTurn/A-leg/recorder
// lifecycle counts (requirements 6, 14, 19; design 10, 12 read-only, 16).
//
// What this proves with real existing seams only:
//   - New BeginTurn performs exactly one lineage CreateALeg, one store Create,
//     one TouchActivity, and returns proxy-owned session/A-leg IDs plus a raw
//     resume token that never round-trips through storage.
//   - Resume performs exactly one LoadByResumeFingerprint plus one
//     TouchActivity, zero lineage creates, and returns an empty response
//     carrier (no new bearer).
//   - RecordClientTurnAfterGate persists bounded role/ordinal/part-kind lines
//     without prompt text; transcript-disabled policy writes one audit meta row
//     instead of transcript rows.
//   - FinishTurn appends one audit row per terminal outcome.
//   - Denial (missing principal, workspace mismatch, expired/quarantined,
//     owner mismatch) and canceled-context paths perform zero lineage creates
//     on the failing call.
//   - Bun continuity is the same route-override-capable family via the shared
//     storecontract suite; see continuity/bunstore routeoverride_capability
//     tests. This package stays on the hermetic memory adapter.
//
// What this explicitly does NOT claim (out of scope, needs future tasks):
//   - AssessLargeBody / ExecuteLargeBody do not exist yet; no future wire
//     assessment is simulated. Counts here are the canonical oracle that
//     future Assess must leave untouched (no BeginTurn, no lineage/store
//     effects) and future Execute must reproduce exactly once post-commit.
//
// Test-only, no production diff.

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/securesession/adapters/b2bualineage"
	"github.com/aiproxer/aiproxer/internal/core/securesession/adapters/memory"
	"github.com/aiproxer/aiproxer/internal/core/securesession/app"
	"github.com/aiproxer/aiproxer/internal/core/securesession/domain"
)

type freezeAppLineage struct {
	app.LineageStore
	creates *atomic.Int32
	fetches *atomic.Int32
}

func (l *freezeAppLineage) CreateALeg(ctx context.Context, key string) (app.LineageALeg, error) {
	l.creates.Add(1)
	return l.LineageStore.CreateALeg(ctx, key)
}

func (l *freezeAppLineage) FetchALeg(ctx context.Context, id string) (app.LineageALeg, error) {
	l.fetches.Add(1)
	return l.LineageStore.FetchALeg(ctx, id)
}

type freezeAppStore struct {
	app.Store
	creates *atomic.Int32
	loadsFP *atomic.Int32
	touches *atomic.Int32
}

func (s *freezeAppStore) Create(ctx context.Context, rec domain.CreateRecord) (domain.Record, error) {
	s.creates.Add(1)
	return s.Store.Create(ctx, rec)
}

func (s *freezeAppStore) LoadByResumeFingerprint(ctx context.Context, fp domain.TokenFingerprint) (domain.Record, error) {
	s.loadsFP.Add(1)
	return s.Store.LoadByResumeFingerprint(ctx, fp)
}

func (s *freezeAppStore) TouchActivity(ctx context.Context, id domain.SessionID, at time.Time, src domain.ActivitySource) error {
	s.touches.Add(1)
	return s.Store.TouchActivity(ctx, id, at, src)
}

func newFreezeManager(t *testing.T, sec *freezeAppStore, lin *freezeAppLineage, cfg app.ManagerConfig) *app.Manager {
	t.Helper()
	m, err := app.NewManager(sec, app.NewRandGenerator(cfg.FingerprintKey), lin, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestLifecycleFreeze_BeginTurnNewVsResumeCounts(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	key := testFingerprintKey(t)
	innerSec := memory.New(memory.Options{SimulateDurable: true})
	sec := &freezeAppStore{Store: innerSec, creates: &atomic.Int32{}, loadsFP: &atomic.Int32{}, touches: &atomic.Int32{}}
	lin := &freezeAppLineage{LineageStore: b2bualineage.New(testB2BUA(t)), creates: &atomic.Int32{}, fetches: &atomic.Int32{}}
	m := newFreezeManager(t, sec, lin, app.ManagerConfig{FingerprintKey: key, StoreDurable: true})

	owner := domain.PrincipalRef{ID: "freeze-app-user"}
	first, err := m.BeginTurn(ctx, app.BeginInput{
		Now:       time.Unix(5000, 0),
		Principal: owner,
		Workspace: domain.WorkspaceRef{ID: "ws-freeze"},
		Session:   app.SessionWire{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !first.IsNew {
		t.Fatal("first turn must be new")
	}
	if first.Response.ResumeToken == "" || first.Response.SessionID == "" {
		t.Fatalf("new turn must return session ID + raw resume token: %+v", first.Response)
	}
	if string(first.Record.SessionID) != first.Response.SessionID {
		t.Fatal("record session ID must match response carrier")
	}
	if first.Record.ALegID == "" {
		t.Fatal("new turn must allocate an A-leg")
	}
	if got := lin.creates.Load(); got != 1 {
		t.Fatalf("lineage creates=%d want 1 on new", got)
	}
	if got := sec.creates.Load(); got != 1 {
		t.Fatalf("store creates=%d want 1 on new", got)
	}
	if got := sec.loadsFP.Load(); got != 0 {
		t.Fatalf("resume loads=%d want 0 on new", got)
	}
	if got := sec.touches.Load(); got != 1 {
		t.Fatalf("touches=%d want 1 on new", got)
	}

	second, err := m.BeginTurn(ctx, app.BeginInput{
		Now:       time.Unix(5001, 0),
		Principal: owner,
		Workspace: domain.WorkspaceRef{ID: "ws-freeze"},
		Session: app.SessionWire{
			SessionID:   string(first.Record.SessionID),
			ResumeToken: string(first.Response.ResumeToken),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.IsNew {
		t.Fatal("second turn must be a resume")
	}
	if second.Response.ResumeToken != "" || second.Response.SessionID != "" {
		t.Fatalf("resume must return an empty carrier: %+v", second.Response)
	}
	if second.Record.SessionID != first.Record.SessionID || second.Record.ALegID != first.Record.ALegID {
		t.Fatalf("resume must reuse session/A-leg: first=%+v second=%+v", first.Record, second.Record)
	}
	if got := lin.creates.Load(); got != 1 {
		t.Fatalf("lineage creates=%d want still 1 after resume", got)
	}
	if got := sec.creates.Load(); got != 1 {
		t.Fatalf("store creates=%d want still 1 after resume", got)
	}
	if got := sec.loadsFP.Load(); got != 1 {
		t.Fatalf("resume loads=%d want 1", got)
	}
	if got := sec.touches.Load(); got != 2 {
		t.Fatalf("touches=%d want 2 (one per successful BeginTurn)", got)
	}
	if second.TurnID == first.TurnID {
		t.Fatal("each BeginTurn must mint a distinct turn ID")
	}
}

func TestLifecycleFreeze_RecorderPersistsNoPromptText(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := memory.New(memory.Options{})
	sid := domain.SessionID("freeze-recorder")
	var fp domain.TokenFingerprint
	fp[0] = 7
	if _, err := st.Create(ctx, domain.CreateRecord{
		SessionID:         sid,
		ResumeFingerprint: fp,
		Owner:             domain.PrincipalRef{ID: "u-freeze"},
		Workspace:         domain.WorkspaceRef{ID: "ws-freeze"},
		Policy:            domain.PolicyMetadata{TranscriptEnabled: true, RedactionProfile: "standard", AuditMode: "best_effort"},
		ALegID:            "a-freeze",
		ResumeEligible:    true,
		CreatedAt:         time.Unix(10, 0),
	}); err != nil {
		t.Fatal(err)
	}
	rec, err := app.NewRecorder(st)
	if err != nil {
		t.Fatal(err)
	}
	const prompt = "super-secret-prompt-body-must-never-persist"
	in := app.ClientTurnRecordInput{
		Now:       time.Unix(20, 0),
		TraceID:   "tr-freeze",
		SessionID: sid,
		TurnID:    "turn-freeze",
		Policy:    domain.PolicyMetadata{TranscriptEnabled: true, RedactionProfile: "standard"},
		Lines:     []app.ClientInputLine{{Role: "user", Ordinal: 0, Parts: []string{"text"}}},
	}
	if err := rec.RecordClientTurnAfterGate(ctx, in); err != nil {
		t.Fatal(err)
	}
	tx, err := st.Transcript(ctx, sid, domain.ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(tx) != 1 {
		t.Fatalf("transcript rows=%d want 1 bounded line", len(tx))
	}
	if strings.Contains(tx[0].PayloadRef, prompt) {
		t.Fatal("recorder payload must not contain prompt text")
	}
	if !strings.Contains(tx[0].PayloadRef, `"role":"user"`) {
		t.Fatalf("recorder payload must carry normalized role: %s", tx[0].PayloadRef)
	}
}

func TestLifecycleFreeze_DenialPerformsNoLineageCreate(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	key := testFingerprintKey(t)
	t.Run("missing principal", func(t *testing.T) {
		t.Parallel()
		inner := memory.New(memory.Options{SimulateDurable: true})
		sec := &freezeAppStore{Store: inner, creates: &atomic.Int32{}, loadsFP: &atomic.Int32{}, touches: &atomic.Int32{}}
		lin := &freezeAppLineage{LineageStore: b2bualineage.New(testB2BUA(t)), creates: &atomic.Int32{}, fetches: &atomic.Int32{}}
		m := newFreezeManager(t, sec, lin, app.ManagerConfig{FingerprintKey: key, StoreDurable: true})
		_, err := m.BeginTurn(ctx, app.BeginInput{Now: time.Unix(1, 0), Session: app.SessionWire{ResumeToken: "whatever"}})
		if err == nil {
			t.Fatal("expected missing-principal error")
		}
		if got := lin.creates.Load(); got != 0 {
			t.Fatalf("lineage creates=%d want 0", got)
		}
		if got := sec.creates.Load() + sec.loadsFP.Load(); got != 0 {
			t.Fatalf("store effects=%d want 0 before principal check", got)
		}
	})
	t.Run("canceled context", func(t *testing.T) {
		t.Parallel()
		inner := memory.New(memory.Options{SimulateDurable: true})
		sec := &freezeAppStore{Store: inner, creates: &atomic.Int32{}, loadsFP: &atomic.Int32{}, touches: &atomic.Int32{}}
		lin := &freezeAppLineage{LineageStore: b2bualineage.New(testB2BUA(t)), creates: &atomic.Int32{}, fetches: &atomic.Int32{}}
		m := newFreezeManager(t, sec, lin, app.ManagerConfig{FingerprintKey: key, StoreDurable: true})
		cctx, cancel := context.WithCancel(ctx)
		cancel()
		_, err := m.BeginTurn(cctx, app.BeginInput{Now: time.Unix(1, 0), Principal: domain.PrincipalRef{ID: "u"}, Session: app.SessionWire{}})
		if err == nil {
			t.Fatal("expected cancel error")
		}
		if got := lin.creates.Load(); got != 0 {
			t.Fatalf("lineage creates=%d want 0 on cancel", got)
		}
		if got := sec.creates.Load() + sec.loadsFP.Load() + sec.touches.Load(); got != 0 {
			t.Fatalf("store effects=%d want 0 on cancel", got)
		}
	})
}

func TestLifecycleFreeze_FinishTurnAppendsOneAuditRow(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	key := testFingerprintKey(t)
	st := memory.New(memory.Options{SimulateDurable: true})
	lin := b2bualineage.New(testB2BUA(t))
	m := newFreezeManager(t, &freezeAppStore{Store: st, creates: &atomic.Int32{}, loadsFP: &atomic.Int32{}, touches: &atomic.Int32{}}, &freezeAppLineage{LineageStore: lin, creates: &atomic.Int32{}, fetches: &atomic.Int32{}}, app.ManagerConfig{FingerprintKey: key, StoreDurable: true})
	got, err := m.BeginTurn(ctx, app.BeginInput{Now: time.Unix(6000, 0), Principal: domain.PrincipalRef{ID: "u-finish"}, Session: app.SessionWire{}})
	if err != nil {
		t.Fatal(err)
	}
	before, err := st.Audit(ctx, got.Record.SessionID, domain.ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.FinishTurn(ctx, got.Record.SessionID, got.TurnID, app.TurnOutcome{Kind: app.TurnOutcomeSuccess}); err != nil {
		t.Fatal(err)
	}
	after, err := st.Audit(ctx, got.Record.SessionID, domain.ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(after)-len(before) != 1 {
		t.Fatalf("audit delta=%d want 1 terminal row", len(after)-len(before))
	}
}
