package runtime

import (
	"fmt"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

type mockUsageStream struct {
	aipapi.ManagedEventStream
	evidence []aipapi.Event
}

func (m *mockUsageStream) DrainUsageEvidence() []aipapi.Event {
	return m.evidence
}

func TestAttemptUsageEvidence_UsageOrAccumulated(t *testing.T) {
	t.Parallel()

	completePrimary := aipapi.Event{
		Kind:          aipapi.EventUsageDelta,
		InputTokens:   100,
		OutputTokens:  50,
		CostNanoUnits: 15000,
		Currency:      "USD",
		CostPresent:   true,
		UsagePresence: aipapi.UsagePresence{InputTokens: true, OutputTokens: true},
	}
	tokenOnlyPrimary := aipapi.Event{
		Kind:          aipapi.EventUsageDelta,
		InputTokens:   80,
		UsagePresence: aipapi.UsagePresence{InputTokens: true},
	}
	costOnlyPrimary := aipapi.Event{
		Kind:          aipapi.EventUsageDelta,
		CostNanoUnits: 20000,
		Currency:      "EUR",
		CostPresent:   true,
	}
	kindEmptyWithPresence := aipapi.Event{
		InputTokens:   60,
		UsagePresence: aipapi.UsagePresence{InputTokens: true},
	}
	kindSetNoPresenceNoCost := aipapi.Event{
		Kind: aipapi.EventUsageDelta,
	}
	fullyEmpty := aipapi.Event{}

	tests := []struct {
		name          string
		nilSession    bool
		seedEvents    []aipapi.Event
		primary       aipapi.Event
		wantKind      aipapi.EventKind
		wantInput     int
		wantCostNano  int64
		wantCurrency  string
		wantCostPres  bool
		wantShellFall bool
	}{
		{
			name:          "nil session with complete primary returns primary",
			nilSession:    true,
			primary:       completePrimary,
			wantKind:      aipapi.EventUsageDelta,
			wantInput:     100,
			wantCostNano:  15000,
			wantCurrency:  "USD",
			wantCostPres:  true,
			wantShellFall: false,
		},
		{
			name:          "nil session with empty primary returns shell",
			nilSession:    true,
			primary:       fullyEmpty,
			wantShellFall: true,
		},
		{
			name:          "empty accumulated with complete primary returns primary",
			primary:       completePrimary,
			wantKind:      aipapi.EventUsageDelta,
			wantInput:     100,
			wantCostNano:  15000,
			wantCurrency:  "USD",
			wantCostPres:  true,
			wantShellFall: false,
		},
		{
			name:          "empty accumulated with token-only primary returns primary",
			primary:       tokenOnlyPrimary,
			wantKind:      aipapi.EventUsageDelta,
			wantInput:     80,
			wantCostPres:  false,
			wantShellFall: false,
		},
		{
			name:          "empty accumulated with cost-only primary returns primary",
			primary:       costOnlyPrimary,
			wantKind:      aipapi.EventUsageDelta,
			wantCostNano:  20000,
			wantCurrency:  "EUR",
			wantCostPres:  true,
			wantShellFall: false,
		},
		{
			name:          "empty accumulated with kind-empty-with-presence returns shell",
			primary:       kindEmptyWithPresence,
			wantShellFall: true,
		},
		{
			name:          "empty accumulated with kind-set-no-presence-no-cost returns shell",
			primary:       kindSetNoPresenceNoCost,
			wantShellFall: true,
		},
		{
			name:          "empty accumulated with fully-empty primary returns shell",
			primary:       fullyEmpty,
			wantShellFall: true,
		},
		{
			name: "token-only accumulated with complete primary returns primary",
			seedEvents: []aipapi.Event{{
				Kind:          aipapi.EventUsageDelta,
				InputTokens:   200,
				UsagePresence: aipapi.UsagePresence{InputTokens: true},
				Accounting: aipapi.UsageAccountingMetadata{
					Source:    aipapi.UsageSourceProviderReported,
					Authority: aipapi.UsageAuthorityAuthoritative,
					DedupeKey: "acc-token-1",
				},
			}},
			primary:       completePrimary,
			wantKind:      aipapi.EventUsageDelta,
			wantInput:     100,
			wantCostNano:  15000,
			wantCurrency:  "USD",
			wantCostPres:  true,
			wantShellFall: false,
		},
		{
			name: "token-only accumulated with empty primary returns accumulated",
			seedEvents: []aipapi.Event{{
				Kind:          aipapi.EventUsageDelta,
				InputTokens:   200,
				UsagePresence: aipapi.UsagePresence{InputTokens: true},
				Accounting: aipapi.UsageAccountingMetadata{
					Source:    aipapi.UsageSourceProviderReported,
					Authority: aipapi.UsageAuthorityAuthoritative,
					DedupeKey: "acc-token-2",
				},
			}},
			primary:       fullyEmpty,
			wantKind:      aipapi.EventUsageDelta,
			wantInput:     200,
			wantCostPres:  false,
			wantShellFall: false,
		},
		{
			name: "token-only accumulated with kind-empty-with-presence returns accumulated",
			seedEvents: []aipapi.Event{{
				Kind:          aipapi.EventUsageDelta,
				InputTokens:   200,
				UsagePresence: aipapi.UsagePresence{InputTokens: true},
				Accounting: aipapi.UsageAccountingMetadata{
					Source:    aipapi.UsageSourceProviderReported,
					Authority: aipapi.UsageAuthorityAuthoritative,
					DedupeKey: "acc-token-3",
				},
			}},
			primary:       kindEmptyWithPresence,
			wantKind:      aipapi.EventUsageDelta,
			wantInput:     200,
			wantCostPres:  false,
			wantShellFall: false,
		},
		{
			name: "token-only accumulated with kind-set-no-presence returns accumulated",
			seedEvents: []aipapi.Event{{
				Kind:          aipapi.EventUsageDelta,
				InputTokens:   200,
				UsagePresence: aipapi.UsagePresence{InputTokens: true},
				Accounting: aipapi.UsageAccountingMetadata{
					Source:    aipapi.UsageSourceProviderReported,
					Authority: aipapi.UsageAuthorityAuthoritative,
					DedupeKey: "acc-token-4",
				},
			}},
			primary:       kindSetNoPresenceNoCost,
			wantKind:      aipapi.EventUsageDelta,
			wantInput:     200,
			wantCostPres:  false,
			wantShellFall: false,
		},
		{
			name: "cost-only accumulated with cost-only primary returns primary",
			seedEvents: []aipapi.Event{{
				Kind:          aipapi.EventUsageDelta,
				CostNanoUnits: 30000,
				Currency:      "USD",
				CostPresent:   true,
				Accounting: aipapi.UsageAccountingMetadata{
					Source:    aipapi.UsageSourceProviderReported,
					Authority: aipapi.UsageAuthorityAuthoritative,
					DedupeKey: "acc-cost-1",
				},
			}},
			primary:       costOnlyPrimary,
			wantKind:      aipapi.EventUsageDelta,
			wantCostNano:  20000,
			wantCurrency:  "EUR",
			wantCostPres:  true,
			wantShellFall: false,
		},
		{
			name: "cost-only accumulated with empty primary returns accumulated",
			seedEvents: []aipapi.Event{{
				Kind:          aipapi.EventUsageDelta,
				CostNanoUnits: 30000,
				Currency:      "USD",
				CostPresent:   true,
				Accounting: aipapi.UsageAccountingMetadata{
					Source:    aipapi.UsageSourceProviderReported,
					Authority: aipapi.UsageAuthorityAuthoritative,
					DedupeKey: "acc-cost-2",
				},
			}},
			primary:       fullyEmpty,
			wantKind:      aipapi.EventUsageDelta,
			wantCostNano:  30000,
			wantCurrency:  "USD",
			wantCostPres:  true,
			wantShellFall: false,
		},
		{
			name: "full accumulated with fully-empty primary returns accumulated",
			seedEvents: []aipapi.Event{{
				Kind:          aipapi.EventUsageDelta,
				InputTokens:   350,
				CostNanoUnits: 45000,
				Currency:      "USD",
				CostPresent:   true,
				UsagePresence: aipapi.UsagePresence{InputTokens: true},
				Accounting: aipapi.UsageAccountingMetadata{
					Source:    aipapi.UsageSourceProviderReported,
					Authority: aipapi.UsageAuthorityAuthoritative,
					DedupeKey: "acc-full-1",
				},
			}},
			primary:       fullyEmpty,
			wantKind:      aipapi.EventUsageDelta,
			wantInput:     350,
			wantCostNano:  45000,
			wantCurrency:  "USD",
			wantCostPres:  true,
			wantShellFall: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var sess *attemptSession
			if !tt.nilSession {
				sess = newAttemptSession(attemptSessionInput{})
				for _, ev := range tt.seedEvents {
					sess.rememberUsageEvidenceOnce(ev)
				}
			}

			got := sess.usageOrAccumulated(tt.primary)

			if tt.wantShellFall {
				wantShell := emptyOperatorUsageShell()
				if got.Kind != wantShell.Kind || got.UsagePresence != wantShell.UsagePresence || got.CostPresent != wantShell.CostPresent {
					t.Fatalf("expected emptyOperatorUsageShell %+v, got %+v", wantShell, got)
				}
				return
			}

			if got.Kind != tt.wantKind {
				t.Fatalf("Kind = %q, want %q", got.Kind, tt.wantKind)
			}
			if tt.wantInput > 0 && got.InputTokens != tt.wantInput {
				t.Fatalf("InputTokens = %d, want %d", got.InputTokens, tt.wantInput)
			}
			if tt.wantCostPres != got.CostPresent {
				t.Fatalf("CostPresent = %v, want %v", got.CostPresent, tt.wantCostPres)
			}
			if tt.wantCostNano > 0 && got.CostNanoUnits != tt.wantCostNano {
				t.Fatalf("CostNanoUnits = %d, want %d", got.CostNanoUnits, tt.wantCostNano)
			}
			if tt.wantCurrency != "" && got.Currency != tt.wantCurrency {
				t.Fatalf("Currency = %q, want %q", got.Currency, tt.wantCurrency)
			}
		})
	}
}

