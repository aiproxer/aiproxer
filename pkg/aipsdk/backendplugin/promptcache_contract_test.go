package backendplugin_test

import (
	"errors"
	"testing"
	"time"

	backendpluginv1 "github.com/aiproxer/aiproxer/api/backendplugin/v1"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/promptcache"
)

func TestPromptCacheABI_NegotiationAndObservationRoundTrip(t *testing.T) {
	t.Parallel()
	neg, err := backendplugin.Negotiate(
		backendplugin.ProtocolOffer{Major: 1, Minor: backendplugin.ProtocolMinorPromptCacheResidency, DisableTransportRetries: true, Features: []backendplugin.Feature{{Name: backendplugin.FeaturePromptCacheResidency}}},
		backendplugin.ProtocolOffer{Major: 1, Minor: backendplugin.ProtocolMinorPromptCacheResidency, DisableTransportRetries: true, Features: []backendplugin.Feature{{Name: backendplugin.FeaturePromptCacheResidency}}},
	)
	if err != nil || !backendplugin.PromptCacheNegotiated(neg) {
		t.Fatalf("neg=%+v err=%v", neg, err)
	}
	now := time.Unix(10, 0).UTC()
	observation := promptcache.Observation{ALegID: "a", BLegID: "b", BackendInstanceID: "instance", TargetID: "target", GenerationID: "generation", Lifecycle: promptcache.LifecycleSlidingExpiry, Timing: promptcache.Timing{ObservedAt: now}, Renewable: true, Handle: promptcache.Handle("opaque")}
	wire, err := backendplugin.PromptCacheObservationToProto(&observation)
	if err != nil {
		t.Fatal(err)
	}
	back, err := backendplugin.PromptCacheObservationFromProto(wire)
	if err != nil {
		t.Fatal(err)
	}
	if back.TargetID != observation.TargetID || string(back.Handle) != string(observation.Handle) || !back.Timing.ObservedAt.Equal(now) {
		t.Fatalf("back=%+v", back)
	}
	frame := backendplugin.ServerFrame{Kind: backendplugin.ServerFramePromptCacheObservation, Sequence: 1, PromptCacheObservation: back}
	if err := frame.ValidateShape(); err != nil {
		t.Fatal(err)
	}
	if _, err := backendplugin.ServerFrameToProto(frame); err != nil {
		t.Fatal(err)
	}
}

func TestPromptCacheABI_LegacyPeerDowngradesWithoutFeature(t *testing.T) {
	t.Parallel()
	neg, err := backendplugin.Negotiate(
		backendplugin.ProtocolOffer{Major: 1, Minor: backendplugin.ProtocolMinorPromptCacheResidency, DisableTransportRetries: true, Features: []backendplugin.Feature{{Name: backendplugin.FeaturePromptCacheResidency}}},
		backendplugin.ProtocolOffer{Major: 1, Minor: backendplugin.ProtocolMinorAccountingEvidence, DisableTransportRetries: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	if backendplugin.PromptCacheNegotiated(neg) {
		t.Fatal("legacy peer enabled prompt-cache feature")
	}
	if neg.NegotiatedMinor != backendplugin.ProtocolMinorAccountingEvidence {
		t.Fatalf("minor=%d", neg.NegotiatedMinor)
	}
}

func TestPromptCacheABI_RejectsMalformedRenewableObservation(t *testing.T) {
	t.Parallel()
	observation := promptcache.Observation{ALegID: "a", BLegID: "b", BackendInstanceID: "instance", TargetID: "target", GenerationID: "generation", Lifecycle: promptcache.LifecycleBestEffort, Timing: promptcache.Timing{ObservedAt: time.Unix(10, 0)}, Renewable: true}
	if !errors.Is(observation.Validate(), promptcache.ErrHandleRequired) {
		t.Fatalf("err=%v", observation.Validate())
	}
	frame := backendplugin.ServerFrame{Kind: backendplugin.ServerFramePromptCacheObservation, Sequence: 1}
	if !errors.Is(frame.ValidateShape(), backendplugin.ErrInvalidFrame) {
		t.Fatalf("err=%v", frame.ValidateShape())
	}
}

func TestPromptCacheABI_RenewalPreservesSeparateAccounting(t *testing.T) {
	t.Parallel()
	input := int64(7)
	accounting := &backendplugin.AccountingEvidence{
		InputTokens: &input,
		Presence:    aipapi.UsagePresence{InputTokens: true},
		Source:      backendplugin.AccountingSourceProviderReported,
		Authority:   backendplugin.AccountingAuthorityAuthoritative,
		Plane:       backendplugin.AccountingPlaneProviderBillable,
		DedupeKey:   "prompt-cache:op-1",
	}
	wire, err := backendplugin.PromptCacheRenewResultToProto(promptcache.RenewResult{Status: promptcache.StillResident}, accounting)
	if err != nil {
		t.Fatal(err)
	}
	_, got, err := backendplugin.PromptCacheRenewResultFromProto(wire)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.DedupeKey != accounting.DedupeKey || got.InputTokens == nil || *got.InputTokens != *accounting.InputTokens {
		t.Fatalf("accounting=%+v", got)
	}
}

func TestPromptCacheABI_RejectsMalformedRenewalAccounting(t *testing.T) {
	t.Parallel()
	negative := int64(-1)
	_, _, err := backendplugin.PromptCacheRenewResultFromProto(&backendpluginv1.RenewPromptCacheResponse{
		Status: backendpluginv1.PromptCacheRenewStatus_PROMPT_CACHE_RENEW_STATUS_STILL_RESIDENT,
		Accounting: &backendpluginv1.AccountingEvidence{
			InputTokens: &negative,
			Presence:    &backendpluginv1.UsagePresence{InputTokens: true},
			Source:      backendpluginv1.AccountingSource_ACCOUNTING_SOURCE_PROVIDER_REPORTED,
			Authority:   backendpluginv1.AccountingAuthority_ACCOUNTING_AUTHORITY_AUTHORITATIVE,
			Plane:       backendpluginv1.AccountingPlane_ACCOUNTING_PLANE_PROVIDER_BILLABLE,
			DedupeKey:   "prompt-cache:op-1",
		},
	})
	if err == nil {
		t.Fatal("malformed renewal accounting was accepted")
	}
}
