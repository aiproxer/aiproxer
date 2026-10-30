package featurehost

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/auth"
	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/continuity/bunstore"
	"github.com/aiproxer/aiproxer/internal/core/conversationprojection"
	"github.com/aiproxer/aiproxer/internal/core/execctx"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/infra/conversationview"
	"github.com/aiproxer/aiproxer/internal/infra/db"
	"github.com/aiproxer/aiproxer/internal/infra/featuredb"
	"github.com/aiproxer/aiproxer/internal/infra/proxymemorystore"
	"github.com/aiproxer/aiproxer/internal/plugins/features/modelsystemprompt"
	"github.com/aiproxer/aiproxer/internal/plugins/features/proxymemory"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/response"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"
	"gopkg.in/yaml.v3"
	_ "modernc.org/sqlite"
)

// openProxyMemoryBunDB opens one SQLite-backed Bun DB for process wiring
// tests. The test owns the pool and closes it on cleanup.
func openProxyMemoryBunDB(t *testing.T) *bun.DB {
	t.Helper()
	path := filepath.ToSlash(filepath.Join(t.TempDir(), "proxy_memory.db"))
	sqlDB, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)")
	require.NoError(t, err)
	bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
	require.NoError(t, err)
	t.Cleanup(func() { _ = bunDB.Close() })
	return bunDB
}

// proxyMemoryTestDatabases is the composition seam's database opener for the
// tests: it hands the process the shared test database as the isolated memory
// database, so the composition tests observe the store without a second
// file. The production opener gives the memory store its own database, so a
// memory write never holds the mandatory core connection.
type proxyMemoryTestDatabases struct {
	database *bun.DB
	identity string
	opens    int
}

func (o *proxyMemoryTestDatabases) OpenFeatureDatabase(context.Context, featuredb.Spec) (featuredb.Database, error) {
	o.opens++
	return featuredb.Database{DB: o.database, Identity: o.identity}, nil
}

// TestProxyMemoryProcess_WiresStoreSchemaWithBunDB pins design §14.2: a
// process built with a durable Bun DB composes the proxy-memory store over an
// isolated memory database, applies its baseline schema when the first
// enabled generation is composed, and keeps a usable collector. With no
// metrics registry the collector stays unregistered but usable.
func TestProxyMemoryProcess_WiresStoreSchemaWithBunDB(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	bunDB := openProxyMemoryBunDB(t)
	databases := &proxyMemoryTestDatabases{database: bunDB, identity: "test-memory"}

	rt, err := NewProcess(ctx, ProcessInput{
		Logger: slog.Default(), BunDB: bunDB, FeatureDatabases: databases,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = rt.Close() })

	out, err := rt.CompileGeneration(ctx, GenerationInput{
		Registrations:    proxyMemoryRegistration(t, true),
		BackgroundClient: proxyMemoryFakeBackgroundClient{},
	})
	require.NoError(t, err)
	require.NotNil(t, out.HostProjections.ProxyMemoryHTTP)
	require.Equal(t, 1, databases.opens, "the memory database is opened once for the generation")

	var banks int
	require.NoError(t, bunDB.NewRaw(`SELECT count(*) FROM proxy_memory_banks`).Scan(ctx, &banks),
		"the composed generation must apply the proxy-memory baseline schema")

	require.NotNil(t, rt.proxyMemoryMetrics, "the collector must be constructed even without a metrics registry")
}

// TestProxyMemoryProcess_RegistersCollectorWithMetricsRegistry pins the
// registration half of design §14.2: a process metrics registry receives the
// feature-owned collector, and the collector stays usable through the process.
func TestProxyMemoryProcess_RegistersCollectorWithMetricsRegistry(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	bunDB := openProxyMemoryBunDB(t)
	registry := prometheus.NewRegistry()

	rt, err := NewProcess(ctx, ProcessInput{Logger: slog.Default(), BunDB: bunDB, MetricsRegistry: registry})
	require.NoError(t, err)
	t.Cleanup(func() { _ = rt.Close() })

	require.NotNil(t, rt.proxyMemoryMetrics)
	rt.proxyMemoryMetrics.Inc(proxymemory.EventInjectMatched)
	families, err := registry.Gather()
	require.NoError(t, err)
	found := false
	for _, family := range families {
		if strings.Contains(family.GetName(), "proxy_memory") {
			found = true
		}
	}
	require.True(t, found, "the proxy-memory collector must be registered with the process metrics registry")
}

// TestProxyMemoryProcess_NoBunDBLeavesStoreNil pins the other side of design
// §14.2: a process without a durable DB composes no proxy-memory store, while
// the collector surface stays available.
func TestProxyMemoryProcess_NoBunDBLeavesStoreNil(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	rt, err := NewProcess(ctx, ProcessInput{Logger: slog.Default()})
	require.NoError(t, err)
	t.Cleanup(func() { _ = rt.Close() })

	_, err = rt.CompileGeneration(ctx, GenerationInput{
		Registrations:    proxyMemoryRegistration(t, true),
		BackgroundClient: proxyMemoryFakeBackgroundClient{},
	})
	require.Error(t, err, "a process without a durable DB must not compose a proxy-memory store")
	require.Contains(t, err.Error(), "durable SQLite or PostgreSQL continuity store")
	require.NotNil(t, rt.proxyMemoryMetrics)
}

// TestProxyMemoryFallbackPrincipalMatchesAuth pins design §2: the feature
// package cannot import internal/core, so this composition-zone test keeps the
// mirrored fallback principal equal to the authoritative auth constant.
func TestProxyMemoryFallbackPrincipalMatchesAuth(t *testing.T) {
	t.Parallel()
	require.Equal(t, auth.LocalUnknownOSPrincipalID, proxymemory.LocalFallbackPrincipalID)
	require.Equal(t, "aip_local_unknown", proxymemory.LocalFallbackPrincipalID)
}

// ---------------------------------------------------------------------------
// Task 5.4: generation composition and memory bootstrap producer (design §14.3)
// ---------------------------------------------------------------------------

const (
	proxyMemoryTestPrincipal = "alice"
	proxyMemoryTestTenant    = "tenant-one"
	proxyMemoryTestLabel     = "/w/app"
	proxyMemoryTestSummary   = "the published memory summary"
)

// proxyMemoryTestAcceptedCall is a canonical admitted call whose instructions
// carry a working-directory line, so DeriveProject yields the test project.
func proxyMemoryTestAcceptedCall() *aipapi.Call {
	return &aipapi.Call{Instructions: []aipapi.Message{{
		Role:  aipapi.RoleSystem,
		Parts: []aipapi.Part{aipapi.TextPart("Working directory: " + proxyMemoryTestLabel)},
	}}}
}

// proxyMemoryTestScope is an eligible principal scope for the test principal.
func proxyMemoryTestScope() scope.PrincipalScopeView {
	return scope.PrincipalScopeView{
		SubjectKind: scope.SubjectHuman,
		PrincipalID: scope.Known(proxyMemoryTestPrincipal),
		TenantID:    scope.Known(proxyMemoryTestTenant),
		Origin:      scope.OriginClient,
	}
}

// proxyMemoryTestContext carries the authoritative scope and session snapshots
// the bootstrap producer reads (design §14.3 step 1).
func proxyMemoryTestContext(sc scope.PrincipalScopeView, sv session.SessionView) context.Context {
	return execctx.WithViews(context.Background(), execctx.Views{Scope: sc, Session: sv})
}

// proxyMemoryTestSession is an established session view for one A-leg.
func proxyMemoryTestSession(aLegID string) session.SessionView {
	return session.SessionView{AuthoritativeSessionID: "session-one", ALegID: aLegID}
}

