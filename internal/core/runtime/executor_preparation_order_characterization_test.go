package runtime

import (
	"context"
	"crypto/rand"
	"io"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/authoritycoord"
	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execctx"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/securesession/adapters/b2bualineage"
	"github.com/aiproxer/aiproxer/internal/core/securesession/adapters/memory"
	"github.com/aiproxer/aiproxer/internal/core/securesession/app"
	"github.com/aiproxer/aiproxer/internal/core/securesession/domain"
	"github.com/aiproxer/aiproxer/internal/core/workspace"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/authority"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/prerequest"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/promptcache"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/routehint"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/sessionclassification"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcatalog"
	aipworkspace "github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
)

type recordingStore struct {
	app.Store
	mu     *sync.Mutex
	events *[]string
}

func (s *recordingStore) Create(ctx context.Context, rec domain.CreateRecord) (domain.Record, error) {
	s.mu.Lock()
	if !slices.Contains(*s.events, "SecureSession.BeginTurn") {
		*s.events = append(*s.events, "SecureSession.BeginTurn")
	}
	s.mu.Unlock()
	return s.Store.Create(ctx, rec)
}

func (s *recordingStore) TouchActivity(ctx context.Context, id domain.SessionID, at time.Time, src domain.ActivitySource) error {
	s.mu.Lock()
	if !slices.Contains(*s.events, "SecureSession.BeginTurn") {
		*s.events = append(*s.events, "SecureSession.BeginTurn")
	}
	s.mu.Unlock()
	return s.Store.TouchActivity(ctx, id, at, src)
}

type recordingB2BuaStore struct {
	b2bua.Store
	mu       *sync.Mutex
	events   *[]string
	executor *Executor
}

func (s *recordingB2BuaStore) FetchALeg(ctx context.Context, id string) (b2bua.ALegRecord, error) {
	s.mu.Lock()
	*s.events = append(*s.events, "FetchALeg")
	s.mu.Unlock()
	if s.executor != nil && s.executor.PromptCacheMaintenance != nil {
		ctl := &fixedResultController{}
		now := time.Unix(2000, 0)
		exp := now.Add(time.Hour)
		obs := []promptcache.Observation{
			{
				ALegID:            id,
				BLegID:            "b-leg-1",
				BackendInstanceID: "backend-1",
				TargetID:          "target-1",
				GenerationID:      "gen-1",
				Lifecycle:         promptcache.LifecycleSlidingExpiry,
				Timing:            promptcache.Timing{ObservedAt: now, ExpiresAt: &exp},
				Renewable:         true,
				Handle:            promptcache.Handle("handle-1"),
			},
		}
		s.executor.PromptCacheMaintenance.ArmCommittedTurn(PromptCacheCommittedTurn{
			ALegID:              id,
			BLegID:              "b-leg-1",
			BackendInstanceID:   "backend-1",
			CanonicalModelID:    "model-1",
			ToolEvents:          []aipapi.ToolEvent{{Kind: aipapi.ToolEventFinished, Category: aipapi.ToolCategoryOSCommand}},
			Observations:        obs,
			Controller:          ctl,
			CommittedSuccessful: true,
		})
	}
	return s.Store.FetchALeg(ctx, id)
}

type spyConcurrencyProvider struct {
	authority.ConcurrencyProvider
	mu     *sync.Mutex
	events *[]string
}

func (s *spyConcurrencyProvider) AdmitLease(ctx context.Context, in authority.LeaseAdmission) (authority.LeaseDecision, error) {
	s.mu.Lock()
	if holder := meteringHolderFrom(ctx); holder != nil && holder.FrontendIngress != nil {
		*s.events = append(*s.events, "FrontendIngressReady")
	} else {
		*s.events = append(*s.events, "FrontendIngressMissing")
	}
	*s.events = append(*s.events, "RequestAdmission")
	s.mu.Unlock()
	return authority.LeaseDecision{
		Kind:       authority.LeaseAllow,
		LeaseID:    "lease-123",
		Generation: 1,
		ExpiresAt:  time.Unix(2000000000, 0),
	}, nil
}

type spyPromptCacheMaintenance struct {
	mu     *sync.Mutex
	events *[]string
}

