package runtime

// Task 1.5 characterization: freeze secure-session/A-leg/route-override lifecycle
// for the future large-payload lane (requirements 6, 7, 14, 19; design 7, 10,
// 12, 16 and steering structure/routing-and-orchestration/testing).
//
// What this proves with real existing seams only:
//   - One Execute performs exactly one principal/scope resolution, one
//     session-open stage, one workspace resolve, one SecureSession.BeginTurn
//     (new: lineage CreateALeg + store Create + TouchActivity; resume:
//     LoadByResumeFingerprint + TouchActivity, no lineage create), one B2BUA
//     FetchALeg after BeginTurn, one route-override Snapshot, one barrier gate
//     (no-op without a test barrier, ordered before B-leg open with one),
//     one secure client-turn recorder call with bounded role/ordinal/part-kind
//     shape (no prompt text), one B-leg open per attempt, one terminal lineage
//     row per attempt, and new-session resume-token return on the client call
//     only (never on backend attempts).
//   - Resume reuses session/A-leg authority, clears the consumed bearer from
//     the call, and returns no new token.
//   - Denial (invalid resume, missing principal, fail-closed workspace) and
//     canceled-context paths open zero B-legs, record zero client turns, and
//     append zero attempt rows.
//   - Detached execution owns a private child A-leg, performs zero BeginTurn
//     store/lineage effects and zero route-override reads, yet still runs the
//     normal submit/B-leg/terminal machinery canonically.
//   - The standard memory continuity store is a route-override-capable
//     composition (implements both b2bua.Store and routeoverride.Store via one
//     object wired as executor Store + RouteOverrideReader). The Bun/SQLite
//     side of the same composition is covered by existing capability/parity
//     suites (continuity/bunstore routeoverride_capability_test.go,
//     dbparity_test.go); this file does not reopen a database to stay hermetic.
//
// What this explicitly does NOT claim (out of scope, needs future tasks):
//   - AssessLargeBody / ExecuteLargeBody / OpenWire do not exist yet (1.1
//     baseline grep for those symbols is zero). No test simulates a
//     nonexistent production assessment; all phased counts go through the real
//     canonical Execute/prepareIdentity path that future wire execution must
//     preserve.
//   - Future wire-native token counting, billing exposure facts, and
//     source/digest contracts are not constructed here; this freeze only proves
//     the current canonical lifecycle counts that Assess must not perturb and
//     Execute must reuse inside the already-certified route domain.
//   - Detached execution stays canonical-only; no detached wire path is
//     characterized.
//
// Test-only, no production diff, no feature flag.

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/execctx"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/routeoverride"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/securesession/adapters/b2bualineage"
	"github.com/aiproxer/aiproxer/internal/core/securesession/adapters/memory"
	"github.com/aiproxer/aiproxer/internal/core/securesession/app"
	"github.com/aiproxer/aiproxer/internal/core/securesession/domain"
	"github.com/aiproxer/aiproxer/internal/core/workspace"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	aipworkspace "github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
)

type freezeContinuity struct {
	*b2bua.MemoryStore
	creates   *atomic.Int32
	fetches   *atomic.Int32
	snapshots *atomic.Int32
}

func (s *freezeContinuity) CreateALeg(ctx context.Context, key string) (b2bua.ALegRecord, error) {
	s.creates.Add(1)
	return s.MemoryStore.CreateALeg(ctx, key)
}

func (s *freezeContinuity) FetchALeg(ctx context.Context, id string) (b2bua.ALegRecord, error) {
	s.fetches.Add(1)
	return s.MemoryStore.FetchALeg(ctx, id)
}

func (s *freezeContinuity) Snapshot(ctx context.Context, aLegID string) (routeoverride.State, error) {
	s.snapshots.Add(1)
	return s.MemoryStore.Snapshot(ctx, aLegID)
}

type freezeSecureStore struct {
	app.Store
	creates *atomic.Int32
	loadsFP *atomic.Int32
	touches *atomic.Int32
}

func (s *freezeSecureStore) Create(ctx context.Context, rec domain.CreateRecord) (domain.Record, error) {
	s.creates.Add(1)
	return s.Store.Create(ctx, rec)
}

