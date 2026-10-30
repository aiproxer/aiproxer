package aipapi_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestFixedEventStream_Recv_nilContext(t *testing.T) {
	t.Parallel()
	s := aipapi.NewFixedEventStream(nil)
	_, err := s.Recv(nil) //nolint:staticcheck // deliberate nil ctx; expect aipapi.ErrNilContext
	if !errors.Is(err, aipapi.ErrNilContext) {
		t.Fatalf("got %v", err)
	}
}

func TestFixedEventStream_Recv_nilReceiver(t *testing.T) {
	t.Parallel()
	var s *aipapi.FixedEventStream
	_, err := s.Recv(context.Background())
	if !errors.Is(err, aipapi.ErrNilFixedEventStream) {
		t.Fatalf("got %v", err)
	}
}

func TestCollectWithLimits_nilContext(t *testing.T) {
	t.Parallel()
	s := aipapi.NewFixedEventStream(nil)
	_, err := aipapi.CollectWithLimits(nil, s, aipapi.CollectLimits{}) //nolint:staticcheck // deliberate nil ctx
	if !errors.Is(err, aipapi.ErrNilContext) {
		t.Fatalf("got %v", err)
	}
}

func TestCollect_nilContext(t *testing.T) {
	t.Parallel()
	s := aipapi.NewFixedEventStream(nil)
	_, err := aipapi.Collect(nil, s) //nolint:staticcheck // deliberate nil ctx
	if !errors.Is(err, aipapi.ErrNilContext) {
		t.Fatalf("got %v", err)
	}
}

func TestCollectUnbounded_nilContext(t *testing.T) {
	t.Parallel()
	s := aipapi.NewFixedEventStream(nil)
	_, err := aipapi.CollectUnbounded(nil, s) //nolint:staticcheck // deliberate nil ctx
	if !errors.Is(err, aipapi.ErrNilContext) {
		t.Fatalf("got %v", err)
	}
}

func TestCollect_usageCostAndUncachedInput(t *testing.T) {
	t.Parallel()
	s := aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{
			Kind:            aipapi.EventUsageDelta,
			InputTokens:     10,
			OutputTokens:    3,
			CacheReadTokens: 4,
			CostNanoUnits:   123,
			Currency:        "USD",
			CostSource:      "estimated",
		},
		{Kind: aipapi.EventResponseFinished},
	})
	col, err := aipapi.Collect(t.Context(), s)
	if err != nil {
		t.Fatal(err)
	}
	if col.UncachedInputTokens() != 6 {
		t.Fatalf("uncached = %d want 6", col.UncachedInputTokens())
	}
	if col.CostNanoUnits != 123 || col.Currency != "USD" || col.CostSource != "estimated" {
		t.Fatalf("cost = %d %q %q", col.CostNanoUnits, col.Currency, col.CostSource)
	}
}

func TestCollectedAccumulateUsageMatchesCollectUsage(t *testing.T) {
	t.Parallel()
	events := []aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventUsageDelta, InputTokens: 10, OutputTokens: 2, CacheReadTokens: 3, CostNanoUnits: 7, Currency: "USD"},
		{Kind: aipapi.EventUsageDelta, InputTokens: 1, OutputTokens: 4, CacheWriteTokens: 5, ReasoningTokens: 2, TotalTokens: 20, CostNanoUnits: 11, CostSource: "estimated"},
		{Kind: aipapi.EventResponseFinished},
	}
	var direct aipapi.Collected
	for _, ev := range events {
		if ev.Kind == aipapi.EventUsageDelta {
			direct.AccumulateUsage(ev)
		}
	}
	col, err := aipapi.Collect(t.Context(), aipapi.NewFixedEventStream(events))
	if err != nil {
		t.Fatal(err)
	}
	if direct.InputTokens != col.InputTokens || direct.OutputTokens != col.OutputTokens || direct.CacheReadTokens != col.CacheReadTokens || direct.CacheWriteTokens != col.CacheWriteTokens || direct.ReasoningTokens != col.ReasoningTokens || direct.TotalTokens != col.TotalTokens || direct.CostNanoUnits != col.CostNanoUnits || direct.Currency != col.Currency || direct.CostSource != col.CostSource {
		t.Fatalf("direct=%+v collect=%+v", direct, col)
	}
}

