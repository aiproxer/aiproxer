package compactioncontinuity

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/compactioncontinuity/observability"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/compaction"
)

func TestObservabilityRecorderUsesBoundedContentFreeSeries(t *testing.T) {
	t.Parallel()
	recorder := observability.NewRecorder(2)
	recorder.Observe(observability.Observation{
		Stage:           observability.StagePreview,
		Outcome:         observability.OutcomeCandidate,
		RuleID:          "protocol.context_compaction.v1",
		CorrelationHash: observability.HashID("raw-transaction-id"),
	})
	recorder.Observe(observability.Observation{
		Stage:   observability.StagePreview,
		Outcome: observability.OutcomeCandidate,
		RuleID:  "protocol.context_compaction.v1",
	})
	recorder.Observe(observability.Observation{
		Stage:   observability.StageCallback,
		Outcome: observability.OutcomePanic,
	})

	series := recorder.Snapshot()
	if len(series) != 2 {
		t.Fatalf("series=%d want bounded 2: %#v", len(series), series)
	}
	var previewCount uint64
	for _, sample := range series {
		if sample.Stage == observability.StagePreview {
			previewCount = sample.Count
		}
	}
	if previewCount != 2 {
		t.Fatalf("preview count=%d want 2", previewCount)
	}
	for _, sample := range series {
		if strings.Contains(sample.CorrelationHash, "raw-transaction-id") {
			t.Fatalf("raw correlation leaked: %#v", sample)
		}
	}
}

func TestPluginObservabilityReportsFailuresWithoutBranchOrContent(t *testing.T) {
	t.Parallel()
	var observations []observability.Observation
	sink := observability.Func(func(sample observability.Observation) {
		observations = append(observations, sample)
	})
	plugin := &Plugin{obs: sink}
	plugin.observeFailure(observability.StageCallback, observability.OutcomeRollback, "tx", "prompt/capsule must not be recorded")
	if len(observations) != 1 {
		t.Fatalf("observations=%d want 1", len(observations))
	}
	got := observations[0]
	if got.Outcome != observability.OutcomeRollback || got.CorrelationHash != observability.HashID("tx") {
		t.Fatalf("observation=%#v", got)
	}
	if strings.Contains(got.CorrelationHash, "prompt") || strings.Contains(got.CorrelationHash, "capsule") {
		t.Fatalf("content leaked in observation: %#v", got)
	}
}

func TestPluginObservabilityReportsQueueSaturationAndKeepsPrimaryOpenAuthority(t *testing.T) {
	t.Parallel()
	plugin, parent, background := openFixture(t)
	recorder := observability.NewRecorder(64)
	plugin.obs = recorder
	background.submitErr = errors.New("queue full")
	if err := plugin.RequestOpened(context.Background(), openCall(), openEvent(), openMeta(), compaction.Services{BackgroundAux: background}); err != nil {
		t.Fatalf("RequestOpened returned primary error: %v", err)
	}
	if parent.state.PendingJobID != "" || len(background.submits) != 1 {
		t.Fatalf("queue failure changed primary authority: pending=%q submits=%d", parent.state.PendingJobID, len(background.submits))
	}
	if !hasObservation(recorder.Snapshot(), observability.StageJob, observability.OutcomeSaturated) {
		t.Fatalf("missing queue saturation observation: %#v", recorder.Snapshot())
	}
}

func TestPluginObservabilityReportsBarrierTimeoutAndPreservesPendingResult(t *testing.T) {
	t.Parallel()
	plugin, parent, background := openFixture(t)
	recorder := observability.NewRecorder(64)
	plugin.obs = recorder
	if err := plugin.RequestOpened(context.Background(), openCall(), openEvent(), openMeta(), compaction.Services{BackgroundAux: background}); err != nil {
		t.Fatalf("RequestOpened: %v", err)
	}
	background.awaitErr = context.DeadlineExceeded
	ev := aipapi.Event{Kind: aipapi.EventResponseFinished, Opaque: []byte("opaque")}
	before := cloneEventForAssertion(ev)
	if err := plugin.BeforeResponseRelease(context.Background(), &ev, completionPreview(), openMeta(), compaction.Services{BackgroundAux: background}); err != nil {
		t.Fatalf("BeforeResponseRelease: %v", err)
	}
	if !reflect.DeepEqual(ev, before) || parent.state.PendingJobID == "" {
		t.Fatalf("barrier failure changed primary event/state: event=%#v state=%#v", ev, parent.state)
	}
	if !hasObservation(recorder.Snapshot(), observability.StageBarrier, observability.OutcomeTimeout) {
		t.Fatalf("missing barrier timeout observation: %#v", recorder.Snapshot())
	}
}

func TestPluginObservabilityReportsCallbackPanicAsFeatureLocal(t *testing.T) {
	t.Parallel()
	parent := &panicCaptureParent{openParentFake: &openParentFake{branch: ParentBranch{Binding: "parent"}}}
	recorder := observability.NewRecorder(64)
	plugin, err := NewWithObservability(openConfig(t), parent, recorder)
	if err != nil {
		t.Fatal(err)
	}
	if err := plugin.RequestOpened(context.Background(), openCall(), openEvent(), openMeta(), compaction.Services{BackgroundAux: &openBackgroundFake{}}); err != nil {
		t.Fatalf("callback panic escaped: %v", err)
	}
	if !hasObservation(recorder.Snapshot(), observability.StageCallback, observability.OutcomePanic) {
		t.Fatalf("missing callback panic observation: %#v", recorder.Snapshot())
	}
}

