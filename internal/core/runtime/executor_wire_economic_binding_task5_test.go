package runtime

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"

	"github.com/aiproxer/aiproxer/internal/core/billingport"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/largebody"
	coremetering "github.com/aiproxer/aiproxer/internal/core/metering"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

// task5BindingStream is a wire provider stream that records trusted economic
// bindings without interpreting them. It proves the acquisition owner bound
// the stream before downstream consumption.
type task5BindingStream struct {
	aipapi.ManagedEventStream
	mu    sync.Mutex
	bound []coremetering.ObservationIdentity
}

func task5StreamWithEvents(events ...aipapi.Event) *task5BindingStream {
	return &task5BindingStream{
		ManagedEventStream: aipapi.CloseOnlyManagedStream{Stream: aipapi.NewFixedEventStream(events)},
	}
}

func (s *task5BindingStream) BindEconomicEvidence(identity coremetering.ObservationIdentity) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bound = append(s.bound, identity)
}

func (s *task5BindingStream) bindings() []coremetering.ObservationIdentity {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]coremetering.ObservationIdentity(nil), s.bound...)
}

// TestTask5WireSuccessfulOpenBindsTrustedLineageOnce proves requirement 4.1
// and 4.2 through the wire acquisition consumer: the trusted store resolves
// once for the request and the successful non-nil provider stream is bound
// once with trusted request/call/store/sequence facts before the assembled
// stream is published for downstream consumption.
func TestTask5WireSuccessfulOpenBindsTrustedLineageOnce(t *testing.T) {
	t.Parallel()
	ex, _, _ := setupTestExecutor(t)
	var resolveCalls atomic.Int32
	ex.BillingIdentity.StoreID = func(context.Context) string {
		resolveCalls.Add(1)
		return " task5-wire-store "
	}

	content := `{"model":"gpt-4o","messages":[{"role":"user","content":"binding proof"}]}`
	src := newTestSource(content)
	acc := makeTestAcceptedAssessment(t, "gen-1", "openai-chat", src, true)

	opened := task5StreamWithEvents(
		aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "bound output"},
		aipapi.Event{Kind: aipapi.EventResponseFinished},
	)
	backend := &stubWireBackend{
		openFunc: func(context.Context, largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
			return opened, nil
		},
	}
	ex.Backends = map[string]execbackend.Backend{
		"default": {OpenWire: backend.OpenWire},
	}
	ex.DefaultBackend = "default"

	ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "usr-task5"})
	ctx = largebody.WithWireIdentity(ctx, "req-task5-bind", "trace-task5-bind")

	res, err := ex.ExecuteLargeBody(ctx, acc, src)
	if err != nil {
		t.Fatalf("ExecuteLargeBody failed: %v", err)
	}

	if got := resolveCalls.Load(); got != 1 {
		t.Fatalf("trusted store resolutions = %d, want exactly one per request", got)
	}

	// The binding must already be present before the first downstream Recv.
	bound := opened.bindings()
	if len(bound) != 1 {
		t.Fatalf("successful wire stream bindings = %d, want exactly one before consumption", len(bound))
	}
	identity := bound[0]
	if identity.StoreID != "task5-wire-store" {
		t.Errorf("binding StoreID = %q, want frozen trusted task5-wire-store", identity.StoreID)
	}
	if identity.RequestID != "req-task5-bind" {
		t.Errorf("binding RequestID = %q, want req-task5-bind", identity.RequestID)
	}
	if identity.CallID == "" || identity.CallID != identity.BillingCallID {
		t.Errorf("binding CallID = %q BillingCallID = %q, want one shared non-empty billing call", identity.CallID, identity.BillingCallID)
	}
	if identity.ALegID == "" || identity.ALegID != res.Facts.ALegID {
		t.Errorf("binding ALegID = %q, want committed %q", identity.ALegID, res.Facts.ALegID)
	}
	if identity.BLegID == "" || identity.AttemptID != identity.BLegID {
		t.Errorf("binding BLegID = %q AttemptID = %q, want one current B-leg attempt", identity.BLegID, identity.AttemptID)
	}
	if identity.AttemptSeq == 0 {
		t.Error("binding AttemptSeq must carry the current attempt sequence")
	}
	if identity.ObservedAt.IsZero() || identity.ReceivedAt.IsZero() {
		t.Error("binding must carry open and receive timestamps")
	}

	// Canonical behavior is unchanged: the published stream still delivers.
	first, err := res.Stream.Recv(ctx)
	if err != nil {
		t.Fatalf("downstream Recv failed: %v", err)
	}
	if first.Kind != aipapi.EventTextDelta || first.Delta != "bound output" {
		t.Fatalf("downstream event = %+v, want bound TextDelta", first)
	}
	second, err := res.Stream.Recv(ctx)
	if err != nil {
		t.Fatalf("downstream second Recv failed: %v", err)
	}
	if second.Kind != aipapi.EventResponseFinished {
		t.Fatalf("downstream second event = %+v, want ResponseFinished", second)
	}
	if err := res.Stream.Close(); err != nil {
		t.Fatalf("downstream Close failed: %v", err)
	}
	if got := len(opened.bindings()); got != 1 {
		t.Fatalf("bindings after consumption = %d, want exactly one", got)
	}
}

