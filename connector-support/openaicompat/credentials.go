package openaicompat

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/credpool"
	"gopkg.in/yaml.v3"
)

// DecodeAPIKeySlots validates host-issued ordered api_key Configure secrets
// through the SDK slot decoder and returns owned secret strings in ascending
// slot order (api_key, api_key_2, ...). Sparse host numbering is preserved as
// ascending order; duplicate values fail closed via the SDK decoder, matching
// the host resolver that never emits them. Every rejection is secret-free:
// errors name only slot names, never secret bytes.
func DecodeAPIKeySlots(bundle backendplugin.SecretBundle) ([]string, error) {
	secrets, _, err := decodeNamedSlotSecrets(bundle, backendplugin.CredentialSlotAPIKey)
	return secrets, err
}

// DecodeAPITokenSlots validates host-issued ordered api_token Configure
// secrets (bearer/PAT provider modes) through the SDK slot decoder and
// returns owned secret strings in ascending slot order (api_token,
// api_token_2, ...). Rejections are fail-closed and secret-free, mirroring
// DecodeAPIKeySlots; api_key-named slots are rejected for this base.
func DecodeAPITokenSlots(bundle backendplugin.SecretBundle) ([]string, error) {
	secrets, _, err := decodeNamedSlotSecrets(bundle, backendplugin.CredentialSlotAPIToken)
	return secrets, err
}

// SlotSecret binds one nonsecret canonical slot name (api_key, api_key_2,
// ... or api_token, api_token_2, ...) to its owned secret string in ascending
// slot order. Names are safe for diagnostics; Secret is secret material and
// must never be logged.
type SlotSecret struct {
	Name   string
	Secret string
}

// DecodeAPIKeySlotSecrets validates host-issued ordered api_key Configure
// secrets like DecodeAPIKeySlots and additionally returns each secret bound
// to its nonsecret canonical slot name. Connectors that maintain per-slot
// state (per-key transports, per-slot token providers) key that state off
// Name, which stays bound to coordinator selection.
func DecodeAPIKeySlotSecrets(bundle backendplugin.SecretBundle) ([]SlotSecret, error) {
	_, named, err := decodeNamedSlotSecrets(bundle, backendplugin.CredentialSlotAPIKey)
	return named, err
}

// DecodeAPITokenSlotSecrets validates host-issued ordered api_token
// Configure secrets like DecodeAPITokenSlots and additionally returns each
// secret bound to its nonsecret canonical slot name. Static non-rotatable
// consumers (single subscription/token identity, no quota pooling) use the
// names to reject surplus slots fail-closed instead of silently ignoring
// them.
func DecodeAPITokenSlotSecrets(bundle backendplugin.SecretBundle) ([]SlotSecret, error) {
	_, named, err := decodeNamedSlotSecrets(bundle, backendplugin.CredentialSlotAPIToken)
	return named, err
}

func decodeNamedSlotSecrets(bundle backendplugin.SecretBundle, base backendplugin.CredentialSlot) ([]string, []SlotSecret, error) {
	slots, err := backendplugin.DecodeOrderedSecretSlots(bundle, base)
	if err != nil {
		return nil, nil, err
	}
	secrets := make([]string, 0, len(slots))
	named := make([]SlotSecret, 0, len(slots))
	for _, slot := range slots {
		secret := strings.TrimSpace(string(slot.Value))
		if secret == "" {
			return nil, nil, fmt.Errorf("%w: empty value for slot %q", backendplugin.ErrInvalidCredentialSlots, slot.Name)
		}
		secrets = append(secrets, secret)
		named = append(named, SlotSecret{Name: slot.Name, Secret: secret})
	}
	if len(secrets) == 0 {
		return nil, nil, fmt.Errorf("%w: missing %q slot", backendplugin.ErrInvalidCredentialSlots, string(base))
	}
	return secrets, named, nil
}

