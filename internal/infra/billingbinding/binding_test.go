package billingbinding_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/billingport"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	runtimecore "github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/core/usageevidence"
	"github.com/aiproxer/aiproxer/internal/infra/billingbinding"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdkbilling "github.com/aiproxer/aiproxer/pkg/aipsdk/billing"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/economics"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

const (
	testStore = "store-test"
	testCall  = "call-1"
	testBLeg  = "bleg-1"
)

// --- Binding-port fakes (pointer receivers: typed-nil boxable). ---

type fakeScreen struct {
	result sdkbilling.CreditScreenResult
	err    error
	calls  int
	last   sdkbilling.CreditScreenInput
}

func (f *fakeScreen) Check(_ context.Context, in sdkbilling.CreditScreenInput) (sdkbilling.CreditScreenResult, error) {
	if f == nil {
		return sdkbilling.CreditScreenResult{}, errors.New("nil screen")
	}
	f.calls++
	f.last = in
	return f.result, f.err
}

type fakeQuoter struct {
	quote economics.ExposureQuote
	err   error
	calls int
	last  economics.QuoteInput
}

func (f *fakeQuoter) Quote(_ context.Context, in economics.QuoteInput) (economics.ExposureQuote, error) {
	if f == nil {
		return economics.ExposureQuote{}, errors.New("nil quoter")
	}
	f.calls++
	f.last = in
	return f.quote, f.err
}

type fakeAdmitter struct {
	handle sdkbilling.ExposureHandle
	err    error
	calls  int
	last   sdkbilling.ExposureAdmissionInput
}

func (f *fakeAdmitter) Admit(_ context.Context, in sdkbilling.ExposureAdmissionInput) (sdkbilling.ExposureHandle, error) {
	if f == nil {
		return sdkbilling.ExposureHandle{}, errors.New("nil admitter")
	}
	f.calls++
	f.last = in
	return f.handle, f.err
}

type fakeTerminal struct {
	ack   sdkbilling.TerminalAck
	err   error
	calls int
	last  sdkbilling.TerminalEnvelope
	// ackFor, when set, answers with an acknowledgement derived from the
	// received envelope instead of the canned ack.
	ackFor func(sdkbilling.TerminalEnvelope) (sdkbilling.TerminalAck, error)
}

// echoAck answers the received envelope with a bound acknowledgement,
// modelling a correct binding. Mismatch tests override ackFor instead.
func echoAck(revision uint64) func(sdkbilling.TerminalEnvelope) (sdkbilling.TerminalAck, error) {
	return func(env sdkbilling.TerminalEnvelope) (sdkbilling.TerminalAck, error) {
		return sdkbilling.TerminalAck{StoreID: env.StoreID, EnvelopeID: env.EnvelopeID, Revision: revision}, nil
	}
}

func (f *fakeTerminal) AppendTerminal(_ context.Context, env sdkbilling.TerminalEnvelope) (sdkbilling.TerminalAck, error) {
	if f == nil {
		return sdkbilling.TerminalAck{}, errors.New("nil terminal")
	}
	f.calls++
	f.last = env.Clone()
	if f.ackFor != nil {
		return f.ackFor(env)
	}
	return f.ack, f.err
}

// --- Fixtures ---

func validScope() scope.PrincipalScopeView {
	return scope.PrincipalScopeView{
		SubjectKind: scope.SubjectHuman,
		PrincipalID: scope.Known("user-1"),
		TenantID:    scope.Known("tenant-1"),
		Origin:      scope.OriginClient,
	}
}

func scopedCtx(sc scope.PrincipalScopeView) context.Context {
	return scope.WithScope(context.Background(), sc)
}

func routingSize(tokens int64) routing.RequestSizeEstimate {
	return routing.RequestSizeEstimate{Available: true, Tokens: tokens}
}

func aipapiCall() aipapi.Call { return aipapi.Call{} }

func bindingScope() sdkbilling.BindingScope {
	return sdkbilling.BindingScope{
		StoreID: testStore,
		Tariff:  economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "tariff-snap", Version: "v1"}},
		Policy:  economics.PolicySnapshotRef{VersionRef: economics.VersionRef{ID: "policy-snap", Version: "v1"}, PolicyID: "policy-1"},
	}
}

func quoteFixture(t *testing.T) economics.ExposureQuote {
	t.Helper()
	q := economics.ExposureQuote{
		ID:           "q-1",
		Version:      1,
		Subject:      metering.SubjectRef{Kind: metering.SubjectBillingCall, StoreID: testStore, BillingCallID: testCall},
		Tariff:       economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "tariff-snap", Version: "v1"}},
		Policy:       economics.PolicySnapshotRef{VersionRef: economics.VersionRef{ID: "policy-snap", Version: "v1"}, PolicyID: "policy-1"},
		Completeness: economics.CompletenessComplete,
	}
	if err := q.Validate(); err != nil {
		t.Fatalf("quote fixture: %v", err)
	}
	return q
}

func observationFixture(t *testing.T) metering.Observation {
	t.Helper()
	qty, err := metering.ParseDecimal("100")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	obs := metering.Observation{
		Version:        metering.ObservationVersionV2,
		ID:             "obs-1",
		SourceEventKey: "evt-1",
		Revision:       1,
		StreamID:       "stream-1",
		Sequence:       1,
		Origin:         metering.OriginLocal,
		Acquisition:    metering.AcquisitionLocalTransport,
		Authority:      metering.AuthorityObservedClaim,
		Perspective:    metering.PerspectiveOperator,
		Boundary:       metering.BoundaryBackendEgress,
		Lifecycle:      metering.LifecycleBackendAttempt,
		Subject:        metering.SubjectRef{Kind: metering.SubjectBLeg, StoreID: testStore, BillingCallID: testCall, BLegID: testBLeg},
		Correlation:    metering.CorrelationV2{StoreID: testStore, BillingCallID: testCall, BLegID: testBLeg},
		Semantics:      metering.SemanticsDelta,
		ObservedAt:     now,
		ReceivedAt:     now,
		MappingRef:     "mapping-test-v1",
		Measures: []metering.Measure{{
			Key:     metering.ComponentKey{Direction: metering.DirectionInput, Component: metering.ComponentInputTokenUncached, Unit: metering.UnitToken},
			Value:   &qty,
			Quality: metering.QualityObserved,
		}},
	}
	if err := obs.Validate(); err != nil {
		t.Fatalf("observation fixture: %v", err)
	}
	return obs
}

type bindingParts struct {
	screen      *fakeScreen
	quoter      *fakeQuoter
	admitter    *fakeAdmitter
	terminal    *fakeTerminal
	maintenance sdkbilling.MaintenanceObserver
}

type fakeMaintenance struct {
	observed int
}

