package runtime_test

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/execctx"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

func scopeTestBackendOpenCapture(openCtx *context.Context, opens *atomic.Int32) execbackend.Backend {
	return execbackend.Backend{
		Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
		Open: func(ctx context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			opens.Add(1)
			*openCtx = ctx
			return aipapi.NewFixedEventStream([]aipapi.Event{
				{Kind: aipapi.EventResponseFinished},
			}), nil
		},
	}
}

// TestExecutor_OpenContext_carriesTrustedScopeInViews proves the authoritative scope from
// the trusted context is available on the backend open context before backend work starts
// and the principal view is derived from it (requirements 4.1, 4.6, 2.2).
func TestExecutor_OpenContext_carriesTrustedScopeInViews(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var openCtx context.Context
	var opens atomic.Int32
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Backends = map[string]execbackend.Backend{
		"openai": scopeTestBackendOpenCapture(&openCtx, &opens),
	}
	ex.Rand = routing.NewSeededRng(3)
	trusted := scope.PrincipalScopeView{
		SubjectKind: scope.SubjectHuman,
		PrincipalID: scope.Known("scope-user"),
		TenantID:    scope.Known("t1"),
		Roles:       []string{"admin"},
	}
	ctx := scope.WithScope(context.Background(), trusted)
	// Legacy principal in context must NOT override the trusted scope (req 2.2).
	ctx = execview.WithPrincipal(ctx, execview.PrincipalView{ID: "legacy-loses"})
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
		t.Fatal("expected execctx views on backend open context")
	}
	if !v.Scope.PrincipalID.Equal(scope.Known("scope-user")) {
		t.Fatalf("scope PrincipalID: %+v", v.Scope.PrincipalID)
	}
	if v.Principal.ID != "scope-user" {
		t.Fatalf("principal must derive from scope, got %q", v.Principal.ID)
	}
	if !v.Scope.TenantID.Equal(scope.Known("t1")) {
		t.Fatalf("scope TenantID: %+v", v.Scope.TenantID)
	}
}

// TestExecutor_OpenContext_legacyPrincipalDerivesScope proves a legacy principal-only
// context (no scope) still yields a scope on the backend open context (requirement 4.2).
func TestExecutor_OpenContext_legacyPrincipalDerivesScope(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var openCtx context.Context
	var opens atomic.Int32
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Backends = map[string]execbackend.Backend{
		"openai": scopeTestBackendOpenCapture(&openCtx, &opens),
	}
	ex.Rand = routing.NewSeededRng(3)
	ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "transport-user"})
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
		t.Fatal("expected execctx views")
	}
	if !v.Scope.PrincipalID.Equal(scope.Known("transport-user")) {
		t.Fatalf("scope PrincipalID: %+v", v.Scope.PrincipalID)
	}
	if v.Principal.ID != "transport-user" {
		t.Fatalf("principal id: want transport-user got %q", v.Principal.ID)
	}
}

// TestExecutor_MultiAttempt_sharesRequestScope proves multiple backend attempts for one
// logical request share the same authoritative request scope without changing recovery
// semantics (requirement 4.5). The first backend returns a recoverable pre-output error so
// the executor opens a second attempt; both open contexts must carry the same scope.
func TestExecutor_MultiAttempt_sharesRequestScope(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var firstOpenCtx, secondOpenCtx context.Context
	var opens atomic.Int32
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Backends = map[string]execbackend.Backend{
		"fail": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(ctx context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				opens.Add(1)
				firstOpenCtx = ctx
				return nil, fmt.Errorf("boom: %w", aipapi.ErrRecoverablePreOutput)
			},
		},
		"ok": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(ctx context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				opens.Add(1)
				secondOpenCtx = ctx
				return aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventResponseFinished},
				}), nil
			},
		},
	}
	ex.Rand = routing.NewSeededRng(2)
	trusted := scope.PrincipalScopeView{
		SubjectKind: scope.SubjectHuman,
		PrincipalID: scope.Known("shared-user"),
		TenantID:    scope.Known("t-shared"),
	}
	ctx := scope.WithScope(context.Background(), trusted)
	stream, err := ex.Execute(ctx, &aipapi.Call{
		Route:    aipapi.RouteIntent{Selector: "fail:gpt-4|ok:gpt-4"},
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := aipapi.Collect(context.Background(), stream); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if opens.Load() < 2 {
		t.Fatalf("expected at least 2 opens, got %d", opens.Load())
	}
	v1, ok1 := execctx.FromContext(firstOpenCtx)
	v2, ok2 := execctx.FromContext(secondOpenCtx)
	if !ok1 || !ok2 {
		t.Fatalf("expected views on both attempts: ok1=%v ok2=%v", ok1, ok2)
	}
	if !v1.Scope.PrincipalID.Equal(v2.Scope.PrincipalID) {
		t.Fatalf("attempt scopes differ: %+v vs %+v", v1.Scope.PrincipalID, v2.Scope.PrincipalID)
	}
	if !v1.Scope.TenantID.Equal(v2.Scope.TenantID) {
		t.Fatalf("attempt tenant scopes differ: %+v vs %+v", v1.Scope.TenantID, v2.Scope.TenantID)
	}
	if !v1.Scope.PrincipalID.Equal(scope.Known("shared-user")) {
		t.Fatalf("shared scope PrincipalID: %+v", v1.Scope.PrincipalID)
	}
}

// TestExecutor_MultiAttempt_recoversOnPreOutputError confirms recovery semantics are
// preserved: a recoverable pre-output failure on the first attempt still yields a finished
// stream from the second attempt (requirement 4.5, 7.5).
func TestExecutor_MultiAttempt_recoversOnPreOutputError(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Backends = map[string]execbackend.Backend{
		"fail": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return nil, fmt.Errorf("boom: %w", aipapi.ErrRecoverablePreOutput)
			},
		},
		"ok": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventResponseFinished},
				}), nil
			},
		},
	}
	ex.Rand = routing.NewSeededRng(2)
	stream, err := ex.Execute(context.Background(), &aipapi.Call{
		Route:    aipapi.RouteIntent{Selector: "fail:gpt-4|ok:gpt-4"},
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	collected, err := aipapi.Collect(context.Background(), stream)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if !collected.FinishReceived {
		t.Fatal("expected finished stream from second attempt")
	}
}
