package metering

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	sdkmetering "github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

func TestProviderDebitReduction_IsolatedAndDeterministic(t *testing.T) {
	t.Parallel()
	first := phase11DebitForCore(t, "core-debit-1", "1.25")
	first.Sequence = 2
	second := phase11DebitForCore(t, "core-debit-2", "2.25")
	second.Sequence = 1
	otherRequest := phase11DebitForCore(t, "core-debit-other", "9")
	otherRequest.RequestID = "request-other"
	otherWindow := first.Clone()
	otherWindow.ID = "core-debit-window"
	otherWindow.SourceEventKey = "core-debit-window-source"
	otherWindow.PoolID = "pool-other"
	otherWindow.WindowID = "window-other"
	otherWindow.ResetAt = phase11Time(1_001)

	forward, err := ReduceProviderDebits([]sdkmetering.ProviderDebit{first, second, otherRequest, otherWindow})
	if err != nil {
		t.Fatalf("forward reduction: %v", err)
	}
	reverse, err := ReduceProviderDebits([]sdkmetering.ProviderDebit{otherWindow, otherRequest, second, first})
	if err != nil {
		t.Fatalf("reverse reduction: %v", err)
	}
	if len(forward.Measures) != 3 || len(reverse.Measures) != 3 {
		t.Fatalf("measures = %d/%d, want three isolated request/window scopes", len(forward.Measures), len(reverse.Measures))
	}
	if got := providerDebitMeasureValue(forward, "request-11"); got != "35/1" {
		t.Fatalf("request-11 quantity = %q, want 35/1 (3.5)", got)
	}
	if got := providerDebitMeasureValue(forward, "request-other"); got != "9/0" {
		t.Fatalf("request-other quantity = %q, want 9/0", got)
	}
	if got := providerDebitMeasureValueForPool(forward, "request-11", "pool-other"); got != "125/2" {
		t.Fatalf("pool-other quantity = %q, want 125/2", got)
	}
	if got, want := providerDebitSnapshotFingerprint(forward), providerDebitSnapshotFingerprint(reverse); got != want {
		t.Fatalf("reordered reduction fingerprint = %q, want %q", got, want)
	}
	for _, observation := range forward.Observations {
		if len(observation.Charges) != 0 {
			t.Fatalf("provider unit debit unexpectedly reduced as money: %+v", observation.Charges)
		}
	}
	for _, measure := range forward.Measures {
		if measure.Scope.Subject.Kind != sdkmetering.SubjectProviderDebit || measure.Scope.Subject.BLegID == "" || measure.Scope.Subject.BillingCallID == "" {
			t.Fatalf("reduced measure lost request/B-leg ownership: %+v", measure.Scope.Subject)
		}
	}
}

func TestProviderDebitReduction_ReplayConflictAndCorrectionsFailClosed(t *testing.T) {
	t.Parallel()
	base := phase11DebitForCore(t, "core-debit-revision", "5")
	duplicate := base.Clone()
	duplicate.ReceivedAt = phase11Time(9_999)
	if got, err := ReduceProviderDebits([]sdkmetering.ProviderDebit{base, duplicate}); err != nil {
		t.Fatalf("identical replay: %v", err)
	} else if got.Replayed != 1 {
		t.Fatalf("replayed = %d, want 1", got.Replayed)
	}

	conflict := base.Clone()
	conflict.Quantity = phase11DecimalPtr(t, "6")
	if _, err := ReduceProviderDebits([]sdkmetering.ProviderDebit{base, conflict}); !errors.Is(err, ErrProviderDebitIdentityConflict) {
		t.Fatalf("conflicting replay error = %v, want ErrProviderDebitIdentityConflict", err)
	}

	correction := base.Clone()
	correction.ID = "core-debit-correction"
	correction.SourceEventKey = "core-debit-correction-source"
	correction.Revision = 2
	correction.Sequence = 2
	correction.Semantics = sdkmetering.SemanticsCorrection
	correction.Quantity = phase11DecimalPtr(t, "-2")
	ref, err := base.Ref()
	if err != nil {
		t.Fatalf("base reference: %v", err)
	}
	correction.Supersedes = []sdkmetering.ProviderDebitRef{ref}
	corrected, err := ReduceProviderDebits([]sdkmetering.ProviderDebit{base, correction})
	if err != nil {
		t.Fatalf("resolved correction: %v", err)
	}
	if got := providerDebitMeasureValue(corrected, base.RequestID); got != "3/0" {
		t.Fatalf("corrected quantity = %q, want 3/0", got)
	}

	replacement := base.Clone()
	replacement.ID = "core-debit-replacement"
	replacement.SourceEventKey = "core-debit-replacement-source"
	replacement.Revision = 2
	replacement.Sequence = 2
	replacement.Semantics = sdkmetering.SemanticsReplacement
	replacement.Quantity = phase11DecimalPtr(t, "3")
	replacement.Supersedes = []sdkmetering.ProviderDebitRef{ref}
	replaced, err := ReduceProviderDebits([]sdkmetering.ProviderDebit{replacement, base})
	if err != nil {
		t.Fatalf("resolved replacement: %v", err)
	}
	if got := providerDebitMeasureValue(replaced, base.RequestID); got != "3/0" {
		t.Fatalf("replacement quantity = %q, want 3/0", got)
	}

	late := correction.Clone()
	late.Supersedes = []sdkmetering.ProviderDebitRef{{StoreID: base.StoreID, ObservationID: base.ID, Revision: base.Revision, PayloadHash: "missing"}}
	if _, err := ReduceProviderDebits([]sdkmetering.ProviderDebit{late}); !errors.Is(err, ErrProviderDebitIncomplete) {
		t.Fatalf("late correction error = %v, want ErrProviderDebitIncomplete", err)
	}
}