func (f *fakeMaintenance) ObserveProviderMaintenance(context.Context, sdkbilling.MaintenanceUsage) error {
	if f == nil {
		return errors.New("nil maintenance observer")
	}
	f.observed++
	return nil
}

func validBinding(t *testing.T, parts *bindingParts) sdkbilling.Binding {
	t.Helper()
	return sdkbilling.Binding{
		ID:           "binding-test",
		Version:      sdkbilling.BindingVersionV1,
		Scope:        bindingScope(),
		CreditScreen: parts.screen,
		Quoter:       parts.quoter,
		Admission:    parts.admitter,
		Terminal:     parts.terminal,
		Maintenance:  parts.maintenance,
		Lifecycle: sdkbilling.Lifecycle{
			Owned: []sdkbilling.OwnedResource{{
				ID:    "worker-1",
				Start: func(context.Context) error { return nil },
				Close: func(context.Context) error { return nil },
			}},
			Borrowed: []sdkbilling.BorrowedRef{{ID: "store-shared", Kind: "journal"}},
		},
	}
}

func newParts() *bindingParts {
	parts := &bindingParts{
		screen:   &fakeScreen{result: sdkbilling.CreditScreenResult{Decision: sdkbilling.CreditAllow, StoreID: testStore, AccountID: "acct-1"}},
		quoter:   &fakeQuoter{},
		admitter: &fakeAdmitter{handle: sdkbilling.ExposureHandle{StoreID: testStore, BillingCallID: testCall, ExposureID: "exp-1", QuoteID: "q-1"}},
		terminal: &fakeTerminal{},
	}
	// Success fixtures answer the actual input envelope, binding the
	// acknowledgement to what was sent. Mismatch tests override ackFor.
	parts.terminal.ackFor = echoAck(1)
	return parts
}

func newAdapter(t *testing.T, parts *bindingParts) *billingbinding.Adapter {
	t.Helper()
	parts.quoter.quote = quoteFixture(t)
	a, err := billingbinding.NewAdapter(validBinding(t, parts))
	if err != nil {
		t.Fatalf("NewAdapter: %v", err)
	}
	return a
}

// --- Construction ---

func TestNewAdapterRejectsInvalidBinding(t *testing.T) {
	t.Parallel()
	parts := newParts()
	bad := validBinding(t, parts)
	bad.CreditScreen = nil
	if _, err := billingbinding.NewAdapter(bad); err == nil {
		t.Fatal("nil credit screen must be rejected")
	}
	bad = validBinding(t, parts)
	bad.Version = 0
	if _, err := billingbinding.NewAdapter(bad); err == nil {
		t.Fatal("bad version must be rejected")
	}
	bad = validBinding(t, parts)
	bad.Scope = sdkbilling.BindingScope{}
	if _, err := billingbinding.NewAdapter(bad); err == nil {
		t.Fatal("missing scope must be rejected")
	}
}

func TestAdapterImplementsRuntimeChokepoints(t *testing.T) {
	t.Parallel()
	parts := newParts()
	a := newAdapter(t, parts)
	var _ runtimecore.BillingCreditGate = a
	var _ runtimecore.BillingExposureAdmission = a
	var _ billingport.TerminalUsageSink = a
}

// TestMaintenanceObserverExposesBindingAttachment pins the D2 adapter surface:
// an absent attachment exposes nil (legacy version-1 no-op), an attached
// observer is returned as the same instance for BuildWithBilling to install.
func TestMaintenanceObserverExposesBindingAttachment(t *testing.T) {
	t.Parallel()
	parts := newParts()
	a := newAdapter(t, parts)
	if got := a.MaintenanceObserver(); got != nil {
		t.Fatalf("absent attachment must expose nil, got %#v", got)
	}
	observer := &fakeMaintenance{}
	parts.maintenance = observer
	a = newAdapter(t, parts)
	if got := a.MaintenanceObserver(); got != sdkbilling.MaintenanceObserver(observer) {
		t.Fatalf("attached observer = %#v, want the same instance", got)
	}
	var nilAdapter *billingbinding.Adapter
	if got := nilAdapter.MaintenanceObserver(); got != nil {
		t.Fatalf("nil adapter must expose nil, got %#v", got)
	}
}

// --- Cheap credit screen ---

func TestCreditAllowPassesThrough(t *testing.T) {
	t.Parallel()
	parts := newParts()
	a := newAdapter(t, parts)
	ctx := scopedCtx(validScope())
	if err := a.Check(ctx, "acct-1"); err != nil {
		t.Fatalf("allow: %v", err)
	}
	if parts.screen.calls != 1 {
		t.Fatalf("screen calls = %d, want 1", parts.screen.calls)
	}
	if parts.screen.last.AccountID != "acct-1" || parts.screen.last.StoreID != testStore {
		t.Fatalf("screen input = %+v, want acct-1/store-test", parts.screen.last)
	}
}

func TestCreditDenyMapsToDeniedClass(t *testing.T) {
	t.Parallel()
	parts := newParts()
	parts.screen.result = sdkbilling.CreditScreenResult{Decision: sdkbilling.CreditDeny, StoreID: testStore, AccountID: "acct-1"}
	a := newAdapter(t, parts)
	err := a.Check(scopedCtx(validScope()), "acct-1")
	if !errors.Is(err, billingport.ErrCreditScreenDenied) {
		t.Fatalf("deny err=%v, want ErrCreditScreenDenied", err)
	}
}

func TestCreditFailsClosedWithoutScope(t *testing.T) {
	t.Parallel()
	parts := newParts()
	a := newAdapter(t, parts)
	if err := a.Check(context.Background(), "acct-1"); err == nil {
		t.Fatal("scope-free credit check must fail closed")
	}
	if parts.screen.calls != 0 {
		t.Fatal("binding screen must not be called with an invalid input")
	}
}

func TestCreditBindingErrorIsUnavailable(t *testing.T) {
	t.Parallel()
	parts := newParts()
	parts.screen.err = errors.New("boom")
	a := newAdapter(t, parts)
	err := a.Check(scopedCtx(validScope()), "acct-1")
	if err == nil || errors.Is(err, billingport.ErrCreditScreenDenied) {
		t.Fatalf("transport error must be unavailable, got %v", err)
	}
}

// --- Quote + atomic exposure admission (same runtime path) ---

func admitInput() runtimecore.BillingExposureAdmissionInput {
	out := 2048
	return runtimecore.BillingExposureAdmissionInput{
		BillingAdmissionInput: runtimecore.BillingAdmissionInput{
			BillingCallID:   testCall,
			Scope:           validScope(),
			AccountID:       "acct-1",
			RequestSize:     routingSize(512),
			MaxOutputTokens: &out,
		},
		CallID: testCall,
	}
}

