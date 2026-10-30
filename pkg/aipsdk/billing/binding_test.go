package billing_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/billing"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/economics"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

// --- Fakes (pointer receivers so typed-nil boxing is expressible). ---

type fakeScreen struct {
	result billing.CreditScreenResult
	err    error
}

func (f *fakeScreen) Check(context.Context, billing.CreditScreenInput) (billing.CreditScreenResult, error) {
	if f == nil {
		return billing.CreditScreenResult{}, errors.New("nil screen")
	}
	return f.result, f.err
}

type fakeQuoter struct {
	quote economics.ExposureQuote
	err   error
}

func (f *fakeQuoter) Quote(context.Context, economics.QuoteInput) (economics.ExposureQuote, error) {
	if f == nil {
		return economics.ExposureQuote{}, errors.New("nil quoter")
	}
	return f.quote, f.err
}

type fakeAdmitter struct {
	handle billing.ExposureHandle
	err    error
}

func (f *fakeAdmitter) Admit(context.Context, billing.ExposureAdmissionInput) (billing.ExposureHandle, error) {
	if f == nil {
		return billing.ExposureHandle{}, errors.New("nil admitter")
	}
	return f.handle, f.err
}

type fakeTerminal struct {
	ack billing.TerminalAck
	err error
}

func (f *fakeTerminal) AppendTerminal(context.Context, billing.TerminalEnvelope) (billing.TerminalAck, error) {
	if f == nil {
		return billing.TerminalAck{}, errors.New("nil terminal")
	}
	return f.ack, f.err
}

// --- Fixtures ---

const (
	testStore = "store-test"
	testCall  = "call-1"
	testBLeg  = "bleg-1"
)

func validScope() scope.PrincipalScopeView {
	return scope.PrincipalScopeView{
		SubjectKind: scope.SubjectHuman,
		PrincipalID: scope.Known("user-1"),
		TenantID:    scope.Known("tenant-1"),
		Origin:      scope.OriginClient,
	}
}

func validPolicyRef() economics.PolicySnapshotRef {
	return economics.PolicySnapshotRef{
		VersionRef: economics.VersionRef{ID: "policy-snap", Version: "v1"},
		PolicyID:   "policy-1",
	}
}

func validTariffRef() economics.RatingSnapshotRef {
	return economics.RatingSnapshotRef{
		VersionRef: economics.VersionRef{ID: "tariff-snap", Version: "v1"},
	}
}

func validCallSubject() metering.SubjectRef {
	return metering.SubjectRef{
		Kind:          metering.SubjectBillingCall,
		StoreID:       testStore,
		BillingCallID: testCall,
	}
}

func validBLegSubject() metering.SubjectRef {
	return metering.SubjectRef{
		Kind:          metering.SubjectBLeg,
		StoreID:       testStore,
		BillingCallID: testCall,
		BLegID:        testBLeg,
	}
}

func validObservation(t *testing.T) metering.Observation {
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
		Subject:        validBLegSubject(),
		Correlation: metering.CorrelationV2{
			StoreID:       testStore,
			BillingCallID: testCall,
			BLegID:        testBLeg,
		},
		Semantics:  metering.SemanticsDelta,
		ObservedAt: now,
		ReceivedAt: now,
		MappingRef: "mapping-test-v1",
		Measures: []metering.Measure{
			{
				Key: metering.ComponentKey{
					Direction: metering.DirectionInput,
					Component: metering.ComponentInputTokenUncached,
					Unit:      metering.UnitToken,
				},
				Value:   &qty,
				Quality: metering.QualityObserved,
			},
		},
	}
	if err := obs.Validate(); err != nil {
		t.Fatalf("fixture observation invalid: %v", err)
	}
	return obs
}

func validQuote(t *testing.T) economics.ExposureQuote {
	t.Helper()
	q := economics.ExposureQuote{
		Version:      1,
		Subject:      validCallSubject(),
		Policy:       validPolicyRef(),
		Completeness: economics.CompletenessComplete,
	}
	if err := q.Validate(); err != nil {
		t.Fatalf("fixture quote invalid: %v", err)
	}
	return q
}

func payloadHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func validEnvelope(t *testing.T) billing.TerminalEnvelope {
	t.Helper()
	env := billing.TerminalEnvelope{
		Version:     billing.TerminalEnvelopeVersionV1,
		EnvelopeID:  "env-1",
		StoreID:     testStore,
		Scope:       validScope(),
		Subject:     validBLegSubject(),
		Correlation: metering.CorrelationV2{StoreID: testStore, BillingCallID: testCall, BLegID: testBLeg, AttemptSeq: 3, ALegID: "aleg-1"},
		Outcome:     billing.TerminalCompleted,
		AttemptSeq:  3,
		ALegID:      "aleg-1",
		Tariff:      validTariffRef(),
		Policy:      validPolicyRef(),
		Observations: []metering.Observation{
			validObservation(t),
		},
		ParentWorkID: "parent-1",
		PayloadHash:  payloadHash("envelope-1"),
	}
	env.Subject.ALegID = "aleg-1"
	env.Subject.AttemptSeq = 3
	if err := env.Validate(); err != nil {
		t.Fatalf("fixture envelope invalid: %v", err)
	}
	return env
}

func validLifecycle() billing.Lifecycle {
	return billing.Lifecycle{
		Owned: []billing.OwnedResource{
			{
				ID:    "worker-1",
				Start: func(context.Context) error { return nil },
				Close: func(context.Context) error { return nil },
			},
		},
		Borrowed: []billing.BorrowedRef{
			{ID: "store-shared", Kind: "journal"},
		},
	}
}

func validBinding(t *testing.T) billing.Binding {
	t.Helper()
	b := billing.Binding{
		ID:           "billing-test",
		Version:      billing.BindingVersionV1,
		Scope:        validBindingScope(),
		CreditScreen: &fakeScreen{},
		Quoter:       &fakeQuoter{},
		Admission:    &fakeAdmitter{},
		Terminal:     &fakeTerminal{},
		Lifecycle:    validLifecycle(),
	}
	if err := b.Validate(); err != nil {
		t.Fatalf("fixture binding invalid: %v", err)
	}
	return b
}

// TestBindingValidateAcceptsIdentityResolver proves a supplied identity
// resolver is accepted when it carries an account callback, and that an
// unusable resolver is refused before publication: a resolver without an
// account callback, and a typed-nil resolver.
func TestBindingValidateAcceptsIdentityResolver(t *testing.T) {
	t.Parallel()
	b := validBinding(t)
	b.IdentityResolver = &billing.IdentityResolver{
		AccountID: func(context.Context, aipapi.Call) string { return "acct-custom" },
	}
	if err := b.Validate(); err != nil {
		t.Fatalf("a complete identity resolver must be accepted: %v", err)
	}
	missing := validBinding(t)
	missing.IdentityResolver = &billing.IdentityResolver{
		StoreID: func(context.Context) string { return "store-custom" },
	}
	if err := missing.Validate(); !errors.Is(err, billing.ErrMissingAccountResolver) {
		t.Fatalf("err = %v, want the missing-account-resolver class", err)
	}
}

// --- Binding identity/version ---

func TestBindingValidateAcceptsCompleteBinding(t *testing.T) {
	t.Parallel()
	b := validBinding(t)
	if !b.IsMonetary() {
		t.Fatal("binding must report IsMonetary true: it owns the monetary authority")
	}
}

func TestBindingValidateRejectsBadID(t *testing.T) {
	t.Parallel()
	for _, id := range []string{"", "   ", strings.Repeat("x", billing.MaxBindingIDLength+1)} {
		b := validBinding(t)
		b.ID = id
		if err := b.Validate(); !errors.Is(err, billing.ErrInvalidBindingID) {
			t.Fatalf("ID %q: err=%v, want ErrInvalidBindingID", id, err)
		}
	}
}

func TestBindingValidateRejectsBadVersion(t *testing.T) {
	t.Parallel()
	for _, v := range []uint32{0, 999} {
		b := validBinding(t)
		b.Version = v
		if err := b.Validate(); !errors.Is(err, billing.ErrUnsupportedBindingVersion) {
			t.Fatalf("version %d: err=%v, want ErrUnsupportedBindingVersion", v, err)
		}
	}
}