func TestAttemptUsageEvidence_AugmentBillingUsage(t *testing.T) {
	t.Parallel()

	completeStream := aipapi.Event{
		Kind:          aipapi.EventUsageDelta,
		InputTokens:   120,
		OutputTokens:  60,
		CostNanoUnits: 18000,
		Currency:      "USD",
		CostPresent:   true,
		UsagePresence: aipapi.UsagePresence{InputTokens: true, OutputTokens: true},
	}
	tokenOnlyStream := aipapi.Event{
		Kind:          aipapi.EventUsageDelta,
		InputTokens:   90,
		UsagePresence: aipapi.UsagePresence{InputTokens: true},
	}
	costOnlyStream := aipapi.Event{
		Kind:          aipapi.EventUsageDelta,
		CostNanoUnits: 25000,
		Currency:      "EUR",
		CostPresent:   true,
	}
	kindEmptyWithPresence := aipapi.Event{
		InputTokens:   70,
		UsagePresence: aipapi.UsagePresence{InputTokens: true},
	}
	kindSetNoPresenceNoCost := aipapi.Event{
		Kind: aipapi.EventUsageDelta,
	}
	fullyEmpty := aipapi.Event{}

	tests := []struct {
		name            string
		nilSession      bool
		seedEvents      []aipapi.Event
		streamEv        aipapi.Event
		fallbackPrimary aipapi.Event
		wantKind        aipapi.EventKind
		wantInput       int
		wantCostNano    int64
		wantCurrency    string
		wantCostPres    bool
		wantShellFall   bool
	}{
		{
			name:          "nil session with complete streamEv returns streamEv",
			nilSession:    true,
			streamEv:      completeStream,
			wantKind:      aipapi.EventUsageDelta,
			wantInput:     120,
			wantCostNano:  18000,
			wantCurrency:  "USD",
			wantCostPres:  true,
			wantShellFall: false,
		},
		{
			name:          "nil session with empty streamEv returns shell",
			nilSession:    true,
			streamEv:      fullyEmpty,
			wantShellFall: true,
		},
		{
			name:          "empty accumulated with complete streamEv returns streamEv",
			streamEv:      completeStream,
			wantKind:      aipapi.EventUsageDelta,
			wantInput:     120,
			wantCostNano:  18000,
			wantCurrency:  "USD",
			wantCostPres:  true,
			wantShellFall: false,
		},
		{
			name:          "empty accumulated with token-only streamEv returns streamEv without cost",
			streamEv:      tokenOnlyStream,
			wantKind:      aipapi.EventUsageDelta,
			wantInput:     90,
			wantCostPres:  false,
			wantShellFall: false,
		},
		{
			name:          "empty accumulated with fully-empty streamEv returns shell",
			streamEv:      fullyEmpty,
			wantShellFall: true,
		},
		{
			name:            "empty streamEv with fallbackPrimary folds fallbackPrimary first",
			fallbackPrimary: completeStream,
			streamEv:        fullyEmpty,
			wantKind:        aipapi.EventUsageDelta,
			wantInput:       120,
			wantCostNano:    18000,
			wantCurrency:    "USD",
			wantCostPres:    true,
			wantShellFall:   false,
		},
		{
			name: "token-only accumulated with complete streamEv leaves streamEv unmodified",
			seedEvents: []aipapi.Event{{
				Kind:          aipapi.EventUsageDelta,
				InputTokens:   300,
				UsagePresence: aipapi.UsagePresence{InputTokens: true},
				Accounting: aipapi.UsageAccountingMetadata{
					Source:    aipapi.UsageSourceProviderReported,
					Authority: aipapi.UsageAuthorityAuthoritative,
					DedupeKey: "aug-tok-1",
				},
			}},
			streamEv:      completeStream,
			wantKind:      aipapi.EventUsageDelta,
			wantInput:     120,
			wantCostNano:  18000,
			wantCurrency:  "USD",
			wantCostPres:  true,
			wantShellFall: false,
		},
		{
			name: "token-only accumulated with empty streamEv substitutes accumulated",
			seedEvents: []aipapi.Event{{
				Kind:          aipapi.EventUsageDelta,
				InputTokens:   300,
				UsagePresence: aipapi.UsagePresence{InputTokens: true},
				Accounting: aipapi.UsageAccountingMetadata{
					Source:    aipapi.UsageSourceProviderReported,
					Authority: aipapi.UsageAuthorityAuthoritative,
					DedupeKey: "aug-tok-2",
				},
			}},
			streamEv:      fullyEmpty,
			wantKind:      aipapi.EventUsageDelta,
			wantInput:     300,
			wantCostPres:  false,
			wantShellFall: false,
		},
		{
			name: "token-only accumulated with kind-empty-with-presence substitutes accumulated",
			seedEvents: []aipapi.Event{{
				Kind:          aipapi.EventUsageDelta,
				InputTokens:   300,
				UsagePresence: aipapi.UsagePresence{InputTokens: true},
				Accounting: aipapi.UsageAccountingMetadata{
					Source:    aipapi.UsageSourceProviderReported,
					Authority: aipapi.UsageAuthorityAuthoritative,
					DedupeKey: "aug-tok-3",
				},
			}},
			streamEv:      kindEmptyWithPresence,
			wantKind:      aipapi.EventUsageDelta,
			wantInput:     300,
			wantCostPres:  false,
			wantShellFall: false,
		},
		{
			name: "cost-only accumulated with token-only streamEv backfills cost while preserving tokens",
			seedEvents: []aipapi.Event{{
				Kind:          aipapi.EventUsageDelta,
				CostNanoUnits: 42000,
				Currency:      "USD",
				CostPresent:   true,
				Accounting: aipapi.UsageAccountingMetadata{
					Source:    aipapi.UsageSourceProviderReported,
					Authority: aipapi.UsageAuthorityAuthoritative,
					DedupeKey: "aug-cost-1",
				},
			}},
			streamEv:      tokenOnlyStream,
			wantKind:      aipapi.EventUsageDelta,
			wantInput:     90,
			wantCostNano:  42000,
			wantCurrency:  "USD",
			wantCostPres:  true,
			wantShellFall: false,
		},
		{
			name: "cost-only accumulated with cost-only streamEv retains streamEv cost without overwrite",
			seedEvents: []aipapi.Event{{
				Kind:          aipapi.EventUsageDelta,
				CostNanoUnits: 42000,
				Currency:      "USD",
				CostPresent:   true,
				Accounting: aipapi.UsageAccountingMetadata{
					Source:    aipapi.UsageSourceProviderReported,
					Authority: aipapi.UsageAuthorityAuthoritative,
					DedupeKey: "aug-cost-2",
				},
			}},
			streamEv:      costOnlyStream,
			wantKind:      aipapi.EventUsageDelta,
			wantCostNano:  25000,
			wantCurrency:  "EUR",
			wantCostPres:  true,
			wantShellFall: false,
		},
		{
			name: "full accumulated with token-only streamEv backfills cost while keeping original stream tokens",
			seedEvents: []aipapi.Event{{
				Kind:          aipapi.EventUsageDelta,
				InputTokens:   999,
				CostNanoUnits: 88000,
				Currency:      "USD",
				CostPresent:   true,
				UsagePresence: aipapi.UsagePresence{InputTokens: true},
				Accounting: aipapi.UsageAccountingMetadata{
					Source:    aipapi.UsageSourceProviderReported,
					Authority: aipapi.UsageAuthorityAuthoritative,
					DedupeKey: "aug-full-1",
				},
			}},
			streamEv:      tokenOnlyStream,
			wantKind:      aipapi.EventUsageDelta,
			wantInput:     90, // Stream tokens preserved!
			wantCostNano:  88000,
			wantCurrency:  "USD",
			wantCostPres:  true,
			wantShellFall: false,
		},
		{
			name: "full accumulated with fully-empty streamEv substitutes accumulated full event",
			seedEvents: []aipapi.Event{{
				Kind:          aipapi.EventUsageDelta,
				InputTokens:   999,
				CostNanoUnits: 88000,
				Currency:      "USD",
				CostPresent:   true,
				UsagePresence: aipapi.UsagePresence{InputTokens: true},
				Accounting: aipapi.UsageAccountingMetadata{
					Source:    aipapi.UsageSourceProviderReported,
					Authority: aipapi.UsageAuthorityAuthoritative,
					DedupeKey: "aug-full-2",
				},
			}},
			streamEv:      fullyEmpty,
			wantKind:      aipapi.EventUsageDelta,
			wantInput:     999,
			wantCostNano:  88000,
			wantCurrency:  "USD",
			wantCostPres:  true,
			wantShellFall: false,
		},
		{
			name: "full accumulated with kind-set-no-presence streamEv substitutes accumulated",
			seedEvents: []aipapi.Event{{
				Kind:          aipapi.EventUsageDelta,
				InputTokens:   999,
				CostNanoUnits: 88000,
				Currency:      "USD",
				CostPresent:   true,
				UsagePresence: aipapi.UsagePresence{InputTokens: true},
				Accounting: aipapi.UsageAccountingMetadata{
					Source:    aipapi.UsageSourceProviderReported,
					Authority: aipapi.UsageAuthorityAuthoritative,
					DedupeKey: "aug-full-3",
				},
			}},
			streamEv:      kindSetNoPresenceNoCost,
			wantKind:      aipapi.EventUsageDelta,
			wantInput:     999,
			wantCostNano:  88000,
			wantCurrency:  "USD",
			wantCostPres:  true,
			wantShellFall: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var sess *attemptSession
			if !tt.nilSession {
				sess = newAttemptSession(attemptSessionInput{})
				for _, ev := range tt.seedEvents {
					sess.rememberUsageEvidenceOnce(ev)
				}
			}

			got := sess.augmentBillingUsage(tt.streamEv, tt.fallbackPrimary)

			if tt.wantShellFall {
				wantShell := emptyOperatorUsageShell()
				if got.Kind != wantShell.Kind || got.UsagePresence != wantShell.UsagePresence || got.CostPresent != wantShell.CostPresent {
					t.Fatalf("expected emptyOperatorUsageShell %+v, got %+v", wantShell, got)
				}
				return
			}

			if got.Kind != tt.wantKind {
				t.Fatalf("Kind = %q, want %q", got.Kind, tt.wantKind)
			}
			if tt.wantInput > 0 && got.InputTokens != tt.wantInput {
				t.Fatalf("InputTokens = %d, want %d", got.InputTokens, tt.wantInput)
			}
			if tt.wantCostPres != got.CostPresent {
				t.Fatalf("CostPresent = %v, want %v", got.CostPresent, tt.wantCostPres)
			}
			if tt.wantCostNano > 0 && got.CostNanoUnits != tt.wantCostNano {
				t.Fatalf("CostNanoUnits = %d, want %d", got.CostNanoUnits, tt.wantCostNano)
			}
			if tt.wantCurrency != "" && got.Currency != tt.wantCurrency {
				t.Fatalf("Currency = %q, want %q", got.Currency, tt.wantCurrency)
			}
		})
	}
}

