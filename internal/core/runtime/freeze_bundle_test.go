package runtime

import (
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
	"github.com/aiproxer/aiproxer/pkg/aipsdk/sessionclassification"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/terminaldecision"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcall"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcatalog"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolpolicy"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/traffic"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/usage"
	aipworkspace "github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
)

type testFeatureBundle struct {
	SchemaVersion                    int
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
	Lifecycles                       []aipplugin.Lifecycle
	SessionClassifier                sessionclassification.Classifier
}

func freezeBundle(b testFeatureBundle) aipfeature.FrozenPlaneSet {
	cs := aipfeature.NewContributionSet()
	if len(b.CompletionGates) > 0 {
		_ = aipfeature.Contribute(cs, aipfeature.PlaneCompletionGates, "test", b.CompletionGates)
	}
	if len(b.RequestTransforms) > 0 {
		_ = aipfeature.Contribute(cs, aipfeature.PlaneRequestTransforms, "test", b.RequestTransforms)
	}
	if len(b.PreRequestHandlers) > 0 {
		_ = aipfeature.Contribute(cs, aipfeature.PlanePreRequestHandlers, "test", b.PreRequestHandlers)
	}
	if len(b.RouteHintProviders) > 0 {
		_ = aipfeature.Contribute(cs, aipfeature.PlaneRouteHintProviders, "test", b.RouteHintProviders)
	}
	if len(b.AttemptTransforms) > 0 {
		_ = aipfeature.Contribute(cs, aipfeature.PlaneAttemptTransforms, "test", b.AttemptTransforms)
	}
	if len(b.SessionOpeners) > 0 {
		_ = aipfeature.Contribute(cs, aipfeature.PlaneSessionOpeners, "test", b.SessionOpeners)
	}
	if len(b.WorkspaceResolvers) > 0 {
		_ = aipfeature.Contribute(cs, aipfeature.PlaneWorkspaceResolvers, "test", b.WorkspaceResolvers)
	}
	if len(b.ToolCatalogFilters) > 0 {
		_ = aipfeature.Contribute(cs, aipfeature.PlaneToolCatalogFilters, "test", b.ToolCatalogFilters)
	}
	if len(b.ToolCallPolicies) > 0 {
		_ = aipfeature.Contribute(cs, aipfeature.PlaneToolCallPolicies, "test", b.ToolCallPolicies)
	}
	if len(b.ToolCallFinalizers) > 0 {
		_ = aipfeature.Contribute(cs, aipfeature.PlaneToolCallFinalizers, "test", b.ToolCallFinalizers)
	}
	if b.ToolCallFinalizationMaxArgsBytes > 0 {
		_ = aipfeature.Contribute(cs, aipfeature.PlaneToolCallFinalizationMaxArgsBytes, "test", b.ToolCallFinalizationMaxArgsBytes)
	}
	if len(b.CompactionObservers) > 0 {
		_ = aipfeature.Contribute(cs, aipfeature.PlaneCompactionObservers, "test", b.CompactionObservers)
	}
	if len(b.CompactionPreservers) > 0 {
		_ = aipfeature.Contribute(cs, aipfeature.PlaneCompactionPreservers, "test", b.CompactionPreservers)
	}
	if len(b.TrafficObservers) > 0 {
		_ = aipfeature.Contribute(cs, aipfeature.PlaneTrafficObservers, "test", b.TrafficObservers)
	}
	if len(b.UsageObservers) > 0 {
		_ = aipfeature.Contribute(cs, aipfeature.PlaneUsageObservers, "test", b.UsageObservers)
	}
	if len(b.RawCaptureSinks) > 0 {
		_ = aipfeature.Contribute(cs, aipfeature.PlaneRawCaptureSinks, "test", b.RawCaptureSinks)
	}
	if len(b.TrafficRedactors) > 0 {
		_ = aipfeature.Contribute(cs, aipfeature.PlaneTrafficRedactors, "test", b.TrafficRedactors)
	}
	if len(b.StreamObserverFactories) > 0 {
		_ = aipfeature.Contribute(cs, aipfeature.PlaneStreamObserverFactories, "test", b.StreamObserverFactories)
	}
	if len(b.LocalTurnHandlers) > 0 {
		_ = aipfeature.Contribute(cs, aipfeature.PlaneLocalTurnHandlers, "test", b.LocalTurnHandlers)
	}
	if b.TerminalDecisionProvider != nil {
		_ = aipfeature.Contribute(cs, aipfeature.PlaneTerminalDecisionProvider, "test", b.TerminalDecisionProvider)
	}
	if len(b.SecretGuards) > 0 {
		_ = aipfeature.Contribute(cs, aipfeature.PlaneSecretGuards, "test", b.SecretGuards)
	}
	if b.SessionClassifier != nil {
		_ = aipfeature.Contribute(cs, aipfeature.PlaneSessionClassifier, "test", b.SessionClassifier)
	}
	return cs.Freeze()
}