// proxyMemoryRegistration is one proxy-memory feature registration with the
// minimal valid configuration subtree.
func proxyMemoryRegistration(t *testing.T, enabled bool) []aipsdk.Registration {
	t.Helper()
	var node yaml.Node
	require.NoError(t, yaml.Unmarshal([]byte(
		"principals:\n  - "+proxyMemoryTestPrincipal+"\nextract_route: aux-extract\nconsolidate_route: aux-consolidate\n"), &node))
	return []aipsdk.Registration{{
		Kind:    aipsdk.PluginKindFeature,
		ID:      proxymemory.ID,
		Enabled: enabled,
		Config:  aipsdk.ConfigPayload{Node: node},
	}}
}

// proxyMemoryFakeBackgroundClient is an inert auxiliary client: composition
// only requires its presence, and no test here ticks the worker loop.
type proxyMemoryFakeBackgroundClient struct{}

func (proxyMemoryFakeBackgroundClient) SubmitCollect(context.Context, auxiliary.Request, auxiliary.SubmitOptions) (auxiliary.JobID, error) {
	return "", nil
}

func (proxyMemoryFakeBackgroundClient) Await(context.Context, auxiliary.JobID) (aipapi.Collected, error) {
	return aipapi.Collected{}, nil
}

func (proxyMemoryFakeBackgroundClient) Forget(auxiliary.JobID) {}

// proxyMemoryTestHarness wires one real SQLite-backed composition: a single Bun
// DB shared by the continuity, conversation-view and proxy-memory stores, the
// process facade over them, and the registry the feature collector registers
// with.
type proxyMemoryTestHarness struct {
	rt         *Runtime
	continuity *bunstore.Store
	memory     *proxymemorystore.Store
	db         *bun.DB
	registry   *prometheus.Registry
	logs       *proxyMemoryLogCapture
	now        time.Time
}

func newProxyMemoryTestHarness(t *testing.T) *proxyMemoryTestHarness {
	t.Helper()
	ctx := context.Background()
	bunDB := openProxyMemoryBunDB(t)
	continuity, err := bunstore.New(bunDB)
	require.NoError(t, err)
	registry := prometheus.NewRegistry()
	logs := &proxyMemoryLogCapture{}
	// The composed generation applies the memory schema; the harness creates
	// its store directly, so it applies the same schema here.
	require.NoError(t, proxymemorystore.EnsureSchema(ctx, bunDB))
	rt, err := NewProcess(ctx, ProcessInput{
		Logger:           slog.New(logs),
		BunDB:            bunDB,
		ContinuityStore:  continuity,
		MetricsRegistry:  registry,
		FeatureDatabases: &proxyMemoryTestDatabases{database: bunDB, identity: "test-memory"},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = rt.Close() })
	return &proxyMemoryTestHarness{
		rt:         rt,
		continuity: continuity,
		memory:     proxymemorystore.New(bunDB, proxymemorystore.NewALegReader(bunDB)),
		db:         bunDB,
		registry:   registry,
		logs:       logs,
		now:        time.UnixMilli(1_750_000_000_000).UTC(),
	}
}

// createALeg allocates one A-leg in the shared continuity store.
func (h *proxyMemoryTestHarness) createALeg(t *testing.T) string {
	t.Helper()
	leg, err := h.continuity.CreateALeg(context.Background(), "")
	require.NoError(t, err)
	return leg.ALegID
}

// generationInput builds accepted-turn inputs over this harness.
func (h *proxyMemoryTestHarness) generationInput(registrations []aipsdk.Registration) GenerationInput {
	return GenerationInput{
		Registrations:    registrations,
		BackgroundClient: proxyMemoryFakeBackgroundClient{},
		NowFn:            func() time.Time { return h.now },
	}
}

// compileGeneration compiles one generation over this harness and requires it
// to succeed.
func (h *proxyMemoryTestHarness) compileGeneration(t *testing.T, registrations []aipsdk.Registration) GenerationOutput {
	t.Helper()
	out, err := h.rt.CompileGeneration(context.Background(), h.generationInput(registrations))
	require.NoError(t, err)
	return out
}

// seedAcceptedSummary seeds the published summary of the owner derived from the
// accepted call and returns the derived project.
func (h *proxyMemoryTestHarness) seedAcceptedSummary(t *testing.T, accepted *aipapi.Call) proxymemory.Project {
	t.Helper()
	project, ok := proxymemory.DeriveProject(accepted)
	require.True(t, ok)
	seedALegID := h.createALeg(t)
	seedProxyMemorySummary(t, h.memory, proxymemory.Owner{
		TenantID:    proxyMemoryTestTenant,
		PrincipalID: proxyMemoryTestPrincipal,
		ProjectKey:  project.Key,
	}, project.Label, proxyMemoryTestSummary, h.now.UnixMilli(), seedALegID)
	return project
}

// counter reads one event counter from the process metrics registry.
func (h *proxyMemoryTestHarness) counter(t *testing.T, event string) float64 {
	t.Helper()
	return proxyMemoryCounterValue(t, h.registry, event)
}

// proxyMemoryCounterValue reads one aip_proxy_memory_events_total child by its
// bounded event label.
func proxyMemoryCounterValue(t *testing.T, registry *prometheus.Registry, event string) float64 {
	t.Helper()
	families, err := registry.Gather()
	require.NoError(t, err)
	for _, family := range families {
		if family.GetName() != "aip_proxy_memory_events_total" {
			continue
		}
		for _, metric := range family.GetMetric() {
			if metric.GetCounter() == nil {
				continue
			}
			for _, label := range metric.GetLabel() {
				if label.GetName() == "event" && label.GetValue() == event {
					return metric.GetCounter().GetValue()
				}
			}
		}
	}
	return 0
}

// steering reads the committed overlay snapshot of one A-leg.
func (h *proxyMemoryTestHarness) steering(t *testing.T, aLegID string) []conversationprojection.Overlay {
	t.Helper()
	snap, err := h.rt.ConversationReader().Snapshot(context.Background(), aLegID)
	require.NoError(t, err)
	return snap.Steering
}

// bootstrapRows counts the stored bootstrap decisions of one producer and A-leg.
func (h *proxyMemoryTestHarness) bootstrapRows(t *testing.T, aLegID, producerID string) int {
	t.Helper()
	var count int
	require.NoError(t, h.db.NewRaw(
		`SELECT count(*) FROM a_leg_steering_bootstrap WHERE a_leg_id = ? AND producer_id = ?`,
		aLegID, producerID).Scan(context.Background(), &count))
	return count
}

// bootstrapOutcome reads the stored decision outcome of one producer and A-leg.
func (h *proxyMemoryTestHarness) bootstrapOutcome(t *testing.T, aLegID, producerID string) conversationview.BootstrapOutcome {
	t.Helper()
	var outcome string
	require.NoError(t, h.db.NewRaw(
		`SELECT outcome FROM a_leg_steering_bootstrap WHERE a_leg_id = ? AND producer_id = ?`,
		aLegID, producerID).Scan(context.Background(), &outcome))
	return conversationview.BootstrapOutcome(outcome)
}

