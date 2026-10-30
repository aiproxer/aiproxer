package runtimebundle

import (
	"context"
	"net/http"
	"sort"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/modelcatalog"
	"github.com/aiproxer/aiproxer/internal/core/modelregistry"
	"github.com/aiproxer/aiproxer/internal/core/modelview"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	terminalworkapp "github.com/aiproxer/aiproxer/internal/core/terminalwork/app"
	"github.com/aiproxer/aiproxer/internal/infra/runtimehost"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/terminaldecision"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/transport/httpauth"
)

type GenerationRuntime interface {
	runtimehost.PublishedRequestPlane
	runtimehost.ExecutorProvider
	runtimehost.PublishedWorkStarter
	runtimehost.ModelViewBinder
	runtimehost.BackendFactoryKindCounter
	TerminalProviders() terminalworkapp.TerminalProviderView
	ReadinessReport() controlplane.ReadinessReportReader
}

type generationExecution struct {
	executor   *runtime.Executor
	backendIDs []string
}

type generationHTTPPublication struct {
	handler       http.Handler
	routing       FrozenRoutingView
	frontends     []config.PluginConfig
	registrations []aipsdk.Registration
	httpAuth      []httpauth.Provider
}

type generationModelViews struct {
	models  *modelregistry.Runtime
	catalog *modelcatalog.CatalogRuntime
	// defaultModelBinding is the generation-owned canonical-to-native binding
	// of a default route derived from a discovered model. The generation keeps
	// it so a later inventory refresh cannot destroy the binding its default
	// route needs. It is nil for every other default route.
	defaultModelBinding *defaultModelBinding
}

type generationOperations struct {
	terminalProviders *terminalworkapp.FrozenTerminalProviders
	Frozen            aipfeature.FrozenPlaneSet
	readiness         controlplane.ReadinessReportReader
}

type GenerationBundle struct {
	execution   generationExecution
	publication generationHTTPPublication
	models      generationModelViews
	operations  generationOperations
	ledger      *ResourceLedger
}

var (
	_ GenerationRuntime                     = (*GenerationBundle)(nil)
	_ runtimehost.OwnedCloser               = (*GenerationBundle)(nil)
	_ runtimehost.QuiesceCloser             = (*GenerationBundle)(nil)
	_ runtimehost.PublishedRequestPlane     = (*GenerationBundle)(nil)
	_ runtimehost.PublishedWorkStarter      = (*GenerationBundle)(nil)
	_ runtimehost.ModelViewBinder           = (*GenerationBundle)(nil)
	_ runtimehost.ExecutorProvider          = (*GenerationBundle)(nil)
	_ runtimehost.BackendFactoryKindCounter = (*GenerationBundle)(nil)
	_ routing.NativeModelResolver           = modelregistry.BoundView{}
)

func (b *GenerationBundle) BindModelViews(ctx context.Context) context.Context {
	ctx = ctxOrBackground(ctx)
	if b == nil {
		return ctx
	}
	regView := b.models.models.BoundView()
	catView := b.models.catalog.BoundView()
	var configGen int64
	var configFP string
	if rb, ok := runtimehost.BindingFromContext(ctx); ok {
		meta := rb.Meta()
		configGen = meta.ID
		configFP = meta.PublicFingerprint
	}
	id := modelview.Derive(configGen, configFP, regView.Generation(), catView.Generation())
	ctx = modelregistry.WithBoundView(ctx, regView)
	ctx = modelcatalog.WithBoundView(ctx, catView)
	// The request-bound registry is authoritative for every route. The
	// generation's frozen bindings are attached beside it, and the executor
	// consults them only for a request whose route provenance is the
	// generation's derived default route.
	ctx = routing.WithNativeModelResolver(ctx, regView)
	ctx = routing.WithDefaultRouteBindings(ctx, b.defaultRouteBindings())
	ctx = modelview.WithIdentity(ctx, id)
	return ctx
}

// defaultRouteBindings returns the generation-owned frozen model bindings.
func (b *GenerationBundle) defaultRouteBindings() routing.DefaultRouteBindings {
	if b == nil || b.models.defaultModelBinding == nil {
		return nil
	}
	return b.models.defaultModelBinding.bindings
}

func (b *GenerationBundle) TerminalProviders() terminalworkapp.TerminalProviderView {
	if b == nil || b.operations.terminalProviders == nil {
		return terminalworkapp.SnapshotTerminalProviders(nil)
	}
	return b.operations.terminalProviders
}

// TerminalDecisionProvider returns the provider frozen into this generation.
// The returned instance is never rebound while the generation is published;
// request admission snapshots the same value for the request lifetime.
func (b *GenerationBundle) TerminalDecisionProvider() terminaldecision.Provider {
	if b == nil {
		return nil
	}
	return aipfeature.Get(b.operations.Frozen, aipfeature.PlaneTerminalDecisionProvider)
}