func (s *freezeSecureStore) LoadByResumeFingerprint(ctx context.Context, fp domain.TokenFingerprint) (domain.Record, error) {
	s.loadsFP.Add(1)
	return s.Store.LoadByResumeFingerprint(ctx, fp)
}

func (s *freezeSecureStore) TouchActivity(ctx context.Context, id domain.SessionID, at time.Time, src domain.ActivitySource) error {
	s.touches.Add(1)
	return s.Store.TouchActivity(ctx, id, at, src)
}

type freezeSessionOpener struct {
	n *atomic.Int32
}

func (o *freezeSessionOpener) ID() string { return "freeze-session-opener" }

func (o *freezeSessionOpener) Open(_ context.Context, _ session.OpenInput) (session.OpenResult, error) {
	o.n.Add(1)
	return session.OpenResult{}, nil
}

type freezeWorkspaceResolver struct {
	n *atomic.Int32
}

func (r *freezeWorkspaceResolver) Resolve(context.Context) (aipworkspace.WorkspaceView, error) {
	r.n.Add(1)
	return aipworkspace.WorkspaceView{ID: "ws-freeze"}, nil
}

type freezeFailingWorkspaceResolver struct{}

func (freezeFailingWorkspaceResolver) Resolve(context.Context) (aipworkspace.WorkspaceView, error) {
	return aipworkspace.WorkspaceView{}, errors.New("workspace unavailable")
}

type freezeRecorder struct {
	n    *atomic.Int32
	last *atomic.Value // stores app.ClientTurnRecordInput
}

func (r *freezeRecorder) RecordClientTurnAfterGate(_ context.Context, in app.ClientTurnRecordInput) error {
	r.n.Add(1)
	if r.last != nil {
		r.last.Store(in)
	}
	return nil
}

func (r *freezeRecorder) RecordPostHookStreamEvent(context.Context, app.StreamEventRecordInput) error {
	return nil
}

type freezeHarness struct {
	ex          *Executor
	cont        *freezeContinuity
	sec         *freezeSecureStore
	sessionN    *atomic.Int32
	workspaceN  *atomic.Int32
	recorderN   *atomic.Int32
	backendN    *atomic.Int32
	backendTok  *atomic.Value // last backend attempt ResumeToken
	recorderIn  *atomic.Value
	innerSecure *memory.Store
}

func newFreezeHarness(t *testing.T, ws aipworkspace.Resolver) *freezeHarness {
	t.Helper()
	innerB2, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	cont := &freezeContinuity{MemoryStore: innerB2, creates: &atomic.Int32{}, fetches: &atomic.Int32{}, snapshots: &atomic.Int32{}}
	innerSec := memory.New(memory.Options{SimulateDurable: true})
	sec := &freezeSecureStore{Store: innerSec, creates: &atomic.Int32{}, loadsFP: &atomic.Int32{}, touches: &atomic.Int32{}}
	lin := b2bualineage.New(cont)
	mgr, err := app.NewManager(sec, app.NewRandGenerator(testFingerprintKey32(t)), lin, app.ManagerConfig{
		FingerprintKey: testFingerprintKey32(t),
		StoreDurable:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	sessionN := &atomic.Int32{}
	workspaceN := &atomic.Int32{}
	if ws == nil {
		ws = &freezeWorkspaceResolver{n: workspaceN}
	}
	bus := hooks.New(hooks.Config{})
	snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		Workspace: workspace.NewResolverChain([]aipworkspace.Resolver{ws}),
		FeaturePlanes: freezeBundle(testFeatureBundle{
			SessionOpeners: []session.Opener{&freezeSessionOpener{n: sessionN}},
		}),
	})
	recorderN := &atomic.Int32{}
	recorderIn := &atomic.Value{}
	backendN := &atomic.Int32{}
	backendTok := &atomic.Value{}
	backendTok.Store("")
	ex := setSecureSessionDenialMapper(TestExecutor())
	ex.Store = cont
	ex.Bus = bus
	ex.RuntimeSnapshot = snap
	ex.SecureSession = mgr
	ex.RouteOverrideReader = cont
	ex.SecureSessionRecorder = &freezeRecorder{n: recorderN, last: recorderIn}
	ex.SyntheticLocalPrincipal = true
	ex.Now = func() time.Time { return time.Unix(3000, 0).UTC() }
	ex.Rand = routing.NewSeededRng(1)
	ex.Backends = map[string]execbackend.Backend{
		"ok": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(_ context.Context, call aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				backendN.Add(1)
				backendTok.Store(call.Session.ResumeToken)
				return aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventMessageStarted},
					{Kind: aipapi.EventResponseFinished},
				}), nil
			},
		},
	}
	return &freezeHarness{
		ex: ex, cont: cont, sec: sec,
		sessionN: sessionN, workspaceN: workspaceN,
		recorderN: recorderN, backendN: backendN,
		backendTok: backendTok, recorderIn: recorderIn, innerSecure: innerSec,
	}
}

