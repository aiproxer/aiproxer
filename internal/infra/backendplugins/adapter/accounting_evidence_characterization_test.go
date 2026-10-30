package adapter

import (
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

func TestAccountingEvidenceToEventPreservesPresenceAndAuthoritativeZero(t *testing.T) {
	t.Parallel()
	input, output, cacheRead, cacheWrite, reasoning, total := int64(4), int64(0), int64(2), int64(0), int64(1), int64(7)
	ev, err := accountingEvidenceToEvent(&backendplugin.AccountingEvidence{
		InputTokens:      &input,
		OutputTokens:     &output,
		CacheReadTokens:  &cacheRead,
		CacheWriteTokens: &cacheWrite,
		ReasoningTokens:  &reasoning,
		TotalTokens:      &total,
		Presence: backendplugin.UsagePresence{
			InputTokens: true, OutputTokens: true, CacheReadTokens: true,
			CacheWriteTokens: true, ReasoningTokens: true, TotalTokens: true,
		},
		Source:    backendplugin.AccountingSourceProviderReported,
		Authority: backendplugin.AccountingAuthorityAuthoritative,
		Plane:     backendplugin.AccountingPlaneProviderBillable,
		DedupeKey: "provider-charge-1",
	})
	if err != nil {
		t.Fatalf("accountingEvidenceToEvent: %v", err)
	}
	if ev.Kind != aipapi.EventUsageDelta || ev.Accounting.Plane != aipapi.UsagePlaneProviderBillable {
		t.Fatalf("event classification = %+v", ev)
	}
	if ev.Accounting.DedupeKey != "provider-charge-1" || ev.Accounting.Source != aipapi.UsageSourceProviderReported || ev.Accounting.Authority != aipapi.UsageAuthorityAuthoritative {
		t.Fatalf("event provenance = %+v", ev.Accounting)
	}
	if ev.InputTokens != 4 || ev.OutputTokens != 0 || ev.CacheReadTokens != 2 || ev.CacheWriteTokens != 0 || ev.ReasoningTokens != 1 || ev.TotalTokens != 7 {
		t.Fatalf("event quantities = %+v", ev)
	}
	if !ev.UsagePresence.InputTokens || !ev.UsagePresence.OutputTokens || !ev.UsagePresence.CacheReadTokens || !ev.UsagePresence.CacheWriteTokens || !ev.UsagePresence.ReasoningTokens || !ev.UsagePresence.TotalTokens {
		t.Fatalf("event presence = %+v", ev.UsagePresence)
	}
}

func TestAccountingEvidenceToEventRejectsAbsentPresenceMismatch(t *testing.T) {
	t.Parallel()
	zero := int64(0)
	_, err := accountingEvidenceToEvent(&backendplugin.AccountingEvidence{
		OutputTokens: &zero,
		Presence:     backendplugin.UsagePresence{},
		Source:       backendplugin.AccountingSourceProviderReported,
		Authority:    backendplugin.AccountingAuthorityAuthoritative,
		Plane:        backendplugin.AccountingPlaneProviderBillable,
		DedupeKey:    "provider-charge-2",
	})
	if err == nil {
		t.Fatal("expected presence mismatch to be rejected")
	}
}

func TestFinalizeBillingResponseToEventPreservesPresenceAndZeroSemantics(t *testing.T) {
	t.Parallel()
	zero := int64(0)
	input := int64(4)
	ev, err := finalizeBillingResponseToEvent(backendplugin.FinalizeBillingResponse{
		Usage: backendplugin.UsageEvidence{
			InputTokens:  &input,
			OutputTokens: &zero,
			Presence:     backendplugin.UsagePresence{InputTokens: true, OutputTokens: true},
		},
		EvidenceQuality: "provider",
	}, "finalize-key")
	if err != nil {
		t.Fatalf("finalizeBillingResponseToEvent: %v", err)
	}
	if ev.InputTokens != 4 || ev.OutputTokens != 0 {
		t.Fatalf("quantities = %+v", ev)
	}
	if !ev.UsagePresence.InputTokens || !ev.UsagePresence.OutputTokens {
		t.Fatalf("presence = %+v", ev.UsagePresence)
	}
	if ev.Accounting.Source != aipapi.UsageSourceProviderReported || ev.Accounting.Authority != aipapi.UsageAuthorityAuthoritative {
		t.Fatalf("provenance = %+v", ev.Accounting)
	}
	if ev.Accounting.DedupeKey != "finalize-key" {
		t.Fatalf("dedupe = %q", ev.Accounting.DedupeKey)
	}
}

func TestFinalizeBillingResponseToEventRejectsPresentNilPointer(t *testing.T) {
	t.Parallel()
	_, err := finalizeBillingResponseToEvent(backendplugin.FinalizeBillingResponse{
		Usage: backendplugin.UsageEvidence{
			Presence: backendplugin.UsagePresence{InputTokens: true},
		},
		EvidenceQuality: "provider",
	}, "finalize-key")
	if err == nil {
		t.Fatal("expected Present=true with nil pointer to be rejected")
	}
}

func TestFinalizeBillingResponseToEventDoesNotInventAuthoritativeWhenQualityUnknown(t *testing.T) {
	t.Parallel()
	input := int64(1)
	ev, err := finalizeBillingResponseToEvent(backendplugin.FinalizeBillingResponse{
		Usage: backendplugin.UsageEvidence{
			InputTokens: &input,
			Presence:    backendplugin.UsagePresence{InputTokens: true},
		},
	}, "finalize-key")
	if err != nil {
		t.Fatalf("finalizeBillingResponseToEvent: %v", err)
	}
	if ev.Accounting.Source != aipapi.UsageSourceUnknown || ev.Accounting.Authority != aipapi.UsageAuthorityUnknown {
		t.Fatalf("unknown quality must not invent authoritative provenance: %+v", ev.Accounting)
	}
}

func TestFinalizeBillingResponseToEventPreservesAbsentCounters(t *testing.T) {
	t.Parallel()
	input := int64(2)
	ev, err := finalizeBillingResponseToEvent(backendplugin.FinalizeBillingResponse{
		Usage: backendplugin.UsageEvidence{
			InputTokens: &input,
			Presence:    backendplugin.UsagePresence{InputTokens: true},
		},
		EvidenceQuality: "estimated",
	}, "finalize-key")
	if err != nil {
		t.Fatalf("finalizeBillingResponseToEvent: %v", err)
	}
	if ev.UsagePresence.OutputTokens || ev.OutputTokens != 0 {
		t.Fatalf("absent output must remain absent, got %+v", ev)
	}
	if ev.Accounting.Authority != aipapi.UsageAuthorityEstimated {
		t.Fatalf("estimated quality authority = %q", ev.Accounting.Authority)
	}
}

func TestAccountingEvidenceToEventPreservesCacheReadExclusion(t *testing.T) {
	t.Parallel()
	input, cache := int64(300), int64(600)
	ev, err := accountingEvidenceToEvent(&backendplugin.AccountingEvidence{
		InputTokens:                 &input,
		CacheReadTokens:             &cache,
		Presence:                    backendplugin.UsagePresence{InputTokens: true, CacheReadTokens: true},
		Source:                      backendplugin.AccountingSourceProviderReported,
		Authority:                   backendplugin.AccountingAuthorityAuthoritative,
		Plane:                       backendplugin.AccountingPlaneProviderBillable,
		DedupeKey:                   "provider-charge-excl",
		InputTokensExcludeCacheRead: true,
	})
	if err != nil {
		t.Fatalf("accountingEvidenceToEvent: %v", err)
	}
	if !ev.Accounting.InputTokensExcludeCacheRead {
		t.Fatalf("cache-read exclusion lost on host reconstruction: %+v", ev.Accounting)
	}
	if ev.InputTokens != 300 || ev.CacheReadTokens != 600 {
		t.Fatalf("event quantities changed: %+v", ev)
	}

	legacy, err := accountingEvidenceToEvent(&backendplugin.AccountingEvidence{
		InputTokens:     &input,
		CacheReadTokens: &cache,
		Presence:        backendplugin.UsagePresence{InputTokens: true, CacheReadTokens: true},
		Source:          backendplugin.AccountingSourceProviderReported,
		Authority:       backendplugin.AccountingAuthorityAuthoritative,
		Plane:           backendplugin.AccountingPlaneProviderBillable,
		DedupeKey:       "provider-charge-legacy",
	})
	if err != nil {
		t.Fatalf("accountingEvidenceToEvent: %v", err)
	}
	if legacy.Accounting.InputTokensExcludeCacheRead {
		t.Fatalf("unset exclusion must retain legacy inclusive interpretation: %+v", legacy.Accounting)
	}
}