func (b *GenerationBundle) Handler() http.Handler {
	if b == nil {
		return nil
	}
	return b.publication.handler
}

func (b *GenerationBundle) ExecutorView() aipsdk.ExecutorView {
	if b == nil || b.execution.executor == nil {
		return nil
	}
	return b.execution.executor
}

func (b *GenerationBundle) ReadinessReport() controlplane.ReadinessReportReader {
	if b == nil {
		return nil
	}
	return b.operations.readiness
}

func (b *GenerationBundle) BackendIDs() []string {
	if b == nil {
		return nil
	}
	return append([]string(nil), b.execution.backendIDs...)
}

func (b *GenerationBundle) BackendFactoryKindCounts() map[string]int {
	if b == nil || len(b.publication.registrations) == 0 {
		return nil
	}
	out := make(map[string]int)
	for _, r := range b.publication.registrations {
		if !r.Enabled || r.Kind != aipsdk.PluginKindBackend {
			continue
		}
		key := r.RegistryFactoryKey()
		if key == "" {
			continue
		}
		out[key]++
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (b *GenerationBundle) Routing() FrozenRoutingView {
	if b == nil {
		return FrozenRoutingView{}
	}
	return FrozenRoutingView{
		DefaultRoute:  b.publication.routing.DefaultRoute,
		RoutePrefixes: append([]string(nil), b.publication.routing.RoutePrefixes...),
	}
}

func (b *GenerationBundle) RoutePrefixes() []string {
	if b == nil {
		return nil
	}
	return append([]string(nil), b.publication.routing.RoutePrefixes...)
}

func (b *GenerationBundle) FrozenFrontends() []config.PluginConfig {
	if b == nil {
		return nil
	}
	return freezePluginConfigs(b.publication.frontends)
}

func (b *GenerationBundle) Registrations() []aipsdk.Registration {
	if b == nil {
		return nil
	}
	return freezeRegistrations(b.publication.registrations)
}

func (b *GenerationBundle) HTTPAuthProviders() []httpauth.Provider {
	if b == nil || b.publication.httpAuth == nil {
		return nil
	}
	return append([]httpauth.Provider(nil), b.publication.httpAuth...)
}

func (b *GenerationBundle) ResourceCount() int {
	if b == nil || b.ledger == nil {
		return 0
	}
	return b.ledger.Len()
}

func (b *GenerationBundle) StartPublished(ctx context.Context) error {
	if b == nil || b.ledger == nil {
		return nil
	}
	return b.ledger.Publish(ctx)
}

func (b *GenerationBundle) Quiesce(ctx context.Context) error {
	if b == nil {
		return nil
	}
	// Generation-scoped cleanup (including keep-warm quiesce) is owned by
	// the transferred resource ledger; no feature-specific path here.
	if b.ledger != nil {
		return b.ledger.Quiesce(ctx)
	}
	return nil
}

func (b *GenerationBundle) Close() error {
	if b == nil {
		return nil
	}
	// Close is also used for unpublished/rollback generations. The ledger
	// owns every generation-scoped cleanup.
	if b.ledger != nil {
		return b.ledger.Close(context.Background())
	}
	return nil
}

func backendIDsOf(exec *runtime.Executor) []string {
	if exec == nil || exec.Backends == nil {
		return nil
	}
	ids := make([]string, 0, len(exec.Backends))
	for id := range exec.Backends {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func newGenerationBundle(in generationBundleInput) *GenerationBundle {
	return &GenerationBundle{
		execution: generationExecution{
			executor:   in.executor,
			backendIDs: append([]string(nil), in.backendIDs...),
		},
		publication: generationHTTPPublication{
			handler:       in.handler,
			routing:       in.routing,
			frontends:     freezePluginConfigs(in.frontends),
			registrations: freezeRegistrations(in.registrations),
			httpAuth:      append([]httpauth.Provider(nil), in.httpAuth...),
		},
		models: generationModelViews{
			models:              in.models,
			catalog:             in.catalog,
			defaultModelBinding: in.defaultModelBinding,
		},
		operations: generationOperations{
			terminalProviders: in.terminalProviders,
			Frozen:            in.frozen,
			readiness:         in.readiness,
		},
		ledger: in.ledger,
	}
}

type generationBundleInput struct {
	handler             http.Handler
	executor            *runtime.Executor
	routing             FrozenRoutingView
	frontends           []config.PluginConfig
	registrations       []aipsdk.Registration
	httpAuth            []httpauth.Provider
	models              *modelregistry.Runtime
	catalog             *modelcatalog.CatalogRuntime
	defaultModelBinding *defaultModelBinding
	backendIDs          []string
	ledger              *ResourceLedger
	terminalProviders   *terminalworkapp.FrozenTerminalProviders
	frozen              aipfeature.FrozenPlaneSet
	readiness           controlplane.ReadinessReportReader
}
