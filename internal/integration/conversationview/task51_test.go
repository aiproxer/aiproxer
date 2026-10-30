package conversationview_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/continuation"
	"github.com/aiproxer/aiproxer/internal/core/conversationprojection"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	secapps "github.com/aiproxer/aiproxer/internal/core/securesession/adapters/b2bualineage"
	secmem "github.com/aiproxer/aiproxer/internal/core/securesession/adapters/memory"
	secapp "github.com/aiproxer/aiproxer/internal/core/securesession/app"
	"github.com/aiproxer/aiproxer/internal/core/workspace"
	"github.com/aiproxer/aiproxer/internal/infra/conversationview"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openresponses"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdkauth "github.com/aiproxer/aiproxer/pkg/aipsdk/auth"
	aipcont "github.com/aiproxer/aiproxer/pkg/aipsdk/continuation"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/traffic"
	aipworkspace "github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"

	"github.com/aiproxer/aiproxer/internal/core/runtime"
)

type trafficCapture struct {
	mu  sync.Mutex
	obs []traffic.Observation
}

func (c *trafficCapture) OnObservation(_ context.Context, ev traffic.Observation) error {
	c.mu.Lock()
	c.obs = append(c.obs, ev)
	c.mu.Unlock()
	return nil
}

func (c *trafficCapture) byLeg(leg traffic.Leg) []traffic.Observation {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []traffic.Observation
	for _, o := range c.obs {
		if o.Leg == leg {
			cp := o
			cp.Body = append([]byte(nil), o.Body...)
			out = append(out, cp)
		}
	}
	return out
}

func (c *trafficCapture) bodies(leg traffic.Leg) [][]byte {
	var out [][]byte
	for _, o := range c.byLeg(leg) {
		out = append(out, append([]byte(nil), o.Body...))
	}
	return out
}

type captureBackend struct {
	mu    sync.Mutex
	calls []aipapi.Call
}

func (c *captureBackend) Backend() execbackend.Backend {
	return execbackend.Backend{
		Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
		Open: func(_ context.Context, call aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			c.mu.Lock()
			c.calls = append(c.calls, aipapi.CloneCall(call))
			c.mu.Unlock()
			return aipapi.NewFixedEventStream([]aipapi.Event{
				{Kind: aipapi.EventResponseStarted},
				{Kind: aipapi.EventMessageStarted},
				{Kind: aipapi.EventTextDelta, Delta: "backend-answer"},
				{Kind: aipapi.EventResponseFinished},
			}), nil
		},
	}
}

func (c *captureBackend) lastCall() (aipapi.Call, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.calls) == 0 {
		return aipapi.Call{}, false
	}
	return aipapi.CloneCall(c.calls[len(c.calls)-1]), true
}

func (c *captureBackend) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.calls)
}

// pinnedReader returns snapshot for pinned ALegID regardless of queried ID, but reads from real store.
type pinnedReader struct {
	store  conversationview.Store
	pinned string
}

func (r *pinnedReader) Snapshot(ctx context.Context, _ string) (conversationprojection.Snapshot, error) {
	return r.store.Snapshot(ctx, r.pinned)
}

type contAuth struct{}

func (contAuth) Authenticate(_ context.Context, _ sdkauth.InboundCallMeta) (sdkauth.Decision, error) {
	return sdkauth.Decision{
		Outcome:   sdkauth.OutcomeAllow,
		Principal: execview.PrincipalView{ID: "principal-1"},
		Scope: &scope.PrincipalScopeView{
			PrincipalID: scope.Known("principal-1"),
			TenantID:    scope.Known("tenant-1"),
			Origin:      scope.OriginClient,
		},
	}, nil
}

type emptyWS struct{}

func (emptyWS) Resolve(context.Context) (aipworkspace.WorkspaceView, error) {
	return aipworkspace.WorkspaceView{}, nil
}

type debugExecutor struct {
	*runtime.Executor
	t *testing.T
}

func (d *debugExecutor) Execute(ctx context.Context, call *aipapi.Call) (aipapi.EventStream, error) {
	s, err := d.Executor.Execute(ctx, call)
	if err != nil {
		d.t.Logf("executor Execute error: %v", err)
	}
	return s, err
}

