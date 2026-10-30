package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/billingport"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/execctx"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/usageevidence"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	aipworkspace "github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
)

func TestStampBillingCallIDOncePerIncomingInvocation(t *testing.T) {
	t.Parallel()
	call1 := &aipapi.Call{
		Session: aipapi.SessionRef{
			ALegID: "a_shared",
		},
	}
	preSess1 := session.SessionView{
		ALegID: "a_shared",
	}
	ibt, err := newIdentityBoundTurn(
		"trace-1",
		call1,
		execview.PrincipalView{},
		scope.PrincipalScopeView{},
		false,
		aipworkspace.WorkspaceView{},
		b2bua.ALegRecord{ALegID: "a_shared"},
		routeAuthoritySnapshot{},
		execctx.SecureSessionTurn{},
		false,
		preSess1,
	)
	if err != nil {
		t.Fatal(err)
	}
	prep := &preparedRequest{
		identity: ibt,
		call:     ibt.call,
	}
	if err := stampBillingCallID(prep); err != nil {
		t.Fatal(err)
	}
	first := prep.billingCallID
	if err := first.Validate(); err != nil {
		t.Fatalf("stamped BillingCallID: %v", err)
	}
	if err := stampBillingCallID(prep); err != nil {
		t.Fatal(err)
	}
	if prep.billingCallID != first {
		t.Fatal("retries and later stamps on the same incoming invocation must keep the original BillingCallID")
	}

	call2 := &aipapi.Call{
		Session: aipapi.SessionRef{
			ALegID: "a_shared",
		},
	}
	preSess2 := session.SessionView{
		ALegID: "a_shared",
	}
	ibt2, err := newIdentityBoundTurn(
		"trace-2",
		call2,
		execview.PrincipalView{},
		scope.PrincipalScopeView{},
		false,
		aipworkspace.WorkspaceView{},
		b2bua.ALegRecord{ALegID: "a_shared"},
		routeAuthoritySnapshot{},
		execctx.SecureSessionTurn{},
		false,
		preSess2,
	)
	if err != nil {
		t.Fatal(err)
	}
	again := &preparedRequest{
		identity: ibt2,
		call:     ibt2.call,
	}
	if err := stampBillingCallID(again); err != nil {
		t.Fatal(err)
	}
	if again.billingCallID == first {
		t.Fatal("a later call reusing the same A-leg/session must allocate a distinct BillingCallID")
	}
}

func TestStampBillingCallIDRemainsIndependentOfExposureIdentity(t *testing.T) {
	t.Parallel()
	ex := TestExecutor()
	ex.BillingIdentity = testBillingIdentity()
	call3 := &aipapi.Call{
		ID: "call-1",
		Session: aipapi.SessionRef{
			ALegID: "a-1",
		},
	}
	preSess3 := session.SessionView{
		ALegID: "a-1",
	}
	ibt, err := newIdentityBoundTurn(
		"trace-1",
		call3,
		execview.PrincipalView{},
		scope.PrincipalScopeView{},
		false,
		aipworkspace.WorkspaceView{},
		b2bua.ALegRecord{ALegID: "a-1"},
		routeAuthoritySnapshot{},
		execctx.SecureSessionTurn{},
		false,
		preSess3,
	)
	if err != nil {
		t.Fatal(err)
	}
	prep := &preparedRequest{identity: ibt, call: ibt.call}
	if err := stampBillingCallID(prep); err != nil {
		t.Fatal(err)
	}
	ex.stampExposureIdentity(context.Background(), prep, billingport.CallExposure{
		AccountID: "acct", CallID: prep.billingCallID.String(),
		PricingRef:      usageevidence.VersionRef{ID: "pricing:test", Version: "1"},
		ChargePolicyRef: usageevidence.VersionRef{ID: "policy:test", Version: "1"},
	}, billingport.CallExposure{})
	if !prep.billingIdentityStamped || prep.billingExposure.AccountID != "acct" {
		t.Fatalf("exposure identity was not stamped: %+v", prep)
	}
	if string(prep.billingCallID) == prep.billingExposure.AccountID || string(prep.billingCallID) == "a-1" {
		t.Fatal("BillingCallID must not reuse A-leg or account identity")
	}
}