// NewAPIKeyPool builds an ordered credential pool from decoded slot secrets.
// The first key stays preferred until quota/auth evidence advances selection.
func NewAPIKeyPool(keys []string) (*credpool.Pool, error) {
	creds := make([]credpool.Credential, 0, len(keys))
	for _, key := range keys {
		if strings.TrimSpace(key) == "" {
			continue
		}
		creds = append(creds, credpool.Credential{Secret: key})
	}
	return credpool.New(creds)
}

// ClassifyKeyError maps OpenAI-compatible HTTP failures to coordinator
// classes: 401 is auth-invalid and advances pre-output; 429 carries the raw
// Retry-After hint for threshold-gated quota rotation; 408/5xx are transient
// upstream failures with no credential evidence; every other outcome
// (including 402/403 provider errors) is neutral and returns as-is without
// consuming another credential. Wrapped *HTTPError values classify via
// errors.As; the hint is the verbatim header value and carries no secrets.
func ClassifyKeyError(err error) (credpool.FailureClass, string) {
	if err == nil {
		return credpool.FailureNeutral, ""
	}
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) || httpErr == nil {
		return credpool.FailureNeutral, ""
	}
	switch httpErr.Status {
	case http.StatusUnauthorized:
		return credpool.FailureAuthInvalid, ""
	case http.StatusTooManyRequests:
		return credpool.FailureRateLimited, strings.TrimSpace(httpErr.RetryAfter)
	case http.StatusRequestTimeout:
		return credpool.FailureRetryable, ""
	default:
		if httpErr.Status >= 500 {
			return credpool.FailureRetryable, ""
		}
		return credpool.FailureNeutral, ""
	}
}

// ThresholdFromPolicy extracts the validated nonsecret quota-switch threshold
// from the host-projected Configure RuntimePolicy. Zero means the coordinator
// default (proxy-wide 1h); a positive value is the explicit Retry-After
// duration strictly above which a 429 advances. Negative values normalize to
// zero. It never observes secret material.
func ThresholdFromPolicy(policy backendplugin.RuntimePolicy) time.Duration {
	if policy.QuotaSwitchRetryAfterMS <= 0 {
		return 0
	}
	return time.Duration(policy.QuotaSwitchRetryAfterMS) * time.Millisecond
}

// RunWithKeys executes fn against ordered credentials with quota-aware
// selection: the first usable key stays preferred, auth-invalid and
// long-quota-throttled keys advance pre-output, and short/missing/invalid
// throttles return ErrTransientThrottle without consuming the next key. The
// quota-switch threshold is the proxy-wide default (strictly greater than 1h
// rotates); attempts never exceed the pool size. Pool-exhaustion sentinels
// propagate for the caller to map onto pre-output routing errors.
//
// Prefer RunWithKeysThreshold with ThresholdFromPolicy(req.RuntimePolicy) so
// executable connectors honor the configured upstream_credentials threshold;
// this wrapper retains the default for callers without host policy.
func RunWithKeys[T any](ctx context.Context, pool *credpool.Pool, fn func(context.Context, credpool.Credential) (T, error)) (T, error) {
	return RunWithKeysThreshold(ctx, pool, 0, fn)
}

// RunWithKeysThreshold executes fn like RunWithKeys with an explicit
// quota-switch threshold: a non-positive threshold selects the coordinator
// default, otherwise a Retry-After strictly greater than threshold advances.
func RunWithKeysThreshold[T any](ctx context.Context, pool *credpool.Pool, threshold time.Duration, fn func(context.Context, credpool.Credential) (T, error)) (T, error) {
	var zero T
	if pool == nil {
		return zero, credpool.ErrNoUsableCredential
	}
	coord, err := credpool.NewCoordinator(pool, threshold, nil)
	if err != nil {
		return zero, err
	}
	return credpool.Do(ctx, coord, ClassifyKeyError, fn)
}

