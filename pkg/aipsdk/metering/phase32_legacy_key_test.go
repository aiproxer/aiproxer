package metering_test

import (
	"slices"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

func TestPhase32_LegacySourceEventKeyPhase31_LiteralZeroVersion(t *testing.T) {
	t.Parallel()
	f := phase3CustomerIngressFact("req-leg", "fe-leg", 1)
	f.IdentityVersion = 0
	f.SourceEventKind = ""
	f.SourceID = ""
	legacy0 := f.LegacySourceEventKeyPhase31()
	wantPrefix := "0\x00"
	if len(legacy0) < 2 || legacy0[:2] != wantPrefix {
		t.Fatalf("phase31 legacy key must start with literal IdentityVersion 0, got %q", legacy0)
	}
	if legacy0 == f.SourceEventKey() {
		t.Fatal("legacy NUL key must differ from canonical length-prefixed key")
	}
	v1 := f
	v1.IdentityVersion = metering.IdentityVersionV1
	legacy1 := v1.LegacySourceEventKeyPhase31()
	keys := f.SourceEventLookupKeys()
	want := []string{f.SourceEventKey(), legacy0, legacy1, f.IdempotencyKey()}
	if len(keys) != len(want) {
		t.Fatalf("lookup keys want %#v, got %#v", want, keys)
	}
	for i := range want {
		if keys[i] != want[i] {
			t.Fatalf("lookup key order wrong at %d: want %#v got %#v", i, want, keys)
		}
	}
}

func TestPhase32_SourceEventLookupKeys_EffectiveV1IncludesBothLiteralNUL(t *testing.T) {
	t.Parallel()
	base := phase3CustomerIngressFact("req-keys", "fe-keys", 1)
	base.SourceEventKind = "k"
	base.SourceID = "src"

	for _, raw := range []int{0, metering.IdentityVersionV1} {
		f := base
		f.IdentityVersion = raw
		keys := f.SourceEventLookupKeys()
		v0 := f
		v0.IdentityVersion = 0
		v1 := f
		v1.IdentityVersion = metering.IdentityVersionV1
		legacy0 := v0.LegacySourceEventKeyPhase31()
		legacy1 := v1.LegacySourceEventKeyPhase31()
		if !containsKey(keys, legacy0) || !containsKey(keys, legacy1) {
			t.Fatalf("raw IdentityVersion=%d must include literal-0 and literal-1 NUL keys: %#v", raw, keys)
		}
		if keys[0] != f.SourceEventKey() {
			t.Fatalf("canonical must be first for raw=%d", raw)
		}
		if keys[len(keys)-1] != f.IdempotencyKey() {
			t.Fatalf("IdempotencyKey must be last for raw=%d", raw)
		}
	}
}

func TestPhase32_SourceEventLookupKeys_V2NoV0V1Alias(t *testing.T) {
	t.Parallel()
	f := phase3CustomerIngressFact("req-v2", "fe-v2", 1)
	f.IdentityVersion = 2
	f.SourceEventKind = "k"
	f.SourceID = "src"
	keys := f.SourceEventLookupKeys()
	legacyOwn := f.LegacySourceEventKeyPhase31()
	v0 := f
	v0.IdentityVersion = 0
	v1 := f
	v1.IdentityVersion = metering.IdentityVersionV1
	legacy0 := v0.LegacySourceEventKeyPhase31()
	legacy1 := v1.LegacySourceEventKeyPhase31()
	want := []string{f.SourceEventKey(), legacyOwn, f.IdempotencyKey()}
	if len(keys) != len(want) {
		t.Fatalf("V2 keys want %#v got %#v", want, keys)
	}
	for i := range want {
		if keys[i] != want[i] {
			t.Fatalf("V2 key order wrong: %#v", keys)
		}
	}
	if containsKey(keys, legacy0) || containsKey(keys, legacy1) {
		t.Fatalf("V2 must not include V0/V1 NUL aliases: %#v", keys)
	}
}

func containsKey(keys []string, want string) bool {
	return slices.Contains(keys, want)
}

func TestPhase32_SameFactReplay_EffectiveKindAndSourceID(t *testing.T) {
	t.Parallel()
	a := phase3CustomerIngressFact("req-eff", "fe-eff", 1)
	a.IdentityVersion = 0
	a.SourceEventKind = ""
	a.SourceID = ""
	b := a
	b.IdentityVersion = metering.IdentityVersionV1
	b.SourceEventKind = string(a.Kind)
	b.SourceID = a.FactID
	if !metering.SameFactReplay(a, b) {
		t.Fatal("empty SourceEventKind/SourceID and IdentityVersion 0 must SameFactReplay with explicit Kind/FactID/V1")
	}
}

func TestPhase32_SourceRevision_NoFalseAliasWithLengthPrefix(t *testing.T) {
	t.Parallel()
	base := phase3CustomerIngressFact("req-sr", "fe-sr", 1)
	base.IdentityVersion = metering.IdentityVersionV1
	base.SourceID = "src"
	base.SourceEventKind = "k"
	base.SourceRevision = 1
	other := base
	other.SourceRevision = 10
	if base.SourceEventKey() == other.SourceEventKey() {
		t.Fatal("SourceRevision 1 vs 10 must not alias under length-prefixed encoding")
	}
	zero := base
	zero.SourceRevision = 0
	omitted := base
	omitted.SourceRevision = 0
	if zero.SourceEventKey() != omitted.SourceEventKey() {
		t.Fatal("SourceRevision zero must be stable")
	}
	if zero.SourceEventKey() == base.SourceEventKey() {
		t.Fatal("SourceRevision 0 vs 1 must stay distinct")
	}
}