func TestFailoverAndParallelBLegsSharePreparedBillingCallID(t *testing.T) {
	t.Parallel()
	call4 := &aipapi.Call{
		Invocation: aipapi.Invocation{Operation: aipapi.OperationOpenAIChatCompletions},
		Session: aipapi.SessionRef{
			ALegID: "a_shared",
		},
	}
	preSess4 := session.SessionView{
		ALegID: "a_shared",
	}
	ibt, err := newIdentityBoundTurn(
		"trace-1",
		call4,
		execview.PrincipalView{},
		scope.PrincipalScopeView{},
		false,
		aipworkspace.WorkspaceView{},
		b2bua.ALegRecord{ALegID: "a_shared"},
		routeAuthoritySnapshot{},
		execctx.SecureSessionTurn{},
		false,
		preSess4,
	)
	if err != nil {
		t.Fatal(err)
	}
	prep := &preparedRequest{identity: ibt, call: ibt.call}
	if err := stampBillingCallID(prep); err != nil {
		t.Fatal(err)
	}
	sel, err := routing.Parse("exec:m")
	if err != nil {
		t.Fatal(err)
	}
	progress := newRecoveryController(recoveryControllerInput{
		budget: &attemptBudget{max: 3},
		sel:    sel,
	})
	plan := &routePlanState{routeFacts: routeFacts{sel: sel}, progress: progress}
	out := openedAttempt{
		ready: newReadyAttempt(&attemptSession{
			inner: aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventResponseFinished}}),
			cand:  routing.AttemptCandidate{Primary: routing.Primary{Backend: "exec", Model: "m"}},
		}, pendingSelectionEffects{}),
	}
	got, err := TestExecutor().assembleExecutorStream(context.Background(), prep, plan, out)
	if err != nil {
		t.Fatal(err)
	}
	rs, ok := got.(*retryRecvStream)
	if !ok {
		t.Fatalf("want *retryRecvStream, got %T", got)
	}
	if rs.facts.billingCallID != prep.billingCallID || rs.facts.billingCallID == "" {
		t.Fatalf("assembled stream BillingCallID = %q, want prepared %q", rs.facts.billingCallID, prep.billingCallID)
	}
}

func TestExecuteFailoverBLegsShareOneBillingCallID(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ex := TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(1)
	ex.Backends = map[string]execbackend.Backend{
		"bad": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return nil, aipapi.RecoverablePreOutputError(errors.New("temp"))
			},
		},
		"ok": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventMessageStarted},
					{Kind: aipapi.EventResponseFinished},
				}), nil
			},
		},
	}
	call := &aipapi.Call{
		Session: aipapi.SessionRef{AuthoritativeSessionID: "sess-shared", ContinuityKey: "sess-shared"},
		Route:   aipapi.RouteIntent{Selector: "bad:m|ok:m"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	id, ok := billingCallIDFromStream(stream)
	if !ok {
		t.Fatal("Execute must stamp a request-local BillingCallID")
	}
	if err := id.Validate(); err != nil {
		t.Fatal(err)
	}
	if _, err := aipapi.Collect(context.Background(), stream); err != nil {
		t.Fatal(err)
	}
	aLegID := strings.TrimSpace(call.Session.ALegID)
	if aLegID == "" {
		t.Fatal("expected A-leg after execute")
	}
	atts, err := st.LoadAttempts(context.Background(), aLegID)
	if err != nil {
		t.Fatal(err)
	}
	if len(atts) != 2 {
		t.Fatalf("attempts = %d, want failover + winner", len(atts))
	}
}

func TestExecuteReusesSessionButAllocatesDistinctBillingCallIDs(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ex := TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(3)
	var opens atomic.Int32
	ex.Backends = map[string]execbackend.Backend{
		"openai": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				opens.Add(1)
				return aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventMessageStarted},
					{Kind: aipapi.EventResponseFinished},
				}), nil
			},
		},
	}
	newCall := func() *aipapi.Call {
		return &aipapi.Call{
			Session: aipapi.SessionRef{AuthoritativeSessionID: "sess-reuse", ContinuityKey: "sess-reuse"},
			Route:   aipapi.RouteIntent{Selector: "openai:gpt-4"},
			Messages: []aipapi.Message{{
				Role:  aipapi.RoleUser,
				Parts: []aipapi.Part{aipapi.TextPart("hi")},
			}},
		}
	}
	firstStream, err := ex.Execute(context.Background(), newCall())
	if err != nil {
		t.Fatal(err)
	}
	first, ok := billingCallIDFromStream(firstStream)
	if !ok {
		t.Fatal("first invocation missing BillingCallID")
	}
	if _, err := aipapi.Collect(context.Background(), firstStream); err != nil {
		t.Fatal(err)
	}
	secondStream, err := ex.Execute(context.Background(), newCall())
	if err != nil {
		t.Fatal(err)
	}
	second, ok := billingCallIDFromStream(secondStream)
	if !ok {
		t.Fatal("second invocation missing BillingCallID")
	}
	if _, err := aipapi.Collect(context.Background(), secondStream); err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("subsequent calls on one session must produce distinct BillingCallIDs")
	}
	if opens.Load() != 2 {
		t.Fatalf("opens = %d, want 2", opens.Load())
	}
}

