package streamusage

import (
	"context"
	"errors"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/tokenaccounting/app"
	"github.com/aiproxer/aiproxer/internal/core/tokenaccounting/domain"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestReconstructor_preservesProviderBillableAndAddsClientVisibleOutput(t *testing.T) {
	t.Parallel()

	provider := scopedUsage(11, 7, aipapi.UsagePlaneProviderBillable, aipapi.UsageSourceProviderReported, aipapi.UsageAuthorityAuthoritative)
	counter := &fakeCounter{
		callResult:   app.CountResult{InputTokens: 10, TotalTokens: 10, Accounting: localAccounting()},
		outputResult: app.CountResult{OutputTokens: 4, TotalTokens: 4, Accounting: localAccounting()},
	}
	reconstructor := New(counter, Config{Policy: domain.PolicyProviderThenClient})

	result := reconstructForTest(t, reconstructor, Input{
		Backend:    "openai",
		Model:      "gpt-test",
		Call:       testCall(),
		OutputText: "visible output",
		Events: []aipapi.Event{
			{Kind: aipapi.EventUsageDelta, UsageScopes: []aipapi.ScopedUsageDelta{provider}},
		},
	})

	if len(result.Events) != 2 {
		t.Fatalf("len(result.Events) = %d, want 2", len(result.Events))
	}
	if got, ok := result.Reconciled.UsageForPlane(aipapi.UsagePlaneProviderBillable); !ok || got.InputTokens != 11 || got.OutputTokens != 7 {
		t.Fatalf("provider_billable usage = %+v, %v; want preserved provider usage", got, ok)
	}
	if got, ok := result.Reconciled.UsageForPlane(aipapi.UsagePlaneClientVisible); !ok || got.InputTokens != 10 || got.OutputTokens != 4 {
		t.Fatalf("client_visible usage = %+v, %v; want reconstructed 10/4", got, ok)
	}
	if result.Reconciled.BillablePlane != aipapi.UsagePlaneProviderBillable {
		t.Fatalf("billable plane = %q, want provider_billable", result.Reconciled.BillablePlane)
	}
}

func TestReconstructor_missingProviderUsageReconstructsClientVisibleInputAndOutput(t *testing.T) {
	t.Parallel()

	counter := &fakeCounter{
		callResult:   app.CountResult{InputTokens: 5, TotalTokens: 5, Accounting: localAccounting()},
		outputResult: app.CountResult{OutputTokens: 3, TotalTokens: 3, Accounting: localAccounting()},
	}
	reconstructor := New(counter, Config{Policy: domain.PolicyBillClientVisible})

	result := reconstructForTest(t, reconstructor, Input{
		Backend:    "local",
		Model:      "test-model",
		Call:       testCall(),
		OutputText: "abc",
	})

	if len(result.Events) != 1 {
		t.Fatalf("len(result.Events) = %d, want 1", len(result.Events))
	}
	usage, ok := result.Reconciled.UsageForPlane(aipapi.UsagePlaneClientVisible)
	if !ok {
		t.Fatal("missing client_visible usage")
	}
	if usage.InputTokens != 5 || usage.OutputTokens != 3 || usage.TotalTokens != 8 {
		t.Fatalf("client_visible usage = %+v, want input=5 output=3 total=8", usage)
	}
	if _, ok := result.Reconciled.UsageForPlane(aipapi.UsagePlaneProviderBillable); ok {
		t.Fatal("unexpected provider_billable usage from local reconstruction")
	}
}

func TestReconstructor_doesNotPromoteUnmarkedLegacyUsageDelta(t *testing.T) {
	t.Parallel()

	counter := &fakeCounter{
		callResult:   app.CountResult{InputTokens: 5, TotalTokens: 5, Accounting: localAccounting()},
		outputResult: app.CountResult{OutputTokens: 3, TotalTokens: 3, Accounting: localAccounting()},
	}
	reconstructor := New(counter, Config{Policy: domain.PolicyProviderThenClient})

	result := reconstructForTest(t, reconstructor, Input{
		Model:      "test-model",
		Call:       testCall(),
		OutputText: "abc",
		Events: []aipapi.Event{{
			Kind:         aipapi.EventUsageDelta,
			InputTokens:  11,
			OutputTokens: 7,
			TotalTokens:  18,
		}},
	})

	usage, ok := result.Reconciled.UsageForPlane(aipapi.UsagePlaneProviderBillable)
	if ok {
		t.Fatalf("unmarked usage must not become provider_billable: %#v", usage)
	}
	usage, ok = result.Reconciled.UsageForPlane(aipapi.UsagePlaneClientVisible)
	if !ok || usage.InputTokens != 5 || usage.OutputTokens != 3 || usage.TotalTokens != 8 {
		t.Fatalf("local fallback usage = %+v, want client-visible tokenizer totals 5/3/8", usage)
	}
	if usage.Accounting.Source != aipapi.UsageSourceLocalTokenizer || usage.Accounting.Authority != aipapi.UsageAuthorityEstimated {
		t.Fatalf("local accounting = %+v, want local_tokenizer/estimated", usage.Accounting)
	}
	if len(result.Reconciled.Warnings) == 0 {
		t.Fatal("unmarked legacy usage must surface unknown metadata warnings")
	}
}

func TestReconstructor_outputCountFailureWarnsUnavailableAndPreservesProviderUsage(t *testing.T) {
	t.Parallel()

	provider := scopedUsage(8, 2, aipapi.UsagePlaneProviderBillable, aipapi.UsageSourceProviderReported, aipapi.UsageAuthorityAuthoritative)
	counter := &fakeCounter{
		callResult: app.CountResult{InputTokens: 8, TotalTokens: 8, Accounting: localAccounting()},
		outputErr:  errors.New("tokenizer unavailable"),
	}
	reconstructor := New(counter, Config{Policy: domain.PolicyProviderThenClient})

	result, err := reconstructor.Reconstruct(context.Background(), Input{
		Backend:    "openai",
		Model:      "gpt-test",
		Call:       testCall(),
		OutputText: "will fail",
		Events: []aipapi.Event{
			{Kind: aipapi.EventUsageDelta, UsageScopes: []aipapi.ScopedUsageDelta{provider}},
		},
	})
	if err == nil {
		t.Fatal("Reconstruct error = nil, want count failure")
	}
	if len(result.Warnings) != 1 || result.Warnings[0].Code != WarningOutputCountUnavailable {
		t.Fatalf("warnings = %+v, want output unavailable warning", result.Warnings)
	}
	if got, ok := result.Reconciled.UsageForPlane(aipapi.UsagePlaneProviderBillable); !ok || got.InputTokens != 8 || got.OutputTokens != 2 {
		t.Fatalf("provider_billable usage = %+v, %v; want preserved provider usage", got, ok)
	}
	if got, ok := result.Reconciled.UsageForPlane(aipapi.UsagePlaneClientVisible); !ok || got.InputTokens != 8 || got.OutputTokens != 0 {
		t.Fatalf("client_visible usage = %+v, %v; want partial input-only usage", got, ok)
	}
}

func TestReconstructor_inputCountFailurePreservesProviderReconciliation(t *testing.T) {
	t.Parallel()

	provider := scopedUsage(9, 4, aipapi.UsagePlaneProviderBillable, aipapi.UsageSourceProviderReported, aipapi.UsageAuthorityAuthoritative)
	counter := &fakeCounter{callErr: errors.New("input tokenizer failed")}
	reconstructor := New(counter, Config{Policy: domain.PolicyProviderThenClient})

	result, err := reconstructor.Reconstruct(context.Background(), Input{
		Model:      "test-model",
		Call:       testCall(),
		OutputText: "ignored",
		Events:     []aipapi.Event{{Kind: aipapi.EventUsageDelta, UsageScopes: []aipapi.ScopedUsageDelta{provider}}},
	})
	if err == nil {
		t.Fatal("Reconstruct error = nil, want input count failure")
	}
	if len(result.Warnings) != 1 || result.Warnings[0].Code != WarningInputCountUnavailable {
		t.Fatalf("warnings = %+v, want input unavailable warning", result.Warnings)
	}
	if got, ok := result.Reconciled.UsageForPlane(aipapi.UsagePlaneProviderBillable); !ok || got.InputTokens != 9 || got.OutputTokens != 4 {
		t.Fatalf("provider_billable usage = %+v, %v; want preserved provider usage", got, ok)
	}
	if result.Reconciled.BillablePlane != aipapi.UsagePlaneProviderBillable {
		t.Fatalf("billable plane = %q, want provider_billable", result.Reconciled.BillablePlane)
	}
}

func TestReconstructor_nonUsageEventsIgnoredForProviderUsageButPassedToOutputCounter(t *testing.T) {
	t.Parallel()

	events := []aipapi.Event{
		{Kind: aipapi.EventTextDelta, Delta: "hello"},
		{Kind: aipapi.EventWarning, WarningCode: "provider_notice", WarningMessage: "ignored for usage"},
	}
	counter := &fakeCounter{
		callResult:   app.CountResult{InputTokens: 2, TotalTokens: 2, Accounting: localAccounting()},
		outputResult: app.CountResult{OutputTokens: 1, TotalTokens: 1, Accounting: localAccounting()},
	}
	reconstructor := New(counter, Config{Policy: domain.PolicyBillClientVisible})

	result := reconstructForTest(t, reconstructor, Input{Model: "test-model", Call: testCall(), OutputText: "hello", Events: events})

	if counter.outputInput.Text != "hello" {
		t.Fatalf("output count text = %q, want %q", counter.outputInput.Text, "hello")
	}
	if len(counter.outputInput.Events) != len(events) || counter.outputInput.Events[0].Kind != aipapi.EventTextDelta {
		t.Fatalf("output count events = %+v, want original non-usage events", counter.outputInput.Events)
	}
	if _, ok := result.Reconciled.UsageForPlane(aipapi.UsagePlaneProviderBillable); ok {
		t.Fatal("non-usage events produced provider_billable usage")
	}
}

func TestReconstructor_excludesUsageDeltaEventsFromOutputCounter(t *testing.T) {
	t.Parallel()

	provider := scopedUsage(11, 7, aipapi.UsagePlaneProviderBillable, aipapi.UsageSourceProviderReported, aipapi.UsageAuthorityAuthoritative)
	counter := &fakeCounter{
		callResult:   app.CountResult{InputTokens: 5, TotalTokens: 5, Accounting: localAccounting()},
		outputResult: app.CountResult{OutputTokens: 3, TotalTokens: 3, Accounting: localAccounting()},
	}
	reconstructor := New(counter, Config{Policy: domain.PolicyProviderThenClient})

	_ = reconstructForTest(t, reconstructor, Input{
		Model:      "test-model",
		Call:       testCall(),
		OutputText: "abc",
		Events: []aipapi.Event{
			{Kind: aipapi.EventTextDelta, Delta: "abc"},
			{Kind: aipapi.EventUsageDelta, UsageScopes: []aipapi.ScopedUsageDelta{provider}},
			{Kind: aipapi.EventResponseFinished, FinishReason: "stop"},
		},
	})

	if len(counter.outputInput.Events) != 2 {
		t.Fatalf("len(output events) = %d, want 2 non-usage events", len(counter.outputInput.Events))
	}
	for _, ev := range counter.outputInput.Events {
		if ev.Kind == aipapi.EventUsageDelta {
			t.Fatalf("output counter received usage_delta event: %+v", ev)
		}
	}
}

func TestReconstructor_reconciliationDetectsTransformedUsageMismatch(t *testing.T) {
	t.Parallel()

	provider := scopedUsage(6, 10, aipapi.UsagePlaneProviderBillable, aipapi.UsageSourceProviderReported, aipapi.UsageAuthorityAuthoritative)
	counter := &fakeCounter{
		callResult:   app.CountResult{InputTokens: 6, TotalTokens: 6, Accounting: localAccounting()},
		outputResult: app.CountResult{OutputTokens: 4, TotalTokens: 4, Accounting: localAccounting()},
	}
	reconstructor := New(counter, Config{Policy: domain.PolicyRequireSamePlaneExactness})

	result := reconstructForTest(t, reconstructor, Input{
		Model:      "test-model",
		Call:       testCall(),
		OutputText: "shorter transformed text",
		Events:     []aipapi.Event{{Kind: aipapi.EventUsageDelta, UsageScopes: []aipapi.ScopedUsageDelta{provider}}},
	})

	if len(result.Reconciled.Conflicts) != 1 || result.Reconciled.Conflicts[0].Code != domain.ConflictTransformedUsage {
		t.Fatalf("conflicts = %+v, want transformed usage conflict", result.Reconciled.Conflicts)
	}
	if result.Reconciled.Complete {
		t.Fatal("reconciled result complete = true, want false under exactness policy")
	}
}

func TestReconstructor_contextCancellationReturnsWarningAndError(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	reconstructor := New(&fakeCounter{}, Config{Policy: domain.PolicyBillClientVisible})

	result, err := reconstructor.Reconstruct(ctx, Input{Model: "test-model", Call: testCall(), OutputText: "ignored"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Reconstruct error = %v, want context.Canceled", err)
	}
	if len(result.Warnings) != 1 || result.Warnings[0].Code != WarningContextCanceled {
		t.Fatalf("warnings = %+v, want context canceled warning", result.Warnings)
	}
}

func TestReconstructor_usageEventCloneDetachesItemPayload(t *testing.T) {
	t.Parallel()

	provider := scopedUsage(11, 7, aipapi.UsagePlaneProviderBillable, aipapi.UsageSourceProviderReported, aipapi.UsageAuthorityAuthoritative)
	usageEvent := aipapi.Event{
		Kind:        aipapi.EventUsageDelta,
		UsageScopes: []aipapi.ScopedUsageDelta{provider},
		Item: &aipapi.Item{
			Kind: aipapi.ItemKindMessage, Role: aipapi.RoleUser, Status: aipapi.ItemStatusCompleted,
			Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "original-item-text"}},
		},
	}
	counter := &fakeCounter{
		callResult:   app.CountResult{InputTokens: 10, TotalTokens: 10, Accounting: localAccounting()},
		outputResult: app.CountResult{OutputTokens: 4, TotalTokens: 4, Accounting: localAccounting()},
	}
	reconstructor := New(counter, Config{Policy: domain.PolicyProviderThenClient})

	result, err := reconstructor.Reconstruct(context.Background(), Input{
		Backend:    "openai",
		Model:      "gpt-test",
		Call:       testCall(),
		OutputText: "visible output",
		Events:     []aipapi.Event{usageEvent},
	})
	if err != nil {
		t.Fatalf("Reconstruct error = %v", err)
	}
	usageEvent.Item.Content[0].Text = "mutated"
	if len(result.Events) == 0 || result.Events[0].Item == nil {
		t.Fatalf("reconstructed usage event lost item payload: %+v", result.Events)
	}
	if got := result.Events[0].Item.Content[0].Text; got != "original-item-text" {
		t.Fatalf("reconstructed item text=%q, want original", got)
	}
}