// RecoverableRunWithKeys executes fn against ordered credentials with quota-aware
// selection (see RunWithKeys) and maps pool-exhaustion outcomes to
// aipapi recoverable pre-output errors for core-owned routing: transient
// throttles, drained pools and retryable upstream failures stay recoverable,
// while provider-specific failures (auth on last key aside, 4xx/5xx taxonomy)
// return as-is. All errors are secret-free.
//
// Prefer RecoverableRunWithKeysThreshold with host policy; this wrapper keeps
// the default threshold.
func RecoverableRunWithKeys[T any](ctx context.Context, pool *credpool.Pool, fn func(context.Context, credpool.Credential) (T, error)) (T, error) {
	return RecoverableRunWithKeysThreshold(ctx, pool, 0, fn)
}

// RecoverableRunWithKeysThreshold executes fn like RecoverableRunWithKeys with
// an explicit quota-switch threshold from ThresholdFromPolicy.
func RecoverableRunWithKeysThreshold[T any](ctx context.Context, pool *credpool.Pool, threshold time.Duration, fn func(context.Context, credpool.Credential) (T, error)) (T, error) {
	result, err := RunWithKeysThreshold(ctx, pool, threshold, fn)
	if err == nil {
		return result, nil
	}
	if errors.Is(err, credpool.ErrTransientThrottle) ||
		errors.Is(err, credpool.ErrQuotaExhausted) ||
		errors.Is(err, credpool.ErrUpstreamRetryable) ||
		errors.Is(err, credpool.ErrNoUsableCredential) {
		var zero T
		return zero, aipapi.RecoverablePreOutputError(err)
	}
	return result, err
}

// SecretBackedService wraps svc, injecting fixed Configure secrets when the
// request carries none. It exists for hermetic module-local tests (for
// example the shared conformance harness, which configures without secrets):
// production hosts always deliver per-export secrets, so the injection only
// fires for secret-less test requests. Like all Configure secrets, injected
// values must never be logged.
type SecretBackedService struct {
	backendplugin.Service
	Secrets backendplugin.SecretBundle
}

// Configure implements backendplugin.Service.
func (s SecretBackedService) Configure(ctx context.Context, req backendplugin.ConfigureRequest) (backendplugin.ConfiguredInstance, error) {
	if len(req.Secrets.Values) == 0 {
		req.Secrets = s.Secrets
	}
	return s.Service.Configure(ctx, req)
}

// forbiddenSecretFields are top-level YAML configuration keys that must never
// carry provider credential material or credential-file pointers in an
// executable-connector config. Comparison is case-insensitive with -/_
// unified. Values are never echoed: rejections name only the field path and
// the approved environment root.
var forbiddenSecretFields = map[string]struct{}{
	"api_key": {}, "api_keys": {}, "apikey": {}, "apikeys": {},
	"access_token": {}, "api_token": {}, "api_tokens": {},
	"auth_token": {}, "bearer_token": {}, "bearer": {},
	"token": {}, "tokens": {}, "iam_token": {},
	"credential": {}, "credentials": {},
	"secret": {}, "secrets": {}, "client_secret": {},
	"private_key": {}, "privatekey": {}, "pat": {}, "password": {},
	"auth_json": {}, "auth_json_path": {}, "authjson": {},
	"dotenv": {}, "env_file": {}, "credential_file": {}, "credentials_file": {},
	"key_file": {}, "secrets_file": {}, "token_file": {},
	"authorization": {},
}

// RejectSecretFields fails closed when raw connector YAML carries a
// secret-bearing top-level field, including legacy singular/list aliases and
// connector-side credential-file pointers. The error names the field path
// and the approved host environment root, never the rejected value. Empty
// input passes; YAML syntax errors fail closed without echoing input.
func RejectSecretFields(provider, envRoot string, raw []byte) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	var fields map[string]any
	if err := yaml.Unmarshal(raw, &fields); err != nil {
		return fmt.Errorf("%s: config: invalid YAML", provider)
	}
	for name := range fields {
		normalized := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(name), "-", "_"))
		if _, forbidden := forbiddenSecretFields[normalized]; forbidden {
			return fmt.Errorf("%s: config field %q is forbidden; provide credentials via host environment %s[_N] delivered as Configure secrets", provider, name, envRoot)
		}
	}
	return nil
}
