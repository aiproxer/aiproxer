package openresponsescompat

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/infra/httpclient"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/credpool"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/modelinventory"
)

// BackendSpec carries the validated construction inputs for one instance.
type BackendSpec struct {
	ID               string
	APIKeyEnvVarRoot string
	APIKey           string
	APIKeys          []string
	// Credentials is the structured credential list. When set, it takes
	// precedence over APIKey/APIKeys and preserves non-secret credential IDs
	// for diagnostics.
	Credentials []credpool.Credential
	// QuotaSwitchThreshold is the Retry-After duration strictly above which
	// a 429 quota-cools a key and advances pre-output. Zero selects the
	// proxy-wide default; it is bound from
	// upstream_credentials.quota_switch_retry_after at candidate build.
	QuotaSwitchThreshold time.Duration
	BaseURL              string
	HTTPClient           *http.Client
	RequestLimits        RequestLimits
	ResponseLimits       ResponseLimits
	Caps                 aipapi.BackendCaps
	DialectSupport       aipapi.DialectSupport
	Inventory            modelinventory.Provider
	// Codec is the explicit codec customization/provenance seam for future
	// provider connectors. The zero value resolves to the generic default via
	// [NewBackend]; provider wrappers may pass explicit options to preserve
	// instance/factory/profile provenance without provider-policy leakage.
	Codec CodecOptions

	// pool is the ordered per-instance credential selector resolved at
	// construction; nil is the compatible no-auth mode.
	pool *credpool.Pool
}

// NewBackend constructs the generic OpenResponses backend for one instance.
// The Open seam maps item-authoritative create calls to a context-aware
// non-streaming JSON request and parses the complete response resource into a
// canonical lifecycle stream.
func NewBackend(spec BackendSpec) execbackend.Backend {
	id := strings.TrimSpace(spec.ID)
	if id == "" {
		return configErrorBackend(id, fmt.Errorf("%s: backend id is required", ID))
	}
	if spec.Caps == nil {
		spec.Caps = aipapi.NewBackendCaps()
	}
	if spec.RequestLimits == (RequestLimits{}) {
		spec.RequestLimits = defaultRequestLimits()
	}
	if spec.ResponseLimits == (ResponseLimits{}) {
		spec.ResponseLimits = defaultResponseLimits()
	}
	if spec.HTTPClient == nil {
		spec.HTTPClient = httpclient.Standard()
	}
	if spec.Codec.profile == "" {
		spec.Codec = DefaultCodecOptions()
	}
	pool, err := buildCredentialPool(spec)
	if err != nil {
		return configErrorBackend(id, fmt.Errorf("%s: credentials: %w", id, err))
	}
	spec.pool = pool
	spec.DialectSupport = aipapi.NormalizeDialectSupport(spec.DialectSupport)
	return execbackend.Backend{
		Caps:                    spec.Caps,
		TransportCaps:           OpenResponsesTransportCaps(),
		BackendPrefixes:         []string{id},
		ModelInventory:          spec.Inventory,
		DialectSupport:          spec.DialectSupport,
		EnforcesMaxOutputTokens: true,
		ResolveCaps: func(_ context.Context, _ aipapi.Call, _ routing.AttemptCandidate) aipapi.BackendCaps {
			return spec.Caps
		},
		Open: func(ctx context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			if ctx == nil {
				return nil, fmt.Errorf("%s: %w", id, aipapi.ErrNilContext)
			}
			switch call.Invocation.Operation {
			case aipapi.OperationContextCompaction:
				return openCompact(ctx, id, spec, call, cand)
			default:
				return openCreate(ctx, id, spec, call, cand)
			}
		},
	}
}

// acceptsCreateOperation reports whether the generic OpenResponses backend
// serves a create call carrying op. The backend owns the openresponses.create
// operation identity and additionally accepts the legacy message-authority
// create operations produced by the bundled OpenAI frontends so those frontend
// families can route to the OpenResponses backend column. The calls are still
// normalized through the single explicit legacy→ordered-items projector before
// any network work; no pairwise translator exists.
func acceptsCreateOperation(op aipapi.Operation) bool {
	switch op {
	case "", aipapi.OperationOpenResponsesCreate,
		aipapi.OperationOpenAIChatCompletions,
		aipapi.OperationOpenAIResponses,
		aipapi.OperationAnthropicMessages,
		aipapi.OperationGeminiGenerateContent:
		return true
	default:
		return false
	}
}