// TestTask5WireEmptyStoreResolutionStaysAbsent proves requirement 4.1 and 4.5
// through the wire acquisition consumer: an empty trusted store result is
// frozen as an explicit absence, never fabricated, while the successful open
// still binds once.
func TestTask5WireEmptyStoreResolutionStaysAbsent(t *testing.T) {
	t.Parallel()
	ex, _, _ := setupTestExecutor(t)
	var resolveCalls atomic.Int32
	ex.BillingIdentity.StoreID = func(context.Context) string {
		resolveCalls.Add(1)
		return "   "
	}

	content := `{"model":"gpt-4o","messages":[{"role":"user","content":"absent store"}]}`
	src := newTestSource(content)
	acc := makeTestAcceptedAssessment(t, "gen-1", "openai-chat", src, true)

	opened := task5StreamWithEvents(aipapi.Event{Kind: aipapi.EventResponseFinished})
	backend := &stubWireBackend{
		openFunc: func(context.Context, largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
			return opened, nil
		},
	}
	ex.Backends = map[string]execbackend.Backend{
		"default": {OpenWire: backend.OpenWire},
	}
	ex.DefaultBackend = "default"

	ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "usr-task5"})
	ctx = largebody.WithWireIdentity(ctx, "req-task5-absent", "trace-task5-absent")

	res, err := ex.ExecuteLargeBody(ctx, acc, src)
	if err != nil {
		t.Fatalf("ExecuteLargeBody failed: %v", err)
	}
	defer func() { _ = res.Stream.Close() }()

	if got := resolveCalls.Load(); got != 1 {
		t.Fatalf("trusted store resolutions = %d, want exactly one per request", got)
	}
	bound := opened.bindings()
	if len(bound) != 1 {
		t.Fatalf("successful wire stream bindings = %d, want exactly one", len(bound))
	}
	if bound[0].StoreID != "" {
		t.Fatalf("binding StoreID = %q, want explicit absence for empty resolver result", bound[0].StoreID)
	}
	if bound[0].RequestID != "req-task5-absent" {
		t.Fatalf("binding RequestID = %q, want req-task5-absent", bound[0].RequestID)
	}
}

// TestTask5WireOpenErrorDoesNotBind proves requirement 4.2 and 4.4 through
// the wire acquisition consumer: a retained evidence-capable stream returned
// WITH an open error binds nothing, and a nil errored open fails without
// binding. The retained stream is observed directly, so an accidental bind of
// a failed open would fail this test.
func TestTask5WireOpenErrorDoesNotBind(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		open      func(retained *task5BindingStream) (aipapi.ManagedEventStream, error)
		wantBinds int
	}{
		{
			name: "retained evidence-capable stream with error binds nothing",
			open: func(retained *task5BindingStream) (aipapi.ManagedEventStream, error) {
				return retained, aipapi.RecoverablePreOutputError(errors.New("upstream unavailable"))
			},
			wantBinds: 0,
		},
		{
			name: "nil stream with error binds nothing",
			open: func(*task5BindingStream) (aipapi.ManagedEventStream, error) {
				return nil, aipapi.RecoverablePreOutputError(errors.New("upstream unavailable"))
			},
			wantBinds: 0,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ex, _, _ := setupTestExecutor(t)
			ex.BillingIdentity.StoreID = func(context.Context) string { return "task5-wire-store" }

			content := `{"model":"gpt-4o","messages":[{"role":"user","content":"open failure"}]}`
			src := newTestSource(content)
			acc := makeTestAcceptedAssessment(t, "gen-1", "openai-chat", src, false, "only:m1")
			acc.WireRequest.CandidateModel = "only:m1"

			retained := task5StreamWithEvents(aipapi.Event{Kind: aipapi.EventResponseFinished})
			open := tc.open
			ex.Backends = map[string]execbackend.Backend{
				"only": {OpenWire: func(context.Context, largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
					return open(retained)
				}},
			}

			ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "usr-task5"})
			ctx = largebody.WithWireIdentity(ctx, "req-task5-openerr", "trace-task5-openerr")

			if _, err := ex.ExecuteLargeBody(ctx, acc, src); err == nil {
				t.Fatal("expected ExecuteLargeBody to fail when the only open fails, got nil")
			}
			if got := len(retained.bindings()); got != tc.wantBinds {
				t.Fatalf("failed wire open bindings = %d, want %d", got, tc.wantBinds)
			}
		})
	}
}

