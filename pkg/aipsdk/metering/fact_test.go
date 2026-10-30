package metering_test

import (
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

func TestFact_Validate_IdentityRequired(t *testing.T) {
	t.Parallel()
	base := validFact()
	if err := base.Validate(); err != nil {
		t.Fatalf("valid fact: %v", err)
	}
	missingID := base
	missingID.FactID = ""
	if err := missingID.Validate(); err == nil {
		t.Fatal("FactID required")
	}
	missingStream := base
	missingStream.StreamID = ""
	if err := missingStream.Validate(); err == nil {
		t.Fatal("StreamID required")
	}
	negSeq := base
	negSeq.Sequence = -1
	if err := negSeq.Validate(); err == nil {
		t.Fatal("negative sequence rejected")
	}
}

func TestFact_SupersedesOnlyForCorrectionKinds(t *testing.T) {
	t.Parallel()
	delta := validFact()
	delta.Kind = metering.FactKindDelta
	delta.Supersedes = []string{"old"}
	if err := delta.Validate(); err == nil {
		t.Fatal("delta must not carry Supersedes")
	}

	correction := validFact()
	correction.Kind = metering.FactKindCorrection
	correction.Supersedes = []string{"old-fact"}
	if err := correction.Validate(); err != nil {
		t.Fatal(err)
	}
	emptySuper := correction
	emptySuper.Supersedes = nil
	if err := emptySuper.Validate(); err == nil {
		t.Fatal("correction requires non-empty Supersedes")
	}

	repl := validFact()
	repl.Kind = metering.FactKindAuthoritativeReplacement
	repl.Supersedes = []string{"old-fact"}
	if err := repl.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestFact_IdempotencyKey(t *testing.T) {
	t.Parallel()
	a := validFact()
	b := validFact()
	if a.IdempotencyKey() == "" || a.IdempotencyKey() != b.IdempotencyKey() {
		t.Fatalf("same identity must share key: %q vs %q", a.IdempotencyKey(), b.IdempotencyKey())
	}
	b.FactID = "other"
	if a.IdempotencyKey() == b.IdempotencyKey() {
		t.Fatal("different FactID must differ")
	}
}

func TestSameFactIdentity(t *testing.T) {
	t.Parallel()
	a := validFact()
	b := validFact()
	if !metering.SameFactIdentity(a, b) {
		t.Fatal("expected same identity")
	}
	b.Sequence = 2
	if metering.SameFactIdentity(a, b) {
		t.Fatal("sequence differs")
	}
}

func TestSameFactReplay(t *testing.T) {
	t.Parallel()
	a := validFact()
	a.Quantities = []metering.Quantity{
		{Component: metering.ComponentOutputToken, Unit: metering.UnitToken, Value: 2, Present: true},
		{Component: metering.ComponentInputToken, Unit: metering.UnitToken, Value: 1, Present: true},
	}
	a.Money = &metering.MoneyObservation{NanoUnits: 10, Currency: "USD", Present: true, Source: metering.SourceObserved}
	a.Supersedes = nil

	identical := a
	identical.Quantities = []metering.Quantity{
		{Component: metering.ComponentInputToken, Unit: metering.UnitToken, Value: 1, Present: true},
		{Component: metering.ComponentOutputToken, Unit: metering.UnitToken, Value: 2, Present: true},
	}
	identical.Money = &metering.MoneyObservation{NanoUnits: 10, Currency: "USD", Present: true, Source: metering.SourceObserved}
	if !metering.SameFactReplay(a, identical) {
		t.Fatal("expected same replay with sort-independent quantities")
	}

	diffKind := a
	diffKind.Kind = metering.FactKindDelta
	if metering.SameFactReplay(a, diffKind) {
		t.Fatal("different Kind must not be same replay")
	}

	diffQty := a
	diffQty.Quantities = []metering.Quantity{
		{Component: metering.ComponentInputToken, Unit: metering.UnitToken, Value: 99, Present: true},
	}
	if metering.SameFactReplay(a, diffQty) {
		t.Fatal("different Quantities must not be same replay")
	}

	diffMoney := a
	diffMoney.Money = &metering.MoneyObservation{NanoUnits: 11, Currency: "USD", Present: true, Source: metering.SourceObserved}
	if metering.SameFactReplay(a, diffMoney) {
		t.Fatal("different Money must not be same replay")
	}

	diffPresence := a
	diffPresence.Presence = metering.PresenceAbsent
	if metering.SameFactReplay(a, diffPresence) {
		t.Fatal("different Presence must not be same replay")
	}

	diffSeq := a
	diffSeq.Sequence = 2
	if metering.SameFactReplay(a, diffSeq) {
		t.Fatal("different Sequence must not be same replay")
	}

	corr := validFact()
	corr.Kind = metering.FactKindCorrection
	corr.Supersedes = []string{"b", "a"}
	corrSame := corr
	corrSame.Supersedes = []string{"a", "b"}
	if !metering.SameFactReplay(corr, corrSame) {
		t.Fatal("expected same replay with sort-independent Supersedes")
	}
	corrDiff := corr
	corrDiff.Supersedes = []string{"a", "c"}
	if metering.SameFactReplay(corr, corrDiff) {
		t.Fatal("different Supersedes must not be same replay")
	}
}

func TestSameFactReplay_RequiresAllSemanticPayloadFields(t *testing.T) {
	t.Parallel()
	base := validFact()
	base.Correlation = metering.Correlation{RequestID: "request-1", TraceID: "trace-1"}
	base.Scope = scope.PrincipalScopeView{
		PrincipalID: scope.Known("principal-1"),
		Roles:       []string{"developer"},
		SafeClaims:  map[string]string{"tier": "gold"},
	}
	base.FrontendID = "frontend-1"
	base.BackendID = "backend-1"
	base.Model = "model-1"
	base.AttemptOutcome = metering.AttemptOutcomeWinner
	base.Surfaced = metering.SurfacedYes
	base.PolicyVersion = metering.VersionRef{ID: "policy-1", Version: "v1", EffectiveAt: 1, FetchedAt: 2}

	tests := []struct {
		name   string
		mutate func(*metering.Fact)
	}{
		{"correlation", func(f *metering.Fact) { f.Correlation.TraceID = "trace-2" }},
		{"scope value", func(f *metering.Fact) { f.Scope.PrincipalID = scope.Known("principal-2") }},
		{"scope roles", func(f *metering.Fact) { f.Scope.Roles = []string{"operator"} }},
		{"scope claims", func(f *metering.Fact) { f.Scope.SafeClaims = map[string]string{"tier": "silver"} }},
		{"frontend", func(f *metering.Fact) { f.FrontendID = "frontend-2" }},
		{"backend", func(f *metering.Fact) { f.BackendID = "backend-2" }},
		{"model", func(f *metering.Fact) { f.Model = "model-2" }},
		{"attempt outcome", func(f *metering.Fact) { f.AttemptOutcome = metering.AttemptOutcomeLoser }},
		{"surfaced", func(f *metering.Fact) { f.Surfaced = metering.SurfacedNo }},
		{"policy version", func(f *metering.Fact) { f.PolicyVersion.Version = "v2" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			changed := base
			changed.Scope = base.Scope.Clone()
			tt.mutate(&changed)
			if metering.SameFactReplay(base, changed) {
				t.Fatal("semantic payload change must not be accepted as an identical replay")
			}
		})
	}

	// RecordedAt is store-assigned when omitted, so timestamp drift alone must not
	// turn a retry of the same producer fact into an identity collision.
	recordedLater := base
	recordedLater.RecordedAt = base.RecordedAt.Add(time.Second)
	if !metering.SameFactReplay(base, recordedLater) {
		t.Fatal("RecordedAt must be excluded from producer replay equality")
	}
}

func validFact() metering.Fact {
	return metering.Fact{
		FactID:      "fact-1",
		StreamID:    "stream-1",
		Sequence:    1,
		Kind:        metering.FactKindCumulative,
		Perspective: metering.PerspectiveCustomer,
		Boundary:    metering.BoundaryFrontendIngress,
		Lifecycle:   metering.LifecycleLogicalRequest,
		Correlation: metering.Correlation{RequestID: "req-1", ALegID: "a-1"},
		Scope:       scope.PrincipalScopeView{},
		Source:      metering.SourceObserved,
		Authority:   metering.AuthorityAuthoritative,
		Presence:    metering.PresencePresent,
		Surfaced:    metering.SurfacedUnknown,
		RecordedAt:  time.Unix(1, 0).UTC(),
	}
}
