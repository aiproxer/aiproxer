package openresponsescompat

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"gopkg.in/yaml.v3"
)

// rotationFailure scripts one key's upstream failure.
type rotationFailure struct {
	status     int
	retryAfter string
}

// rotationEmulator fails scripted keys and serves successBody to every other
// key while recording the ordered Authorization material it observed.
type rotationEmulator struct {
	mu       sync.Mutex
	seen     []string
	fail     map[string]rotationFailure
	success  func(w http.ResponseWriter)
	attempts atomic.Int32
}

func (e *rotationEmulator) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.attempts.Add(1)
		key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		e.mu.Lock()
		e.seen = append(e.seen, key)
		e.mu.Unlock()
		if f, ok := e.fail[key]; ok {
			if f.retryAfter != "" {
				w.Header().Set("Retry-After", f.retryAfter)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(f.status)
			_, _ = io.WriteString(w, `{"error":{"message":"limited","type":"rate_limit"}}`)
			return
		}
		e.success(w)
	})
}

func (e *rotationEmulator) seenKeys() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.seen...)
}

func writeResourceJSON(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, completeResourceJSON)
}

func writeCompactJSON(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, completeCompactResourceJSON)
}

func rotationBackend(t *testing.T, srv *httptest.Server, keys ...string) BackendSpec {
	t.Helper()
	spec := testSpec()
	spec.ID = "rot-or"
	spec.BaseURL = srv.URL
	spec.HTTPClient = srv.Client()
	if len(keys) > 0 {
		spec.APIKey = keys[0]
		spec.APIKeys = append([]string(nil), keys...)
	}
	return spec
}

func rotationCandidate() routing.AttemptCandidate {
	return routing.AttemptCandidate{Primary: routing.Primary{Model: "model-x"}}
}

func openDrainRotation(t *testing.T, be execbackend.Backend, call aipapi.Call) []aipapi.Event {
	t.Helper()
	es, err := be.Open(context.Background(), call, rotationCandidate())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return drainManagedEvents(t, es)
}

// TestCredentialRotation_nonStreamingLong429AdvancesToNextKey proves
// requirement 3.2 for the generic OpenResponses non-streaming path: a 429
// with a Retry-After strictly above the 1h threshold quota-cools the first
// key and the pre-output attempt continues with the ordered second key.
func TestCredentialRotation_nonStreamingLong429AdvancesToNextKey(t *testing.T) {
	t.Parallel()
	emu := &rotationEmulator{fail: map[string]rotationFailure{"rot-k1": {status: 429, retryAfter: "7200"}}, success: writeResourceJSON}
	srv := httptest.NewServer(emu.handler())
	t.Cleanup(srv.Close)

	be := NewBackend(rotationBackend(t, srv, "rot-k1", "rot-k2"))
	events := openDrainRotation(t, be, itemAuthorityCreateCall())
	if !hasTextDelta(events, "The weather in Paris is sunny.") {
		t.Fatalf("missing success text after rotation in %+v", events)
	}
	if seen := emu.seenKeys(); len(seen) != 2 || seen[0] != "rot-k1" || seen[1] != "rot-k2" {
		t.Fatalf("credential order = %v, want [rot-k1 rot-k2]", seen)
	}
}

// TestCredentialRotation_nonStreamingAuthInvalidAdvancesToNextKey proves a
// 401 retires the first key pre-output and the attempt continues with the
// next eligible key.
func TestCredentialRotation_nonStreamingAuthInvalidAdvancesToNextKey(t *testing.T) {
	t.Parallel()
	emu := &rotationEmulator{fail: map[string]rotationFailure{"rot-k1": {status: 401}}, success: writeResourceJSON}
	srv := httptest.NewServer(emu.handler())
	t.Cleanup(srv.Close)

	be := NewBackend(rotationBackend(t, srv, "rot-k1", "rot-k2"))
	events := openDrainRotation(t, be, itemAuthorityCreateCall())
	if !hasTextDelta(events, "The weather in Paris is sunny.") {
		t.Fatalf("missing success text after auth advance in %+v", events)
	}
	if seen := emu.seenKeys(); len(seen) != 2 || seen[0] != "rot-k1" || seen[1] != "rot-k2" {
		t.Fatalf("credential order = %v, want [rot-k1 rot-k2]", seen)
	}
}

// TestCredentialRotation_nonStreamingShortThrottleKeepsSameKey proves the
// short-throttle invariant: a 429 at or below the threshold never consumes
// the secondary key, including for the streaming-adjacent non-streaming path.
func TestCredentialRotation_nonStreamingShortThrottleKeepsSameKey(t *testing.T) {
	t.Parallel()
	emu := &rotationEmulator{fail: map[string]rotationFailure{"rot-k1": {status: 429, retryAfter: "60"}}, success: writeResourceJSON}
	srv := httptest.NewServer(emu.handler())
	t.Cleanup(srv.Close)

	be := NewBackend(rotationBackend(t, srv, "rot-k1", "rot-k2"))
	_, err := be.Open(context.Background(), itemAuthorityCreateCall(), rotationCandidate())
	if err == nil {
		t.Fatal("expected transient-throttle error, got success")
	}
	if !aipapi.IsRecoverablePreOutput(err) {
		t.Fatalf("throttle must stay recoverable pre-output, got: %v", err)
	}
	if n := emu.attempts.Load(); n != 1 {
		t.Fatalf("upstream attempts = %d, want 1 (secondary key must not be consumed)", n)
	}
}

