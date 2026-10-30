//go:build integration

package conformance

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	coreauth "github.com/aiproxer/aiproxer/internal/core/auth"
	refopenairesponses "github.com/aiproxer/aiproxer/internal/refclient/openairesponses"
	stdhttpauth "github.com/aiproxer/aiproxer/internal/stdhttp/auth"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auth"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/transport/httpauth"

	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"
)

const aipAuthKeyConformance = "aip-stream-auth-key-conformance-only"

func openAIResponsesStreamEventTypes(tb testing.TB, proxyOrigin string, httpClient *http.Client) []string {
	tb.Helper()
	ctx := context.Background()
	cli := refopenairesponses.New(refopenairesponses.Config{
		BaseURL:    strings.TrimRight(proxyOrigin, "/") + "/v1",
		APIKey:     aipAuthKeyConformance,
		HTTPClient: httpClient,
	})
	stream := cli.CreateResponseStream(ctx, responses.ResponseNewParams{
		Model: shared.ResponsesModel(wireModelForFrontend("openai-responses")),
		Input: responses.ResponseNewParamsInputUnion{
			OfInputItemList: []responses.ResponseInputItemUnionParam{
				responses.ResponseInputItemParamOfMessage("ping", responses.EasyInputMessageRoleUser),
			},
		},
	})
	var types []string
	for stream.Next() {
		types = append(types, stream.Current().Type)
	}
	if err := stream.Err(); err != nil {
		tb.Fatalf("responses stream: %v", err)
	}
	return types
}

func openAIResponsesStreamAssistantText(
	tb testing.TB,
	proxyOrigin string,
	httpClient *http.Client,
	apiKey string,
) string {
	tb.Helper()
	ctx := context.Background()
	cli := refopenairesponses.New(refopenairesponses.Config{
		BaseURL:    strings.TrimRight(proxyOrigin, "/") + "/v1",
		APIKey:     apiKey,
		HTTPClient: httpClient,
	})
	stream := cli.CreateResponseStream(ctx, responses.ResponseNewParams{
		Model: shared.ResponsesModel(wireModelForFrontend("openai-responses")),
		Input: responses.ResponseNewParamsInputUnion{
			OfInputItemList: []responses.ResponseInputItemUnionParam{
				responses.ResponseInputItemParamOfMessage("ping", responses.EasyInputMessageRoleUser),
			},
		},
	})
	var b strings.Builder
	for stream.Next() {
		cur := stream.Current()
		if cur.Type != "response.completed" {
			continue
		}
		raw, err := json.Marshal(cur.Response)
		if err != nil {
			tb.Fatal(err)
		}
		b.WriteString(responsesOutputTextFromRaw(raw))
	}
	if err := stream.Err(); err != nil {
		tb.Fatalf("responses stream: %v", err)
	}
	return b.String()
}

func aipAuthProvider(tb testing.TB) *stdhttpauth.PolicyProvider {
	tb.Helper()
	ak, err := coreauth.NewLocalAPIKeyAuthenticator([]coreauth.LocalAPIKeyRecord{
		{KeyID: "aip", PrincipalID: "stream-test", Key: aipAuthKeyConformance},
	})
	if err != nil {
		tb.Fatal(err)
	}
	pa := coreauth.PolicyAuthenticator{
		Handler:  auth.HandlerLocalAPIKey,
		Required: auth.LevelAPIKey,
		APIKey:   ak,
	}
	disp := coreauth.NewEventDispatcher(&noopAuthSink{}, coreauth.EventFailureBestEffort)
	return stdhttpauth.NewPolicyProvider(&pa, disp, stdhttpauth.PolicySnapshot{
		AccessMode: auth.AccessMultiUser, HandlerKind: auth.HandlerLocalAPIKey, RequiredLevel: auth.LevelAPIKey,
	}, nil)
}

type noopAuthSink struct{}

func (noopAuthSink) OnAuthDecision(context.Context, auth.AuthDecisionEvent) error { return nil }
func (noopAuthSink) OnSessionStart(context.Context, auth.SessionStartEvent) error { return nil }

