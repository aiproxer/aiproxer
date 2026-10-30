package featurehost

// Task 5.5 end-to-end isolation sentinel (design §16 sentinel, Requirements
// 1.5, 2.1, 2.2, 6.1, 8.3, 8.4).
//
// This is a composed-acceptance test, not a unit test: it builds ONE real
// generation through CompileGeneration over a durable SQLite topology whose
// proxy-memory store owns an isolated database, as the production composition
// does, drives real canonical turns through a real generic-core executor with a
// stub backend, and observes only production surfaces. Nothing here
// re-implements a production decision:
//
//  1. principal A's Codex-shaped turn is captured by the composed capture plane
//     and released successfully;
//  2. a manual Tick of the composed worker (captured through
//     proxyMemoryWorkerHook) runs extraction and consolidation with a canned
//     auxiliary client and the injected clock;
//  3. a new Claude-Code-shaped A-leg for principal A receives the published
//     summary as the proxy-memory overlay in the backend-bound request;
//  4. a same-path A-leg for principal B receives nothing, and the projected
//     HTTP handler lists nothing for B;
//  5. A's memory scope is forgotten, after which a further A session receives
//     nothing while B's isolation is untouched.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	continuitybunstore "github.com/aiproxer/aiproxer/internal/core/continuity/bunstore"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/core/securesession/adapters/aipapidenial"
	"github.com/aiproxer/aiproxer/internal/core/securesession/adapters/b2bualineage"
	ssbunstore "github.com/aiproxer/aiproxer/internal/core/securesession/adapters/bunstore"
	"github.com/aiproxer/aiproxer/internal/core/securesession/app"
	"github.com/aiproxer/aiproxer/internal/core/workspace"
	"github.com/aiproxer/aiproxer/internal/infra/proxymemorystore"
	"github.com/aiproxer/aiproxer/internal/plugins/features/proxymemory"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
	aipworkspace "github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"
	"gopkg.in/yaml.v3"
)

const (
	sentinelPrincipalA  = "alice"
	sentinelPrincipalB  = "bob"
	sentinelTenant      = "tenant-one"
	sentinelProjectPath = "/w/app"

	// sentinelSummary is the published memory summary the sentinel follows
	// from the canned phase 2 reply to the backend-bound overlay. All content
	// in this file is deliberately benign plain text: no secret-shaped value
	// ever appears in a staged diff.
	sentinelSummary = "kale standup notes summary"

	// sentinelRolePhase1/sentinelRolePhase2 mirror the phase 1 and phase 2
	// auxiliary request roles of design §7.2/§7.3 (unexported in the feature
	// package). The fake client answers by role; a role rename would make
	// Await fail loudly instead of silently serving the wrong phase.
	sentinelRolePhase1 = "proxy_memory_phase1"
	sentinelRolePhase2 = "proxy_memory_phase2"
)

// sentinelPhase1Reply and sentinelPhase2Reply are the canned auxiliary model
// replies of the two phases. Both satisfy their parsers' strict contracts:
// phase 1 carries non-blank raw_memory/rollout_summary and a normalized slug,
// phase 2 stays far below the 256 KiB memory_md bound.
const (
	sentinelPhase1Reply = `{"raw_memory":"kale standup raw memory","rollout_summary":"kale standup rollout summary","rollout_slug":"kale-standup-notes"}`
	sentinelPhase2Reply = `{"memory_md":"# Memory\nkale standup notes","memory_summary":"` + sentinelSummary + `"}`
)

// sentinelRegistrations is ONE enabled proxy-memory registration allowing both
// principals, so principal B passes eligibility and isolation is proved by
// owner scoping rather than by the allowlist.
func sentinelRegistrations(t *testing.T) []aipsdk.Registration {
	t.Helper()
	var node yaml.Node
	require.NoError(t, yaml.Unmarshal([]byte(
		"principals:\n  - "+sentinelPrincipalA+"\n  - "+sentinelPrincipalB+
			"\nextract_route: aux-extract\nconsolidate_route: aux-consolidate\n"), &node))
	return []aipsdk.Registration{{
		Kind:    aipsdk.PluginKindFeature,
		ID:      proxymemory.ID,
		Enabled: true,
		Config:  aipsdk.ConfigPayload{Node: node},
	}}
}