// TestAdmitRetainsPortAdmissionClass pins the D1 binding-admission wrapper:
// the class returned by Binding.Admission.Admit survives the adapter wrap, so
// the executor -> frontend classifier can still see the port sentinel, while
// SDK validation classes stay terminal (outer ErrAdmission only).
func TestAdmitRetainsPortAdmissionClass(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		admitErr  error
		wantClass error
	}{
		{
			name:      "port sentinel account not ready",
			admitErr:  fmt.Errorf("domain: %w", billingport.ErrAccountNotReady),
			wantClass: billingport.ErrAccountNotReady,
		},
		{
			name:      "sdk terminal invalid admission",
			admitErr:  sdkbilling.ErrInvalidAdmission,
			wantClass: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			parts := newParts()
			parts.admitter.err = tc.admitErr
			a := newAdapter(t, parts)
			_, err := a.Admit(scopedCtx(validScope()), admitInput())
			if err == nil {
				t.Fatal("Admit must fail closed when the binding denies")
			}
			if !errors.Is(err, billingbinding.ErrAdmission) {
				t.Fatalf("err=%v, want outer ErrAdmission", err)
			}
			if tc.wantClass != nil {
				if !errors.Is(err, tc.wantClass) {
					t.Fatalf("err=%v, want retained class %v through the adapter wrap", err, tc.wantClass)
				}
				return
			}
			// The SDK terminal class is preserved as its own identity, but it is
			// not a billing-port sentinel.
			if !errors.Is(err, sdkbilling.ErrInvalidAdmission) {
				t.Fatalf("err=%v, want the SDK validation class in the chain", err)
			}
		})
	}
}

func TestAdmitExercisesQuoteThenAdmit(t *testing.T) {
	t.Parallel()
	parts := newParts()
	a := newAdapter(t, parts)
	exp, err := a.Admit(scopedCtx(validScope()), admitInput())
	if err != nil {
		t.Fatalf("Admit: %v", err)
	}
	if parts.quoter.calls != 1 || parts.admitter.calls != 1 {
		t.Fatalf("quote/admit calls = %d/%d, want 1/1", parts.quoter.calls, parts.admitter.calls)
	}
	if exp.AccountID != "acct-1" || exp.CallID != testCall {
		t.Fatalf("exposure = %+v, want acct-1/call-1", exp)
	}
	if exp.PricingRef.ID != "tariff-snap" || exp.ChargePolicyRef.ID != "policy-snap" {
		t.Fatalf("exposure refs = %+v/+%v, want frozen quote refs", exp.PricingRef, exp.ChargePolicyRef)
	}
	if parts.admitter.last.Quote.Subject.StoreID != testStore {
		t.Fatalf("admission quote store = %q, want %q", parts.admitter.last.Quote.Subject.StoreID, testStore)
	}
	if len(parts.admitter.last.ExecutionLimits) != 2 {
		t.Fatalf("execution limits = %d, want input+max-output", len(parts.admitter.last.ExecutionLimits))
	}
}

func TestAdmitFailsClosedWithoutFiniteLimits(t *testing.T) {
	t.Parallel()
	parts := newParts()
	a := newAdapter(t, parts)
	in := admitInput()
	in.RequestSize.Available = false
	in.MaxOutputTokens = nil
	if _, err := a.Admit(scopedCtx(validScope()), in); err == nil {
		t.Fatal("unbounded admission must fail closed")
	}
	if parts.quoter.calls != 0 {
		t.Fatal("quoter must not be called without finite limits")
	}
}

func TestAdmitPropagatesQuoterFailure(t *testing.T) {
	t.Parallel()
	parts := newParts()
	parts.quoter.err = errors.New("no quote")
	a := newAdapter(t, parts)
	if _, err := a.Admit(scopedCtx(validScope()), admitInput()); err == nil {
		t.Fatal("quoter failure must propagate")
	}
	if parts.admitter.calls != 0 {
		t.Fatal("admission must not run without a frozen quote")
	}
}

// --- Terminal evidence handoff (same runtime path) ---

func legRecord() usageevidence.CallLegUsageRecord {
	now := time.Now().UTC()
	return usageevidence.CallLegUsageRecord{
		Key:         testCall + ":" + testBLeg,
		Fingerprint: "fp-leg-1",
		CallID:      usageevidence.BillingCallID(testCall),
		ALegID:      "aleg-1",
		BLegID:      testBLeg,
		AttemptSeq:  3,
		BackendID:   "backend-1",
		ProviderID:  "provider-1",
		ModelID:     "model-1",
		StartedAt:   now,
		FinishedAt:  now.Add(time.Second),
		Outcome:     usageevidence.LegOutcomeWinner,
		Surfaced:    usageevidence.SurfacedYes,
	}
}

func TestAppendLegHandsEnvelopeToBinding(t *testing.T) {
	t.Parallel()
	parts := newParts()
	a := newAdapter(t, parts)
	rec := legRecord()
	rec.Observations = []metering.Observation{observationFixture(t)}
	if err := a.AppendLeg(scopedCtx(validScope()), rec); err != nil {
		t.Fatalf("AppendLeg: %v", err)
	}
	if parts.terminal.calls != 1 {
		t.Fatalf("terminal calls = %d, want 1", parts.terminal.calls)
	}
	env := parts.terminal.last
	if env.StoreID != testStore || env.Subject.BLegID != testBLeg || env.AttemptSeq != 3 {
		t.Fatalf("envelope lineage = %+v/%+v, want store-test/bleg-1/3", env.StoreID, env.Subject)
	}
	if env.Outcome != sdkbilling.TerminalCompleted {
		t.Fatalf("outcome = %q, want completed", env.Outcome)
	}
	if len(env.Observations) != 1 || env.Observations[0].ID != "obs-1" {
		t.Fatalf("observations not handed through: %+v", env.Observations)
	}
	if len(env.PayloadHash) != 64 {
		t.Fatalf("payload hash = %q, want sha-256 hex", env.PayloadHash)
	}
}

func TestAppendLegRejectsUnmappableOutcome(t *testing.T) {
	t.Parallel()
	parts := newParts()
	a := newAdapter(t, parts)
	rec := legRecord()
	rec.Outcome = usageevidence.LegOutcomeNeverStarted
	if err := a.AppendLeg(context.Background(), rec); err == nil {
		t.Fatal("never-started leg must not map to a terminal outcome")
	}
	if parts.terminal.calls != 0 {
		t.Fatal("binding must not be called for an unmappable outcome")
	}
}

