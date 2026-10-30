package billing_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/billing"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/economics"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

func knownTestScope() scope.PrincipalScopeView {
	return scope.PrincipalScopeView{
		SubjectKind: scope.SubjectHuman,
		PrincipalID: scope.Known("user-1"),
		TenantID:    scope.Known("tenant-1"),
		Origin:      scope.OriginClient,
	}
}

// TestTerminalAckMatchesEnvelope locks the C7 durability invariant: an
// acknowledgement answers exactly the envelope it was issued for, identified
// by store and envelope ID.
func TestTerminalAckMatchesEnvelope(t *testing.T) {
	t.Parallel()
	env := billing.TerminalEnvelope{StoreID: "store-a", EnvelopeID: "env-1"}
	ack := billing.TerminalAck{StoreID: "store-a", EnvelopeID: "env-1", Revision: 1}
	if !ack.MatchesEnvelope(env) {
		t.Fatal("exact store/envelope identity must match")
	}
	for name, other := range map[string]billing.TerminalAck{
		"wrong store":    {StoreID: "store-b", EnvelopeID: "env-1", Revision: 1},
		"wrong envelope": {StoreID: "store-a", EnvelopeID: "env-2", Revision: 1},
		"both foreign":   {StoreID: "store-b", EnvelopeID: "env-2", Revision: 7},
	} {
		if other.MatchesEnvelope(env) {
			t.Fatalf("%s: foreign acknowledgement must not match", name)
		}
	}
}

// TestTerminalAckMismatchIsTyped ensures a mismatch surfaces through a
// stable sentinel suitable for errors.Is, so callers never mistake a
// foreign acknowledgement for durable success.
func TestTerminalAckMismatchIsTyped(t *testing.T) {
	t.Parallel()
	wrapped := fmt.Errorf("handoff: %w", billing.ErrTerminalAckMismatch)
	if !errors.Is(wrapped, billing.ErrTerminalAckMismatch) {
		t.Fatal("ErrTerminalAckMismatch must survive wrapping for errors.Is")
	}
}

func lineageEnvelopeBase() billing.TerminalEnvelope {
	return billing.TerminalEnvelope{
		Version:      billing.TerminalEnvelopeVersionV1,
		EnvelopeID:   "env-lineage-1",
		StoreID:      "store-test",
		AccountID:    "acct-1",
		ALegID:       "aleg-1",
		SubmissionID: "sub-1",
		Outcome:      billing.TerminalCompleted,
		Tariff:       economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "tariff-snap", Version: "v1"}},
		Policy:       economics.PolicySnapshotRef{VersionRef: economics.VersionRef{ID: "policy-snap", Version: "v1"}, PolicyID: "policy-1"},
		PayloadHash:  strings.Repeat("a", 64),
	}
}

func lineageLegEnvelope() billing.TerminalEnvelope {
	env := lineageEnvelopeBase()
	env.AccountID = ""
	env.Scope = knownTestScope()
	env.Scope = knownTestScope()
	env.Subject = metering.SubjectRef{
		Kind: metering.SubjectBLeg, StoreID: "store-test",
		BillingCallID: "call-1", BLegID: "bleg-1", AttemptSeq: 3,
		ALegID: "aleg-1", SubmissionID: "sub-1",
	}
	env.Correlation = metering.CorrelationV2{
		StoreID: "store-test", BillingCallID: "call-1", BLegID: "bleg-1",
		AttemptSeq: 3, ALegID: "aleg-1", SubmissionID: "sub-1",
	}
	env.AttemptSeq = 3
	env.Workload = &billing.TerminalWorkload{Class: "auxiliary", Role: "test-role"}
	return env
}

func lineageCallEnvelope() billing.TerminalEnvelope {
	env := lineageEnvelopeBase()
	env.Scope = knownTestScope()
	env.Subject = metering.SubjectRef{
		Kind: metering.SubjectBillingCall, StoreID: "store-test",
		BillingCallID: "call-1", ALegID: "aleg-1", SubmissionID: "sub-1",
	}
	env.Correlation = metering.CorrelationV2{
		StoreID: "store-test", BillingCallID: "call-1",
		ALegID: "aleg-1", SubmissionID: "sub-1",
	}
	env.ExpectedBLegIDs = []string{"bleg-1", "bleg-2"}
	return env
}

