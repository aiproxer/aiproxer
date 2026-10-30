package runtime_test

import (
	"bytes"
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/execctx"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
	sdktraffic "github.com/aiproxer/aiproxer/pkg/aipsdk/traffic"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/usage"
)

// phase6BackendOpenCapture captures the backend open context (the streaming entry point) so the
// authoritative scope visible to the backend can be compared against observer evidence collected
// from the non-streaming collection path (aipapi.Collect).
func phase6BackendOpenCapture(openCtx *context.Context, opens *atomic.Int32) execbackend.Backend {
	return execbackend.Backend{
		Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
		Open: func(ctx context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			opens.Add(1)
			*openCtx = ctx
			return aipapi.NewFixedEventStream([]aipapi.Event{
				{Kind: aipapi.EventResponseStarted},
				{Kind: aipapi.EventUsageDelta, InputTokens: 3, RawUsageJSON: `{"usage":true}`},
				{Kind: aipapi.EventResponseFinished},
			}), nil
		},
	}
}

// phase6ExecutorWithObservers builds an executor wired to the supplied usage and traffic observers
// and a single backend, so evidence emission is observable without a full composition root.
func phase6ExecutorWithObservers(t *testing.T, uobs usage.Observer, tobs sdktraffic.Observer, openCtx *context.Context, opens *atomic.Int32) *runtime.Executor {
	t.Helper()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	snap := extensions.NewRequestRuntimeSnapshot(hooks.New(hooks.Config{}), extensions.SnapshotOptions{
		UsageObserver:   uobs,
		TrafficObserver: tobs,
	})
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.RuntimeSnapshot = snap
	ex.Backends = map[string]execbackend.Backend{
		"openai": phase6BackendOpenCapture(openCtx, opens),
	}
	ex.Rand = routing.NewSeededRng(1)
	return ex
}

// TestPhase6_streamingAndNonStreamingCarrySameScope proves the authoritative scope visible at the
// streaming backend-open entry point is identical to the scope carried on usage evidence collected
// via the non-streaming canonical collection path (requirement 7.6, 6.5).
func TestPhase6_streamingAndNonStreamingCarrySameScope(t *testing.T) {
	t.Parallel()
	uobs := &scopeCaptureUsage{}
	var openCtx context.Context
	var opens atomic.Int32
	ex := phase6ExecutorWithObservers(t, uobs, nil, &openCtx, &opens)
	trusted := scope.PrincipalScopeView{
		SubjectKind: scope.SubjectHuman,
		PrincipalID: scope.Known("scope-user-7-6"),
		TenantID:    scope.Known("t-7-6"),
		Roles:       []string{"admin"},
	}
	ctx := scope.WithScope(context.Background(), trusted)
	stream, err := ex.Execute(ctx, &aipapi.Call{
		Route:    aipapi.RouteIntent{Selector: "openai:gpt-4"},
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := aipapi.Collect(context.Background(), stream); err != nil {
		t.Fatalf("Collect: %v", err)
	}

	openViews, ok := execctx.FromContext(openCtx)
	if !ok {
		t.Fatal("expected execctx views on backend open context")
	}
	if !openViews.Scope.PrincipalID.Equal(trusted.PrincipalID) {
		t.Fatalf("streaming open scope PrincipalID: %+v want %q", openViews.Scope.PrincipalID, trusted.PrincipalID)
	}
	uobs.mu.Lock()
	defer uobs.mu.Unlock()
	if len(uobs.events) == 0 {
		t.Fatal("expected usage events on collection path")
	}
	uev := uobs.events[0]
	if !uev.Scope.PrincipalID.Equal(openViews.Scope.PrincipalID) {
		t.Fatalf("usage scope PrincipalID %+v must equal streaming scope %+v", uev.Scope.PrincipalID, openViews.Scope.PrincipalID)
	}
	if !uev.Scope.TenantID.Equal(openViews.Scope.TenantID) {
		t.Fatalf("usage scope TenantID %+v must equal streaming scope %+v", uev.Scope.TenantID, openViews.Scope.TenantID)
	}
}

// TestPhase6_missingOptionalScopeDoesNotChangeRoutingOrAttempts proves the presence or absence of
// optional tenant/project/department/cost-center attribution does not alter backend attempt
// selection or attempt count (requirements 7.2, 7.5, 8.5).
func TestPhase6_missingOptionalScopeDoesNotChangeRoutingOrAttempts(t *testing.T) {
	t.Parallel()
	run := func(t *testing.T, sc scope.PrincipalScopeView) (string, int32) {
		t.Helper()
		var openCtx context.Context
		var opens atomic.Int32
		ex := phase6ExecutorWithObservers(t, nil, nil, &openCtx, &opens)
		ctx := scope.WithScope(context.Background(), sc)
		stream, err := ex.Execute(ctx, &aipapi.Call{
			Route:    aipapi.RouteIntent{Selector: "openai:gpt-4"},
			Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}}},
		})
		if err != nil {
			t.Fatal(err)
		}
		_, _ = aipapi.Collect(context.Background(), stream)
		v, ok := execctx.FromContext(openCtx)
		if !ok {
			t.Fatal("expected open context views")
		}
		return v.Scope.PrincipalID.String(), opens.Load()
	}

	rich := scope.PrincipalScopeView{
		SubjectKind:    scope.SubjectHuman,
		PrincipalID:    scope.Known("user-r"),
		TenantID:       scope.Known("t-r"),
		ProjectID:      scope.Known("p-r"),
		DepartmentID:   scope.Known("d-r"),
		CostCenterID:   scope.Known("c-r"),
		OrganizationID: scope.Known("o-r"),
	}
	minScope := scope.PrincipalScopeView{
		SubjectKind: scope.SubjectHuman,
		PrincipalID: scope.Known("user-r"),
	}
	_, richOpens := run(t, rich)
	minID, minOpens := run(t, minScope)
	if richOpens != minOpens {
		t.Fatalf("optional scope changed attempt count: rich=%d min=%d", richOpens, minOpens)
	}
	if minID != "user-r" {
		t.Fatalf("principal id drifted without optional scope: %q", minID)
	}
}

