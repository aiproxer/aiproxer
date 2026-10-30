package standardplugins

import (
	"context"
	"sync"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/featurebundle"
	"github.com/aiproxer/aiproxer/internal/plugins/features/downstreamcacheaffinity"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/stretchr/testify/require"
)

type cacheAffinityPartHook struct{ observed sync.Map }

func (*cacheAffinityPartHook) ID() string                        { return "cache-affinity-delivery-probe" }
func (*cacheAffinityPartHook) Order() int                        { return 0 }
func (*cacheAffinityPartHook) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }

func (h *cacheAffinityPartHook) HandleRequestParts(_ context.Context, call *aipapi.Call, _ sdkhooks.PartMeta) error {
	// Prove final scrub removes late hook writes too.
	call.Session.ContinuityKey = "late-legacy-continuity"
	call.Session.ResumeToken = "late-resume-token"
	h.observed.Store("call", aipapi.CloneCall(*call))
	call.Messages = append(call.Messages, aipapi.Message{
		Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hook-appended")},
	})
	return nil
}

func TestStandardBundle_DownstreamCacheAffinityReachesBackendOpen(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, clientKey string }{
		{"generated", ""},
		{"explicit", "client-owned-cache-key"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg := testRegistryWithStdBundle(t)
			merged, err := featurebundle.MergeFeatureSurfacesWithHost(reg, []aipsdk.Registration{
				{ID: downstreamcacheaffinity.ID, Kind: aipsdk.PluginKindFeature, Enabled: true},
			}, featurebundle.HostContributions{})
			require.NoError(t, err)
			var capture sync.Map
			// Real secure-session admission; ordered-items forces candidate adaptation.
			ex := testkit.NewStubExecutor(t, aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityOrderedItems), "ok", &capture)
			hook := &cacheAffinityPartHook{}
			ex.Bus = hooks.New(hooks.Config{RequestPartHooks: []sdkhooks.RequestPartHook{hook}})
			ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(ex.Bus, extensions.SnapshotOptions{FeaturePlanes: merged.Frozen})
			be := ex.Backends["stub"]
			open := be.Open
			be.Open = func(ctx context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				capture.Store("candidate", cand)
				return open(ctx, call, cand)
			}
			ex.Backends["stub"] = be
			call := &aipapi.Call{
				Session:        aipapi.SessionRef{ClientSessionID: "client-hint", ContinuityKey: "legacy-continuity"},
				Route:          aipapi.RouteIntent{Selector: "stub:gpt-4o-mini"},
				Messages:       []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hello")}}},
				PromptCacheKey: tc.clientKey,
				Invocation: aipapi.Invocation{
					Operation:    aipapi.OperationOpenAIChatCompletions,
					DeliveryMode: aipapi.DeliveryModeNonStreaming, TransportMode: aipapi.TransportModeNonStreaming,
				},
			}
			stream, err := ex.Execute(t.Context(), call)
			require.NoError(t, err)
			_, err = aipapi.Collect(t.Context(), stream)
			require.NoError(t, err)
			v, ok := capture.Load("last")
			require.True(t, ok, "backend Open was not called")
			delivered, ok := v.(aipapi.Call)
			require.True(t, ok, "backend Open capture has type %T, want aipapi.Call", v)
			v, ok = hook.observed.Load("call")
			require.True(t, ok, "request-part hook did not run")
			beforeHook, ok := v.(aipapi.Call)
			require.True(t, ok, "request-part hook capture has type %T, want aipapi.Call", v)
			require.NotEmpty(t, call.Session.AuthoritativeSessionID)
			require.NotEmpty(t, call.Session.ResumeToken)
			candValue, ok := capture.Load("candidate")
			require.True(t, ok, "backend Open candidate was not captured")
			cand, ok := candValue.(routing.AttemptCandidate)
			require.True(t, ok, "backend Open candidate capture has type %T, want routing.AttemptCandidate", candValue)
			require.Equal(t, "stub", cand.Primary.Backend)
			require.Equal(t, call.Route.Selector, delivered.Route.Selector)
			require.Empty(t, delivered.PreviousResponseID)
			want := tc.clientKey
			if want == "" {
				want = downstreamcacheaffinity.Derive(cand.Primary.Backend, call.Session.AuthoritativeSessionID)
			}
			for _, observed := range []aipapi.Call{beforeHook, delivered} {
				key, err := observed.PromptCacheKeyValue()
				require.NoError(t, err)
				require.Equal(t, want, key)
			}
			require.Empty(t, beforeHook.Items)
			require.Len(t, beforeHook.Messages, 1)
			require.Empty(t, delivered.Messages)
			require.Len(t, delivered.Items, 2)
			require.Len(t, delivered.Items[1].Content, 1)
			require.Equal(t, "hook-appended", delivered.Items[1].Content[0].Text)
			for _, raw := range []string{
				beforeHook.Session.ClientSessionID, beforeHook.Session.ContinuityKey,
				beforeHook.Session.AuthoritativeSessionID, beforeHook.Session.ResumeToken,
			} {
				require.NotEmpty(t, raw)
			}
			require.Equal(t, aipapi.SessionRef{ALegID: beforeHook.Session.ALegID}, delivered.Session, "scrub raw session credentials, preserve A-leg correlation")
		})
	}
}
