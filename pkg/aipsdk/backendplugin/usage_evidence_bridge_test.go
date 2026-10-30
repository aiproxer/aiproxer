package backendplugin_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

func TestUsageEvidenceBuffer_BridgesProviderUsageWithPresence(t *testing.T) {
	t.Parallel()
	in, out, cache := int64(0), int64(7), int64(3)
	buffer := backendplugin.NewUsageEvidenceBuffer()
	buffer.AddUsageEvent(aipapi.Event{
		Kind:            aipapi.EventUsageDelta,
		InputTokens:     int(in),
		OutputTokens:    int(out),
		CacheReadTokens: int(cache),
		UsagePresence:   aipapi.UsagePresence{InputTokens: true, OutputTokens: true, CacheReadTokens: true},
		Accounting: aipapi.UsageAccountingMetadata{
			Plane: aipapi.UsagePlaneProviderBillable, Source: aipapi.UsageSourceProviderReported,
			Authority: aipapi.UsageAuthorityAuthoritative, DedupeKey: "provider:req-1",
		},
	}, "provider:stream")

	got := buffer.DrainAccountingEvidence()
	if len(got) != 1 {
		t.Fatalf("bridged evidence count=%d, want 1", len(got))
	}
	if got[0].InputTokens == nil || *got[0].InputTokens != in || got[0].OutputTokens == nil || *got[0].OutputTokens != out || got[0].CacheReadTokens == nil || *got[0].CacheReadTokens != cache {
		t.Fatalf("bridged counters=%+v", got[0])
	}
	if got[0].Presence != (aipapi.UsagePresence{InputTokens: true, OutputTokens: true, CacheReadTokens: true}) {
		t.Fatalf("presence=%+v", got[0].Presence)
	}
	if got[0].DedupeKey != "provider:req-1" || got[0].Source != backendplugin.AccountingSourceProviderReported {
		t.Fatalf("provider lineage=%+v", got[0])
	}
}

func TestUsageEvidenceBuffer_DeduplicatesExactReplayAndRejectsUnsafeUsage(t *testing.T) {
	t.Parallel()
	value := 4
	base := aipapi.Event{
		Kind: aipapi.EventUsageDelta, InputTokens: value,
		UsagePresence: aipapi.UsagePresence{InputTokens: true},
		Accounting: aipapi.UsageAccountingMetadata{
			Plane: aipapi.UsagePlaneProviderBillable, Source: aipapi.UsageSourceProviderReported,
			Authority: aipapi.UsageAuthorityAuthoritative, DedupeKey: "provider:req-2",
		},
	}
	buffer := backendplugin.NewUsageEvidenceBuffer()
	buffer.AddUsageEvent(base, "fallback")
	buffer.AddUsageEvent(base, "fallback")
	if got := buffer.DrainAccountingEvidence(); len(got) != 1 {
		t.Fatalf("exact replay count=%d, want 1", len(got))
	}

	unsafe := base
	unsafe.Accounting.Plane = aipapi.UsagePlaneClientVisible
	buffer.AddUsageEvent(unsafe, "fallback")
	if got := buffer.DrainAccountingEvidence(); len(got) != 0 {
		t.Fatalf("client-visible usage was bridged: %+v", got)
	}

	missingPresence := base
	missingPresence.UsagePresence = aipapi.UsagePresence{}
	buffer.AddUsageEvent(missingPresence, "fallback-2")
	if got := buffer.DrainAccountingEvidence(); len(got) != 0 {
		t.Fatalf("usage without explicit presence was bridged: %+v", got)
	}
}

func TestUsageEvidenceBuffer_PreservesChangedSourceAsSeparateV1Evidence(t *testing.T) {
	t.Parallel()
	first, second := 1, 2
	base := aipapi.Event{
		Kind: aipapi.EventUsageDelta, InputTokens: first,
		UsagePresence: aipapi.UsagePresence{InputTokens: true},
		Accounting: aipapi.UsageAccountingMetadata{
			Plane: aipapi.UsagePlaneProviderBillable, Source: aipapi.UsageSourceProviderReported,
			Authority: aipapi.UsageAuthorityAuthoritative, DedupeKey: "provider:req-3",
		},
	}
	changed := base
	changed.InputTokens = second
	buffer := backendplugin.NewUsageEvidenceBuffer()
	buffer.AddUsageEvent(base, "fallback")
	buffer.AddUsageEvent(changed, "fallback")
	if got := buffer.DrainAccountingEvidence(); len(got) != 2 {
		t.Fatalf("changed source count=%d, want 2 for host-side V1 reconciliation", len(got))
	}
}