// TestTask5WireReplacementSharesFrozenLineageWithDistinctAttempt proves
// requirement 4.4 through the wire acquisition consumer: the permitted
// replacement attempt reuses the frozen request lineage resolved once, while
// its binding carries its own distinct B-leg attempt identity.
func TestTask5WireReplacementSharesFrozenLineageWithDistinctAttempt(t *testing.T) {
	t.Parallel()
	ex, _, _ := setupTestExecutor(t)
	var resolveCalls atomic.Int32
	ex.BillingIdentity.StoreID = func(context.Context) string {
		resolveCalls.Add(1)
		return "task5-wire-store"
	}

	content := `{"model":"gpt-4o","messages":[{"role":"user","content":"replacement lineage"}]}`
	src := newTestSource(content)
	acc := makeTestAcceptedAssessment(t, "gen-1", "openai-chat", src, false, "b1:gpt-4o", "b2:gpt-4o")
	acc.WireRequest.CandidateModel = "b1:gpt-4o | b2:gpt-4o"

	be1 := &stubWireBackend{
		openFunc: func(context.Context, largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
			return nil, aipapi.RecoverablePreOutputError(errors.New("first attempt fails before output"))
		},
	}
	opened := task5StreamWithEvents(aipapi.Event{Kind: aipapi.EventResponseFinished})
	be2 := &stubWireBackend{
		openFunc: func(context.Context, largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
			return opened, nil
		},
	}
	ex.Backends = map[string]execbackend.Backend{
		"b1": {OpenWire: be1.OpenWire},
		"b2": {OpenWire: be2.OpenWire},
	}

	ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "usr-task5"})
	ctx = largebody.WithWireIdentity(ctx, "req-task5-replace", "trace-task5-replace")

	res, err := ex.ExecuteLargeBody(ctx, acc, src)
	if err != nil {
		t.Fatalf("ExecuteLargeBody failed: %v", err)
	}
	defer func() { _ = res.Stream.Close() }()

	if got := resolveCalls.Load(); got != 1 {
		t.Fatalf("trusted store resolutions = %d, want exactly one across replacement attempts", got)
	}
	bound := opened.bindings()
	if len(bound) != 1 {
		t.Fatalf("replacement stream bindings = %d, want exactly one", len(bound))
	}
	identity := bound[0]
	if identity.StoreID != "task5-wire-store" {
		t.Errorf("replacement binding StoreID = %q, want frozen task5-wire-store", identity.StoreID)
	}
	if identity.RequestID != "req-task5-replace" {
		t.Errorf("replacement binding RequestID = %q, want frozen req-task5-replace", identity.RequestID)
	}
	if identity.CallID == "" || identity.CallID != identity.BillingCallID {
		t.Errorf("replacement binding CallID = %q BillingCallID = %q, want frozen shared billing call", identity.CallID, identity.BillingCallID)
	}
	firstCalls, secondCalls := be1.Calls(), be2.Calls()
	if len(firstCalls) != 1 || len(secondCalls) != 1 {
		t.Fatalf("backend opens = %d/%d, want one failed and one replacement open", len(firstCalls), len(secondCalls))
	}
	if firstCalls[0].BLegID == "" || secondCalls[0].BLegID == "" {
		t.Fatal("both attempts must carry a B-leg identity")
	}
	if firstCalls[0].BLegID == secondCalls[0].BLegID {
		t.Fatalf("replacement reused B-leg %q, want a distinct attempt identity", secondCalls[0].BLegID)
	}
	if identity.BLegID != secondCalls[0].BLegID {
		t.Errorf("replacement binding BLegID = %q, want current attempt %q", identity.BLegID, secondCalls[0].BLegID)
	}
}