func TestAttemptUsageEvidence_DrainAndDedupe(t *testing.T) {
	t.Parallel()

	sess := newAttemptSession(attemptSessionInput{})

	mock := &mockUsageStream{
		evidence: []aipapi.Event{
			{
				Kind:          aipapi.EventUsageDelta,
				InputTokens:   10,
				UsagePresence: aipapi.UsagePresence{InputTokens: true},
				Accounting: aipapi.UsageAccountingMetadata{
					Source:    aipapi.UsageSourceProviderReported,
					Authority: aipapi.UsageAuthorityAuthoritative,
					DedupeKey: "dup-key",
				},
			},
			{
				Kind:          aipapi.EventUsageDelta,
				InputTokens:   20,
				UsagePresence: aipapi.UsagePresence{InputTokens: true},
				Accounting: aipapi.UsageAccountingMetadata{
					Source:    aipapi.UsageSourceProviderReported,
					Authority: aipapi.UsageAuthorityAuthoritative,
					DedupeKey: "dup-key", // duplicate key
				},
			},
			{
				Kind: "non.usage.event",
			},
			{
				Kind:          aipapi.EventUsageDelta,
				InputTokens:   30,
				UsagePresence: aipapi.UsagePresence{InputTokens: true},
				Accounting: aipapi.UsageAccountingMetadata{
					Source:    aipapi.UsageSourceProviderReported,
					Authority: aipapi.UsageAuthorityAuthoritative,
					DedupeKey: "unique-key",
				},
			},
		},
	}

	sess.drainStreamUsageEvidence(mock)

	agg := sess.aggregatedUsageEvidence()
	if agg.Kind == "" {
		t.Fatalf("expected aggregated evidence, got empty")
	}
	// 10 + 30 = 40 input tokens (second 20 skipped due to duplicate key)
	if agg.InputTokens != 40 {
		t.Fatalf("InputTokens = %d, want 40", agg.InputTokens)
	}
}

