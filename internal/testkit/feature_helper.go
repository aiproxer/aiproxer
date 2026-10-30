package testkit

import (
	"fmt"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/compaction"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/completion"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/localturn"
	aipplugin "github.com/aiproxer/aiproxer/pkg/aipsdk/plugin"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/prerequest"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/response"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/routehint"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/terminaldecision"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcall"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcatalog"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolpolicy"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/traffic"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/usage"
	aipworkspace "github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
)

// TestFeatureBundle is a test fixture struct for constructing FrozenPlaneSets.
type TestFeatureBundle struct {
	ContributorID                    string
	CompletionGates                  []completion.Gate
	RequestTransforms                []request.Transform
	PreRequestHandlers               []prerequest.Handler
	RouteHintProviders               []routehint.Provider
	AttemptTransforms                []request.AttemptTransform
	SessionOpeners                   []session.Opener
	WorkspaceResolvers               []aipworkspace.Resolver
	ToolCatalogFilters               []toolcatalog.Filter
	ToolCallPolicies                 []toolpolicy.Policy
	ToolCallFinalizers               []toolcall.Finalizer
	ToolCallFinalizationMaxArgsBytes int
	CompactionObservers              []compaction.Observer
	CompactionPreservers             []compaction.Preserver
	TrafficObservers                 []traffic.Observer
	UsageObservers                   []usage.Observer
	RawCaptureSinks                  []traffic.RawCaptureSink
	TrafficRedactors                 []traffic.Redactor
	StreamObserverFactories          []response.StreamObserverFactory
	LocalTurnHandlers                []localturn.Handler
	TerminalDecisionProvider         terminaldecision.Provider
	SecretGuards                     []secretguard.Guard
}