// TestTask5CanonicalOpenBindsTrustedLineageOnce proves requirements 4.1 and
// 4.2 through the actual canonical acquisition consumer: the trusted store
// resolves once for the request and the successful non-nil provider stream
// returned by the canonical backend open is bound once with the actual
// canonical request identity before downstream consumption. The bound
// RequestID is compared against the backend-observed call identity and the
// bound billing call against the terminal call record, so a wrong RequestID
// projection at the canonical call site fails this test.
func TestTask5CanonicalOpenBindsTrustedLineageOnce(t *testing.T) {
	t.Parallel()
	ex, _, _ := setupTestExecutor(t)
	var resolveCalls atomic.Int32
	ex.BillingIdentity.StoreID = func(context.Context) string {
		resolveCalls.Add(1)
		return " task5-canonical-store "
	}
	sink := &capturingTerminalUsageSink{}
	ex.TerminalUsageSink = sink

	opened := task5StreamWithEvents(
		aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "canonical bound output"},
		aipapi.Event{Kind: aipapi.EventResponseFinished},
	)
	var gotCall aipapi.Call
	ex.Backends = map[string]execbackend.Backend{
		"default": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(_ context.Context, call aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				gotCall = call
				return opened, nil
			},
		},
	}
	ex.DefaultBackend = "default"

	ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "usr-task5"})
	stream, err := ex.Execute(ctx, &aipapi.Call{
		ID:    "call-task5-canonical-9",
		Route: aipapi.RouteIntent{Selector: "default:gpt-4o"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("canonical binding proof")},
		}},
		Invocation: aipapi.Invocation{
			Operation:    aipapi.OperationOpenAIChatCompletions,
			DeliveryMode: aipapi.DeliveryModeStreaming,
		},
	})
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if got := resolveCalls.Load(); got != 1 {
		t.Fatalf("trusted store resolutions = %d, want exactly one per request", got)
	}

	// The binding must already be present before the first downstream Recv.
	bound := opened.bindings()
	if len(bound) != 1 {
		t.Fatalf("successful canonical stream bindings = %d, want exactly one before consumption", len(bound))
	}
	identity := bound[0]
	if identity.StoreID != "task5-canonical-store" {
		t.Errorf("binding StoreID = %q, want frozen trusted task5-canonical-store", identity.StoreID)
	}
	if gotCall.ID == "" {
		t.Fatal("backend observed no canonical call identity")
	}
	if identity.RequestID != gotCall.ID {
		t.Errorf("binding RequestID = %q, want backend-observed canonical %q", identity.RequestID, gotCall.ID)
	}
	if identity.RequestID != "call-task5-canonical-9" {
		t.Errorf("binding RequestID = %q, want seeded call-task5-canonical-9", identity.RequestID)
	}
	if identity.CallID == "" || identity.CallID != identity.BillingCallID {
		t.Errorf("binding CallID = %q BillingCallID = %q, want one shared non-empty billing call", identity.CallID, identity.BillingCallID)
	}
	if identity.ALegID == "" {
		t.Error("binding ALegID must carry the current A-leg identity")
	}
	if identity.BLegID == "" || identity.AttemptID != identity.BLegID {
		t.Errorf("binding BLegID = %q AttemptID = %q, want one current B-leg attempt", identity.BLegID, identity.AttemptID)
	}
	if identity.AttemptSeq == 0 {
		t.Error("binding AttemptSeq must carry the current attempt sequence")
	}
	if identity.ObservedAt.IsZero() || identity.ReceivedAt.IsZero() {
		t.Error("binding must carry open and receive timestamps")
	}

	// Canonical behavior is unchanged: the published stream still delivers.
	first, err := stream.Recv(ctx)
	if err != nil {
		t.Fatalf("downstream Recv failed: %v", err)
	}
	if first.Kind != aipapi.EventTextDelta || first.Delta != "canonical bound output" {
		t.Fatalf("downstream event = %+v, want canonical bound TextDelta", first)
	}
	for {
		ev, rerr := stream.Recv(ctx)
		if rerr != nil {
			break
		}
		if ev.Kind == aipapi.EventTextDelta {
			t.Fatalf("unexpected extra TextDelta after first event: %+v", ev)
		}
	}
	if err := stream.Close(); err != nil {
		t.Fatalf("downstream Close failed: %v", err)
	}
	if got := len(opened.bindings()); got != 1 {
		t.Fatalf("bindings after consumption = %d, want exactly one", got)
	}

	// The bound billing call and attempt lineage must match the independent
	// terminal leg record.
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.legs) != 1 {
		t.Fatalf("terminal leg records = %d, want exactly one for the single attempt", len(sink.legs))
	}
	leg := sink.legs[0]
	if leg.CallID.String() != identity.BillingCallID {
		t.Errorf("terminal leg CallID = %q, want bound billing call %q", leg.CallID.String(), identity.BillingCallID)
	}
	if leg.ALegID != identity.ALegID {
		t.Errorf("terminal leg ALegID = %q, want bound %q", leg.ALegID, identity.ALegID)
	}
	if leg.BLegID != identity.BLegID {
		t.Errorf("terminal leg BLegID = %q, want bound %q", leg.BLegID, identity.BLegID)
	}
	if leg.AttemptSeq != int(identity.AttemptSeq) {
		t.Errorf("terminal leg AttemptSeq = %d, want bound %d", leg.AttemptSeq, identity.AttemptSeq)
	}
	if leg.BackendID != "default" {
		t.Errorf("terminal leg BackendID = %q, want default", leg.BackendID)
	}
}