func reconstructForTest(t *testing.T, reconstructor *Reconstructor, input Input) Result {
	t.Helper()
	result, err := reconstructor.Reconstruct(context.Background(), input)
	if err != nil {
		t.Fatalf("Reconstruct error = %v", err)
	}
	return result
}

type fakeCounter struct {
	callResult   app.CountResult
	callErr      error
	outputResult app.CountResult
	outputErr    error
	outputInput  app.CountOutputInput
}

func (f *fakeCounter) CountCall(ctx context.Context, input app.CountCallInput) (app.CountResult, error) {
	if err := ctx.Err(); err != nil {
		return app.CountResult{}, err
	}
	return f.callResult, f.callErr
}

func (f *fakeCounter) CountOutput(ctx context.Context, input app.CountOutputInput) (app.CountResult, error) {
	f.outputInput = input
	if err := ctx.Err(); err != nil {
		return app.CountResult{}, err
	}
	return f.outputResult, f.outputErr
}

func testCall() aipapi.Call {
	return aipapi.Call{
		ID: "call-test",
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hello")},
		}},
	}
}

func scopedUsage(input, output int, plane aipapi.UsagePlane, source aipapi.UsageSource, authority aipapi.UsageAuthority) aipapi.ScopedUsageDelta {
	return aipapi.ScopedUsageDelta{
		InputTokens:  input,
		OutputTokens: output,
		TotalTokens:  input + output,
		Accounting: aipapi.UsageAccountingMetadata{
			Plane:     plane,
			Source:    source,
			Authority: authority,
		},
	}
}

func localAccounting() aipapi.UsageAccountingMetadata {
	return aipapi.UsageAccountingMetadata{Source: aipapi.UsageSourceLocalTokenizer, Authority: aipapi.UsageAuthorityEstimated}
}
