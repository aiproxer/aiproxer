package featurehost

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/auxreq"
	"github.com/aiproxer/aiproxer/internal/core/execctx"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/infra/featuredb"
	"github.com/aiproxer/aiproxer/internal/infra/proxymemorystore"
	"github.com/aiproxer/aiproxer/internal/plugins/features/proxymemory"
	"github.com/aiproxer/aiproxer/internal/stdhttp/proxymemoryhttp"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	aipplugin "github.com/aiproxer/aiproxer/pkg/aipsdk/plugin"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/response"
	"github.com/uptrace/bun"
)

// Prerequisite rejections of design §14.3: an enabled proxy-memory generation
// without its durable store and process-owned decision cache (Requirement 1.7),
// or without the background auxiliary client, fails the candidate. The
// messages name the missing capability only, never a path or configuration
// value.
var (
	errProxyMemoryStorePrerequisite = errors.New("featurehost: proxy-memory requires a durable SQLite or PostgreSQL continuity store")
	errProxyMemoryBackgroundClient  = errors.New("featurehost: proxy-memory requires the background auxiliary client")
	// errProxyMemoryDatabaseClosed rejects an acquisition after the process
	// closed its memory databases: a closed process serves no generation.
	errProxyMemoryDatabaseClosed = errors.New("featurehost: the proxy-memory database pool is closed")
)

// proxyMemoryGeneration carries one generation's proxy-memory composition: the
// frozen-plane contribution pass, the two lifecycles and the projected HTTP
// handler. Every field is nil/empty when the feature is disabled.
type proxyMemoryGeneration struct {
	planes     func(aipfeature.FrozenPlaneSet) (aipfeature.FrozenPlaneSet, error)
	lifecycles []aipplugin.Lifecycle // [capture writer (stops at generation Close), worker (QuiescePhase)]
	http       http.Handler
	// attemptGuard is the feature's pre-open invariant check (design §2). The
	// core owns the provider-open chokepoint; the feature owns the policy.
	attemptGuard runtime.AttemptGuard
}

// proxyMemoryWorkerHook is a package-local test seam: when non-nil it is
// called with the worker right after it is built, so tests can drive Tick
// directly. It is nil in production.
var proxyMemoryWorkerHook func(*proxymemory.Worker)

// proxyMemoryCompositionStore decorates the memory store one composed
// generation shares between its capture writer, worker and service.
// Production returns it unchanged; package-local tests substitute
// decorators to observe composition-time store calls.
var proxyMemoryCompositionStore = func(r *Runtime, store proxymemory.Store) proxymemory.Store { return store }

// proxyMemoryDatabase is one process-owned isolated memory database. The
// memory tables live in their own database, so a memory transaction can never
// hold the mandatory core connection.
type proxyMemoryDatabase struct {
	db       *bun.DB
	identity string
}

// proxyMemoryDatabasePool owns every isolated memory database handle of one
// process. The pool is created with the process and disposed by the process
// close, so the closer list of a process never grows while a generation
// compiles.
//
// The pool keeps one handle per database identity: a generation reload reuses
// the handle of the running process instead of opening a second one, and the
// schema is applied once per identity. The pool mutex serializes the
// acquisition with the process close, so a handle can never escape the
// process cleanup.
type proxyMemoryDatabasePool struct {
	mu         sync.Mutex
	closing    atomic.Bool
	byIdentity map[string]*proxyMemoryDatabase
	byKey      map[string]*proxyMemoryDatabase
	closed     bool
}

// newProxyMemoryDatabasePool returns the empty pool of one process.
func newProxyMemoryDatabasePool() *proxyMemoryDatabasePool {
	return &proxyMemoryDatabasePool{
		byIdentity: map[string]*proxyMemoryDatabase{},
		byKey:      map[string]*proxyMemoryDatabase{},
	}
}