// FreezeTestBundleChecked converts a TestFeatureBundle into a FrozenPlaneSet,
// returning an attributed error if any contribution fails.
func FreezeTestBundleChecked(b TestFeatureBundle) (aipfeature.FrozenPlaneSet, error) {
	contributorID := b.ContributorID
	if contributorID == "" {
		contributorID = "test"
	}
	cs := aipfeature.NewContributionSet()
	if b.CompletionGates != nil {
		if err := aipfeature.Contribute(cs, aipfeature.PlaneCompletionGates, contributorID, b.CompletionGates); err != nil {
			return aipfeature.FrozenPlaneSet{}, fmt.Errorf("testkit: contribute %s: %w", aipfeature.PlaneCompletionGates.ID, err)
		}
	}
	if b.RequestTransforms != nil {
		if err := aipfeature.Contribute(cs, aipfeature.PlaneRequestTransforms, contributorID, b.RequestTransforms); err != nil {
			return aipfeature.FrozenPlaneSet{}, fmt.Errorf("testkit: contribute %s: %w", aipfeature.PlaneRequestTransforms.ID, err)
		}
	}
	if b.PreRequestHandlers != nil {
		if err := aipfeature.Contribute(cs, aipfeature.PlanePreRequestHandlers, contributorID, b.PreRequestHandlers); err != nil {
			return aipfeature.FrozenPlaneSet{}, fmt.Errorf("testkit: contribute %s: %w", aipfeature.PlanePreRequestHandlers.ID, err)
		}
	}
	if b.RouteHintProviders != nil {
		if err := aipfeature.Contribute(cs, aipfeature.PlaneRouteHintProviders, contributorID, b.RouteHintProviders); err != nil {
			return aipfeature.FrozenPlaneSet{}, fmt.Errorf("testkit: contribute %s: %w", aipfeature.PlaneRouteHintProviders.ID, err)
		}
	}
	if b.AttemptTransforms != nil {
		if err := aipfeature.Contribute(cs, aipfeature.PlaneAttemptTransforms, contributorID, b.AttemptTransforms); err != nil {
			return aipfeature.FrozenPlaneSet{}, fmt.Errorf("testkit: contribute %s: %w", aipfeature.PlaneAttemptTransforms.ID, err)
		}
	}
	if b.SessionOpeners != nil {
		if err := aipfeature.Contribute(cs, aipfeature.PlaneSessionOpeners, contributorID, b.SessionOpeners); err != nil {
			return aipfeature.FrozenPlaneSet{}, fmt.Errorf("testkit: contribute %s: %w", aipfeature.PlaneSessionOpeners.ID, err)
		}
	}
	if b.WorkspaceResolvers != nil {
		if err := aipfeature.Contribute(cs, aipfeature.PlaneWorkspaceResolvers, contributorID, b.WorkspaceResolvers); err != nil {
			return aipfeature.FrozenPlaneSet{}, fmt.Errorf("testkit: contribute %s: %w", aipfeature.PlaneWorkspaceResolvers.ID, err)
		}
	}
	if b.ToolCatalogFilters != nil {
		if err := aipfeature.Contribute(cs, aipfeature.PlaneToolCatalogFilters, contributorID, b.ToolCatalogFilters); err != nil {
			return aipfeature.FrozenPlaneSet{}, fmt.Errorf("testkit: contribute %s: %w", aipfeature.PlaneToolCatalogFilters.ID, err)
		}
	}
	if b.ToolCallPolicies != nil {
		if err := aipfeature.Contribute(cs, aipfeature.PlaneToolCallPolicies, contributorID, b.ToolCallPolicies); err != nil {
			return aipfeature.FrozenPlaneSet{}, fmt.Errorf("testkit: contribute %s: %w", aipfeature.PlaneToolCallPolicies.ID, err)
		}
	}
	if b.ToolCallFinalizers != nil {
		if err := aipfeature.Contribute(cs, aipfeature.PlaneToolCallFinalizers, contributorID, b.ToolCallFinalizers); err != nil {
			return aipfeature.FrozenPlaneSet{}, fmt.Errorf("testkit: contribute %s: %w", aipfeature.PlaneToolCallFinalizers.ID, err)
		}
	}
	if b.ToolCallFinalizationMaxArgsBytes != 0 {
		if err := aipfeature.Contribute(cs, aipfeature.PlaneToolCallFinalizationMaxArgsBytes, contributorID, b.ToolCallFinalizationMaxArgsBytes); err != nil {
			return aipfeature.FrozenPlaneSet{}, fmt.Errorf("testkit: contribute %s: %w", aipfeature.PlaneToolCallFinalizationMaxArgsBytes.ID, err)
		}
	}
	if b.CompactionObservers != nil {
		if err := aipfeature.Contribute(cs, aipfeature.PlaneCompactionObservers, contributorID, b.CompactionObservers); err != nil {
			return aipfeature.FrozenPlaneSet{}, fmt.Errorf("testkit: contribute %s: %w", aipfeature.PlaneCompactionObservers.ID, err)
		}
	}
	if b.CompactionPreservers != nil {
		if err := aipfeature.Contribute(cs, aipfeature.PlaneCompactionPreservers, contributorID, b.CompactionPreservers); err != nil {
			return aipfeature.FrozenPlaneSet{}, fmt.Errorf("testkit: contribute %s: %w", aipfeature.PlaneCompactionPreservers.ID, err)
		}
	}
	if b.TrafficObservers != nil {
		if err := aipfeature.Contribute(cs, aipfeature.PlaneTrafficObservers, contributorID, b.TrafficObservers); err != nil {
			return aipfeature.FrozenPlaneSet{}, fmt.Errorf("testkit: contribute %s: %w", aipfeature.PlaneTrafficObservers.ID, err)
		}
	}
	if b.UsageObservers != nil {
		if err := aipfeature.Contribute(cs, aipfeature.PlaneUsageObservers, contributorID, b.UsageObservers); err != nil {
			return aipfeature.FrozenPlaneSet{}, fmt.Errorf("testkit: contribute %s: %w", aipfeature.PlaneUsageObservers.ID, err)
		}
	}
	if b.RawCaptureSinks != nil {
		if err := aipfeature.Contribute(cs, aipfeature.PlaneRawCaptureSinks, contributorID, b.RawCaptureSinks); err != nil {
			return aipfeature.FrozenPlaneSet{}, fmt.Errorf("testkit: contribute %s: %w", aipfeature.PlaneRawCaptureSinks.ID, err)
		}
	}
	if b.TrafficRedactors != nil {
		if err := aipfeature.Contribute(cs, aipfeature.PlaneTrafficRedactors, contributorID, b.TrafficRedactors); err != nil {
			return aipfeature.FrozenPlaneSet{}, fmt.Errorf("testkit: contribute %s: %w", aipfeature.PlaneTrafficRedactors.ID, err)
		}
	}
	if b.StreamObserverFactories != nil {
		if err := aipfeature.Contribute(cs, aipfeature.PlaneStreamObserverFactories, contributorID, b.StreamObserverFactories); err != nil {
			return aipfeature.FrozenPlaneSet{}, fmt.Errorf("testkit: contribute %s: %w", aipfeature.PlaneStreamObserverFactories.ID, err)
		}
	}
	if b.LocalTurnHandlers != nil {
		if err := aipfeature.Contribute(cs, aipfeature.PlaneLocalTurnHandlers, contributorID, b.LocalTurnHandlers); err != nil {
			return aipfeature.FrozenPlaneSet{}, fmt.Errorf("testkit: contribute %s: %w", aipfeature.PlaneLocalTurnHandlers.ID, err)
		}
	}
	if b.TerminalDecisionProvider != nil {
		if err := aipfeature.Contribute(cs, aipfeature.PlaneTerminalDecisionProvider, contributorID, b.TerminalDecisionProvider); err != nil {
			return aipfeature.FrozenPlaneSet{}, fmt.Errorf("testkit: contribute %s: %w", aipfeature.PlaneTerminalDecisionProvider.ID, err)
		}
	}
	if b.SecretGuards != nil {
		if err := aipfeature.Contribute(cs, aipfeature.PlaneSecretGuards, contributorID, b.SecretGuards); err != nil {
			return aipfeature.FrozenPlaneSet{}, fmt.Errorf("testkit: contribute %s: %w", aipfeature.PlaneSecretGuards.ID, err)
		}
	}
	return cs.Freeze(), nil
}