func freezeCall(selector, clientHint string) *aipapi.Call {
	return &aipapi.Call{
		Session: aipapi.SessionRef{ClientSessionID: clientHint},
		Route:   aipapi.RouteIntent{Selector: selector},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("freeze-lifecycle-prompt")},
		}},
	}
}

func freezeCollect(t *testing.T, stream aipapi.EventStream) {
	t.Helper()
	if _, err := aipapi.Collect(context.Background(), stream); err != nil {
		t.Fatal(err)
	}
}

func TestLifecycleFreeze_NewSessionStageCounts(t *testing.T) {
	t.Parallel()
	h := newFreezeHarness(t, nil)
	ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "freeze-user-1"})
	call := freezeCall("ok:m", "freeze-hint-1")
	stream, err := h.ex.Execute(ctx, call)
	if err != nil {
		t.Fatal(err)
	}
	freezeCollect(t, stream)

	if got := h.sessionN.Load(); got != 1 {
		t.Fatalf("session-open stages=%d want 1", got)
	}
	if got := h.workspaceN.Load(); got != 1 {
		t.Fatalf("workspace resolves=%d want 1", got)
	}
	if got := h.sec.creates.Load(); got != 1 {
		t.Fatalf("secure creates (BeginTurn new)=%d want 1", got)
	}
	if got := h.sec.loadsFP.Load(); got != 0 {
		t.Fatalf("resume loads=%d want 0 on new session", got)
	}
	if got := h.sec.touches.Load(); got != 1 {
		t.Fatalf("touch activity=%d want 1 on new session", got)
	}
	if got := h.cont.creates.Load(); got != 1 {
		t.Fatalf("A-leg creates=%d want 1 on new session", got)
	}
	if got := h.cont.fetches.Load(); got != 1 {
		t.Fatalf("A-leg fetches=%d want 1 after BeginTurn", got)
	}
	if got := h.cont.snapshots.Load(); got != 1 {
		t.Fatalf("route-override snapshots=%d want 1", got)
	}
	if got := h.recorderN.Load(); got != 1 {
		t.Fatalf("recorder client turns=%d want 1", got)
	}
	if got := h.backendN.Load(); got != 1 {
		t.Fatalf("B-leg opens=%d want 1", got)
	}
	if call.Session.ResumeToken == "" {
		t.Fatal("new session must return a resume token on the client call")
	}
	if tok, _ := h.backendTok.Load().(string); tok != "" {
		t.Fatalf("backend attempt carried resume bearer %q want empty", tok)
	}
	in, ok := h.recorderIn.Load().(app.ClientTurnRecordInput)
	if !ok {
		t.Fatal("recorder input not captured")
	}
	if len(in.Lines) == 0 || in.Lines[0].Role != "user" {
		t.Fatalf("recorder lines=%+v want normalized user line", in.Lines)
	}
	if strings.Contains(strings.ToLower(strings.Join([]string{in.TraceID, string(in.SessionID), string(in.TurnID)}, " ")), "freeze-lifecycle-prompt") {
		t.Fatal("recorder identity fields must not embed prompt text")
	}
	for _, ln := range in.Lines {
		for _, p := range ln.Parts {
			if strings.Contains(p, "freeze-lifecycle-prompt") {
				t.Fatalf("recorder part must be a kind, not prompt text: %+v", in.Lines)
			}
		}
	}
	if call.Session.ALegID == "" || call.Session.AuthoritativeSessionID == "" {
		t.Fatalf("call missing A-leg/session authority: %+v", call.Session)
	}
	atts, err := h.cont.LoadAttempts(context.Background(), call.Session.ALegID)
	if err != nil {
		t.Fatal(err)
	}
	if len(atts) != 1 || atts[0].Seq != 1 || atts[0].Outcome != aipapi.AttemptSuccess {
		t.Fatalf("terminal lineage=%+v want one success seq 1", atts)
	}
}