// seedProxyMemorySummary drives one real capture through extraction and
// consolidation so the owner has a published bank summary.
func seedProxyMemorySummary(t *testing.T, store *proxymemorystore.Store, owner proxymemory.Owner, label, summary string, activityMS int64, aLegID string) {
	t.Helper()
	ctx := context.Background()
	// A capture is learnable input only when the A-leg carries the durable
	// first-turn binding of its owner, so the fixture commits that decision
	// first.
	committed, err := store.CommitFirstTurn(ctx, proxymemory.FirstTurnJob{
		Principal:  proxymemory.Principal{TenantID: owner.TenantID, PrincipalID: owner.PrincipalID},
		ALegID:     aLegID,
		Project:    proxymemory.Project{Key: owner.ProjectKey, Label: label},
		Decision:   proxymemory.FirstTurnDecision{Outcome: proxymemory.FirstTurnNoSummary},
		EpochKnown: true,
	})
	require.NoError(t, err)
	require.True(t, committed, "the seeded first-turn decision must persist")
	outcome, err := store.UpsertSession(ctx, proxymemory.SessionCapture{
		Owner:        owner,
		ProjectLabel: label,
		ALegID:       aLegID,
		Transcript:   "[]",
		ActivityMS:   activityMS,
		SourceSeq:    1,
	})
	require.NoError(t, err)
	require.Equal(t, proxymemory.UpsertStored, outcome)

	claimed, err := store.ClaimSessions(ctx, proxymemory.ClaimQuery{
		NowMS:        activityMS,
		IdleBeforeMS: activityMS,
		NotBeforeMS:  activityMS - 1_000,
		LeaseUntilMS: activityMS + 3_600_000,
		LeaseOwner:   "seed",
		Limit:        1,
	})
	require.NoError(t, err)
	require.Len(t, claimed, 1)

	require.NoError(t, store.CompleteSession(ctx, claimed[0], proxymemory.Extraction{
		Owner:           owner,
		ALegID:          claimed[0].ALegID,
		SessionRef:      "seed-session",
		RawMemory:       "raw memory",
		RolloutSummary:  "rollout summary",
		RolloutSlug:     "seed-slug",
		SourceUpdatedMS: activityMS,
		GeneratedAtMS:   activityMS,
	}))

	banks, err := store.ClaimBanks(ctx, proxymemory.ClaimQuery{
		NowMS:        activityMS,
		LeaseUntilMS: activityMS + 3_600_000,
		LeaseOwner:   "seed",
		Limit:        1,
	})
	require.NoError(t, err)
	require.Len(t, banks, 1)

	require.NoError(t, store.PublishBank(ctx, banks[0], proxymemory.Publication{
		MemoryMD:      "# memory",
		MemorySummary: summary,
		PublishedAtMS: activityMS,
	}))
}

// proxyMemoryCountingStore decorates the real store to observe the bootstrap's
// published-summary reads.
type proxyMemoryCountingStore struct {
	proxymemory.Store
	mu        sync.Mutex
	published int
}

func (s *proxyMemoryCountingStore) PublishedSummary(ctx context.Context, o proxymemory.Owner) (string, int64, error) {
	s.mu.Lock()
	s.published++
	s.mu.Unlock()
	return s.Store.PublishedSummary(ctx, o)
}

func (s *proxyMemoryCountingStore) publishedSummaryCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.published
}

// proxyMemoryFailingSummaryStore makes the published-summary read fail with a
// store failure that is not ErrNotFound.
type proxyMemoryFailingSummaryStore struct {
	proxymemory.Store
	err error
}

func (s *proxyMemoryFailingSummaryStore) PublishedSummary(context.Context, proxymemory.Owner) (string, int64, error) {
	return "", 0, s.err
}

// proxyMemoryInstallCompositionStore substitutes the composition store seam for
// one test and restores it on cleanup. The seam is process-global, so tests
// that use it must not run in parallel; it is read only while the feature is
// enabled.
func proxyMemoryInstallCompositionStore(t *testing.T, decorate func(proxymemory.Store) proxymemory.Store) {
	t.Helper()
	previous := proxyMemoryCompositionStore
	proxyMemoryCompositionStore = func(_ *Runtime, store proxymemory.Store) proxymemory.Store {
		return decorate(store)
	}
	t.Cleanup(func() { proxyMemoryCompositionStore = previous })
}

// proxyMemoryLogCapture is a slog handler that retains every record.
type proxyMemoryLogCapture struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *proxyMemoryLogCapture) Enabled(context.Context, slog.Level) bool { return true }

func (h *proxyMemoryLogCapture) Handle(_ context.Context, record slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, record.Clone())
	return nil
}

func (h *proxyMemoryLogCapture) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *proxyMemoryLogCapture) WithGroup(string) slog.Handler      { return h }

func (h *proxyMemoryLogCapture) snapshot() []slog.Record {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]slog.Record(nil), h.records...)
}

// proxyMemoryLogRecordText flattens a record's message and attribute values
// into one string for content scans; nested groups are walked.
func proxyMemoryLogRecordText(record slog.Record) string {
	var b strings.Builder
	b.WriteString(record.Message)
	record.Attrs(func(a slog.Attr) bool {
		proxyMemoryLogAppendAttr(&b, a)
		return true
	})
	return b.String()
}

func proxyMemoryLogAppendAttr(b *strings.Builder, a slog.Attr) {
	if a.Value.Kind() == slog.KindGroup {
		for _, inner := range a.Value.Group() {
			proxyMemoryLogAppendAttr(b, inner)
		}
		return
	}
	b.WriteString(" ")
	b.WriteString(a.Key)
	b.WriteString("=")
	b.WriteString(a.Value.String())
}

// TestProxyMemoryGeneration_RejectsMissingPrerequisites pins design §14.3's
// compile-time prerequisites: enabling proxy-memory without a durable Bun DB or
// without the background auxiliary client rejects the candidate generation and
// publishes no bootstrap producer.
func TestProxyMemoryGeneration_RejectsMissingPrerequisites(t *testing.T) {
	ctx := context.Background()

	t.Run("no durable store", func(t *testing.T) {
		rt, err := NewProcess(ctx, ProcessInput{Logger: slog.Default()})
		require.NoError(t, err)
		t.Cleanup(func() { _ = rt.Close() })
		out, err := rt.CompileGeneration(ctx, GenerationInput{
			Registrations:    proxyMemoryRegistration(t, true),
			BackgroundClient: proxyMemoryFakeBackgroundClient{},
		})
		require.Error(t, err)
		require.Contains(t, err.Error(), "durable SQLite or PostgreSQL continuity store")
		require.Nil(t, out.CorePorts.ConversationBootstrap)
	})

	t.Run("no background client", func(t *testing.T) {
		h := newProxyMemoryTestHarness(t)
		out, err := h.rt.CompileGeneration(ctx, GenerationInput{Registrations: proxyMemoryRegistration(t, true)})
		require.Error(t, err)
		require.Contains(t, err.Error(), "background auxiliary client")
		require.Nil(t, out.CorePorts.ConversationBootstrap)
	})

	t.Run("disabled registration compiles off", func(t *testing.T) {
		h := newProxyMemoryTestHarness(t)
		out, err := h.rt.CompileGeneration(ctx, h.generationInput(proxyMemoryRegistration(t, false)))
		require.NoError(t, err)
		require.Nil(t, out.CorePorts.ConversationBootstrap)
		require.Nil(t, out.HostProjections.ProxyMemoryHTTP)
	})

	t.Run("memory-only continuity without a durable store", func(t *testing.T) {
		// Requirement 1.7 / design §14.3: a real in-memory continuity authority
		// is a supported process setup, but on its own it cannot back
		// proxy-memory. With no Bun DB the process owns no concrete store, and
		// the composition seam would otherwise hand its typed-nil concrete
		// store to the capture, worker and service as a non-nil interface. The
		// concrete prerequisite must reject the generation and publish nothing.
		continuity, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{MaxLegs: 4})
		require.NoError(t, err)
		rt, err := NewProcess(ctx, ProcessInput{Logger: slog.Default(), ContinuityStore: continuity})
		require.NoError(t, err)
		t.Cleanup(func() { _ = rt.Close() })
		require.True(t, rt.bootstrapAuthority, "memory continuity still certifies lifecycle cleanup authority")

		out, err := rt.CompileGeneration(ctx, GenerationInput{
			Registrations:    proxyMemoryRegistration(t, true),
			BackgroundClient: proxyMemoryFakeBackgroundClient{},
		})
		require.Error(t, err)
		require.Contains(t, err.Error(), "durable SQLite or PostgreSQL continuity store")
		require.Nil(t, out.CorePorts.ConversationBootstrap, "no feature bootstrap may be published without a durable store")
		require.Nil(t, out.HostProjections.ProxyMemoryHTTP, "no HTTP projection may be published without a durable store")
	})

	t.Run("durable composition still composes", func(t *testing.T) {
		// Positive control for the concrete durable-store prerequisite: the
		// same enabled registration over a real durable composition (a Bun DB
		// owning the proxy-memory store, paired with its continuity store)
		// must keep composing, so the stricter prerequisite never rejects a
		// working setup. A memory continuity authority paired with a Bun DB is
		// rejected earlier by the process bootstrap-authority binding, not by
		// this prerequisite, so it cannot serve as the over-rejection control.
		bunDB := openProxyMemoryBunDB(t)
		continuity, err := bunstore.New(bunDB)
		require.NoError(t, err)
		rt, err := NewProcess(ctx, ProcessInput{
			Logger: slog.Default(), BunDB: bunDB, ContinuityStore: continuity,
			FeatureDatabases: &proxyMemoryTestDatabases{database: bunDB, identity: "test-memory"},
		})
		require.NoError(t, err)
		t.Cleanup(func() { _ = rt.Close() })

		out, err := rt.CompileGeneration(ctx, GenerationInput{
			Registrations:    proxyMemoryRegistration(t, true),
			BackgroundClient: proxyMemoryFakeBackgroundClient{},
		})
		require.NoError(t, err)
		require.Nil(t, out.CorePorts.ConversationBootstrap,
			"proxy memory contributes recall through the request-transform plane")
		require.NotNil(t, out.HostProjections.ProxyMemoryHTTP)
	})
}

