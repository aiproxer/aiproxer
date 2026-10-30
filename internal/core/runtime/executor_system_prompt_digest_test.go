package runtime_test

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/featurebundle"
	"github.com/aiproxer/aiproxer/internal/infra/metrics"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/usage"
	"github.com/prometheus/client_golang/prometheus"
)

// promptDigestExecutor mirrors seamTestExecutor with the instruction-identity
// feed wired instead of the per-item seam feed: the same real executor, real
// backend open, and real usage emission.
func promptDigestExecutor(obs usage.Observer, enabled bool) *runtime.Executor {
	bus := hooks.New(hooks.Config{})
	planes := aipfeature.NewContributionSet()
	snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{UsageObserver: obs, FeaturePlanes: planes.Freeze()})
	ex := runtime.TestExecutor()
	ex.Bus = bus
	ex.RuntimeSnapshot = snap
	ex.SystemPromptDigestEnabled = enabled
	// The process key is present even while the gate is off. Otherwise the
	// zero-key check, not the gate, would suppress the feed, and the gate-off
	// claim would stay green under a mutant that removes only the gate.
	ex.SystemPromptDigestKey = seamExecutorTestKey()
	return ex
}

func promptDigestCall(system, user string) *aipapi.Call {
	return &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "openai:gpt-4"},
		Instructions: []aipapi.Message{{
			Role:  aipapi.RoleSystem,
			Parts: []aipapi.Part{aipapi.TextPart(system)},
		}},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart(user)},
		}},
	}
}

// TestNewExecutor_ZeroKeyDisablesSystemPromptDigest proves fail-closed
// composition: an enabled instruction feed without the process-stable key
// would publish publicly reproducible fingerprints, so the constructor refuses
// the feed instead of running keyless.
func TestNewExecutor_ZeroKeyDisablesSystemPromptDigest(t *testing.T) {
	t.Parallel()
	keyless := runtime.NewExecutor(runtime.ExecutorConfig{
		Observability: runtime.ObservabilityRuntime{SystemPromptDigestEnabled: true},
	})
	if keyless.SystemPromptDigestEnabled {
		t.Fatal("NewExecutor must refuse a keyless instruction-identity feed")
	}
	keyed := runtime.NewExecutor(runtime.ExecutorConfig{
		Observability: runtime.ObservabilityRuntime{SystemPromptDigestEnabled: true, SystemPromptDigestKey: seamExecutorTestKey()},
	})
	if !keyed.SystemPromptDigestEnabled {
		t.Fatal("NewExecutor must keep a keyed instruction-identity feed armed")
	}
}

// TestExecutor_SystemPromptDigestDisabledEmitsNoIdentity proves the cost gate:
// with the feed off the usage sample carries no identity, so the detector never
// compares instruction prefixes it was not asked to hash.
func TestExecutor_SystemPromptDigestDisabledEmitsNoIdentity(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	obs := &captureUsage{}
	ex := promptDigestExecutor(obs, false)
	if ex.SystemPromptDigestKey.IsZero() {
		t.Fatal("fixture must keep the process key so the gate is the only suppressor")
	}
	ex.Store = st
	ex.Backends = map[string]execbackend.Backend{"openai": seamTestBackend(nil)}
	ex.Rand = routing.NewSeededRng(1)
	stream, err := ex.Execute(context.Background(), promptDigestCall("be terse", "hi"))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = aipapi.Collect(context.Background(), stream)
	if len(obs.events) != 1 {
		t.Fatalf("usage events %d want 1", len(obs.events))
	}
	if obs.events[0].SystemPromptDigest != ([32]byte{}) {
		t.Fatal("a disabled feed must emit no instruction identity")
	}
}

