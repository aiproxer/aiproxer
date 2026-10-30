package metering_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

// Phase 3.2 public identity/validation contracts (requirements 5.5–5.7, 6.1–6.4;
// design Deterministic Identity, D2, D5, D6, D14).

func TestPhase32_SourceEventRef_LengthPrefixedKeyRejectsDelimiterAmbiguity(t *testing.T) {
	t.Parallel()
	base := phase3CustomerIngressFact("req-sep", "fe-sep", 1)
	base.IdentityVersion = metering.IdentityVersionV1
	base.SourceEventKind = "ingress"
	base.SourceID = "src-a"

	a := base
	a.StreamID = "customer-request:a"
	a.SourceID = "b\x00c"

	b := base
	b.StreamID = "customer-request:a\x00b"
	b.SourceID = "c"

	if a.SourceEventKey() == b.SourceEventKey() {
		t.Fatalf("NUL-shifted lifecycle/source fields must not collide under canonical encoding: %q", a.SourceEventKey())
	}
}

func TestPhase32_SourceEventKey_IncludesAllIdentityComponents(t *testing.T) {
	t.Parallel()
	base := phase3CustomerIngressFact("req-comp", "fe-comp", 7)
	base.IdentityVersion = metering.IdentityVersionV1
	base.SourceRevision = 2
	base.SourceEventKind = "fe-ingress"
	base.SourceID = "src-comp"
	base.RecordedAt = time.Unix(100, 0).UTC()
	wantKey := base.SourceEventKey()
	if wantKey == "" {
		t.Fatal("SourceEventKey empty")
	}

	cases := []struct {
		name string
		mut  func(*metering.Fact)
	}{
		{"identity_version", func(f *metering.Fact) { f.IdentityVersion = 2 }},
		{"lifecycle_id_stream", func(f *metering.Fact) { f.StreamID = "customer-request:other" }},
		{"boundary", func(f *metering.Fact) { f.Boundary = metering.BoundaryFrontendEgress }},
		{"source_event_kind", func(f *metering.Fact) { f.SourceEventKind = "other-kind" }},
		{"source_id", func(f *metering.Fact) { f.SourceID = "other-src" }},
		{"source_revision", func(f *metering.Fact) { f.SourceRevision = 9 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := base
			tc.mut(&f)
			if f.SourceEventKey() == wantKey {
				t.Fatalf("%s must change SourceEventKey", tc.name)
			}
		})
	}

	samePayload := base
	samePayload.Sequence = 99
	samePayload.FactID = "other-fact-id"
	samePayload.RecordedAt = time.Unix(999, 0).UTC()
	samePayload.Quantities = []metering.Quantity{{
		Component: metering.ComponentInputToken,
		Unit:      metering.UnitToken,
		Value:     99,
		Present:   true,
	}}
	if samePayload.SourceEventKey() != wantKey {
		t.Fatalf("Sequence/FactID/RecordedAt/quantities must not affect SourceEventKey when SourceID set: %q vs %q",
			samePayload.SourceEventKey(), wantKey)
	}
}

func TestPhase32_SourceEventKey_StableAcrossRetryClone(t *testing.T) {
	t.Parallel()
	a := phase3CustomerIngressFact("req-stable", "fe-stable", 1)
	a.IdentityVersion = 0 // historical producer: V1 default
	a.SourceEventKind = ""
	a.SourceID = ""
	a.RecordedAt = time.Unix(1, 0).UTC()
	b := a
	b.RecordedAt = time.Unix(2, 0).UTC()
	if a.SourceEventKey() != b.SourceEventKey() {
		t.Fatalf("retry/restart must keep SourceEventKey stable: %q vs %q", a.SourceEventKey(), b.SourceEventKey())
	}
	if a.EffectiveIdentityVersion() != metering.IdentityVersionV1 {
		t.Fatalf("IdentityVersion 0 must default to V1, got %d", a.EffectiveIdentityVersion())
	}
	explicit := a
	explicit.IdentityVersion = metering.IdentityVersionV1
	if explicit.SourceEventKey() != a.SourceEventKey() {
		t.Fatalf("explicit V1 must match zero-default V1 key: %q vs %q", explicit.SourceEventKey(), a.SourceEventKey())
	}
}

