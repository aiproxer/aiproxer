package runtime_test

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/infra/metrics"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/usage"
	"github.com/prometheus/client_golang/prometheus"
)

// captureSeamDigests records every digest vector the executor feeds so the
// secrecy test can assert the exact emitted bytes never surface in logs,
// read models, or metric exposition.
type captureSeamDigests struct{ vectors [][][32]byte }

func (c *captureSeamDigests) OnUsage(_ context.Context, ev usage.Event) error {
	if len(ev.SeamDigests) > 0 {
		c.vectors = append(c.vectors, append([][32]byte(nil), ev.SeamDigests...))
	}
	return nil
}

func seamTestBackend(opened *aipapi.Call) execbackend.Backend {
	return execbackend.Backend{
		Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
		Open: func(_ context.Context, call aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			if opened != nil {
				*opened = call
			}
			return aipapi.NewFixedEventStream([]aipapi.Event{
				{Kind: aipapi.EventUsageDelta, InputTokens: 3, OutputTokens: 5, TotalTokens: 8},
				{Kind: aipapi.EventResponseFinished},
			}), nil
		},
	}
}

func seamTestCall() *aipapi.Call {
	return &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "openai:gpt-4"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
}

func seamTestExecutor(obs *captureUsage, bus *hooks.Bus, enabled bool) *runtime.Executor {
	planes := aipfeature.NewContributionSet()
	snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{UsageObserver: obs, FeaturePlanes: planes.Freeze()})
	ex := runtime.TestExecutor()
	ex.Bus = bus
	ex.RuntimeSnapshot = snap
	ex.SeamDigestsEnabled = enabled
	if enabled {
		ex.SeamDigestKey = seamExecutorTestKey()
	}
	return ex
}

// TestNewExecutor_ZeroKeyDisablesFeed proves fail-closed composition: an
// enabled feed without the process-stable key would publish publicly
// reproducible fingerprints, so the constructor refuses the feed instead of
// running keyless. A non-zero key keeps the feed armed.
func TestNewExecutor_ZeroKeyDisablesFeed(t *testing.T) {
	t.Parallel()
	keyless := runtime.NewExecutor(runtime.ExecutorConfig{
		Observability: runtime.ObservabilityRuntime{SeamDigestsEnabled: true},
	})
	if keyless.SeamDigestsEnabled {
		t.Fatal("NewExecutor must refuse a keyless digest feed")
	}
	keyed := runtime.NewExecutor(runtime.ExecutorConfig{
		Observability: runtime.ObservabilityRuntime{SeamDigestsEnabled: true, SeamDigestKey: seamExecutorTestKey()},
	})
	if !keyed.SeamDigestsEnabled {
		t.Fatal("NewExecutor must keep a keyed digest feed armed")
	}
}

// TestExecutor_ZeroKeyEmitsNoDigests proves the request-path half of the
// fail-closed invariant for executors built outside NewExecutor: an enabled
// feed with a zero key publishes no digests, so no predictable fingerprint
// ever reaches observers. Without the key check the same executor emits one
// zero-key digest.
func TestExecutor_ZeroKeyEmitsNoDigests(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	obs := &captureUsage{}
	bus := hooks.New(hooks.Config{})
	planes := aipfeature.NewContributionSet()
	snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{UsageObserver: obs, FeaturePlanes: planes.Freeze()})
	ex := runtime.TestExecutor()
	ex.Bus = bus
	ex.RuntimeSnapshot = snap
	ex.SeamDigestsEnabled = true
	ex.SeamDigestKey = runtime.SeamDigestKey{}
	ex.Store = st
	ex.Backends = map[string]execbackend.Backend{"openai": seamTestBackend(nil)}
	ex.Rand = routing.NewSeededRng(1)
	stream, err := ex.Execute(context.Background(), seamTestCall())
	if err != nil {
		t.Fatal(err)
	}
	_, _ = aipapi.Collect(context.Background(), stream)
	if len(obs.events) != 1 {
		t.Fatalf("usage events %d want 1", len(obs.events))
	}
	if len(obs.events[0].SeamDigests) != 0 {
		t.Fatalf("keyless executor digests=%d want 0: predictable fingerprints must never publish", len(obs.events[0].SeamDigests))
	}
}