func TestCollected_UncachedInputSemantics(t *testing.T) {
	t.Parallel()
	exclusive := aipapi.UsageAccountingMetadata{InputTokensExcludeCacheRead: true}
	seeded := aipapi.Collected{InputTokens: 100, CacheReadTokens: 40}
	seeded.AccumulateUsage(aipapi.Event{InputTokens: 300, CacheReadTokens: 600, Accounting: exclusive})
	if seeded.InputTokens != 400 || seeded.CacheReadTokens != 640 || seeded.UncachedInputTokens() != 360 {
		t.Fatalf("pre-populated usage changed: %+v", seeded)
	}
	for _, tc := range []struct {
		name  string
		usage []aipapi.Event
		want  int
	}{
		{"inclusive", []aipapi.Event{{InputTokens: 900, CacheReadTokens: 600}}, 300},
		{"exclusive", []aipapi.Event{{InputTokens: 300, CacheReadTokens: 600, Accounting: exclusive}}, 300},
		{"mixed", []aipapi.Event{{InputTokens: 300, CacheReadTokens: 600, Accounting: exclusive}, {InputTokens: 900, CacheReadTokens: 600}}, 600},
		{"scoped", []aipapi.Event{{InputTokens: 1, Accounting: exclusive, UsageScopes: []aipapi.ScopedUsageDelta{{InputTokens: 900, CacheReadTokens: 600, Accounting: aipapi.UsageAccountingMetadata{Plane: aipapi.UsagePlaneClientVisible}}}}}, 300},
		{"scoped exclusive", []aipapi.Event{{UsageScopes: []aipapi.ScopedUsageDelta{{InputTokens: 300, CacheReadTokens: 600, Accounting: aipapi.UsageAccountingMetadata{Plane: aipapi.UsagePlaneClientVisible, InputTokensExcludeCacheRead: true}}}}}, 300},
		{"underflow", []aipapi.Event{{InputTokens: 1, CacheReadTokens: 2}}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			events := []aipapi.Event{{Kind: aipapi.EventResponseStarted}}
			for _, ev := range tc.usage {
				ev.Kind = aipapi.EventUsageDelta
				events = append(events, ev)
			}
			events = append(events, aipapi.Event{Kind: aipapi.EventResponseFinished})
			col, err := aipapi.Collect(t.Context(), aipapi.NewFixedEventStream(events))
			if err != nil {
				t.Fatal(err)
			}
			if got := col.UncachedInputTokens(); got != tc.want {
				t.Fatalf("uncached=%d want %d", got, tc.want)
			}
			if got := aipapi.CloneCollected(&col).UncachedInputTokens(); got != tc.want {
				t.Fatalf("clone uncached=%d want %d", got, tc.want)
			}
		})
	}
}

