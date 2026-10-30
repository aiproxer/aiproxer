package promptcache

import (
	"errors"
	"testing"
	"time"
)

func TestProfile_NormalizeAndRejectsUnsafeRenewal(t *testing.T) {
	t.Parallel()
	p, err := (Profile{ObservationSupported: true, RenewalSupported: true, LifecycleKinds: []LifecycleKind{LifecycleUnknown, LifecycleSlidingExpiry}}).Normalize()
	if err != nil {
		t.Fatal(err)
	}
	if p.LifecycleKinds[0] != LifecycleSlidingExpiry || p.LifecycleKinds[1] != LifecycleUnknown {
		t.Fatalf("normalized=%v", p.LifecycleKinds)
	}
	if err := (Profile{RenewalSupported: true}).Validate(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("err=%v", err)
	}
	if err := (Profile{LifecycleKinds: []LifecycleKind{"vendor_ttl"}}).Validate(); !errors.Is(err, ErrUnknownLifecycle) {
		t.Fatalf("err=%v", err)
	}
}

func validObservation(renewable bool) Observation {
	now := time.Unix(100, 0).UTC()
	o := Observation{ALegID: "a", BLegID: "b", BackendInstanceID: "instance", TargetID: "target", GenerationID: "generation", Lifecycle: LifecycleBestEffort, Timing: Timing{ObservedAt: now}, Renewable: renewable}
	if renewable {
		o.Handle = Handle("opaque")
	}
	return o
}

func TestObservation_RequiresBoundedOpaqueHandleAndPreservesUnknownTiming(t *testing.T) {
	t.Parallel()
	if err := validObservation(false).Validate(); err != nil {
		t.Fatal(err)
	}
	if err := validObservation(true).Validate(); err != nil {
		t.Fatal(err)
	}
	bad := validObservation(true)
	bad.Handle = nil
	if !errors.Is(bad.Validate(), ErrHandleRequired) {
		t.Fatalf("err=%v", bad.Validate())
	}
	bad = validObservation(false)
	bad.Timing.ExpiresAt = new(time.Unix(99, 0))
	if !errors.Is(bad.Validate(), ErrInvalid) {
		t.Fatalf("err=%v", bad.Validate())
	}
	bad = validObservation(true)
	bad.TargetID = TargetID(string(make([]byte, MaxTargetIDBytes+1)))
	if !errors.Is(bad.Validate(), ErrOversized) {
		t.Fatalf("err=%v", bad.Validate())
	}
}

func TestCacheEvidence_RejectsNegativeAndAllowsExplicitZero(t *testing.T) {
	t.Parallel()
	zero := int64(0)
	if err := (CacheEvidence{TotalTokens: &zero}).Validate(); err != nil {
		t.Fatal(err)
	}
	negative := int64(-1)
	if !errors.Is((CacheEvidence{TotalTokens: &negative}).Validate(), ErrInvalid) {
		t.Fatal("negative evidence accepted")
	}
}

func TestObservationBuffer_CommitsOnceAndDiscardsFailedAttempt(t *testing.T) {
	t.Parallel()
	var b ObservationBuffer
	if err := b.Add(validObservation(true)); err != nil {
		t.Fatal(err)
	}
	if got := b.DrainPromptCacheObservations(); got != nil {
		t.Fatalf("uncommitted observations published: %#v", got)
	}
	b.Commit()
	got := b.DrainPromptCacheObservations()
	if len(got) != 1 || got[0].TargetID != "target" {
		t.Fatalf("got=%#v", got)
	}
	if got := b.DrainPromptCacheObservations(); got != nil {
		t.Fatalf("second drain=%#v", got)
	}
	var failed ObservationBuffer
	if err := failed.Add(validObservation(true)); err != nil {
		t.Fatal(err)
	}
	failed.Discard()
	if got := failed.DrainPromptCacheObservations(); got != nil {
		t.Fatalf("discarded=%#v", got)
	}
}

func timePtr(t time.Time) *time.Time { return new(t) }
