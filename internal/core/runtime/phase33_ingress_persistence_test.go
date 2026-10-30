package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/authoritycoord"
	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/metering/checkpoint"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	accountingapp "github.com/aiproxer/aiproxer/internal/core/tokenaccounting/app"
	accountingpreflight "github.com/aiproxer/aiproxer/internal/core/tokenaccounting/preflight"
	usagelimit "github.com/aiproxer/aiproxer/internal/core/usagelimit"
	"github.com/aiproxer/aiproxer/internal/infra/metering/journalstore"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/authority"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

type phase33RequestProvider struct {
	id    string
	scope atomic.Value // scope.PrincipalScopeView
}

func (p *phase33RequestProvider) AdmitRequest(_ context.Context, in authority.RequestAdmission) (authority.Decision, error) {
	p.scope.Store(in.Scope)
	return authority.Decision{Kind: authority.DecisionAllow, Evidence: authority.SafeEvidence{Category: "ok", Message: "allow"}}, nil
}

func (p *phase33RequestProvider) SettleRequest(context.Context, authority.RequestSettlement) (authority.Settlement, error) {
	return authority.Settlement{}, nil
}

func (p *phase33RequestProvider) ReleaseRequest(context.Context, authority.RequestRelease) error {
	return nil
}

func TestPhase33_FrontendIngress_AppendFailureFailClosed(t *testing.T) {
	t.Parallel()
	recorder := &failingMeteringRecorder{err: errors.New("journal unavailable")}
	prov := &phase33RequestProvider{id: "quota"}
	ex := &Executor{AccountingRuntime: AccountingRuntime{MeteringRecorder: recorder}}
	ex.RequestCoordinator = &authoritycoord.RequestCoordinator{
		Slots: []authoritycoord.RequestSlot{{
			ID: "quota", Class: authoritycoord.PriorityQuotaBudgetRate, Provider: prov, Strength: authority.StrengthRequired,
		}},
	}
	ctx, _, err := captureFrontendIngressBeforeSubmit(context.Background(), aipapi.Call{ID: "req-fail"},
		scope.PrincipalScopeView{}, time.Unix(1, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	_, err = ex.admitRequestAuthorityOnce(ctx, "req-fail", "a-1", "trace-fail", scope.PrincipalScopeView{})
	if err == nil {
		t.Fatal("strict FE ingress persist failure must fail-closed before request authority")
	}
}

func TestPhase33_FrontendIngress_RestartReconstructionNoContentLeak(t *testing.T) {
	t.Parallel()
	store, err := journalstore.NewMemoryStore(journalstore.MemoryConfig{StoreID: "p33-fe-restart"})
	if err != nil {
		t.Fatal(err)
	}
	ex := &Executor{AccountingRuntime: AccountingRuntime{MeteringRecorder: store}}
	ex.Now = func() time.Time { return time.Unix(41, 0).UTC() }
	ex.RequestCoordinator = &authoritycoord.RequestCoordinator{
		Slots: []authoritycoord.RequestSlot{{
			ID: "quota", Class: authoritycoord.PriorityQuotaBudgetRate, Provider: &phase33RequestProvider{id: "quota"}, Strength: authority.StrengthRequired,
		}},
	}
	secret := "PROMPT-SECRET-DO-NOT-JOURNAL"
	trusted := scope.PrincipalScopeView{PrincipalID: scope.Known("restart-prin")}
	ctx, holder, err := captureFrontendIngressBeforeSubmit(context.Background(), aipapi.Call{
		ID: "req-fe-restart",
		Messages: []aipapi.Message{{
			Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart(secret)},
		}},
	}, trusted, time.Unix(1, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	holder.MergeFrontendIngressQuantities([]metering.Quantity{
		{Component: metering.ComponentInputToken, Unit: metering.UnitToken, Value: 7, Present: true},
	})
	if _, err := ex.admitRequestAuthorityOnce(ctx, "req-fe-restart", "a-1", "t-restart", scope.PrincipalScopeView{}); err != nil {
		t.Fatal(err)
	}
	page, err := store.List(context.Background(), metering.Query{StreamID: "customer-request:req-fe-restart", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Facts) != 1 {
		t.Fatalf("restart want 1 FE ingress fact, got %d", len(page.Facts))
	}
	f := page.Facts[0]
	if f.Boundary != metering.BoundaryFrontendIngress || f.Perspective != metering.PerspectiveCustomer {
		t.Fatalf("reconstructed fact plane=%s/%s", f.Boundary, f.Perspective)
	}
	if f.Scope.PrincipalID.String() != "restart-prin" {
		t.Fatalf("reconstructed scope=%q", f.Scope.PrincipalID.String())
	}
	in, ok := checkpoint.QuantityComponentValue(f.Quantities, metering.ComponentInputToken)
	if !ok || in != 7 {
		t.Fatalf("reconstructed input qty=%d ok=%v", in, ok)
	}
	raw, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), secret) {
		t.Fatal("journal fact must not capture raw prompt content (D14)")
	}
}

func TestPhase33_FrontendIngress_ReplayIdempotent(t *testing.T) {
	t.Parallel()
	store, err := journalstore.NewMemoryStore(journalstore.MemoryConfig{StoreID: "p33-fe"})
	if err != nil {
		t.Fatal(err)
	}
	ex := &Executor{AccountingRuntime: AccountingRuntime{MeteringRecorder: store}}
	ex.Now = func() time.Time { return time.Unix(40, 0).UTC() }
	ex.RequestCoordinator = &authoritycoord.RequestCoordinator{
		Slots: []authoritycoord.RequestSlot{{
			ID: "quota", Class: authoritycoord.PriorityQuotaBudgetRate, Provider: &phase33RequestProvider{id: "quota"}, Strength: authority.StrengthRequired,
		}},
	}
	call := aipapi.Call{ID: "req-replay", Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("x")}}}}
	ctx, _, err := captureFrontendIngressBeforeSubmit(context.Background(), call, scope.PrincipalScopeView{}, time.Unix(1, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	ctx, err = ex.admitRequestAuthorityOnce(ctx, "req-replay", "a-1", "t1", scope.PrincipalScopeView{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = ex.admitRequestAuthorityOnce(ctx, "req-replay", "a-1", "t1", scope.PrincipalScopeView{})
	if err != nil {
		t.Fatalf("replay admit: %v", err)
	}
	page, err := store.List(context.Background(), metering.Query{StreamID: "customer-request:req-replay", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Facts) != 1 {
		t.Fatalf("want 1 customer ingress fact after replay, got %d", len(page.Facts))
	}
}

func TestPhase33_BackendIngress_OperatorAttemptStream_AndRestart(t *testing.T) {
	t.Parallel()
	store, err := journalstore.NewMemoryStore(journalstore.MemoryConfig{StoreID: "p33-be"})
	if err != nil {
		t.Fatal(err)
	}
	ex := &Executor{AccountingRuntime: AccountingRuntime{MeteringRecorder: store}}
	ex.Now = func() time.Time { return time.Unix(50, 0).UTC() }
	holder := &checkpoint.RequestHolder{}
	_, err = holder.StoreBackendIngress(checkpoint.BackendIngressInput{
		Call:      aipapi.Call{ID: "req-be-33", Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("final")}}}},
		AttemptID: "att-1", BLegID: "att-1", CheckpointID: "operator-attempt:att-1", StreamID: "operator-attempt:att-1",
		Scope: scope.PrincipalScopeView{PrincipalID: scope.Known("op-prin")},
		Now:   time.Unix(2, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	holder.MergeBackendIngressQuantities("att-1", []metering.Quantity{
		{Component: metering.ComponentInputToken, Unit: metering.UnitToken, Value: 42, Present: true},
	})
	factID, err := ex.persistBackendIngressFact(context.Background(), holder, "att-1")
	if err != nil || factID == "" {
		t.Fatalf("persist: id=%q err=%v", factID, err)
	}
	page, err := store.List(context.Background(), metering.Query{StreamID: "operator-attempt:att-1", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Facts) != 1 {
		t.Fatalf("want 1 operator ingress, got %d", len(page.Facts))
	}
	if page.Facts[0].StreamID != "operator-attempt:att-1" {
		t.Fatalf("StreamID=%q", page.Facts[0].StreamID)
	}
	in, ok := checkpoint.QuantityComponentValue(page.Facts[0].Quantities, metering.ComponentInputToken)
	if !ok || in != 42 {
		t.Fatalf("restart quantities input=%d ok=%v", in, ok)
	}
	// Distinct second attempt stream.
	_, err = holder.StoreBackendIngress(checkpoint.BackendIngressInput{
		Call:      aipapi.Call{ID: "req-be-33", Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("final2")}}}},
		AttemptID: "att-2", BLegID: "att-2", CheckpointID: "operator-attempt:att-2", StreamID: "operator-attempt:att-2",
		Now: time.Unix(3, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ex.persistBackendIngressFact(context.Background(), holder, "att-2"); err != nil {
		t.Fatal(err)
	}
	p1, _ := store.List(context.Background(), metering.Query{StreamID: "operator-attempt:att-1", Limit: 10})
	p2, _ := store.List(context.Background(), metering.Query{StreamID: "operator-attempt:att-2", Limit: 10})
	if len(p1.Facts) != 1 || len(p2.Facts) != 1 {
		t.Fatalf("distinct attempts: att1=%d att2=%d", len(p1.Facts), len(p2.Facts))
	}
}

func TestPhase33_CaptureDefaults_CustomerAndOperatorStreams(t *testing.T) {
	t.Parallel()
	fe, err := checkpoint.CaptureFrontendIngress(checkpoint.FrontendIngressInput{
		Call: aipapi.Call{ID: "req-def"}, CheckpointID: "cp-fe", Now: time.Unix(1, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if fe.Public.StreamID != "customer-request:req-def" {
		t.Fatalf("FE default StreamID=%q", fe.Public.StreamID)
	}
	be, err := checkpoint.CaptureBackendIngress(checkpoint.BackendIngressInput{
		Call: aipapi.Call{ID: "req-def"}, AttemptID: "b9", BLegID: "b9", CheckpointID: "cp-be", Now: time.Unix(1, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if be.Public.StreamID != "operator-attempt:b9" {
		t.Fatalf("BE default StreamID=%q", be.Public.StreamID)
	}
}

func TestPhase33_FactRef_PublicContract(t *testing.T) {
	t.Parallel()
	ref := metering.FactRef{StreamID: "customer-request:r1", FactID: "fe-1"}
	if err := ref.Validate(); err != nil {
		t.Fatal(err)
	}
	if metering.FactRefsFactIDs([]metering.FactRef{ref})[0] != "fe-1" {
		t.Fatal("FactRefsFactIDs")
	}
}

func TestPhase33_BuiltInAttemptAuthority_FactRefsAndTrustedScope(t *testing.T) {
	t.Parallel()
	ua := &recordingAuthorityService{
		admitResult: usagelimit.AdmissionResult{Allowed: true, Reserved: true, ReservationID: "res-ua-33"},
	}
	rec := &recordingMeter{}
	ex := &Executor{AccountingRuntime: AccountingRuntime{MeteringRecorder: rec, UsageAuthority: ua}}
	ex.Now = func() time.Time { return time.Unix(60, 0).UTC() }
	trusted := scope.PrincipalScopeView{PrincipalID: scope.Known("trusted-be-prin")}
	ctx, holder, err := captureFrontendIngressBeforeSubmit(context.Background(), aipapi.Call{ID: "req-ua-33"}, trusted, time.Unix(1, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	_, err = holder.StoreBackendIngress(checkpoint.BackendIngressInput{
		Call:      aipapi.Call{ID: "req-ua-33", Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("x")}}}},
		AttemptID: "att-ua", BLegID: "att-ua", CheckpointID: "operator-attempt:att-ua", StreamID: "operator-attempt:att-ua",
		Scope: trusted, Now: time.Unix(2, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	holder.MergeBackendIngressQuantities("att-ua", []metering.Quantity{
		{Component: metering.ComponentInputToken, Unit: metering.UnitToken, Value: 9, Present: true},
	})
	factID, err := ex.persistBackendIngressFact(ctx, holder, "att-ua")
	if err != nil || factID == "" {
		t.Fatalf("persist: id=%q err=%v", factID, err)
	}
	ctx = scope.WithScope(ctx, scope.PrincipalScopeView{PrincipalID: scope.Known("untrusted-spoof")})
	decision := accountingpreflight.Decision{
		Count: accountingapp.CountResult{InputTokens: 1, OutputTokens: 1, TotalTokens: 2, TotalTokensPresent: true},
	}
	_, err = ex.admitAttemptAuthority(ctx, "trace-ua", "a-1", b2bua.BLegRecord{BLegID: "att-ua", Seq: 1},
		aipapi.Call{ID: "req-ua-33"},
		routing.AttemptCandidate{Key: "backend-1:model-1", Primary: routing.Primary{Backend: "backend-1", Model: "model-1"}},
		decision, false)
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	if ua.admitCalls.Load() != 1 {
		t.Fatalf("admitCalls=%d", ua.admitCalls.Load())
	}
	in := ua.lastAdmit()
	if len(in.Exposure.FactRefs) != 1 || in.Exposure.FactRefs[0].FactID != factID || in.Exposure.FactRefs[0].StreamID != "operator-attempt:att-ua" {
		t.Fatalf("built-in Exposure.FactRefs=%v want fact %q", in.Exposure.FactRefs, factID)
	}
	if in.Scope.PrincipalID.String() != "trusted-be-prin" {
		t.Fatalf("built-in admit scope=%q want trusted-be-prin", in.Scope.PrincipalID.String())
	}
}

type writeThenErrMeter struct {
	store *journalstore.MemoryStore
	n     atomic.Int64
	err   error
}

func (m *writeThenErrMeter) Append(ctx context.Context, fact metering.Fact) error {
	if err := m.store.Append(ctx, fact); err != nil {
		return err
	}
	if m.n.Add(1) == 1 {
		return m.err
	}
	return nil
}

func TestPhase33_IngressStableSourceID_AmbiguousAppendRetryNoDuplicate(t *testing.T) {
	t.Parallel()
	store, err := journalstore.NewMemoryStore(journalstore.MemoryConfig{StoreID: "p33-ambig"})
	if err != nil {
		t.Fatal(err)
	}
	meter := &writeThenErrMeter{store: store, err: errors.New("ack lost after write")}
	ex := &Executor{AccountingRuntime: AccountingRuntime{MeteringRecorder: meter}}
	ex.Now = func() time.Time { return time.Unix(70, 0).UTC() }
	ctx, holder, err := captureFrontendIngressBeforeSubmit(context.Background(), aipapi.Call{ID: "req-ambig"},
		scope.PrincipalScopeView{PrincipalID: scope.Known("p")}, time.Unix(1, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	holder.MergeFrontendIngressQuantities([]metering.Quantity{
		{Component: metering.ComponentInputToken, Unit: metering.UnitToken, Value: 3, Present: true},
	})
	if _, err := ex.persistFrontendIngressFact(ctx, holder); err == nil {
		t.Fatal("first persist must surface ambiguous append error")
	}
	if holder.FrontendIngressFactID() != "" {
		t.Fatal("FactID must not bind after failed append ack")
	}
	id2, err := ex.persistFrontendIngressFact(ctx, holder)
	if err != nil || id2 == "" {
		t.Fatalf("retry persist: id=%q err=%v", id2, err)
	}
	page, err := store.List(context.Background(), metering.Query{StreamID: "customer-request:req-ambig", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Facts) != 1 {
		t.Fatalf("want 1 fact after ambiguous write+retry, got %d", len(page.Facts))
	}
	f := page.Facts[0]
	wantID, wantSrc, wantSeq := checkpoint.FrontendIngressIdentity("req-ambig")
	if f.SourceID != wantSrc || f.FactID != wantID || f.Sequence != wantSeq {
		t.Fatalf("identity FactID=%q SourceID=%q Seq=%d want %q/%q/%d", f.FactID, f.SourceID, f.Sequence, wantID, wantSrc, wantSeq)
	}
	if f.IdentityVersion != metering.IdentityVersionV1 {
		t.Fatalf("IdentityVersion=%d", f.IdentityVersion)
	}
	key := f.SourceEventKey()
	mut := f
	mut.Sequence = f.Sequence + 99
	mut.FactID = f.FactID + ":retry-diff"
	if mut.SourceEventKey() != key {
		t.Fatalf("SourceEventKey must be independent of FactID/Sequence: %q vs %q", key, mut.SourceEventKey())
	}
	if holder.FrontendIngressFactID() != f.FactID {
		t.Fatalf("bound FactID=%q", holder.FrontendIngressFactID())
	}
}

func TestPhase33_IngressIdentity_NewHolderRestartReplaysSameFactIDSequence(t *testing.T) {
	t.Parallel()
	store, err := journalstore.NewMemoryStore(journalstore.MemoryConfig{StoreID: "p33-restart-id"})
	if err != nil {
		t.Fatal(err)
	}
	ex := &Executor{AccountingRuntime: AccountingRuntime{MeteringRecorder: store}}
	ex.Now = func() time.Time { return time.Unix(71, 0).UTC() }
	_, holder1, err := captureFrontendIngressBeforeSubmit(context.Background(), aipapi.Call{ID: "req-restart-id"},
		scope.PrincipalScopeView{PrincipalID: scope.Known("p")}, time.Unix(1, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	holder1.MergeFrontendIngressQuantities([]metering.Quantity{
		{Component: metering.ComponentInputToken, Unit: metering.UnitToken, Value: 4, Present: true},
	})
	id1, err := ex.persistFrontendIngressFact(context.Background(), holder1)
	if err != nil || id1 == "" {
		t.Fatalf("first: id=%q err=%v", id1, err)
	}
	_, holder2, err := captureFrontendIngressBeforeSubmit(context.Background(), aipapi.Call{ID: "req-restart-id"},
		scope.PrincipalScopeView{PrincipalID: scope.Known("p")}, time.Unix(1, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	holder2.MergeFrontendIngressQuantities([]metering.Quantity{
		{Component: metering.ComponentInputToken, Unit: metering.UnitToken, Value: 4, Present: true},
	})
	id2, err := ex.persistFrontendIngressFact(context.Background(), holder2)
	if err != nil || id2 == "" {
		t.Fatalf("restart holder: id=%q err=%v", id2, err)
	}
	if id1 != id2 {
		t.Fatalf("FactID changed across restart holders: %q vs %q", id1, id2)
	}
	page, err := store.List(context.Background(), metering.Query{StreamID: "customer-request:req-restart-id", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Facts) != 1 {
		t.Fatalf("want 1 fact after new-holder restart persist, got %d", len(page.Facts))
	}
	if page.Facts[0].FactID != id1 || page.Facts[0].Sequence != checkpoint.IngressSequence {
		t.Fatalf("restart fact FactID=%q Seq=%d", page.Facts[0].FactID, page.Facts[0].Sequence)
	}
}

func TestPhase33_BackendIngress_AppendFailureFailClosedBeforeAuthority(t *testing.T) {
	t.Parallel()
	ua := &recordingAuthorityService{
		admitResult: usagelimit.AdmissionResult{Allowed: true, Reserved: true, ReservationID: "res-be-fail"},
	}
	ex, backend, aLegID := newAuthorityRuntimeTestExecutor(t, ua)
	ex.MeteringRecorder = &failingMeteringRecorder{err: errors.New("journal unavailable")}
	ex.Now = func() time.Time { return time.Unix(80, 0).UTC() }
	holder := &checkpoint.RequestHolder{}
	budget := &attemptBudget{max: 5}
	req := authorityOpenRequest(t, aLegID, budget)
	req.reqFacts.metering = holder
	req.reqFacts.billingCallID = "bc_ffffffffffffffffffffffffffffffff"
	req.reqFacts.billingCallState = newBillingCallState("bc_ffffffffffffffffffffffffffffffff")
	ctx := context.Background()
	req.reqFacts.baseline = aipapi.Call{
		ID:    "req-be-fail",
		Route: aipapi.RouteIntent{Selector: "backend-1:model-1"},
		Invocation: aipapi.Invocation{
			Operation: aipapi.OperationOpenAIChatCompletions, DeliveryMode: aipapi.DeliveryModeStreaming,
		},
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}}},
	}
	plan := candidatePlan{
		cand: authorityCandidate(),
	}
	_, err := ex.evaluateAndOpenCandidate(ctx, req, plan)
	if err == nil || !strings.Contains(err.Error(), "backend ingress fact") {
		t.Fatalf("want fail-closed backend ingress fact error, got %v", err)
	}
	if backend.openCalls.Load() != 0 {
		t.Fatal("Open must not run after BE ingress persist failure")
	}
	for _, in := range ua.admitInputs() {
		if !in.EstimateOnly {
			t.Fatalf("authoritative Admit must not run after BE persist failure: %+v", in)
		}
	}
}

func TestPhase33_OperatorAttempt_DistinctStableIdentities(t *testing.T) {
	t.Parallel()
	store, err := journalstore.NewMemoryStore(journalstore.MemoryConfig{StoreID: "p33-dist"})
	if err != nil {
		t.Fatal(err)
	}
	ex := &Executor{AccountingRuntime: AccountingRuntime{MeteringRecorder: store}}
	ex.Now = func() time.Time { return time.Unix(90, 0).UTC() }
	holder := &checkpoint.RequestHolder{}
	for _, att := range []string{"att-fail", "att-win"} {
		_, err = holder.StoreBackendIngress(checkpoint.BackendIngressInput{
			Call:      aipapi.Call{ID: "req-dist"},
			AttemptID: att, BLegID: att, CheckpointID: "operator-attempt:" + att, StreamID: "operator-attempt:" + att,
			Now: time.Unix(2, 0).UTC(),
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ex.persistBackendIngressFact(context.Background(), holder, att); err != nil {
			t.Fatal(err)
		}
	}
	p1, _ := store.List(context.Background(), metering.Query{StreamID: "operator-attempt:att-fail", Limit: 5})
	p2, _ := store.List(context.Background(), metering.Query{StreamID: "operator-attempt:att-win", Limit: 5})
	if len(p1.Facts) != 1 || len(p2.Facts) != 1 {
		t.Fatalf("facts fail=%d win=%d", len(p1.Facts), len(p2.Facts))
	}
	idFail, srcFail, seqFail := checkpoint.BackendIngressIdentity("att-fail")
	idWin, srcWin, seqWin := checkpoint.BackendIngressIdentity("att-win")
	if p1.Facts[0].SourceID != srcFail || p1.Facts[0].FactID != idFail || p1.Facts[0].Sequence != seqFail {
		t.Fatalf("fail identity %+v", p1.Facts[0])
	}
	if p2.Facts[0].SourceID != srcWin || p2.Facts[0].FactID != idWin || p2.Facts[0].Sequence != seqWin {
		t.Fatalf("win identity %+v", p2.Facts[0])
	}
	if p1.Facts[0].SourceID == p2.Facts[0].SourceID || p1.Facts[0].SourceEventKey() == p2.Facts[0].SourceEventKey() {
		t.Fatal("failover attempts must have distinct SourceID/SourceEventKey")
	}
}