// TestCollectedAccumulateUsageSelectsOneClientVisiblePerspective proves that
// one turn reports one client usage perspective: the first explicit
// client-visible usage replaces the fallback accumulation, later fallback usage
// is ignored, and explicit client-visible deltas accumulate.
func TestCollectedAccumulateUsageSelectsOneClientVisiblePerspective(t *testing.T) {
	t.Parallel()

	providerPlane := aipapi.UsageAccountingMetadata{Plane: aipapi.UsagePlaneProviderBillable}
	clientPlane := aipapi.UsageAccountingMetadata{Plane: aipapi.UsagePlaneClientVisible}
	readExclusive := aipapi.UsageAccountingMetadata{InputTokensExcludeCacheRead: true}

	type expectation struct {
		input, output, cacheRead, cacheWrite, reasoning, total int
		uncached                                               int
		cost                                                   int64
		currency, costSource                                   string
	}

	for _, tc := range []struct {
		name   string
		events []aipapi.Event
		want   expectation
	}{
		{
			name: "fallback only keeps unscoped usage",
			events: []aipapi.Event{
				{InputTokens: 10, OutputTokens: 5, CacheReadTokens: 2, TotalTokens: 15, Accounting: providerPlane},
			},
			want: expectation{input: 10, output: 5, cacheRead: 2, total: 15, uncached: 8},
		},
		{
			name: "first explicit selection replaces fallback counters",
			events: []aipapi.Event{
				{InputTokens: 100, OutputTokens: 50, CacheReadTokens: 10, TotalTokens: 150, Accounting: providerPlane},
				{UsageScopes: []aipapi.ScopedUsageDelta{{InputTokens: 7, OutputTokens: 3, TotalTokens: 10, Accounting: clientPlane}}},
			},
			want: expectation{input: 7, output: 3, total: 10, uncached: 7},
		},
		{
			name: "scoped client payload beats the event shell",
			events: []aipapi.Event{
				{
					InputTokens: 100, OutputTokens: 50, CacheReadTokens: 10, TotalTokens: 150, Accounting: providerPlane,
					UsageScopes: []aipapi.ScopedUsageDelta{{InputTokens: 7, OutputTokens: 3, TotalTokens: 10, Accounting: clientPlane}},
				},
			},
			want: expectation{input: 7, output: 3, total: 10, uncached: 7},
		},
		{
			name: "explicit zero still selects the client perspective",
			events: []aipapi.Event{
				{InputTokens: 100, OutputTokens: 50, Accounting: providerPlane},
				{UsageScopes: []aipapi.ScopedUsageDelta{{Accounting: clientPlane}}},
			},
			want: expectation{},
		},
		{
			name: "later fallback usage is ignored",
			events: []aipapi.Event{
				{UsageScopes: []aipapi.ScopedUsageDelta{{InputTokens: 7, OutputTokens: 3, Accounting: clientPlane}}},
				{InputTokens: 100, OutputTokens: 50, CacheReadTokens: 10, Accounting: providerPlane},
			},
			want: expectation{input: 7, output: 3, uncached: 7},
		},
		{
			name: "subsequent client deltas accumulate",
			events: []aipapi.Event{
				{UsageScopes: []aipapi.ScopedUsageDelta{{InputTokens: 5, OutputTokens: 1, Accounting: clientPlane}}},
				{UsageScopes: []aipapi.ScopedUsageDelta{{InputTokens: 3, OutputTokens: 2, Accounting: clientPlane}}},
			},
			want: expectation{input: 8, output: 3, uncached: 8},
		},
		{
			name: "every client visible scope contributes",
			events: []aipapi.Event{
				{
					InputTokens: 999, OutputTokens: 999, Accounting: providerPlane,
					UsageScopes: []aipapi.ScopedUsageDelta{
						{InputTokens: 10, OutputTokens: 1, Accounting: clientPlane},
						{InputTokens: 5, OutputTokens: 2, Accounting: clientPlane},
						{InputTokens: 777, OutputTokens: 777, Accounting: providerPlane},
					},
				},
			},
			want: expectation{input: 15, output: 3, uncached: 15},
		},
		{
			name: "top level client usage without scopes selects",
			events: []aipapi.Event{
				{InputTokens: 4, OutputTokens: 2, Accounting: clientPlane},
			},
			want: expectation{input: 4, output: 2, uncached: 4},
		},
		{
			name: "provider scope without a client scope keeps the legacy fallback",
			events: []aipapi.Event{
				{
					InputTokens: 20, OutputTokens: 6, Accounting: providerPlane,
					UsageScopes: []aipapi.ScopedUsageDelta{{InputTokens: 999, OutputTokens: 999, Accounting: providerPlane}},
				},
			},
			want: expectation{input: 20, output: 6, uncached: 20},
		},
		{
			name: "explicit selection resets exclusion corrections",
			events: []aipapi.Event{
				{InputTokens: 300, CacheReadTokens: 600, Accounting: readExclusive},
				{UsageScopes: []aipapi.ScopedUsageDelta{{InputTokens: 100, CacheReadTokens: 50, Accounting: clientPlane}}},
			},
			want: expectation{input: 100, cacheRead: 50, uncached: 50},
		},
		{
			name: "explicit selection replaces usage cost metadata",
			events: []aipapi.Event{
				{
					InputTokens: 10, CostNanoUnits: 100, Currency: "USD", CostSource: "estimated", Accounting: providerPlane,
				},
				{
					InputTokens: 5, CostNanoUnits: 7, Currency: "EUR", CostSource: "provider_reported",
					UsageScopes: []aipapi.ScopedUsageDelta{{InputTokens: 5, Accounting: clientPlane}},
				},
			},
			want: expectation{input: 5, uncached: 5, cost: 7, currency: "EUR", costSource: "provider_reported"},
		},
		{
			name: "selected perspective owns cache write and reasoning counters",
			events: []aipapi.Event{
				{
					InputTokens: 10, OutputTokens: 5, CacheWriteTokens: 4, ReasoningTokens: 2, Accounting: providerPlane,
				},
				{
					UsageScopes: []aipapi.ScopedUsageDelta{{
						InputTokens: 7, OutputTokens: 3, CacheWriteTokens: 1, ReasoningTokens: 2, Accounting: clientPlane,
					}},
				},
			},
			want: expectation{input: 7, output: 3, cacheWrite: 1, reasoning: 2, uncached: 7},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var col aipapi.Collected
			for _, ev := range tc.events {
				col.AccumulateUsage(ev)
			}
			if col.InputTokens != tc.want.input || col.OutputTokens != tc.want.output ||
				col.CacheReadTokens != tc.want.cacheRead || col.CacheWriteTokens != tc.want.cacheWrite ||
				col.ReasoningTokens != tc.want.reasoning || col.TotalTokens != tc.want.total {
				t.Fatalf("usage = %+v want %+v", col, tc.want)
			}
			if got := col.UncachedInputTokens(); got != tc.want.uncached {
				t.Fatalf("uncached = %d want %d", got, tc.want.uncached)
			}
			if col.CostNanoUnits != tc.want.cost || col.Currency != tc.want.currency || col.CostSource != tc.want.costSource {
				t.Fatalf("cost = %d %q %q want %d %q %q",
					col.CostNanoUnits, col.Currency, col.CostSource,
					tc.want.cost, tc.want.currency, tc.want.costSource)
			}
		})
	}
}

