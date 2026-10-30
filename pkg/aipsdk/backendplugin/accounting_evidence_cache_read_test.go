package backendplugin

import (
	"testing"

	"google.golang.org/protobuf/proto"

	backendpluginv1 "github.com/aiproxer/aiproxer/api/backendplugin/v1"
)

// TestAccountingEvidenceProtoRoundTripPreservesCacheReadExclusion proves the
// exclusion flag survives the executable connector wire ABI in both states.
// The zero value retains the legacy inclusive-input interpretation.
func TestAccountingEvidenceProtoRoundTripPreservesCacheReadExclusion(t *testing.T) {
	t.Parallel()
	input, cache := int64(300), int64(600)
	for _, excluded := range []bool{true, false} {
		evidence := AccountingEvidence{
			InputTokens:                 &input,
			CacheReadTokens:             &cache,
			Presence:                    UsagePresence{InputTokens: true, CacheReadTokens: true},
			Source:                      AccountingSourceProviderReported,
			Authority:                   AccountingAuthorityAuthoritative,
			Plane:                       AccountingPlaneProviderBillable,
			DedupeKey:                   "provider:req-cache-read",
			InputTokensExcludeCacheRead: excluded,
		}
		wire, err := accountingEvidenceToProto(&evidence)
		if err != nil {
			t.Fatalf("excluded=%v to proto: %v", excluded, err)
		}
		if wire.GetInputTokensExcludeCacheRead() != excluded {
			t.Fatalf("excluded=%v wire field=%v", excluded, wire.GetInputTokensExcludeCacheRead())
		}
		got, err := accountingEvidenceFromProto(wire)
		if err != nil {
			t.Fatalf("excluded=%v from proto: %v", excluded, err)
		}
		if got.InputTokensExcludeCacheRead != excluded {
			t.Fatalf("excluded=%v round-trip=%v", excluded, got.InputTokensExcludeCacheRead)
		}
		if got.InputTokens == nil || *got.InputTokens != input || got.CacheReadTokens == nil || *got.CacheReadTokens != cache {
			t.Fatalf("excluded=%v counters changed: %+v", excluded, got)
		}
	}
}

// The host-only sideband carries the two input relations independently, next
// to unchanged native quantities, unchanged accounting identity and truthful
// total presence.
func TestAccountingEvidenceProtoRoundTripPreservesIndependentCacheExclusions(t *testing.T) {
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
	totals := []struct {
		name    string
		value   *int64
		present bool
	}{
		{name: "omitted total", value: nil, present: false},
		{name: "explicit zero total", value: new(int64(0)), present: true},
		{name: "reported total", value: new(int64(1000)), present: true},
	}
	for _, excluded := range exclusions {
		for _, total := range totals {
			input, output, cacheRead, cacheWrite := int64(300), int64(50), int64(600), int64(100)
			evidence := AccountingEvidence{
				InputTokens:                  &input,
				OutputTokens:                 &output,
				CacheReadTokens:              &cacheRead,
				CacheWriteTokens:             &cacheWrite,
				TotalTokens:                  total.value,
				Presence:                     UsagePresence{InputTokens: true, OutputTokens: true, CacheReadTokens: true, CacheWriteTokens: true, TotalTokens: total.present},
				Source:                       AccountingSourceProviderReported,
				Authority:                    AccountingAuthorityAuthoritative,
				Plane:                        AccountingPlaneProviderBillable,
				DedupeKey:                    "provider:req-cache-write",
				InputTokensExcludeCacheRead:  excluded.read,
				InputTokensExcludeCacheWrite: excluded.write,
			}
			wire, err := accountingEvidenceToProto(&evidence)
			if err != nil {
				t.Fatalf("read=%v write=%v %s: to proto: %v", excluded.read, excluded.write, total.name, err)
			}
			if wire.GetInputTokensExcludeCacheRead() != excluded.read || wire.GetInputTokensExcludeCacheWrite() != excluded.write {
				t.Fatalf("read=%v write=%v %s: wire=%+v", excluded.read, excluded.write, total.name, wire)
			}
			encoded, err := proto.Marshal(wire)
			if err != nil {
				t.Fatalf("read=%v write=%v %s: marshal: %v", excluded.read, excluded.write, total.name, err)
			}
			var decoded backendpluginv1.AccountingEvidence
			if err := proto.Unmarshal(encoded, &decoded); err != nil {
				t.Fatalf("read=%v write=%v %s: unmarshal: %v", excluded.read, excluded.write, total.name, err)
			}
			got, err := accountingEvidenceFromProto(&decoded)
			if err != nil {
				t.Fatalf("read=%v write=%v %s: from proto: %v", excluded.read, excluded.write, total.name, err)
			}
			if got.InputTokensExcludeCacheRead != excluded.read || got.InputTokensExcludeCacheWrite != excluded.write {
				t.Fatalf("read=%v write=%v %s: round-trip=%+v", excluded.read, excluded.write, total.name, got)
			}
			if got.InputTokens == nil || *got.InputTokens != input || got.OutputTokens == nil || *got.OutputTokens != output ||
				got.CacheReadTokens == nil || *got.CacheReadTokens != cacheRead || got.CacheWriteTokens == nil || *got.CacheWriteTokens != cacheWrite {
				t.Fatalf("read=%v write=%v %s: counters changed: %+v", excluded.read, excluded.write, total.name, got)
			}
			if got.Presence.TotalTokens != total.present {
				t.Fatalf("read=%v write=%v %s: total presence=%v want %v", excluded.read, excluded.write, total.name, got.Presence.TotalTokens, total.present)
			}
			switch {
			case !total.present && got.TotalTokens != nil:
				t.Fatalf("read=%v write=%v %s: omitted total was reported as %d", excluded.read, excluded.write, total.name, *got.TotalTokens)
			case total.present && (got.TotalTokens == nil || *got.TotalTokens != *total.value):
				t.Fatalf("read=%v write=%v %s: reported total changed: %+v", excluded.read, excluded.write, total.name, got.TotalTokens)
			}
			if got.DedupeKey != evidence.DedupeKey || got.Source != evidence.Source ||
				got.Authority != evidence.Authority || got.Plane != evidence.Plane {
				t.Fatalf("read=%v write=%v %s: accounting identity changed: %+v", excluded.read, excluded.write, total.name, got)
			}
		}
	}
}