func TestAppendCallHandsCallClosureToBinding(t *testing.T) {
	t.Parallel()
	parts := newParts()
	a := newAdapter(t, parts)
	now := time.Now().UTC()
	rec := usageevidence.CallUsageRecord{
		Key:             testCall,
		Fingerprint:     "fp-call-1",
		CallID:          usageevidence.BillingCallID(testCall),
		AccountID:       "acct-1",
		ALegID:          "aleg-1",
		SessionID:       "sess-1",
		StartedAt:       now,
		FinishedAt:      now.Add(time.Second),
		Outcome:         usageevidence.TurnOutcomeFailed,
		ExpectedBLegIDs: []string{testBLeg},
	}
	if err := a.AppendCall(scopedCtx(validScope()), rec); err != nil {
		t.Fatalf("AppendCall: %v", err)
	}
	if parts.terminal.calls != 1 {
		t.Fatalf("terminal calls = %d, want 1", parts.terminal.calls)
	}
	if parts.terminal.last.Outcome != sdkbilling.TerminalFailed {
		t.Fatalf("outcome = %q, want failed", parts.terminal.last.Outcome)
	}
	if parts.terminal.last.Subject.Kind != metering.SubjectBillingCall {
		t.Fatalf("subject kind = %q, want billing_call", parts.terminal.last.Subject.Kind)
	}
}

// --- Identity (account/store/snapshot resolvers for the executor) ---

func TestIdentityResolvesFromScopeAndBinding(t *testing.T) {
	t.Parallel()
	parts := newParts()
	a := newAdapter(t, parts)
	id := a.Identity()
	if id.StoreID == nil || id.StoreID(context.Background()) != testStore {
		t.Fatal("StoreID must resolve the binding store")
	}
	if id.AccountID == nil {
		t.Fatal("AccountID resolver required")
	}
	if got := id.AccountID(scopedCtx(validScope()), aipapiCall()); got != "user-1" {
		t.Fatalf("AccountID = %q, want scope principal", got)
	}
	if got := id.AccountID(context.Background(), aipapiCall()); got != "" {
		t.Fatalf("unscoped AccountID = %q, want fail-closed empty", got)
	}
	if !id.WireBounded {
		t.Fatal("identity must be wire-bounded so the fast path stays open")
	}
}

// TestIdentityResolverPreservesSuppliedAccountAndStore proves a supplied
// identity resolver is the owner's authority: the account and store callbacks
// are preserved unchanged, the identity is not wire-bounded, and an empty
// supplied account stays missing instead of falling back to the principal.
func TestIdentityResolverPreservesSuppliedAccountAndStore(t *testing.T) {
	t.Parallel()
	parts := newParts()
	binding := validBinding(t, parts)
	binding.IdentityResolver = &sdkbilling.IdentityResolver{
		AccountID: func(context.Context, aipapi.Call) string { return "acct-custom" },
		StoreID:   func(context.Context) string { return "store-custom" },
	}
	a, err := billingbinding.NewAdapter(binding)
	if err != nil {
		t.Fatalf("NewAdapter: %v", err)
	}
	id := a.Identity()
	if got := id.AccountID(scopedCtx(validScope()), aipapiCall()); got != "acct-custom" {
		t.Fatalf("AccountID = %q, want the supplied custom account", got)
	}
	if got := id.StoreID(context.Background()); got != "store-custom" {
		t.Fatalf("StoreID = %q, want the supplied custom store", got)
	}
	if id.WireBounded {
		t.Fatal("a supplied account callback inspects the request: the identity must not be wire-bounded")
	}
	if id.HasCustomCallCallbacks() != true {
		t.Fatal("a supplied account callback must remain a custom call callback")
	}
}

// TestIdentityResolverEmptyAccountIsNotPrincipalDerived proves a supplied
// account resolver that resolves to an empty account stays missing: the
// adapter refuses the admission instead of crediting the trusted principal.
func TestIdentityResolverEmptyAccountIsNotPrincipalDerived(t *testing.T) {
	t.Parallel()
	parts := newParts()
	binding := validBinding(t, parts)
	binding.IdentityResolver = &sdkbilling.IdentityResolver{
		AccountID: func(context.Context, aipapi.Call) string { return "  " },
	}
	a, err := billingbinding.NewAdapter(binding)
	if err != nil {
		t.Fatalf("NewAdapter: %v", err)
	}
	parts.quoter.quote = quoteFixture(t)
	in := admitInput()
	in.AccountID = ""
	if _, err := a.Admit(scopedCtx(validScope()), in); err == nil {
		t.Fatal("an empty supplied account must be refused")
	} else if !errors.Is(err, billingbinding.ErrAdmission) {
		t.Fatalf("err = %v, want the admission failure class", err)
	}
	if parts.admitter.calls != 0 {
		t.Fatalf("the admission port ran %d times although the credited account was missing", parts.admitter.calls)
	}
}

// TestIdentityResolverStoreFallsBackToTheBindingStore proves an absent store
// resolver keeps the fixed binding store identity, while a supplied one is
// preserved unchanged.
func TestIdentityResolverStoreFallsBackToTheBindingStore(t *testing.T) {
	t.Parallel()
	parts := newParts()
	binding := validBinding(t, parts)
	binding.IdentityResolver = &sdkbilling.IdentityResolver{
		AccountID: func(context.Context, aipapi.Call) string { return "acct-custom" },
	}
	a, err := billingbinding.NewAdapter(binding)
	if err != nil {
		t.Fatalf("NewAdapter: %v", err)
	}
	if got := a.Identity().StoreID(context.Background()); got != testStore {
		t.Fatalf("StoreID = %q, want the fixed binding store %q", got, testStore)
	}
}

// --- Owned/borrowed lifecycle ---
func TestOwnedResourcesStartOnceUnwindReverse(t *testing.T) {
	t.Parallel()
	var order []string
	fail := errors.New("start failed")
	mk := func(id string, startErr error) sdkbilling.OwnedResource {
		return sdkbilling.OwnedResource{
			ID:    id,
			Start: func(context.Context) error { order = append(order, "start-"+id); return startErr },
			Close: func(context.Context) error { order = append(order, "close-"+id); return nil },
		}
	}
	parts := newParts()
	b := validBinding(t, parts)
	b.Lifecycle = sdkbilling.Lifecycle{Owned: []sdkbilling.OwnedResource{mk("a", nil), mk("b", fail), mk("c", nil)}}
	a, err := billingbinding.NewAdapter(b)
	if err != nil {
		t.Fatalf("NewAdapter: %v", err)
	}
	if err := a.StartOwnedResources(context.Background()); err == nil {
		t.Fatal("failing start must propagate")
	}
	want := []string{"start-a", "start-b", "close-a"}
	if strings.Join(order, ",") != strings.Join(want, ",") {
		t.Fatalf("order = %v, want %v", order, want)
	}
}