// TestProxyMemoryCaptureOrderIsIndependentOfCompletionClock pins that equal
// admission snapshots are ordered by the source-sequence CAS, not by the
// activity timestamp: whichever release completes last, exactly one snapshot is
// stored, the other is stale, and a later completion clock never replaces the
// stored transcript.
func TestProxyMemoryCaptureOrderIsIndependentOfCompletionClock(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		t.Run(map[bool]string{false: "same-millisecond", true: "reverse-completion"}[reverse], func(t *testing.T) {
			h := newProxyMemoryTestHarness(t)
			aLeg := h.createALeg(t)
			ctx := context.Background()
			cfg := proxymemory.DefaultConfig()
			cfg.Principals = []string{proxyMemoryTestPrincipal}
			capture := proxymemory.NewCapture(cfg, h.memory, h.rt.proxyMemoryMetrics, func() time.Time { return h.now }, slog.Default(), nil)
			old := proxyMemoryTestAcceptedCall()
			old.Messages = []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("old snapshot")}}}
			newer := proxyMemoryTestAcceptedCall()
			newer.Messages = append(old.Messages, aipapi.Message{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("new conversation state")}})
			observers := make([]response.StreamObserver, 0, 2)
			for i, call := range []*aipapi.Call{old, newer} {
				trace := []string{"old", "new"}[i]
				sessionView := proxyMemoryTestSession(aLeg)
				sessionView.AuthoritativeFirstTurn = i == 0
				require.NoError(t, capture.Handle(ctx, call, request.RequestMeta{
					TraceID: trace, Scope: proxyMemoryTestScope(), Session: sessionView,
				}, request.Services{}))
				obs, err := capture.Open(ctx, response.StreamMeta{TraceID: trace, ALegID: aLeg}, response.Services{})
				require.NoError(t, err)
				observers = append(observers, obs)
			}
			if reverse {
				observers[0], observers[1] = observers[1], observers[0]
			}
			for _, obs := range observers {
				require.NoError(t, obs.Finish(ctx, response.OutcomeSuccessReleased))
				if reverse {
					h.now = h.now.Add(time.Second)
				}
			}
			require.NoError(t, capture.Start(ctx))
			require.NoError(t, capture.Stop(ctx))
			var transcript string
			require.NoError(t, h.db.NewRaw(`SELECT transcript FROM proxy_memory_sessions WHERE a_leg_id = ?`, aLeg).Scan(ctx, &transcript))
			// The first release that reaches the writer is stored; an equal
			// admission sequence is stale afterwards, whatever its completion
			// clock says.
			if reverse {
				require.Contains(t, transcript, "new conversation state",
					"a later completion clock must not let the older snapshot replace the stored transcript")
			} else {
				require.NotContains(t, transcript, "new conversation state",
					"an equal-sequence snapshot must not replace the stored transcript")
			}
			require.Equal(t, 1.0, h.counter(t, proxymemory.EventCaptureRecorded),
				"equal admission snapshots must not both be recorded")
			require.Equal(t, 1.0, h.counter(t, proxymemory.EventCaptureStale))
		})
	}
}

func TestProxyMemoryFirstProjectDecisionGovernsCaptureAcrossReload(t *testing.T) {
	for _, initialProject := range []bool{false, true} {
		t.Run(map[bool]string{false: "no-project", true: "project-pinned"}[initialProject], func(t *testing.T) {
			h := newProxyMemoryTestHarness(t)
			first := &aipapi.Call{}
			if initialProject {
				first = proxyMemoryTestAcceptedCall()
			}
			firstGeneration := h.compileGeneration(t, proxyMemoryRegistration(t, true))
			firstCapture := proxyMemoryCaptureTransform(firstGeneration)
			require.NotNil(t, firstCapture)
			aLeg := h.createALeg(t)
			firstSession := proxyMemoryTestSession(aLeg)
			firstSession.AuthoritativeFirstTurn = true
			require.NoError(t, firstCapture.Handle(context.Background(), first, request.RequestMeta{
				TraceID: "first", Scope: proxyMemoryTestScope(), Session: firstSession,
			}, request.Services{}))
			require.NoError(t, firstCapture.Start(context.Background()))
			require.NoError(t, firstCapture.Stop(context.Background()))

			// A later generation sees the first turn's durable project decision;
			// changing the workspace on a resumed request cannot move the binding.
			secondGeneration := h.compileGeneration(t, proxyMemoryRegistration(t, true))
			capture := proxyMemoryCaptureTransform(secondGeneration)
			require.NotNil(t, capture)
			later := proxyMemoryTestAcceptedCall()
			later.Instructions[0].Parts[0] = aipapi.TextPart("Working directory: /different/project")
			require.NoError(t, capture.Handle(context.Background(), later, request.RequestMeta{
				TraceID: "later", Scope: proxyMemoryTestScope(), Session: proxyMemoryTestSession(aLeg),
			}, request.Services{}))
			obs, err := capture.Open(context.Background(), response.StreamMeta{TraceID: "later", ALegID: aLeg}, response.Services{})
			require.NoError(t, err)
			require.NoError(t, obs.Finish(context.Background(), response.OutcomeSuccessReleased))
			require.NoError(t, capture.Start(context.Background()))
			require.NoError(t, capture.Stop(context.Background()))
			var keys []string
			require.NoError(t, h.db.NewRaw(`SELECT project_key FROM proxy_memory_sessions WHERE a_leg_id = ?`, aLeg).Scan(context.Background(), &keys))
			if initialProject {
				require.Equal(t, []string{proxymemory.ProjectKey(proxyMemoryTestLabel)}, keys)
			} else {
				require.Empty(t, keys, "a later project must not activate capture for a no-project session")
			}
		})
	}
}

