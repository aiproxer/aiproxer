package featurehost

import (
	"context"
	"log/slog"
	"testing"

	featureclassification "github.com/aiproxer/aiproxer/internal/plugins/features/sessionclassification"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	sdkclassification "github.com/aiproxer/aiproxer/pkg/aipsdk/sessionclassification"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

var classificationHotPathFamilies = []string{
	"aip_session_classification_evaluations_total",
	"aip_session_classification_transitions_total",
	"aip_session_classification_remote_total",
	"aip_session_classification_remote_seconds",
	"aip_session_classification_store_total",
}

// TestSessionClassificationMetrics_AbsentAndDisabledGenerationsEmitNoObservations
// is the executable form of requirements 9.6 and 10.8: a deployment that has not
// enabled the feature exports only the static feature/inventory state, so there
// is no classifier evaluation and no remote or store hot-path observation
// attributable to the feature.
func TestSessionClassificationMetrics_AbsentAndDisabledGenerationsEmitNoObservations(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	registry := prometheus.NewRegistry()
	runtime, err := NewProcess(ctx, ProcessInput{Logger: slog.Default(), MetricsRegistry: registry})
	if err != nil {
		t.Fatalf("NewProcess: %v", err)
	}
	t.Cleanup(func() {
		if err := runtime.Close(); err != nil {
			t.Errorf("Runtime.Close: %v", err)
		}
	})

	cases := []struct {
		name          string
		registrations []aipsdk.Registration
	}{
		{name: "absent registration", registrations: nil},
		{name: "outer-disabled registration", registrations: []aipsdk.Registration{{
			ID: featureclassification.ID, Kind: aipsdk.PluginKindFeature, Enabled: false,
		}}},
		{name: "unrelated enabled registration", registrations: []aipsdk.Registration{{
			ID: "some-other-feature", Kind: aipsdk.PluginKindFeature, Enabled: true,
		}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			out, err := runtime.CompileGeneration(ctx, GenerationInput{Registrations: tc.registrations})
			if err != nil {
				t.Fatalf("CompileGeneration: %v", err)
			}
			if classifier := aipfeature.Get(out.Planes, aipfeature.PlaneSessionClassifier); classifier != nil {
				t.Fatalf("classification plane = %v, want no classifier", classifier)
			}
			families, err := registry.Gather()
			if err != nil {
				t.Fatal(err)
			}
			assertOnlyStaticClassificationState(t, families)
		})
	}
}

// TestSessionClassificationMetrics_EnabledGenerationPublishesOneBoundedObserver
// proves the registered collector is the same bounded observer the generation's
// classifier records into: one bounded observation recorded through the
// process-owned sink and the classifier's own fail-open evaluation must land in
// the same registry, so the metrics surface cannot drift from the feature that
// owns it.
func TestSessionClassificationMetrics_EnabledGenerationPublishesOneBoundedObserver(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	registry := prometheus.NewRegistry()
	runtime, err := NewProcess(ctx, ProcessInput{Logger: slog.Default(), MetricsRegistry: registry})
	if err != nil {
		t.Fatalf("NewProcess: %v", err)
	}
	t.Cleanup(func() {
		if err := runtime.Close(); err != nil {
			t.Errorf("Runtime.Close: %v", err)
		}
	})

	observer := runtime.sessionClassification.Observer()
	if observer == nil {
		t.Fatal("process-owned classification observer is nil")
	}
	// The process-owned sink must be the collector this registry gathers: a
	// bounded observation recorded directly through it must appear in the same
	// registry, so the generation cannot record into a second surface.
	observer.ObserveStore(featureclassification.StoreObservation{
		Operation: featureclassification.StoreOperationLoad,
		Outcome:   featureclassification.StoreOutcomeMiss,
	})
	enabled := []aipsdk.Registration{{
		ID: featureclassification.ID, Kind: aipsdk.PluginKindFeature, Enabled: true,
	}}
	out, err := runtime.CompileGeneration(ctx, GenerationInput{Registrations: enabled})
	if err != nil {
		t.Fatalf("CompileGeneration: %v", err)
	}
	classifier := aipfeature.Get(out.Planes, aipfeature.PlaneSessionClassifier)
	if classifier == nil {
		t.Fatal("enabled generation published no session classifier")
	}
	requireBoundedClassifierPlane(t, classifier)

	// The candidate has not been prepared yet, so process state is unavailable
	// and the turn fails open through the bounded state-unavailable diagnostic.
	got, err := classifier.Classify(ctx, unclassifiedSessionInput())
	if err == nil {
		t.Fatalf("Classify before candidate prepare error = nil, want the bounded state failure")
	}
	if got.IsCodingAgent() {
		t.Fatalf("classification = %+v, want unknown before candidate prepare", got)
	}

	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	evaluations := findClassificationFamily(t, families, "aip_session_classification_evaluations_total")
	if len(evaluations.GetMetric()) != 1 {
		t.Fatalf("evaluation series = %d, want one bounded (mode,outcome) series", len(evaluations.GetMetric()))
	}
	labels := map[string]string{}
	for _, pair := range evaluations.GetMetric()[0].GetLabel() {
		labels[pair.GetName()] = pair.GetValue()
	}
	if labels["mode"] != "heuristic" || labels["outcome"] != "state_unavailable" {
		t.Fatalf("evaluation labels = %v, want bounded heuristic/state_unavailable", labels)
	}
	stores := findClassificationFamily(t, families, "aip_session_classification_store_total")
	if len(stores.GetMetric()) != 1 {
		t.Fatalf("store series = %d, want the one bounded observation recorded through the process observer", len(stores.GetMetric()))
	}
	storeLabels := map[string]string{}
	for _, pair := range stores.GetMetric()[0].GetLabel() {
		storeLabels[pair.GetName()] = pair.GetValue()
	}
	if storeLabels["operation"] != "load" || storeLabels["outcome"] != "miss" {
		t.Fatalf("store labels = %v, want the bounded load/miss observation recorded through the process observer", storeLabels)
	}
	for _, family := range families {
		switch family.GetName() {
		case "aip_session_classification_store_ready",
			"aip_session_classification_evaluations_total",
			"aip_session_classification_transitions_total",
			"aip_session_classification_remote_total",
			"aip_session_classification_remote_seconds",
			"aip_session_classification_store_total":
		default:
			t.Fatalf("unexpected classification metric family %q", family.GetName())
		}
	}
}

// requireBoundedClassifierPlane pins the exclusive plane payload to the bounded
// SDK classifier contract: an operator-visible surface consumes the returned
// snapshot and never needs feature-private classifier state (requirement 9.5).
func requireBoundedClassifierPlane(t *testing.T, classifier sdkclassification.Classifier) {
	t.Helper()
	if _, err := sdkclassification.ClassifierIdentity(classifier); err != nil {
		t.Fatalf("published classifier identity: %v", err)
	}
}

func unclassifiedSessionInput() sdkclassification.Input {
	return sdkclassification.Input{
		Session: session.SessionView{AuthoritativeSessionID: "sess-metrics-hostile-51ac0f"},
		Evidence: sdkclassification.Evidence{
			ClientUserAgent: "codex_cli_rs/9.9.9-hostile",
		},
	}
}

func assertOnlyStaticClassificationState(t *testing.T, families []*dto.MetricFamily) {
	t.Helper()
	seenReady := false
	for _, family := range families {
		if family.GetName() != "aip_session_classification_store_ready" {
			for _, hot := range classificationHotPathFamilies {
				if family.GetName() == hot {
					t.Fatalf("classification hot-path family %q was gathered without an enabled generation", hot)
				}
			}
			continue
		}
		seenReady = true
		if len(family.GetMetric()) != 1 || family.GetMetric()[0].GetGauge().GetValue() != 0 {
			t.Fatalf("store readiness = %+v, want a single zero gauge while the feature is absent", family.GetMetric())
		}
	}
	if !seenReady {
		t.Fatal("static feature/inventory store readiness was not exported")
	}
}

func findClassificationFamily(t *testing.T, families []*dto.MetricFamily, name string) *dto.MetricFamily {
	t.Helper()
	for _, family := range families {
		if family.GetName() == name {
			return family
		}
	}
	t.Fatalf("metric family %q not gathered", name)
	return nil
}