// TestTerminalEnvelopeRequiresLegLineageAndSequence locks the B-leg closure
// contract: full call/leg lineage, positive authoritative sequence, and no
// required account (leg records carry none).
func TestTerminalEnvelopeRequiresLegLineageAndSequence(t *testing.T) {
	t.Parallel()
	if err := lineageLegEnvelope().Validate(); err != nil {
		t.Fatalf("fixture leg envelope: %v", err)
	}
	for name, mutate := range map[string]func(*billing.TerminalEnvelope){
		"missing a-leg":   func(e *billing.TerminalEnvelope) { e.ALegID = "" },
		"zero sequence":   func(e *billing.TerminalEnvelope) { e.AttemptSeq = 0 },
		"missing b-leg":   func(e *billing.TerminalEnvelope) { e.Subject.BLegID = "" },
		"missing subject": func(e *billing.TerminalEnvelope) { e.Subject = metering.SubjectRef{} },
	} {
		env := lineageLegEnvelope()
		mutate(&env)
		if err := env.Validate(); err == nil {
			t.Fatalf("%s: expected validation failure", name)
		}
	}
}

// TestTerminalEnvelopeRequiresCallLineage locks the call-closure contract:
// account, A-leg, and expected-B-leg coverage are required, while no leg
// attempt sequence applies.
func TestTerminalEnvelopeRequiresCallLineage(t *testing.T) {
	t.Parallel()
	env := lineageCallEnvelope()
	if err := env.Validate(); err != nil {
		t.Fatalf("fixture call envelope: %v", err)
	}
	if env.AttemptSeq != 0 {
		t.Fatal("call closures carry no leg attempt sequence")
	}
	for name, mutate := range map[string]func(*billing.TerminalEnvelope){
		"missing account": func(e *billing.TerminalEnvelope) { e.AccountID = "" },
		"missing a-leg":   func(e *billing.TerminalEnvelope) { e.ALegID = "" },
	} {
		mut := lineageCallEnvelope()
		mutate(&mut)
		if err := mut.Validate(); err == nil {
			t.Fatalf("%s: expected validation failure", name)
		}
	}
}

// TestTerminalEnvelopeValidatesCoverage locks deterministic closure
// coverage: bounded unique well-formed B-leg IDs.
func TestTerminalEnvelopeValidatesCoverage(t *testing.T) {
	t.Parallel()
	env := lineageCallEnvelope()
	env.ExpectedBLegIDs = []string{"bleg-1", "bleg-2"}
	if err := env.Validate(); err != nil {
		t.Fatalf("coverage fixture: %v", err)
	}
	duplicates := lineageCallEnvelope()
	duplicates.ExpectedBLegIDs = []string{"bleg-1", "bleg-1"}
	if err := duplicates.Validate(); err == nil {
		t.Fatal("duplicate coverage must fail")
	}
	colon := lineageCallEnvelope()
	colon.ExpectedBLegIDs = []string{"call:leg"}
	if err := colon.Validate(); err == nil {
		t.Fatal("colon coverage must fail")
	}
	over := lineageCallEnvelope()
	over.ExpectedBLegIDs = make([]string, billing.MaxExpectedBLegIDs+1)
	for i := range over.ExpectedBLegIDs {
		over.ExpectedBLegIDs[i] = "bleg-id"
	}
	if err := over.Validate(); err == nil {
		t.Fatal("over-bound coverage must fail")
	}
}

// TestTerminalEnvelopeValidatesWorkload locks the typed parent/workload
// correlation: bounded safe labels, absent when the record carries none.
func TestTerminalEnvelopeValidatesWorkload(t *testing.T) {
	t.Parallel()
	env := lineageLegEnvelope()
	env.Workload = nil
	if err := env.Validate(); err != nil {
		t.Fatalf("absent workload must be valid: %v", err)
	}
	bad := lineageLegEnvelope()
	bad.Workload = &billing.TerminalWorkload{Class: strings.Repeat("x", 513)}
	if err := bad.Validate(); err == nil {
		t.Fatal("over-bound workload class must fail")
	}
}