// TestExecutor_SystemPromptDigestFeedsProviderBoundInstructions proves the
// feed: every leg carries one identity of the request the backend actually
// received, it is stable while the instructions are unchanged, and it changes
// when the provider-bound instruction prefix changes.
func TestExecutor_SystemPromptDigestFeedsProviderBoundInstructions(t *testing.T) {
	t.Parallel()
	run := func(call *aipapi.Call) [32]byte {
		st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
		if err != nil {
			t.Fatal(err)
		}
		obs := &captureUsage{}
		ex := promptDigestExecutor(obs, true)
		ex.Store = st
		ex.Backends = map[string]execbackend.Backend{"openai": seamTestBackend(nil)}
		ex.Rand = routing.NewSeededRng(1)
		stream, err := ex.Execute(context.Background(), call)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = aipapi.Collect(context.Background(), stream)
		if len(obs.events) != 1 {
			t.Fatalf("usage events %d want 1", len(obs.events))
		}
		return obs.events[0].SystemPromptDigest
	}
	first := run(promptDigestCall("be terse", "hi"))
	second := run(promptDigestCall("be terse", "a different user turn"))
	changed := run(promptDigestCall("be verbose", "hi"))
	var none [32]byte
	if first == none {
		t.Fatal("an enabled feed must publish a non-zero instruction identity")
	}
	if first != second {
		t.Fatal("unchanged instructions must keep the same identity across legs")
	}
	if first == changed {
		t.Fatal("a different provider-bound instruction must change the identity")
	}
}

// captureSystemPromptDigests records the content-free identities the executor
// publishes, so the end-to-end secrecy test can prove they never reach the
// diagnostic's log, read model, or metric exposition.
type captureSystemPromptDigests struct {
	digests   [][32]byte
	launchSeq []uint64
}

func (c *captureSystemPromptDigests) OnUsage(_ context.Context, ev usage.Event) error {
	var none [32]byte
	if ev.SystemPromptDigest != none {
		c.digests = append(c.digests, ev.SystemPromptDigest)
		c.launchSeq = append(c.launchSeq, ev.PromptCacheLaunchSeq)
	}
	return nil
}

// TestExecutor_SystemPromptDigestEndToEndCollapse connects the real executor to
// the real collapse diagnostic: one healthy leg with one instruction prefix,
// then five cold legs with a changed prefix trip one episode attributed to
// system-prompt, and neither the instruction text nor the keyed fingerprint
// reaches the diagnostic's emission boundaries.
func TestExecutor_SystemPromptDigestEndToEndCollapse(t *testing.T) {
	t.Parallel()
	var logs bytes.Buffer
	reg := prometheus.NewRegistry()
	detector := metrics.NewPromptCacheCollapseWithSettings(
		metrics.RegisterPromptCacheCollapseProm(reg),
		slog.New(slog.NewJSONHandler(&logs, nil)),
		nil,
		metrics.PromptCacheCollapseSettings{MinPromptTokens: 1},
	)
	fingerprints := &captureSystemPromptDigests{}
	const session = "e2e-instruction-session"
	pinned := pinSessionObserver{inner: usage.ChainObservers(fingerprints, detector), session: session}
	var calls atomic.Int64
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ex := promptDigestExecutor(pinned, true)
	ex.Store = st
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
	systems := []string{"STEERED-SYSTEM-PROMPT-A", "STEERED-SYSTEM-PROMPT-B", "STEERED-SYSTEM-PROMPT-B", "STEERED-SYSTEM-PROMPT-B", "STEERED-SYSTEM-PROMPT-B", "STEERED-SYSTEM-PROMPT-B"}
	for i, system := range systems {
		stream, err := ex.Execute(context.Background(), promptDigestCall(system, "hi"))
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		_, _ = aipapi.Collect(context.Background(), stream)
	}
	if n := calls.Load(); n != 6 {
		t.Fatalf("backend opens=%d want 6", n)
	}
	var trips float64
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range families {
		if family.GetName() == "aip_prompt_cache_collapse_total" {
			for _, metric := range family.GetMetric() {
				trips += metric.GetCounter().GetValue()
			}
		}
	}
	if trips != 1 {
		t.Fatalf("trips=%v want 1", trips)
	}
	text := logs.String()
	if !strings.Contains(text, `"cause":"system-prompt"`) {
		t.Fatalf("cross-turn instruction change was not attributed in %s", text)
	}
	// The fingerprint is process-stable diagnostic evidence: the diagnostic's
	// log, read model and metric exposition must never carry it. The capture
	// proves observations were actually emitted, so the leak assertions are
	// not vacuous.
	if len(fingerprints.digests) == 0 {
		t.Fatal("no instruction identity was emitted; secrecy assertions would be vacuous")
	}
	for i, seq := range fingerprints.launchSeq {
		if seq == 0 {
			t.Fatalf("instruction identity %d was emitted without a launch ordinal", i)
		}
		if i > 0 && seq <= fingerprints.launchSeq[i-1] {
			t.Fatalf("launch ordinals must increase with request order: %v", fingerprints.launchSeq)
		}
	}
	fingerprintHex := make([]string, 0, len(fingerprints.digests))
	for _, digest := range fingerprints.digests {
		fingerprintHex = append(fingerprintHex, hex.EncodeToString(digest[:]))
	}
	health, err := json.Marshal(detector.SessionCacheHealth(session))
	if err != nil {
		t.Fatal(err)
	}
	var exposition bytes.Buffer
	for _, family := range families {
		for _, metric := range family.GetMetric() {
			for _, label := range metric.GetLabel() {
				exposition.WriteString(label.GetValue())
			}
		}
	}
	for _, leaked := range append([]string{"STEERED-SYSTEM-PROMPT", session}, fingerprintHex...) {
		for name, haystack := range map[string]string{
			"log":        text,
			"read model": string(health),
			"exposition": exposition.String(),
		} {
			if strings.Contains(haystack, leaked) {
				t.Fatalf("%s leaks %q", name, leaked)
			}
		}
	}
}