// seamExecutorTestKey pins one process-stable key for executor tests: every
// rebuilt executor in this package borrows the same key, mirroring the
// process→generation boundary.
func seamExecutorTestKey() runtime.SeamDigestKey {
	var key runtime.SeamDigestKey
	for i := range key {
		key[i] = byte(i + 0x31)
	}
	return key
}

// TestExecutor_SeamDigestsGateOffEmitsNoDigests proves the cost gate: with the
// feed disabled and a process key present, the proxy hashes nothing and the
// leg's usage sample carries no digests, so the enable switch alone decides
// whether per-request hashing runs.
func TestExecutor_SeamDigestsGateOffEmitsNoDigests(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	obs := &captureUsage{}
	bus := hooks.New(hooks.Config{})
	ex := seamTestExecutor(obs, bus, false)
	// A key is present, so only the disabled gate can suppress the feed.
	ex.SeamDigestKey = seamExecutorTestKey()
	ex.Store = st
	ex.Backends = map[string]execbackend.Backend{"openai": seamTestBackend(nil)}
	ex.Rand = routing.NewSeededRng(1)
	stream, err := ex.Execute(context.Background(), seamTestCall())
	if err != nil {
		t.Fatal(err)
	}
	_, _ = aipapi.Collect(context.Background(), stream)
	if len(obs.events) != 1 {
		t.Fatalf("usage events %d want 1", len(obs.events))
	}
	if len(obs.events[0].SeamDigests) != 0 {
		t.Fatalf("gate-off digests=%d want 0: the enable switch must suppress the feed even with a key present", len(obs.events[0].SeamDigests))
	}
}

// TestExecutor_SeamDigestsPerModelBoundMessage proves the feed's shape: one
// digest per model-bound item of the request the backend received, so the
// vector addresses every item the provider's cache sees. A truncated vector
// leaves the tail of the trajectory unattributable.
func TestExecutor_SeamDigestsPerModelBoundMessage(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	obs := &captureUsage{}
	bus := hooks.New(hooks.Config{})
	ex := seamTestExecutor(obs, bus, true)
	ex.Store = st
	ex.Backends = map[string]execbackend.Backend{"openai": seamTestBackend(nil)}
	ex.Rand = routing.NewSeededRng(1)
	call := seamTestCall()
	call.Messages = append(call.Messages, aipapi.Message{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("second")}})
	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = aipapi.Collect(context.Background(), stream)
	if len(obs.events) != 1 {
		t.Fatalf("usage events %d want 1", len(obs.events))
	}
	if got := len(obs.events[0].SeamDigests); got != 2 {
		t.Fatalf("digests=%d want 2, one per model-bound item", got)
	}
}

// lateSeamHook rewrites the user text after all request mutations, so the
// provider-bound request differs from the baseline the request started with.
type lateSeamHook struct{ text string }

func (h lateSeamHook) ID() string                        { return "test-late-seam" }
func (h lateSeamHook) Order() int                        { return 0 }
func (h lateSeamHook) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (h lateSeamHook) HandleRequestParts(_ context.Context, call *aipapi.Call, _ sdkhooks.PartMeta) error {
	for i := range call.Messages {
		for j := range call.Messages[i].Parts {
			if call.Messages[i].Parts[j].Text != "" {
				call.Messages[i].Parts[j].Text = h.text
			}
		}
	}
	return nil
}