// sentinelClock is the injected generation clock: the capture timestamps and
// the worker ticks both read it, so a 7 h advance (past the 6 h min_idle
// default) moves one captured session into the worker's claimable window
// without any wall-clock sleep.
type sentinelClock struct {
	mu  sync.Mutex
	now time.Time
}

func newSentinelClock() *sentinelClock {
	return &sentinelClock{now: time.UnixMilli(1_750_000_000_000).UTC()}
}

func (c *sentinelClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *sentinelClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// sentinelAuxClient is the fake background auxiliary client: it records the
// role of every submission and answers the matching canned phase reply.
type sentinelAuxClient struct {
	mu     sync.Mutex
	next   int
	phases map[auxiliary.JobID]string
}

func (c *sentinelAuxClient) SubmitCollect(_ context.Context, req auxiliary.Request, _ auxiliary.SubmitOptions) (auxiliary.JobID, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.next++
	id := auxiliary.JobID("sentinel-job-" + strconv.Itoa(c.next))
	if c.phases == nil {
		c.phases = make(map[auxiliary.JobID]string)
	}
	c.phases[id] = req.Role
	return id, nil
}

func (c *sentinelAuxClient) Await(_ context.Context, id auxiliary.JobID) (aipapi.Collected, error) {
	c.mu.Lock()
	role := c.phases[id]
	c.mu.Unlock()
	var text string
	switch role {
	case sentinelRolePhase1:
		text = sentinelPhase1Reply
	case sentinelRolePhase2:
		text = sentinelPhase2Reply
	default:
		return aipapi.Collected{}, fmt.Errorf("proxy-memory sentinel: unexpected auxiliary request role %q", role)
	}
	var collected aipapi.Collected
	collected.Text.WriteString(text)
	return collected, nil
}

func (c *sentinelAuxClient) Forget(auxiliary.JobID) {}

// sentinelBackend is the stub inference backend: every Open records the
// backend-bound canonical call (the request after projection) and releases an
// empty successful response.
type sentinelBackend struct {
	mu    sync.Mutex
	calls []aipapi.Call
}

func (b *sentinelBackend) open(_ context.Context, call aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
	b.mu.Lock()
	b.calls = append(b.calls, aipapi.CloneCall(call))
	b.mu.Unlock()
	return aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventResponseFinished},
	}), nil
}

func (b *sentinelBackend) backend() execbackend.Backend {
	return execbackend.Backend{
		Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityTools),
		Open: b.open,
	}
}

func (b *sentinelBackend) callCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.calls)
}

func (b *sentinelBackend) callAt(t *testing.T, index int) aipapi.Call {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	require.Greater(t, len(b.calls), index, "the stub backend recorded no call for this turn")
	return b.calls[index]
}

// sentinelServe drives one canonical client turn through the executor under
// the given principal scope and returns the backend-bound call this turn
// produced. Exactly one backend attempt per turn is required, so the returned
// call cannot be attributed to another turn.
func sentinelServe(t *testing.T, ex *runtime.Executor, scopeView scope.PrincipalScopeView, call *aipapi.Call, backend *sentinelBackend) aipapi.Call {
	t.Helper()
	before := backend.callCount()
	stream, err := ex.Execute(scope.WithScope(context.Background(), scopeView), call)
	require.NoError(t, err)
	_, err = aipapi.Collect(context.Background(), stream)
	require.NoError(t, err)
	require.Equal(t, before+1, backend.callCount(), "one turn must open exactly one backend attempt")
	return backend.callAt(t, before)
}

