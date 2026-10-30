package metering_test

import (
	"errors"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

func TestPhase35_ValidateQuery_AcceptsSourceAuthorityIdentityVersionWithSelectiveBound(t *testing.T) {
	t.Parallel()
	q := metering.Query{
		StreamID:        "customer-request:r1",
		Source:          metering.SourceObserved,
		Authority:       metering.AuthorityAuthoritative,
		IdentityVersion: metering.IdentityVersionV1,
		Limit:           10,
	}
	if err := metering.ValidateQuery(q); err != nil {
		t.Fatalf("ValidateQuery: %v", err)
	}
	if metering.HasSelectiveBound(metering.Query{Source: metering.SourceObserved}) {
		t.Fatal("source alone must not be a selective bound")
	}
	if err := metering.ValidateQuery(metering.Query{Source: metering.SourceObserved}); err == nil {
		t.Fatal("source-only query must be too broad")
	}
}

func TestPhase35_ValidateQuery_RejectsLimitAboveMax(t *testing.T) {
	t.Parallel()
	err := metering.ValidateQuery(metering.Query{
		StreamID: "s1",
		Limit:    metering.MaxQueryLimit + 1,
	})
	if err == nil {
		t.Fatal("expected ErrQueryLimitExceeded")
	}
	if !errors.Is(err, metering.ErrQueryLimitExceeded) {
		t.Fatalf("got %v want ErrQueryLimitExceeded", err)
	}
	if err := metering.ValidateQuery(metering.Query{StreamID: "s1", Limit: metering.MaxQueryLimit}); err != nil {
		t.Fatalf("max limit must be accepted: %v", err)
	}
}

func TestPhase35_FactMatchesQuery_FiltersSourceAuthorityIdentityVersion(t *testing.T) {
	t.Parallel()
	base := metering.Fact{
		FactID:          "f1",
		StreamID:        "s1",
		Sequence:        1,
		IdentityVersion: 0,
		Kind:            metering.FactKindCumulative,
		Perspective:     metering.PerspectiveCustomer,
		Boundary:        metering.BoundaryFrontendIngress,
		Lifecycle:       metering.LifecycleLogicalRequest,
		Source:          metering.SourceObserved,
		Authority:       metering.AuthorityAuthoritative,
		Presence:        metering.PresencePresent,
		RecordedAt:      time.Unix(1, 0).UTC(),
	}
	if !metering.FactMatchesQuery(base, metering.Query{
		StreamID:        "s1",
		Source:          metering.SourceObserved,
		Authority:       metering.AuthorityAuthoritative,
		IdentityVersion: metering.IdentityVersionV1,
	}) {
		t.Fatal("want match for EffectiveV1 + source/authority")
	}
	if metering.FactMatchesQuery(base, metering.Query{StreamID: "s1", Source: metering.SourceDerived}) {
		t.Fatal("source mismatch must not match")
	}
	if metering.FactMatchesQuery(base, metering.Query{StreamID: "s1", Authority: metering.AuthorityEstimated}) {
		t.Fatal("authority mismatch must not match")
	}
	if metering.FactMatchesQuery(base, metering.Query{StreamID: "s1", IdentityVersion: 2}) {
		t.Fatal("identity_version=2 must not match EffectiveV1")
	}
}