// TestTask5CanonicalOpenErrorDoesNotBind proves requirement 4.2 and 4.4
// through the actual canonical acquisition consumer: a retained
// evidence-capable stream returned WITH an open error binds nothing, and a
// nil errored open fails without binding.
func TestTask5CanonicalOpenErrorDoesNotBind(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		open func(retained *task5BindingStream) (aipapi.ManagedEventStream, error)
	}{
		{
			name: "retained evidence-capable stream with error binds nothing",
			open: func(retained *task5BindingStream) (aipapi.ManagedEventStream, error) {
				return retained, aipapi.RecoverablePreOutputError(errors.New("canonical upstream unavailable"))
			},
		},
		{
			name: "nil stream with error binds nothing",
			open: func(*task5BindingStream) (aipapi.ManagedEventStream, error) {
				return nil, aipapi.RecoverablePreOutputError(errors.New("canonical upstream unavailable"))
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ex, _, _ := setupTestExecutor(t)
			ex.BillingIdentity.StoreID = func(context.Context) string { return "task5-canonical-store" }

			retained := task5StreamWithEvents(aipapi.Event{Kind: aipapi.EventResponseFinished})
			open := tc.open
			ex.Backends = map[string]execbackend.Backend{
				"default": {
					Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
					Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
						return open(retained)
					},
				},
			}
			ex.DefaultBackend = "default"

			ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "usr-task5"})
			if _, err := ex.Execute(ctx, &aipapi.Call{
				ID:    "call-task5-canonical-err",
				Route: aipapi.RouteIntent{Selector: "default:gpt-4o"},
				Messages: []aipapi.Message{{
					Role:  aipapi.RoleUser,
					Parts: []aipapi.Part{aipapi.TextPart("canonical open failure")},
				}},
				Invocation: aipapi.Invocation{
					Operation:    aipapi.OperationOpenAIChatCompletions,
					DeliveryMode: aipapi.DeliveryModeStreaming,
				},
			}); err == nil {
				t.Fatal("expected Execute to fail when the only canonical open fails, got nil")
			}
			if got := len(retained.bindings()); got != 0 {
				t.Fatalf("failed canonical open bindings = %d, want zero", got)
			}
		})
	}
}

