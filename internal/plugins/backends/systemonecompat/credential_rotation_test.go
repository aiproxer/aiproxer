package systemonecompat

import (
	"context"
	"fmt"
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

// sysoneFailure scripts one key's upstream failure.
type sysoneFailure struct {
	status     int
	retryAfter string
	body       string
}

// sysoneEmulator fails scripted keys and serves wireResponse to every other
// key while recording the ordered bearer material it observed.
type sysoneEmulator struct {
	mu       sync.Mutex
	seen     []string
	fail     map[string]sysoneFailure
	attempts atomic.Int32
}

func (e *sysoneEmulator) handler() http.Handler {
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
			body := f.body
			if body == "" {
				body = `{"detail":[{"loc":["body","questions"],"msg":"limited","type":"value_error"}]}`
			}
			w.WriteHeader(f.status)
			_, _ = io.WriteString(w, body)
			return
		}
		_, _ = io.WriteString(w, wireResponse)
	})
}

func (e *sysoneEmulator) seenKeys() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.seen...)
}

func sysoneNode(t *testing.T, base, root string) yaml.Node {
	t.Helper()
	var n yaml.Node
	raw := fmt.Sprintf("backend_prefix: decision\nbase_url: %s\napi_key_env_var_root: %s\nmodels:\n  source: inline\n  items:\n    - canonical_id: jev\n      native_id: jev\n", base, root)
	if err := yaml.Unmarshal([]byte(raw), &n); err != nil {
		t.Fatal(err)
	}
	return n
}

func sysoneCandidate() routing.AttemptCandidate {
	return routing.AttemptCandidate{Primary: routing.Primary{Model: "jev"}}
}

func drainSysone(t *testing.T, be execbackend.Backend) []aipapi.Event {
	t.Helper()
	es, err := be.Open(context.Background(), decisionCall(), sysoneCandidate())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = es.Close() }()
	var events []aipapi.Event
	for {
		ev, rerr := es.Recv(context.Background())
		if rerr == io.EOF {
			return events
		}
		if rerr != nil {
			t.Fatalf("Recv: %v", rerr)
		}
		events = append(events, ev)
	}
}

// clearSysoneRoot clears a custom root and its numbered slots.
func clearSysoneRoot(t *testing.T, root string) {
	t.Helper()
	t.Setenv(root, "")
	for i := 1; i <= 256; i++ {
		t.Setenv(fmt.Sprintf("%s_%d", root, i), "")
	}
}