// FreezeTestBundle converts a TestFeatureBundle into a FrozenPlaneSet,
// panicking if any contribution fails.
func FreezeTestBundle(b TestFeatureBundle) aipfeature.FrozenPlaneSet {
	frozen, err := FreezeTestBundleChecked(b)
	if err != nil {
		panic(fmt.Errorf("testkit: freeze test bundle: %w", err))
	}
	return frozen
}

// FeatureBundle constructs a FeatureBundle by contributing into a new ContributionSet,
// freezing it, and wrapping it via aipfeature.BundleFromPlanes.
// contributorID provides the bundle/error-context identifier for test failure reporting and does
// not override contributor IDs passed to aipfeature.Contribute inside the callback.
// If contribute is nil, an empty PlaneSet is used.
// If contribute fails, the test fails via t.Fatalf (or panics if t is nil).
func FeatureBundle( //nolint:thelper // tb is optional to support testing panic fallback on nil
	tb testing.TB,
	contributorID string,
	contribute func(*aipfeature.ContributionSet) error,
	lifecycles []aipplugin.Lifecycle,
) aipfeature.FeatureBundle {
	if tb != nil {
		tb.Helper()
	}
	if contributorID == "" {
		contributorID = "test-feature"
	}
	cs := aipfeature.NewContributionSet()
	if contribute != nil {
		if err := contribute(cs); err != nil {
			if tb != nil {
				tb.Fatalf("testkit.FeatureBundle (%s): contribute: %v", contributorID, err)
			}
			panic(fmt.Errorf("testkit.FeatureBundle (%s): contribute: %w", contributorID, err))
		}
	}
	frozen := cs.Freeze()
	return aipfeature.BundleFromPlanes(frozen, lifecycles)
}

// FreezeBundle merges one or more FeatureBundles through generated plane adapters
// and returns the resulting FrozenPlaneSet. Panics if merging fails, making it ideal
// for concise test setup.
func FreezeBundle(bundles ...aipfeature.FeatureBundle) aipfeature.FrozenPlaneSet {
	cs := aipfeature.NewContributionSet()
	for i, b := range bundles {
		id := fmt.Sprintf("bundle-%d", i)
		if provId, ok := aipfeature.FrozenIdentity(b.PlaneSet, aipfeature.PlaneTerminalDecisionProvider); ok && provId != "" {
			id = provId
		}
		if err := b.PlaneSet.ReplayTo(cs, id); err != nil {
			panic(err)
		}
	}
	return cs.Freeze()
}