// TestExecutor_SeamDigestsCapturedAtFinalBoundary proves the digests name the
// request the backend actually received: a late mutation changes the fed
// vector, so the diagnostic never attributes a change the provider-bound
// request no longer carries.
func TestExecutor_SeamDigestsCapturedAtFinalBoundary(t *testing.T) {
	t.Parallel()
	run := func(hookText string) [][32]byte {
		st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
		if err != nil {
			t.Fatal(err)
		}
		obs := &captureUsage{}
		cfg := hooks.Config{}
		if hookText != "" {
			cfg.RequestPartHooks = []sdkhooks.RequestPartHook{lateSeamHook{text: hookText}}
		}
		bus := hooks.New(cfg)
		ex := seamTestExecutor(obs, bus, true)
		ex.Store = st
		var opened aipapi.Call
		ex.Backends = map[string]execbackend.Backend{"openai": seamTestBackend(&opened)}
		ex.Rand = routing.NewSeededRng(1)
		stream, err := ex.Execute(context.Background(), seamTestCall())
		if err != nil {
			t.Fatal(err)
		}
		_, _ = aipapi.Collect(context.Background(), stream)
		if len(obs.events) != 1 {
			t.Fatalf("usage events %d want 1", len(obs.events))
		}
		if got := opened.Messages[0].Parts[0].Text; hookText != "" && got != hookText {
			t.Fatalf("opened text=%q want %q", got, hookText)
		}
		return obs.events[0].SeamDigests
	}
	plain := run("")
	hooked := run("hooked-payload")
	if len(plain) != 1 || len(hooked) != 1 {
		t.Fatalf("digests=%d,%d want 1,1", len(plain), len(hooked))
	}
	if plain[0] == hooked[0] {
		t.Fatal("late mutation must change the fed digests: capture is not at the final boundary")
	}
}

// TestExecutor_SeamDigestsPerRequestExecution proves each request feeds its
// own vector: two requests with different content carry different digests,
// so no request inherits another's seam.
func TestExecutor_SeamDigestsPerRequestExecution(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	obs := &captureUsage{}
	bus := hooks.New(hooks.Config{})
	ex := seamTestExecutor(obs, bus, true)
	ex.Store = st
	ex.Backends = map[string]execbackend.Backend{"openai": seamTestBackend(nil)}
	ex.Rand = routing.NewSeededRng(1)
	mk := func(text string) *aipapi.Call {
		return &aipapi.Call{
			Route: aipapi.RouteIntent{Selector: "openai:gpt-4"},
			Messages: []aipapi.Message{{
				Role:  aipapi.RoleUser,
				Parts: []aipapi.Part{aipapi.TextPart(text)},
			}},
		}
	}
	for _, text := range []string{"first", "second"} {
		stream, err := ex.Execute(context.Background(), mk(text))
		if err != nil {
			t.Fatal(err)
		}
		_, _ = aipapi.Collect(context.Background(), stream)
	}
	if len(obs.events) != 2 {
		t.Fatalf("usage events %d want 2", len(obs.events))
	}
	if len(obs.events[0].SeamDigests) != 1 || len(obs.events[1].SeamDigests) != 1 {
		t.Fatalf("digests=%d,%d want 1,1", len(obs.events[0].SeamDigests), len(obs.events[1].SeamDigests))
	}
	if obs.events[0].SeamDigests[0] == obs.events[1].SeamDigests[0] {
		t.Fatal("different requests must feed different digests")
	}
}

// pinSessionObserver pins the session key for the collapse detector.
// TestExecutor mints a fresh secure-session record per request without a
// pinned continuity store, so six independent requests would land on six
// detector keys and never form an episode. Production conversations carry a
// stable session; pinning simulates that while keeping the executor's real
// B-leg IDs, token counts, and per-leg digest vectors intact.
type pinSessionObserver struct {
	inner   usage.Observer
	session string
}

func (p pinSessionObserver) OnUsage(ctx context.Context, ev usage.Event) error {
	ev.SessionID = p.session
	return p.inner.OnUsage(ctx, ev)
}