// sentinelScope is one authenticated, memory-eligible client principal.
func sentinelScope(principal string) scope.PrincipalScopeView {
	return scope.PrincipalScopeView{
		SubjectKind: scope.SubjectHuman,
		PrincipalID: scope.Known(principal),
		TenantID:    scope.Known(sentinelTenant),
		Origin:      scope.OriginClient,
	}
}

// sentinelCodexTurn is a Codex-shaped client turn: the working directory is
// only inside a start-anchored environment-context user part.
func sentinelCodexTurn(clientSessionID string) *aipapi.Call {
	return &aipapi.Call{
		Route:   aipapi.RouteIntent{Selector: "only:model"},
		Session: aipapi.SessionRef{ClientSessionID: clientSessionID},
		Invocation: aipapi.Invocation{
			Operation: aipapi.OperationOpenAIResponses,
		},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("<environment_context><cwd>" + sentinelProjectPath + "</cwd><shell>bash</shell></environment_context>")},
		}},
	}
}

// sentinelClaudeTurn is a Claude-Code-shaped client turn: the working
// directory is a "Working directory:" instruction line.
func sentinelClaudeTurn(clientSessionID string) *aipapi.Call {
	return &aipapi.Call{
		Route:   aipapi.RouteIntent{Selector: "only:model"},
		Session: aipapi.SessionRef{ClientSessionID: clientSessionID},
		Invocation: aipapi.Invocation{
			Operation: aipapi.OperationOpenAIResponses,
		},
		Instructions: []aipapi.Message{{
			Role:  aipapi.RoleSystem,
			Parts: []aipapi.Part{aipapi.TextPart("Working directory: " + sentinelProjectPath)},
		}},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("continue")},
		}},
	}
}

// sentinelCallCarries reports whether any text part of the backend-bound call
// (instructions or messages) contains needle.
func sentinelCallCarries(call aipapi.Call, needle string) bool {
	messages := make([]aipapi.Message, 0, len(call.Instructions)+len(call.Messages))
	messages = append(messages, call.Instructions...)
	messages = append(messages, call.Messages...)
	for _, message := range messages {
		for _, part := range message.Parts {
			if part.Kind == aipapi.PartText && strings.Contains(part.Text, needle) {
				return true
			}
		}
	}
	return false
}

// sentinelInstructionOverlay returns the first system instruction message of
// the backend-bound call whose text contains needle.
func sentinelInstructionOverlay(call aipapi.Call, needle string) (aipapi.Message, bool) {
	for _, message := range call.Instructions {
		if message.Role != aipapi.RoleSystem {
			continue
		}
		for _, part := range message.Parts {
			if part.Kind == aipapi.PartText && strings.Contains(part.Text, needle) {
				return message, true
			}
		}
	}
	return aipapi.Message{}, false
}

// sentinelMemoryRequest runs one authenticated request against the projected
// memory handler.
func sentinelMemoryRequest(h http.Handler, method, target string, scopeView scope.PrincipalScopeView) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	req = req.WithContext(scope.WithScope(req.Context(), scopeView))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// sentinelBankJSON is the wire shape of one bank status (design §11.2).
type sentinelBankJSON struct {
	Scope         string `json:"scope"`
	ProjectLabel  string `json:"project_label"`
	MemorySummary string `json:"memory_summary"`
}

type sentinelBanksJSON struct {
	Banks []sentinelBankJSON `json:"banks"`
}

