package metrics

import (
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	accountingobs "github.com/aiproxer/aiproxer/internal/core/tokenaccounting/observability"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestNewBundle_executorSink(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Observability: config.ObservabilityConfig{
			Metrics: config.MetricsConfig{ExemplarsEnabled: true},
		},
	}
	b := NewBundle(cfg, nil, nil)
	allPresent := b != nil &&
		b.Registry != nil &&
		b.HTTP != nil &&
		b.Executor != nil &&
		b.SecureSession != nil &&
		b.ExtensionStages != nil &&
		b.AuthorityStages != nil &&
		b.Upstream != nil &&
		b.PostgresPool != nil
	if !allPresent {
		t.Fatal("expected non-nil bundle components")
	}
	if b.ExtensionStageSink() == nil {
		t.Fatal("expected extension stage sink")
	}
	if b.AuthorityStageSink() == nil {
		t.Fatal("expected authority stage sink")
	}
	sink := b.ExecutorSink()
	if sink == nil {
		t.Fatal("expected sink")
	}
	sink.OnAttemptRecorded(aipapi.AttemptSuccess, "bedrock")
	sink.OnBackendOpenDuration("bedrock", 0.42)
	sink.OnTransportNegotiation(aipapi.OperationOpenAIChatCompletions, aipapi.TransportModeStreaming, "accept")
	sink.OnCancellation(runtime.CancellationObservation{
		CauseClass: runtime.CancellationCauseExplicit,
		Mode:       runtime.CancellationModeProvider,
		Phase:      runtime.CancellationPhaseOutcome,
		Fallback:   runtime.CancellationFallbackNegotiated,
	})
	// Bounded per-attempt skip counter (req 4.1): opted in via the optional
	// observer extension so existing sinks keep working unchanged.
	skipObs, ok := sink.(runtime.ReasoningReplaySkipObserver)
	if !ok {
		t.Fatal("executor sink must implement ReasoningReplaySkipObserver")
	}
	skipObs.OnReasoningReplaySkipped("bedrock")
	// Bounded per-turn empty-visible counter: same optional-observer pattern.
	emptyTurnObs, ok := sink.(runtime.EmptyVisibleTurnObserver)
	if !ok {
		t.Fatal("executor sink must implement EmptyVisibleTurnObserver")
	}
	emptyTurnObs.OnEmptyVisibleTurn("bedrock")
	// Bounded corrective-retry decision counter: same optional-observer pattern.
	retryObs, ok := sink.(runtime.EmptyVisibleTurnRetryObserver)
	if !ok {
		t.Fatal("executor sink must implement EmptyVisibleTurnRetryObserver")
	}
	retryObs.OnEmptyVisibleTurnRetry("bedrock", "corrected")
	mfs, err := b.Registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	var dump strings.Builder
	for _, mf := range mfs {
		dump.WriteString(mf.String())
	}
	for _, want := range []string{
		"aip_executor_transport_negotiations_total",
		"openai.chat_completions",
		"streaming",
		"accept",
		"aip_executor_cancellations_total",
		"explicit",
		"provider",
		"outcome",
		"negotiated",
		"aip_reasoning_replay_skipped_total",
		"aip_empty_visible_turn_total",
		"aip_empty_visible_turn_retry_total",
		"bedrock",
	} {
		if !strings.Contains(dump.String(), want) {
			t.Fatalf("metrics missing %q:\n%s", want, dump.String())
		}
	}
}

func TestTokenAccountingPromRecordsBoundedObservations(t *testing.T) {
	t.Parallel()
	b := NewBundle(&config.Config{}, nil, nil)
	sink := b.TokenAccountingObservabilitySink()
	if sink == nil {
		t.Fatal("TokenAccountingObservabilitySink returned nil")
	}
	obs, err := accountingobs.NewObservation(accountingobs.Input{
		Labels: accountingobs.Labels{
			Backend:   "openai",
			Model:     "gpt-4o-mini",
			Plane:     accountingobs.PlaneClientVisible,
			Source:    accountingobs.SourceLocalTokenizer,
			Authority: accountingobs.AuthorityEstimated,
		},
		Status:            accountingobs.StatusUnavailable,
		UnavailableReason: "provider secret raw prompt unsupported",
		Duration:          25 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}

	sink.Record(obs)
	mfs, err := b.Registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	var dump strings.Builder
	for _, mf := range mfs {
		dump.WriteString(mf.String())
	}
	s := dump.String()
	for _, want := range []string{"aip_token_accounting_observations_total", "aip_token_accounting_observation_seconds", "client_visible", "local_tokenizer", "estimated", "unavailable", "openai", "specified"} {
		if !strings.Contains(s, want) {
			t.Fatalf("metrics missing %q:\n%s", want, s)
		}
	}
	for _, forbidden := range []string{"raw prompt", "secret", "gpt-4o-mini"} {
		if strings.Contains(s, forbidden) {
			t.Fatalf("metrics leaked %q:\n%s", forbidden, s)
		}
	}
}