// --- Nil and typed-nil members ---

func TestBindingValidateRejectsNilMembers(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		mutate func(*billing.Binding)
		want   error
	}{
		{"credit", func(b *billing.Binding) { b.CreditScreen = nil }, billing.ErrMissingCreditScreen},
		{"quoter", func(b *billing.Binding) { b.Quoter = nil }, billing.ErrMissingQuoter},
		{"admission", func(b *billing.Binding) { b.Admission = nil }, billing.ErrMissingAdmission},
		{"terminal", func(b *billing.Binding) { b.Terminal = nil }, billing.ErrMissingTerminal},
	}
	for _, tc := range cases {
		b := validBinding(t)
		tc.mutate(&b)
		if err := b.Validate(); !errors.Is(err, tc.want) {
			t.Fatalf("%s: err=%v, want %v", tc.name, err, tc.want)
		}
	}
}

func TestBindingValidateRejectsTypedNilMembers(t *testing.T) {
	t.Parallel()
	b := validBinding(t)
	b.CreditScreen = (*fakeScreen)(nil)
	if err := b.Validate(); !errors.Is(err, billing.ErrMissingCreditScreen) {
		t.Fatalf("typed-nil screen: err=%v, want ErrMissingCreditScreen", err)
	}
	b = validBinding(t)
	b.Quoter = (*fakeQuoter)(nil)
	if err := b.Validate(); !errors.Is(err, billing.ErrMissingQuoter) {
		t.Fatalf("typed-nil quoter: err=%v, want ErrMissingQuoter", err)
	}
	b = validBinding(t)
	b.Admission = (*fakeAdmitter)(nil)
	if err := b.Validate(); !errors.Is(err, billing.ErrMissingAdmission) {
		t.Fatalf("typed-nil admission: err=%v, want ErrMissingAdmission", err)
	}
	b = validBinding(t)
	b.Terminal = (*fakeTerminal)(nil)
	if err := b.Validate(); !errors.Is(err, billing.ErrMissingTerminal) {
		t.Fatalf("typed-nil terminal: err=%v, want ErrMissingTerminal", err)
	}
}

// --- Lifecycle ownership ---

func TestBindingValidateRejectsInvalidLifecycle(t *testing.T) {
	t.Parallel()
	b := validBinding(t)
	b.Lifecycle.Owned[0].ID = ""
	if err := b.Validate(); !errors.Is(err, billing.ErrInvalidLifecycle) {
		t.Fatalf("empty owned ID: err=%v, want ErrInvalidLifecycle", err)
	}
	b = validBinding(t)
	b.Lifecycle.Owned[0].Start = nil
	if err := b.Validate(); !errors.Is(err, billing.ErrInvalidLifecycle) {
		t.Fatalf("nil owned Start: err=%v, want ErrInvalidLifecycle", err)
	}
	b = validBinding(t)
	b.Lifecycle.Owned[0].Close = nil
	if err := b.Validate(); !errors.Is(err, billing.ErrInvalidLifecycle) {
		t.Fatalf("nil owned Close: err=%v, want ErrInvalidLifecycle", err)
	}
}

func TestLifecycleRejectsDuplicateOwnedAndBorrowedCollision(t *testing.T) {
	t.Parallel()
	dup := validLifecycle()
	dup.Owned = append(dup.Owned, dup.Owned[0])
	if err := dup.Validate(); !errors.Is(err, billing.ErrInvalidLifecycle) {
		t.Fatalf("duplicate owned: err=%v, want ErrInvalidLifecycle", err)
	}
	collide := validLifecycle()
	collide.Borrowed[0].ID = collide.Owned[0].ID
	if err := collide.Validate(); !errors.Is(err, billing.ErrInvalidLifecycle) {
		t.Fatalf("owned/borrowed collision: err=%v, want ErrInvalidLifecycle", err)
	}
	empty := billing.Lifecycle{}
	if err := empty.Validate(); err != nil {
		t.Fatalf("empty lifecycle must be valid (no owned resources): %v", err)
	}
}