// TestCollected_CacheInclusionDeclarationsAreIndependent proves that the
// cache-read and the cache-write inclusion declarations are independent: a
// producer that declares only the cache-read exclusion adds only its excluded
// reads to the inclusive prompt count, and cache writes the producer already
// includes in its input are never added a second time.
func TestCollected_CacheInclusionDeclarationsAreIndependent(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		accounting aipapi.UsageAccountingMetadata
		input      int
		wantPrompt int
	}{
		{"fully inclusive", aipapi.UsageAccountingMetadata{}, 400, 400},
		{"read exclusive", aipapi.UsageAccountingMetadata{InputTokensExcludeCacheRead: true}, 400, 1000},
		{"both exclusive", aipapi.UsageAccountingMetadata{InputTokensExcludeCacheRead: true, InputTokensExcludeCacheWrite: true}, 300, 1000},
		{"write only exclusive", aipapi.UsageAccountingMetadata{InputTokensExcludeCacheWrite: true}, 400, 500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var col aipapi.Collected
			col.AccumulateUsage(aipapi.Event{
				InputTokens:      tc.input,
				CacheReadTokens:  600,
				CacheWriteTokens: 100,
				Accounting:       tc.accounting,
			})
			if got := col.InclusiveInputTokens(); got != tc.wantPrompt {
				t.Fatalf("prompt=%d want %d", got, tc.wantPrompt)
			}
			if got := aipapi.CloneCollected(&col).InclusiveInputTokens(); got != tc.wantPrompt {
				t.Fatalf("clone prompt=%d want %d", got, tc.wantPrompt)
			}
		})
	}
}