func TestOwnedResourcesCloseOnceBorrowedNeverTouched(t *testing.T) {
	t.Parallel()
	closes := 0
	parts := newParts()
	b := validBinding(t, parts)
	b.Lifecycle.Owned[0].Close = func(context.Context) error { closes++; return nil }
	a, err := billingbinding.NewAdapter(b)
	if err != nil {
		t.Fatalf("NewAdapter: %v", err)
	}
	if err := a.StartOwnedResources(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := a.StartOwnedResources(context.Background()); err != nil {
		t.Fatalf("second start: %v", err)
	}
	if err := a.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := a.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
	if closes != 1 {
		t.Fatalf("owned closes = %d, want exactly 1", closes)
	}
	if len(b.Lifecycle.Borrowed) != 1 {
		t.Fatal("borrowed declaration must be preserved without being closed")
	}
}

// --- Acknowledgement/envelope identity (Finding 1) ---

func TestAppendLegRejectsForeignAckStore(t *testing.T) {
	t.Parallel()
	parts := newParts()
	parts.terminal.ackFor = func(env sdkbilling.TerminalEnvelope) (sdkbilling.TerminalAck, error) {
		return sdkbilling.TerminalAck{StoreID: "other-store", EnvelopeID: env.EnvelopeID, Revision: 1}, nil
	}
	a := newAdapter(t, parts)
	rec := legRecord()
	rec.Observations = []metering.Observation{observationFixture(t)}
	err := a.AppendLeg(scopedCtx(validScope()), rec)
	if !errors.Is(err, sdkbilling.ErrTerminalAckMismatch) {
		t.Fatalf("foreign store ack err=%v, want ErrTerminalAckMismatch", err)
	}
	if parts.terminal.calls != 1 {
		t.Fatalf("terminal calls = %d, want exactly 1 (no retry on mismatch)", parts.terminal.calls)
	}
}

func TestAppendLegRejectsForeignAckEnvelope(t *testing.T) {
	t.Parallel()
	parts := newParts()
	parts.terminal.ackFor = func(env sdkbilling.TerminalEnvelope) (sdkbilling.TerminalAck, error) {
		return sdkbilling.TerminalAck{StoreID: env.StoreID, EnvelopeID: "env-foreign", Revision: 1}, nil
	}
	a := newAdapter(t, parts)
	rec := legRecord()
	rec.Observations = []metering.Observation{observationFixture(t)}
	err := a.AppendLeg(scopedCtx(validScope()), rec)
	if !errors.Is(err, sdkbilling.ErrTerminalAckMismatch) {
		t.Fatalf("foreign envelope ack err=%v, want ErrTerminalAckMismatch", err)
	}
	if parts.terminal.calls != 1 {
		t.Fatalf("terminal calls = %d, want exactly 1 (no double-submit on mismatch)", parts.terminal.calls)
	}
}

func TestAppendCallRejectsForeignAck(t *testing.T) {
	t.Parallel()
	parts := newParts()
	parts.terminal.ackFor = func(env sdkbilling.TerminalEnvelope) (sdkbilling.TerminalAck, error) {
		return sdkbilling.TerminalAck{StoreID: "other-store", EnvelopeID: "env-foreign", Revision: 3}, nil
	}
	a := newAdapter(t, parts)
	now := time.Now().UTC()
	rec := usageevidence.CallUsageRecord{
		Key:         testCall,
		Fingerprint: "fp-call-1",
		CallID:      usageevidence.BillingCallID(testCall),
		AccountID:   "acct-1",
		ALegID:      "aleg-1",
		SessionID:   "sess-1",
		StartedAt:   now,
		FinishedAt:  now.Add(time.Second),
		Outcome:     usageevidence.TurnOutcomeCompleted,
	}
	err := a.AppendCall(scopedCtx(validScope()), rec)
	if !errors.Is(err, sdkbilling.ErrTerminalAckMismatch) {
		t.Fatalf("foreign ack err=%v, want ErrTerminalAckMismatch", err)
	}
	if parts.terminal.calls != 1 {
		t.Fatalf("terminal calls = %d, want exactly 1 (no retry on mismatch)", parts.terminal.calls)
	}
}

func TestAppendLegAcceptsBoundAck(t *testing.T) {
	t.Parallel()
	parts := newParts()
	parts.terminal.ackFor = echoAck(7)
	a := newAdapter(t, parts)
	rec := legRecord()
	rec.Observations = []metering.Observation{observationFixture(t)}
	if err := a.AppendLeg(scopedCtx(validScope()), rec); err != nil {
		t.Fatalf("bound ack: %v", err)
	}
	if parts.terminal.last.EnvelopeID == "" || parts.terminal.last.StoreID != testStore {
		t.Fatalf("sent envelope = %+v, want bound store identity", parts.terminal.last)
	}
}

// --- Credit/admission identity binding (Finding 2) ---

func TestCreditRejectsForeignStoreResult(t *testing.T) {
	t.Parallel()
	parts := newParts()
	parts.screen.result = sdkbilling.CreditScreenResult{Decision: sdkbilling.CreditAllow, StoreID: "store-foreign", AccountID: "acct-1"}
	a := newAdapter(t, parts)
	err := a.Check(scopedCtx(validScope()), "acct-1")
	if !errors.Is(err, sdkbilling.ErrCreditResultMismatch) {
		t.Fatalf("foreign store result err=%v, want ErrCreditResultMismatch", err)
	}
	if parts.screen.calls != 1 {
		t.Fatalf("screen calls = %d, want exactly 1 (no retry)", parts.screen.calls)
	}
}

func TestCreditRejectsForeignAccountResult(t *testing.T) {
	t.Parallel()
	parts := newParts()
	parts.screen.result = sdkbilling.CreditScreenResult{Decision: sdkbilling.CreditDeny, StoreID: testStore, AccountID: "acct-foreign"}
	a := newAdapter(t, parts)
	err := a.Check(scopedCtx(validScope()), "acct-1")
	if !errors.Is(err, sdkbilling.ErrCreditResultMismatch) {
		t.Fatalf("foreign account result err=%v, want ErrCreditResultMismatch", err)
	}
}

// --- Degraded posture (Finding 4) ---

func TestCreditDegradedFailsClosedUnavailable(t *testing.T) {
	t.Parallel()
	parts := newParts()
	parts.screen.result = sdkbilling.CreditScreenResult{Decision: sdkbilling.CreditDegraded, StoreID: testStore, AccountID: "acct-1"}
	a := newAdapter(t, parts)
	err := a.Check(scopedCtx(validScope()), "acct-1")
	if err == nil {
		t.Fatal("degraded must not succeed as ordinary allow")
	}
	if !errors.Is(err, billingport.ErrCreditScreenUnavailable) {
		t.Fatalf("degraded err=%v, want ErrCreditScreenUnavailable class", err)
	}
	if errors.Is(err, billingport.ErrCreditScreenDenied) {
		t.Fatalf("degraded must not classify as denied: %v", err)
	}
	if parts.screen.calls != 1 {
		t.Fatalf("screen calls = %d, want exactly 1 (no retry)", parts.screen.calls)
	}
	if parts.quoter.calls != 0 || parts.admitter.calls != 0 {
		t.Fatalf("quote/admit calls = %d/%d, want 0 (fail closed pre-route)", parts.quoter.calls, parts.admitter.calls)
	}
}

func TestCreditDegradedForeignIdentityStillMismatch(t *testing.T) {
	t.Parallel()
	parts := newParts()
	parts.screen.result = sdkbilling.CreditScreenResult{Decision: sdkbilling.CreditDegraded, StoreID: "store-foreign", AccountID: "acct-1"}
	a := newAdapter(t, parts)
	err := a.Check(scopedCtx(validScope()), "acct-1")
	if !errors.Is(err, sdkbilling.ErrCreditResultMismatch) {
		t.Fatalf("foreign degraded result err=%v, want ErrCreditResultMismatch first", err)
	}
}

func foreignQuote(t *testing.T, mutate func(*economics.ExposureQuote)) economics.ExposureQuote {
	t.Helper()
	q := quoteFixture(t)
	mutate(&q)
	if err := q.Validate(); err != nil {
		t.Fatalf("foreign quote fixture invalid: %v", err)
	}
	return q
}

func TestAdmitRejectsForeignQuoteCallSubject(t *testing.T) {
	t.Parallel()
	parts := newParts()
	a := newAdapter(t, parts)
	parts.quoter.quote = foreignQuote(t, func(q *economics.ExposureQuote) {
		q.Subject.BillingCallID = "call-foreign"
	})
	_, err := a.Admit(scopedCtx(validScope()), admitInput())
	if !errors.Is(err, sdkbilling.ErrQuoteMismatch) {
		t.Fatalf("foreign call quote err=%v, want ErrQuoteMismatch", err)
	}
	if parts.quoter.calls != 1 {
		t.Fatalf("quoter calls = %d, want exactly 1", parts.quoter.calls)
	}
	if parts.admitter.calls != 0 {
		t.Fatalf("admitter calls = %d, want 0 (rejected before admit)", parts.admitter.calls)
	}
}

func TestAdmitRejectsForeignQuoteTariff(t *testing.T) {
	t.Parallel()
	parts := newParts()
	a := newAdapter(t, parts)
	parts.quoter.quote = foreignQuote(t, func(q *economics.ExposureQuote) {
		q.Tariff.ID = "tariff-foreign"
	})
	_, err := a.Admit(scopedCtx(validScope()), admitInput())
	if !errors.Is(err, sdkbilling.ErrQuoteMismatch) {
		t.Fatalf("foreign tariff quote err=%v, want ErrQuoteMismatch", err)
	}
	if parts.admitter.calls != 0 {
		t.Fatalf("admitter calls = %d, want 0 (rejected before admit)", parts.admitter.calls)
	}
}

func TestAdmitRejectsForeignQuotePolicy(t *testing.T) {
	t.Parallel()
	parts := newParts()
	a := newAdapter(t, parts)
	parts.quoter.quote = foreignQuote(t, func(q *economics.ExposureQuote) {
		q.Policy.PolicyID = "policy-foreign"
	})
	_, err := a.Admit(scopedCtx(validScope()), admitInput())
	if !errors.Is(err, sdkbilling.ErrQuoteMismatch) {
		t.Fatalf("foreign policy quote err=%v, want ErrQuoteMismatch", err)
	}
	if parts.admitter.calls != 0 {
		t.Fatalf("admitter calls = %d, want 0 (rejected before admit)", parts.admitter.calls)
	}
}

func TestAdmitRejectsForeignHandleStore(t *testing.T) {
	t.Parallel()
	parts := newParts()
	parts.admitter.handle = sdkbilling.ExposureHandle{StoreID: "store-foreign", BillingCallID: testCall, ExposureID: "exp-1", QuoteID: "q-1"}
	a := newAdapter(t, parts)
	_, err := a.Admit(scopedCtx(validScope()), admitInput())
	if !errors.Is(err, sdkbilling.ErrAdmissionMismatch) {
		t.Fatalf("foreign store handle err=%v, want ErrAdmissionMismatch", err)
	}
	if parts.admitter.calls != 1 {
		t.Fatalf("admitter calls = %d, want exactly 1 (no retry)", parts.admitter.calls)
	}
}

func TestAdmitRejectsForeignHandleCall(t *testing.T) {
	t.Parallel()
	parts := newParts()
	parts.admitter.handle = sdkbilling.ExposureHandle{StoreID: testStore, BillingCallID: "call-foreign", ExposureID: "exp-1", QuoteID: "q-1"}
	a := newAdapter(t, parts)
	_, err := a.Admit(scopedCtx(validScope()), admitInput())
	if !errors.Is(err, sdkbilling.ErrAdmissionMismatch) {
		t.Fatalf("foreign call handle err=%v, want ErrAdmissionMismatch", err)
	}
	if parts.admitter.calls != 1 {
		t.Fatalf("admitter calls = %d, want exactly 1 (no retry)", parts.admitter.calls)
	}
}

func TestAdmitRejectsForeignHandleQuoteID(t *testing.T) {
	t.Parallel()
	parts := newParts()
	a := newAdapter(t, parts)
	parts.quoter.quote = foreignQuote(t, func(q *economics.ExposureQuote) {
		q.ID = "q-9"
	})
	_, err := a.Admit(scopedCtx(validScope()), admitInput())
	if !errors.Is(err, sdkbilling.ErrAdmissionMismatch) {
		t.Fatalf("foreign quote-ID handle err=%v, want ErrAdmissionMismatch", err)
	}
	if parts.admitter.calls != 1 {
		t.Fatalf("admitter calls = %d, want exactly 1 (no retry)", parts.admitter.calls)
	}
}

// --- Trusted admission scope/account (scope admission finding) ---

func TestAdmitRejectsMissingScope(t *testing.T) {
	t.Parallel()
	parts := newParts()
	a := newAdapter(t, parts)
	in := admitInput()
	in.Scope = scope.PrincipalScopeView{}
	_, err := a.Admit(scopedCtx(validScope()), in)
	if err == nil {
		t.Fatal("missing admission scope must be rejected")
	}
	if parts.quoter.calls != 0 || parts.admitter.calls != 0 {
		t.Fatalf("quote/admit calls = %d/%d, want 0/0 (rejected before quote)", parts.quoter.calls, parts.admitter.calls)
	}
}

func TestAdmitRejectsScopeMismatch(t *testing.T) {
	t.Parallel()
	parts := newParts()
	a := newAdapter(t, parts)
	in := admitInput()
	foreign := validScope()
	foreign.PrincipalID = scope.Known("user-foreign")
	_, err := a.Admit(scopedCtx(foreign), in)
	if err == nil {
		t.Fatal("context-mismatched admission scope must be rejected")
	}
	if parts.quoter.calls != 0 || parts.admitter.calls != 0 {
		t.Fatalf("quote/admit calls = %d/%d, want 0/0 (rejected before quote)", parts.quoter.calls, parts.admitter.calls)
	}
}

func TestAdmitForwardsTrustedScopeAndAccount(t *testing.T) {
	t.Parallel()
	parts := newParts()
	a := newAdapter(t, parts)
	if _, err := a.Admit(scopedCtx(validScope()), admitInput()); err != nil {
		t.Fatalf("Admit: %v", err)
	}
	last := parts.admitter.last
	if !last.Scope.PrincipalID.Equal(validScope().PrincipalID) {
		t.Fatalf("admission scope = %+v, want trusted request scope", last.Scope.PrincipalID)
	}
	if last.AccountID != "acct-1" {
		t.Fatalf("admission account = %q, want credited acct-1", last.AccountID)
	}
	if parts.quoter.calls != 1 || parts.admitter.calls != 1 {
		t.Fatalf("quote/admit calls = %d/%d, want 1/1", parts.quoter.calls, parts.admitter.calls)
	}
}

// --- Terminal lineage mapping (Finding 3) ---

func lineageLegRecord() usageevidence.CallLegUsageRecord {
	rec := legRecord()
	rec.SubmissionID = "sub-1"
	rec.Workload = usageevidence.WorkloadIdentity{Class: usageevidence.WorkloadClassAuxiliary, Role: "test-role"}
	return rec
}

func TestAppendLegMapsFullLineage(t *testing.T) {
	t.Parallel()
	parts := newParts()
	a := newAdapter(t, parts)
	rec := lineageLegRecord()
	rec.Observations = []metering.Observation{observationFixture(t)}
	sc := validScope()
	if err := a.AppendLeg(scopedCtx(sc), rec); err != nil {
		t.Fatalf("AppendLeg: %v", err)
	}
	env := parts.terminal.last
	if env.StoreID != testStore || env.Subject.BillingCallID != testCall || env.Subject.BLegID != testBLeg {
		t.Fatalf("subject lineage = %+v, want store-test/call-1/bleg-1", env.Subject)
	}
	if env.Subject.AttemptSeq != 3 || env.Correlation.AttemptSeq != 3 || env.AttemptSeq != 3 {
		t.Fatalf("sequence = %d/%d/%d, want 3", env.Subject.AttemptSeq, env.Correlation.AttemptSeq, env.AttemptSeq)
	}
	if env.ALegID != "aleg-1" || env.Subject.ALegID != "aleg-1" || env.Correlation.ALegID != "aleg-1" {
		t.Fatalf("a-leg lineage = %q/%q/%q, want aleg-1", env.ALegID, env.Subject.ALegID, env.Correlation.ALegID)
	}
	if env.SubmissionID != "sub-1" || env.Subject.SubmissionID != "sub-1" || env.Correlation.SubmissionID != "sub-1" {
		t.Fatalf("submission lineage = %q/%q/%q, want sub-1", env.SubmissionID, env.Subject.SubmissionID, env.Correlation.SubmissionID)
	}
	if env.AccountID != "" {
		t.Fatalf("leg account = %q, want empty (leg records carry none)", env.AccountID)
	}
	if env.Workload == nil || env.Workload.Class != "auxiliary" || env.Workload.Role != "test-role" {
		t.Fatalf("workload = %+v, want auxiliary/test-role", env.Workload)
	}
	if !env.Scope.PrincipalID.Equal(sc.PrincipalID) {
		t.Fatalf("scope principal = %+v, want trusted context scope", env.Scope.PrincipalID)
	}
	if len(env.Observations) != 1 {
		t.Fatalf("observations = %d, want 1", len(env.Observations))
	}
}

func TestAppendLegRejectsMissingScope(t *testing.T) {
	t.Parallel()
	parts := newParts()
	a := newAdapter(t, parts)
	rec := lineageLegRecord()
	if err := a.AppendLeg(context.Background(), rec); err == nil {
		t.Fatal("scope-free leg handoff must be rejected")
	}
	if parts.terminal.calls != 0 {
		t.Fatalf("terminal calls = %d, want 0 (missing scope invokes sink zero times)", parts.terminal.calls)
	}
}

func TestAppendCallRejectsMissingScope(t *testing.T) {
	t.Parallel()
	parts := newParts()
	a := newAdapter(t, parts)
	if err := a.AppendCall(context.Background(), lineageCallRecord()); err == nil {
		t.Fatal("scope-free call handoff must be rejected")
	}
	if parts.terminal.calls != 0 {
		t.Fatalf("terminal calls = %d, want 0 (missing scope invokes sink zero times)", parts.terminal.calls)
	}
}

func TestAppendLegRejectsZeroAttemptSequence(t *testing.T) {
	t.Parallel()
	parts := newParts()
	a := newAdapter(t, parts)
	rec := lineageLegRecord()
	rec.AttemptSeq = 0
	if err := a.AppendLeg(scopedCtx(validScope()), rec); err == nil {
		t.Fatal("zero attempt sequence must be rejected")
	}
	if parts.terminal.calls != 0 {
		t.Fatalf("terminal calls = %d, want 0 (rejected before sink)", parts.terminal.calls)
	}
}

func TestAppendLegRejectsMissingALeg(t *testing.T) {
	t.Parallel()
	parts := newParts()
	a := newAdapter(t, parts)
	rec := lineageLegRecord()
	rec.ALegID = ""
	if err := a.AppendLeg(scopedCtx(validScope()), rec); err == nil {
		t.Fatal("missing A-leg lineage must be rejected")
	}
	if parts.terminal.calls != 0 {
		t.Fatalf("terminal calls = %d, want 0 (rejected before sink)", parts.terminal.calls)
	}
}

func TestAppendLegRejectsForeignObservationCall(t *testing.T) {
	t.Parallel()
	parts := newParts()
	a := newAdapter(t, parts)
	rec := lineageLegRecord()
	obs := observationFixture(t)
	obs.Subject.BillingCallID = "call-foreign"
	obs.Correlation.BillingCallID = ""
	rec.Observations = []metering.Observation{obs}
	if err := a.AppendLeg(scopedCtx(validScope()), rec); err == nil {
		t.Fatal("foreign-call observation must be rejected")
	}
	if parts.terminal.calls != 0 {
		t.Fatalf("terminal calls = %d, want 0 (rejected before sink)", parts.terminal.calls)
	}
}

// TestAppendLegRejectsForeignObservationTenant locks trusted scope
// agreement through the adapter: the same principal under a foreign tenant
// is rejected before the sink.
func TestAppendLegRejectsForeignObservationTenant(t *testing.T) {
	t.Parallel()
	parts := newParts()
	a := newAdapter(t, parts)
	rec := lineageLegRecord()
	obs := observationFixture(t)
	sc := validScope()
	sc.TenantID = scope.Known("tenant-foreign")
	obs.Scope = sc
	if err := obs.Validate(); err != nil {
		t.Fatalf("mutated observation must stay valid: %v", err)
	}
	rec.Observations = []metering.Observation{obs}
	if err := a.AppendLeg(scopedCtx(validScope()), rec); err == nil {
		t.Fatal("foreign-tenant observation must be rejected")
	}
	if parts.terminal.calls != 0 {
		t.Fatalf("terminal calls = %d, want 0 (rejected before sink)", parts.terminal.calls)
	}
}

// TestAppendLegRejectsObservationLineageMismatch locks lineage rejection
// across every record-expressible axis: subject A-leg/submission/sequence
// and correlation A-leg/call/submission/attempt. Each mutation keeps the
// observation itself valid, isolating the envelope check, and the sink is
// never invoked.
func TestAppendLegRejectsObservationLineageMismatch(t *testing.T) {
	t.Parallel()
	cases := map[string]func(*metering.Observation){
		"subject a-leg": func(o *metering.Observation) {
			o.Subject.ALegID = "aleg-foreign"
		},
		"subject submission": func(o *metering.Observation) {
			o.Subject.SubmissionID = "sub-foreign"
		},
		"subject sequence": func(o *metering.Observation) {
			o.Subject.AttemptSeq = 9
		},
		"correlation a-leg": func(o *metering.Observation) {
			o.Correlation.ALegID = "aleg-foreign"
		},
		"correlation call": func(o *metering.Observation) {
			o.Subject.BillingCallID = ""
			o.Correlation.BillingCallID = "call-foreign"
		},
		"correlation submission": func(o *metering.Observation) {
			o.Correlation.SubmissionID = "sub-foreign"
		},
		"correlation attempt": func(o *metering.Observation) {
			o.Subject.AttemptSeq = 0
			o.Correlation.AttemptSeq = 9
		},
	}
	for name, mutate := range cases {
		parts := newParts()
		a := newAdapter(t, parts)
		rec := lineageLegRecord()
		obs := observationFixture(t)
		mutate(&obs)
		if err := obs.Validate(); err != nil {
			t.Fatalf("%s: mutated observation must stay valid: %v", name, err)
		}
		rec.Observations = []metering.Observation{obs}
		if err := a.AppendLeg(scopedCtx(validScope()), rec); err == nil {
			t.Fatalf("%s: expected rejection before sink", name)
		}
		if parts.terminal.calls != 0 {
			t.Fatalf("%s: terminal calls = %d, want 0", name, parts.terminal.calls)
		}
	}
}

func lineageCallRecord() usageevidence.CallUsageRecord {
	now := time.Now().UTC()
	return usageevidence.CallUsageRecord{
		Key:             testCall,
		Fingerprint:     "fp-call-1",
		CallID:          usageevidence.BillingCallID(testCall),
		AccountID:       "acct-1",
		ALegID:          "aleg-1",
		SessionID:       "sess-1",
		SubmissionID:    "sub-1",
		StartedAt:       now,
		FinishedAt:      now.Add(time.Second),
		Outcome:         usageevidence.TurnOutcomeCompleted,
		ExpectedBLegIDs: []string{"bleg-2", "bleg-1"},
		Workload:        usageevidence.WorkloadIdentity{Class: usageevidence.WorkloadClassAuxiliary, Role: "test-role"},
	}
}

func TestAppendCallMapsFullLineage(t *testing.T) {
	t.Parallel()
	parts := newParts()
	a := newAdapter(t, parts)
	sc := validScope()
	if err := a.AppendCall(scopedCtx(sc), lineageCallRecord()); err != nil {
		t.Fatalf("AppendCall: %v", err)
	}
	env := parts.terminal.last
	if env.AccountID != "acct-1" {
		t.Fatalf("account = %q, want acct-1", env.AccountID)
	}
	if env.ALegID != "aleg-1" || env.Subject.ALegID != "aleg-1" || env.Correlation.ALegID != "aleg-1" {
		t.Fatalf("a-leg lineage = %q/%q/%q, want aleg-1", env.ALegID, env.Subject.ALegID, env.Correlation.ALegID)
	}
	if env.SubmissionID != "sub-1" || env.Subject.SubmissionID != "sub-1" || env.Correlation.SubmissionID != "sub-1" {
		t.Fatalf("submission lineage = %q/%q/%q, want sub-1", env.SubmissionID, env.Subject.SubmissionID, env.Correlation.SubmissionID)
	}
	if len(env.ExpectedBLegIDs) != 2 || env.ExpectedBLegIDs[0] != "bleg-2" || env.ExpectedBLegIDs[1] != "bleg-1" {
		t.Fatalf("coverage = %v, want record order preserved", env.ExpectedBLegIDs)
	}
	if env.Workload == nil || env.Workload.Class != "auxiliary" || env.Workload.Role != "test-role" {
		t.Fatalf("workload = %+v, want auxiliary/test-role", env.Workload)
	}
	if !env.Scope.PrincipalID.Equal(sc.PrincipalID) {
		t.Fatalf("scope principal = %+v, want trusted context scope", env.Scope.PrincipalID)
	}
	if env.AttemptSeq != 0 {
		t.Fatalf("call attempt sequence = %d, want 0 (no leg sequence for call closures)", env.AttemptSeq)
	}
	if env.Subject.BLegID != "" {
		t.Fatalf("call subject b-leg = %q, want empty", env.Subject.BLegID)
	}
}

func TestAppendCallRejectsMissingAccount(t *testing.T) {
	t.Parallel()
	parts := newParts()
	a := newAdapter(t, parts)
	rec := lineageCallRecord()
	rec.AccountID = ""
	if err := a.AppendCall(scopedCtx(validScope()), rec); err == nil {
		t.Fatal("missing account must be rejected")
	}
	if parts.terminal.calls != 0 {
		t.Fatalf("terminal calls = %d, want 0 (rejected before sink)", parts.terminal.calls)
	}
}

func TestAppendCallRejectsBadCoverage(t *testing.T) {
	t.Parallel()
	parts := newParts()
	a := newAdapter(t, parts)
	rec := lineageCallRecord()
	rec.ExpectedBLegIDs = []string{"bleg-1", "bleg-1"}
	if err := a.AppendCall(scopedCtx(validScope()), rec); err == nil {
		t.Fatal("duplicate coverage must be rejected")
	}
	if parts.terminal.calls != 0 {
		t.Fatalf("terminal calls = %d, want 0 (rejected before sink)", parts.terminal.calls)
	}
}