// TestTerminalEnvelopeRejectsForeignObservationLineage locks cross-field
// consistency between the envelope lineage and each carried observation.
func TestTerminalEnvelopeRejectsForeignObservationLineage(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	foreignCall := lineageLegEnvelope()
	foreignCall.Observations = []metering.Observation{{
		Version:        metering.ObservationVersionV2,
		ID:             "obs-foreign",
		SourceEventKey: "evt-foreign",
		Revision:       1,
		StreamID:       "stream-foreign",
		Origin:         metering.OriginLocal,
		Acquisition:    metering.AcquisitionLocalTransport,
		Authority:      metering.AuthorityObservedClaim,
		Perspective:    metering.PerspectiveOperator,
		Boundary:       metering.BoundaryBackendEgress,
		Lifecycle:      metering.LifecycleBackendAttempt,
		Subject:        metering.SubjectRef{Kind: metering.SubjectBLeg, StoreID: "store-test", BillingCallID: "call-foreign", BLegID: "bleg-1"},
		Correlation:    metering.CorrelationV2{StoreID: "store-test", BillingCallID: "call-foreign", BLegID: "bleg-1"},
		Semantics:      metering.SemanticsDelta,
		ObservedAt:     now,
		ReceivedAt:     now,
		MappingRef:     "mapping-test",
	}}
	// Observations without measures/charges/evidence need unavailable authority.
	foreignCall.Observations[0].Authority = metering.AuthorityUnavailableClaim
	if err := foreignCall.Validate(); err == nil {
		t.Fatal("foreign-call observation must fail")
	}
	foreignLeg := lineageLegEnvelope()
	foreignLeg.Observations = []metering.Observation{{
		Version:        metering.ObservationVersionV2,
		ID:             "obs-foreign-leg",
		SourceEventKey: "evt-foreign-leg",
		Revision:       1,
		StreamID:       "stream-foreign-leg",
		Origin:         metering.OriginLocal,
		Acquisition:    metering.AcquisitionLocalTransport,
		Authority:      metering.AuthorityUnavailableClaim,
		Perspective:    metering.PerspectiveOperator,
		Boundary:       metering.BoundaryBackendEgress,
		Lifecycle:      metering.LifecycleBackendAttempt,
		Subject:        metering.SubjectRef{Kind: metering.SubjectBLeg, StoreID: "store-test", BillingCallID: "call-1", BLegID: "bleg-foreign"},
		Correlation:    metering.CorrelationV2{StoreID: "store-test", BillingCallID: "call-1", BLegID: "bleg-foreign"},
		Semantics:      metering.SemanticsDelta,
		ObservedAt:     now,
		ReceivedAt:     now,
		MappingRef:     "mapping-test",
	}}
	if err := foreignLeg.Validate(); err == nil {
		t.Fatal("foreign-leg observation must fail")
	}
}

// fullLineageObservation builds a runtime-shaped B-leg observation carrying
// the complete applicable lineage in both subject and correlation, mirroring
// runtime-generated evidence.
func fullLineageObservation() metering.Observation {
	now := time.Now().UTC()
	qty, err := metering.ParseDecimal("100")
	if err != nil {
		panic(err)
	}
	return metering.Observation{
		Version:        metering.ObservationVersionV2,
		ID:             "obs-full-1",
		SourceEventKey: "evt-full-1",
		Revision:       1,
		StreamID:       "stream-full-1",
		Sequence:       1,
		Origin:         metering.OriginLocal,
		Acquisition:    metering.AcquisitionLocalTransport,
		Authority:      metering.AuthorityObservedClaim,
		Perspective:    metering.PerspectiveOperator,
		Boundary:       metering.BoundaryBackendEgress,
		Lifecycle:      metering.LifecycleBackendAttempt,
		Subject: metering.SubjectRef{
			Kind: metering.SubjectBLeg, StoreID: "store-test",
			TenantID: "tenant-1", AccountID: "acct-1",
			ALegID: "aleg-1", BillingCallID: "call-1", BLegID: "bleg-1",
			AttemptSeq: 3, SubmissionID: "sub-1",
		},
		Correlation: metering.CorrelationV2{
			StoreID: "store-test", BillingCallID: "call-1", BLegID: "bleg-1",
			AttemptSeq: 3, ALegID: "aleg-1", SubmissionID: "sub-1",
			ParentWorkID: "parent-1",
		},
		Scope:      knownTestScope(),
		Semantics:  metering.SemanticsDelta,
		ObservedAt: now,
		ReceivedAt: now,
		MappingRef: "mapping-full-v1",
		Measures: []metering.Measure{{
			Key:     metering.ComponentKey{Direction: metering.DirectionInput, Component: metering.ComponentInputTokenUncached, Unit: metering.UnitToken},
			Value:   &qty,
			Quality: metering.QualityObserved,
		}},
	}
}

func fullLineageEnvelope() billing.TerminalEnvelope {
	env := lineageLegEnvelope()
	env.AccountID = "acct-1"
	env.ParentWorkID = "parent-1"
	env.Scope = knownTestScope()
	return env
}