// sentinelExecutor builds the generic-core executor the sentinel drives over
// the same durable topology the composed generation borrows (precedent:
// certificationExecutorWithWorkspace). The generation's frozen planes carry
// the capture transform/observer; the generation's conversation-view reader,
// request-transform recall and stub backend are wired the way the runtime bundle
// would.
func sentinelExecutor(t *testing.T, database *bun.DB, out GenerationOutput, backend *sentinelBackend) *runtime.Executor {
	t.Helper()
	ctx := context.Background()
	sessionStore, err := ssbunstore.NewWithContext(ctx, database)
	require.NoError(t, err)
	continuityStore, err := continuitybunstore.NewWithContext(ctx, database)
	require.NoError(t, err)
	fingerprintKey := certificationFingerprintKey()
	manager, err := app.NewManager(
		sessionStore,
		app.NewRandGenerator(fingerprintKey),
		b2bualineage.New(continuityStore),
		app.ManagerConfig{FingerprintKey: fingerprintKey, StoreDurable: true},
	)
	require.NoError(t, err)

	ex := runtime.TestExecutor()
	ex.SessionDenialMapper = aipapidenial.MapToSessionDenial
	ex.Store = continuityStore
	ex.SecureSession = manager
	ex.ConversationViewReader = out.CorePorts.ConversationViewReader
	ex.ConversationBootstrap = out.CorePorts.ConversationBootstrap
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(3)
	ex.LargeBodyGenerationID = "proxy-memory-sentinel-generation"
	ex.LargeBodyCandidateDomainGeneration = "proxy-memory-sentinel-domain"
	ex.DefaultBackend = "only"
	ex.Backends = map[string]execbackend.Backend{"only": backend.backend()}
	ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(ex.Bus, extensions.SnapshotOptions{
		Workspace:     workspace.NewResolverChain([]aipworkspace.Resolver{certificationWorkspaceResolver{}}),
		FeaturePlanes: out.Planes,
	})
	return ex
}

type proxyMemorySentinelWriteProbe struct {
	proxymemory.Store

	firstTurnCommitted bool
	firstTurnErr       error
	captureOutcome     proxymemory.UpsertOutcome
	captureErr         error
	// captured is closed after the first committed capture, so the sentinel
	// can wait for the writer without stopping it: a later first turn must
	// still be able to admit its decision to the queue.
	captured    chan struct{}
	captureOnce sync.Once
}

func (s *proxyMemorySentinelWriteProbe) CommitFirstTurn(ctx context.Context, job proxymemory.FirstTurnJob) (bool, error) {
	s.firstTurnCommitted, s.firstTurnErr = s.Store.CommitFirstTurn(ctx, job)
	return s.firstTurnCommitted, s.firstTurnErr
}

func (s *proxyMemorySentinelWriteProbe) UpsertSession(ctx context.Context, capture proxymemory.SessionCapture) (proxymemory.UpsertOutcome, error) {
	outcome, err := s.Store.UpsertSession(ctx, capture)
	// Only the first capture is recorded, so the sentinel's read of the
	// committed outcome never races a later write of the same field.
	s.captureOnce.Do(func() {
		s.captureOutcome, s.captureErr = outcome, err
		close(s.captured)
	})
	return outcome, err
}

type stalledProxyMemoryWriter struct {
	proxymemory.Store
	started  chan struct{}
	release  chan struct{}
	returned chan struct{}
}

func (s *stalledProxyMemoryWriter) CommitFirstTurn(ctx context.Context, job proxymemory.FirstTurnJob) (bool, error) {
	close(s.started)
	select {
	case <-s.release:
		defer close(s.returned)
		return s.Store.CommitFirstTurn(ctx, job)
	case <-ctx.Done():
		return false, ctx.Err()
	}
}