// TestTask5CanonicalOpenBindsOnlyTheAdmittedAccount proves the account half
// of the host-binding trust row (connector-v2-host-binding-amendment.md §12):
// the provider binding carries the account frozen at admission, never the
// request principal, an absent admitted account binds no account, and the
// account resolver runs once at admission and never again at the bind site.
func TestTask5CanonicalOpenBindsOnlyTheAdmittedAccount(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		resolved string
		admitted string
		want     string
	}{
		{name: "admitted account distinct from principal", resolved: "acct-task5-fallback", admitted: "acct-task5-admitted", want: "acct-task5-admitted"},
		{name: "no admitted account binds no account", want: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ex, _, _ := setupTestExecutor(t)
			ex.BillingIdentity.StoreID = func(context.Context) string { return "task5-canonical-store" }
			var resolveCalls atomic.Int32
			ex.BillingIdentity.AccountID = func(context.Context, aipapi.Call) string {
				resolveCalls.Add(1)
				return tc.resolved
			}
			ex.BillingExposureAdmission = exposureAdmissionFunc(func(context.Context, BillingExposureAdmissionInput) (billingport.CallExposure, error) {
				return billingport.CallExposure{AccountID: tc.admitted}, nil
			})

			opened := task5StreamWithEvents(aipapi.Event{Kind: aipapi.EventResponseFinished})
			ex.Backends = map[string]execbackend.Backend{
				"default": {
					Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
					Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
						return opened, nil
					},
				},
			}
			ex.DefaultBackend = "default"

			const principal = "usr-task5-principal"
			ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: principal})
			ctx = scope.WithScope(ctx, scope.PrincipalScopeView{PrincipalID: scope.Known(principal)})
			stream, err := ex.Execute(ctx, &aipapi.Call{
				ID:    "call-task5-canonical-account",
				Route: aipapi.RouteIntent{Selector: "default:gpt-4o"},
				Messages: []aipapi.Message{{
					Role:  aipapi.RoleUser,
					Parts: []aipapi.Part{aipapi.TextPart("admitted account binding")},
				}},
				Invocation: aipapi.Invocation{
					Operation:    aipapi.OperationOpenAIChatCompletions,
					DeliveryMode: aipapi.DeliveryModeStreaming,
				},
			})
			if err != nil {
				t.Fatalf("Execute failed: %v", err)
			}
			defer func() { _ = stream.Close() }()

			bound := opened.bindings()
			if len(bound) != 1 {
				t.Fatalf("successful canonical stream bindings = %d, want exactly one", len(bound))
			}
			if got := bound[0].Provider.AccountID; got != tc.want {
				t.Errorf("binding Provider.AccountID = %q, want %q (principal is %q)", got, tc.want, principal)
			}
			if got := resolveCalls.Load(); got != 1 {
				t.Errorf("account resolutions = %d, want exactly one at admission", got)
			}
		})
	}
}

// TestTask5BindingProjectionIgnoresNilAndIncapableOpens proves the shared
// successful-open projection keeps existing behavior for streams without
// evidence capability: nil and incapable opens bind nothing, and a negative
// B-leg sequence still clamps to zero. Errored opens never reach this
// projection; they are covered through the actual canonical and wire
// acquisition consumers above.
func TestTask5BindingProjectionIgnoresNilAndIncapableOpens(t *testing.T) {
	t.Parallel()
	binding := successfulOpenBinding{
		requestID:     "req-task5-unbindable",
		storeID:       "task5-wire-store",
		billingCallID: "bc_task5unbindable00000000000000000000001",
		aLegID:        "a-task5-unbindable",
		bleg:          b2bua.BLegRecord{ALegID: "a-task5-unbindable", BLegID: "b-task5-unbindable", Seq: -1},
		openedAt:      time.Unix(2000, 0).UTC(),
		receivedAt:    time.Unix(2001, 0).UTC(),
	}

	plain := aipapi.CloseOnlyManagedStream{Stream: aipapi.NewFixedEventStream(nil)}
	clamped := task5StreamWithEvents()
	for _, s := range []aipapi.ManagedEventStream{nil, plain, aipapi.ManagedEventStream(nil), clamped} {
		if err := bindSuccessfulProviderStream(s, binding); err != nil {
			t.Fatalf("bindSuccessfulProviderStream returned error: %v", err)
		}
	}
	bound := clamped.bindings()
	if len(bound) != 1 {
		t.Fatalf("unbindable opens left %d bindings, want exactly the one clamped bind", len(bound))
	}
	if bound[0].AttemptSeq != 0 {
		t.Errorf("negative B-leg sequence projected AttemptSeq = %d, want clamped zero", bound[0].AttemptSeq)
	}
}