// TestExecutor_SeamDigestsEndToEndCollapse connects the real executor usage
// events to the real collapse diagnostic: one healthy-shaped leg followed by
// five cold-shaped legs with a persistently mutated prompt trips exactly one
// episode reporting the first divergent item index, without leaking content.
func TestExecutor_SeamDigestsEndToEndCollapse(t *testing.T) {
	t.Parallel()
	var logs bytes.Buffer
	reg := prometheus.NewRegistry()
	detector := metrics.NewPromptCacheCollapseWithSettings(
		metrics.RegisterPromptCacheCollapseProm(reg),
		slog.New(slog.NewJSONHandler(&logs, nil)),
		nil,
		metrics.PromptCacheCollapseSettings{MinPromptTokens: 1},
	)
	pinned := pinSessionObserver{inner: detector, session: "e2e-seam-session"}
	var calls atomic.Int64
	ex := func() *runtime.Executor {
		st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
		if err != nil {
			t.Fatal(err)
		}
		bus := hooks.New(hooks.Config{})
		planes := aipfeature.NewContributionSet()
		snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{UsageObserver: pinned, FeaturePlanes: planes.Freeze()})
		e := runtime.TestExecutor()
		e.Store = st
		e.Bus = bus
		e.RuntimeSnapshot = snap
		e.SeamDigestsEnabled = true
		e.SeamDigestKey = seamExecutorTestKey()
		e.Backends = map[string]execbackend.Backend{
			"openai": {
				Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
				Open: func(_ context.Context, _ aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
					n := calls.Add(1)
					cacheRead := 0
					if n == 1 {
						cacheRead = 10
					}
					return aipapi.NewFixedEventStream([]aipapi.Event{
						{Kind: aipapi.EventUsageDelta, InputTokens: 10, CacheReadTokens: cacheRead},
						{Kind: aipapi.EventResponseFinished},
					}), nil
				},
			},
		}
		e.Rand = routing.NewSeededRng(1)
		return e
	}()
	mk := func(second string) *aipapi.Call {
		return &aipapi.Call{
			Route: aipapi.RouteIntent{Selector: "openai:gpt-4"},
			Messages: []aipapi.Message{
				{Role: aipapi.RoleSystem, Parts: []aipapi.Part{aipapi.TextPart("sys")}},
				{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart(second)}},
			},
		}
	}
	texts := []string{"user", "ELIDED-tool-output", "ELIDED-tool-output", "ELIDED-tool-output", "ELIDED-tool-output", "ELIDED-tool-output"}
	for i, text := range texts {
		stream, err := ex.Execute(context.Background(), mk(text))
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		_, _ = aipapi.Collect(context.Background(), stream)
	}
	var trips float64
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range families {
		if f.GetName() == "aip_prompt_cache_collapse_total" {
			for _, m := range f.GetMetric() {
				trips += m.GetCounter().GetValue()
			}
		}
	}
	if trips != 1 {
		t.Fatalf("trips=%v want 1", trips)
	}
	text := logs.String()
	if !strings.Contains(text, `"seam_item_index":1`) {
		t.Fatalf("missing seam_item_index 1 in %s", text)
	}
	for _, leaked := range []string{"ELIDED-tool-output", "e2e-seam-session", "sys", "user"} {
		if strings.Contains(text, leaked) {
			t.Fatalf("diagnostic leaked %q in %s", leaked, text)
		}
	}
	if n := calls.Load(); n != 6 {
		t.Fatalf("backend opens=%d want 6", n)
	}
}