// TestProxyMemoryStalledFirstTurnWriterDoesNotBlockBackendProgress covers the
// composed request path, not just the request transform in isolation.
func TestProxyMemoryStalledFirstTurnWriterDoesNotBlockBackendProgress(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	database := certificationOpenSQLite(t, certificationSQLiteDSN(filepath.Join(t.TempDir(), "stalled-memory.db")))
	// SQLite has one writer; keep this composed contract on that supported topology.
	database.SetMaxOpenConns(1)
	continuity, err := continuitybunstore.New(database)
	require.NoError(t, err)
	process, err := NewProcess(ctx, ProcessInput{
		Logger: slog.Default(), BunDB: database, ContinuityStore: continuity,
		FeatureDatabases: &proxyMemoryTestDatabases{database: database, identity: "sentinel-memory"},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = process.Close() })
	probe := &stalledProxyMemoryWriter{
		Store:   proxymemorystore.New(database, proxymemorystore.NewALegReader(database)),
		started: make(chan struct{}),
		release: make(chan struct{}), returned: make(chan struct{}),
	}
	proxyMemoryInstallCompositionStore(t, func(store proxymemory.Store) proxymemory.Store {
		probe.Store = store
		return probe
	})
	out, err := process.CompileGeneration(ctx, GenerationInput{
		Registrations: sentinelRegistrations(t), BackgroundClient: &sentinelAuxClient{},
	})
	require.NoError(t, err)
	var capture captureLifecycle
	for _, lifecycle := range out.Lifecycles {
		if candidate, ok := lifecycle.(captureLifecycle); ok {
			capture = candidate
			break
		}
	}
	require.NotNil(t, capture.capture)
	require.NoError(t, capture.Start(ctx))
	t.Cleanup(func() {
		close(probe.release)
		stopCtx, stopCancel := context.WithTimeout(context.Background(), time.Second)
		defer stopCancel()
		if err := capture.Stop(stopCtx); err != nil {
			t.Errorf("capture stop: %v", err)
		}
	})

	backend := &sentinelBackend{}
	ex := sentinelExecutor(t, database, out, backend)
	done := make(chan error, 1)
	go func() {
		stream, err := ex.Execute(scope.WithScope(ctx, sentinelScope(sentinelPrincipalA)), sentinelClaudeTurn("stalled-memory"))
		if err == nil {
			_, err = aipapi.Collect(ctx, stream)
		}
		done <- err
	}()
	select {
	case <-probe.started:
	case <-ctx.Done():
		t.Fatal("first-turn persistence never reached the stalled writer")
	}
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatal("backend request waited for the stalled first-turn writer")
	}
	if backend.callCount() != 1 {
		t.Fatal("backend did not receive the request")
	}
	select {
	case <-probe.returned:
		t.Fatal("first-turn write returned before backend progress was observed")
	default:
	}
}