func TestLifecycleFreeze_ResumeNextTurnReusesSession(t *testing.T) {
	t.Parallel()
	h := newFreezeHarness(t, nil)
	ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "freeze-user-resume"})
	first := freezeCall("ok:m", "freeze-hint-resume")
	stream, err := h.ex.Execute(ctx, first)
	if err != nil {
		t.Fatal(err)
	}
	freezeCollect(t, stream)
	sid := first.Session.AuthoritativeSessionID
	aLeg := first.Session.ALegID
	token := first.Session.ResumeToken
	if sid == "" || aLeg == "" || token == "" {
		t.Fatalf("seed turn missing authority: %+v", first.Session)
	}
	second := &aipapi.Call{
		Session: aipapi.SessionRef{
			ClientSessionID:        "freeze-hint-resume",
			AuthoritativeSessionID: sid,
			ALegID:                 aLeg,
			ResumeToken:            token,
		},
		Route: first.Route,
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("second turn")},
		}},
	}
	stream2, err := h.ex.Execute(ctx, second)
	if err != nil {
		t.Fatal(err)
	}
	freezeCollect(t, stream2)

	if got := h.sessionN.Load(); got != 2 {
		t.Fatalf("session-open stages=%d want 2 (one per turn)", got)
	}
	if got := h.workspaceN.Load(); got != 2 {
		t.Fatalf("workspace resolves=%d want 2", got)
	}
	if got := h.sec.creates.Load(); got != 1 {
		t.Fatalf("secure creates=%d want 1 (resume must not create)", got)
	}
	if got := h.sec.loadsFP.Load(); got != 1 {
		t.Fatalf("resume loads=%d want 1", got)
	}
	if got := h.sec.touches.Load(); got != 2 {
		t.Fatalf("touches=%d want 2 (one per successful BeginTurn)", got)
	}
	if got := h.cont.creates.Load(); got != 1 {
		t.Fatalf("A-leg creates=%d want 1 (resume reuses A-leg)", got)
	}
	if got := h.cont.fetches.Load(); got != 2 {
		t.Fatalf("A-leg fetches=%d want 2", got)
	}
	if got := h.cont.snapshots.Load(); got != 2 {
		t.Fatalf("snapshots=%d want 2", got)
	}
	if got := h.recorderN.Load(); got != 2 {
		t.Fatalf("recorder turns=%d want 2", got)
	}
	if got := h.backendN.Load(); got != 2 {
		t.Fatalf("B-leg opens=%d want 2", got)
	}
	if second.Session.AuthoritativeSessionID != sid {
		t.Fatalf("resume session id=%q want %q", second.Session.AuthoritativeSessionID, sid)
	}
	if second.Session.ALegID != aLeg {
		t.Fatalf("resume A-leg=%q want %q", second.Session.ALegID, aLeg)
	}
	if second.Session.ResumeToken != "" {
		t.Fatalf("resume must not return a new bearer, got %q", second.Session.ResumeToken)
	}
	atts, err := h.cont.LoadAttempts(context.Background(), aLeg)
	if err != nil {
		t.Fatal(err)
	}
	if len(atts) != 2 || atts[0].Seq != 1 || atts[1].Seq != 2 {
		t.Fatalf("resume lineage=%+v want seq 1,2 on same A-leg", atts)
	}
}

