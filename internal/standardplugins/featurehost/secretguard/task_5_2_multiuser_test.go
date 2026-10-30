package secretguard_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/accessmode"
	coreauth "github.com/aiproxer/aiproxer/internal/core/auth"
	"github.com/aiproxer/aiproxer/internal/plugins/features/secretguard"
	sgcompose "github.com/aiproxer/aiproxer/internal/standardplugins/featurehost/secretguard"
	stdhttpauth "github.com/aiproxer/aiproxer/internal/stdhttp/auth"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	sdkauth "github.com/aiproxer/aiproxer/pkg/aipsdk/auth"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/transport/httpauth"
	"github.com/stretchr/testify/require"
)

func TestTask52MultiUserComposition_resolvesAcceptedCredentialFromActualIngress(t *testing.T) {
	t.Parallel()

	const credential = "opaque-composed-request-credential-task-5-2-2026"
	for _, betterLeaksEnabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "betterleaks_off", true: "betterleaks_on"}[betterLeaksEnabled], func(t *testing.T) {
			t.Parallel()

			env := &panicEnv{}
			runtimeCfg := &secretguard.RuntimeConfig{
				Enabled:            true,
				Action:             "block",
				BetterLeaksEnabled: betterLeaksEnabled,
			}
			out, err := sgcompose.Compose(sgcompose.Input{
				AccessMode:    accessmode.ModeMultiUser,
				RuntimeConfig: runtimeCfg,
				Environment:   env,
				Logger:        discardLogger(),
			})
			require.NoError(t, err)
			require.NotNil(t, out)
			require.NotNil(t, out.Plane.MatcherResolver)

			authenticator, err := coreauth.NewLocalAPIKeyAuthenticator([]coreauth.LocalAPIKeyRecord{
				{KeyID: "composed-kid", PrincipalID: "composed-user", Key: credential},
			})
			require.NoError(t, err)
			provider := stdhttpauth.NewPolicyProvider(&coreauth.PolicyAuthenticator{
				Handler:  sdkauth.HandlerLocalAPIKey,
				Required: sdkauth.LevelAPIKey,
				APIKey:   authenticator,
			}, nil, stdhttpauth.PolicySnapshot{
				AccessMode: sdkauth.AccessMultiUser, HandlerKind: sdkauth.HandlerLocalAPIKey, RequiredLevel: sdkauth.LevelAPIKey,
			}, nil)

			var gotCtx context.Context
			h := stdhttpauth.Middleware(nil, []httpauth.Provider{provider}, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				gotCtx = r.Context()
			}))
			request := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			request.Header.Set(aipsdk.HeaderAuthorization, "Bearer "+credential)
			response := httptest.NewRecorder()
			h.ServeHTTP(response, request)
			require.Equal(t, http.StatusOK, response.Code)
			require.NotNil(t, gotCtx)
			require.Equal(t, 0, env.calls, "multi_user request execution must never consult the process environment")

			matcher, err := out.Plane.MatcherResolver.Resolve(gotCtx)
			require.NoError(t, err)
			require.NotNil(t, matcher, "multi_user composition must resolve the accepted ingress matcher")
			findings, err := matcher.ScanString(gotCtx, `{"api_key":"`+credential+`"}`)
			require.NoError(t, err)
			require.Len(t, findings, 1)
			require.Equal(t, sdk.SourceCategoryRequestCred, findings[0].SourceCategory)
			require.Equal(t, 1, findings[0].OccurrenceCount)

			withoutRequestCredential, err := out.Plane.MatcherResolver.Resolve(context.Background())
			require.NoError(t, err)
			require.Nil(t, withoutRequestCredential, "request credential matcher must not escape its request context")
		})
	}
}