func TestAttemptUsageEvidence_OverflowDoesNotRetainOrObserve(t *testing.T) {
	t.Parallel()

	sess := newAttemptSession(attemptSessionInput{})
	for i := range maxAttemptAccumulatedUsage {
		sess.recordUsageEvidence(aipapi.Event{
			Kind:         aipapi.EventUsageDelta,
			OutputTokens: 1,
			Accounting:   aipapi.UsageAccountingMetadata{DedupeKey: fmt.Sprintf("usage-%d", i)},
		})
	}
	if got := len(sess.internalUsageKeys); got != maxAttemptAccumulatedUsage {
		t.Fatalf("dedupe keys before overflow = %d, want %d", got, maxAttemptAccumulatedUsage)
	}
	if got := len(sess.accumulatedUsage); got != maxAttemptAccumulatedUsage {
		t.Fatalf("retained evidence before overflow = %d, want %d", got, maxAttemptAccumulatedUsage)
	}
	observedBefore := sess.accounting.outputTokens
	overflow := aipapi.Event{
		Kind:         aipapi.EventUsageDelta,
		OutputTokens: 100,
		Accounting:   aipapi.UsageAccountingMetadata{DedupeKey: "usage-overflow"},
	}
	sess.recordUsageEvidence(overflow)
	sess.recordUsageEvidence(overflow)
	if got := len(sess.internalUsageKeys); got != maxAttemptAccumulatedUsage {
		t.Fatalf("dedupe keys after overflow = %d, want bounded %d", got, maxAttemptAccumulatedUsage)
	}
	if got := len(sess.accumulatedUsage); got != maxAttemptAccumulatedUsage {
		t.Fatalf("retained evidence after overflow = %d, want bounded %d", got, maxAttemptAccumulatedUsage)
	}
	if got := sess.accounting.outputTokens; got != observedBefore {
		t.Fatalf("overflow output tokens = %d, want unchanged %d", got, observedBefore)
	}
	if sess.rememberUsageEvidenceOnce(aipapi.Event{
		Kind:       aipapi.EventUsageDelta,
		Accounting: aipapi.UsageAccountingMetadata{DedupeKey: "usage-0"},
	}) {
		t.Fatal("existing duplicate key was accepted after the cap")
	}
}

func TestAttemptUsageEvidence_OversizedDedupeKeyIsRejectedBeforeObservation(t *testing.T) {
	t.Parallel()

	sess := newAttemptSession(attemptSessionInput{})
	sess.recordUsageEvidence(aipapi.Event{
		Kind: aipapi.EventUsageDelta,
		Accounting: aipapi.UsageAccountingMetadata{
			DedupeKey: strings.Repeat("k", maxAttemptUsageDedupeKeyBytes+1),
		},
	})
	if len(sess.internalUsageKeys) != 0 {
		t.Fatalf("oversized key retained in dedupe map: %d entries", len(sess.internalUsageKeys))
	}
	if len(sess.accumulatedUsage) != 0 {
		t.Fatalf("oversized evidence retained: %d entries", len(sess.accumulatedUsage))
	}
	if sess.accounting.usageObserved || sess.accounting.outputTokens != 0 {
		t.Fatalf("oversized evidence was observed: usageObserved=%v outputTokens=%d", sess.accounting.usageObserved, sess.accounting.outputTokens)
	}
}