func TestLifecycleFreeze_TrustedScopeWinsForBeginTurnOwner(t *testing.T) {
	t.Parallel()
	h := newFreezeHarness(t, nil)
	trusted := scope.PrincipalScopeView{
		Origin:      scope.OriginClient,
		SubjectKind: scope.SubjectHuman,
		PrincipalID: scope.Known("scope-user"),
		AuthMethod:  scope.Known("test"),
	}
	ctx := scope.WithScope(execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "legacy-user"}), trusted)
	call := freezeCall("ok:m", "freeze-scope")
	stream, err := h.ex.Execute(ctx, call)
	if err != nil {
		t.Fatal(err)
	}
	freezeCollect(t, stream)
	rec, err := h.innerSecure.LoadByALegID(context.Background(), call.Session.ALegID)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Owner.ID != "scope-user" {
		t.Fatalf("BeginTurn owner=%q want trusted scope %q", rec.Owner.ID, "scope-user")
	}
	if got := h.sec.creates.Load(); got != 1 {
		t.Fatalf("creates=%d want exactly 1 with scoped identity", got)
	}
}

func TestLifecycleFreeze_DenialPathsOpenNoBLeg(t *testing.T) {
	t.Parallel()
	t.Run("invalid resume", func(t *testing.T) {
		t.Parallel()
		h := newFreezeHarness(t, nil)
		ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "freeze-user-bad"})
		call := freezeCall("ok:m", "freeze-bad")
		call.Session.ResumeToken = "not-a-valid-resume-token"
		_, err := h.ex.Execute(ctx, call)
		if err == nil {
			t.Fatal("expected denial")
		}
		if !aipapi.IsSessionDenial(err) {
			t.Fatalf("want session denial got %T %v", err, err)
		}
		if got := h.sec.loadsFP.Load(); got != 1 {
			t.Fatalf("resume loads=%d want 1 attempt", got)
		}
		if got := h.sec.creates.Load(); got != 0 {
			t.Fatalf("creates=%d want 0 on denial", got)
		}
		if got := h.recorderN.Load(); got != 0 {
			t.Fatalf("recorder=%d want 0 on pre-recorder denial", got)
		}
		if got := h.backendN.Load(); got != 0 {
			t.Fatalf("B-legs=%d want 0 on denial", got)
		}
		if got := h.cont.snapshots.Load(); got != 0 {
			t.Fatalf("snapshots=%d want 0 before BeginTurn success", got)
		}
	})
	t.Run("missing principal", func(t *testing.T) {
		t.Parallel()
		h := newFreezeHarness(t, nil)
		h.ex.SyntheticLocalPrincipal = false
		_, err := h.ex.Execute(context.Background(), freezeCall("ok:m", "freeze-noprincipal"))
		if err == nil {
			t.Fatal("expected denial")
		}
		if !aipapi.IsSessionDenial(err) {
			t.Fatalf("want session denial got %T %v", err, err)
		}
		if got := h.sec.creates.Load() + h.sec.loadsFP.Load(); got != 0 {
			t.Fatalf("BeginTurn store effects=%d want 0", got)
		}
		if got := h.recorderN.Load(); got != 0 {
			t.Fatalf("recorder=%d want 0", got)
		}
		if got := h.backendN.Load(); got != 0 {
			t.Fatalf("B-legs=%d want 0", got)
		}
	})
	t.Run("workspace fail-closed", func(t *testing.T) {
		t.Parallel()
		h := newFreezeHarness(t, nil)
		bus := hooks.New(hooks.Config{})
		h.ex.Bus = bus
		h.ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
			Workspace: workspace.NewStrictChain([]aipworkspace.Resolver{freezeFailingWorkspaceResolver{}}),
		})
		h.ex.SecureSessionWorkspaceResolveFailClosed = true
		h.ex.SecureSessionRequireWorkspaceID = false
		ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "freeze-user-ws"})
		_, err := h.ex.Execute(ctx, freezeCall("ok:m", "freeze-ws"))
		if err == nil {
			t.Fatal("expected workspace denial")
		}
		if !aipapi.IsSessionDenial(err) {
			t.Fatalf("want session denial got %T %v", err, err)
		}
		if got := h.sec.creates.Load() + h.sec.loadsFP.Load(); got != 0 {
			t.Fatalf("BeginTurn must not run after fail-closed workspace, effects=%d", got)
		}
		if got := h.recorderN.Load(); got != 0 {
			t.Fatalf("recorder=%d want 0", got)
		}
		if got := h.backendN.Load(); got != 0 {
			t.Fatalf("B-legs=%d want 0", got)
		}
	})
	t.Run("canceled before BeginTurn", func(t *testing.T) {
		t.Parallel()
		h := newFreezeHarness(t, nil)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		ctx = execview.WithPrincipal(ctx, execview.PrincipalView{ID: "freeze-user-cancel"})
		_, err := h.ex.Execute(ctx, freezeCall("ok:m", "freeze-cancel"))
		if err == nil {
			t.Fatal("expected cancel error")
		}
		if got := h.backendN.Load(); got != 0 {
			t.Fatalf("B-legs=%d want 0 on canceled request", got)
		}
		if got := h.recorderN.Load(); got != 0 {
			t.Fatalf("recorder=%d want 0 on canceled request", got)
		}
		if got := h.sec.creates.Load() + h.sec.loadsFP.Load(); got != 0 {
			t.Fatalf("BeginTurn store effects=%d want 0 on canceled request", got)
		}
	})
}