const promptDigestCandidateMarker = "CANDIDATE-ONLY-SYSTEM-INSTRUCTION"

// conditionalSystemInstructionTransform appends a system instruction only while
// armed. It models a candidate-scoped transform (for example the interleaved
// thinker prefix) that changes the provider-bound instruction prefix without
// changing the request baseline.
type conditionalSystemInstructionTransform struct {
	enabled atomic.Bool
	marker  string
}

func (t *conditionalSystemInstructionTransform) ID() string { return "conditional-system-instruction" }
func (t *conditionalSystemInstructionTransform) Order() int { return 0 }
func (t *conditionalSystemInstructionTransform) FailureMode() sdkhooks.FailureMode {
	return sdkhooks.FailClosed
}

func (t *conditionalSystemInstructionTransform) HandleAttempt(_ context.Context, call *aipapi.Call, _ request.AttemptMeta, _ request.Services) (request.AttemptDecision, error) {
	if t.enabled.Load() && call != nil {
		// Mirrors the interleaved thinker shaping: the candidate-specific
		// instruction is prepended without touching the request baseline.
		call.Instructions = append([]aipapi.Message{{
			Role:  aipapi.RoleSystem,
			Parts: []aipapi.Part{aipapi.TextPart(t.marker)},
		}}, call.Instructions...)
	}
	return request.AttemptDecision{Kind: request.AttemptContinue}, nil
}

// promptDigestExecutorWithAttemptTransforms wires an attempt transform into the
// same real executor and usage surface as promptDigestExecutor.
func promptDigestExecutorWithAttemptTransforms(t *testing.T, obs usage.Observer, xforms ...request.AttemptTransform) *runtime.Executor {
	t.Helper()
	cs := aipfeature.NewContributionSet()
	if err := aipfeature.Contribute(cs, aipfeature.PlaneAttemptTransforms, "identity-xform", xforms); err != nil {
		t.Fatalf("Contribute: %v", err)
	}
	bundle := aipfeature.BundleFromPlanes(cs.Freeze(), nil)
	if err := bundle.Validate(); err != nil {
		t.Fatalf("FeatureBundle.Validate: %v", err)
	}
	gen, err := featurebundle.MergeBundlesGenerated(bundle)
	if err != nil {
		t.Fatalf("MergeBundlesGenerated: %v", err)
	}
	bus := hooks.New(hooks.Config{})
	snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		UsageObserver: obs,
		FeaturePlanes: gen.Frozen,
	})
	ex := runtime.TestExecutor()
	ex.Bus = bus
	ex.RuntimeSnapshot = snap
	ex.SystemPromptDigestEnabled = true
	ex.SystemPromptDigestKey = seamExecutorTestKey()
	return ex
}