func TestProxyMemoryFirstTurnDecisionReplaysAcrossGenerationReload(t *testing.T) {
	h := newProxyMemoryTestHarness(t)
	firstCall := proxyMemoryTestAcceptedCall()
	project := h.seedAcceptedSummary(t, firstCall)
	aLegID := h.createALeg(t)

	firstGeneration := h.compileGeneration(t, proxyMemoryRegistration(t, true))
	firstCapture := proxyMemoryCaptureTransform(firstGeneration)
	require.NotNil(t, firstCapture)
	firstSession := proxyMemoryTestSession(aLegID)
	firstSession.AuthoritativeFirstTurn = true
	require.NoError(t, firstCapture.Handle(context.Background(), firstCall, request.RequestMeta{
		TraceID: "first-turn", Scope: proxyMemoryTestScope(), Session: firstSession,
	}, request.Services{}))
	require.NotEmpty(t, firstCall.Instructions[0].Parts[0].Text)
	require.Contains(t, firstCall.Instructions[0].Parts[0].Text, proxyMemoryTestSummary)
	require.Contains(t, firstCall.Instructions[0].Parts[0].Text, project.Key)

	// The first generation has not started its writer, so the first-turn decision
	// is queued but not durable. The bank moves on to a different summary before
	// the next generation compiles, so a decision re-frozen from the current
	// store would be visible.
	const newerSummary = "a newer bank summary published after the first turn"
	seedProxyMemorySummary(t, h.memory, proxymemory.Owner{
		TenantID:    proxyMemoryTestTenant,
		PrincipalID: proxyMemoryTestPrincipal,
		ProjectKey:  project.Key,
	}, project.Label, newerSummary, h.now.Add(time.Minute).UnixMilli(), h.createALeg(t))
	published, _, err := h.memory.PublishedSummary(context.Background(), proxymemory.Owner{
		TenantID: proxyMemoryTestTenant, PrincipalID: proxyMemoryTestPrincipal, ProjectKey: project.Key,
	})
	require.NoError(t, err)
	require.Equal(t, newerSummary, published, "the current bank must publish the newer summary")

	// A later generation must replay that exact frozen decision from the
	// process-owned cache rather than read the newer bank snapshot.
	secondGeneration := h.compileGeneration(t, proxyMemoryRegistration(t, true))
	secondCapture := proxyMemoryCaptureTransform(secondGeneration)
	require.NotNil(t, secondCapture)
	resumedCall := proxyMemoryTestAcceptedCall()
	resumedSession := proxyMemoryTestSession(aLegID)
	require.NoError(t, secondCapture.Handle(context.Background(), resumedCall, request.RequestMeta{
		TraceID: "resumed-turn", Scope: proxyMemoryTestScope(), Session: resumedSession,
	}, request.Services{}))
	require.Equal(t, firstCall.Instructions[0], resumedCall.Instructions[0],
		"a generation reload must preserve the exact first-turn injection")
	require.NotContains(t, resumedCall.Instructions[0].Parts[0].Text, newerSummary,
		"the resumed request must replay the frozen decision, not the newer bank snapshot")
}

func proxyMemoryCaptureTransform(out GenerationOutput) *proxymemory.Capture {
	for _, transform := range aipfeature.Get(out.Planes, aipfeature.PlaneRequestTransforms) {
		if capture, ok := transform.(*proxymemory.Capture); ok {
			return capture
		}
	}
	return nil
}

func TestProxyMemoryGeneration_RequestTransformRecallsPublishedSummary(t *testing.T) {
	h := newProxyMemoryTestHarness(t)
	call := proxyMemoryTestAcceptedCall()
	project := h.seedAcceptedSummary(t, call)
	out := h.compileGeneration(t, proxyMemoryRegistration(t, true))
	require.Nil(t, out.CorePorts.ConversationBootstrap,
		"proxy memory recall belongs to the request transform, not conversation bootstrap")

	capture := proxyMemoryCaptureTransform(out)
	require.NotNil(t, capture)
	aLegID := h.createALeg(t)
	turn := proxyMemoryTestSession(aLegID)
	turn.AuthoritativeFirstTurn = true
	require.NoError(t, capture.Handle(context.Background(), call, request.RequestMeta{
		TraceID: "first-turn", Scope: proxyMemoryTestScope(), Session: turn,
	}, request.Services{}))

	require.Contains(t, call.Instructions[0].Parts[0].Text, proxyMemoryTestSummary)
	require.Contains(t, call.Instructions[0].Parts[0].Text, project.Key)
	require.Empty(t, h.steering(t, aLegID), "recall must not persist a conversation overlay")
	require.Equal(t, 0, h.bootstrapRows(t, aLegID, proxymemory.ID))
	require.Equal(t, 1.0, h.counter(t, proxymemory.EventInjectMatched))
}

func TestProxyMemoryGeneration_ComposesWithModelSystemPrompt(t *testing.T) {
	h := newProxyMemoryTestHarness(t)
	call := proxyMemoryTestAcceptedCall()
	h.seedAcceptedSummary(t, call)

	registrations := append(promptRegistration(t), proxyMemoryRegistration(t, true)...)
	out := h.compileGeneration(t, registrations)
	require.NotNil(t, out.CorePorts.ConversationBootstrap)

	aLegID := h.createALeg(t)
	ctx := proxyMemoryTestContext(proxyMemoryTestScope(), proxyMemoryTestSession(aLegID))
	cause, err := out.CorePorts.ConversationBootstrap(ctx, aLegID, call, func() (runtime.InitialModelIntent, error) {
		return runtime.InitialModelIntent{Model: "logical"}, nil
	})
	require.NoError(t, err)
	require.Equal(t, modelsystemprompt.CauseSystemPrompt, cause, "memory composition preserves the model-system-prompt diagnostic cause")
	steering := h.steering(t, aLegID)
	require.Len(t, steering, 2, "only model-system-prompt overlays are persisted")
	require.Equal(t, "model-system-prompt.first", steering[0].OverlayID)
	require.Equal(t, "model-system-prompt.second", steering[1].OverlayID)

	turn := proxyMemoryTestSession(aLegID)
	turn.AuthoritativeFirstTurn = true
	capture := proxyMemoryCaptureTransform(out)
	require.NotNil(t, capture)
	require.NoError(t, capture.Handle(context.Background(), call, request.RequestMeta{
		TraceID: "first-turn", Scope: proxyMemoryTestScope(), Session: turn,
	}, request.Services{}))
	require.Contains(t, call.Instructions[0].Parts[0].Text, proxyMemoryTestSummary,
		"request-transform recall remains separate from stored bootstrap overlays")
	require.Len(t, h.steering(t, aLegID), 2)
	require.Equal(t, 1.0, h.counter(t, proxymemory.EventInjectMatched))

	secondLeg := h.createALeg(t)
	secondCtx := proxyMemoryTestContext(proxyMemoryTestScope(), proxyMemoryTestSession(secondLeg))
	_, err = out.CorePorts.ConversationBootstrap(secondCtx, secondLeg, call, func() (runtime.InitialModelIntent, error) {
		return runtime.InitialModelIntent{}, errors.New("private msp sentinel")
	})
	require.ErrorIs(t, err, conversationview.ErrBootstrapDecision)
	require.Empty(t, h.steering(t, secondLeg))
}