func (s *spyPromptCacheMaintenance) BeginRealTurn(aLegID string) {
	s.mu.Lock()
	*s.events = append(*s.events, "Keepwarm.BeginRealTurn")
	s.mu.Unlock()
}

func (s *spyPromptCacheMaintenance) EndSession(aLegID string) {}

func (s *spyPromptCacheMaintenance) ArmCommittedTurn(turn PromptCacheCommittedTurn) {}

type spySubmitHook struct {
	mu                      *sync.Mutex
	events                  *[]string
	frontendIngressAtSubmit *bool
}

func (s spySubmitHook) ID() string                        { return "spy_submit" }
func (s spySubmitHook) Order() int                        { return 0 }
func (s spySubmitHook) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (s spySubmitHook) Handle(ctx context.Context, call *aipapi.Call, meta *sdkhooks.SubmitMeta) (sdkhooks.SubmitDecision, error) {
	s.mu.Lock()
	if holder := meteringHolderFrom(ctx); holder != nil && holder.FrontendIngress != nil && s.frontendIngressAtSubmit != nil {
		*s.frontendIngressAtSubmit = true
	}
	*s.events = append(*s.events, "SubmitHooks")
	s.mu.Unlock()
	return sdkhooks.SubmitDecision{}, nil
}

type orderWorkspaceResolver struct{}

func (orderWorkspaceResolver) Resolve(context.Context) (aipworkspace.WorkspaceView, error) {
	return aipworkspace.WorkspaceView{ID: "workspace-order"}, nil
}

type preparationOrderStages struct {
	mu     *sync.Mutex
	events *[]string
	view   *session.SessionView
}

func (s *preparationOrderStages) record(event string, view session.SessionView, workspaceID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if view.AuthoritativeSessionID == "" || view.AuthoritativeSessionID == view.ClientSessionHint || view.ALegID == "" || view.TurnID == "" || view.WorkspaceID != "workspace-order" || view.ClientSessionHint != "c-order" || workspaceID != "workspace-order" {
		*s.events = append(*s.events, event+"MissingBoundSession")
	} else {
		if s.view == nil {
			copy := view
			s.view = &copy
		} else if s.view.AuthoritativeSessionID != view.AuthoritativeSessionID || s.view.ALegID != view.ALegID || s.view.TurnID != view.TurnID || s.view.WorkspaceID != view.WorkspaceID || s.view.ClientSessionHint != view.ClientSessionHint {
			*s.events = append(*s.events, event+"SessionDrift")
		}
		*s.events = append(*s.events, event)
	}
}

type orderSecretGuard struct{ stages *preparationOrderStages }

func (g orderSecretGuard) ID() string                           { return "order-secret-guard" }
func (g orderSecretGuard) Order() int                           { return 0 }
func (g orderSecretGuard) FailureMode() secretguard.FailureMode { return secretguard.FailOpen }
func (g orderSecretGuard) Evaluate(_ context.Context, _ *aipapi.Call, meta secretguard.Meta, _ secretguard.Services) (secretguard.Decision, error) {
	g.stages.record("SecretGuard", meta.Session, meta.Workspace.ID)
	return secretguard.Decision{Outcome: secretguard.OutcomePass}, nil
}

type orderToolCatalogFilter struct{ stages *preparationOrderStages }

func (f orderToolCatalogFilter) ID() string                        { return "order-tool-catalog" }
func (f orderToolCatalogFilter) Order() int                        { return 0 }
func (f orderToolCatalogFilter) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (f orderToolCatalogFilter) Handle(_ context.Context, _ *aipapi.Call, meta toolcatalog.CatalogMeta, _ toolcatalog.Services) error {
	f.stages.record("ToolCatalog", meta.Session, meta.Workspace.ID)
	return nil
}

type orderRequestTransform struct{ stages *preparationOrderStages }

func (r orderRequestTransform) ID() string                        { return "order-request-transform" }
func (r orderRequestTransform) Order() int                        { return 0 }
func (r orderRequestTransform) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (r orderRequestTransform) Handle(_ context.Context, _ *aipapi.Call, meta request.RequestMeta, _ request.Services) error {
	r.stages.record("RequestTransform", meta.Session, meta.Workspace.ID)
	return nil
}

type orderPreRequestHandler struct{ stages *preparationOrderStages }