func TestUsageEvidenceBufferRetainsABACorrectionAcrossDrains(t *testing.T) {
	t.Parallel()
	base := aipapi.Event{
		Kind:          aipapi.EventUsageDelta,
		UsagePresence: aipapi.UsagePresence{InputTokens: true},
		Accounting: aipapi.UsageAccountingMetadata{
			Plane: aipapi.UsagePlaneProviderBillable, Source: aipapi.UsageSourceProviderReported,
			Authority: aipapi.UsageAuthorityAuthoritative, DedupeKey: "provider:aba",
		},
	}
	buffer := backendplugin.NewUsageEvidenceBuffer()
	for _, value := range []int{1, 2, 1} {
		event := base
		event.InputTokens = value
		buffer.AddUsageEvent(event, "fallback")
		got := buffer.DrainAccountingEvidence()
		if len(got) != 1 {
			t.Fatalf("input=%d evidence=%d, want one correction", value, len(got))
		}
	}
	last := base
	last.InputTokens = 1
	buffer.AddUsageEvent(last, "fallback")
	if got := buffer.DrainAccountingEvidence(); len(got) != 0 {
		t.Fatalf("consecutive A replay evidence=%d, want 0", len(got))
	}
}

func TestUsageEvidenceBuffer_PreservesCacheReadExclusionAcrossBridge(t *testing.T) {
	t.Parallel()
	buffer := backendplugin.NewUsageEvidenceBuffer()
	buffer.AddUsageEvent(aipapi.Event{
		Kind:            aipapi.EventUsageDelta,
		InputTokens:     300,
		CacheReadTokens: 600,
		UsagePresence:   aipapi.UsagePresence{InputTokens: true, CacheReadTokens: true},
		Accounting: aipapi.UsageAccountingMetadata{
			Plane: aipapi.UsagePlaneProviderBillable, Source: aipapi.UsageSourceProviderReported,
			Authority: aipapi.UsageAuthorityAuthoritative, DedupeKey: "provider:req-cache-read",
			InputTokensExcludeCacheRead: true,
		},
	}, "fallback")
	got := buffer.DrainAccountingEvidence()
	if len(got) != 1 {
		t.Fatalf("bridged evidence count=%d, want 1", len(got))
	}
	if !got[0].InputTokensExcludeCacheRead {
		t.Fatalf("cache-read exclusion lost across V1 bridge: %+v", got[0])
	}
	if got[0].InputTokens == nil || *got[0].InputTokens != 300 || got[0].CacheReadTokens == nil || *got[0].CacheReadTokens != 600 {
		t.Fatalf("bridged counters changed: %+v", got[0])
	}
}

func TestUsageEvidenceBuffer_EmitsCorrectionWhenExclusionFlips(t *testing.T) {
	t.Parallel()
	base := aipapi.Event{
		Kind:            aipapi.EventUsageDelta,
		InputTokens:     300,
		CacheReadTokens: 600,
		UsagePresence:   aipapi.UsagePresence{InputTokens: true, CacheReadTokens: true},
		Accounting: aipapi.UsageAccountingMetadata{
			Plane: aipapi.UsagePlaneProviderBillable, Source: aipapi.UsageSourceProviderReported,
			Authority: aipapi.UsageAuthorityAuthoritative, DedupeKey: "provider:req-flip",
			InputTokensExcludeCacheRead: true,
		},
	}
	buffer := backendplugin.NewUsageEvidenceBuffer()
	buffer.AddUsageEvent(base, "fallback")
	flipped := base
	flipped.Accounting.InputTokensExcludeCacheRead = false
	buffer.AddUsageEvent(flipped, "fallback")
	got := buffer.DrainAccountingEvidence()
	if len(got) != 2 {
		t.Fatalf("exclusion flip evidence=%d, want 2 (changed semantics are a correction, not a replay)", len(got))
	}
	if !got[0].InputTokensExcludeCacheRead || got[1].InputTokensExcludeCacheRead {
		t.Fatalf("exclusion sequence lost: %+v", got)
	}
}

