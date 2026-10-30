package runtimebundle

import (
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/auxreq"
	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	corestate "github.com/aiproxer/aiproxer/internal/core/state"
	coreworkspace "github.com/aiproxer/aiproxer/internal/core/workspace"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/policydecision"
	aipstate "github.com/aiproxer/aiproxer/pkg/aipsdk/state"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/traffic"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/usage"
	aipworkspace "github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
)

// extensionRuntime holds the request runtime snapshot for [Built] generation.
type extensionRuntime struct {
	Snap *extensions.RequestRuntimeSnapshot
}

// buildExtensionRuntime builds the snapshot preserving exec-cell closure.
// The secret-guard plane is planes-derived by the caller; no concrete feature
// assembly crosses this boundary.
func buildExtensionRuntime(bctx buildContext, nowFn func() time.Time, execRunnerProvider func() auxreq.ExecutorRunner, cp *controlPlaneRuntime, policyObs policydecision.Observer, sgPlane extensions.SecretGuardPlane, extensionState aipstate.Store, diagnostic usage.Observer) *extensionRuntime {
	snap := buildRuntimeSnapshot(bctx.Bus, bctx.Cfg, bctx.Opts, nowFn, execRunnerProvider, cp, policyObs, sgPlane, extensionState, diagnostic)
	return &extensionRuntime{Snap: snap}
}

// assemblePolicyObserverChain builds the policydecision observer chain shared by
// the runtime snapshot and the usage-authority evidence sink. The control-plane
// policy observer adapter is prepended (when enabled) so authority decisions
// project into the control-plane ledger as CategoryPolicy events alongside
// operator-supplied observers. Returns NoopObserver when neither is configured.
func assemblePolicyObserverChain(opts *BuildOptions, cp *controlPlaneRuntime) policydecision.Observer {
	var policyObs policydecision.Observer = policydecision.NoopObserver{}
	cpPolicyObs := cp.policyObserver()
	var operators []policydecision.Observer
	if opts != nil {
		operators = append(operators, opts.Policy.PolicyObservers...)
		operators = append(operators, opts.Production.PolicyObservers...)
	}
	if len(operators) > 0 {
		chain := make([]policydecision.Observer, 0, len(operators)+1)
		if cpPolicyObs != nil {
			chain = append(chain, cpPolicyObs)
		}
		chain = append(chain, operators...)
		policyObs = policydecision.NewChainObserver(chain...)
	} else if cpPolicyObs != nil {
		policyObs = cpPolicyObs
	}
	return policyObs
}

func buildRuntimeSnapshot(
	bus *hooks.Bus,
	cfg *config.Config,
	opts *BuildOptions,
	nowFn func() time.Time,
	execRunnerProvider func() auxreq.ExecutorRunner,
	cp *controlPlaneRuntime,
	policyObs policydecision.Observer,
	sgPlane extensions.SecretGuardPlane,
	extensionState aipstate.Store,
	diagnostics ...usage.Observer,
) *extensions.RequestRuntimeSnapshot {
	var frozen aipfeature.FrozenPlaneSet
	if opts != nil {
		frozen = opts.FeaturePlanes
	}
	wsResolvers := aipfeature.Get(frozen, aipfeature.PlaneWorkspaceResolvers)
	var ws aipworkspace.Resolver = aipworkspace.DisabledResolver{}
	if len(wsResolvers) > 0 {
		ss := cfg.SecureSession
		secureOn := cfg.SecureSessionEffectivelyEnabled()
		resolveFailClosed := strings.ToLower(strings.TrimSpace(ss.WorkspaceResolveOnError)) == "fail_closed"
		failClosedWS := secureOn && resolveFailClosed
		if failClosedWS {
			ws = coreworkspace.NewStrictChain(wsResolvers)
		} else {
			ws = coreworkspace.NewResolverChain(wsResolvers)
		}
	}
	var trafficObs traffic.Observer = traffic.NoopObserver{}
	if rawObs := aipfeature.Get(frozen, aipfeature.PlaneTrafficObservers); len(rawObs) > 0 {
		trafficObs = traffic.ChainObservers(rawObs...)
	}
	var usageObs usage.Observer = usage.NoopObserver{}
	cpUsageObs := cp.usageObserver()
	if rawUsage := aipfeature.Get(frozen, aipfeature.PlaneUsageObservers); len(rawUsage) > 0 {
		chain := make([]usage.Observer, 0, len(rawUsage)+1)
		if cpUsageObs != nil {
			chain = append(chain, cpUsageObs)
		}
		chain = append(chain, rawUsage...)
		usageObs = usage.ChainObservers(chain...)
	} else if cpUsageObs != nil {
		usageObs = cpUsageObs
	}
	// Diagnostics are last: they cannot alter earlier observers' error behavior.
	// Leave the disabled chain (including its no-op shape) exactly unchanged.
	for _, diagnostic := range diagnostics {
		if diagnostic != nil {
			usageObs = usage.ChainObservers(usageObs, diagnostic)
		}
	}
	var trafficRaw traffic.RawCaptureSink = traffic.DisabledRawCapture{}
	if rawSinks := aipfeature.Get(frozen, aipfeature.PlaneRawCaptureSinks); len(rawSinks) > 0 {
		trafficRaw = traffic.MultiRawCapture(rawSinks...)
	}
	var budgetSrc extensions.TimeoutBudgetSource = extensions.DefaultTimeoutBudgetSource{}
	if opts != nil && opts.Policy.PolicyTimeoutBudgetSource != nil {
		budgetSrc = opts.Policy.PolicyTimeoutBudgetSource
	}
	stateStore := extensionState
	if stateStore == nil {
		stateStore = corestate.NewMem(nowFn)
	}
	return extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		State:               stateStore,
		Aux:                 auxreq.NewClient(execRunnerProvider),
		Workspace:           ws,
		TrafficObserver:     trafficObs,
		UsageObserver:       usageObs,
		RawCapture:          trafficRaw,
		FeaturePlanes:       frozen,
		SecretGuardPlane:    sgPlane,
		PolicyObserver:      policyObs,
		TimeoutBudgetSource: budgetSrc,
	})
}
