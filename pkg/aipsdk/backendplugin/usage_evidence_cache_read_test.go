package backendplugin

import (
	"testing"

	"google.golang.org/protobuf/proto"

	backendpluginv1 "github.com/aiproxer/aiproxer/api/backendplugin/v1"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// The input relation travels with the counters across the executable
// connector wire ABI in both states. The zero value retains the legacy
// inclusive-input interpretation.
func TestUsageEvidenceProtoRoundTripPreservesCacheReadExclusion(t *testing.T) {
	t.Parallel()
	input, cache := int64(300), int64(600)
	for _, excluded := range []bool{true, false} {
		evidence := UsageEvidence{
			InputTokens:                 &input,
			CacheReadTokens:             &cache,
			Presence:                    UsagePresence{InputTokens: true, CacheReadTokens: true},
			RawUsageJSON:                RawJSONAbsentValue(),
			InputTokensExcludeCacheRead: excluded,
		}
		wire, err := UsageEvidenceToProto(evidence)
		if err != nil {
			t.Fatalf("excluded=%v to proto: %v", excluded, err)
		}
		if wire.GetInputTokensExcludeCacheRead() != excluded {
			t.Fatalf("excluded=%v wire field=%v", excluded, wire.GetInputTokensExcludeCacheRead())
		}
		got, err := UsageEvidenceFromProto(wire)
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

// A connector publishes its canonical usage event with the provider's input
// relation; the host must receive that relation with the counters.
func TestCanonicalEventFromAipapi_carriesCacheReadExclusion(t *testing.T) {
	t.Parallel()
	for _, excluded := range []bool{true, false} {
		event := aipapi.Event{
			Kind:            aipapi.EventUsageDelta,
			InputTokens:     300,
			CacheReadTokens: 600,
			UsagePresence:   aipapi.UsagePresence{InputTokens: true, CacheReadTokens: true},
			Accounting:      aipapi.UsageAccountingMetadata{InputTokensExcludeCacheRead: excluded},
		}
		dto := CanonicalEventFromAipapi(event)
		if dto.Usage == nil {
			t.Fatalf("excluded=%v usage missing", excluded)
		}
		if dto.Usage.InputTokensExcludeCacheRead != excluded {
			t.Fatalf("excluded=%v canonical usage=%v", excluded, dto.Usage.InputTokensExcludeCacheRead)
		}
		wire, err := CanonicalEventToProto(dto)
		if err != nil {
			t.Fatalf("excluded=%v to proto: %v", excluded, err)
		}
		back, err := CanonicalEventFromProto(wire)
		if err != nil {
			t.Fatalf("excluded=%v from proto: %v", excluded, err)
		}
		if back.Usage == nil || back.Usage.InputTokensExcludeCacheRead != excluded {
			t.Fatalf("excluded=%v round-trip=%+v", excluded, back.Usage)
		}
	}
}

// The two input relations are independent declarations: a producer may exclude
// reads while including writes, exclude writes while including reads, or
// exclude both. Each state must survive the canonical event, the SDK DTO and
// real protobuf bytes, next to unchanged native quantities and truthful total
// presence.
func TestUsageEvidenceProtoRoundTripPreservesIndependentCacheExclusions(t *testing.T) {
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
		value   int
		present bool
	}{
		{name: "omitted total", value: 0, present: false},
		{name: "explicit zero total", value: 0, present: true},
		{name: "reported total", value: 1000, present: true},
	}
	for _, excluded := range exclusions {
		for _, total := range totals {
			event := aipapi.Event{
				Kind:             aipapi.EventUsageDelta,
				InputTokens:      300,
				OutputTokens:     50,
				CacheReadTokens:  600,
				CacheWriteTokens: 100,
				TotalTokens:      total.value,
				UsagePresence: aipapi.UsagePresence{
					InputTokens: true, OutputTokens: true, CacheReadTokens: true,
					CacheWriteTokens: true, TotalTokens: total.present,
				},
				Accounting: aipapi.UsageAccountingMetadata{
					InputTokensExcludeCacheRead:  excluded.read,
					InputTokensExcludeCacheWrite: excluded.write,
				},
			}
			dto := CanonicalEventFromAipapi(event)
			if dto.Usage == nil {
				t.Fatalf("read=%v write=%v %s: usage missing", excluded.read, excluded.write, total.name)
			}
			if dto.Usage.InputTokensExcludeCacheRead != excluded.read || dto.Usage.InputTokensExcludeCacheWrite != excluded.write {
				t.Fatalf("read=%v write=%v %s: canonical usage=%+v", excluded.read, excluded.write, total.name, dto.Usage)
			}
			wire, err := UsageEvidenceToProto(*dto.Usage)
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
			var decoded backendpluginv1.UsageEvidence
			if err := proto.Unmarshal(encoded, &decoded); err != nil {
				t.Fatalf("read=%v write=%v %s: unmarshal: %v", excluded.read, excluded.write, total.name, err)
			}
			got, err := UsageEvidenceFromProto(&decoded)
			if err != nil {
				t.Fatalf("read=%v write=%v %s: from proto: %v", excluded.read, excluded.write, total.name, err)
			}
			if got.InputTokensExcludeCacheRead != excluded.read || got.InputTokensExcludeCacheWrite != excluded.write {
				t.Fatalf("read=%v write=%v %s: round-trip=%+v", excluded.read, excluded.write, total.name, got)
			}
			if got.InputTokens == nil || *got.InputTokens != 300 || got.OutputTokens == nil || *got.OutputTokens != 50 ||
				got.CacheReadTokens == nil || *got.CacheReadTokens != 600 || got.CacheWriteTokens == nil || *got.CacheWriteTokens != 100 {
				t.Fatalf("read=%v write=%v %s: counters changed: %+v", excluded.read, excluded.write, total.name, got)
			}
			if got.Presence.TotalTokens != total.present {
				t.Fatalf("read=%v write=%v %s: total presence=%v want %v", excluded.read, excluded.write, total.name, got.Presence.TotalTokens, total.present)
			}
			switch {
			case !total.present && got.TotalTokens != nil:
				t.Fatalf("read=%v write=%v %s: omitted total was reported as %d", excluded.read, excluded.write, total.name, *got.TotalTokens)
			case total.present && (got.TotalTokens == nil || *got.TotalTokens != int64(total.value)):
				t.Fatalf("read=%v write=%v %s: reported total changed: %+v", excluded.read, excluded.write, total.name, got.TotalTokens)
			}
		}
	}
}
