package openaicompat_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/connector-support/openaicompat"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/credpool"
)

func slotBundle(values map[string]string) backendplugin.SecretBundle {
	bundle := backendplugin.SecretBundle{Values: make(map[string][]byte, len(values))}
	for name, value := range values {
		bundle.Values[name] = []byte(value)
	}
	return bundle
}

func TestDecodeAPIKeySlots_OrderedSecrets(t *testing.T) {
	t.Parallel()
	keys, err := openaicompat.DecodeAPIKeySlots(slotBundle(map[string]string{
		"api_key_3": "third-key",
		"api_key":   "first-key",
		"api_key_2": "second-key",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 3 || keys[0] != "first-key" || keys[1] != "second-key" || keys[2] != "third-key" {
		t.Fatalf("keys=%q", keys)
	}
}

func TestDecodeAPIKeySlots_MissingFailsSecretFree(t *testing.T) {
	t.Parallel()
	_, err := openaicompat.DecodeAPIKeySlots(backendplugin.SecretBundle{})
	if err == nil || !strings.Contains(err.Error(), "api_key") {
		t.Fatalf("err=%v", err)
	}
}

func TestDecodeAPIKeySlots_WrongBaseRejected(t *testing.T) {
	t.Parallel()
	_, err := openaicompat.DecodeAPIKeySlots(slotBundle(map[string]string{
		"api_token": "some-token",
	}))
	if err == nil {
		t.Fatal("expected rejection of api_token slot for api_key decoder")
	}
}

func TestDecodeAPIKeySlots_DuplicateValuesFailClosed(t *testing.T) {
	t.Parallel()
	_, err := openaicompat.DecodeAPIKeySlots(slotBundle(map[string]string{
		"api_key":   "same-key",
		"api_key_2": "same-key",
	}))
	if err == nil {
		t.Fatal("expected duplicate slot values to fail closed")
	}
	if strings.Contains(err.Error(), "same-key") {
		t.Fatalf("rejection leaks secret: %v", err)
	}
}

func TestDecodeAPITokenSlots_OrderedSecrets(t *testing.T) {
	t.Parallel()
	tokens, err := openaicompat.DecodeAPITokenSlots(slotBundle(map[string]string{
		"api_token_2": "second-token",
		"api_token":   "first-token",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(tokens) != 2 || tokens[0] != "first-token" || tokens[1] != "second-token" {
		t.Fatalf("tokens=%q", tokens)
	}
}

func TestDecodeAPITokenSlots_WrongBaseRejected(t *testing.T) {
	t.Parallel()
	_, err := openaicompat.DecodeAPITokenSlots(slotBundle(map[string]string{
		"api_key": "some-key",
	}))
	if err == nil {
		t.Fatal("expected rejection of api_key slot for api_token decoder")
	}
	if _, err := openaicompat.DecodeAPITokenSlots(backendplugin.SecretBundle{}); err == nil ||
		!strings.Contains(err.Error(), "api_token") {
		t.Fatalf("err=%v", err)
	}
}

func TestDecodeAPIKeySlotSecrets_BindsSlotNames(t *testing.T) {
	t.Parallel()
	named, err := openaicompat.DecodeAPIKeySlotSecrets(slotBundle(map[string]string{
		"api_key_2": "second-key",
		"api_key":   "first-key",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(named) != 2 || named[0].Name != "api_key" || named[0].Secret != "first-key" ||
		named[1].Name != "api_key_2" || named[1].Secret != "second-key" {
		t.Fatalf("named=%+v", named)
	}
}

func TestDecodeAPITokenSlotSecrets_BindsSlotNames(t *testing.T) {
	t.Parallel()
	named, err := openaicompat.DecodeAPITokenSlotSecrets(slotBundle(map[string]string{
		"api_token_2": "second-token",
		"api_token":   "first-token",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(named) != 2 || named[0].Name != "api_token" || named[0].Secret != "first-token" ||
		named[1].Name != "api_token_2" || named[1].Secret != "second-token" {
		t.Fatalf("named=%+v", named)
	}
	if _, err := openaicompat.DecodeAPITokenSlotSecrets(slotBundle(map[string]string{
		"api_key": "wrong-base",
	})); err == nil {
		t.Fatal("expected api_key-named slots to be rejected for the api_token base")
	}
}

func TestClassifyKeyError_ProviderTaxonomy(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  error
		want credpool.FailureClass
		hint bool
	}{
		{"auth-invalid", &openaicompat.HTTPError{Status: 401}, credpool.FailureAuthInvalid, false},
		{"long-throttle", &openaicompat.HTTPError{Status: 429, RetryAfter: "7200"}, credpool.FailureRateLimited, true},
		{"short-throttle", &openaicompat.HTTPError{Status: 429, RetryAfter: "60"}, credpool.FailureRateLimited, true},
		{"bare-429", &openaicompat.HTTPError{Status: 429}, credpool.FailureRateLimited, false},
		{"forbidden-neutral", &openaicompat.HTTPError{Status: 403}, credpool.FailureNeutral, false},
		{"payment-neutral", &openaicompat.HTTPError{Status: 402}, credpool.FailureNeutral, false},
		{"notfound-neutral", &openaicompat.HTTPError{Status: 404}, credpool.FailureNeutral, false},
		{"server-retryable", &openaicompat.HTTPError{Status: 500}, credpool.FailureRetryable, false},
		{"timeout-retryable", &openaicompat.HTTPError{Status: 408}, credpool.FailureRetryable, false},
		{"nil-neutral", nil, credpool.FailureNeutral, false},
		{"other-neutral", errors.New("boom"), credpool.FailureNeutral, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			class, hint := openaicompat.ClassifyKeyError(tc.err)
			if class != tc.want {
				t.Fatalf("class=%v want %v", class, tc.want)
			}
			if tc.hint && strings.TrimSpace(hint) == "" {
				t.Fatal("expected non-empty Retry-After hint")
			}
		})
	}
}

func TestRunWithKeys_AuthInvalidAdvances(t *testing.T) {
	t.Parallel()
	pool, err := openaicompat.NewAPIKeyPool([]string{"bad-key", "good-key"})
	if err != nil {
		t.Fatal(err)
	}
	var seen []string
	got, err := openaicompat.RunWithKeys(context.Background(), pool, func(_ context.Context, cred credpool.Credential) (string, error) {
		seen = append(seen, cred.Secret)
		if cred.Secret == "bad-key" {
			return "", &openaicompat.HTTPError{Status: 401, Message: "invalid"}
		}
		return "ok", nil
	})
	if err != nil || got != "ok" {
		t.Fatalf("got=%q err=%v", got, err)
	}
	if len(seen) != 2 || seen[0] != "bad-key" || seen[1] != "good-key" {
		t.Fatalf("seen=%q", seen)
	}
}

func TestRunWithKeys_ShortThrottleKeepsPrimary(t *testing.T) {
	t.Parallel()
	pool, err := openaicompat.NewAPIKeyPool([]string{"primary-key", "secondary-key"})
	if err != nil {
		t.Fatal(err)
	}
	var seen []string
	_, err = openaicompat.RunWithKeys(context.Background(), pool, func(_ context.Context, cred credpool.Credential) (string, error) {
		seen = append(seen, cred.Secret)
		return "", &openaicompat.HTTPError{Status: 429, RetryAfter: "60", Message: "slow down"}
	})
	if err == nil || !errors.Is(err, credpool.ErrTransientThrottle) {
		t.Fatalf("err=%v", err)
	}
	if len(seen) != 1 || seen[0] != "primary-key" {
		t.Fatalf("secondary must not be consumed by a short throttle: seen=%q", seen)
	}
}

func TestRunWithKeys_LongThrottleAdvances(t *testing.T) {
	t.Parallel()
	pool, err := openaicompat.NewAPIKeyPool([]string{"cooling-key", "next-key"})
	if err != nil {
		t.Fatal(err)
	}
	var seen []string
	got, err := openaicompat.RunWithKeys(context.Background(), pool, func(_ context.Context, cred credpool.Credential) (string, error) {
		seen = append(seen, cred.Secret)
		if cred.Secret == "cooling-key" {
			return "", &openaicompat.HTTPError{Status: 429, RetryAfter: "7200", Message: "quota"}
		}
		return "ok", nil
	})
	if err != nil || got != "ok" {
		t.Fatalf("got=%q err=%v", got, err)
	}
	if len(seen) != 2 {
		t.Fatalf("seen=%q", seen)
	}
}

func TestRunWithKeys_ErrorsNeverContainSecrets(t *testing.T) {
	t.Parallel()
	secret := "super-secret-key-material"
	pool, err := openaicompat.NewAPIKeyPool([]string{secret})
	if err != nil {
		t.Fatal(err)
	}
	_, err = openaicompat.RunWithKeys(context.Background(), pool, func(_ context.Context, _ credpool.Credential) (string, error) {
		return "", &openaicompat.HTTPError{Status: 429, RetryAfter: "30"}
	})
	if err == nil {
		t.Fatal("expected throttle error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error leaks secret: %v", err)
	}
	snapErr := func() error {
		_, derr := openaicompat.DecodeAPIKeySlots(backendplugin.SecretBundle{})
		return derr
	}()
	if strings.Contains(snapErr.Error(), secret) {
		t.Fatalf("decode error leaks secret: %v", snapErr)
	}
}

func TestRejectSecretFields_ForbiddenYAMLKeys(t *testing.T) {
	t.Parallel()
	for _, field := range []string{"api_key", "api_keys", "API_KEY", "access_token", "auth_json_path", "credentials", "private_key", "bearer_token"} {
		raw := field + ": hunter2\nbase_url: https://example.test\n"
		err := openaicompat.RejectSecretFields("test-provider", "TEST_PROVIDER_API_KEY", []byte(raw))
		if err == nil {
			t.Fatalf("field %q must be rejected", field)
		}
		if strings.Contains(err.Error(), "hunter2") {
			t.Fatalf("rejection leaks value for field %q: %v", field, err)
		}
		if !strings.Contains(err.Error(), "TEST_PROVIDER_API_KEY") {
			t.Fatalf("rejection must name env root for field %q: %v", field, err)
		}
	}
}

func TestRejectSecretFields_CleanConfigPasses(t *testing.T) {
	t.Parallel()
	if err := openaicompat.RejectSecretFields("test-provider", "TEST_PROVIDER_API_KEY", []byte("base_url: https://example.test\n")); err != nil {
		t.Fatal(err)
	}
	if err := openaicompat.RejectSecretFields("test-provider", "TEST_PROVIDER_API_KEY", nil); err != nil {
		t.Fatal(err)
	}
}

func TestRecoverableRunWithKeys_MapsPoolSentinels(t *testing.T) {
	t.Parallel()
	pool, err := openaicompat.NewAPIKeyPool([]string{"only-key"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = openaicompat.RecoverableRunWithKeys(context.Background(), pool, func(_ context.Context, _ credpool.Credential) (string, error) {
		return "", &openaicompat.HTTPError{Status: 429, RetryAfter: "30"}
	})
	if err == nil || !strings.Contains(err.Error(), "transient throttle") {
		t.Fatalf("err=%v", err)
	}
	_, err = openaicompat.RecoverableRunWithKeys(context.Background(), pool, func(_ context.Context, _ credpool.Credential) (string, error) {
		return "", &openaicompat.HTTPError{Status: 403, Message: "denied"}
	})
	if err == nil || strings.Contains(err.Error(), "transient throttle") {
		t.Fatalf("provider denial must return as-is: err=%v", err)
	}
}

func TestThresholdFromPolicy_NonsecretProjection(t *testing.T) {
	t.Parallel()
	if got := openaicompat.ThresholdFromPolicy(backendplugin.RuntimePolicy{}); got != 0 {
		t.Fatalf("zero policy must select default, got %v", got)
	}
	if got := openaicompat.ThresholdFromPolicy(backendplugin.RuntimePolicy{QuotaSwitchRetryAfterMS: -1}); got != 0 {
		t.Fatalf("negative threshold must normalize to default, got %v", got)
	}
	policy := backendplugin.RuntimePolicy{QuotaSwitchRetryAfterMS: (30 * 60 * 1000)}
	if got := openaicompat.ThresholdFromPolicy(policy); got != 30*time.Minute {
		t.Fatalf("30m policy = %v", got)
	}
	// Round-trip through the wire form preserves the nonsecret threshold.
	wire := backendplugin.RuntimePolicyToProto(policy)
	back := backendplugin.RuntimePolicyFromProto(wire)
	if back.QuotaSwitchRetryAfterMS != policy.QuotaSwitchRetryAfterMS {
		t.Fatalf("wire round-trip = %+v, want %+v", back, policy)
	}
}

// TestRunWithKeysThreshold_NondefaultThresholdRotates proves host-to-connector
// policy propagation end to end: with a configured 30m threshold, a 2700s
// (45m) Retry-After advances to the second key, while the proxy-wide default
// (1h) treats the same hint as a transient same-key throttle.
func TestRunWithKeysThreshold_NondefaultThresholdRotates(t *testing.T) {
	t.Parallel()
	newPool := func(t *testing.T) *credpool.Pool {
		t.Helper()
		pool, err := openaicompat.NewAPIKeyPool([]string{"first-key", "second-key"})
		if err != nil {
			t.Fatal(err)
		}
		return pool
	}
	attempt := func(_ context.Context, cred credpool.Credential) (string, error) {
		if cred.Secret == "first-key" {
			return "", &openaicompat.HTTPError{Status: 429, RetryAfter: "2700", Message: "quota"}
		}
		return "ok", nil
	}
	// Nondefault 30m threshold rotates.
	threshold := openaicompat.ThresholdFromPolicy(backendplugin.RuntimePolicy{QuotaSwitchRetryAfterMS: 30 * 60 * 1000})
	var seen []string
	got, err := openaicompat.RunWithKeysThreshold(context.Background(), newPool(t), threshold, func(ctx context.Context, cred credpool.Credential) (string, error) {
		seen = append(seen, cred.Secret)
		return attempt(ctx, cred)
	})
	if err != nil || got != "ok" {
		t.Fatalf("30m threshold must rotate on 2700s hint: got=%q err=%v", got, err)
	}
	if len(seen) != 2 {
		t.Fatalf("30m threshold must consume both keys, seen=%q", seen)
	}
	// Default threshold pins primary on the same hint.
	seen = nil
	_, err = openaicompat.RunWithKeysThreshold(context.Background(), newPool(t), 0, func(ctx context.Context, cred credpool.Credential) (string, error) {
		seen = append(seen, cred.Secret)
		return attempt(ctx, cred)
	})
	if err == nil || !errors.Is(err, credpool.ErrTransientThrottle) {
		t.Fatalf("default threshold must not rotate on 2700s hint: err=%v", err)
	}
	if len(seen) != 1 {
		t.Fatalf("default threshold must not consume secondary: seen=%q", seen)
	}
}