func TestBorrowedRefCarriesNoCloseContract(t *testing.T) {
	t.Parallel()
	// Borrowed handles are declaration-only values: the type system offers no
	// Close/Start entry point, so a host cannot implicitly close them.
	ref := billing.BorrowedRef{ID: "store-shared", Kind: "journal"}
	if err := ref.Validate(); err != nil {
		t.Fatalf("borrowed ref: %v", err)
	}
	if err := (billing.BorrowedRef{}).Validate(); err == nil {
		t.Fatal("empty borrowed ref must be rejected")
	}
}

// --- Duplicate monetary bindings (contract rule for Task 15.2) ---

func TestValidateBindingsRejectsMultipleMonetary(t *testing.T) {
	t.Parallel()
	if err := billing.ValidateBindings(nil); err != nil {
		t.Fatalf("zero bindings must be valid (non-money host): %v", err)
	}
	if err := billing.ValidateBindings([]billing.Binding{validBinding(t)}); err != nil {
		t.Fatalf("single binding: %v", err)
	}
	two := []billing.Binding{validBinding(t), validBinding(t)}
	two[1].ID = "billing-second"
	if err := billing.ValidateBindings(two); !errors.Is(err, billing.ErrDuplicateMonetaryBinding) {
		t.Fatalf("two bindings: err=%v, want ErrDuplicateMonetaryBinding", err)
	}
}

func TestValidateBindingsSurfacesInvalidBeforeDuplicate(t *testing.T) {
	t.Parallel()
	bad := validBinding(t)
	bad.CreditScreen = nil
	err := billing.ValidateBindings([]billing.Binding{validBinding(t), bad})
	if err == nil || errors.Is(err, billing.ErrDuplicateMonetaryBinding) {
		t.Fatalf("invalid member must surface before duplicate rule: %v", err)
	}
}

// --- Credit screen port ---

func TestCreditScreenInputValidation(t *testing.T) {
	t.Parallel()
	valid := billing.CreditScreenInput{
		Scope:     validScope(),
		StoreID:   testStore,
		AccountID: "acct-1",
		Policy:    validPolicyRef(),
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("fixture credit input: %v", err)
	}
	missingScope := valid
	missingScope.Scope = scope.PrincipalScopeView{}
	if err := missingScope.Validate(); !errors.Is(err, billing.ErrInvalidCreditScreen) {
		t.Fatalf("anonymous scope: err=%v, want ErrInvalidCreditScreen", err)
	}
	for _, tc := range []struct {
		name   string
		mutate func(*billing.CreditScreenInput)
	}{
		{"store", func(in *billing.CreditScreenInput) { in.StoreID = "" }},
		{"account", func(in *billing.CreditScreenInput) { in.AccountID = "  " }},
		{"policy", func(in *billing.CreditScreenInput) { in.Policy.PolicyID = "" }},
	} {
		mut := valid
		tc.mutate(&mut)
		if err := mut.Validate(); !errors.Is(err, billing.ErrInvalidCreditScreen) {
			t.Fatalf("%s: err=%v, want ErrInvalidCreditScreen", tc.name, err)
		}
	}
}

func TestCreditScreenResultValidation(t *testing.T) {
	t.Parallel()
	for _, d := range []billing.CreditDecision{billing.CreditAllow, billing.CreditDeny, billing.CreditDegraded} {
		r := billing.CreditScreenResult{Decision: d, StoreID: testStore, AccountID: "acct-1"}
		if err := r.Validate(); err != nil {
			t.Fatalf("decision %q: %v", d, err)
		}
	}
	bad := billing.CreditScreenResult{Decision: "maybe", StoreID: testStore, AccountID: "acct-1"}
	if err := bad.Validate(); !errors.Is(err, billing.ErrInvalidCreditScreen) {
		t.Fatalf("unknown decision: err=%v, want ErrInvalidCreditScreen", err)
	}
}

// --- Quote + atomic exposure admission port ---