// TestCollected_PerDeltaCacheInclusionDeclarations proves that each usage
// delta contributes its own cache inclusion declarations: a delta that
// declares only the cache-read exclusion adds only its excluded reads, and a
// delta that declares only the cache-write exclusion adds only its excluded
// writes. The selected client perspective accumulates its deltas with the
// same per-delta declarations.
func TestCollected_PerDeltaCacheInclusionDeclarations(t *testing.T) {
	t.Parallel()
	readExclusive := aipapi.UsageAccountingMetadata{InputTokensExcludeCacheRead: true}
	writeExclusive := aipapi.UsageAccountingMetadata{InputTokensExcludeCacheWrite: true}
	clientPlane := aipapi.UsagePlaneClientVisible

	for _, tc := range []struct {
		name   string
		events []aipapi.Event
	}{
		{
			name: "fallback perspective",
			events: []aipapi.Event{
				{InputTokens: 400, CacheReadTokens: 600, CacheWriteTokens: 100, Accounting: readExclusive},
				{InputTokens: 10, CacheReadTokens: 20, CacheWriteTokens: 30, Accounting: writeExclusive},
			},
		},
		{
			name: "selected client perspective",
			events: []aipapi.Event{
				{UsageScopes: []aipapi.ScopedUsageDelta{{
					InputTokens: 400, CacheReadTokens: 600, CacheWriteTokens: 100,
					Accounting: aipapi.UsageAccountingMetadata{Plane: clientPlane, InputTokensExcludeCacheRead: true},
				}}},
				{UsageScopes: []aipapi.ScopedUsageDelta{{
					InputTokens: 10, CacheReadTokens: 20, CacheWriteTokens: 30,
					Accounting: aipapi.UsageAccountingMetadata{Plane: clientPlane, InputTokensExcludeCacheWrite: true},
				}}},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var col aipapi.Collected
			for _, ev := range tc.events {
				col.AccumulateUsage(ev)
			}
			// 410 reported input + 600 excluded reads + 30 excluded writes; the
			// 100 cache writes of the first delta are already inside its input.
			if got := col.InclusiveInputTokens(); got != 1040 {
				t.Fatalf("prompt=%d want 1040", got)
			}
		})
	}
}

// TestCollected_ExplicitSelectionResetsBothExclusionCorrections proves that
// the first explicit client-visible selection clears the read and the write
// exclusion corrections accumulated from the fallback perspective.
func TestCollected_ExplicitSelectionResetsBothExclusionCorrections(t *testing.T) {
	t.Parallel()
	var col aipapi.Collected
	col.AccumulateUsage(aipapi.Event{
		InputTokens:      300,
		CacheReadTokens:  600,
		CacheWriteTokens: 100,
		Accounting: aipapi.UsageAccountingMetadata{
			InputTokensExcludeCacheRead:  true,
			InputTokensExcludeCacheWrite: true,
		},
	})
	col.AccumulateUsage(aipapi.Event{
		UsageScopes: []aipapi.ScopedUsageDelta{{
			InputTokens:      10,
			CacheReadTokens:  5,
			CacheWriteTokens: 2,
			Accounting:       aipapi.UsageAccountingMetadata{Plane: aipapi.UsagePlaneClientVisible},
		}},
	})
	if got := col.InclusiveInputTokens(); got != 10 {
		t.Fatalf("prompt=%d want 10", got)
	}
}

