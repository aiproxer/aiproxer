package modeldiscover_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/plugins/backends/modeldiscover"
)

const rotationModelsBody = `{"object":"list","data":[{"id":"gpt-4o"}]}`

const rotationAnthropicModelsBody = `{"data":[{"id":"claude-sonnet-4-5","display_name":"Claude Sonnet 4.5"}]}`

// inventoryEmulator fails scripted keys with status/Retry-After and serves
// the success body to every other key, recording observed secrets in order.
type inventoryEmulator struct {
	mu         sync.Mutex
	seen       []string
	fail       map[string]int
	retryAfter map[string]string
	header     string
	success    string
	attempts   atomic.Int32
}

func (e *inventoryEmulator) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.attempts.Add(1)
		key := r.Header.Get(e.header)
		key = strings.TrimPrefix(key, "Bearer ")
		e.mu.Lock()
		e.seen = append(e.seen, key)
		e.mu.Unlock()
		if status, ok := e.fail[key]; ok {
			if ra := e.retryAfter[key]; ra != "" {
				w.Header().Set("Retry-After", ra)
			}
			w.WriteHeader(status)
			return
		}
		_, _ = w.Write([]byte(e.success))
	})
}

func (e *inventoryEmulator) seenKeys() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.seen...)
}

// TestInventoryRotation_openAIAuthInvalidAdvancesToNextKey proves the remote
// model inventory for custom OpenAI families uses the shared ordered selector:
// a 401 on the first key advances pre-output instead of failing closed.
func TestInventoryRotation_openAIAuthInvalidAdvancesToNextKey(t *testing.T) {
	t.Parallel()
	emu := &inventoryEmulator{
		header:  "Authorization",
		success: rotationModelsBody,
		fail:    map[string]int{"inv-k1": http.StatusUnauthorized},
	}
	srv := httptest.NewServer(emu.handler())
	t.Cleanup(srv.Close)

	p := modeldiscover.OpenAICompatibleModelsProvider{
		BaseURL:            srv.URL + "/v1",
		APIKey:             "inv-k1",
		APIKeys:            []string{"inv-k1", "inv-k2"},
		HTTPClient:         srv.Client(),
		CanonicalPrefix:    "custom",
		CompatibleModeAuth: true,
	}
	snap, err := p.LoadModels(context.Background())
	if err != nil {
		t.Fatalf("LoadModels: %v", err)
	}
	if len(snap.Models) != 1 || snap.Models[0].NativeID != "gpt-4o" {
		t.Fatalf("models = %+v", snap.Models)
	}
	if seen := emu.seenKeys(); len(seen) != 2 || seen[0] != "inv-k1" || seen[1] != "inv-k2" {
		t.Fatalf("credential order = %v, want [inv-k1 inv-k2]", seen)
	}
}

// TestInventoryRotation_openAILong429AdvancesToNextKey proves a long-hint 429
// advances the inventory selector to the next ordered key.
func TestInventoryRotation_openAILong429AdvancesToNextKey(t *testing.T) {
	t.Parallel()
	emu := &inventoryEmulator{
		header:     "Authorization",
		success:    rotationModelsBody,
		fail:       map[string]int{"inv-k1": http.StatusTooManyRequests},
		retryAfter: map[string]string{"inv-k1": "7200"},
	}
	srv := httptest.NewServer(emu.handler())
	t.Cleanup(srv.Close)

	p := modeldiscover.OpenAICompatibleModelsProvider{
		BaseURL:            srv.URL + "/v1",
		APIKeys:            []string{"inv-k1", "inv-k2"},
		HTTPClient:         srv.Client(),
		CanonicalPrefix:    "custom",
		CompatibleModeAuth: true,
	}
	if _, err := p.LoadModels(context.Background()); err != nil {
		t.Fatalf("LoadModels: %v", err)
	}
	if seen := emu.seenKeys(); len(seen) != 2 || seen[0] != "inv-k1" || seen[1] != "inv-k2" {
		t.Fatalf("credential order = %v, want [inv-k1 inv-k2]", seen)
	}
}