func (r orderPreRequestHandler) ID() string                        { return "order-pre-request" }
func (r orderPreRequestHandler) Order() int                        { return 0 }
func (r orderPreRequestHandler) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (r orderPreRequestHandler) Handle(_ context.Context, _ *aipapi.Call, meta prerequest.Meta, _ prerequest.Services) (prerequest.Decision, error) {
	r.stages.record("PreRequest", meta.Session, meta.Workspace.ID)
	return prerequest.Allow(), nil
}

type orderRouteHintProvider struct{ stages *preparationOrderStages }

func (r orderRouteHintProvider) ID() string                        { return "order-route-hint" }
func (r orderRouteHintProvider) Order() int                        { return 0 }
func (r orderRouteHintProvider) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (r orderRouteHintProvider) Hint(_ context.Context, in routehint.Input) (routehint.Result, error) {
	r.stages.record("RouteHint", in.Session, in.Workspace.ID)
	return routehint.Result{}, nil
}

// orderSessionClassifier records the session-classification stage in the same
// canonical order trace and asserts the bound secure-session/workspace views it
// receives are the post-BeginTurn/A-leg ones.
type orderSessionClassifier struct {
	stages *preparationOrderStages
	result session.Classification
}

func (c orderSessionClassifier) ID() string { return "order-session-classifier" }

func (c orderSessionClassifier) Classify(_ context.Context, in sessionclassification.Input) (session.Classification, error) {
	c.stages.record("SessionClassification", in.Session, in.Workspace.ID)
	return c.result, nil
}

func isCaller(name string) bool {
	var pcs [32]uintptr
	n := runtime.Callers(2, pcs[:])
	frames := runtime.CallersFrames(pcs[:n])
	for {
		frame, more := frames.Next()
		if strings.Contains(frame.Function, name) {
			return true
		}
		if !more {
			break
		}
	}
	return false
}

