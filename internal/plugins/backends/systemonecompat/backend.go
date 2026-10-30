package systemonecompat

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/infra/httpclient"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/compatmode"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/credpool"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/transporterr"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"gopkg.in/yaml.v3"
)

// ID is the configurable System One protocol-family backend kind.
const ID = "custom-systemone-compatible"

// BuildCompatible constructs a decision-only backend using strict compatible
// configuration and static model inventory. Credentials are resolved per call.
func BuildCompatible(instanceID string, n yaml.Node, client *http.Client) (execbackend.Backend, error) {
	return BuildCompatibleWithHeaders(instanceID, n, client, nil)
}

// BuildCompatibleWithHeaders binds validated, operator-owned profile headers.
// The header map is cloned once; no caller header is consulted during execution.
func BuildCompatibleWithHeaders(instanceID string, n yaml.Node, client *http.Client, headers http.Header) (execbackend.Backend, error) {
	return BuildCompatibleWithSnapshot(instanceID, n, client, headers, pluginreg.HostCredentialSnapshot{})
}

// BuildCompatibleWithSnapshot constructs the backend with admitted
// candidate-snapshot credentials (requirement 1.4): the configured
// api_key_env_var_root resolves through the authorized snapshot when the
// composition root captured one, so serving keeps admitted keys across
// process-environment rotation. Without a resolved snapshot the
// candidate-build process-environment lookup applies.
func BuildCompatibleWithSnapshot(instanceID string, n yaml.Node, client *http.Client, headers http.Header, snap pluginreg.HostCredentialSnapshot) (execbackend.Backend, error) {
	headers = headers.Clone()
	cfg, err := config.DecodeCompatibleModeConfig(instanceID, ID, n)
	if err != nil {
		return execbackend.Backend{}, err
	}
	if err := pluginreg.ValidatePrefixSyntax(cfg.BackendPrefix); err != nil {
		return execbackend.Backend{}, err
	}
	base, err := url.Parse(cfg.BaseURL)
	if err != nil || base.Host == "" || (base.Scheme != "https" && base.Scheme != "http") || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return execbackend.Backend{}, errors.New("system one requires an HTTP(S) base URL without credentials, query or fragment")
	}
	endpoint := strings.TrimRight(base.String(), "/") + "/systemone"
	if client == nil {
		client = httpclient.Standard()
	}
	// Candidate-build snapshot: resolve once here so serving uses admitted
	// credentials even if the process environment rotates afterwards. Each
	// instance owns its ordered pool, so different project/account roots
	// stay isolated and one root's 429 never cools another instance.
	keys := compatmode.ResolveCustomKeys(cfg.APIKeyEnvVarRoot, snap)
	var pool *credpool.Pool
	if len(keys) > 0 {
		credentials := make([]credpool.Credential, 0, len(keys))
		for _, secret := range keys {
			credentials = append(credentials, credpool.Credential{Secret: secret})
		}
		if pool, err = credpool.New(credentials); err != nil {
			return execbackend.Backend{}, err
		}
	}
	threshold := snap.QuotaSwitchRetryAfter
	caps := aipapi.NewBackendCaps(aipapi.CapabilityDecisions)
	be := execbackend.Backend{
		Caps: caps, BackendPrefixes: []string{cfg.BackendPrefix},
		TransportCaps: aipapi.NewBackendTransportCaps(aipapi.OperationTransportSupport{
			Operation: aipapi.OperationDecisionEvaluate, Modes: []aipapi.TransportMode{aipapi.TransportModeNonStreaming},
		}),
		ResolveCaps: func(context.Context, aipapi.Call, routing.AttemptCandidate) aipapi.BackendCaps { return caps },
		Open: func(ctx context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			if ctx == nil {
				return nil, aipapi.ErrNilContext
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if call.Decision == nil || call.Invocation.Operation != aipapi.OperationDecisionEvaluate || call.Invocation.TransportMode == aipapi.TransportModeStreaming {
				return nil, &aipapi.RejectError{Reason: "system one supports only non-streaming decision.evaluate"}
			}
			if err := call.Validate(); err != nil {
				return nil, err
			}
			return openWithPool(ctx, client, endpoint, pool, threshold, cand.Primary.Model, *call.Decision, headers)
		},
	}
	be, err = compatmode.ApplyStaticModelInventory(be, cfg.Models)
	if err != nil {
		return execbackend.Backend{}, err
	}
	if be.ModelInventory == nil {
		return execbackend.Backend{}, errors.New("system one requires static model inventory")
	}
	return compatmode.ApplyRuntimePolicy(be, cfg)
}

// LifecycleSystemOneCompatible is the standard distribution factory seam.
func LifecycleSystemOneCompatible(instanceID string, n yaml.Node, client *http.Client, deps pluginreg.BackendFactoryDeps) (pluginreg.BackendBuildResult, error) {
	be, err := BuildCompatibleWithSnapshot(instanceID, n, client, nil, deps.Credentials)
	return pluginreg.BackendBuildResult{Backend: be}, err
}