// openCreate is the create mapping for both canonical authority forms. Legacy
// message-authority calls are projected to ordered items through the explicit
// legacy→ordered-items projector before the request builder (Task 5.3). Every
// rejection before the HTTP round trip returns a classified pre-output error
// with zero round trips.
func openCreate(ctx context.Context, id string, spec BackendSpec, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
	if !acceptsCreateOperation(call.Invocation.Operation) {
		return nil, fmt.Errorf("%s: %w: operation %q", id, ErrOperationUnsupported, call.Invocation.Operation)
	}

	if err := call.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", id, err)
	}
	projected, err := normalizeLegacyAuthority(id, spec, call)
	if err != nil {
		return nil, err
	}
	if err := projected.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", id, err)
	}
	if err := checkRequirements(id, projected, spec.Caps, spec.DialectSupport); err != nil {
		return nil, err
	}

	endpointURL, err := resolveCreateEndpoint(spec.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", id, err)
	}

	transportMode := call.Invocation.TransportMode
	if transportMode == "" {
		// Legacy message-authority frontends (Anthropic, Gemini) produce empty
		// operations, so transport negotiation leaves the mode unselected. Derive
		// it from the client delivery mode exactly like the bundled backends do,
		// so streaming delivery is honored over SSE.
		transportMode = aipapi.PreferredTransportMode(call.Invocation.DeliveryMode)
	}
	switch transportMode {
	case aipapi.TransportModeStreaming:
		return openCreateStreaming(ctx, id, spec, projected, cand)
	case aipapi.TransportModeNonStreaming, "":
		body, err := buildCreateRequest(id, spec, projected, cand)
		if err != nil {
			return nil, err
		}
		// The request body is fully materialized bytes, so every pre-output
		// attempt replays it exactly; key selection follows the shared
		// ordered quota-aware contract instead of the pinned first key.
		return executeWithPool(ctx, id, spec.pool, spec.QuotaSwitchThreshold, func(ctx context.Context, cred credpool.Credential) (aipapi.ManagedEventStream, error) {
			respBody, err := doNonStreaming(ctx, spec.HTTPClient, endpointURL, body, cred.Secret, spec.ResponseLimits.MaxEventBytes)
			if err != nil {
				return nil, classifyCreateOpenError(fmt.Errorf("%s: %w", id, err))
			}

			events, _, err := parseResource(id, respBody, spec.ResponseLimits)
			if err != nil {
				return nil, classifyCreateOpenError(err)
			}
			return newProviderEventStream(events, "openresponses.compat.v2"), nil
		})
	default:
		return nil, fmt.Errorf("%s: %w: transport mode %q is not supported", id, ErrUnrepresentable, call.Invocation.TransportMode)
	}
}

// classifyCreateOpenError classifies a non-streaming create attempt failure for
// failover. Context cancellation/deadline is never retried; terminal upstream
// HTTP failures (4xx validation/auth) stay terminal; every other pre-output
// protocol, transport, content-type, body, or parse failure is retryable so core
// can fail over to another candidate before commitment.
func classifyCreateOpenError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if aipapi.IsRecoverablePreOutput(err) {
		return err
	}
	if hf, ok := errors.AsType[*httpFailureError](err); ok {
		if hf.Kind == httpFailureAuthInvalid || hf.Kind == httpFailureTerminal {
			return err
		}
	}
	return aipapi.RecoverablePreOutputError(err)
}

// OpenResponsesTransportCaps declares the generic mode's operation+transport
// surface: create over JSON and SSE, and compaction over non-streaming
// transport (the pinned profile's initial compaction transport).
func OpenResponsesTransportCaps() aipapi.BackendTransportCaps {
	return aipapi.NewBackendTransportCaps(
		aipapi.OperationTransportSupport{
			Operation: aipapi.OperationOpenResponsesCreate,
			Modes:     []aipapi.TransportMode{aipapi.TransportModeStreaming, aipapi.TransportModeNonStreaming},
		},
		aipapi.OperationTransportSupport{
			Operation: aipapi.OperationContextCompaction,
			Modes:     []aipapi.TransportMode{aipapi.TransportModeNonStreaming},
		},
	)
}

func configErrorBackend(id string, err error) execbackend.Backend {
	return execbackend.Backend{
		Caps:           aipapi.NewBackendCaps(aipapi.CapabilityOrderedItems),
		TransportCaps:  OpenResponsesTransportCaps(),
		ModelInventory: modelinventory.ErrorProvider{Err: err},
		ResolveCaps: func(_ context.Context, _ aipapi.Call, _ routing.AttemptCandidate) aipapi.BackendCaps {
			return aipapi.NewBackendCaps(aipapi.CapabilityOrderedItems)
		},
		Open: func(_ context.Context, _ aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			return nil, err
		},
	}
}
