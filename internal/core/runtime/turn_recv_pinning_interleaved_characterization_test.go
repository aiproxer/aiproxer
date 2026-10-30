package runtime

import (
	"context"
	"sync"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/core/execctx"
	"github.com/aiproxer/aiproxer/internal/core/interleavedstate"
	"github.com/aiproxer/aiproxer/internal/core/metering/checkpoint"
	"github.com/aiproxer/aiproxer/internal/core/modelcatalog"
	"github.com/aiproxer/aiproxer/internal/core/modelregistry"
	"github.com/aiproxer/aiproxer/internal/core/modelview"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/securesession/domain"
	"github.com/aiproxer/aiproxer/internal/core/streamrecovery"
	"github.com/aiproxer/aiproxer/internal/core/usageevidence"
	"github.com/aiproxer/aiproxer/internal/core/usagelimit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/modelinventory"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/policydecision"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
)

// TestTurnRecvPinningInterleavedContinuationRetainsAllBoundFacts exercises the
// interleaved thinker-to-executor handoff from a bare context after registry and
// catalog refresh. Existing coverage checks the catalog generation alone; this
// characterization pins the complete continuation carrier: execution views,
// metering/request authority, secure turn and route preferences, native model
// resolver, aggregate model-view identity, and billing identity.
func TestTurnRecvPinningInterleavedContinuationRetainsAllBoundFacts(t *testing.T) {
	auth := &recordingAuthorityService{
		admitResult: authorityAdmissionResultForTurnRecvPinning(),
	}
	ex, from := setupInterleavedAuthorityContinuation(t, auth, "hidden")

	provider := &turnRecvPinningInventory{models: []modelinventory.Model{{CanonicalID: "vendor/model-1", NativeID: "native-A"}}}
	registry := modelregistry.NewRuntime(modelregistry.RuntimeConfig{
		Inventories: []modelregistry.BackendInventory{{
			BackendID: "backend-1", Kind: "test", BackendPrefixes: []string{"backend-1"}, Provider: provider,
		}},
	})
	if err := registry.Start(context.Background()); err != nil {
		t.Fatalf("registry start: %v", err)
	}
	boundRegistry := registry.BoundView()

	catalog := modelcatalog.NewCatalogRuntime(modelcatalog.RuntimeConfig{})
	catalog.PublishSnapshot(modelcatalog.Snapshot{
		Generation: "catalog-A",
		Index: modelcatalog.NewSnapshotIndex(map[string]modelcatalog.ModelFacts{
			"vendor/model-1": {Source: modelcatalog.FactSourceCatalog, MatchKind: modelcatalog.MatchExact},
		}),
	})
	boundCatalog := catalog.BoundView()
	ex.CatalogResolver = modelcatalog.NewCatalogResolver(
		modelcatalog.DefaultMatcher{},
		modelcatalog.NewOverrideResolver(modelcatalog.OverrideSet{}),
		true,
		catalog,
	)

	holder := &checkpoint.RequestHolder{}
	requestAuth := &requestAuthorityState{}
	callID, err := usageevidence.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	from = withTestRecvFacts(from, func(f recvTurnFacts) recvTurnFacts {
		f.metering = holder
		f.requestAuth = requestAuth
		f.recvViewsOK = true
		f.recvViews = execctx.Views{Session: session.SessionView{ClientSessionHint: "pinned-session"}}
		f.routePrefs = []string{"backend-1:vendor/model-1"}
		return f
	})
	from.recovery.sel, err = routing.Parse("backend-1:vendor/model-1")
	if err != nil {
		t.Fatalf("parse pinned selector: %v", err)
	}
	if err := routing.BindNativeModelIDs(from.recovery.sel, boundRegistry); err != nil {
		t.Fatalf("bind pinned native model: %v", err)
	}
	from = withTestRecvFacts(from, func(f recvTurnFacts) recvTurnFacts {
		f.secureTurnOK = true
		f.secureTurn = execctx.SecureSessionTurn{SessionID: domain.SessionID("secure-session"), TurnID: domain.TurnID("turn-A")}
		f.boundRegistry = boundRegistry
		f.boundRegistryOK = true
		f.boundCatalog = boundCatalog
		f.boundCatalogOK = true
		f.nativeResolver = boundRegistry
		f.modelViewID = modelview.Derive(7, "config-A", boundRegistry.Generation(), boundCatalog.Generation())
		f.modelViewIDOK = true
		f.billingCallID = callID
		return f
	})
	from = stampStreamIdentity(from)
	from.recovery.recoverPolicy = streamrecovery.NewPolicy(ex.StreamRecovery, ex.now())
	oldPolicy := from.recovery.recoverPolicy

	// Refresh both live publications after the thinker stream has captured A.
	catalog.PublishSnapshot(modelcatalog.Snapshot{
		Generation: "catalog-B",
		Index: modelcatalog.NewSnapshotIndex(map[string]modelcatalog.ModelFacts{
			"vendor/model-1": {Source: modelcatalog.FactSourceCatalog, MatchKind: modelcatalog.MatchExact},
		}),
	})
	provider.set([]modelinventory.Model{{CanonicalID: "vendor/model-1", NativeID: "native-B"}})
	registry.RunRefresh(context.Background())

	var openedCtx context.Context
	var openedCandidate routing.AttemptCandidate
	backend := ex.Backends["backend-1"]
	backend.Open = func(ctx context.Context, _ aipapi.Call, candidate routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
		openedCtx = ctx
		openedCandidate = candidate
		return aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventResponseFinished}}), nil
	}
	ex.Backends["backend-1"] = backend

	continuation, err := ex.openInterleavedExecutorContinuation(context.Background(), from, interleavedstate.State{})
	if err != nil {
		t.Fatalf("open interleaved continuation: %v", err)
	}
	defer func() { _ = continuation.Close() }()
	if continuation.recovery != from.recovery {
		t.Fatal("interleaved continuation must share the request recovery controller")
	}
	if from.recovery.recoverPolicy == nil || from.recovery.recoverPolicy == oldPolicy {
		t.Fatal("successful interleaved continuation must reset the shared thinker recovery policy")
	}
	if openedCtx == nil {
		t.Fatal("backend did not receive continuation context")
	}

	if got := diagTraceIDForTurnRecvPinning(openedCtx); got != from.facts.traceID {
		t.Fatalf("trace id = %q, want %q", got, from.facts.traceID)
	}
	gotViews, ok := execctx.FromContext(openedCtx)
	if !ok || gotViews.Session.ClientSessionHint != "pinned-session" {
		t.Fatalf("execution views = ok:%v %+v", ok, gotViews)
	}
	if got := execctx.RouteCandidatePreferences(openedCtx); len(got) != 1 || got[0] != "backend-1:vendor/model-1" {
		t.Fatalf("route preferences = %#v", got)
	}
	gotTurn, ok := execctx.SecureSessionTurnFromContext(openedCtx)
	if !ok || gotTurn.SessionID != domain.SessionID("secure-session") || gotTurn.TurnID != domain.TurnID("turn-A") {
		t.Fatalf("secure turn = ok:%v %+v", ok, gotTurn)
	}
	if meteringHolderFrom(openedCtx) != holder {
		t.Fatal("bare continuation context lost metering holder identity")
	}
	if requestAuthorityFrom(openedCtx) != requestAuth {
		t.Fatal("bare continuation context lost request authority identity")
	}
	gotRegistry, ok := modelregistry.BoundViewFromContext(openedCtx)
	if !ok || gotRegistry != boundRegistry {
		t.Fatalf("registry view = ok:%v generation:%q, want bound generation:%q", ok, gotRegistry.Generation(), boundRegistry.Generation())
	}
	gotCatalog, ok := modelcatalog.BoundViewFromContext(openedCtx)
	if !ok || gotCatalog != boundCatalog {
		t.Fatalf("catalog view = ok:%v generation:%q, want bound catalog-A", ok, gotCatalog.Generation())
	}
	gotResolver, ok := routing.NativeModelResolverFromContext(openedCtx)
	gotBinding := routing.ModelBinding{}
	if ok {
		gotBinding = gotResolver.ResolveModelBinding("backend-1", "vendor/model-1")
	}
	if !ok || gotBinding != (routing.ModelBinding{Kind: routing.ModelBindingExactCanonical, Native: "native-A"}) {
		t.Fatalf("native resolver binding = ok:%v %+v, want native-A", ok, gotBinding)
	}
	gotIdentity, ok := modelview.FromContext(openedCtx)
	if !ok || gotIdentity != from.facts.modelViewID {
		t.Fatalf("model-view identity = ok:%v %+v, want %+v", ok, gotIdentity, from.facts.modelViewID)
	}
	if openedCandidate.Primary.Model != "native-A" || openedCandidate.Primary.NativeModel != "native-A" {
		t.Fatalf("backend candidate = %+v, want pinned native-A", openedCandidate.Primary)
	}

	gotContinuation := continuation
	if gotContinuation.facts.billingCallID != callID || gotContinuation.facts.billingAccountID != from.facts.billingAccountID || !gotContinuation.facts.billingIdentityStamped {
		t.Fatalf("billing identity = call:%q account:%q stamped:%v, want call:%q account:%q stamped:true", gotContinuation.facts.billingCallID, gotContinuation.facts.billingAccountID, gotContinuation.facts.billingIdentityStamped, callID, from.facts.billingAccountID)
	}
	if gotContinuation.facts.boundRegistry != boundRegistry || gotContinuation.facts.boundCatalog != boundCatalog || gotContinuation.facts.modelViewID != from.facts.modelViewID {
		t.Fatalf("continuation model views were not copied: registry=%v catalog=%v identity=%+v", gotContinuation.facts.boundRegistry == boundRegistry, gotContinuation.facts.boundCatalog == boundCatalog, gotContinuation.facts.modelViewID)
	}
}

func authorityAdmissionResultForTurnRecvPinning() usagelimit.AdmissionResult {
	return usagelimit.AdmissionResult{
		Allowed: true, Reserved: true, ReservationID: "turn-recv-pinning-reservation",
		ReservedAmount: authorityInputAmount(9), PolicyRecord: policydecision.Record{ReasonCode: "reserved"},
	}
}

type turnRecvPinningInventory struct {
	mu     sync.Mutex
	models []modelinventory.Model
}

func (p *turnRecvPinningInventory) set(models []modelinventory.Model) {
	p.mu.Lock()
	p.models = append([]modelinventory.Model(nil), models...)
	p.mu.Unlock()
}

func (p *turnRecvPinningInventory) LoadModels(context.Context) (modelinventory.Snapshot, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return modelinventory.Snapshot{Source: modelinventory.SourceRemote, Models: append([]modelinventory.Model(nil), p.models...)}, nil
}

// Keep the context assertion in this file independent from the broad test helper
// surface; the diagnostic accessor itself is part of the pinned Recv contract.
func diagTraceIDForTurnRecvPinning(ctx context.Context) string {
	return diag.TraceID(ctx)
}
