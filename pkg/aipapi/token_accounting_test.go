package aipapi_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestTokenAccounting_zeroValueCompatibility(t *testing.T) {
	t.Parallel()

	var meta aipapi.UsageAccountingMetadata
	if meta.Plane != aipapi.UsagePlaneUnknown {
		t.Fatalf("plane zero value: %q", meta.Plane)
	}
	if meta.Source != aipapi.UsageSourceUnknown {
		t.Fatalf("source zero value: %q", meta.Source)
	}
	if meta.Authority != aipapi.UsageAuthorityUnknown {
		t.Fatalf("authority zero value: %q", meta.Authority)
	}

	ev := aipapi.Event{Kind: aipapi.EventUsageDelta, InputTokens: 7, OutputTokens: 3}
	if len(ev.UsageScopes) != 0 {
		t.Fatalf("zero-value usage scopes: %#v", ev.UsageScopes)
	}
}

func TestEventUsageDelta_scopedUsagePreservesLegacyTotals(t *testing.T) {
	t.Parallel()

	ev := aipapi.Event{
		Kind:         aipapi.EventUsageDelta,
		InputTokens:  10,
		OutputTokens: 4,
		UsageScopes: []aipapi.ScopedUsageDelta{
			{
				InputTokens:  10,
				OutputTokens: 4,
				Accounting: aipapi.UsageAccountingMetadata{
					Plane:     aipapi.UsagePlaneProviderBillable,
					Source:    aipapi.UsageSourceProviderReported,
					Authority: aipapi.UsageAuthorityAuthoritative,
					Tokenizer: aipapi.TokenizerRef{Type: "provider", ID: "openai", Version: "2026-01", Source: "response.usage", ModelUsed: "gpt-x"},
				},
			},
			{
				InputTokens:  8,
				OutputTokens: 4,
				Accounting: aipapi.UsageAccountingMetadata{
					Plane:     aipapi.UsagePlaneClientVisible,
					Source:    aipapi.UsageSourceProxyAdjusted,
					Authority: aipapi.UsageAuthorityDelegated,
				},
			},
		},
	}

	if ev.InputTokens != 10 || ev.OutputTokens != 4 {
		t.Fatalf("legacy totals changed: in=%d out=%d", ev.InputTokens, ev.OutputTokens)
	}
	if len(ev.UsageScopes) != 2 {
		t.Fatalf("usage scopes: %#v", ev.UsageScopes)
	}
	if ev.UsageScopes[0].Accounting.Plane != aipapi.UsagePlaneProviderBillable {
		t.Fatalf("provider plane: %#v", ev.UsageScopes[0].Accounting)
	}
	if ev.UsageScopes[1].Accounting.Plane != aipapi.UsagePlaneClientVisible {
		t.Fatalf("client plane: %#v", ev.UsageScopes[1].Accounting)
	}
}

func TestEventUsageDelta_jsonRoundTrip(t *testing.T) {
	t.Parallel()

	in := aipapi.Event{
		Kind:             aipapi.EventUsageDelta,
		InputTokens:      5,
		OutputTokens:     2,
		CacheReadTokens:  1,
		Accounting:       aipapi.UsageAccountingMetadata{Plane: aipapi.UsagePlaneProviderBillable, Source: aipapi.UsageSourceProviderReported, Authority: aipapi.UsageAuthorityAuthoritative},
		UsageScopes:      []aipapi.ScopedUsageDelta{{InputTokens: 5, OutputTokens: 2, Accounting: aipapi.UsageAccountingMetadata{Plane: aipapi.UsagePlaneClientVisible, Source: aipapi.UsageSourceLocalTokenizer, Authority: aipapi.UsageAuthorityEstimated}}},
		RawUsageJSON:     `{"input_tokens":5}`,
		CostNanoUnits:    1200,
		Currency:         "USD",
		CostSource:       "provider",
		CostPresent:      true,
		ReasoningTokens:  3,
		CacheWriteTokens: 4,
		TotalTokens:      7,
		UsagePresence:    aipapi.UsagePresence{InputTokens: true, OutputTokens: true, TotalTokens: true},
	}

	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out aipapi.Event
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if out.Kind != aipapi.EventUsageDelta || out.InputTokens != 5 || out.OutputTokens != 2 {
		t.Fatalf("legacy fields: %#v", out)
	}
	if out.Accounting.Plane != aipapi.UsagePlaneProviderBillable || out.UsageScopes[0].Accounting.Source != aipapi.UsageSourceLocalTokenizer {
		t.Fatalf("accounting metadata: %#v", out)
	}
	if out.UsagePresence != in.UsagePresence {
		t.Fatalf("usage presence: got %+v, want %+v", out.UsagePresence, in.UsagePresence)
	}
	if !out.CostPresent {
		t.Fatal("cost presence: got false, want true")
	}
}

func TestFixedEventStream_deepCopiesScopedUsage(t *testing.T) {
	t.Parallel()

	events := []aipapi.Event{{
		Kind:        aipapi.EventUsageDelta,
		UsageScopes: []aipapi.ScopedUsageDelta{{InputTokens: 1, Accounting: aipapi.UsageAccountingMetadata{Plane: aipapi.UsagePlaneProviderBillable}}},
	}}
	stream := aipapi.NewFixedEventStream(events)
	events[0].UsageScopes[0].InputTokens = 99

	out, err := stream.Recv(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	out.UsageScopes[0].InputTokens = 42

	stream2 := aipapi.NewFixedEventStream([]aipapi.Event{out})
	out.UsageScopes[0].InputTokens = 77
	out2, err := stream2.Recv(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if out2.UsageScopes[0].InputTokens != 42 {
		t.Fatalf("usage scopes were not deep-copied: %#v", out2.UsageScopes)
	}
}

func TestValidateEventEnvelope_rejectsOversizedAccountingFields(t *testing.T) {
	t.Parallel()

	ev := &aipapi.Event{
		Kind: aipapi.EventUsageDelta,
		Accounting: aipapi.UsageAccountingMetadata{
			Tokenizer: aipapi.TokenizerRef{ID: strings.Repeat("x", aipapi.MaxRefStringBytes+1)},
		},
	}
	if err := aipapi.ValidateEventEnvelope(ev); err == nil {
		t.Fatal("expected error")
	}
}
