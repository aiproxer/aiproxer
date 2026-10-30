package extensions_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestCanonicalStageMetricLabels_allowsFixedOutcomes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		stage, outcome string
	}{
		{extensions.MetricsStageCandidateAttemptTransform, extensions.StageOutcomeOK},
		{extensions.MetricsStageCandidateAttemptTransform, extensions.StageOutcomeError},
		{extensions.MetricsStageCandidateAttemptTransform, extensions.StageOutcomeFailOpen},
		{extensions.MetricsStageCandidateAttemptTransform, extensions.StageOutcomeExcluded},
		{extensions.MetricsStageFinalStreamObservation, extensions.StageOutcomeOK},
		{extensions.MetricsStageFinalStreamObservation, extensions.StageOutcomeError},
		{extensions.StageSessionOpen, extensions.StageOutcomeOK},
		{extensions.StageToolEventReaction, extensions.StageOutcomeFailOpen},
	}
	for _, tc := range cases {
		stage, outcome := extensions.CanonicalStageMetricLabels(tc.stage, tc.outcome)
		if stage != tc.stage || outcome != tc.outcome {
			t.Fatalf("stage=%q outcome=%q got (%q,%q)", tc.stage, tc.outcome, stage, outcome)
		}
	}
}

func TestCanonicalStageMetricLabels_collapsesSensitiveUnbounded(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name           string
		stage, outcome string
		wantStage      string
		wantOutcome    string
	}{
		{"model_as_stage", "gpt-4o-mini", extensions.StageOutcomeOK, extensions.StageMetricLabelUnknown, extensions.StageOutcomeOK},
		{"session_as_stage", "sess_abc123", extensions.StageOutcomeOK, extensions.StageMetricLabelUnknown, extensions.StageOutcomeOK},
		{"anchor_as_outcome", extensions.MetricsStageFinalStreamObservation, "sha256:deadbeef", extensions.MetricsStageFinalStreamObservation, extensions.StageMetricLabelUnknown},
		{"reasoning_excerpt_outcome", extensions.MetricsStageCandidateAttemptTransform, "because the user said secret", extensions.MetricsStageCandidateAttemptTransform, extensions.StageMetricLabelUnknown},
		{"backend_route_stage", "openai/chat/completions", extensions.StageOutcomeOK, extensions.StageMetricLabelUnknown, extensions.StageOutcomeOK},
		{"empty_collapses", "", "", extensions.StageMetricLabelUnknown, extensions.StageMetricLabelUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			stage, outcome := extensions.CanonicalStageMetricLabels(tc.stage, tc.outcome)
			if stage != tc.wantStage || outcome != tc.wantOutcome {
				t.Fatalf("got (%q,%q) want (%q,%q)", stage, outcome, tc.wantStage, tc.wantOutcome)
			}
		})
	}
}

type recordingCountByteMetrics struct {
	stages   [][2]string
	counts   []int64
	bytes    []int64
	failOpen []string
}

func (m *recordingCountByteMetrics) ObserveStage(stage, outcome string, _ float64) {
	m.stages = append(m.stages, [2]string{stage, outcome})
}

func (m *recordingCountByteMetrics) IncFailOpenSkip(stage string) {
	m.failOpen = append(m.failOpen, stage)
}

func (m *recordingCountByteMetrics) AddStageCount(stage, outcome string, n int64) {
	m.counts = append(m.counts, n)
	_ = stage
	_ = outcome
}

func (m *recordingCountByteMetrics) ObserveStageBytes(stage, outcome string, n int64) {
	m.bytes = append(m.bytes, n)
	_ = stage
	_ = outcome
}

func TestRecordStageObservation_nilMetricsNoOp(t *testing.T) {
	t.Parallel()
	extensions.RecordStageObservation(nil, extensions.MetricsStageFinalStreamObservation, extensions.StageOutcomeOK, 0.01, 1, 4)
}

func TestRecordStageObservation_recordsCountsAndBytesOnOptionalSink(t *testing.T) {
	t.Parallel()
	m := &recordingCountByteMetrics{}
	extensions.RecordStageObservation(m, extensions.MetricsStageFinalStreamObservation, extensions.StageOutcomeOK, 0.02, 1, 12)
	if len(m.stages) != 1 || m.stages[0][0] != extensions.MetricsStageFinalStreamObservation || m.stages[0][1] != extensions.StageOutcomeOK {
		t.Fatalf("stages=%#v", m.stages)
	}
	if len(m.counts) != 1 || m.counts[0] != 1 {
		t.Fatalf("counts=%#v", m.counts)
	}
	if len(m.bytes) != 1 || m.bytes[0] != 12 {
		t.Fatalf("bytes=%#v", m.bytes)
	}
}

func TestRecordStageObservation_collapsesSensitiveLabelsBeforeSink(t *testing.T) {
	t.Parallel()
	m := &recordingCountByteMetrics{}
	extensions.RecordStageObservation(m, "claude-3-opus", "sess_xyz", 0.01, 1, 0)
	if len(m.stages) != 1 {
		t.Fatalf("stages=%#v", m.stages)
	}
	if m.stages[0][0] != extensions.StageMetricLabelUnknown || m.stages[0][1] != extensions.StageMetricLabelUnknown {
		t.Fatalf("want unknown/unknown got %#v", m.stages[0])
	}
}

func TestRecordStageObservation_ignoresNonPositiveCountAndBytes(t *testing.T) {
	t.Parallel()
	m := &recordingCountByteMetrics{}
	extensions.RecordStageObservation(m, extensions.MetricsStageFinalStreamObservation, extensions.StageOutcomeOK, 0.01, 0, 0)
	extensions.RecordStageObservation(m, extensions.MetricsStageFinalStreamObservation, extensions.StageOutcomeOK, 0.01, -3, -9)
	if len(m.stages) != 2 {
		t.Fatalf("duration still recorded: stages=%#v", m.stages)
	}
	if len(m.counts) != 0 || len(m.bytes) != 0 {
		t.Fatalf("non-positive must not reach sink counts=%#v bytes=%#v", m.counts, m.bytes)
	}
}

func TestSafeEventObserveBytes_countsPayloadLengthsOnly(t *testing.T) {
	t.Parallel()
	ev := aipapi.Event{Delta: "abcd", Signature: "sig12"}
	if got := extensions.SafeEventObserveBytes(ev); got != 9 {
		t.Fatalf("bytes=%d want 9", got)
	}
}

func TestSafeCallReasoningObserveBytes_aggregatesReasoningPayloadOnly(t *testing.T) {
	t.Parallel()
	call := &aipapi.Call{
		Messages: []aipapi.Message{{
			Role: aipapi.RoleAssistant,
			Parts: []aipapi.Part{
				aipapi.TextPart("prompt-not-counted"),
				{Kind: aipapi.PartReasoning, Reasoning: &aipapi.ReasoningPart{
					Dialect: aipapi.ReasoningDialectOpenAIChatTextV1, Text: "abcd", Signature: "xy",
				}},
			},
		}},
	}
	if got := extensions.SafeCallReasoningObserveBytes(call); got != 6 {
		t.Fatalf("bytes=%d want 6", got)
	}
	if got := extensions.SafeCallReasoningObserveBytes(nil); got != 0 {
		t.Fatalf("nil call bytes=%d", got)
	}
}