// TestProxyMemorySentinel_CrossPrincipalIsolationAndForget is the §16 sentinel:
// one composed generation, two principals over the same project path, a real
// extraction/consolidation cycle, a cross-harness overlay assertion, an HTTP
// isolation assertion and an end-to-end forget.
func TestProxyMemorySentinel_CrossPrincipalIsolationAndForget(t *testing.T) {
	ctx := context.Background()
	clock := newSentinelClock()
	aux := &sentinelAuxClient{}
	backend := &sentinelBackend{}
	registry := prometheus.NewRegistry()

	continuityDB := certificationOpenSQLite(t,
		certificationSQLiteDSN(filepath.Join(t.TempDir(), "proxy-memory-sentinel.db")))
	// The memory store lives in its own database, as the production
	// composition does: a memory transaction on the continuity file would
	// contend with the foreground continuity writes.
	memoryDB := certificationOpenSQLite(t,
		certificationSQLiteDSN(filepath.Join(t.TempDir(), "proxy-memory-sentinel-memory.db")))
	require.NoError(t, proxymemorystore.EnsureSchema(ctx, memoryDB))
	continuity, err := continuitybunstore.New(continuityDB)
	require.NoError(t, err)
	rt, err := NewProcess(ctx, ProcessInput{
		Logger:           slog.Default(),
		BunDB:            continuityDB,
		ContinuityStore:  continuity,
		MetricsRegistry:  registry,
		FeatureDatabases: &proxyMemoryTestDatabases{database: memoryDB, identity: "sentinel-memory"},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = rt.Close() })
	memoryStore := proxymemorystore.New(memoryDB, proxymemorystore.NewALegReader(continuityDB))
	writeProbe := &proxyMemorySentinelWriteProbe{Store: memoryStore, captured: make(chan struct{})}
	proxyMemoryInstallCompositionStore(t, func(store proxymemory.Store) proxymemory.Store {
		writeProbe.Store = store
		return writeProbe
	})

	var worker *proxymemory.Worker
	previousHook := proxyMemoryWorkerHook
	proxyMemoryWorkerHook = func(w *proxymemory.Worker) { worker = w }
	t.Cleanup(func() { proxyMemoryWorkerHook = previousHook })

	out, err := rt.CompileGeneration(ctx, GenerationInput{
		Registrations:    sentinelRegistrations(t),
		BackgroundClient: aux,
		NowFn:            clock.Now,
	})
	require.NoError(t, err)
	require.Nil(t, out.CorePorts.ConversationBootstrap,
		"proxy-memory recall belongs to the request-transform plane")
	require.NotNil(t, out.HostProjections.ProxyMemoryHTTP)
	require.NotNil(t, worker, "the worker hook must capture the composed worker")

	require.GreaterOrEqual(t, len(out.Lifecycles), 2)
	captureLife := out.Lifecycles[len(out.Lifecycles)-2]
	require.Equal(t, worker, out.Lifecycles[len(out.Lifecycles)-1],
		"the worker must be the last proxy-memory lifecycle")
	capturing := true
	stopCapture := func() {
		if !capturing {
			return
		}
		capturing = false
		require.NoError(t, captureLife.Stop(context.Background()))
	}
	t.Cleanup(stopCapture)
	require.NoError(t, captureLife.Start(ctx))

	ex := sentinelExecutor(t, continuityDB, out, backend)
	scopeA := sentinelScope(sentinelPrincipalA)
	scopeB := sentinelScope(sentinelPrincipalB)
	projectKey := proxymemory.ProjectKey(proxymemory.NormalizePath(sentinelProjectPath))
	ownerA := proxymemory.Owner{TenantID: sentinelTenant, PrincipalID: sentinelPrincipalA, ProjectKey: projectKey}

	// ---- step 1: principal A's Codex-shaped turn is captured ----
	codexBound := sentinelServe(t, ex, scopeA, sentinelCodexTurn("sentinel-a-codex"), backend)
	require.False(t, sentinelCallCarries(codexBound, sentinelSummary),
		"the first turn has no published summary and must not carry an overlay")

	// Wait for the capture writer to commit the released turn: the captured
	// session must be durable before the worker ticks and before the durable
	// assertions below. The writer stays alive, so the first turn of a later
	// session can still admit its decision to the queue.
	<-writeProbe.captured
	require.NoError(t, writeProbe.firstTurnErr)
	require.True(t, writeProbe.firstTurnCommitted, "the immutable first-turn decision must persist")
	require.NoError(t, writeProbe.captureErr)
	require.Equal(t, proxymemory.UpsertStored, writeProbe.captureOutcome)

	require.Equal(t, 1, sentinelCapturedSessions(t, memoryDB, ownerA),
		"exactly A's released turn must be captured and flushed")
	require.Equal(t, proxymemory.NormalizePath(sentinelProjectPath), sentinelCapturedLabel(t, memoryDB, ownerA))
	require.Equal(t, 1.0, proxyMemoryCounterValue(t, registry, proxymemory.EventInjectNoSummary),
		"the first turn has no published summary and commits its own no-match decision")

	// ---- step 2: 7 h later the worker extracts and consolidates ----
	//
	// The sentinel drives Tick directly on the hook-captured worker, the way
	// design §16 prescribes: the worker is intentionally not Started, so no
	// wall-clock ticker runs and every phase happens exactly when the test
	// asks for it. One tick already runs the whole order (purge, then
	// extraction, then consolidation), and the bank phase 1 dirties is
	// claimable immediately, so the first tick may complete both phases; the
	// second tick is a bounded no-op. The assertions below observe the
	// committed outcome rather than which tick produced it.
	clock.Advance(7 * time.Hour)
	worker.Tick(ctx)
	worker.Tick(ctx)

	published, _, err := memoryStore.PublishedSummary(ctx, ownerA)
	require.NoError(t, err, "the two ticks must publish A's bank summary")
	require.Equal(t, sentinelSummary, published)
	require.Equal(t, 1.0, proxyMemoryCounterValue(t, registry, proxymemory.EventPhase1Succeeded))
	require.Equal(t, 1.0, proxyMemoryCounterValue(t, registry, proxymemory.EventPhase2Published))
	require.Equal(t, 0.0, proxyMemoryCounterValue(t, registry, proxymemory.EventPhase1Failed))
	require.Equal(t, 0.0, proxyMemoryCounterValue(t, registry, proxymemory.EventPhase2Failed))

	// A's own HTTP control surface sees the bank (positive control for the
	// empty B list below).
	aBanks := sentinelListBanks(t, out.HostProjections.ProxyMemoryHTTP, scopeA)
	require.Len(t, aBanks, 1)
	require.Equal(t, projectKey, aBanks[0].Scope)
	require.Equal(t, proxymemory.NormalizePath(sentinelProjectPath), aBanks[0].ProjectLabel)

	// ---- step 3: a new Claude-Code-shaped A session receives the overlay ----
	claudeBound := sentinelServe(t, ex, scopeA, sentinelClaudeTurn("sentinel-a-claude"), backend)
	require.True(t, sentinelCallCarries(claudeBound, sentinelSummary),
		"a new eligible A session must receive the published summary as the memory overlay")
	overlay, ok := sentinelInstructionOverlay(claudeBound, sentinelSummary)
	require.True(t, ok, "the overlay must be a system instruction message")
	require.Equal(t, aipapi.RoleSystem, overlay.Role)
	require.True(t, sentinelCallCarries(claudeBound, projectKey),
		"the overlay must carry the opaque project scope identifier")
	require.Equal(t, 1.0, proxyMemoryCounterValue(t, registry, proxymemory.EventInjectMatched))

	// ---- step 4: principal B over the same path receives nothing ----
	bBound := sentinelServe(t, ex, scopeB, sentinelCodexTurn("sentinel-b-codex"), backend)
	require.False(t, sentinelCallCarries(bBound, sentinelSummary),
		"another principal over the same path must never receive A's summary")
	require.False(t, sentinelCallCarries(bBound, projectKey),
		"another principal must never receive A's scope overlay")
	require.Empty(t, sentinelListBanks(t, out.HostProjections.ProxyMemoryHTTP, scopeB),
		"the projected handler must list nothing for B")
	require.Equal(t, 2.0, proxyMemoryCounterValue(t, registry, proxymemory.EventInjectNoSummary),
		"B ran the request transform and committed its own no-match decision")
	require.Equal(t, 0.0, proxyMemoryCounterValue(t, registry, proxymemory.EventInjectIneligible))
	require.Equal(t, 0.0, proxyMemoryCounterValue(t, registry, proxymemory.EventInjectNoSession))
	require.Equal(t, 0.0, proxyMemoryCounterValue(t, registry, proxymemory.EventInjectNoProject))
	require.Equal(t, 0.0, proxyMemoryCounterValue(t, registry, proxymemory.EventInjectError))
	require.Equal(t, http.StatusNotFound,
		sentinelMemoryStatus(t, out.HostProjections.ProxyMemoryHTTP, scopeB, projectKey),
		"another owner's scope is not found, never revealed")
	_, _, err = memoryStore.PublishedSummary(ctx, proxymemory.Owner{
		TenantID: sentinelTenant, PrincipalID: sentinelPrincipalB, ProjectKey: projectKey,
	})
	require.ErrorIs(t, err, proxymemory.ErrNotFound,
		"A's extraction and publication must never land in B's bank")

	// ---- step 5: A forgets the scope; the next A session receives nothing ----
	forget := sentinelMemoryRequest(out.HostProjections.ProxyMemoryHTTP, http.MethodDelete, "/v1/aip/memory/"+projectKey, scopeA)
	require.Equal(t, http.StatusNoContent, forget.Code)
	require.Equal(t, 1.0, proxyMemoryCounterValue(t, registry, proxymemory.EventControlForget))

	afterForget := sentinelServe(t, ex, scopeA, sentinelClaudeTurn("sentinel-a-after-forget"), backend)
	require.False(t, sentinelCallCarries(afterForget, sentinelSummary),
		"a forgotten scope must not inject into a further A session")
	require.False(t, sentinelCallCarries(afterForget, projectKey))
	require.Empty(t, sentinelListBanks(t, out.HostProjections.ProxyMemoryHTTP, scopeA))
	require.Equal(t, http.StatusNotFound,
		sentinelMemoryStatus(t, out.HostProjections.ProxyMemoryHTTP, scopeA, projectKey),
		"a forgotten scope is not found to its owner either")
	require.Equal(t, 3.0, proxyMemoryCounterValue(t, registry, proxymemory.EventInjectNoSummary))

	// B's isolation is untouched by A's forget: a further B turn still gets
	// nothing and B's list stays empty.
	require.Empty(t, sentinelListBanks(t, out.HostProjections.ProxyMemoryHTTP, scopeB))
	bAfterForget := sentinelServe(t, ex, scopeB, sentinelClaudeTurn("sentinel-b-after-forget"), backend)
	require.False(t, sentinelCallCarries(bAfterForget, sentinelSummary),
		"B must stay isolated after A's forget")
	require.False(t, sentinelCallCarries(bAfterForget, projectKey))
	require.Equal(t, 4.0, proxyMemoryCounterValue(t, registry, proxymemory.EventInjectNoSummary))
}