func TestExecuteResumeSameALegAllocatesDistinctBillingCallIDs(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ex := TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(5)
	ex.Backends = map[string]execbackend.Backend{
		"openai": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventMessageStarted},
					{Kind: aipapi.EventResponseFinished},
				}), nil
			},
		},
	}
	firstCall := &aipapi.Call{
		Session: aipapi.SessionRef{AuthoritativeSessionID: "sess-resume-aleg", ContinuityKey: "sess-resume-aleg"},
		Route:   aipapi.RouteIntent{Selector: "openai:gpt-4"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("first")},
		}},
	}
	firstStream, err := ex.Execute(context.Background(), firstCall)
	if err != nil {
		t.Fatal(err)
	}
	first, ok := billingCallIDFromStream(firstStream)
	if !ok {
		t.Fatal("first invocation missing BillingCallID")
	}
	if _, err := aipapi.Collect(context.Background(), firstStream); err != nil {
		t.Fatal(err)
	}
	firstALeg := strings.TrimSpace(firstCall.Session.ALegID)
	if firstALeg == "" {
		t.Fatal("expected A-leg after first execute")
	}
	if firstCall.Session.ResumeToken == "" || firstCall.Session.AuthoritativeSessionID == "" {
		t.Fatal("expected resume token and authoritative session for second call")
	}
	secondCall := &aipapi.Call{
		Session: aipapi.SessionRef{
			AuthoritativeSessionID: firstCall.Session.AuthoritativeSessionID,
			ContinuityKey:          firstCall.Session.ContinuityKey,
			ResumeToken:            firstCall.Session.ResumeToken,
			ALegID:                 firstALeg,
		},
		Route: aipapi.RouteIntent{Selector: "openai:gpt-4"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("second")},
		}},
	}
	secondStream, err := ex.Execute(context.Background(), secondCall)
	if err != nil {
		t.Fatal(err)
	}
	second, ok := billingCallIDFromStream(secondStream)
	if !ok {
		t.Fatal("second invocation missing BillingCallID")
	}
	if _, err := aipapi.Collect(context.Background(), secondStream); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(secondCall.Session.ALegID) != firstALeg {
		t.Fatalf("resume A-leg = %q, want same A-leg %q", secondCall.Session.ALegID, firstALeg)
	}
	if first == second {
		t.Fatal("later calls on the same resumed A-leg must produce distinct BillingCallIDs")
	}
}

func TestRequestLocalBillingCallIDIsNotOnProviderWireTypes(t *testing.T) {
	t.Parallel()
	assertNoBillingCallIDWireField(t, aipapi.Call{})
	assertNoBillingCallIDWireField(t, aipapi.SessionRef{})
	assertNoBillingCallIDWireField(t, backendplugin.FinalizeBillingRequest{})
	assertNoBillingCallIDWireField(t, routing.AttemptCandidate{})

	payload, err := json.Marshal(aipapi.Call{
		ID:      "call-1",
		Session: aipapi.SessionRef{AuthoritativeSessionID: "sess-1", ALegID: "a-1"},
		Route:   aipapi.RouteIntent{Selector: "backend:model"},
	})
	if err != nil {
		t.Fatal(err)
	}
	lower := strings.ToLower(string(payload))
	if strings.Contains(lower, "billingcallid") || strings.Contains(lower, "billing_call_id") {
		t.Fatalf("aipapi.Call JSON must not carry BillingCallID: %s", payload)
	}
}

func billingCallIDFromStream(stream aipapi.EventStream) (usageevidence.BillingCallID, bool) {
	rs, ok := stream.(*retryRecvStream)
	if !ok || rs == nil || rs.facts.billingCallID == "" {
		return "", false
	}
	return rs.facts.billingCallID, true
}

func assertNoBillingCallIDWireField(t *testing.T, sample any) {
	t.Helper()
	typ := reflect.TypeOf(sample)
	if typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	if typ.Kind() != reflect.Struct {
		t.Fatalf("%s is not a struct", typ)
	}
	for field := range typ.Fields() {
		name := strings.ToLower(field.Name)
		if name == "billingcallid" || strings.Contains(name, "billingcall") {
			t.Fatalf("%s.%s must not carry request-local BillingCallID on the provider wire", typ.Name(), field.Name)
		}
		tag := strings.ToLower(string(field.Tag))
		if strings.Contains(tag, "billing_call_id") || strings.Contains(tag, "billingcallid") {
			t.Fatalf("%s.%s tag leaks BillingCallID: %s", typ.Name(), field.Name, field.Tag)
		}
	}
}
