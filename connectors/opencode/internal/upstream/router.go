package upstream

import (
	"context"
	"fmt"
	"net/http"

	"github.com/aiproxer/aiproxer/connector-support/openaicompat"
	"github.com/aiproxer/aiproxer/connectors/opencode/internal/catalog"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

type Router struct {
	kind    catalog.BackendKind
	baseURL string
	apiKey  string
	hc      *http.Client
}

func NewRouter(kind catalog.BackendKind, baseURL, apiKey string, hc *http.Client) *Router {
	if hc == nil {
		hc = http.DefaultClient
	}
	return &Router{
		kind:    kind,
		baseURL: baseURL,
		apiKey:  apiKey,
		hc:      hc,
	}
}

func (r *Router) Open(ctx context.Context, call aipapi.Call, resolved catalog.ResolvedModel) (aipapi.ManagedEventStream, error) {
	return r.OpenWithKey(ctx, call, resolved, r.apiKey)
}

// OpenWithKey opens one attempt with an explicit caller-selected credential.
// Quota-aware selection lives with the caller (per-instance credential
// coordinator); the router applies the given key to every flavor without
// caching it, so cooled or retired keys are never reused by a stale client.
func (r *Router) OpenWithKey(ctx context.Context, call aipapi.Call, resolved catalog.ResolvedModel, apiKey string) (aipapi.ManagedEventStream, error) {
	if ctx == nil {
		return nil, fmt.Errorf("opencode: %w", aipapi.ErrNilContext)
	}
	baseURL := catalog.EndpointBaseURL(resolved.Entry, r.baseURL, resolved.Flavor)
	switch resolved.Flavor {
	case catalog.FlavorAnthropicMessages:
		return openAnthropic(ctx, r.hc, baseURL, apiKey, call, resolved.WireModel)
	case catalog.FlavorGoogleGemini:
		return openGemini(ctx, r.hc, baseURL, apiKey, call, resolved.WireModel)
	case catalog.FlavorOpenAIResponses:
		return r.openAIClientFor(baseURL, apiKey).Open(ctx, call, resolved.WireModel, openaicompat.FlavorResponses)
	default:
		return r.openAIClientFor(baseURL, apiKey).Open(ctx, call, resolved.WireModel, openaicompat.FlavorChat)
	}
}

func (r *Router) openAIClientFor(baseURL, apiKey string) *openaicompat.Client {
	return &openaicompat.Client{
		BaseURL:    baseURL,
		APIKey:     apiKey,
		HTTPClient: r.hc,
		Transport:  openaicompat.TransportChatAndResponses,
		Hooks: openaicompat.RequestHooks{
			PrepareHeaders: func(h http.Header, call aipapi.Call, model string, flavor openaicompat.Flavor) {
				if sid := resolveSessionID(call); sid != "" {
					h.Set(HeaderOpenCodeSession, sid)
				}
			},
			MutateBody: func(body map[string]any, call aipapi.Call, model string, flavor openaicompat.Flavor) error {
				sanitizeOpenAIPayload(body, model)
				return nil
			},
		},
	}
}