// The V1 sideband carries both input relations independently, next to the
// provider's own counters.
func TestUsageEvidenceBuffer_PreservesIndependentCacheExclusions(t *testing.T) {
	t.Parallel()
	exclusions := []struct {
		read  bool
		write bool
	}{
		{read: false, write: false},
		{read: true, write: false},
		{read: false, write: true},
		{read: true, write: true},
	}
	for _, excluded := range exclusions {
		buffer := backendplugin.NewUsageEvidenceBuffer()
		buffer.AddUsageEvent(aipapi.Event{
			Kind:             aipapi.EventUsageDelta,
			InputTokens:      300,
			OutputTokens:     50,
			CacheReadTokens:  600,
			CacheWriteTokens: 100,
			UsagePresence:    aipapi.UsagePresence{InputTokens: true, OutputTokens: true, CacheReadTokens: true, CacheWriteTokens: true},
			Accounting: aipapi.UsageAccountingMetadata{
				Plane: aipapi.UsagePlaneProviderBillable, Source: aipapi.UsageSourceProviderReported,
				Authority: aipapi.UsageAuthorityAuthoritative, DedupeKey: "provider:req-cache-write",
				InputTokensExcludeCacheRead:  excluded.read,
				InputTokensExcludeCacheWrite: excluded.write,
			},
		}, "fallback")
		got := buffer.DrainAccountingEvidence()
		if len(got) != 1 {
			t.Fatalf("read=%v write=%v bridged evidence count=%d, want 1", excluded.read, excluded.write, len(got))
		}
		if got[0].InputTokensExcludeCacheRead != excluded.read || got[0].InputTokensExcludeCacheWrite != excluded.write {
			t.Fatalf("read=%v write=%v bridged exclusions lost: %+v", excluded.read, excluded.write, got[0])
		}
		if got[0].InputTokens == nil || *got[0].InputTokens != 300 || got[0].OutputTokens == nil || *got[0].OutputTokens != 50 ||
			got[0].CacheReadTokens == nil || *got[0].CacheReadTokens != 600 || got[0].CacheWriteTokens == nil || *got[0].CacheWriteTokens != 100 {
			t.Fatalf("read=%v write=%v bridged counters changed: %+v", excluded.read, excluded.write, got[0])
		}
	}
}

// A write-only declaration change is a semantic correction, not a replay: the
// existing sideband fingerprint must separate it from the earlier payload
// while an exact replay of the same payload stays deduplicated.
func TestUsageEvidenceBuffer_EmitsCorrectionWhenCacheWriteExclusionFlips(t *testing.T) {
	t.Parallel()
	base := aipapi.Event{
		Kind:            aipapi.EventUsageDelta,
		InputTokens:     300,
		CacheReadTokens: 600,
		UsagePresence:   aipapi.UsagePresence{InputTokens: true, CacheReadTokens: true},
		Accounting: aipapi.UsageAccountingMetadata{
			Plane: aipapi.UsagePlaneProviderBillable, Source: aipapi.UsageSourceProviderReported,
			Authority: aipapi.UsageAuthorityAuthoritative, DedupeKey: "provider:req-write-flip",
			InputTokensExcludeCacheRead: true,
		},
	}
	flipped := base
	flipped.Accounting.InputTokensExcludeCacheWrite = true
	buffer := backendplugin.NewUsageEvidenceBuffer()
	buffer.AddUsageEvent(base, "fallback")
	buffer.AddUsageEvent(base, "fallback")
	buffer.AddUsageEvent(flipped, "fallback")
	got := buffer.DrainAccountingEvidence()
	if len(got) != 2 {
		t.Fatalf("write exclusion flip evidence=%d, want 2 (exact replay is deduplicated, a changed declaration is a correction)", len(got))
	}
	if got[0].InputTokensExcludeCacheWrite || !got[1].InputTokensExcludeCacheWrite {
		t.Fatalf("write exclusion sequence lost: %+v", got)
	}
	if !got[0].InputTokensExcludeCacheRead || !got[1].InputTokensExcludeCacheRead {
		t.Fatalf("read exclusion sequence lost: %+v", got)
	}
}