func TestConformance_authenticatedStreaming_openAIResponses_eventTypesMatchUnauthenticatedBaseline(t *testing.T) {
	t.Parallel()
	beSrv := NewSuccessRefBackend(t, "openai-responses", nil)
	exec := NewTestExecutor(t, "openai-responses", beSrv.URL, beSrv.Client())
	route := RouteSelector("openai-responses", DefaultModel("openai-responses"))

	muxBare := http.NewServeMux()
	if err := MountFrontend(muxBare, "openai-responses", exec, route); err != nil {
		t.Fatal(err)
	}
	srvBare := httptest.NewServer(muxBare)
	t.Cleanup(srvBare.Close)
	clientBare := testkit.IntegrationHTTPClient(srvBare.Client())
	baseTypes := openAIResponsesStreamEventTypes(t, srvBare.URL, clientBare)
	baseText := openAIResponsesStreamAssistantText(t, srvBare.URL, clientBare, "sk-test")
	if !strings.Contains(baseText, parityText) {
		t.Fatalf("baseline text: %q", baseText)
	}

	muxAuth := http.NewServeMux()
	if err := MountFrontend(muxAuth, "openai-responses", exec, route); err != nil {
		t.Fatal(err)
	}
	prov := aipAuthProvider(t)
	h := stdhttpauth.Middleware(nil, []httpauth.Provider{prov}, muxAuth)
	srvAuth := httptest.NewServer(h)
	t.Cleanup(srvAuth.Close)
	clientAuth := testkit.IntegrationHTTPClient(srvAuth.Client())
	authTypes := openAIResponsesStreamEventTypes(t, srvAuth.URL, clientAuth)
	authText := openAIResponsesStreamAssistantText(t, srvAuth.URL, clientAuth, aipAuthKeyConformance)
	if !strings.Contains(authText, parityText) {
		t.Fatalf("auth path text: %q", authText)
	}
	if len(authTypes) != len(baseTypes) {
		t.Fatalf("event type count: auth=%d base=%d auth=%v base=%v", len(authTypes), len(baseTypes), authTypes, baseTypes)
	}
	for i := range authTypes {
		if authTypes[i] != baseTypes[i] {
			t.Fatalf("event type mismatch at %d: auth=%v base=%v", i, authTypes, baseTypes)
		}
	}
	rawAuth, _ := json.Marshal(authTypes)
	if strings.Contains(string(rawAuth), aipAuthKeyConformance) {
		t.Fatal("AIProxer auth key leaked into collected event type metadata")
	}
}

func TestConformance_authenticatedText_bundledFrontends_containsParityText(t *testing.T) {
	t.Parallel()
	// OpenAI-shaped frontends send the configured API key as a Bearer token, matching local_api_key
	// middleware. Anthropic/Gemini SDKs use vendor-specific headers, so they are covered elsewhere
	// (e.g. stdhttp/auth frontend_bundle_auth_test).
	cases := []struct {
		name string
		fe   string
		be   string
	}{
		{name: "openai_responses", fe: "openai-responses", be: "openai-responses"},
		{name: "openai_legacy", fe: "openai-legacy", be: "openai-legacy"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			beSrv := NewSuccessRefBackend(t, tc.be, nil)
			exec := NewTestExecutor(t, tc.be, beSrv.URL, beSrv.Client())
			route := RouteSelector(tc.be, DefaultModel(tc.be))

			muxBare := http.NewServeMux()
			if err := MountFrontend(muxBare, tc.fe, exec, route); err != nil {
				t.Fatal(err)
			}
			srvBare := httptest.NewServer(muxBare)
			t.Cleanup(srvBare.Close)
			clientBare := testkit.IntegrationHTTPClient(srvBare.Client())
			base := nonStreamAssistantText(t, tc.fe, srvBare.URL, clientBare)
			if !strings.Contains(base, parityText) {
				t.Fatalf("baseline: %q", base)
			}

			muxAuth := http.NewServeMux()
			if err := MountFrontend(muxAuth, tc.fe, exec, route); err != nil {
				t.Fatal(err)
			}
			prov := aipAuthProvider(t)
			h := stdhttpauth.Middleware(nil, []httpauth.Provider{prov}, muxAuth)
			srvAuth := httptest.NewServer(h)
			t.Cleanup(srvAuth.Close)
			clientAuth := testkit.IntegrationHTTPClient(srvAuth.Client())
			authText := nonStreamAssistantTextWithAPIKey(t, tc.fe, srvAuth.URL, clientAuth, aipAuthKeyConformance)
			if !strings.Contains(authText, parityText) {
				t.Fatalf("auth path: %q", authText)
			}
		})
	}
}
