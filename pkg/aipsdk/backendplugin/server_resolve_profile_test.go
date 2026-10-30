package backendplugin_test

import (
	"context"
	"strings"
	"testing"

	backendpluginv1 "github.com/aiproxer/aiproxer/api/backendplugin/v1"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/promptcache"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// resolveProfileService serves one connector-authored resolved profile so the
// ResolveProfile RPC boundary can be exercised with valid and invalid profiles.
type resolveProfileService struct {
	stubService
	profile backendplugin.ResolvedProfile
}

func (s *resolveProfileService) Configure(context.Context, backendplugin.ConfigureRequest) (backendplugin.ConfiguredInstance, error) {
	return &resolveProfileInstance{profile: s.profile}, nil
}

type resolveProfileInstance struct {
	stubInstance
	profile backendplugin.ResolvedProfile
}

func (i *resolveProfileInstance) Resolve(context.Context, *string) (backendplugin.ResolvedProfile, error) {
	return i.profile, nil
}

// TestGRPCServer_ResolveProfileRejectsUnrepresentableProfile proves the RPC
// reports an invalid connector-authored profile instead of returning success
// with an empty profile that the host would later misclassify.
func TestGRPCServer_ResolveProfileRejectsUnrepresentableProfile(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name          string
		profile       backendplugin.ResolvedProfile
		wantCode      codes.Code
		wantCacheKind backendpluginv1.PromptCacheLifecycle
	}{
		{
			name: "valid prompt cache profile",
			profile: backendplugin.ResolvedProfile{
				Capabilities: backendplugin.CapabilitySummary{Streaming: true},
				PromptCacheProfile: promptcache.Profile{
					ObservationSupported: true,
					LifecycleKinds:       []promptcache.LifecycleKind{promptcache.LifecycleSlidingExpiry},
				},
			},
			wantCode:      codes.OK,
			wantCacheKind: backendpluginv1.PromptCacheLifecycle_PROMPT_CACHE_LIFECYCLE_SLIDING_EXPIRY,
		},
		{
			name: "renewal without observation",
			profile: backendplugin.ResolvedProfile{
				Capabilities:       backendplugin.CapabilitySummary{Streaming: true},
				PromptCacheProfile: promptcache.Profile{RenewalSupported: true},
			},
			wantCode: codes.InvalidArgument,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			client, stop := startBufServer(t, &resolveProfileService{profile: tc.profile})
			defer stop()
			token := negotiateOK(t, client)
			if _, err := client.Configure(context.Background(), &backendpluginv1.ConfigureRequest{
				InstanceId: "i1", FactoryKind: "stub", NegotiationToken: token,
				RuntimePolicy: &backendpluginv1.RuntimePolicy{DisableTransportRetries: true},
			}); err != nil {
				t.Fatal(err)
			}

			resp, err := client.ResolveProfile(context.Background(), &backendpluginv1.ResolveProfileRequest{InstanceId: "i1"})
			if got := status.Code(err); got != tc.wantCode {
				t.Fatalf("code=%v err=%v response=%+v want code=%v", got, err, resp, tc.wantCode)
			}
			if tc.wantCode != codes.OK {
				if !strings.Contains(status.Convert(err).Message(), "renewal requires observation") {
					t.Fatalf("err=%v want the prompt cache validation cause", err)
				}
				return
			}
			if resp.GetProfile() == nil {
				t.Fatal("valid profile must be encoded on the wire")
			}
			kinds := resp.GetProfile().GetPromptCacheProfile().GetLifecycleKinds()
			if len(kinds) != 1 || kinds[0] != tc.wantCacheKind {
				t.Fatalf("lifecycle kinds=%v want [%v]", kinds, tc.wantCacheKind)
			}
		})
	}
}