func TestLifecycleFreeze_RouteAuthorityBarrierOrdersSnapshotBeforeBLeg(t *testing.T) {
	t.Parallel()
	h := newFreezeHarness(t, nil)
	barrier := newRouteAuthoritySnapshotBarrier()
	ctx := withRouteAuthoritySnapshotBarrier(execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "freeze-user-barrier"}), barrier)
	call := freezeCall("ok:m", "freeze-barrier")
	done := make(chan error, 1)
	var stream aipapi.EventStream
	go func() {
		s, err := h.ex.Execute(ctx, call)
		stream = s
		done <- err
	}()
	if err := barrier.waitUntilArrived(context.Background()); err != nil {
		barrier.releaseWaiters()
		t.Fatalf("barrier arrival: %v", err)
	}
	if got := h.cont.snapshots.Load(); got != 1 {
		barrier.releaseWaiters()
		<-done
		t.Fatalf("snapshots before release=%d want 1", got)
	}
	if got := h.backendN.Load(); got != 0 {
		barrier.releaseWaiters()
		<-done
		t.Fatalf("B-legs before barrier release=%d want 0", got)
	}
	if got := barrier.resolvedALegID(); got == "" {
		barrier.releaseWaiters()
		<-done
		t.Fatal("barrier must carry the resolved A-leg ID")
	}
	barrier.releaseWaiters()
	if err := <-done; err != nil {
		t.Fatalf("execute after release: %v", err)
	}
	freezeCollect(t, stream)
	if got := h.backendN.Load(); got != 1 {
		t.Fatalf("B-legs after release=%d want 1", got)
	}
	if got := h.recorderN.Load(); got != 1 {
		t.Fatalf("recorder=%d want 1", got)
	}
}

