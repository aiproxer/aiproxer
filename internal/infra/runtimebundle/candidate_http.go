package runtimebundle

import (
	"context"
	"net/http"
	"sync"

	"github.com/aiproxer/aiproxer/internal/core/auth"
	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	concurrencyapp "github.com/aiproxer/aiproxer/internal/core/concurrencyauthority/app"
	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/controlplane"
	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/ingressdefense"
	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/internal/core/modelcatalog"
	"github.com/aiproxer/aiproxer/internal/core/modelregistry"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	ssessionapp "github.com/aiproxer/aiproxer/internal/core/securesession/app"
	terminalworkapp "github.com/aiproxer/aiproxer/internal/core/terminalwork/app"
	accountingapp "github.com/aiproxer/aiproxer/internal/core/tokenaccounting/app"
	"github.com/aiproxer/aiproxer/internal/core/usagelimit"
	infraGeoIP "github.com/aiproxer/aiproxer/internal/infra/geoip"
	"github.com/aiproxer/aiproxer/internal/infra/metrics"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/standardplugins/featurehost"
	httpcontract "github.com/aiproxer/aiproxer/internal/stdhttp/contract"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/transport/httpauth"
)

type candidateExecutionGroup struct {
	executor      *runtime.Executor
	routePrefixes []string
	// defaultRoute is the configured default route the frontends fall back to.
	// The executor applies the model aliases to it, so the route the executor
	// runs is the effective default route the operator check approved.
	defaultRoute string
	// defaultModelBinding is the generation-owned canonical-to-native binding
	// of a default route derived from a discovered model. It is nil for every
	// other default route.
	defaultModelBinding *defaultModelBinding
	decodeAdmission     aipsdk.DecodeAdmission
	upstreamHTTP        *http.Client
}
type candidateSecurityGroup struct {
	httpAuth           []httpauth.Provider
	secureSessionStore ssessionapp.Store
	authEvents         *auth.EventDispatcher
	runtimeSnapshot    *extensions.RequestRuntimeSnapshot
	geoip              *config.CompiledGeoIP
	// selfDefense is the immutable, reloadable generation policy projection. The
	// process-owned adaptive state it borrows is referenced separately, because
	// capacity and TTL are restart-required and never generation-scoped.
	selfDefense *config.CompiledSelfDefense
}
type candidateModelGroup struct {
	catalog         *modelcatalog.CatalogRuntime
	registry        *modelregistry.Registry
	registryRuntime *modelregistry.Runtime
}
type candidateOperationsGroup struct {
	tokenAccountingAdmin *accountingapp.Service
	readinessReport      *controlplane.ReadinessReportService
	secretGuardInventory *diag.InventoryExtras
	terminalRegistry     *terminalworkapp.Registry
	// hostProjections carries the candidate generation admin projections and policy factories.
	hostProjections featurehost.HostProjections
}
type candidateProcessRefs struct {
	// ps is the owning process, for test inspection of process-owned services.
	ps                   *ProcessServices
	store                b2bua.Store
	pluginRegistry       *pluginreg.Registry
	metrics              *metrics.Bundle
	controlPlaneQueries  *controlplane.QueryService
	usageAuthority       usagelimit.Authority
	concurrencyAuthority *concurrencyapp.Service
	meteringQuerier      metering.Querier
	geoip                *infraGeoIP.Service
	ingressDefense       *ingressdefense.State
	spoolLedger          *largebody.SpoolLedger
}
type candidateAssembly struct {
	execution                      candidateExecutionGroup
	security                       candidateSecurityGroup
	models                         candidateModelGroup
	operations                     candidateOperationsGroup
	process                        candidateProcessRefs
	ledger                         *ResourceLedger
	lifeMu                         sync.Mutex
	lifeClaimed, ledgerTransferred bool
	terminalWorkReady              func(context.Context) error
	terminalWorkRT                 *terminalWorkRuntime
}

var _ interface {
	Quiesce(context.Context) error
	Close() error
} = (*candidateAssembly)(nil)

type CandidateHTTPCompile struct{ assem *candidateAssembly }