func TestExposureAdmissionInputValidation(t *testing.T) {
	t.Parallel()
	valid := billing.ExposureAdmissionInput{
		Subject:   validCallSubject(),
		Scope:     validScope(),
		AccountID: "acct-1",
		Quote:     validQuote(t),
		ExecutionLimits: []economics.Limit{
			{Name: "max-output", Value: 100, Unit: "token"},
		},
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("fixture admission input: %v", err)
	}
	noLimits := valid
	noLimits.ExecutionLimits = nil
	if err := noLimits.Validate(); !errors.Is(err, billing.ErrInvalidAdmission) {
		t.Fatalf("missing limits: err=%v, want ErrInvalidAdmission", err)
	}
	badSubject := valid
	badSubject.Subject = metering.SubjectRef{}
	if err := badSubject.Validate(); !errors.Is(err, billing.ErrInvalidAdmission) {
		t.Fatalf("bad subject: err=%v, want ErrInvalidAdmission", err)
	}
	badQuote := valid
	badQuote.Quote = economics.ExposureQuote{}
	if err := badQuote.Validate(); !errors.Is(err, billing.ErrInvalidAdmission) {
		t.Fatalf("bad quote: err=%v, want ErrInvalidAdmission", err)
	}
	crossStore := valid
	crossStore.Quote.Subject.StoreID = "other-store"
	if err := crossStore.Validate(); !errors.Is(err, billing.ErrInvalidAdmission) {
		t.Fatalf("cross-store quote: err=%v, want ErrInvalidAdmission", err)
	}
}

func TestExposureHandleValidation(t *testing.T) {
	t.Parallel()
	h := billing.ExposureHandle{StoreID: testStore, BillingCallID: testCall, ExposureID: "exp-1", QuoteID: "q-1"}
	if err := h.Validate(); err != nil {
		t.Fatalf("fixture handle: %v", err)
	}
	empty := billing.ExposureHandle{}
	if err := empty.Validate(); !errors.Is(err, billing.ErrInvalidAdmission) {
		t.Fatalf("empty handle: err=%v, want ErrInvalidAdmission", err)
	}
}

// --- Terminal evidence handoff port ---

func TestTerminalEnvelopeValidation(t *testing.T) {
	t.Parallel()
	if err := validEnvelope(t).Validate(); err != nil {
		t.Fatalf("fixture envelope: %v", err)
	}
	cases := []struct {
		name   string
		mutate func(*billing.TerminalEnvelope)
	}{
		{"version", func(e *billing.TerminalEnvelope) { e.Version = 0 }},
		{"envelope id", func(e *billing.TerminalEnvelope) { e.EnvelopeID = "" }},
		{"store", func(e *billing.TerminalEnvelope) { e.StoreID = "" }},
		{"subject", func(e *billing.TerminalEnvelope) { e.Subject = metering.SubjectRef{} }},
		{"outcome", func(e *billing.TerminalEnvelope) { e.Outcome = "vanished" }},
		{"tariff", func(e *billing.TerminalEnvelope) { e.Tariff.ID = "" }},
		{"policy", func(e *billing.TerminalEnvelope) { e.Policy.PolicyID = "" }},
		{"hash", func(e *billing.TerminalEnvelope) { e.PayloadHash = "not-a-hash" }},
		{"correlation store", func(e *billing.TerminalEnvelope) { e.Correlation.StoreID = "other" }},
	}
	for _, tc := range cases {
		env := validEnvelope(t)
		tc.mutate(&env)
		if err := env.Validate(); !errors.Is(err, billing.ErrInvalidTerminal) {
			t.Fatalf("%s: err=%v, want ErrInvalidTerminal", tc.name, err)
		}
	}
}

func TestTerminalEnvelopeRejectsCrossStoreObservation(t *testing.T) {
	t.Parallel()
	env := validEnvelope(t)
	other := validObservation(t)
	other.Subject.StoreID = "other-store"
	other.Correlation.StoreID = "other-store"
	env.Observations = []metering.Observation{other}
	if err := env.Validate(); !errors.Is(err, billing.ErrInvalidTerminal) {
		t.Fatalf("cross-store observation: err=%v, want ErrInvalidTerminal", err)
	}
}

func TestTerminalAckValidation(t *testing.T) {
	t.Parallel()
	ack := billing.TerminalAck{StoreID: testStore, EnvelopeID: "env-1", Revision: 1}
	if err := ack.Validate(); err != nil {
		t.Fatalf("fixture ack: %v", err)
	}
	zero := billing.TerminalAck{StoreID: testStore, EnvelopeID: "env-1"}
	if err := zero.Validate(); !errors.Is(err, billing.ErrInvalidTerminal) {
		t.Fatalf("zero revision: err=%v, want ErrInvalidTerminal", err)
	}
}