// TestInventoryRotation_openAIShortThrottleDoesNotRotate proves the
// short-throttle invariant for inventory: a short-hint 429 never consumes the
// secondary key.
func TestInventoryRotation_openAIShortThrottleDoesNotRotate(t *testing.T) {
	t.Parallel()
	emu := &inventoryEmulator{
		header:     "Authorization",
		success:    rotationModelsBody,
		fail:       map[string]int{"inv-k1": http.StatusTooManyRequests},
		retryAfter: map[string]string{"inv-k1": "60"},
	}
	srv := httptest.NewServer(emu.handler())
	t.Cleanup(srv.Close)

	p := modeldiscover.OpenAICompatibleModelsProvider{
		BaseURL:            srv.URL + "/v1",
		APIKeys:            []string{"inv-k1", "inv-k2"},
		HTTPClient:         srv.Client(),
		CanonicalPrefix:    "custom",
		CompatibleModeAuth: true,
	}
	if _, err := p.LoadModels(context.Background()); err == nil {
		t.Fatal("expected throttle error, got success")
	}
	if n := emu.attempts.Load(); n != 1 {
		t.Fatalf("upstream attempts = %d, want 1 (secondary key must not be consumed)", n)
	}
}

// TestInventoryRotation_anthropicAuthInvalidAdvancesToNextKey proves the same
// ordered selector contract for the custom Anthropic remote inventory.
func TestInventoryRotation_anthropicAuthInvalidAdvancesToNextKey(t *testing.T) {
	t.Parallel()
	emu := &inventoryEmulator{
		header:  "x-api-key",
		success: rotationAnthropicModelsBody,
		fail:    map[string]int{"inv-k1": http.StatusUnauthorized},
	}
	srv := httptest.NewServer(emu.handler())
	t.Cleanup(srv.Close)

	p := modeldiscover.AnthropicModelsProvider{
		BaseURL:            srv.URL,
		APIKeys:            []string{"inv-k1", "inv-k2"},
		HTTPClient:         srv.Client(),
		CanonicalPrefix:    "custom",
		CompatibleModeAuth: true,
	}
	snap, err := p.LoadModels(context.Background())
	if err != nil {
		t.Fatalf("LoadModels: %v", err)
	}
	if len(snap.Models) != 1 || snap.Models[0].NativeID != "claude-sonnet-4-5" {
		t.Fatalf("models = %+v", snap.Models)
	}
	if seen := emu.seenKeys(); len(seen) != 2 || seen[0] != "inv-k1" || seen[1] != "inv-k2" {
		t.Fatalf("credential order = %v, want [inv-k1 inv-k2]", seen)
	}
}

// TestInventoryRotation_errorsOmitKeyMaterial proves inventory failures never
// echo secret values.
func TestInventoryRotation_errorsOmitKeyMaterial(t *testing.T) {
	t.Parallel()
	emu := &inventoryEmulator{
		header:  "Authorization",
		success: rotationModelsBody,
		fail:    map[string]int{"inv-secret-k1": http.StatusUnauthorized},
	}
	srv := httptest.NewServer(emu.handler())
	t.Cleanup(srv.Close)

	p := modeldiscover.OpenAICompatibleModelsProvider{
		BaseURL:            srv.URL + "/v1",
		APIKeys:            []string{"inv-secret-k1"},
		HTTPClient:         srv.Client(),
		CanonicalPrefix:    "custom",
		CompatibleModeAuth: true,
	}
	_, err := p.LoadModels(context.Background())
	if err == nil {
		t.Fatal("expected auth error, got success")
	}
	if strings.Contains(err.Error(), "inv-secret-k1") {
		t.Fatalf("inventory failure leaked secret: %v", err)
	}
}

const rotationGeminiModelsBody = `{"models":[{"name":"models/gemini-2.0-flash","displayName":"Gemini 2.0 Flash"}]}`

// geminiEmulator serves Gemini inventory: x-goog-api-key header, /v1beta/models.
type geminiEmulator struct {
	mu         sync.Mutex
	seen       []string
	fail       map[string]int
	retryAfter map[string]string
	success    string
	attempts   atomic.Int32
}