// CompileCandidate runs the same candidate compile as [CompileGeneration]
// (feature surface, feature host, candidate assembly) without composing the
// HTTP request plane, so callers can inspect the assembled generation.
//
// When the process or candidate options carry a prebuilt feature surface
// (FeaturePlanes or FeatureLifecycles), that surface is assembled as given and
// the feature host is not compiled: a test seam for injected planes and
// lifecycles that production composition never sets.
func CompileCandidate(ctx context.Context, in GenerationCompileInput) (*CandidateHTTPCompile, error) {
	var processOpts *BuildOptions
	if in.Process != nil {
		processOpts = in.Process.opts
	}
	if hasPrebuiltFeatureSurface(processOpts) || hasPrebuiltFeatureSurface(in.CandidateOpts) {
		if in.Process != nil && !in.Process.Closed() {
			candidate := in.Candidate
			if candidate == nil {
				candidate = in.Process.cfg
			}
			if err := validateRequiredAuthorityEvidenceWiring(candidate, in.Process.UsageAuthority); err != nil {
				return nil, err
			}
		}
		assem, err := compileCandidate(ctx, in)
		if err != nil {
			return nil, err
		}
		return &CandidateHTTPCompile{assem: assem}, nil
	}
	gc, err := compileGenerationCandidate(ctx, in, false)
	if err != nil {
		return nil, err
	}
	return &CandidateHTTPCompile{assem: gc.cand}, nil
}

func hasPrebuiltFeatureSurface(opts *BuildOptions) bool {
	return opts != nil && (!opts.FeaturePlanes.IsZero() || opts.FeatureLifecycles != nil)
}

func (c *CandidateHTTPCompile) a() *candidateAssembly {
	if c == nil {
		return nil
	}
	return c.assem
}

func (c *CandidateHTTPCompile) Close() error {
	if a := c.a(); a != nil {
		return a.Close()
	}
	return nil
}

func (c *CandidateHTTPCompile) RollbackUnpublished() error {
	if a := c.a(); a != nil {
		return a.RollbackUnpublished()
	}
	return nil
}

func (c *CandidateHTTPCompile) Executor() *runtime.Executor {
	if a := c.a(); a != nil {
		return a.execution.executor
	}
	return nil
}

func (c *CandidateHTTPCompile) DecodeAdmission() aipsdk.DecodeAdmission {
	if a := c.a(); a != nil {
		return a.execution.decodeAdmission
	}
	return nil
}

// DefaultRouteSelector returns the configured default route the frontends fall
// back to when a client sends no route prefix. The executor applies the model
// aliases to it, exactly as it does to a client selector, so the route the
// executor runs is the effective default route.
func (c *CandidateHTTPCompile) DefaultRouteSelector() string {
	if a := c.a(); a != nil {
		return a.execution.defaultRoute
	}
	return ""
}

func (c *CandidateHTTPCompile) RoutePrefixes() []string {
	if a := c.a(); a != nil {
		return append([]string(nil), a.execution.routePrefixes...)
	}
	return nil
}

func (c *CandidateHTTPCompile) PluginRegistry() *pluginreg.Registry {
	if a := c.a(); a != nil {
		return a.process.pluginRegistry
	}
	return nil
}

func (c *CandidateHTTPCompile) RuntimeSnapshot() *extensions.RequestRuntimeSnapshot {
	if a := c.a(); a != nil {
		return a.security.runtimeSnapshot
	}
	return nil
}

func (c *CandidateHTTPCompile) Metrics() *metrics.Bundle {
	if a := c.a(); a != nil {
		return a.process.metrics
	}
	return nil
}

func (c *CandidateHTTPCompile) Store() b2bua.Store {
	if a := c.a(); a != nil {
		return a.process.store
	}
	return nil
}

func (c *CandidateHTTPCompile) StandardHTTPInput(frozen *config.Config, regs []aipsdk.Registration, route string) httpcontract.StandardHTTPInput {
	if a := c.a(); a != nil {
		return buildStandardHTTPInput(context.TODO(), a, frozen, regs, route)
	}
	return httpcontract.StandardHTTPInput{}
}