func TestProviderDebitReduction_UnsupportedAndPartialEvidenceRemainAbsent(t *testing.T) {
	t.Parallel()
	if _, err := ReduceProviderDebits(nil); !errors.Is(err, sdkmetering.ErrProviderDebitAbsent) {
		t.Fatalf("empty reduction error = %v, want ErrProviderDebitAbsent", err)
	}
	partial := phase11DebitForCore(t, "core-debit-partial", "1")
	partial.Quality = sdkmetering.QualityEstimated
	if _, err := ReduceProviderDebits([]sdkmetering.ProviderDebit{partial}); !errors.Is(err, ErrProviderDebitIncomplete) {
		t.Fatalf("estimated debit error = %v, want ErrProviderDebitIncomplete", err)
	}
	missing := phase11DebitForCore(t, "core-debit-missing", "1")
	missing.Quantity = nil
	if _, err := ReduceProviderDebits([]sdkmetering.ProviderDebit{missing}); !errors.Is(err, ErrProviderDebitIncomplete) {
		t.Fatalf("missing debit error = %v, want ErrProviderDebitIncomplete", err)
	}
}

func phase11DebitForCore(t *testing.T, id, value string) sdkmetering.ProviderDebit {
	t.Helper()
	quantity := phase11DecimalPtr(t, value)
	return sdkmetering.ProviderDebit{
		Version:            sdkmetering.ProviderDebitVersionV1,
		ID:                 id,
		SourceEventKey:     id + "-source",
		Revision:           1,
		StreamID:           "core-provider-debit-stream",
		Sequence:           1,
		StoreID:            "store-11",
		TenantID:           "tenant-11",
		ProviderAccountKey: "provider-account-11",
		PoolID:             "pool-11",
		WindowID:           "window-11",
		ResetAt:            phase11Time(1_000),
		RequestID:          "request-11",
		BillingCallID:      "billing-call-11",
		BLegID:             "b-leg-11",
		ALegID:             "a-leg-11",
		AttemptID:          "attempt-11",
		ProviderRequestID:  "provider-request-11",
		Component:          sdkmetering.ComponentKey{Direction: sdkmetering.DirectionNone, Component: sdkmetering.ComponentCredit, Unit: sdkmetering.UnitCredit},
		Quantity:           quantity,
		Quality:            sdkmetering.QualityObserved,
		MethodRef:          "provider-api-v1",
		Acquisition:        sdkmetering.AcquisitionProviderResponse,
		Authority:          sdkmetering.AuthorityObservedClaim,
		Semantics:          sdkmetering.SemanticsDelta,
		ObservedAt:         phase11Time(2_000),
		ReceivedAt:         phase11Time(2_001),
	}
}

func phase11DecimalPtr(t *testing.T, raw string) *sdkmetering.Decimal {
	t.Helper()
	value, err := sdkmetering.ParseDecimal(raw)
	if err != nil {
		t.Fatalf("parse decimal %q: %v", raw, err)
	}
	return &value
}

func phase11Time(seconds int64) (out time.Time) {
	return time.Unix(seconds, 0).UTC()
}

func providerDebitMeasureValue(snapshot ProviderDebitSnapshot, requestID string) string {
	for _, measure := range snapshot.Measures {
		if measure.Scope.Subject.RequestID == requestID {
			return measure.Value.CanonicalString()
		}
	}
	return ""
}

func providerDebitMeasureValueForPool(snapshot ProviderDebitSnapshot, requestID, poolID string) string {
	for _, measure := range snapshot.Measures {
		if measure.Scope.Subject.RequestID == requestID && measure.Scope.Subject.PoolID == poolID {
			return measure.Value.CanonicalString()
		}
	}
	return ""
}

func providerDebitSnapshotFingerprint(snapshot ProviderDebitSnapshot) string {
	parts := make([]string, 0, len(snapshot.Measures))
	for _, measure := range snapshot.Measures {
		parts = append(parts, measure.Scope.Key()+"="+measure.Value.CanonicalString())
	}
	slices.Sort(parts)
	return strings.Join(parts, "\n")
}