// --- DTO immutability (clone independence) ---

func TestTerminalEnvelopeCloneIsIndependent(t *testing.T) {
	t.Parallel()
	env := validEnvelope(t)
	dup := env.Clone()
	dup.Observations[0].ID = "mutated"
	if env.Observations[0].ID == "mutated" {
		t.Fatal("Clone must deep-copy observations")
	}
}

func TestAdmissionInputCloneIsIndependent(t *testing.T) {
	t.Parallel()
	in := billing.ExposureAdmissionInput{
		Subject:         validCallSubject(),
		Scope:           validScope(),
		Quote:           validQuote(t),
		ExecutionLimits: []economics.Limit{{Name: "max-output", Value: 1, Unit: "token"}},
	}
	dup := in.Clone()
	dup.ExecutionLimits[0].Name = "mutated"
	if in.ExecutionLimits[0].Name == "mutated" {
		t.Fatal("Clone must deep-copy execution limits")
	}
}

// --- Preserved economics seams use the shared contracts (no fork) ---

func TestBindingReusesSharedEconomicsContracts(t *testing.T) {
	t.Parallel()
	b := validBinding(t)
	// Explicit interface types are compile-time conformance assertions: the
	// preserved seams must still satisfy the shared economics contracts.
	var _ economics.Quoter = b.Quoter             //nolint:staticcheck // conformance assertion, not an inferred declaration
	var _ billing.CreditScreener = b.CreditScreen //nolint:staticcheck // conformance assertion, not an inferred declaration
	var _ billing.ExposureAdmitter = b.Admission  //nolint:staticcheck // conformance assertion, not an inferred declaration
	var _ billing.TerminalSink = b.Terminal       //nolint:staticcheck // conformance assertion, not an inferred declaration
}

// --- Composition-time economics scope (Task 15.2 integration) ---

func validBindingScope() billing.BindingScope {
	return billing.BindingScope{
		StoreID: testStore,
		Tariff:  validTariffRef(),
		Policy:  validPolicyRef(),
	}
}

func TestBindingScopeValidation(t *testing.T) {
	t.Parallel()
	if err := validBindingScope().Validate(); err != nil {
		t.Fatalf("fixture scope: %v", err)
	}
	for _, tc := range []struct {
		name   string
		mutate func(*billing.BindingScope)
	}{
		{"store", func(s *billing.BindingScope) { s.StoreID = "" }},
		{"tariff", func(s *billing.BindingScope) { s.Tariff.ID = "" }},
		{"policy", func(s *billing.BindingScope) { s.Policy.PolicyID = "" }},
	} {
		mut := validBindingScope()
		tc.mutate(&mut)
		if err := mut.Validate(); !errors.Is(err, billing.ErrInvalidBindingScope) {
			t.Fatalf("%s: err=%v, want ErrInvalidBindingScope", tc.name, err)
		}
	}
}

func TestBindingValidateRejectsMissingScope(t *testing.T) {
	t.Parallel()
	b := validBinding(t)
	b.Scope = billing.BindingScope{}
	if err := b.Validate(); !errors.Is(err, billing.ErrInvalidBindingScope) {
		t.Fatalf("missing scope: err=%v, want ErrInvalidBindingScope", err)
	}
}

// --- Optional provider-maintenance attachment ---

type fakeMaintenance struct {
	observed int
}

func (f *fakeMaintenance) ObserveProviderMaintenance(context.Context, billing.MaintenanceUsage) error {
	if f == nil {
		return errors.New("nil maintenance observer")
	}
	f.observed++
	return nil
}