func TestLifecycleFreeze_DetachedStaysCanonicalOnly(t *testing.T) {
	t.Parallel()
	h := newFreezeHarness(t, nil)
	parentCtx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "freeze-parent"})
	parent := freezeCall("ok:m", "freeze-parent-hint")
	stream, err := h.ex.Execute(parentCtx, parent)
	if err != nil {
		t.Fatal(err)
	}
	freezeCollect(t, stream)
	baseCreates := h.sec.creates.Load()
	baseLoads := h.sec.loadsFP.Load()
	baseTouches := h.sec.touches.Load()
	baseSnapshots := h.cont.snapshots.Load()
	baseRecorder := h.recorderN.Load()
	baseBackend := h.backendN.Load()
	parentALeg := parent.Session.ALegID
	parentSID := parent.Session.AuthoritativeSessionID
	if parentALeg == "" || parentSID == "" {
		t.Fatalf("parent missing authority: %+v", parent.Session)
	}

	childCall := &aipapi.Call{
		Session: aipapi.SessionRef{
			ClientSessionID:        "child-hint",
			AuthoritativeSessionID: parentSID,
			ALegID:                 parentALeg,
			ContinuityKey:          "child-branch",
			ResumeToken:            parent.Session.ResumeToken,
		},
		Route: aipapi.RouteIntent{Selector: "ok:m"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("extract")},
		}},
	}
	childCtx := execctx.WithDetachedSession(parentCtx, execctx.DetachedSession{
		ParentSessionID:     parentSID,
		ParentALegID:        parentALeg,
		ParentTraceID:       "parent-trace",
		ParentBranchBinding: "captured-parent-branch",
	})
	childStream, err := h.ex.Execute(childCtx, childCall)
	if err != nil {
		t.Fatal(err)
	}
	freezeCollect(t, childStream)

	if got := h.sec.creates.Load() - baseCreates; got != 0 {
		t.Fatalf("detached BeginTurn creates delta=%d want 0", got)
	}
	if got := h.sec.loadsFP.Load() - baseLoads; got != 0 {
		t.Fatalf("detached resume loads delta=%d want 0", got)
	}
	if got := h.sec.touches.Load() - baseTouches; got != 0 {
		t.Fatalf("detached touch delta=%d want 0 (must not touch primary activity)", got)
	}
	if got := h.cont.snapshots.Load() - baseSnapshots; got != 0 {
		t.Fatalf("detached override snapshots delta=%d want 0", got)
	}
	if got := h.recorderN.Load() - baseRecorder; got != 0 {
		t.Fatalf("detached recorder delta=%d want 0 (no primary secure turn)", got)
	}
	if got := h.backendN.Load() - baseBackend; got != 1 {
		t.Fatalf("detached B-leg delta=%d want 1 (canonical auxiliary still executes)", got)
	}
	if childCall.Session.ALegID == "" || childCall.Session.ALegID == parentALeg {
		t.Fatalf("detached child A-leg=%q want private non-parent", childCall.Session.ALegID)
	}
	if childCall.Session.AuthoritativeSessionID != "" || childCall.Session.ResumeToken != "" || childCall.Session.ClientSessionID != "" || childCall.Session.ContinuityKey != "" {
		t.Fatalf("detached child leaked parent authority: %+v", childCall.Session)
	}
	atts, err := h.cont.LoadAttempts(context.Background(), childCall.Session.ALegID)
	if err != nil {
		t.Fatal(err)
	}
	if len(atts) != 1 || atts[0].Outcome != aipapi.AttemptSuccess {
		t.Fatalf("detached terminal lineage=%+v want one success", atts)
	}
}

func TestLifecycleFreeze_RouteOverrideCapableComposition(t *testing.T) {
	t.Parallel()
	inner, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := routeoverride.AsStore(inner); !ok {
		t.Fatal("standard memory continuity must implement routeoverride.Store")
	}
	if _, ok := routeoverride.AsReader(inner); !ok {
		t.Fatal("standard memory continuity must implement routeoverride.Reader")
	}
	leg, err := inner.CreateALeg(context.Background(), "freeze-capable")
	if err != nil {
		t.Fatal(err)
	}
	got, err := inner.Snapshot(context.Background(), leg.ALegID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Active || got.Revision != 0 || got.Selector != "" {
		t.Fatalf("fresh A-leg snapshot=%+v want inactive revision 0", got)
	}
	now := time.Unix(4000, 0).UTC()
	active, err := inner.Replace(context.Background(), leg.ALegID, "ok:m", now)
	if err != nil {
		t.Fatal(err)
	}
	if !active.Active || active.Selector != "ok:m" || active.Revision != 1 {
		t.Fatalf("replace=%+v want active revision 1", active)
	}
	snap, err := inner.Snapshot(context.Background(), leg.ALegID)
	if err != nil {
		t.Fatal(err)
	}
	if !snap.Active || snap.Selector != "ok:m" {
		t.Fatalf("snapshot after replace=%+v", snap)
	}
	snap.Selector = "mutated"
	again, err := inner.Snapshot(context.Background(), leg.ALegID)
	if err != nil {
		t.Fatal(err)
	}
	if again.Selector != "ok:m" {
		t.Fatalf("snapshot must be a value copy, got %+v", again)
	}

	h := newFreezeHarness(t, nil)
	h.ex.RouteOverrideReader = nil
	ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "freeze-nil-reader"})
	call := freezeCall("ok:m", "freeze-nil")
	stream, err := h.ex.Execute(ctx, call)
	if err != nil {
		t.Fatal(err)
	}
	freezeCollect(t, stream)
	if got := h.backendN.Load(); got != 1 {
		t.Fatalf("nil override reader must still execute canonically, B-legs=%d", got)
	}
	if got := h.cont.snapshots.Load(); got != 0 {
		t.Fatalf("nil reader must not read the override store, snapshots=%d", got)
	}
}