// TestTerminalEnvelopeCloneDeepCopiesCoverage locks clone independence for
// the new coverage and workload members.
func TestTerminalEnvelopeCloneDeepCopiesCoverage(t *testing.T) {
	t.Parallel()
	env := lineageCallEnvelope()
	env.Workload = &billing.TerminalWorkload{Class: "auxiliary", Role: "test-role"}
	dup := env.Clone()
	dup.ExpectedBLegIDs[0] = "mutated"
	dup.Workload.Class = "mutated"
	if env.ExpectedBLegIDs[0] == "mutated" {
		t.Fatal("Clone must deep-copy coverage")
	}
	if env.Workload.Class == "mutated" {
		t.Fatal("Clone must deep-copy workload")
	}
}

// TestTerminalEnvelopeRejectsForeignCorrelation locks the envelope's own
// correlation carrier to the frozen lineage, independent of observations:
// call, A-leg, submission, B-leg, attempt sequence, and parent work must
// agree with the top-level/subject lineage when present.
func TestTerminalEnvelopeRejectsForeignCorrelation(t *testing.T) {
	t.Parallel()
	cases := map[string]func(*billing.TerminalEnvelope){
		"foreign call": func(e *billing.TerminalEnvelope) {
			e.Correlation.BillingCallID = "call-foreign"
		},
		"foreign a-leg": func(e *billing.TerminalEnvelope) {
			e.Correlation.ALegID = "aleg-foreign"
		},
		"foreign submission": func(e *billing.TerminalEnvelope) {
			e.Correlation.SubmissionID = "sub-foreign"
		},
		"foreign b-leg": func(e *billing.TerminalEnvelope) {
			e.Correlation.BLegID = "bleg-foreign"
		},
		"foreign attempt": func(e *billing.TerminalEnvelope) {
			e.Correlation.AttemptSeq = 9
		},
		"foreign parent": func(e *billing.TerminalEnvelope) {
			e.ParentWorkID = "parent-1"
			e.Correlation.ParentWorkID = "parent-foreign"
		},
	}
	for name, mutate := range cases {
		env := lineageLegEnvelope()
		mutate(&env)
		if err := env.Validate(); !errors.Is(err, billing.ErrInvalidTerminal) {
			t.Fatalf("%s: err=%v, want ErrInvalidTerminal", name, err)
		}
	}
}

// TestTerminalEnvelopeRejectsForeignObservationTenant locks trusted scope
// agreement beyond the principal: the same principal under a foreign tenant
// is a foreign attribution.
func TestTerminalEnvelopeRejectsForeignObservationTenant(t *testing.T) {
	t.Parallel()
	env := fullLineageEnvelope()
	obs := fullLineageObservation()
	tenant := knownTestScope()
	tenant.TenantID = scope.Known("tenant-foreign")
	obs.Scope = tenant
	if err := obs.Validate(); err != nil {
		t.Fatalf("mutated observation must stay valid: %v", err)
	}
	env.Observations = []metering.Observation{obs}
	if err := env.Validate(); !errors.Is(err, billing.ErrInvalidTerminal) {
		t.Fatalf("err=%v, want ErrInvalidTerminal", err)
	}
}

// TestTerminalEnvelopeAcceptsRuntimeShapedObservation locks the positive
// case: a runtime-shaped observation whose every present lineage field
// agrees with the envelope validates.
func TestTerminalEnvelopeAcceptsRuntimeShapedObservation(t *testing.T) {
	t.Parallel()
	env := fullLineageEnvelope()
	obs := fullLineageObservation()
	if err := obs.Validate(); err != nil {
		t.Fatalf("runtime-shaped observation invalid: %v", err)
	}
	env.Observations = []metering.Observation{obs}
	if err := env.Validate(); err != nil {
		t.Fatalf("agreeing lineage must validate: %v", err)
	}
}

// stripCorrelationLineage returns the observation with correlation lineage
// removed (store and B-leg ownership kept, which metering requires on both
// carriers), so exactly one subject axis under test can disagree with the
// envelope while the observation stays internally valid.
func stripCorrelationLineage(obs metering.Observation) metering.Observation {
	obs.Correlation = metering.CorrelationV2{StoreID: obs.Correlation.StoreID, BLegID: obs.Correlation.BLegID}
	return obs
}