func TestProxyMemoryGeneration_RequestTransformSkipsResumedSessionWithoutDecision(t *testing.T) {
	h := newProxyMemoryTestHarness(t)
	call := proxyMemoryTestAcceptedCall()
	h.seedAcceptedSummary(t, call)
	out := h.compileGeneration(t, proxyMemoryRegistration(t, true))
	capture := proxyMemoryCaptureTransform(out)
	require.NotNil(t, capture)

	aLegID := h.createALeg(t)
	require.NoError(t, capture.Handle(context.Background(), call, request.RequestMeta{
		TraceID: "resumed-turn", Scope: proxyMemoryTestScope(), Session: proxyMemoryTestSession(aLegID),
	}, request.Services{}))
	require.NotContains(t, call.Instructions[0].Parts[0].Text, proxyMemoryTestSummary,
		"a resumed session without a frozen decision must not recall memory")
}

// TestProxyMemoryGeneration_ContributesCapturePlanes pins the plane
// contributions of design §14.3: the capture transform on the request-transform
// plane and the capture observer factory on the stream-observer plane.
func TestProxyMemoryGeneration_ContributesCapturePlanes(t *testing.T) {
	h := newProxyMemoryTestHarness(t)
	out := h.compileGeneration(t, proxyMemoryRegistration(t, true))

	require.True(t, proxyMemoryPlaneHasTransform(aipfeature.Get(out.Planes, aipfeature.PlaneRequestTransforms)),
		"the request-transform plane must carry the capture transform")
	require.True(t, proxyMemoryPlaneHasObserverFactory(aipfeature.Get(out.Planes, aipfeature.PlaneStreamObserverFactories)),
		"the stream-observer plane must carry the capture observer factory")
	require.True(t, proxyMemoryPlaneHasTransform(aipfeature.Get(out.Bundle.PlaneSet, aipfeature.PlaneRequestTransforms)),
		"the published bundle must carry the same frozen plane set")
	require.True(t, proxyMemoryPlaneHasObserverFactory(aipfeature.Get(out.Bundle.PlaneSet, aipfeature.PlaneStreamObserverFactories)))
}

func proxyMemoryPlaneHasTransform(transforms []request.Transform) bool {
	for _, transform := range transforms {
		if transform != nil && transform.ID() == proxymemory.CaptureHandlerID {
			return true
		}
	}
	return false
}

func proxyMemoryPlaneHasObserverFactory(factories []response.StreamObserverFactory) bool {
	for _, factory := range factories {
		if factory != nil && factory.ID() == proxymemory.CaptureHandlerID {
			return true
		}
	}
	return false
}

// TestProxyMemoryGeneration_AppendsCaptureAndWorkerLifecycles pins the two
// lifecycles of design §14.3: the capture writer stops only at generation
// Close, and the worker opts into PhaseQuiesce stop semantics.
func TestProxyMemoryGeneration_AppendsCaptureAndWorkerLifecycles(t *testing.T) {
	h := newProxyMemoryTestHarness(t)
	out := h.compileGeneration(t, proxyMemoryRegistration(t, true))
	require.GreaterOrEqual(t, len(out.Lifecycles), 2)

	tail := out.Lifecycles[len(out.Lifecycles)-2:]
	captureSafe, ok := tail[0].(interface{ SafeUnderCandidateOverlap() bool })
	require.True(t, ok, "the capture lifecycle must be overlap-safe")
	require.True(t, captureSafe.SafeUnderCandidateOverlap())
	_, captureQuiesces := tail[0].(interface{ QuiescePhase() bool })
	require.False(t, captureQuiesces, "the capture writer stops only at generation Close")

	worker, ok := tail[1].(*proxymemory.Worker)
	require.True(t, ok, "the worker must be the second proxy-memory lifecycle")
	require.True(t, worker.QuiescePhase())
	require.True(t, worker.SafeUnderCandidateOverlap())
}

// TestProxyMemoryGeneration_ProjectsHTTPHandler pins the generation projection:
// an enabled generation publishes the memory handler, a disabled one does not.
func TestProxyMemoryGeneration_ProjectsHTTPHandler(t *testing.T) {
	h := newProxyMemoryTestHarness(t)
	out := h.compileGeneration(t, proxyMemoryRegistration(t, true))
	require.NotNil(t, out.HostProjections.ProxyMemoryHTTP)

	off, err := h.rt.CompileGeneration(context.Background(), h.generationInput(proxyMemoryRegistration(t, false)))
	require.NoError(t, err)
	require.Nil(t, off.HostProjections.ProxyMemoryHTTP)
}

type proxyMemoryAdmissionFailure struct {
	proxymemory.Store
	err error
}

func (s *proxyMemoryAdmissionFailure) ReadAdmission(context.Context, proxymemory.AdmissionQuery) (proxymemory.AdmissionSnapshot, error) {
	return proxymemory.AdmissionSnapshot{}, s.err
}

// proxyMemoryFirstTurnWriteFailure makes the durable first-turn write fail in
// a composed generation. It is the only way a decision that was already
// admitted to the writer queue becomes terminal, so it drives the replay
// rules of the process-owned decision cache.
//
// The reached channel makes the failure observable without polling: the
// writer's drain completes before Stop returns, so the terminal state is
// recorded when the test continues.
type proxyMemoryFirstTurnWriteFailure struct {
	proxymemory.Store

	failWithError bool
	once          sync.Once
	reached       chan struct{}
}

func (s *proxyMemoryFirstTurnWriteFailure) CommitFirstTurn(context.Context, proxymemory.FirstTurnJob) (bool, error) {
	s.once.Do(func() { close(s.reached) })
	if s.failWithError {
		return false, errors.New("private first-turn write sentinel")
	}
	return false, nil
}

// TestProxyMemoryDroppedFirstTurnDecisionNeverReachesTheRequest pins the
// eligibility rule of design §9 on the composed path: a decision the bounded
// writer queue refuses is dropped before it can become durable, so it must
// not reach the model-bound request and must not count an injection.
func TestProxyMemoryDroppedFirstTurnDecisionNeverReachesTheRequest(t *testing.T) {
	h := newProxyMemoryTestHarness(t)
	call := proxyMemoryTestAcceptedCall()
	h.seedAcceptedSummary(t, call)
	out := h.compileGeneration(t, proxyMemoryRegistration(t, true))
	capture := proxyMemoryCaptureTransform(out)
	require.NotNil(t, capture)

	aLegID := h.createALeg(t)
	turn := proxyMemoryTestSession(aLegID)
	turn.AuthoritativeFirstTurn = true
	// The writer is stopped, so the bounded queue refuses the decision.
	require.NoError(t, capture.Stop(context.Background()))

	require.NoError(t, capture.Handle(context.Background(), call, request.RequestMeta{
		TraceID: "first-turn", Scope: proxyMemoryTestScope(), Session: turn,
	}, request.Services{}))

	require.NotContains(t, call.Instructions[0].Parts[0].Text, proxyMemoryTestSummary,
		"a dropped decision must not reach the model request")
	require.Equal(t, 0.0, h.counter(t, proxymemory.EventInjectMatched),
		"a dropped decision must not count an injection")
	require.Equal(t, 1.0, h.counter(t, proxymemory.EventInjectDroppedQueue),
		"the refused decision stays observable")
	require.Equal(t, 1.0, h.counter(t, proxymemory.EventInjectDecisionUnavailable),
		"the omitted decision stays observable")
}