// A producer that declares exclusive input semantics reports its input
// without the cache reads and cache writes it also reports. Frontends that
// count the prompt inclusively need those subsets inside the prompt.
//
// The fixture declares both exclusions, because the reported input sits
// outside the reported cache reads and the reported cache writes. The
// inclusive-input projection is the only total rule the canonical package
// owns: the per-family wire totals belong to the frontend encoders.
func TestCollected_InclusiveInputSemantics(t *testing.T) {
	t.Parallel()
	fullyExclusive := aipapi.UsageAccountingMetadata{
		InputTokensExcludeCacheRead:  true,
		InputTokensExcludeCacheWrite: true,
	}
	for _, tc := range []struct {
		name       string
		usage      []aipapi.Event
		wantPrompt int
	}{
		{"inclusive", []aipapi.Event{{InputTokens: 900, CacheReadTokens: 600, OutputTokens: 20}}, 900},
		{"exclusive", []aipapi.Event{{InputTokens: 300, CacheReadTokens: 600, CacheWriteTokens: 100, OutputTokens: 20, Accounting: fullyExclusive}}, 1000},
		{"exclusive provider total", []aipapi.Event{{InputTokens: 300, CacheReadTokens: 600, CacheWriteTokens: 100, OutputTokens: 20, TotalTokens: 320, Accounting: fullyExclusive}}, 1000},
		{"inclusive provider total", []aipapi.Event{{InputTokens: 900, CacheReadTokens: 600, OutputTokens: 20, TotalTokens: 920}}, 900},
		{"provider total only", []aipapi.Event{{TotalTokens: 500}}, 0},
		{"provider total larger", []aipapi.Event{{InputTokens: 900, OutputTokens: 20, TotalTokens: 1000}}, 900},
	} {
		t.Run(tc.name, func(t *testing.T) {
			events := []aipapi.Event{{Kind: aipapi.EventResponseStarted}}
			for _, ev := range tc.usage {
				ev.Kind = aipapi.EventUsageDelta
				events = append(events, ev)
			}
			events = append(events, aipapi.Event{Kind: aipapi.EventResponseFinished})
			col, err := aipapi.Collect(t.Context(), aipapi.NewFixedEventStream(events))
			if err != nil {
				t.Fatal(err)
			}
			prompt := col.InclusiveInputTokens()
			if prompt != tc.wantPrompt {
				t.Fatalf("prompt=%d want %d", prompt, tc.wantPrompt)
			}
			clone := aipapi.CloneCollected(&col)
			if clone.InclusiveInputTokens() != tc.wantPrompt {
				t.Fatalf("clone prompt=%d want %d", clone.InclusiveInputTokens(), tc.wantPrompt)
			}
		})
	}
}

// TestClientVisibleUsageKeepsEventLocalSemantics proves that the exported
// accessor stays an event-local compatibility accessor: it returns the first
// client-visible scope of the event and otherwise falls back to the event's
// own totals. Turn-wide precedence and multi-scope accumulation stay private
// to the collector.
func TestClientVisibleUsageKeepsEventLocalSemantics(t *testing.T) {
	t.Parallel()

	clientPlane := aipapi.UsageAccountingMetadata{Plane: aipapi.UsagePlaneClientVisible}

	scoped := aipapi.ClientVisibleUsage(aipapi.Event{
		InputTokens: 999,
		UsageScopes: []aipapi.ScopedUsageDelta{
			{InputTokens: 777, Accounting: aipapi.UsageAccountingMetadata{Plane: aipapi.UsagePlaneProviderBillable}},
			{InputTokens: 10, OutputTokens: 1, Accounting: clientPlane},
			{InputTokens: 5, OutputTokens: 2, Accounting: clientPlane},
		},
	})
	if scoped.InputTokens != 10 || scoped.OutputTokens != 1 {
		t.Fatalf("scoped usage = %+v want the first client-visible scope", scoped)
	}

	fallback := aipapi.ClientVisibleUsage(aipapi.Event{InputTokens: 42, OutputTokens: 7})
	if fallback.InputTokens != 42 || fallback.OutputTokens != 7 {
		t.Fatalf("fallback usage = %+v want the event totals", fallback)
	}
}