// stripSubjectLineage returns the observation with the named subject lineage
// cleared, so exactly one correlation axis under test can disagree while the
// observation stays internally valid. Only optional subject fields are
// clearable; kind-mandatory identity stays.
func stripSubjectLineage(obs metering.Observation) metering.Observation {
	obs.Subject.BillingCallID = ""
	obs.Subject.ALegID = ""
	obs.Subject.SubmissionID = ""
	obs.Subject.AttemptSeq = 0
	obs.Subject.AccountID = ""
	return obs
}

// TestTerminalEnvelopeRejectsSubjectLineageMismatch locks every present
// subject lineage axis: call, leg, A-leg, submission, sequence, and account.
func TestTerminalEnvelopeRejectsSubjectLineageMismatch(t *testing.T) {
	t.Parallel()
	cases := map[string]func(*metering.Observation){
		"foreign call": func(o *metering.Observation) {
			*o = stripCorrelationLineage(*o)
			o.Subject.BillingCallID = "call-foreign"
		},
		"foreign b-leg": func(o *metering.Observation) {
			*o = stripCorrelationLineage(*o)
			o.Subject.BLegID = "bleg-foreign"
			o.Correlation.BLegID = "bleg-foreign"
		},
		"foreign a-leg": func(o *metering.Observation) {
			*o = stripCorrelationLineage(*o)
			o.Subject.ALegID = "aleg-foreign"
		},
		"foreign submission": func(o *metering.Observation) {
			*o = stripCorrelationLineage(*o)
			o.Subject.SubmissionID = "sub-foreign"
		},
		"foreign sequence": func(o *metering.Observation) {
			*o = stripCorrelationLineage(*o)
			o.Subject.AttemptSeq = 9
		},
		"foreign account": func(o *metering.Observation) {
			*o = stripCorrelationLineage(*o)
			o.Subject.AccountID = "acct-foreign"
		},
	}
	for name, mutate := range cases {
		env := fullLineageEnvelope()
		obs := fullLineageObservation()
		mutate(&obs)
		if err := obs.Validate(); err != nil {
			t.Fatalf("%s: mutated observation must stay valid: %v", name, err)
		}
		env.Observations = []metering.Observation{obs}
		if err := env.Validate(); !errors.Is(err, billing.ErrInvalidTerminal) {
			t.Fatalf("%s: err=%v, want ErrInvalidTerminal", name, err)
		}
	}
}

// TestTerminalEnvelopeRejectsCorrelationLineageMismatch locks every present
// correlation axis that metering subject/correlation agreement leaves free:
// call, A-leg, submission, attempt sequence, and parent work identity.
// B-leg correlation agreement is already enforced by metering itself, and
// the envelope subject check covers the B-leg axis here.
func TestTerminalEnvelopeRejectsCorrelationLineageMismatch(t *testing.T) {
	t.Parallel()
	cases := map[string]func(*metering.Observation){
		"foreign call": func(o *metering.Observation) {
			*o = stripSubjectLineage(*o)
			o.Correlation.BillingCallID = "call-foreign"
		},
		"foreign a-leg": func(o *metering.Observation) {
			*o = stripSubjectLineage(*o)
			o.Correlation.ALegID = "aleg-foreign"
		},
		"foreign submission": func(o *metering.Observation) {
			*o = stripSubjectLineage(*o)
			o.Correlation.SubmissionID = "sub-foreign"
		},
		"foreign attempt": func(o *metering.Observation) {
			*o = stripSubjectLineage(*o)
			o.Correlation.AttemptSeq = 9
		},
		"foreign parent": func(o *metering.Observation) {
			o.Correlation.ParentWorkID = "parent-foreign"
		},
	}
	for name, mutate := range cases {
		env := fullLineageEnvelope()
		obs := fullLineageObservation()
		mutate(&obs)
		if err := obs.Validate(); err != nil {
			t.Fatalf("%s: mutated observation must stay valid: %v", name, err)
		}
		env.Observations = []metering.Observation{obs}
		if err := env.Validate(); !errors.Is(err, billing.ErrInvalidTerminal) {
			t.Fatalf("%s: err=%v, want ErrInvalidTerminal", name, err)
		}
	}
}

// TestTerminalEnvelopeRejectsScopePrincipalMismatch locks trusted scope
// agreement: a known observation principal differing from the envelope
// principal is a foreign attribution.
func TestTerminalEnvelopeRejectsScopePrincipalMismatch(t *testing.T) {
	t.Parallel()
	env := fullLineageEnvelope()
	obs := fullLineageObservation()
	obs.Scope.PrincipalID = scope.Known("user-foreign")
	if err := obs.Validate(); err != nil {
		t.Fatalf("mutated observation must stay valid: %v", err)
	}
	env.Observations = []metering.Observation{obs}
	if err := env.Validate(); !errors.Is(err, billing.ErrInvalidTerminal) {
		t.Fatalf("err=%v, want ErrInvalidTerminal", err)
	}
}