// TestProxyMemoryFailedFirstTurnWriteIsNeverReplayedOnAResumedTurn pins the
// terminal state of a failed decision: the durable write failed, so the
// decision never becomes durable and a resumed turn must not replay it.
func TestProxyMemoryFailedFirstTurnWriteIsNeverReplayedOnAResumedTurn(t *testing.T) {
	for _, tc := range []struct {
		name          string
		failWithError bool
	}{
		{name: "write-error", failWithError: true},
		{name: "write-refused", failWithError: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newProxyMemoryTestHarness(t)
			call := proxyMemoryTestAcceptedCall()
			h.seedAcceptedSummary(t, call)
			failure := &proxyMemoryFirstTurnWriteFailure{failWithError: tc.failWithError, reached: make(chan struct{})}
			proxyMemoryInstallCompositionStore(t, func(store proxymemory.Store) proxymemory.Store {
				failure.Store = store
				return failure
			})
			out := h.compileGeneration(t, proxyMemoryRegistration(t, true))
			capture := proxyMemoryCaptureTransform(out)
			require.NotNil(t, capture)

			aLegID := h.createALeg(t)
			turn := proxyMemoryTestSession(aLegID)
			turn.AuthoritativeFirstTurn = true
			require.NoError(t, capture.Handle(context.Background(), call, request.RequestMeta{
				TraceID: "first-turn", Scope: proxyMemoryTestScope(), Session: turn,
			}, request.Services{}))
			// The decision was admitted to the queue, so the first turn may use
			// it before its durable write completes.
			require.Contains(t, call.Instructions[0].Parts[0].Text, proxyMemoryTestSummary)
			require.Equal(t, 1.0, h.counter(t, proxymemory.EventInjectMatched))

			// The writer fails the durable write. The drain completes before
			// Stop returns, so the failure is already recorded.
			require.NoError(t, capture.Start(context.Background()))
			<-failure.reached
			require.NoError(t, capture.Stop(context.Background()))

			resumed := &aipapi.Call{
				Items: []aipapi.Item{{
					Kind:    aipapi.ItemKindMessage,
					Role:    aipapi.RoleUser,
					Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "continue"}},
				}},
				PreviousResponseID: "response-one",
			}
			require.NoError(t, capture.Handle(context.Background(), resumed, request.RequestMeta{
				TraceID: "resumed-turn", Scope: proxyMemoryTestScope(), Session: proxyMemoryTestSession(aLegID),
			}, request.Services{}))
			require.Len(t, resumed.Items, 1, "a failed decision must not reach the resumed request")
			require.Equal(t, "response-one", resumed.PreviousResponseID,
				"a failed decision must not disturb the resumed request")
			require.Equal(t, 1.0, h.counter(t, proxymemory.EventInjectMatched),
				"a failed decision must never be replayed as an injection")
			require.Equal(t, 1.0, h.counter(t, proxymemory.EventInjectDecisionUnavailable),
				"the omitted decision stays observable")
		})
	}
}

// TestProxyMemoryEnabledFeatureRefusesAnEphemeralDatabase proves the
// composition refuses a non-durable memory database: the feature is enabled,
// so the candidate generation must fail instead of publishing a memory
// surface whose decisions never survive the process.
func TestProxyMemoryEnabledFeatureRefusesAnEphemeralDatabase(t *testing.T) {
	h := newProxyMemoryTestHarness(t)
	var node yaml.Node
	require.NoError(t, yaml.Unmarshal([]byte(
		"principals:\n  - "+proxyMemoryTestPrincipal+"\nextract_route: aux-extract\n"+
			"consolidate_route: aux-consolidate\nsqlite_path: \":memory:\"\n"), &node))
	registrations := []aipsdk.Registration{{
		Kind:    aipsdk.PluginKindFeature,
		ID:      proxymemory.ID,
		Enabled: true,
		Config:  aipsdk.ConfigPayload{Node: node},
	}}

	_, err := h.rt.CompileGeneration(context.Background(), h.generationInput(registrations))
	require.Error(t, err, "an in-memory memory database must not be composed")
	require.Contains(t, err.Error(), "sqlite_path")
}

// proxyMemoryBackendFixture is one composed proxy-memory generation driven
// through the real executor with the recording backend: the durable topology,
// the secure session manager, the generation's request planes and the isolated
// memory database of the §16 sentinel. Every turn it serves is a real
// backend-bound request, not a direct transform call.
type proxyMemoryBackendFixture struct {
	executor   *runtime.Executor
	backend    *sentinelBackend
	memory     *proxymemorystore.Store
	continuity *bunstore.Store
	registry   *prometheus.Registry
	start      func(context.Context) error
	stop       func(context.Context) error
}

// newProxyMemoryBackendFixture composes the generation and wires the real
// executor over it. The composition store decorator is installed by the
// caller before this constructor runs, because the seam is read when the
// generation is compiled.
func newProxyMemoryBackendFixture(t *testing.T) *proxyMemoryBackendFixture {
	t.Helper()
	ctx := context.Background()
	continuityDB := certificationOpenSQLite(t,
		certificationSQLiteDSN(filepath.Join(t.TempDir(), "proxy-memory-backend.db")))
	// The memory store owns its own database, as the production composition
	// does: a memory transaction on the continuity file would contend with
	// the foreground continuity writes.
	memoryDB := certificationOpenSQLite(t,
		certificationSQLiteDSN(filepath.Join(t.TempDir(), "proxy-memory-backend-memory.db")))
	require.NoError(t, proxymemorystore.EnsureSchema(ctx, memoryDB))
	continuity, err := bunstore.New(continuityDB)
	require.NoError(t, err)
	registry := prometheus.NewRegistry()
	rt, err := NewProcess(ctx, ProcessInput{
		Logger:           slog.Default(),
		BunDB:            continuityDB,
		ContinuityStore:  continuity,
		MetricsRegistry:  registry,
		FeatureDatabases: &proxyMemoryTestDatabases{database: memoryDB, identity: "backend-memory"},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = rt.Close() })
	out, err := rt.CompileGeneration(ctx, GenerationInput{
		Registrations:    proxyMemoryRegistration(t, true),
		BackgroundClient: proxyMemoryFakeBackgroundClient{},
	})
	require.NoError(t, err)
	backend := &sentinelBackend{}
	captureLife := out.Lifecycles[len(out.Lifecycles)-2]
	return &proxyMemoryBackendFixture{
		executor:   sentinelExecutor(t, continuityDB, out, backend),
		backend:    backend,
		memory:     proxymemorystore.New(memoryDB, proxymemorystore.NewALegReader(continuityDB)),
		continuity: continuity,
		registry:   registry,
		start:      captureLife.Start,
		stop:       captureLife.Stop,
	}
}

// publishBank publishes a bank summary for the test owner through the real
// store, so the next first turn has a published summary to recall.
func (f *proxyMemoryBackendFixture) publishBank(t *testing.T, summary string) {
	t.Helper()
	project, ok := proxymemory.DeriveProject(sentinelCodexTurn("proxy-memory-seed"))
	require.True(t, ok)
	leg, err := f.continuity.CreateALeg(context.Background(), "")
	require.NoError(t, err)
	seedProxyMemorySummary(t, f.memory, proxymemory.Owner{
		TenantID: sentinelTenant, PrincipalID: sentinelPrincipalA, ProjectKey: project.Key,
	}, project.Label, summary, time.Now().UnixMilli(), leg.ALegID)
}

// serve drives one turn through the real executor and returns the call the
// backend observed.
func (f *proxyMemoryBackendFixture) serve(t *testing.T, call *aipapi.Call) aipapi.Call {
	t.Helper()
	return sentinelServe(t, f.executor, sentinelScope(sentinelPrincipalA), call, f.backend)
}