func TestPluginObservabilityReportsRollbackWithoutMutatingPrimaryCall(t *testing.T) {
	t.Parallel()
	plugin, parent, background := openFixture(t)
	recorder := observability.NewRecorder(64)
	plugin.obs = recorder
	parent.injectionErr = errors.New("validation failed")
	call := openCall()
	before := aipapi.CloneCall(call)
	_ = plugin.BeforeRequest(context.Background(), &call, compaction.RequestPreview{
		Kind: compaction.PreviewCompletionCandidate, BoundaryFingerprint: "rollback-boundary",
	}, openMeta(), compaction.Services{BackgroundAux: background})
	if !reflect.DeepEqual(call, before) || len(background.submits) != 0 {
		t.Fatalf("rollback changed primary call or submitted work: call=%#v submits=%d", call, len(background.submits))
	}
	if !hasObservation(recorder.Snapshot(), observability.StageReinjection, observability.OutcomeRollback) {
		t.Fatalf("missing rollback observation: %#v", recorder.Snapshot())
	}
}

func TestPluginObservabilityReportsInvalidAndStaleResultsAsFailOpen(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		payload string
		outcome observability.Outcome
	}{
		{name: "invalid", payload: "not-json", outcome: observability.OutcomeInvalid},
		{name: "stale", payload: `{"schema_version":1,"base_revision":999,"facts":[],"plan_updates":[],"decision_updates":[],"remove_or_supersede":[]}`, outcome: observability.OutcomeStale},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			plugin, parent, background := openFixture(t)
			recorder := observability.NewRecorder(64)
			plugin.obs = recorder
			_ = plugin.RequestOpened(context.Background(), openCall(), openEvent(), openMeta(), compaction.Services{BackgroundAux: background})
			background.awaitResult.Text.WriteString(tc.payload)
			ev := aipapi.Event{Kind: aipapi.EventResponseFinished, Opaque: []byte("opaque")}
			before := cloneEventForAssertion(ev)
			_ = plugin.BeforeResponseRelease(context.Background(), &ev, completionPreview(), openMeta(), compaction.Services{BackgroundAux: background})
			if !reflect.DeepEqual(ev, before) || parent.state.PendingJobID == "" || len(background.submits) != 1 {
				t.Fatalf("failure altered primary flow: event=%#v state=%#v submits=%d", ev, parent.state, len(background.submits))
			}
			if !hasObservation(recorder.Snapshot(), observability.StageBarrier, tc.outcome) {
				t.Fatalf("missing %s observation: %#v", tc.outcome, recorder.Snapshot())
			}
		})
	}
}

func TestDeepEventSnapshotDetectsInPlaceOpaqueMutation(t *testing.T) {
	t.Parallel()
	ev := aipapi.Event{Kind: aipapi.EventResponseFinished, Opaque: []byte("opaque")}
	shallow := ev
	deep := cloneEventForAssertion(ev)
	ev.Opaque[0] = 'X'
	if !reflect.DeepEqual(ev, shallow) {
		t.Fatal("control setup failed: shallow snapshot should alias Opaque and miss mutation")
	}
	if reflect.DeepEqual(ev, deep) {
		t.Fatal("deep event snapshot missed in-place Opaque mutation")
	}
}

func cloneEventForAssertion(in aipapi.Event) aipapi.Event {
	out := in
	if in.Opaque != nil {
		out.Opaque = append([]byte(nil), in.Opaque...)
	}
	if in.Reasoning != nil {
		reasoning := *in.Reasoning
		reasoning.Opaque = append([]byte(nil), in.Reasoning.Opaque...)
		reasoning.Summary = append([]byte(nil), in.Reasoning.Summary...)
		reasoning.Content = append([]byte(nil), in.Reasoning.Content...)
		reasoning.EncryptedContent = append([]byte(nil), in.Reasoning.EncryptedContent...)
		out.Reasoning = &reasoning
	}
	if in.Item != nil {
		cloned := aipapi.CloneCall(aipapi.Call{Items: []aipapi.Item{*in.Item}}).Items[0]
		out.Item = &cloned
	}
	if in.UsageScopes != nil {
		out.UsageScopes = append([]aipapi.ScopedUsageDelta(nil), in.UsageScopes...)
	}
	return out
}

func hasObservation(series []observability.Observation, stage observability.Stage, outcome observability.Outcome) bool {
	for _, sample := range series {
		if sample.Stage == stage && sample.Outcome == outcome {
			return true
		}
	}
	return false
}

type panicCaptureParent struct {
	*openParentFake
}

func (*panicCaptureParent) Capture(context.Context, aipapi.Call, compaction.PreservationMeta) (ParentBranch, error) {
	panic("parent capture panic")
}
