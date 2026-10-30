package featurehost

import (
	"fmt"
	"time"

	featureclassification "github.com/aiproxer/aiproxer/internal/plugins/features/sessionclassification"
	hostclassification "github.com/aiproxer/aiproxer/internal/standardplugins/featurehost/sessionclassification"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	sdkclassification "github.com/aiproxer/aiproxer/pkg/aipsdk/sessionclassification"
)

// bindSessionClassifier publishes the concrete session-classification classifier
// as a generation-bound value on the exclusive classifier plane.
//
// The binder owns three properties of the feature:
//
//   - configuration is decoded and validated while the candidate generation is
//     compiled, so an unknown mode, an impossible threshold, contradictory
//     local/remote settings, or unbounded matcher data reject the candidate
//     before publication;
//   - the classifier is bound to the process-owned state holder, never to a
//     generation-local store, so reload keeps one monotonic shared state and
//     composing a generation initializes no feature state;
//   - the plane is exclusive, so a second effective classifier contributor
//     rejects the candidate instead of silently replacing the single
//     classification authority.
//
// An absent or outer-disabled registration contributes no plane at all.
func (r *Runtime) bindSessionClassifier(outPlanes aipfeature.FrozenPlaneSet, in GenerationInput) (aipfeature.FrozenPlaneSet, error) {
	if r == nil || r.sessionClassification == nil {
		return outPlanes, nil
	}
	registration, enabled := enabledSessionClassificationRegistration(in.Registrations)
	if !enabled {
		return outPlanes, nil
	}
	cfg, err := featureclassification.DecodeConfig(registration.Config.Node)
	if err != nil {
		return aipfeature.FrozenPlaneSet{}, fmt.Errorf("featurehost: %w", err)
	}
	// Requirement 6.2: the adapter is constructed only for a mode whose promotion
	// depends on a remote decision. A heuristic candidate therefore performs no
	// credential reference, no adapter construction, and no egress, and a remote
	// mode with unusable settings fails this candidate before publication
	// (requirements 6.10, 8.4).
	decider, err := hostclassification.NewRemoteDecider(cfg)
	if err != nil {
		return aipfeature.FrozenPlaneSet{}, fmt.Errorf("featurehost: session classification remote decider: %w", err)
	}
	classifier, err := featureclassification.NewClassifier(cfg, featureclassification.ClassifierDeps{
		State:  r.sessionClassification,
		Remote: decider,
		// The bounded observation sink exists only on the process-owned collector,
		// which this holder owns for the whole process. Because the sink is bound
		// here, an absent or disabled registration contributes no observer at all
		// and therefore no classification-specific observation (requirements 9.6,
		// 10.8).
		Observer: r.sessionClassification.Observer(),
		Now:      classificationNowFunc(in.NowFn),
	})
	if err != nil {
		return aipfeature.FrozenPlaneSet{}, fmt.Errorf("featurehost: session classification classifier: %w", err)
	}
	cs := outPlanes.ToContributions()
	if err := aipfeature.Contribute(cs, aipfeature.PlaneSessionClassifier, featureclassification.ID, sdkclassification.Classifier(classifier)); err != nil {
		return aipfeature.FrozenPlaneSet{}, fmt.Errorf("featurehost: session classification classifier plane: %w", err)
	}
	return cs.Freeze(), nil
}

func classificationNowFunc(now func() time.Time) func() time.Time {
	if now != nil {
		return now
	}
	return func() time.Time { return time.Now().UTC() }
}