// TestTerminalEnvelopeAllowsAccountWindowEvidence locks the documented
// allowance: explicitly account-window evidence without request attribution
// validates, gated by its typed subject kind.
func TestTerminalEnvelopeAllowsAccountWindowEvidence(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	gauge, err := metering.ParseDecimal("25")
	if err != nil {
		t.Fatal(err)
	}
	env := fullLineageEnvelope()
	env.Observations = []metering.Observation{{
		Version:        metering.ObservationVersionV2,
		ID:             "obs-gauge-1",
		SourceEventKey: "evt-gauge-1",
		Revision:       1,
		StreamID:       "stream-gauge-1",
		Origin:         metering.OriginProvider,
		Acquisition:    metering.AcquisitionProviderHeader,
		Authority:      metering.AuthorityObservedClaim,
		Perspective:    metering.PerspectiveOperator,
		Boundary:       metering.BoundaryBackendEgress,
		Lifecycle:      metering.LifecycleBackendAttempt,
		Subject: metering.SubjectRef{
			Kind: metering.SubjectAccountWindow, StoreID: "store-test",
			ProviderAccountKey: "provider-acct-1", PoolID: "pool-1", WindowID: "window-1",
			ResetAt: now,
		},
		Correlation: metering.CorrelationV2{StoreID: "store-test"},
		Semantics:   metering.SemanticsGauge,
		ObservedAt:  now,
		ReceivedAt:  now,
		MappingRef:  "mapping-gauge-v1",
		Measures: []metering.Measure{{
			Key:     metering.ComponentKey{Direction: metering.DirectionNone, Component: metering.ComponentCredit, Unit: metering.UnitCredit},
			Value:   &gauge,
			Quality: metering.QualityObserved,
		}},
	}}
	if err := env.Observations[0].Validate(); err != nil {
		t.Fatalf("gauge observation invalid: %v", err)
	}
	if err := env.Validate(); err != nil {
		t.Fatalf("account-window evidence must validate: %v", err)
	}
}

// TestTerminalEnvelopeRequiresSubjectSequenceAgreement locks the reviewer
// probe: for a B-leg envelope, a present subject attempt sequence must equal
// the top-level authoritative sequence and the correlation sequence.
// Top-level/correlation 3 with subject 9 fails; exact 3/3/3 succeeds.
func TestTerminalEnvelopeRequiresSubjectSequenceAgreement(t *testing.T) {
	t.Parallel()
	exact := lineageLegEnvelope()
	if err := exact.Validate(); err != nil {
		t.Fatalf("3/3/3 envelope: %v", err)
	}
	drifted := lineageLegEnvelope()
	drifted.Subject.AttemptSeq = 9
	if err := drifted.Validate(); !errors.Is(err, billing.ErrInvalidTerminal) {
		t.Fatalf("subject sequence drift err=%v, want ErrInvalidTerminal", err)
	}
}

// TestTerminalEnvelopeRequiresCustomerScope locks the monetary closure
// boundary: leg and call envelopes must carry a trusted customer scope with
// a known principal. Scope-free envelopes are not attributable.
func TestTerminalEnvelopeRequiresCustomerScope(t *testing.T) {
	t.Parallel()
	leg := lineageLegEnvelope()
	leg.Scope = scope.PrincipalScopeView{}
	if err := leg.Validate(); !errors.Is(err, billing.ErrInvalidTerminal) {
		t.Fatalf("scope-free leg err=%v, want ErrInvalidTerminal", err)
	}
	call := lineageCallEnvelope()
	call.Scope = scope.PrincipalScopeView{}
	if err := call.Validate(); !errors.Is(err, billing.ErrInvalidTerminal) {
		t.Fatalf("scope-free call err=%v, want ErrInvalidTerminal", err)
	}
	anonymous := lineageCallEnvelope()
	sc := knownTestScope()
	sc.PrincipalID = scope.Unknown()
	anonymous.Scope = sc
	if err := anonymous.Validate(); !errors.Is(err, billing.ErrInvalidTerminal) {
		t.Fatalf("anonymous scope err=%v, want ErrInvalidTerminal", err)
	}
}