// TestCredentialRotation_long429AdvancesToNextKey proves requirement 3.2 for
// the SystemOne decision path: a long-hint 429 quota-cools the first key and
// the pre-output attempt continues with the ordered second key.
func TestCredentialRotation_long429AdvancesToNextKey(t *testing.T) {
	root := "SYS1_ROT_LONG_API_KEY"
	clearSysoneRoot(t, root)
	t.Setenv(root, "sys-k1")
	t.Setenv(root+"_2", "sys-k2")

	emu := &sysoneEmulator{fail: map[string]sysoneFailure{"sys-k1": {status: 429, retryAfter: "7200"}}}
	srv := httptest.NewServer(emu.handler())
	t.Cleanup(srv.Close)

	be, err := BuildCompatible("decision", sysoneNode(t, srv.URL, root), srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	events := drainSysone(t, be)
	if len(events) == 0 || events[0].Kind != aipapi.EventResponseStarted {
		t.Fatalf("missing decision stream after rotation in %+v", events)
	}
	if seen := emu.seenKeys(); len(seen) != 2 || seen[0] != "sys-k1" || seen[1] != "sys-k2" {
		t.Fatalf("credential order = %v, want [sys-k1 sys-k2]", seen)
	}
}

// TestCredentialRotation_authInvalidAdvancesToNextKey proves a 401 retires the
// first key pre-output and the decision attempt continues with the next key.
func TestCredentialRotation_authInvalidAdvancesToNextKey(t *testing.T) {
	root := "SYS1_ROT_AUTH_API_KEY"
	clearSysoneRoot(t, root)
	t.Setenv(root, "sys-k1")
	t.Setenv(root+"_2", "sys-k2")

	emu := &sysoneEmulator{fail: map[string]sysoneFailure{"sys-k1": {status: 401}}}
	srv := httptest.NewServer(emu.handler())
	t.Cleanup(srv.Close)

	be, err := BuildCompatible("decision", sysoneNode(t, srv.URL, root), srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	events := drainSysone(t, be)
	if len(events) == 0 || events[0].Kind != aipapi.EventResponseStarted {
		t.Fatalf("missing decision stream after auth advance in %+v", events)
	}
	if seen := emu.seenKeys(); len(seen) != 2 || seen[0] != "sys-k1" || seen[1] != "sys-k2" {
		t.Fatalf("credential order = %v, want [sys-k1 sys-k2]", seen)
	}
}

// TestCredentialRotation_shortThrottleKeepsSameKey proves the short-throttle
// invariant on the decision path: no secondary key is consumed.
func TestCredentialRotation_shortThrottleKeepsSameKey(t *testing.T) {
	root := "SYS1_ROT_SHORT_API_KEY"
	clearSysoneRoot(t, root)
	t.Setenv(root, "sys-k1")
	t.Setenv(root+"_2", "sys-k2")

	emu := &sysoneEmulator{fail: map[string]sysoneFailure{"sys-k1": {status: 429, retryAfter: "60"}}}
	srv := httptest.NewServer(emu.handler())
	t.Cleanup(srv.Close)

	be, err := BuildCompatible("decision", sysoneNode(t, srv.URL, root), srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	_, err = be.Open(context.Background(), decisionCall(), sysoneCandidate())
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

// TestCredentialRotation_failedAttemptUsageAccumulatesAcrossKeys proves
// provider-billable usage evidence from rotated attempts is preserved: when
// every key fails with usage-bearing errors, the terminal failure sideband
// carries each attempt's usage instead of only the last one.
func TestCredentialRotation_failedAttemptUsageAccumulatesAcrossKeys(t *testing.T) {
	root := "SYS1_ROT_USAGE_API_KEY"
	clearSysoneRoot(t, root)
	t.Setenv(root, "sys-k1")
	t.Setenv(root+"_2", "sys-k2")

	usageBody := `{"usage":{"input_tokens":3,"output_tokens":0}}`
	emu := &sysoneEmulator{fail: map[string]sysoneFailure{
		"sys-k1": {status: 401, body: usageBody},
		"sys-k2": {status: 401, body: usageBody},
	}}
	srv := httptest.NewServer(emu.handler())
	t.Cleanup(srv.Close)

	be, err := BuildCompatible("decision", sysoneNode(t, srv.URL, root), srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	es, err := be.Open(context.Background(), decisionCall(), sysoneCandidate())
	if err != nil {
		t.Fatalf("terminal failure must arrive as evidence stream, got error: %v", err)
	}
	if es == nil {
		t.Fatal("expected evidence stream, got nil")
	}
	defer func() { _ = es.Close() }()
	evidencer, ok := es.(interface{ DrainUsageEvidence() []aipapi.Event })
	if !ok {
		t.Fatalf("terminal stream %T carries no usage sideband", es)
	}
	got := evidencer.DrainUsageEvidence()
	if len(got) != 2 {
		t.Fatalf("accumulated usage evidence = %d events, want 2 (one per rotated key)", len(got))
	}
	if seen := emu.seenKeys(); len(seen) != 2 {
		t.Fatalf("credential order = %v, want both keys attempted", seen)
	}
	// Each billable attempt must carry a distinct nonsecret source identity
	// so the core billing ledger admits both instead of deduplicating as one.
	// This is the final-ledger shape, not merely the adapter event count.
	seenKeys := map[string]struct{}{}
	for _, ev := range got {
		if ev.Kind != aipapi.EventUsageDelta {
			t.Fatalf("evidence kind = %q, want usage_delta", ev.Kind)
		}
		if ev.Accounting.Plane != aipapi.UsagePlaneProviderBillable {
			t.Fatalf("evidence plane = %q, want provider_billable", ev.Accounting.Plane)
		}
		key := strings.TrimSpace(ev.Accounting.DedupeKey)
		if key == "" {
			t.Fatal("evidence carries empty dedupe identity")
		}
		if !strings.HasPrefix(key, "systemone:failed-attempt-usage:") {
			t.Fatalf("evidence identity = %q, want systemone:failed-attempt-usage:<slot> prefix", key)
		}
		if _, dup := seenKeys[key]; dup {
			t.Fatalf("duplicate billing identity %q; core ledger would admit only one", key)
		}
		seenKeys[key] = struct{}{}
		if strings.Contains(key, "sys-k1") || strings.Contains(key, "sys-k2") {
			t.Fatalf("billing identity leaks secret material: %q", key)
		}
	}
	// Simulate the core per-attempt dedupe map: both identities must be
	// admitted, not collapsed.
	admitted := map[string]struct{}{}
	for _, ev := range got {
		admitted[strings.TrimSpace(ev.Accounting.DedupeKey)] = struct{}{}
	}
	if len(admitted) != 2 {
		t.Fatalf("core ledger would admit %d records, want 2", len(admitted))
	}
}

// TestCredentialRotation_successPreservesPriorFailedAttemptUsage proves the
// integrated billing case the adapter-level count cannot catch: when the
// first key fails billable and the second key succeeds, the successful
// stream must still drain the failed attempt's evidence for the core ledger.
func TestCredentialRotation_successPreservesPriorFailedAttemptUsage(t *testing.T) {
	root := "SYS1_ROT_SUCCESS_USAGE_API_KEY"
	clearSysoneRoot(t, root)
	t.Setenv(root, "sys-k1")
	t.Setenv(root+"_2", "sys-k2")

	usageBody := `{"usage":{"input_tokens":7,"output_tokens":0}}`
	emu := &sysoneEmulator{fail: map[string]sysoneFailure{
		"sys-k1": {status: 401, body: usageBody},
	}}
	srv := httptest.NewServer(emu.handler())
	t.Cleanup(srv.Close)

	be, err := BuildCompatible("decision", sysoneNode(t, srv.URL, root), srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	es, err := be.Open(context.Background(), decisionCall(), sysoneCandidate())
	if err != nil {
		t.Fatalf("rotation success must not return an error: %v", err)
	}
	defer func() { _ = es.Close() }()
	events := drainSysoneEvents(t, es)
	if len(events) == 0 || events[0].Kind != aipapi.EventResponseStarted {
		t.Fatalf("missing decision stream after rotation in %+v", events)
	}
	evidencer, ok := es.(interface{ DrainUsageEvidence() []aipapi.Event })
	if !ok {
		t.Fatalf("success stream %T carries no usage sideband; failed-attempt evidence would be lost", es)
	}
	got := evidencer.DrainUsageEvidence()
	if len(got) != 1 {
		t.Fatalf("success sideband evidence = %d events, want 1 (failed first key)", len(got))
	}
	ev := got[0]
	if ev.Kind != aipapi.EventUsageDelta || ev.InputTokens != 7 {
		t.Fatalf("failed-attempt evidence = %+v, want input_tokens=7 usage_delta", ev)
	}
	key := strings.TrimSpace(ev.Accounting.DedupeKey)
	if !strings.HasPrefix(key, "systemone:failed-attempt-usage:") || key == "systemone:failed-attempt-usage" {
		t.Fatalf("success sideband identity = %q, want distinct per-attempt identity", key)
	}
	if seen := emu.seenKeys(); len(seen) != 2 {
		t.Fatalf("credential order = %v, want both keys attempted", seen)
	}
}

func drainSysoneEvents(t *testing.T, es interface {
	Recv(context.Context) (aipapi.Event, error)
},
) []aipapi.Event {
	t.Helper()
	var events []aipapi.Event
	for {
		ev, err := es.Recv(context.Background())
		if err == io.EOF {
			return events
		}
		if err != nil {
			t.Fatalf("Recv: %v", err)
		}
		events = append(events, ev)
	}
}

// TestCredentialSnapshot_candidateKeysPreferredOverProcessEnv closes the
// task-2 known gap for this family: an authorized candidate snapshot wins
// over divergent process-environment material at factory build.
func TestCredentialSnapshot_candidateKeysPreferredOverProcessEnv(t *testing.T) {
	root := "SYS1_SNAP_RED_API_KEY"
	clearSysoneRoot(t, root)
	t.Setenv(root, "env-key")

	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if gotAuth != "Bearer snap-k1" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = io.WriteString(w, wireResponse)
	}))
	t.Cleanup(srv.Close)

	snap := pluginreg.HostCredentialSnapshot{
		ByRoot:      map[string][]string{root: {"snap-k1"}},
		MaxEnvIndex: 20,
	}
	res, err := LifecycleSystemOneCompatible("decision", sysoneNode(t, srv.URL, root), srv.Client(), pluginreg.BackendFactoryDeps{Credentials: snap})
	if err != nil {
		t.Fatalf("lifecycle build: %v", err)
	}
	es, err := res.Backend.Open(context.Background(), decisionCall(), sysoneCandidate())
	if err != nil {
		t.Fatalf("Open with snapshot keys: %v", err)
	}
	func() {
		defer func() { _ = es.Close() }()
		for {
			_, rerr := es.Recv(context.Background())
			if rerr == io.EOF {
				return
			}
			if rerr != nil {
				t.Fatalf("Recv: %v", rerr)
			}
		}
	}()
	if gotAuth != "Bearer snap-k1" {
		t.Fatalf("serving auth = %q, want admitted snapshot key", gotAuth)
	}
	if strings.Contains(gotAuth, "env-key") {
		t.Fatalf("serving used process-env material: %q", gotAuth)
	}
}