// sentinelListBanks reads the GET /v1/aip/memory list for one principal
// through the projected handler and requires a 200 array envelope.
func sentinelListBanks(t *testing.T, h http.Handler, scopeView scope.PrincipalScopeView) []sentinelBankJSON {
	t.Helper()
	rec := sentinelMemoryRequest(h, http.MethodGet, "/v1/aip/memory", scopeView)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.Bytes())
	var envelope sentinelBanksJSON
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope))
	return envelope.Banks
}

// sentinelMemoryStatus requests one bank status through the projected handler
// and returns its HTTP status.
func sentinelMemoryStatus(t *testing.T, h http.Handler, scopeView scope.PrincipalScopeView, scopeID string) int {
	t.Helper()
	rec := sentinelMemoryRequest(h, http.MethodGet, "/v1/aip/memory/"+scopeID, scopeView)
	return rec.Code
}

// sentinelCapturedSessions counts the durable captured sessions of one owner
// directly in the shared database, so the assertion observes committed state
// rather than the capture's in-memory pending map (precedent: bootstrapRows in
// proxy_memory_test.go).
func sentinelCapturedSessions(t *testing.T, database *bun.DB, owner proxymemory.Owner) int {
	t.Helper()
	var count int
	require.NoError(t, database.NewRaw(
		`SELECT count(*) FROM proxy_memory_sessions WHERE tenant_id = ? AND principal_id = ? AND project_key = ?`,
		owner.TenantID, owner.PrincipalID, owner.ProjectKey).Scan(context.Background(), &count))
	return count
}

// sentinelCapturedLabel reads the durable project label of one owner's
// captured session.
func sentinelCapturedLabel(t *testing.T, database *bun.DB, owner proxymemory.Owner) string {
	t.Helper()
	var label string
	require.NoError(t, database.NewRaw(
		`SELECT project_label FROM proxy_memory_sessions WHERE tenant_id = ? AND principal_id = ? AND project_key = ?`,
		owner.TenantID, owner.PrincipalID, owner.ProjectKey).Scan(context.Background(), &label))
	return label
}