// TestExecutor_SeamDigestsStableAcrossRebuiltExecutors proves the
// process→generation boundary: two executors rebuilt from the same borrowed
// process key hash the same prompt to equal digests, so a generation reload
// never shifts attribution. A per-generation key would fail this test.
func TestExecutor_SeamDigestsStableAcrossRebuiltExecutors(t *testing.T) {
	t.Parallel()
	key := seamExecutorTestKey()
	run := func() [][32]byte {
		st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
		if err != nil {
			t.Fatal(err)
		}
		obs := &captureUsage{}
		bus := hooks.New(hooks.Config{})
		planes := aipfeature.NewContributionSet()
		snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{UsageObserver: obs, FeaturePlanes: planes.Freeze()})
		ex := runtime.TestExecutor()
		ex.Bus = bus
		ex.RuntimeSnapshot = snap
		ex.SeamDigestsEnabled = true
		ex.SeamDigestKey = key
		ex.Store = st
		ex.Backends = map[string]execbackend.Backend{"openai": seamTestBackend(nil)}
		ex.Rand = routing.NewSeededRng(1)
		stream, err := ex.Execute(context.Background(), seamTestCall())
		if err != nil {
			t.Fatal(err)
		}
		_, _ = aipapi.Collect(context.Background(), stream)
		if len(obs.events) != 1 {
			t.Fatalf("usage events %d want 1", len(obs.events))
		}
		return obs.events[0].SeamDigests
	}
	first, second := run(), run()
	if len(first) != 1 || len(second) != 1 {
		t.Fatalf("digests=%d,%d want 1,1", len(first), len(second))
	}
	if first[0] != second[0] {
		t.Fatal("same prompt under the same borrowed process key must yield equal digests across rebuilt executors")
	}
}

// TestExecutor_SeamDigestsKeyedAcrossKeys proves key separation at the
// executor boundary: the same prompt under different process keys yields
// different digests, so digests from one process cannot be matched with a
// dictionary built under another key.
func TestExecutor_SeamDigestsKeyedAcrossKeys(t *testing.T) {
	t.Parallel()
	run := func(key runtime.SeamDigestKey) [][32]byte {
		st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
		if err != nil {
			t.Fatal(err)
		}
		obs := &captureUsage{}
		bus := hooks.New(hooks.Config{})
		planes := aipfeature.NewContributionSet()
		snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{UsageObserver: obs, FeaturePlanes: planes.Freeze()})
		ex := runtime.TestExecutor()
		ex.Bus = bus
		ex.RuntimeSnapshot = snap
		ex.SeamDigestsEnabled = true
		ex.SeamDigestKey = key
		ex.Store = st
		ex.Backends = map[string]execbackend.Backend{"openai": seamTestBackend(nil)}
		ex.Rand = routing.NewSeededRng(1)
		stream, err := ex.Execute(context.Background(), seamTestCall())
		if err != nil {
			t.Fatal(err)
		}
		_, _ = aipapi.Collect(context.Background(), stream)
		if len(obs.events) != 1 {
			t.Fatalf("usage events %d want 1", len(obs.events))
		}
		return obs.events[0].SeamDigests
	}
	var other runtime.SeamDigestKey
	for i := range other {
		other[i] = 0xA5
	}
	a, b := run(seamExecutorTestKey()), run(other)
	if len(a) != 1 || len(b) != 1 {
		t.Fatalf("digests=%d,%d want 1,1", len(a), len(b))
	}
	if a[0] == b[0] {
		t.Fatal("same prompt under different process keys must yield different digests")
	}
}