func TestExecutor_PreparationOrderCharacterization(t *testing.T) {
	ctx := context.Background()
	var events []string
	var mu sync.Mutex
	var frontendIngressAtSubmit bool
	stages := &preparationOrderStages{mu: &mu, events: &events}

	// 1. Setup B2BUA Store
	b2, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}

	// 2. Setup SecureSession Store
	memSS := memory.New(memory.Options{SimulateDurable: true})
	recSSStore := &recordingStore{Store: memSS, mu: &mu, events: &events}

	// 4. Setup Executor
	ex := setSecureSessionDenialMapper(TestExecutor())
	recB2Store := &recordingB2BuaStore{Store: b2, mu: &mu, events: &events, executor: ex}
	ex.Store = recB2Store
	ex.SyntheticLocalPrincipal = true
	ex.SnapshotGeneration = nil

	// 3. Setup Secure Manager
	mgr, err := app.NewManager(recSSStore, app.NewRandGenerator(testFingerprintKey32(t)), b2bualineage.New(recB2Store), app.ManagerConfig{
		FingerprintKey: testFingerprintKey32(t),
		StoreDurable:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	ex.SecureSession = mgr

	// Setup PromptCacheMaintenance
	ex.PromptCacheMaintenance = &spyPromptCacheMaintenance{
		mu:     &mu,
		events: &events,
	}

	// Hook submit hook
	ex.Bus = hooks.New(hooks.Config{
		SubmitHooks: []sdkhooks.SubmitHook{spySubmitHook{mu: &mu, events: &events, frontendIngressAtSubmit: &frontendIngressAtSubmit}},
	})

	// Setup Runtime Snapshot with a resolver
	snap := extensions.NewRequestRuntimeSnapshot(ex.Bus, extensions.SnapshotOptions{
		Workspace: workspace.NewResolverChain([]aipworkspace.Resolver{orderWorkspaceResolver{}}),
		FeaturePlanes: freezeBundle(testFeatureBundle{
			SecretGuards:       []secretguard.Guard{orderSecretGuard{stages: stages}},
			SessionClassifier:  orderSessionClassifier{stages: stages, result: codingAgentSessionClassification("order.identity", 1)},
			ToolCatalogFilters: []toolcatalog.Filter{orderToolCatalogFilter{stages: stages}},
			RequestTransforms:  []request.Transform{orderRequestTransform{stages: stages}},
			PreRequestHandlers: []prerequest.Handler{orderPreRequestHandler{stages: stages}},
			RouteHintProviders: []routehint.Provider{orderRouteHintProvider{stages: stages}},
		}),
	})
	ex.RuntimeSnapshot = snap

	// 5. Setup Route Authority Snapshot Barrier
	barrier := newRouteAuthoritySnapshotBarrier()
	ctx = withRouteAuthoritySnapshotBarrier(ctx, barrier)

	// Watch barrier arrival in background
	go func() {
		err := barrier.waitUntilArrived(ctx)
		if err == nil {
			mu.Lock()
			events = append(events, "RouteAuthoritySnapshotBarrier")
			mu.Unlock()
			barrier.releaseWaiters()
		}
	}()

	// 7. Override clock to intercept metering capture
	ex.Now = func() time.Time {
		select {
		case <-barrier.arrived:
			mu.Lock()
			if !slices.Contains(events, "MeteringCapture") {
				events = append(events, "MeteringCapture")
			}
			mu.Unlock()
		default:
		}
		return time.Unix(2000, 0).UTC()
	}

	// 6. Setup Request Coordinator
	ex.RequestCoordinator = &authoritycoord.RequestCoordinator{
		Concurrency: &spyConcurrencyProvider{mu: &mu, events: &events},
		Now:         ex.Now,
	}

	call := &aipapi.Call{
		Session: aipapi.SessionRef{
			ClientSessionID: "c-order",
		},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}

	// Intercept rand.Reader for billing ID generation
	oldRandReader := rand.Reader
	rand.Reader = &customRandReader{
		Reader: oldRandReader,
		onRead: func() {
			if isCaller("stampBillingCallID") {
				mu.Lock()
				events = append(events, "Billing.NewBillingCallID")
				mu.Unlock()
			}
		},
	}
	defer func() { rand.Reader = oldRandReader }()

	// Execute preparation
	pr, prepCtx, closeFn, err := ex.prepareRequest(ctx, call)
	if err != nil {
		t.Fatalf("prepareRequest failed: %v", err)
	}
	defer func() {
		closeFn()
	}()

	// Ensure the A-leg ID matches
	if v, ok := execctx.FromContext(prepCtx); ok {
		if v.Session.ALegID == "" {
			t.Fatal("expected aleg ID to be populated")
		}
	}

	// Verify order
	expectedOrder := []string{
		"SecureSession.BeginTurn",
		"FetchALeg",
		"RouteAuthoritySnapshotBarrier",
		"SecretGuard",
		"SessionClassification",
		"MeteringCapture",
		"FrontendIngressReady",
		"RequestAdmission",
		"SubmitHooks",
		"ToolCatalog",
		"RequestTransform",
		"PreRequest",
		"RouteHint",
		"Keepwarm.BeginRealTurn",
		"Billing.NewBillingCallID",
	}

	mu.Lock()
	actualEvents := append([]string(nil), events...)
	mu.Unlock()

	if len(actualEvents) != len(expectedOrder) {
		t.Fatalf("expected %d events, got %d. Expected: %v, Got: %v", len(expectedOrder), len(actualEvents), expectedOrder, actualEvents)
	}

	for i, expected := range expectedOrder {
		if actualEvents[i] != expected {
			t.Errorf("event %d: expected %s, got %s. Full trace: %v", i, expected, actualEvents[i], actualEvents)
		}
	}
	if !frontendIngressAtSubmit {
		t.Error("submit hook did not observe the frontend-ingress checkpoint")
	}
	if stages.view == nil {
		t.Fatal("no downstream stage observed a bound session view")
	}

	// Verify that A-leg scope is non-nil and was started after billing (which completed billing setup)
	if pr.aScope == nil {
		t.Error("expected A-leg scope to be initialized at the end of preparation")
	}
}

type fixedResultController struct{}

func (c *fixedResultController) Renew(ctx context.Context, req promptcache.RenewRequest) (promptcache.RenewResponse, error) {
	return promptcache.RenewResponse{}, nil
}

func (c *fixedResultController) Release(ctx context.Context, req promptcache.ReleaseRequest) error {
	return nil
}

type customRandReader struct {
	io.Reader
	onRead func()
}

func (r *customRandReader) Read(p []byte) (n int, err error) {
	if r.onRead != nil {
		r.onRead()
	}
	return r.Reader.Read(p)
}