// TestCredentialRotation_streamingLong429AdvancesToNextKey proves the same
// ordered selection contract on the SSE streaming path before commitment.
func TestCredentialRotation_streamingLong429AdvancesToNextKey(t *testing.T) {
	t.Parallel()
	emu := &rotationEmulator{
		fail: map[string]rotationFailure{"rot-k1": {status: 429, retryAfter: "7200"}},
		success: func(w http.ResponseWriter) {
			writeSSE(w, textSSEBody())
		},
	}
	srv := httptest.NewServer(emu.handler())
	t.Cleanup(srv.Close)

	be := NewBackend(rotationBackend(t, srv, "rot-k1", "rot-k2"))
	events := openDrainRotation(t, be, streamingCreateCall())
	if !hasTextDelta(events, "Hello") {
		t.Fatalf("missing streamed text after rotation in %+v", events)
	}
	if seen := emu.seenKeys(); len(seen) != 2 || seen[0] != "rot-k1" || seen[1] != "rot-k2" {
		t.Fatalf("credential order = %v, want [rot-k1 rot-k2]", seen)
	}
}

// TestCredentialRotation_compactLong429AdvancesToNextKey proves the
// context-compaction path uses the shared selector instead of the pinned
// first key.
func TestCredentialRotation_compactLong429AdvancesToNextKey(t *testing.T) {
	t.Parallel()
	emu := &rotationEmulator{fail: map[string]rotationFailure{"rot-k1": {status: 429, retryAfter: "7200"}}, success: writeCompactJSON}
	srv := httptest.NewServer(emu.handler())
	t.Cleanup(srv.Close)

	be := NewBackend(rotationBackend(t, srv, "rot-k1", "rot-k2"))
	events := openDrainRotation(t, be, itemAuthorityCompactCall())
	if !hasEventKind(events, aipapi.EventResponseFinished) {
		t.Fatalf("missing compaction finish after rotation in %+v", events)
	}
	if seen := emu.seenKeys(); len(seen) != 2 || seen[0] != "rot-k1" || seen[1] != "rot-k2" {
		t.Fatalf("credential order = %v, want [rot-k1 rot-k2]", seen)
	}
}

// TestCredentialRotation_errorOmitsKeyMaterial proves classified credential
// failures never echo secret values.
func TestCredentialRotation_errorOmitsKeyMaterial(t *testing.T) {
	t.Parallel()
	emu := &rotationEmulator{fail: map[string]rotationFailure{"rot-only": {status: 401}}, success: writeResourceJSON}
	srv := httptest.NewServer(emu.handler())
	t.Cleanup(srv.Close)

	be := NewBackend(rotationBackend(t, srv, "rot-only"))
	_, err := be.Open(context.Background(), itemAuthorityCreateCall(), rotationCandidate())
	if err == nil {
		t.Fatal("expected auth failure, got success")
	}
	if strings.Contains(err.Error(), "rot-only") {
		t.Fatalf("credential failure leaked secret: %v", err)
	}
}

// TestCredentialSnapshot_candidateKeysPreferredOverProcessEnv closes the
// task-2 known gap for this family: when the composition root hands an
// authorized candidate snapshot to the factory, serving uses the admitted
// keys even though the process environment carries different material.
func TestCredentialSnapshot_candidateKeysPreferredOverProcessEnv(t *testing.T) {
	root := "OR_SNAP_RED_API_KEY"
	t.Setenv(root, "env-key")
	t.Setenv(root+"_2", "env-key-2")

	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if gotAuth != "Bearer snap-k1" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		writeResourceJSON(w)
	}))
	t.Cleanup(srv.Close)

	raw := "backend_prefix: snap-or\nbase_url: " + srv.URL + "\napi_key_env_var_root: " + root + "\n"
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &node); err != nil {
		t.Fatal(err)
	}
	snap := pluginreg.HostCredentialSnapshot{
		ByRoot:      map[string][]string{root: {"snap-k1"}},
		MaxEnvIndex: 20,
	}
	res, err := LifecycleOpenResponsesCompatible("snap-or", node, srv.Client(), pluginreg.BackendFactoryDeps{Credentials: snap})
	if err != nil {
		t.Fatalf("lifecycle build: %v", err)
	}
	es, err := res.Backend.Open(context.Background(), itemAuthorityCreateCall(), rotationCandidate())
	if err != nil {
		t.Fatalf("Open with snapshot keys: %v", err)
	}
	drainManagedEvents(t, es)
	if gotAuth != "Bearer snap-k1" {
		t.Fatalf("serving auth = %q, want admitted snapshot key", gotAuth)
	}
}

// TestCredentialIsolation_distinctRootsKeepIndependentPools proves
// requirement 4.4: two instances bound to different project roots keep
// independent selection state, so a long 429 cooling one instance's primary
// never steers the other instance away from its own primary.
func TestCredentialIsolation_distinctRootsKeepIndependentPools(t *testing.T) {
	t.Parallel()
	emu := &rotationEmulator{fail: map[string]rotationFailure{"iso-a1": {status: 429, retryAfter: "7200"}}, success: writeResourceJSON}
	srv := httptest.NewServer(emu.handler())
	t.Cleanup(srv.Close)

	beA := NewBackend(rotationBackend(t, srv, "iso-a1", "iso-a2"))
	beB := NewBackend(rotationBackend(t, srv, "iso-b1", "iso-b2"))
	openDrainRotation(t, beA, itemAuthorityCreateCall())
	openDrainRotation(t, beB, itemAuthorityCreateCall())

	seen := emu.seenKeys()
	if len(seen) != 3 || seen[0] != "iso-a1" || seen[1] != "iso-a2" || seen[2] != "iso-b1" {
		t.Fatalf("credential order = %v, want [iso-a1 iso-a2 iso-b1] (B keeps its primary)", seen)
	}
}