func newSecureExecutorWithCapture(t *testing.T, store *b2bua.MemoryStore, cap *trafficCapture) *runtime.Executor {
	t.Helper()
	memSS := secmem.New(secmem.Options{SimulateDurable: true})
	fk := make([]byte, 32)
	for i := range fk {
		fk[i] = byte(i + 1)
	}
	mgr, err := secapp.NewManager(memSS, secapp.NewRandGenerator(fk), secapps.New(store), secapp.ManagerConfig{FingerprintKey: fk, StoreDurable: true})
	if err != nil {
		t.Fatalf("secure manager: %v", err)
	}
	bus := hooks.New(hooks.Config{})
	snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		Workspace:       workspace.NewResolverChain([]aipworkspace.Resolver{emptyWS{}}),
		TrafficObserver: cap,
	})
	ex := runtime.TestExecutor()
	ex.Store = store
	ex.Bus = bus
	ex.RuntimeSnapshot = snap
	ex.SecureSession = mgr
	ex.SyntheticLocalPrincipal = true
	ex.Rand = routing.NewSeededRng(1)
	ex.Now = func() time.Time { return time.Unix(5000, 0) }
	return ex
}

// TestTask51_OpenResponses_RealExecutor_EndToEnd proves previous_response_id materialization reconstructs tagged local
// messages, then real runtime filters them and injects hidden steering only after materialization.
// Uses real openresponses.Handler + real continuation.Resolver/Store + real b2bua MemoryStore CV state + real runtime.Executor + real traffic observer + real Backend.Open.
// Asserts CTP tagged/no steering, PTB/backend no tagged+steering, client/continuation no steering.
func TestTask51_OpenResponses_RealExecutor_EndToEnd(t *testing.T) {
	t.Parallel()
	localInput := aipapi.Item{Kind: aipapi.ItemKindMessage, ID: "item-local-input", Status: aipapi.ItemStatusCompleted, Role: aipapi.RoleUser, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "local-tagged-input"}}}
	localReply := aipapi.Item{Kind: aipapi.ItemKindMessage, ID: "item-local-reply", Status: aipapi.ItemStatusCompleted, Role: aipapi.RoleAssistant, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "local-tagged-reply"}}}
	localInputID, _ := conversationprojection.ItemIdentityOf(localInput)
	localReplyID, _ := conversationprojection.ItemIdentityOf(localReply)

	steeringText := "hidden-steering-integration-OpenResponses"
	steeringOverlayText := steeringText

	// Real stores
	b2Store, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatalf("b2 store: %v", err)
	}
	contStore := continuation.NewMemoryStore()
	scope := aipcont.Scope{TenantID: "tenant-1", PrincipalID: "principal-1"}

	// Seed parent continuation record containing tagged local messages as client-visible history.
	parentID := func() aipcont.ResponseID {
		ctx := context.Background()
		policy := aipcont.StoragePolicy{Mode: aipcont.PersistencePersistent, TTL: 24 * time.Hour}
		id, err := contStore.Reserve(ctx, scope, policy)
		if err != nil {
			t.Fatalf("Reserve parent: %v", err)
		}
		rec := aipcont.ContinuationRecord{
			ID: id, Scope: scope, Terminal: true, Status: aipcont.RecordStatusCompleted,
			InputItems: []aipapi.Item{
				{Kind: aipapi.ItemKindMessage, ID: "parent-user-1", Status: aipapi.ItemStatusCompleted, Role: aipapi.RoleUser, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "parent-user-before"}}},
				localInput,
			},
			OutputItems: []aipapi.Item{localReply},
			Lineage:     aipcont.Lineage{ProfileID: "openresponses", Model: "openai:gpt-4", RouteSelector: "openai:gpt-4"},
			Policy:      policy,
		}
		if err := contStore.PutTerminal(ctx, rec); err != nil {
			t.Fatalf("PutTerminal: %v", err)
		}
		return id
	}()

	// Create a pinned A-leg and tag/steering for it, then use pinnedReader so any fresh secure A-leg sees same snapshot.
	// First create a temporary secure executor to obtain a real A-leg via secure session, then reuse its ALegID as pinned.
	tmpCap := &trafficCapture{}
	tmpEx := newSecureExecutorWithCapture(t, b2Store, tmpCap)
	// Need a backend for the dummy call to allocate A-leg
	tmpEx.Backends = map[string]execbackend.Backend{"openai": (&captureBackend{}).Backend()}
	// Simpler: directly create ALeg via b2Store.CreateALeg and use its ID as pinned.
	rec, err := b2Store.CreateALeg(context.Background(), "pin-continuity-openresponses")
	if err != nil {
		t.Fatalf("CreateALeg: %v", err)
	}
	pinnedALeg := rec.ALegID
	cv := conversationview.NewReferenceStore()
	if err := cv.CreateALeg(context.Background(), pinnedALeg); err != nil {
		t.Fatalf("CreateALeg: %v", err)
	}
	if _, err := cv.TagNeverBackend(context.Background(), pinnedALeg, []conversationview.TagRequest{{Identity: localInputID, Reason: "test_local"}, {Identity: localReplyID, Reason: "test_local"}}); err != nil {
		t.Fatalf("Tag: %v", err)
	}
	if _, err := cv.PutSteering(context.Background(), pinnedALeg, conversationview.PutSteeringRequest{
		OverlayID: "ov-openresponses-integration", Message: conversationview.StoredMessageV1{Role: aipapi.RoleSystem, Text: steeringOverlayText},
		Placement: conversationview.StoredPlacement{Kind: conversationview.PlacementStablePrefix}, AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback, Reason: "test",
	}); err != nil {
		t.Fatalf("PutSteering: %v", err)
	}

	trafficCap := &trafficCapture{}
	capBackend := &captureBackend{}
	// Real runtime executor with secure session, but ConversationViewReader pinned to real store's pinned ALeg
	exReal := newSecureExecutorWithCapture(t, b2Store, trafficCap)
	exReal.ConversationViewReader = &pinnedReader{store: cv, pinned: pinnedALeg}
	exReal.Backends = map[string]execbackend.Backend{"openai": capBackend.Backend()}
	// Debug wrapper to surface executor error in test log
	ex := &debugExecutor{Executor: exReal, t: t}

	handler := openresponses.NewHandler(openresponses.HandlerConfig{
		AllowUnauthenticated: true,
		Authorizer:           contAuth{},
		Executor:             ex,
		ContinuationStore:    contStore,
		ContinuationResolver: openresponses.NewStoreContinuationResolver(contStore, aipcont.Bounds{MaxChainDepth: 64, MaxMaterializedBytes: 64 << 20}),
		Config: openresponses.Config{
			Continuation: openresponses.ContinuationConfig{MaxChainDepth: 64, MaxMaterializedBytes: 64 << 20},
		},
	})

	body := `{"previous_response_id":"` + parentID.String() + `","input":"next-question","store":true}`
	req := httptest.NewRequest(http.MethodPost, "/openresponses/v1/responses", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	recH := httptest.NewRecorder()
	// Need principal in context for secure session? Handler's authorize will set principal via contAuth.
	// For secure session path, Executor needs principal in context. The Handler's frontendpipe will propagate auth decision via context?
	// The openresponses Handler uses sdkauth.Decision stored in context via authorizeCreate, but our contAuth returns principal.
	// The executor's resolveRequestScope will read from context's principal.
	// Ensure request has auth decision via handler's authorize.
	handler.ServeHTTP(recH, req)

	if recH.Code != http.StatusOK {
		t.Fatalf("handler status %d body %s", recH.Code, recH.Body.String())
	}
	if capBackend.count() != 1 {
		t.Fatalf("backend calls %d want 1", capBackend.count())
	}

	// CTP: must contain tagged, no steering
	ctpBodies := trafficCap.bodies(traffic.LegCTP)
	if len(ctpBodies) == 0 {
		t.Fatalf("no CTP observations (secure path should emit CTP)")
	}
	foundCTPTagged := false
	hasCTPSteering := false
	for _, raw := range ctpBodies {
		var c aipapi.Call
		if err := json.Unmarshal(raw, &c); err != nil {
			continue
		}
		for _, it := range c.Items {
			if it.Kind == aipapi.ItemKindMessage {
				if id, _ := conversationview.ItemIdentityOf(it); id == localInputID || id == localReplyID {
					foundCTPTagged = true
				}
				for _, p := range it.Content {
					if p.Text == steeringText {
						hasCTPSteering = true
					}
				}
			}
		}
	}
	if !foundCTPTagged {
		t.Fatalf("CTP must contain client-visible tagged local messages")
	}
	if hasCTPSteering {
		t.Fatalf("CTP must NOT contain hidden steering")
	}

	// PTB/backend: no tagged, has steering
	ptbBodies := trafficCap.bodies(traffic.LegPTB)
	if len(ptbBodies) == 0 {
		t.Fatalf("no PTB observations")
	}
	hasPTBSteering := false
	hasPTBTagged := false
	for _, raw := range ptbBodies {
		var c aipapi.Call
		if err := json.Unmarshal(raw, &c); err != nil {
			continue
		}
		for _, it := range c.Items {
			if it.Kind == aipapi.ItemKindMessage {
				if id, _ := conversationview.ItemIdentityOf(it); id == localInputID || id == localReplyID {
					hasPTBTagged = true
				}
				for _, p := range it.Content {
					if p.Text == steeringText {
						hasPTBSteering = true
					}
				}
			}
		}
		for _, m := range c.Instructions {
			for _, p := range m.Parts {
				if p.Text == steeringText {
					hasPTBSteering = true
				}
			}
		}
	}
	if !hasPTBSteering {
		t.Fatalf("PTB must contain hidden steering")
	}
	if hasPTBTagged {
		t.Fatalf("PTB must NOT contain tagged local messages")
	}
	openCall, ok := capBackend.lastCall()
	if !ok {
		t.Fatalf("backend not called")
	}
	hasOpenSteering := false
	hasOpenTagged := false
	for _, it := range openCall.Items {
		if it.Kind == aipapi.ItemKindMessage {
			if id, _ := conversationview.ItemIdentityOf(it); id == localInputID || id == localReplyID {
				hasOpenTagged = true
			}
			for _, p := range it.Content {
				if p.Text == steeringText {
					hasOpenSteering = true
				}
			}
		}
	}
	for _, m := range openCall.Instructions {
		for _, p := range m.Parts {
			if p.Text == steeringText {
				hasOpenSteering = true
			}
		}
	}
	if !hasOpenSteering {
		t.Fatalf("backend Open must contain hidden steering")
	}
	if hasOpenTagged {
		t.Fatalf("backend Open must NOT contain tagged")
	}

	// Client response no steering
	respBody := recH.Body.String()
	if strings.Contains(respBody, steeringText) {
		t.Fatalf("client response must NOT contain hidden steering")
	}
	if !strings.Contains(respBody, "backend-answer") {
		t.Fatalf("client response missing backend answer")
	}
	// Drain stream fully to ensure continuation recorder persisted (handler's observed stream)
	// Handler's recorder persists on stream close; httptest already closed. Need to ensure PutTerminal happened.
	var respRes struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(recH.Body.Bytes(), &respRes); err != nil {
		t.Fatalf("decode response id: %v", err)
	}
	if respRes.ID == "" {
		t.Fatalf("missing id")
	}
	// Poll for continuation record (recorder is async but should be done after ServeHTTP returns; it uses StreamObserver)
	stored, err := contStore.Get(context.Background(), scope, aipcont.ResponseID(respRes.ID))
	if err != nil {
		// Try with empty scope SessionID variance: handler's scope is Tenant+Principal only, so same.
		t.Fatalf("Get stored second record: %v", err)
	}
	for _, it := range stored.InputItems {
		for _, p := range it.Content {
			if p.Text == steeringText {
				t.Fatalf("continuation InputItems must NOT contain hidden steering")
			}
		}
	}
	for _, it := range stored.OutputItems {
		for _, p := range it.Content {
			if p.Text == steeringText {
				t.Fatalf("continuation OutputItems must NOT contain hidden steering")
			}
		}
	}
	foundNext := false
	for _, it := range stored.InputItems {
		for _, p := range it.Content {
			if p.Text == "next-question" {
				foundNext = true
			}
		}
	}
	if !foundNext {
		t.Fatalf("stored InputItems missing next-question: %+v", stored.InputItems)
	}
	_ = io.Discard
}