// TestExecutor_SystemPromptDigestTracksCandidateOnlySystemInstruction proves the
// identity is derived from the call the backend actually receives, not from the
// pre-candidate request baseline. A conditional candidate transform appends a
// system instruction while the baseline stays byte-identical: the emitted
// identity must change, must be stable while the transform keeps applying, and
// must equal the identity of an otherwise identical call that carries the
// instruction directly. The captured backend call proves the transform really
// reached the provider boundary.
func TestExecutor_SystemPromptDigestTracksCandidateOnlySystemInstruction(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	obs := &captureUsage{}
	xform := &conditionalSystemInstructionTransform{marker: promptDigestCandidateMarker}
	ex := promptDigestExecutorWithAttemptTransforms(t, obs, xform)
	ex.Store = st
	var openedMu sync.Mutex
	var opened []aipapi.Call
	ex.Backends = map[string]execbackend.Backend{
		"openai": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(_ context.Context, call aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				openedMu.Lock()
				opened = append(opened, call)
				openedMu.Unlock()
				return aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventUsageDelta, InputTokens: 3},
					{Kind: aipapi.EventResponseFinished},
				}), nil
			},
		},
	}
	ex.Rand = routing.NewSeededRng(1)
	run := func(call *aipapi.Call) [32]byte {
		before := len(obs.events)
		stream, execErr := ex.Execute(context.Background(), call)
		if execErr != nil {
			t.Fatalf("execute: %v", execErr)
		}
		if _, collectErr := aipapi.Collect(context.Background(), stream); collectErr != nil {
			t.Fatalf("collect: %v", collectErr)
		}
		if len(obs.events) != before+1 {
			t.Fatalf("usage events %d want %d", len(obs.events), before+1)
		}
		return obs.events[before].SystemPromptDigest
	}

	baseline := run(promptDigestCall("be terse", "hi"))
	xform.enabled.Store(true)
	transformed := run(promptDigestCall("be terse", "hi"))
	transformedAgain := run(promptDigestCall("be terse", "hi"))
	xform.enabled.Store(false)
	direct := run(&aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "openai:gpt-4"},
		Instructions: []aipapi.Message{
			{Role: aipapi.RoleSystem, Parts: []aipapi.Part{aipapi.TextPart(promptDigestCandidateMarker)}},
			{Role: aipapi.RoleSystem, Parts: []aipapi.Part{aipapi.TextPart("be terse")}},
		},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	})

	if baseline == transformed {
		t.Fatal("a candidate-only system instruction did not change the emitted identity")
	}
	if transformed != transformedAgain {
		t.Fatal("a stable candidate-only instruction must keep the emitted identity stable")
	}
	if direct != transformed {
		t.Fatal("the emitted identity does not match the instructions the backend received")
	}
	openedMu.Lock()
	defer openedMu.Unlock()
	if len(opened) != 4 {
		t.Fatalf("backend opens=%d want 4", len(opened))
	}
	hasMarker := func(call aipapi.Call) bool {
		for _, msg := range call.Instructions {
			if strings.Contains(textOf(msg), promptDigestCandidateMarker) {
				return true
			}
		}
		return false
	}
	if hasMarker(opened[0]) {
		t.Fatal("the baseline request must not reach the backend with the candidate instruction")
	}
	for i := 1; i < len(opened); i++ {
		if !hasMarker(opened[i]) {
			t.Fatalf("request %d: backend call is missing the candidate system instruction", i)
		}
	}
}

// TestExecutor_SystemPromptLaunchSeqSharedAcrossExecutors proves the executor
// consumes the injected process-owned sequence rather than a private counter:
// requests through two executors sharing one sequence emit strictly increasing
// ordinals. A per-executor counter would repeat values after a generation
// rebuild.
func TestExecutor_SystemPromptLaunchSeqSharedAcrossExecutors(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	seq := runtime.NewRequestLaunchSequence()
	fingerprints := &captureSystemPromptDigests{}
	newExec := func(seed int64) *runtime.Executor {
		ex := promptDigestExecutor(fingerprints, true)
		ex.SystemPromptLaunchSeq = seq
		ex.Store = st
		ex.Backends = map[string]execbackend.Backend{"openai": seamTestBackend(nil)}
		ex.Rand = routing.NewSeededRng(seed)
		return ex
	}
	first, second := newExec(1), newExec(2)
	for i, ex := range []*runtime.Executor{first, second, first} {
		stream, err := ex.Execute(context.Background(), promptDigestCall("be terse", "turn "+string(rune('a'+i))))
		if err != nil {
			t.Fatalf("Execute %d: %v", i, err)
		}
		_, _ = aipapi.Collect(context.Background(), stream)
	}
	if len(fingerprints.launchSeq) != 3 {
		t.Fatalf("instruction identities=%d want 3", len(fingerprints.launchSeq))
	}
	for i, got := range fingerprints.launchSeq {
		if want := uint64(i + 1); got != want {
			t.Fatalf("launch ordinals=%v want [1 2 3]", fingerprints.launchSeq)
		}
	}
}