// TestExecutor_SeamDigestsDoNotLeakKey proves key secrecy at every emission
// boundary the diagnostic owns. It drives a real collapse episode (one
// healthy-shaped leg plus five cold-shaped legs with a mutated prompt), so
// the warning below is actually emitted — an empty log cannot pass. It then
// asserts that neither the process key nor any emitted digest appears in:
// the diagnostic logs (hex and raw encodings), the SessionCacheHealth read
// model snapshot, or the Prometheus exposition. The detector retains digest
// vectors in memory only (dropped on eviction, never written to a store),
// and the billing journal carries no digest field, so these three surfaces
// plus the in-memory-only retention are the complete emission boundary.
func TestExecutor_SeamDigestsDoNotLeakKey(t *testing.T) {
	t.Parallel()
	var logs bytes.Buffer
	reg := prometheus.NewRegistry()
	detector := metrics.NewPromptCacheCollapseWithSettings(
		metrics.RegisterPromptCacheCollapseProm(reg),
		slog.New(slog.NewJSONHandler(&logs, nil)),
		nil,
		metrics.PromptCacheCollapseSettings{MinPromptTokens: 1},
	)
	key := seamExecutorTestKey()
	const session = "key-secrecy-session"
	fed := &captureSeamDigests{}
	pinned := pinSessionObserver{inner: usage.ChainObservers(fed, detector), session: session}
	var calls atomic.Int64
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	bus := hooks.New(hooks.Config{})
	planes := aipfeature.NewContributionSet()
	snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{UsageObserver: pinned, FeaturePlanes: planes.Freeze()})
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = bus
	ex.RuntimeSnapshot = snap
	ex.SeamDigestsEnabled = true
	ex.SeamDigestKey = key
	ex.Backends = map[string]execbackend.Backend{
		"openai": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(_ context.Context, _ aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				n := calls.Add(1)
				cacheRead := 0
				if n == 1 {
					cacheRead = 10
				}
				return aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventUsageDelta, InputTokens: 10, CacheReadTokens: cacheRead},
					{Kind: aipapi.EventResponseFinished},
				}), nil
			},
		},
	}
	ex.Rand = routing.NewSeededRng(1)
	mk := func(second string) *aipapi.Call {
		return &aipapi.Call{
			Route: aipapi.RouteIntent{Selector: "openai:gpt-4"},
			Messages: []aipapi.Message{
				{Role: aipapi.RoleSystem, Parts: []aipapi.Part{aipapi.TextPart("sys")}},
				{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart(second)}},
			},
		}
	}
	texts := []string{"user", "MUTATED-tool-output", "MUTATED-tool-output", "MUTATED-tool-output", "MUTATED-tool-output", "MUTATED-tool-output"}
	for i, text := range texts {
		stream, err := ex.Execute(context.Background(), mk(text))
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		_, _ = aipapi.Collect(context.Background(), stream)
	}
	if n := calls.Load(); n != 6 {
		t.Fatalf("backend opens=%d want 6", n)
	}
	// The episode must actually trip: without a warning the log assertions
	// below would pass vacuously on an empty log.
	var trips float64
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range families {
		if f.GetName() == "aip_prompt_cache_collapse_total" {
			for _, m := range f.GetMetric() {
				trips += m.GetCounter().GetValue()
			}
		}
	}
	if trips != 1 {
		t.Fatalf("trips=%v want 1: no collapse episode, secrecy assertions would be vacuous", trips)
	}
	text := logs.String()
	if !strings.Contains(text, "prompt-cache collapse") {
		t.Fatalf("missing collapse warning in %s", text)
	}
	if !strings.Contains(text, `"seam_item_index":1`) {
		t.Fatalf("missing seam attribution in %s", text)
	}
	if len(fed.vectors) != 6 {
		t.Fatalf("fed vectors=%d want 6: the test must observe the emitted digests it claims are secret", len(fed.vectors))
	}
	// Secret material in every encoding a text log, JSON snapshot, or
	// exposition could carry: key hex/raw and every emitted digest hex/raw.
	secrets := []string{hex.EncodeToString(key[:]), string(key[:])}
	for _, vec := range fed.vectors {
		for _, dg := range vec {
			secrets = append(secrets, hex.EncodeToString(dg[:]), string(dg[:]))
		}
	}
	secrets = append(secrets, "MUTATED-tool-output", session, "sys", "user")
	assertNoSecret := func(name, haystack string) {
		t.Helper()
		for _, secret := range secrets {
			if secret != "" && strings.Contains(haystack, secret) {
				t.Fatalf("%s leaks %q", name, secret)
			}
		}
	}
	assertNoSecret("diagnostic log", text)
	// Read-model boundary: the health snapshot carries status enums only.
	health, err := json.Marshal(detector.SessionCacheHealth(session))
	if err != nil {
		t.Fatal(err)
	}
	assertNoSecret("read-model snapshot", string(health))
	// Metrics boundary: the episode counter carries backend/model labels only.
	var exposition bytes.Buffer
	for _, f := range families {
		for _, m := range f.GetMetric() {
			labels := []string{}
			for _, lp := range m.GetLabel() {
				labels = append(labels, lp.GetValue())
			}
			exposition.WriteString(strings.Join(labels, ","))
		}
	}
	assertNoSecret("metric exposition", exposition.String())
}
