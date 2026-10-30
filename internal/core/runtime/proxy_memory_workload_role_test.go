package runtime

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/auxreq"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/usageevidence"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

// TestProxyMemoryAuxiliaryRoleAuthoritySeam is the focused core seam of the
// auxiliary role registration (design §2, Requirement 1.1): a registered
// auxiliary role reaches the backend and opens it exactly once, while an
// unknown role is refused by the request-authority admission before the
// backend is opened at all.
//
// The seam is driven through the real auxiliary client and the real executor,
// so the workload projection is the production one. The core package may not
// import the Proxy Memory feature, so the roles are named by their registered
// spelling. The terminal attribution of the registered roles is observed once,
// by the composed production-path proof in the featurehost package.
func TestProxyMemoryAuxiliaryRoleAuthoritySeam(t *testing.T) {
	principalID := "principal-proxy-memory-role"
	ex := newReasoningCompressorExecutor(t, &reasoningCompressorCapture{}, "acct:"+principalID)
	var opens atomic.Int64
	ex.Backends = map[string]execbackend.Backend{
		"backend": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				opens.Add(1)
				return aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventResponseFinished},
				}), nil
			},
		},
	}
	ctx := scope.WithScope(context.Background(), scope.PrincipalScopeView{
		SubjectKind: scope.SubjectHuman,
		PrincipalID: scope.Known(principalID),
		Origin:      scope.OriginClient,
	})
	client := auxreq.NewClient(func() auxreq.ExecutorRunner { return ex })
	for _, tc := range []struct {
		role     string
		admitted bool
	}{
		{role: usageevidence.WorkloadRoleCompactionContinuityExtractor, admitted: true},
		{role: "proxy_memory_phase1", admitted: true},
		{role: "proxy_memory_phase2", admitted: true},
		{role: "user-supplied-role"},
		{role: "PROXY_MEMORY_PHASE1"},
		{role: "proxy_memory_phase1_raw"},
		{role: "proxy_memory_phase3"},
	} {
		t.Run(tc.role, func(t *testing.T) {
			before := opens.Load()
			stream, err := client.Stream(ctx, auxiliary.Request{
				Role:        tc.role,
				Visibility:  "private",
				SessionMode: auxiliary.SessionModeDetached,
				Call: &aipapi.Call{
					Route:    aipapi.RouteIntent{Selector: "backend:aux-model"},
					Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("auxiliary request")}}},
				},
			})
			if tc.admitted {
				if err != nil {
					t.Fatalf("registered role %q was rejected: %v", tc.role, err)
				}
				if _, err := aipapi.Collect(ctx, stream); err != nil {
					t.Fatalf("registered role %q: collect: %v", tc.role, err)
				}
				if got := opens.Load() - before; got != 1 {
					t.Fatalf("registered role %q opened the backend %d times, want exactly one", tc.role, got)
				}
				return
			}
			if err == nil {
				_, _ = aipapi.Collect(ctx, stream)
				t.Fatalf("unknown role %q was admitted", tc.role)
			}
			if !errors.Is(err, usageevidence.ErrInvalidWorkloadIdentity) {
				t.Fatalf("unknown role %q error = %v, want ErrInvalidWorkloadIdentity", tc.role, err)
			}
			if got := opens.Load() - before; got != 0 {
				t.Fatalf("unknown role %q opened the backend %d times, want zero", tc.role, got)
			}
		})
	}
}