func (e *geminiEmulator) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.attempts.Add(1)
		key := r.Header.Get("x-goog-api-key")
		e.mu.Lock()
		e.seen = append(e.seen, key)
		e.mu.Unlock()
		if status, ok := e.fail[key]; ok {
			if ra := e.retryAfter[key]; ra != "" {
				w.Header().Set("Retry-After", ra)
			}
			w.WriteHeader(status)
			return
		}
		_, _ = w.Write([]byte(e.success))
	})
}

func (e *geminiEmulator) seenKeys() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.seen...)
}

// TestInventoryRotation_geminiAuthInvalidAdvancesToNextKey proves Gemini
// discovery uses the shared ordered selector: a 401 on the first key
// advances instead of failing closed when the second key is valid.
func TestInventoryRotation_geminiAuthInvalidAdvancesToNextKey(t *testing.T) {
	t.Parallel()
	emu := &geminiEmulator{
		success: rotationGeminiModelsBody,
		fail:    map[string]int{"inv-k1": http.StatusUnauthorized},
	}
	srv := httptest.NewServer(emu.handler())
	t.Cleanup(srv.Close)

	p := modeldiscover.GeminiModelsProvider{
		BaseURL:    srv.URL,
		APIKeys:    []string{"inv-k1", "inv-k2"},
		HTTPClient: srv.Client(),
	}
	snap, err := p.LoadModels(context.Background())
	if err != nil {
		t.Fatalf("LoadModels: %v", err)
	}
	if len(snap.Models) != 1 || snap.Models[0].NativeID != "gemini-2.0-flash" {
		t.Fatalf("models = %+v", snap.Models)
	}
	if seen := emu.seenKeys(); len(seen) != 2 || seen[0] != "inv-k1" || seen[1] != "inv-k2" {
		t.Fatalf("credential order = %v, want [inv-k1 inv-k2]", seen)
	}
}

// TestInventoryRotation_geminiThresholdHonorsHostPolicy proves Gemini
// discovery honors the host-projected quota threshold with the same taxonomy
// as the other migrated providers: a 2700s hint rotates under a 30m policy
// but stays a same-key throttle under the default.
func TestInventoryRotation_geminiThresholdHonorsHostPolicy(t *testing.T) {
	t.Parallel()
	newEmu := func() *geminiEmulator {
		return &geminiEmulator{
			success:    rotationGeminiModelsBody,
			fail:       map[string]int{"inv-k1": http.StatusTooManyRequests},
			retryAfter: map[string]string{"inv-k1": "2700"},
		}
	}
	// Nondefault 30m threshold rotates.
	emu := newEmu()
	srv := httptest.NewServer(emu.handler())
	t.Cleanup(srv.Close)
	p := modeldiscover.GeminiModelsProvider{
		BaseURL:              srv.URL,
		APIKeys:              []string{"inv-k1", "inv-k2"},
		HTTPClient:           srv.Client(),
		QuotaSwitchThreshold: 30 * time.Minute,
	}
	if _, err := p.LoadModels(context.Background()); err != nil {
		t.Fatalf("30m threshold must rotate on 2700s hint: %v", err)
	}
	if seen := emu.seenKeys(); len(seen) != 2 {
		t.Fatalf("30m threshold must consume both keys, seen=%v", seen)
	}
	// Default threshold pins primary.
	emu2 := newEmu()
	srv2 := httptest.NewServer(emu2.handler())
	t.Cleanup(srv2.Close)
	p2 := modeldiscover.GeminiModelsProvider{
		BaseURL:    srv2.URL,
		APIKeys:    []string{"inv-k1", "inv-k2"},
		HTTPClient: srv2.Client(),
	}
	if _, err := p2.LoadModels(context.Background()); err == nil {
		t.Fatal("default threshold must not rotate on 2700s hint")
	}
	if n := emu2.attempts.Load(); n != 1 {
		t.Fatalf("default attempts = %d, want 1", n)
	}
}