// acquire returns the process-owned memory database of spec. The database is
// opened on first use and kept until the process closes.
//
// A duplicate handle is never kept: when the opener hands out a second
// handle for a database the process already owns, that handle is closed
// again. A borrowed handle (a test seam that returns the very same pointer)
// stays open, because the process does not own it.
func (p *proxyMemoryDatabasePool) acquire(
	ctx context.Context,
	spec featuredb.Spec,
	open featuredb.Opener,
) (*proxyMemoryDatabase, error) {
	if p == nil || open == nil {
		return nil, errProxyMemoryStorePrerequisite
	}
	database, duplicate, err := p.acquireLocked(ctx, spec, open)
	// Cleanup never runs while the pool mutex is held.
	if duplicate != nil {
		if closeErr := duplicate.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("featurehost: close duplicate proxy-memory database: %w", closeErr))
		}
	}
	return database, err
}

// acquireLocked performs the acquisition under the pool mutex. The returned
// duplicate handle is closed by the caller, outside the mutex.
func (p *proxyMemoryDatabasePool) acquireLocked(
	ctx context.Context,
	spec featuredb.Spec,
	open featuredb.Opener,
) (*proxyMemoryDatabase, *bun.DB, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.closing.Load() {
		return nil, nil, errProxyMemoryDatabaseClosed
	}
	key := proxyMemoryDatabaseKey(spec)
	if existing, ok := p.byKey[key]; ok {
		return existing, nil, nil
	}
	opened, err := open.OpenFeatureDatabase(ctx, spec)
	if err != nil {
		return nil, nil, err
	}
	if opened.DB == nil {
		return nil, nil, errProxyMemoryStorePrerequisite
	}
	if p.closing.Load() {
		// The process close linearized while the handle was opening: the
		// handle never becomes process-owned, so it is closed again.
		return nil, opened.DB, errProxyMemoryDatabaseClosed
	}
	if existing, ok := p.byIdentity[opened.Identity]; ok {
		p.byKey[key] = existing
		if opened.DB == existing.db {
			return existing, nil, nil
		}
		return existing, opened.DB, nil
	}
	if err := proxymemorystore.EnsureSchema(ctx, opened.DB); err != nil {
		// The handle never becomes process-owned: it is closed again.
		return nil, opened.DB, fmt.Errorf("featurehost: proxy-memory ensure schema: %w", err)
	}
	if p.closing.Load() {
		// The process close linearized while the schema was applied: the
		// handle never becomes process-owned, so it is closed again.
		return nil, opened.DB, errProxyMemoryDatabaseClosed
	}
	database := &proxyMemoryDatabase{db: opened.DB, identity: opened.Identity}
	p.byIdentity[database.identity] = database
	p.byKey[key] = database
	return database, nil, nil
}

// beginClose publishes the terminal state of the pool: no acquisition is
// admitted anymore, and an acquisition in flight never publishes its handle
// as a success. The pool is closed after the dependents of its handles
// stopped.
func (p *proxyMemoryDatabasePool) beginClose() {
	if p == nil {
		return
	}
	p.closing.Store(true)
}

// Close disposes every memory database handle the pool owns and refuses later
// acquisitions. It is idempotent.
func (p *proxyMemoryDatabasePool) Close() error {
	if p == nil {
		return nil
	}
	// The terminal state is published before the sweep: an acquisition that
	// is in flight never publishes its handle as a success afterwards.
	p.beginClose()
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	databases := p.byIdentity
	p.byIdentity = nil
	p.byKey = nil
	p.mu.Unlock()

	var out error
	for _, database := range databases {
		if err := database.db.Close(); err != nil {
			out = errors.Join(out, fmt.Errorf("featurehost: close proxy-memory database: %w", err))
		}
	}
	return out
}

// proxyMemoryDatabaseKey is the stable acquisition key of one requested
// memory database. Two requests with the same key name the same database, so
// the process opens it once.
func proxyMemoryDatabaseKey(spec featuredb.Spec) string {
	return spec.FeatureID + "\x00" + strings.TrimSpace(spec.SQLitePath)
}