func TestBindingMaintenanceAttachmentIsOptional(t *testing.T) {
	t.Parallel()
	// Absent: the binding stays a valid version-1 binding and never errors.
	b := validBinding(t)
	if b.Maintenance != nil {
		t.Fatal("fixture binding must not declare maintenance")
	}
	if b.Version != billing.BindingVersionV1 {
		t.Fatalf("version = %d, want BindingVersionV1", b.Version)
	}
	// Present: the observer is carried unchanged and version stays v1.
	observer := &fakeMaintenance{}
	b = validBinding(t)
	b.Maintenance = observer
	if err := b.Validate(); err != nil {
		t.Fatalf("attached maintenance binding: %v", err)
	}
	if b.Maintenance != billing.MaintenanceObserver(observer) {
		t.Fatal("binding must carry the attached observer unchanged")
	}
	if b.Version != billing.BindingVersionV1 {
		t.Fatalf("version = %d, want BindingVersionV1 (additive optional field)", b.Version)
	}
	// Typed-nil: a non-nil interface holding a nil observer is rejected so the
	// host never installs a silently-dead seam.
	b = validBinding(t)
	b.Maintenance = (*fakeMaintenance)(nil)
	if err := b.Validate(); !errors.Is(err, billing.ErrInvalidBinding) {
		t.Fatalf("typed-nil maintenance: err=%v, want ErrInvalidBinding", err)
	}
}

// TestExternalMaintenanceABIAndCallShapedIdentityResolver proves the public
// surface an external implementer compiles against: the historical
// maintenance vocabulary keeps its names, its type identity, and its durable
// fingerprint, and the identity resolver carries a call-shaped account
// callback an external owner can implement against the real request.
//
// The fixture is an external consumer (package billing_test): it only uses
// exported names, exactly as an out-of-tree owner does.
func TestExternalMaintenanceABIAndCallShapedIdentityResolver(t *testing.T) {
	t.Parallel()
	// The historical maintenance vocabulary is unchanged: same names, same
	// error sentinel, same durable fingerprint.
	usage := billing.MaintenanceUsage{
		OperationID: "op-maint-abi",
		ALegID:      "a-leg-abi",
		TargetID:    "target-abi",
		BackendID:   "backend-abi",
		ModelID:     "model-abi",
		RecordedAt:  time.Unix(1_700_000_000, 0).UTC(),
		Evidence: billing.MaintenanceEvidence{
			InputTokens: billing.MaintenanceQuantity{Value: 23, Present: true},
			Source:      billing.MaintenanceSourceProviderReported,
			Authority:   billing.MaintenanceAuthorityAuthoritative,
			DedupeKey:   "op-maint-abi",
		},
	}
	if err := usage.Validate(); err != nil {
		t.Fatalf("external maintenance usage: %v", err)
	}
	fingerprint, err := usage.Fingerprint()
	if err != nil {
		t.Fatalf("external maintenance fingerprint: %v", err)
	}
	if !strings.HasPrefix(fingerprint, "provider-maintenance:v1:") {
		t.Fatalf("external maintenance fingerprint = %q, want the durable prefix", fingerprint)
	}
	var observed billing.MaintenanceUsage
	observer := billing.MaintenanceObserverFunc(func(_ context.Context, received billing.MaintenanceUsage) error {
		observed = received
		return nil
	})
	if err := observer.ObserveProviderMaintenance(context.Background(), usage); err != nil {
		t.Fatalf("external maintenance observer: %v", err)
	}
	if observed.OperationID != usage.OperationID {
		t.Fatalf("external maintenance observer received %q, want %q", observed.OperationID, usage.OperationID)
	}

	// The identity resolver carries a call-shaped account callback: an
	// external owner resolves the credited account from the actual request.
	var seen aipapi.Call
	resolver := &billing.IdentityResolver{
		AccountID: func(_ context.Context, call aipapi.Call) string {
			seen = call
			return "acct-external"
		},
		StoreID: func(context.Context) string { return "store-external" },
	}
	b := validBinding(t)
	b.IdentityResolver = resolver
	if err := b.Validate(); err != nil {
		t.Fatalf("an external call-shaped identity resolver must be accepted: %v", err)
	}
	if got := resolver.AccountID(context.Background(), aipapi.Call{ID: "call-external"}); got != "acct-external" {
		t.Fatalf("external account resolver = %q, want the owner's value", got)
	}
	if seen.ID != "call-external" {
		t.Fatalf("external account resolver saw call %q, want the actual request", seen.ID)
	}
}