// TestPhase6_canonicalRequestShapeUnchangedByScope proves the client-to-proxy canonical request
// payload does not carry scope attribution and that scope attachment does not alter the structural
// request shape (messages, route, tools, options). Generated session/ALeg ids are non-deterministic
// per run and are excluded from the comparison. (requirements 7.1, 7.4)
func TestPhase6_canonicalRequestShapeUnchangedByScope(t *testing.T) {
	t.Parallel()
	ctpCall := func(t *testing.T, withScope bool) aipapi.Call {
		t.Helper()
		tobs := &scopeCaptureTraffic{}
		var openCtx context.Context
		var opens atomic.Int32
		ex := phase6ExecutorWithObservers(t, nil, tobs, &openCtx, &opens)
		ctx := context.Background()
		if withScope {
			ctx = scope.WithScope(ctx, scope.PrincipalScopeView{
				SubjectKind: scope.SubjectHuman,
				PrincipalID: scope.Known("shape-user"),
				TenantID:    scope.Known("shape-tenant"),
				Roles:       []string{"admin"},
				SafeClaims:  map[string]string{"team": "core"},
			})
		}
		stream, err := ex.Execute(ctx, &aipapi.Call{
			Route:    aipapi.RouteIntent{Selector: "openai:gpt-4"},
			Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}}},
		})
		if err != nil {
			t.Fatal(err)
		}
		_, _ = aipapi.Collect(context.Background(), stream)
		tobs.mu.Lock()
		defer tobs.mu.Unlock()
		for _, o := range tobs.obs {
			if o.Leg == sdktraffic.LegCTP {
				if bytes.Contains(o.Body, []byte("shape-user")) || bytes.Contains(o.Body, []byte("shape-tenant")) {
					t.Fatalf("scope attribution leaked into canonical client request payload: %s", o.Body)
				}
				if bytes.Contains(o.Body, []byte(`"Roles"`)) || bytes.Contains(o.Body, []byte(`"SafeClaims"`)) || bytes.Contains(o.Body, []byte(`"PrincipalID"`)) {
					t.Fatalf("scope contract fields leaked into canonical client request payload: %s", o.Body)
				}
				var c aipapi.Call
				if err := json.Unmarshal(o.Body, &c); err != nil {
					t.Fatalf("unmarshal CTP call: %v", err)
				}
				return c
			}
		}
		t.Fatal("no CTP observation captured")
		return aipapi.Call{}
	}

	withoutScope := ctpCall(t, false)
	withScope := ctpCall(t, true)
	// Structural request shape must be identical; only generated session/ALeg ids may differ.
	if withoutScope.Route.Selector != withScope.Route.Selector {
		t.Fatalf("Route.Selector changed by scope: %q vs %q", withoutScope.Route.Selector, withScope.Route.Selector)
	}
	if len(withoutScope.Messages) != len(withScope.Messages) {
		t.Fatalf("Messages length changed by scope: %d vs %d", len(withoutScope.Messages), len(withScope.Messages))
	}
	if withoutScope.Messages[0].Parts[0].Text != withScope.Messages[0].Parts[0].Text {
		t.Fatalf("message text changed by scope: %q vs %q", withoutScope.Messages[0].Parts[0].Text, withScope.Messages[0].Parts[0].Text)
	}
	if withoutScope.Tools != nil || withScope.Tools != nil {
		t.Fatalf("Tools changed by scope: without=%v with=%v", withoutScope.Tools, withScope.Tools)
	}
}
