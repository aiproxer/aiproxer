package runtime_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	coreauth "github.com/aiproxer/aiproxer/internal/core/auth"
	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdkauth "github.com/aiproxer/aiproxer/pkg/aipsdk/auth"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

// phase6SessionSink captures session-start audit events for secret-safety verification.
type phase6SessionSink struct {
	mu  sync.Mutex
	evs []sdkauth.SessionStartEvent
}

func (s *phase6SessionSink) OnAuthDecision(context.Context, sdkauth.AuthDecisionEvent) error {
	return nil
}

func (s *phase6SessionSink) OnSessionStart(_ context.Context, ev sdkauth.SessionStartEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.evs = append(s.evs, ev)
	return nil
}

func (s *phase6SessionSink) snapshot() []sdkauth.SessionStartEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]sdkauth.SessionStartEvent, len(s.evs))
	copy(out, s.evs)
	return out
}

// phase6SecureExecutor builds an executor with an auth-event dispatcher wired to sink. SecureSession
// is intentionally left nil so Execute auto-wires the test secure-session manager (export_test.go),
// keeping the test free of internal helpers.
func phase6SecureExecutor(t *testing.T, sink *phase6SessionSink) *runtime.Executor {
	t.Helper()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	disp := coreauth.NewEventDispatcher(sink, coreauth.EventFailureBestEffort)
	snap := extensions.NewRequestRuntimeSnapshot(hooks.New(hooks.Config{}), extensions.SnapshotOptions{})
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.RuntimeSnapshot = snap
	ex.Now = func() time.Time { return time.Unix(3000, 0).UTC() }
	ex.AuthEvents = disp
	ex.SessionAuditPolicy = coreauth.SessionAuditPolicy{}
	ex.Backends = map[string]execbackend.Backend{
		"openai": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventResponseFinished}}), nil
			},
		},
	}
	ex.Rand = routing.NewSeededRng(1)
	return ex
}

// TestPhase6_sessionStartEvidenceDerivedFromScopeAndSecretFree proves that when an authoritative
// scope is attached, the session-start audit event's principal fields are derived from the scope
// projection and the event carries no raw secret material (requirements 6.2, 5.2, 4.6).
func TestPhase6_sessionStartEvidenceDerivedFromScopeAndSecretFree(t *testing.T) {
	t.Parallel()
	sink := &phase6SessionSink{}
	ex := phase6SecureExecutor(t, sink)
	trusted := scope.PrincipalScopeView{
		SubjectKind:  scope.SubjectHuman,
		PrincipalID:  scope.Known("scope-session-user"),
		DisplayName:  scope.Known("Scope Alice"),
		AuthMethod:   scope.Known("oidc"),
		CredentialID: scope.Known("key-session"),
		TenantID:     scope.Known("t-session"),
		Roles:        []string{"ops"},
		SafeClaims:   map[string]string{"team": "core"},
	}
	ctx := scope.WithScope(context.Background(), trusted)
	ctx = execview.WithFrontendID(ctx, "anthropic")
	stream, err := ex.Execute(ctx, &aipapi.Call{
		Route:    aipapi.RouteIntent{Selector: "openai:gpt-4"},
		Session:  aipapi.SessionRef{ClientSessionID: "client-hint-phase6"},
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = aipapi.Collect(context.Background(), stream)

	evs := sink.snapshot()
	if len(evs) != 1 {
		t.Fatalf("session-start events: want 1 got %d", len(evs))
	}
	ev := evs[0]
	if ev.PrincipalID != "scope-session-user" || ev.PrincipalDisplayName != "Scope Alice" {
		t.Fatalf("session evidence principal must derive from scope: id=%q name=%q", ev.PrincipalID, ev.PrincipalDisplayName)
	}
	joined := strings.Join([]string{ev.PrincipalID, ev.PrincipalDisplayName, ev.SessionID, ev.ClientSessionRef, ev.ALegID}, " ")
	for _, bad := range []string{"bearer ", "key-session", "secret", "access_token", "authorization:"} {
		if strings.Contains(strings.ToLower(joined), bad) {
			t.Fatalf("session evidence leaked secret-like material %q: %s", bad, joined)
		}
	}
}

// TestPhase6_observerEventScopeIsCopy proves usage and traffic observer events receive independent
// copies of the authoritative scope: mutating one emitted event's scope slices/maps does not affect
// another event's scope from the same request (requirements 5.5, 5.3).
func TestPhase6_observerEventScopeIsCopy(t *testing.T) {
	t.Parallel()
	uobs := &scopeCaptureUsage{}
	tobs := &scopeCaptureTraffic{}
	ex := scopeEmissionExecutor(t, uobs, tobs)
	trusted := scope.PrincipalScopeView{
		SubjectKind:  scope.SubjectHuman,
		PrincipalID:  scope.Known("copy-user"),
		Roles:        []string{"admin", "ops"},
		SafeClaims:   map[string]string{"team": "core"},
		PolicyLabels: map[string]string{"env": "prod"},
	}
	ctx := scope.WithScope(context.Background(), trusted)
	stream, err := ex.Execute(ctx, &aipapi.Call{
		Route:    aipapi.RouteIntent{Selector: "openai:gpt-4"},
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = aipapi.Collect(context.Background(), stream)

	uobs.mu.Lock()
	if len(uobs.events) == 0 {
		t.Fatal("expected usage events")
	}
	uev := uobs.events[0]
	if uev.Scope.Roles[0] != "admin" {
		t.Fatalf("usage scope Roles: %+v", uev.Scope.Roles)
	}
	uev.Scope.Roles[0] = "mutated"
	uev.Scope.SafeClaims["team"] = "mutated"
	uev.Scope.PolicyLabels["env"] = "mutated"
	uobs.mu.Unlock()

	tobs.mu.Lock()
	defer tobs.mu.Unlock()
	for _, o := range tobs.obs {
		if !o.Scope.PrincipalID.IsKnown() {
			continue
		}
		if o.Scope.Roles[0] == "mutated" {
			t.Fatal("traffic scope Roles mutated via usage event copy (no isolation)")
		}
		if o.Scope.SafeClaims["team"] == "mutated" {
			t.Fatal("traffic scope SafeClaims mutated via usage event copy (no isolation)")
		}
		if o.Scope.PolicyLabels["env"] == "mutated" {
			t.Fatal("traffic scope PolicyLabels mutated via usage event copy (no isolation)")
		}
	}
}
