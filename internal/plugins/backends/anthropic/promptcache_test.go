package anthropic

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/promptcache"
)

func TestCacheControllerRequiresCacheEvidence(t *testing.T) {
	t.Parallel()
	c, err := NewCacheController(CacheControllerConfig{BaseURL: "https://example.test"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.IssueTarget(CacheTarget{ALegID: "a", BLegID: "b", BackendInstanceID: "anthropic", TargetID: "t", GenerationID: "g", Model: "claude", TTL: "5m", Renewal: RenewalSnapshot{Model: "claude", Messages: []RenewalMessage{{Role: "user", Content: "continue"}}}}, time.Now())
	if err != ErrNoCacheEvidence {
		t.Fatalf("err=%v", err)
	}
}

func TestCacheControllerRenewSanitizesZeroOutputRequestAndUsesFreshCredential(t *testing.T) {
	t.Parallel()
	var captured struct {
		Body   map[string]any
		APIKey string
	}
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		captured.APIKey = r.Header.Get("x-api-key")
		if err := json.NewDecoder(r.Body).Decode(&captured.Body); err != nil {
			t.Error(err)
			return
		}
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"usage":{"input_tokens":4,"output_tokens":0,"cache_read_input_tokens":20,"cache_creation_input_tokens":0}}`))
	}))
	defer srv.Close()
	fresh := atomic.Int32{}
	c, err := NewCacheController(CacheControllerConfig{BaseURL: srv.URL, APIKey: "old", HTTPClient: srv.Client(), ResolveAPIKey: func(context.Context, CacheTarget) (string, error) { fresh.Add(1); return "fresh", nil }})
	if err != nil {
		t.Fatal(err)
	}
	read := int64(20)
	o, err := c.IssueTarget(CacheTarget{ALegID: "a", BLegID: "b", BackendInstanceID: "anthropic", TargetID: "t", GenerationID: "g", Model: "claude", TTL: "5m", Renewal: RenewalSnapshot{Model: "claude", System: []RenewalSystemBlock{{Type: "text", Text: "cached prefix", CacheControl: &RenewalCacheControl{Type: "ephemeral", TTL: "5m"}}}, Messages: []RenewalMessage{{Role: "user", Content: "continue"}}}, Evidence: promptcache.CacheEvidence{CacheReadTokens: &read}}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.Renew(context.Background(), promptcache.RenewRequest{Handle: o.Handle, OperationID: "op-1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := resp.Validate(); err != nil {
		t.Fatal(err)
	}
	if resp.Result.Status != promptcache.Renewed || resp.Result.Observation == nil {
		t.Fatalf("result=%+v", resp.Result)
	}
	if calls.Load() != 1 || fresh.Load() != 1 || captured.APIKey != "fresh" {
		t.Fatalf("calls=%d fresh=%d key=%q", calls.Load(), fresh.Load(), captured.APIKey)
	}
	if got := captured.Body["max_tokens"]; got != float64(0) {
		t.Fatalf("max_tokens=%v want 0 (zero-output prewarm)", got)
	}
	if got := captured.Body["stream"]; got != false {
		t.Fatalf("stream=%v", got)
	}
	for _, key := range []string{"thinking", "response_format", "tool_choice"} {
		if _, ok := captured.Body[key]; ok {
			t.Fatalf("incompatible field %q retained", key)
		}
	}
}

func TestCacheControllerReleaseIsIdempotentAndStaleHandleFails(t *testing.T) {
	t.Parallel()
	c, err := NewCacheController(CacheControllerConfig{BaseURL: "https://example.test"})
	if err != nil {
		t.Fatal(err)
	}
	read := int64(1)
	o, err := c.IssueTarget(CacheTarget{ALegID: "a", BLegID: "b", BackendInstanceID: "anthropic", TargetID: "t", GenerationID: "g", Model: "claude", TTL: "1h", Renewal: RenewalSnapshot{Model: "claude", Messages: []RenewalMessage{{Role: "user", Content: "continue"}}}, Evidence: promptcache.CacheEvidence{CacheReadTokens: &read}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Release(context.Background(), promptcache.ReleaseRequest{Handle: o.Handle}); err != nil {
		t.Fatal(err)
	}
	if err := c.Release(context.Background(), promptcache.ReleaseRequest{Handle: o.Handle}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Renew(context.Background(), promptcache.RenewRequest{Handle: o.Handle, OperationID: "op"}); err != promptcache.ErrStaleHandle {
		t.Fatalf("err=%v", err)
	}
}