// TestTask51_LegacyFullHistory_RealExecutor verifies legacy Instructions/Messages full history replay
// with real runtime.Executor and traffic observers: CTP contains tagged client truth, PTB/backend does not.
func TestTask51_LegacyFullHistory_RealExecutor(t *testing.T) {
	t.Parallel()
	b2Store, _ := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	cap := &trafficCapture{}
	ex := newSecureExecutorWithCapture(t, b2Store, cap)
	// Create pinned A-leg for tags
	rec, _ := b2Store.CreateALeg(context.Background(), "legacy-pin")
	pinned := rec.ALegID
	sourceMsg := aipapi.Message{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("local-tagged-legacy")}}
	replyMsg := aipapi.Message{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{aipapi.TextPart("local-reply-legacy")}}
	sourceID, _ := conversationprojection.MessageIdentityOf(sourceMsg)
	replyID, _ := conversationprojection.MessageIdentityOf(replyMsg)
	cv := conversationview.NewReferenceStore()
	if err := cv.CreateALeg(context.Background(), pinned); err != nil {
		t.Fatalf("CreateALeg: %v", err)
	}
	if _, err := cv.TagNeverBackend(context.Background(), pinned, []conversationview.TagRequest{{Identity: sourceID, Reason: "test_local"}, {Identity: replyID, Reason: "test_local"}}); err != nil {
		t.Fatalf("TagNeverBackend: %v", err)
	}
	steeringText := "hidden-steering-legacy-51"
	if _, err := cv.PutSteering(context.Background(), pinned, conversationview.PutSteeringRequest{
		OverlayID: "ov-legacy-51", Message: conversationview.StoredMessageV1{Role: aipapi.RoleSystem, Text: steeringText},
		Placement: conversationview.StoredPlacement{Kind: conversationview.PlacementStablePrefix}, AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback, Reason: "test",
	}); err != nil {
		t.Fatalf("PutSteering: %v", err)
	}
	ex.ConversationViewReader = &pinnedReader{store: cv, pinned: pinned}
	backendCap := &captureBackend{}
	ex.Backends = map[string]execbackend.Backend{"openai": backendCap.Backend()}

	// Need to inject principal for secure session
	ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "principal-legacy"})
	legacyCall := &aipapi.Call{
		Route:        aipapi.RouteIntent{Selector: "openai:gpt-4"},
		Instructions: []aipapi.Message{{Role: aipapi.RoleSystem, Parts: []aipapi.Part{aipapi.TextPart("sys-instr")}}},
		Messages:     []aipapi.Message{sourceMsg, replyMsg, {Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("next-question")}}},
	}
	stream, err := ex.Execute(ctx, legacyCall)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if _, err := aipapi.Collect(context.Background(), stream); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	_ = stream.Close()

	// CTP must contain tagged
	ctp := cap.bodies(traffic.LegCTP)
	if len(ctp) == 0 {
		t.Fatalf("CTP missing")
	}
	hasCTPTagged := false
	hasCTPSteering := false
	for _, raw := range ctp {
		var c aipapi.Call
		_ = json.Unmarshal(raw, &c)
		for _, m := range c.Messages {
			if id, _ := conversationview.MessageIdentityOf(m); id == sourceID || id == replyID {
				hasCTPTagged = true
			}
			for _, p := range m.Parts {
				if p.Text == steeringText {
					hasCTPSteering = true
				}
			}
		}
	}
	if !hasCTPTagged {
		t.Fatalf("CTP must contain tagged legacy messages")
	}
	if hasCTPSteering {
		t.Fatalf("CTP must NOT contain steering")
	}
	// PTB/backend must not contain tagged, must contain steering
	ptb := cap.bodies(traffic.LegPTB)
	if len(ptb) == 0 {
		t.Fatalf("PTB missing")
	}
	hasPTBTagged := false
	hasPTBSteering := false
	for _, raw := range ptb {
		var c aipapi.Call
		_ = json.Unmarshal(raw, &c)
		for _, m := range c.Messages {
			if id, _ := conversationview.MessageIdentityOf(m); id == sourceID || id == replyID {
				hasPTBTagged = true
			}
		}
		for _, m := range c.Instructions {
			for _, p := range m.Parts {
				if p.Text == steeringText {
					hasPTBSteering = true
				}
			}
		}
	}
	if hasPTBTagged {
		t.Fatalf("PTB must NOT contain tagged")
	}
	if !hasPTBSteering {
		t.Fatalf("PTB must contain steering")
	}
	open, _ := backendCap.lastCall()
	hasOpenTagged := false
	hasOpenSteering := false
	for _, m := range open.Messages {
		if id, _ := conversationview.MessageIdentityOf(m); id == sourceID || id == replyID {
			hasOpenTagged = true
		}
	}
	for _, m := range open.Instructions {
		for _, p := range m.Parts {
			if p.Text == steeringText {
				hasOpenSteering = true
			}
		}
	}
	if hasOpenTagged || !hasOpenSteering {
		t.Fatalf("backend Open tagged=%v steering=%v want false/true", hasOpenTagged, hasOpenSteering)
	}
	// Client truth: legacyCall still had tagged (input not mutated), but stream output is backend-answer not steering
	if len(legacyCall.Messages) != 3 {
		t.Fatalf("client call mutated")
	}
}