func TestPhase32_SourceEventRef_MatchesFactKey(t *testing.T) {
	t.Parallel()
	f := phase3OperatorEgressFact("att-ref", "be-ref", 3)
	f.IdentityVersion = metering.IdentityVersionV1
	f.SourceRevision = 4
	f.SourceEventKind = "be-egress"
	f.SourceID = "provider-evt-1"
	ref := f.SourceEventRef()
	if ref.LifecycleID != f.StreamID {
		t.Fatalf("LifecycleID=%q want StreamID %q", ref.LifecycleID, f.StreamID)
	}
	if ref.CanonicalKey() != f.SourceEventKey() {
		t.Fatalf("SourceEventRef.CanonicalKey=%q SourceEventKey=%q", ref.CanonicalKey(), f.SourceEventKey())
	}
}

func TestPhase32_MoneyPresentRequiresCurrency(t *testing.T) {
	t.Parallel()
	f := phase3OperatorEgressFact("att-cur", "be-cur", 1)
	f.Money = &metering.MoneyObservation{NanoUnits: 10, Present: true, Source: metering.SourceProviderReported}
	err := f.Validate()
	if err == nil {
		t.Fatal("Present money without currency must fail (D5)")
	}
	if !errors.Is(err, metering.ErrInvalidFact) {
		t.Fatalf("want ErrInvalidFact, got %v", err)
	}
}

func TestPhase32_AbsentQuantityForbidsNonZeroValue(t *testing.T) {
	t.Parallel()
	f := phase3CustomerIngressFact("req-abs", "fe-abs", 1)
	f.Quantities = []metering.Quantity{{
		Component: metering.ComponentInputToken,
		Unit:      metering.UnitToken,
		Value:     -1,
		Present:   false,
	}}
	if err := f.Validate(); err == nil {
		t.Fatal("absent quantity must reject non-zero value (req 6.4 presence semantics)")
	}
}

func TestPhase32_AbsentMoneyForbidsNonZeroNano(t *testing.T) {
	t.Parallel()
	f := phase3OperatorEgressFact("att-abs", "be-abs", 1)
	f.Money = &metering.MoneyObservation{NanoUnits: 5, Currency: "USD", Present: false}
	if err := f.Validate(); err == nil {
		t.Fatal("absent money must reject non-zero nano units")
	}
}

func TestPhase32_IdentityFieldLengthBound(t *testing.T) {
	t.Parallel()
	f := phase3CustomerIngressFact("req-len", "fe-len", 1)
	f.SourceID = strings.Repeat("x", metering.MaxSourceEventFieldLen+1)
	if err := f.Validate(); err == nil {
		t.Fatal("oversized SourceID must fail bounded identity validation")
	}
}

func TestPhase32_RevisedIdentityDistinctFromBase(t *testing.T) {
	t.Parallel()
	base := phase3CustomerIngressFact("req-rev", "fe-rev", 1)
	base.IdentityVersion = metering.IdentityVersionV1
	base.SourceID = "src"
	base.SourceEventKind = "k"
	rev := base
	rev.SourceRevision = 1
	if base.SourceEventKey() == rev.SourceEventKey() {
		t.Fatal("SourceRevision must distinguish revised identities (D6)")
	}
}

func TestPhase32_OperatorAuxiliaryLifecycleAllowed(t *testing.T) {
	t.Parallel()
	f := phase3OperatorIngressFact("att-aux", "be-aux", 1)
	f.Lifecycle = metering.LifecycleAuxiliaryRequest
	f.StreamID = "auxiliary-request:aux-1"
	if err := f.Validate(); err != nil {
		t.Fatalf("operator+auxiliary_request must validate: %v", err)
	}
}

func TestPhase32_SameFactReplay_TreatsZeroIdentityVersionAsV1(t *testing.T) {
	t.Parallel()
	a := phase3CustomerIngressFact("req-v0", "fe-v0", 1)
	a.IdentityVersion = 0
	b := a
	b.IdentityVersion = metering.IdentityVersionV1
	if !metering.SameFactReplay(a, b) {
		t.Fatal("IdentityVersion 0 and V1 must SameFactReplay for historical producers")
	}
}