// classifyWireError translates one decision-wire attempt into the shared
// quota-aware selection contract: 401 retires the key and a 429 carries its
// Retry-After hint, while validation rejections stay terminal and every other
// pre-output failure remains a failover candidate without touching pool
// state, preserving the existing outer classification.
func classifyWireError(err error) (credpool.FailureClass, string) {
	if hf, ok := errors.AsType[*wireHTTPError](err); ok {
		switch hf.status {
		case 401:
			return credpool.FailureAuthInvalid, ""
		case 429:
			return credpool.FailureRateLimited, hf.retryAfter
		case 400, 413, 422:
			return credpool.FailureNeutral, ""
		default:
			return credpool.FailureRetryable, ""
		}
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return credpool.FailureNeutral, ""
	}
	if aipapi.IsRecoverablePreOutput(err) || transporterr.IsRetryable(err) {
		return credpool.FailureRetryable, ""
	}
	return credpool.FailureRetryable, ""
}

// sanitizedPoolError renders a classified pool-stop error without secret or
// upstream-body material: the stable wire status when the stop wraps one,
// otherwise a fixed transport failure matching the existing boundary.
func sanitizedPoolError(runErr error) error {
	if hf, ok := errors.AsType[*wireHTTPError](runErr); ok {
		return hf
	}
	return errors.New("system one upstream transport failed")
}

// openWithPool evaluates one decision across the ordered credentials before
// any output. A nil pool is the compatible no-auth mode: a single attempt
// without Authorization. Usage evidence from every failed attempt accumulates
// into the terminal sideband so rotated billable attempts are never dropped
// silently: on terminal failure the failure stream drains all attempts, and
// on success the success stream drains the prior failed attempts alongside
// the successful events. Each failed attempt carries a distinct nonsecret
// source identity so the core billing ledger admits every billable attempt
// instead of deduplicating them as one.
func openWithPool(ctx context.Context, client *http.Client, endpoint string, pool *credpool.Pool, threshold time.Duration, model string, decision aipapi.DecisionRequest, safeHeaders http.Header) (aipapi.ManagedEventStream, error) {
	if pool == nil {
		events, err := evaluateWire(ctx, client, endpoint, "", model, decision, safeHeaders)
		if err != nil {
			classified := classifyError(ctx, err)
			if evidence, ok := errors.AsType[*failureUsageError](err); ok {
				usage := evidence.usage
				usage.Accounting.DedupeKey = failedAttemptDedupeKey("direct")
				return &failureUsageStream{FixedEventStream: aipapi.NewFixedEventStream(nil), failure: classified, evidence: []aipapi.Event{usage}}, nil
			}
			return nil, classified
		}
		return aipapi.NewFixedEventStream(events), nil
	}
	coord, err := credpool.NewCoordinator(pool, threshold, nil)
	if err != nil {
		return nil, err
	}
	var evidence []aipapi.Event
	events, runErr := credpool.Do(ctx, coord, classifyWireError, func(ctx context.Context, cred credpool.Credential) ([]aipapi.Event, error) {
		attempt, attemptErr := evaluateWire(ctx, client, endpoint, cred.Secret, model, decision, safeHeaders)
		if attemptErr != nil {
			if fu, ok := errors.AsType[*failureUsageError](attemptErr); ok {
				usage := fu.usage
				usage.Accounting.DedupeKey = failedAttemptDedupeKey(cred.ID)
				evidence = append(evidence, usage)
			}
			return nil, attemptErr
		}
		return attempt, nil
	})
	if runErr == nil {
		if len(evidence) > 0 {
			return &rotatedSuccessStream{FixedEventStream: aipapi.NewFixedEventStream(events), evidence: evidence}, nil
		}
		return aipapi.NewFixedEventStream(events), nil
	}
	if errors.Is(runErr, credpool.ErrTransientThrottle) ||
		errors.Is(runErr, credpool.ErrQuotaExhausted) ||
		errors.Is(runErr, credpool.ErrUpstreamRetryable) ||
		errors.Is(runErr, credpool.ErrNoUsableCredential) {
		classified := aipapi.RecoverablePreOutputError(sanitizedPoolError(runErr))
		if len(evidence) > 0 {
			return &failureUsageStream{FixedEventStream: aipapi.NewFixedEventStream(nil), failure: classified, evidence: evidence}, nil
		}
		return nil, classified
	}
	classified := classifyError(ctx, runErr)
	if len(evidence) > 0 {
		return &failureUsageStream{FixedEventStream: aipapi.NewFixedEventStream(nil), failure: classified, evidence: evidence}, nil
	}
	return nil, classified
}