// proxyMemoryStoreFor resolves the memory store of one composed generation:
// the isolated memory database and the narrow core A-leg view it reads.
//
// The core view reads the core continuity database, never the memory
// database: a memory transaction must never hold a core connection, and the
// core facts the store needs are read before every memory transaction starts.
func (r *Runtime) proxyMemoryStoreFor(ctx context.Context, cfg proxymemory.Config) (proxymemory.Store, error) {
	if r == nil {
		return nil, errProxyMemoryStorePrerequisite
	}
	if r.coreDB == nil || r.featureDatabases == nil {
		return nil, errProxyMemoryStorePrerequisite
	}
	database, err := r.proxyMemoryDatabaseFor(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return proxymemorystore.New(database.db, proxymemorystore.NewALegReader(r.coreDB)), nil
}

// proxyMemoryDatabaseFor resolves the isolated memory database of the
// requested specification, once per identity. The handle is process-owned: it
// survives generation reloads and is closed with the process.
func (r *Runtime) proxyMemoryDatabaseFor(ctx context.Context, cfg proxymemory.Config) (*proxyMemoryDatabase, error) {
	if r == nil || r.proxyMemoryDatabases == nil || r.featureDatabases == nil {
		return nil, errProxyMemoryStorePrerequisite
	}
	database, err := r.proxyMemoryDatabases.acquire(ctx, featuredb.Spec{
		FeatureID:  proxymemory.ID,
		SQLitePath: strings.TrimSpace(cfg.SQLitePath),
	}, r.featureDatabases)
	if err != nil {
		if errors.Is(err, errProxyMemoryStorePrerequisite) || errors.Is(err, errProxyMemoryDatabaseClosed) {
			return nil, err
		}
		return nil, fmt.Errorf("featurehost: proxy-memory database: %w", err)
	}
	return database, nil
}

// compileProxyMemory composes the proxy-memory generation contributions per
// design §14.3. A disabled registration (or no registration at all) yields the
// zero value. An enabled registration fails the candidate when the isolated
// memory database, the core continuity database, the process-owned decision
// cache or the background auxiliary client is missing, so a misconfiguration
// never publishes a half-working memory surface.
func (r *Runtime) compileProxyMemory(ctx context.Context, in GenerationInput) (proxyMemoryGeneration, error) {
	var registration *aipsdk.Registration
	for i := range in.Registrations {
		reg := in.Registrations[i]
		if reg.Kind != aipsdk.PluginKindFeature || !reg.Enabled {
			continue
		}
		if reg.ID == proxymemory.ID || reg.FactoryKind == proxymemory.ID {
			registration = &in.Registrations[i]
			break
		}
	}
	if registration == nil {
		return proxyMemoryGeneration{}, nil
	}

	cfg, err := proxymemory.DecodeConfig(registration.Config.Node)
	if err != nil {
		return proxyMemoryGeneration{}, fmt.Errorf("featurehost: proxy-memory config: %w", err)
	}

	if r == nil {
		return proxyMemoryGeneration{}, errProxyMemoryStorePrerequisite
	}
	memStore, err := r.proxyMemoryStoreFor(ctx, cfg)
	if err != nil {
		return proxyMemoryGeneration{}, err
	}
	memStore = proxyMemoryCompositionStore(r, memStore)
	if memStore == nil || r.proxyMemoryDecisions == nil {
		return proxyMemoryGeneration{}, errProxyMemoryStorePrerequisite
	}
	if in.BackgroundClient == nil {
		return proxyMemoryGeneration{}, errProxyMemoryBackgroundClient
	}

	now := in.NowFn
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	// The generation-bound target compiler is the feature's only way to learn
	// the final compiled actual target of a phase (design §1). A
	// configuration that declares no model policy does not use it.
	cfg.TargetCompiler = in.ProxyMemoryTargetCompiler
	log := r.logger
	if log == nil {
		log = slog.Default()
	}
	metrics := r.proxyMemoryMetrics
	capture := proxymemory.NewCapture(cfg, memStore, metrics, now, log, r.proxyMemoryDecisions)
	// The operator-declared funding decision is bound here, by the
	// composition root, and travels with every background job the worker
	// submits. It is never carried by the auxiliary request, the canonical
	// call or any wire payload, so no client can select or spoof it.
	background := auxreq.WithSponsorship(in.BackgroundClient, proxyMemorySponsorship(cfg))
	worker := proxymemory.NewWorker(cfg, memStore, proxyMemoryCollector{inner: background}, metrics, now, log)
	if proxyMemoryWorkerHook != nil {
		proxyMemoryWorkerHook(worker)
	}
	svc := proxymemory.NewServiceWithDecisionCache(cfg, memStore, metrics, now, r.proxyMemoryDecisions, log)

	gen := proxyMemoryGeneration{
		planes: func(planes aipfeature.FrozenPlaneSet) (aipfeature.FrozenPlaneSet, error) {
			contributions := planes.ToContributions()
			if err := aipfeature.Contribute(contributions, aipfeature.PlaneRequestTransforms, proxymemory.ID, []request.Transform{capture.Transform()}); err != nil {
				return aipfeature.FrozenPlaneSet{}, fmt.Errorf("featurehost: proxy-memory planes: %w", err)
			}
			if err := aipfeature.Contribute(contributions, aipfeature.PlaneStreamObserverFactories, proxymemory.ID, []response.StreamObserverFactory{capture.ObserverFactory()}); err != nil {
				return aipfeature.FrozenPlaneSet{}, fmt.Errorf("featurehost: proxy-memory planes: %w", err)
			}
			return contributions.Freeze(), nil
		},
		lifecycles: []aipplugin.Lifecycle{captureLifecycle{capture: capture}, worker},
		http:       proxymemoryhttp.NewHandler(svc, metrics),
	}
	// The pre-open guard is the feature's own invariant check at the
	// provider-open chokepoint (design §2). It is bound only when the model
	// policy is declared, so a deployment without one runs every request,
	// its own phases included, with no guard at all. The adapter maps the
	// core's content-free facts to the feature's vocabulary.
	if cfg.ModelsDeclared {
		gen.attemptGuard = proxyMemoryAttemptGuard{guard: proxymemory.NewAttemptGuard(cfg, metrics, log)}
	}
	return gen, nil
}

// proxyMemorySponsorship maps the operator's feature funding configuration
// onto the core's trusted funding declaration.
//
// The mode is bound by the operator composition, never by the request: the
// auxiliary role is a known label and carries no funding authority, and the
// declaration never names an account. An operator who declared no mode gets
// the undeclared mode, which the runtime refuses before any provider dispatch.
func proxyMemorySponsorship(cfg proxymemory.Config) execctx.Sponsorship {
	decl := execctx.Sponsorship{
		SponsorKey:          cfg.SponsorKey,
		DeclaredSponsorKeys: append([]string(nil), cfg.SponsorKeys...),
	}
	switch cfg.FundingMode {
	case proxymemory.FundingModeCustomerFunded:
		decl.Mode = execctx.SponsorshipModeCustomerFunded
	case proxymemory.FundingModeOperatorSponsored:
		decl.Mode = execctx.SponsorshipModeOperatorSponsored
	default:
		decl.Mode = execctx.SponsorshipModeUndeclared
	}
	return decl
}

// captureLifecycle adapts the capture writer to the generation lifecycle
// (design §14.3). It has no QuiescePhase, so it stops only at generation Close
// and in-flight turns of a retiring generation can still be captured.
// SafeUnderCandidateOverlap is satisfied structurally, so the generic candidate
// ledger accepts the handle without featurehost importing generic runtimebundle.
type captureLifecycle struct {
	capture *proxymemory.Capture
}

func (l captureLifecycle) Start(ctx context.Context) error {
	if l.capture == nil {
		return nil
	}
	return l.capture.Start(ctx)
}

func (l captureLifecycle) Stop(ctx context.Context) error {
	if l.capture == nil {
		return nil
	}
	return l.capture.Stop(ctx)
}

// SafeUnderCandidateOverlap reports the handle safe under candidate overlap:
// capture state is per-generation by construction.
func (l captureLifecycle) SafeUnderCandidateOverlap() bool { return true }