// TestCollectedAccumulateUsageSelectionPreservesContentAndEvents proves that
// selecting the client perspective preserves the collected content and never
// mutates the incoming events.
func TestCollectedAccumulateUsageSelectionPreservesContentAndEvents(t *testing.T) {
	t.Parallel()

	clientPlane := aipapi.UsageAccountingMetadata{Plane: aipapi.UsagePlaneClientVisible}
	fallback := aipapi.Event{
		Kind:            aipapi.EventUsageDelta,
		InputTokens:     100,
		OutputTokens:    50,
		CacheReadTokens: 10,
		Accounting:      aipapi.UsageAccountingMetadata{Plane: aipapi.UsagePlaneProviderBillable},
	}
	explicit := aipapi.Event{
		Kind: aipapi.EventUsageDelta,
		UsageScopes: []aipapi.ScopedUsageDelta{{
			InputTokens: 7, OutputTokens: 3, TotalTokens: 10, Accounting: clientPlane,
		}},
	}
	fallbackBefore := fallback.Clone()
	explicitBefore := explicit.Clone()

	events := []aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventTextDelta, Delta: "hello"},
		{Kind: aipapi.EventToolCallStarted, ToolCallID: "t1", ToolName: "search"},
		fallback,
		{Kind: aipapi.EventWarning, WarningMessage: "slow"},
		explicit,
		{Kind: aipapi.EventResponseFinished, FinishReason: "stop"},
	}
	col, err := aipapi.Collect(t.Context(), aipapi.NewFixedEventStream(events))
	if err != nil {
		t.Fatal(err)
	}
	if col.Text.String() != "hello" {
		t.Fatalf("text = %q want %q", col.Text.String(), "hello")
	}
	if len(col.ToolCallOrder) != 1 || col.ToolCallOrder[0] != "t1" || col.ToolNames["t1"] != "search" {
		t.Fatalf("tools = %+v", col.ToolCallOrder)
	}
	if len(col.Warnings) != 1 || col.Warnings[0] != "slow" {
		t.Fatalf("warnings = %+v", col.Warnings)
	}
	if !col.FinishReceived || col.FinishReason != "stop" {
		t.Fatalf("terminal = %v %q", col.FinishReceived, col.FinishReason)
	}
	if col.InputTokens != 7 || col.OutputTokens != 3 || col.TotalTokens != 10 || col.CacheReadTokens != 0 {
		t.Fatalf("selected usage = %+v", col)
	}
	if !reflect.DeepEqual(fallback, fallbackBefore) {
		t.Fatalf("fallback event mutated: %+v want %+v", fallback, fallbackBefore)
	}
	if !reflect.DeepEqual(explicit, explicitBefore) {
		t.Fatalf("explicit event mutated: %+v want %+v", explicit, explicitBefore)
	}
}

// TestCloneCollectedPreservesUsagePerspectiveSelection proves that a clone
// keeps the selected perspective, so later fallback usage stays ignored, and
// that a clone taken before the selection still replaces its fallback usage.
func TestCloneCollectedPreservesUsagePerspectiveSelection(t *testing.T) {
	t.Parallel()

	clientPlane := aipapi.UsageAccountingMetadata{Plane: aipapi.UsagePlaneClientVisible}
	fallback := aipapi.Event{
		InputTokens: 100, OutputTokens: 50,
		Accounting: aipapi.UsageAccountingMetadata{Plane: aipapi.UsagePlaneProviderBillable},
	}
	explicit := aipapi.Event{
		UsageScopes: []aipapi.ScopedUsageDelta{{InputTokens: 7, OutputTokens: 3, Accounting: clientPlane}},
	}

	var before aipapi.Collected
	before.AccumulateUsage(fallback)
	cloneBefore := aipapi.CloneCollected(&before)
	cloneBefore.AccumulateUsage(explicit)
	if cloneBefore.InputTokens != 7 || cloneBefore.OutputTokens != 3 {
		t.Fatalf("clone before selection = %+v want the client perspective", cloneBefore)
	}

	var after aipapi.Collected
	after.AccumulateUsage(explicit)
	cloneAfter := aipapi.CloneCollected(&after)
	cloneAfter.AccumulateUsage(fallback)
	if cloneAfter.InputTokens != 7 || cloneAfter.OutputTokens != 3 {
		t.Fatalf("clone after selection = %+v want the client perspective", cloneAfter)
	}
}

// TestCollectedDirectlyPopulatedKeepsLegacyFallbackSemantics proves that a
// directly populated collector keeps the legacy fallback interpretation until
// an explicit client perspective is selected.
func TestCollectedDirectlyPopulatedKeepsLegacyFallbackSemantics(t *testing.T) {
	t.Parallel()

	clientPlane := aipapi.UsageAccountingMetadata{Plane: aipapi.UsagePlaneClientVisible}
	seeded := aipapi.Collected{InputTokens: 100, CacheReadTokens: 40}
	if got := seeded.UncachedInputTokens(); got != 60 {
		t.Fatalf("uncached = %d want 60", got)
	}
	seeded.AccumulateUsage(aipapi.Event{InputTokens: 7, OutputTokens: 3, Accounting: clientPlane})
	if seeded.InputTokens != 7 || seeded.OutputTokens != 3 {
		t.Fatalf("seeded usage = %+v want the client perspective", seeded)
	}
}