// proxyMemoryCallCarriesMemory reports whether the backend-bound call carries
// the memory text in any content carrier: its instructions, its items or its
// messages. The walk is typed: it inspects the message parts and the item
// content parts, never a rendered body.
func proxyMemoryCallCarriesMemory(call aipapi.Call, needle string) bool {
	for _, message := range call.Instructions {
		for _, part := range message.Parts {
			if part.Kind == aipapi.PartText && strings.Contains(part.Text, needle) {
				return true
			}
		}
	}
	for _, item := range call.Items {
		for _, part := range item.Content {
			if part.Kind == aipapi.ContentPartText && strings.Contains(part.Text, needle) {
				return true
			}
		}
	}
	for _, message := range call.Messages {
		for _, part := range message.Parts {
			if part.Kind == aipapi.PartText && strings.Contains(part.Text, needle) {
				return true
			}
		}
	}
	return false
}

// proxyMemoryCallTexts returns the text parts of the backend-bound call's
// messages, in order.
func proxyMemoryCallTexts(call aipapi.Call) []string {
	texts := make([]string, 0, len(call.Messages))
	for _, message := range call.Messages {
		for _, part := range message.Parts {
			if part.Kind == aipapi.PartText {
				texts = append(texts, part.Text)
			}
		}
	}
	return texts
}

// TestProxyMemoryBackendBoundFirstTurnDecisionEligibility proves the F1
// eligibility rule at the backend boundary: a decision the bounded writer
// queue refuses never reaches the model, and a decision whose durable write
// failed is never replayed — and never reconstructed from the current bank —
// on a resumed request of the same secure session.
func TestProxyMemoryBackendBoundFirstTurnDecisionEligibility(t *testing.T) {
	const (
		clientSession = "proxy-memory-backend-session"
		oldSummary    = "kale standup notes summary"
		newSummary    = "parrot standup notes summary"
	)

	t.Run("a refusing writer keeps the overlay out of the backend-bound request", func(t *testing.T) {
		f := newProxyMemoryBackendFixture(t)
		f.publishBank(t, oldSummary)
		// The writer is stopped, so the bounded queue refuses the decision.
		require.NoError(t, f.stop(context.Background()))

		observed := f.serve(t, sentinelCodexTurn(clientSession))

		require.False(t, proxyMemoryCallCarriesMemory(observed, oldSummary),
			"a refused decision must not reach the backend-bound request")
		require.Equal(t, 0.0, proxyMemoryCounterValue(t, f.registry, proxymemory.EventInjectMatched),
			"a refused decision must not count an injection")
	})

	for _, tc := range []struct {
		name          string
		failWithError bool
	}{
		{name: "a failed durable write", failWithError: true},
		{name: "a refused durable write", failWithError: false},
	} {
		t.Run(tc.name+" is never replayed on the resumed request", func(t *testing.T) {
			failure := &proxyMemoryFirstTurnWriteFailure{failWithError: tc.failWithError, reached: make(chan struct{})}
			proxyMemoryInstallCompositionStore(t, func(store proxymemory.Store) proxymemory.Store {
				failure.Store = store
				return failure
			})
			f := newProxyMemoryBackendFixture(t)
			f.publishBank(t, oldSummary)
			require.NoError(t, f.start(context.Background()))

			// The first turn is admitted to the queue, so it may use the
			// decision: the durable write can still fail afterwards.
			firstCall := sentinelCodexTurn(clientSession)
			first := f.serve(t, firstCall)
			require.True(t, proxyMemoryCallCarriesMemory(first, oldSummary),
				"the admitted first turn recalls the published summary")
			resumeToken := firstCall.Session.ResumeToken
			require.NotEmpty(t, resumeToken,
				"the executor must issue the resume token of the secure session")

			// The writer fails the durable write. The drain completes before
			// Stop returns, so the terminal state is recorded.
			<-failure.reached
			require.NoError(t, f.stop(context.Background()))

			// The current bank publishes a different summary now, so a
			// reconstructed decision would be visible.
			f.publishBank(t, newSummary)
			published, _, err := f.memory.PublishedSummary(context.Background(), proxymemory.Owner{
				TenantID: sentinelTenant, PrincipalID: sentinelPrincipalA,
				ProjectKey: proxymemory.ProjectKey(proxymemory.NormalizePath(sentinelProjectPath)),
			})
			require.NoError(t, err)
			require.Equal(t, newSummary, published, "the current bank must publish the new summary")

			// The resumed request carries the resume token of the secure
			// session, so the executor resumes the same A-leg; no SessionView
			// flag is set by hand.
			resumed := &aipapi.Call{
				Route:   aipapi.RouteIntent{Selector: "only:model"},
				Session: aipapi.SessionRef{ClientSessionID: clientSession, ResumeToken: resumeToken},
				Invocation: aipapi.Invocation{
					Operation: aipapi.OperationOpenAIResponses,
				},
				Items: []aipapi.Item{
					{
						Kind:    aipapi.ItemKindMessage,
						ID:      "item-original",
						Role:    aipapi.RoleUser,
						Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "continue"}},
					},
					{
						Kind:    aipapi.ItemKindMessage,
						ID:      "item-second",
						Role:    aipapi.RoleUser,
						Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "and then"}},
					},
				},
				PreviousResponseID: "response-one",
			}
			observed := f.serve(t, resumed)

			require.False(t, proxyMemoryCallCarriesMemory(observed, oldSummary),
				"a failed decision must not be replayed on the resumed request")
			require.False(t, proxyMemoryCallCarriesMemory(observed, newSummary),
				"a missing decision must not be reconstructed from the current bank")
			require.Empty(t, observed.Items, "the resumed request must not gain a memory item")
			require.Equal(t, []string{"continue", "and then"}, proxyMemoryCallTexts(observed),
				"the original item order must survive the request")
			require.Equal(t, "response-one", observed.PreviousResponseID,
				"the resumed request must keep its continuation parent")
			require.Equal(t, 1.0, proxyMemoryCounterValue(t, f.registry, proxymemory.EventInjectMatched),
				"only the admitted first turn may count an injection")
		})
	}
}

func TestProxyMemoryCapture_LogHygiene(t *testing.T) {
	h := newProxyMemoryTestHarness(t)
	call := proxyMemoryTestAcceptedCall()
	project, ok := proxymemory.DeriveProject(call)
	require.True(t, ok)
	failure := errors.New("private admission sentinel")
	proxyMemoryInstallCompositionStore(t, func(store proxymemory.Store) proxymemory.Store {
		return &proxyMemoryAdmissionFailure{Store: store, err: failure}
	})
	out := h.compileGeneration(t, proxyMemoryRegistration(t, true))
	capture := proxyMemoryCaptureTransform(out)
	require.NotNil(t, capture)

	aLegID := h.createALeg(t)
	turn := proxyMemoryTestSession(aLegID)
	turn.AuthoritativeFirstTurn = true
	require.NoError(t, capture.Handle(context.Background(), call, request.RequestMeta{
		TraceID: "first-turn", Scope: proxyMemoryTestScope(), Session: turn,
	}, request.Services{}), "an admission read failure must fail open")
	require.Equal(t, 1.0, h.counter(t, proxymemory.EventInjectError))
	require.Equal(t, 1.0, h.counter(t, proxymemory.EventStoreUnavailable))
	require.NotContains(t, call.Instructions[0].Parts[0].Text, proxyMemoryTestSummary)

	records := h.logs.snapshot()
	require.NotEmpty(t, records, "the admission failure must leave an observable diagnostic")
	for _, record := range records {
		text := proxyMemoryLogRecordText(record)
		require.NotContains(t, text, project.Label)
		require.NotContains(t, text, proxyMemoryTestPrincipal)
		require.NotContains(t, text, aLegID)
		require.NotContains(t, text, proxyMemoryTestSummary)
		require.NotContains(t, text, failure.Error())
	}
}
