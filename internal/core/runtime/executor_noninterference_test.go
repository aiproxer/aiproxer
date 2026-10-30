package runtime_test

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/aiproxer/aiproxer/internal/testkit"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/plugins/features/interleavedthinking"
	"github.com/aiproxer/aiproxer/internal/standardplugins/featurehost"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/prerequest"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
)

type niRtx struct {
	prefix string
}

func (r niRtx) ID() string                        { return "ni-rtx" }
func (r niRtx) Order() int                        { return 10 }
func (r niRtx) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }
func (r niRtx) Handle(_ context.Context, call *aipapi.Call, _ request.RequestMeta, _ request.Services) error {
	if r.prefix == "" {
		return nil
	}
	call.Messages[0].Parts[0].Text = r.prefix + call.Messages[0].Parts[0].Text
	return nil
}

type niPreReq struct {
	prefix string
}

func (p niPreReq) ID() string                        { return "ni-pre" }
func (p niPreReq) Order() int                        { return 20 }
func (p niPreReq) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }
func (p niPreReq) Handle(_ context.Context, call *aipapi.Call, _ prerequest.Meta, _ prerequest.Services) (prerequest.Decision, error) {
	if p.prefix != "" {
		call.Messages[0].Parts[0].Text = p.prefix + call.Messages[0].Parts[0].Text
	}
	return prerequest.Decision{}, nil
}

func nonInterferenceExecutor(t *testing.T, backends map[string]execbackend.Backend, interleavedEnabled bool) (*runtime.Executor, *b2bua.MemoryStore) {
	t.Helper()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(0)
	ex.Backends = backends
	if interleavedEnabled {
		proc, err := interleavedthinking.NewProcessor(interleavedthinking.Config{
			Enabled:      true,
			Instructions: "Think step by step.",
		}, interleavedthinking.NewMemoStore(4096))
		if err != nil {
			t.Fatal(err)
		}
		ex.InterleavedProcessor = featurehost.NewInterleavedProcessorAdapter(proc)
	}
	return ex, st
}

func nonInterferenceSecureExecutor(t *testing.T, backends map[string]execbackend.Backend, interleavedEnabled bool) (*runtime.Executor, *b2bua.MemoryStore) {
	t.Helper()
	ex, st := interleavedSecureExecutor(t, backends)
	if !interleavedEnabled {
		ex.InterleavedProcessor = nil
	}
	snap := extensions.NewRequestRuntimeSnapshot(ex.Bus, extensions.SnapshotOptions{
		Workspace: voidWorkspaceResolver{},
		FeaturePlanes: testkit.FreezeTestBundle(testkit.TestFeatureBundle{
			RequestTransforms:  []request.Transform{niRtx{prefix: "rtx:"}},
			PreRequestHandlers: []prerequest.Handler{niPreReq{prefix: "pre:"}},
		}),
	})
	ex.RuntimeSnapshot = snap
	return ex, st
}

func niTextStream() aipapi.ManagedEventStream {
	return aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventTextDelta, Delta: "ok"},
		{Kind: aipapi.EventResponseFinished},
	})
}

func niUserText(c aipapi.Call) string {
	if len(c.Messages) == 0 {
		return ""
	}
	return textOf(c.Messages[0])
}

func TestExecutor_DisabledInterleavedPreservesRouteKinds(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		selector   string
		rngSeed    int64
		wantOpens  []string
		firstOnly  bool
		continuity string
	}{
		{
			name:      "weighted",
			selector:  "[weight=1]a:m^[weight=1]b:m",
			rngSeed:   0,
			wantOpens: []string{"a"},
		},
		{
			name:      "failover",
			selector:  "a:m|b:m",
			wantOpens: []string{"a"},
		},
		{
			name:      "parallel",
			selector:  "a:m!b:m",
			wantOpens: nil, // parallel race opens are concurrent; assert membership below
		},
		{
			name:       "first",
			selector:   "[first]cheap:m^[weight=100]expensive:m",
			rngSeed:    99,
			wantOpens:  []string{"cheap"},
			firstOnly:  true,
			continuity: "ni-first-disabled",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var opensMu sync.Mutex
			var opened []string
			record := func(id string) func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
					opensMu.Lock()
					opened = append(opened, id)
					opensMu.Unlock()
					return niTextStream(), nil
				}
			}
			caps := aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityTools)
			backends := map[string]execbackend.Backend{
				"a": {Caps: caps, Open: record("a")},
				"b": {Caps: caps, Open: record("b")},
				"cheap": {
					Caps: caps,
					Open: record("cheap"),
				},
				"expensive": {
					Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
					Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
						t.Fatal("expensive backend must not open when [first] cheap arm succeeds")
						return nil, nil
					},
				},
			}
			ex, st := nonInterferenceExecutor(t, backends, false)
			if tc.rngSeed != 0 {
				ex.Rand = routing.NewSeededRng(tc.rngSeed)
			}
			call := &aipapi.Call{
				Session: aipapi.SessionRef{ContinuityKey: tc.continuity},
				Route:   aipapi.RouteIntent{Selector: tc.selector},
				Messages: []aipapi.Message{{
					Role:  aipapi.RoleUser,
					Parts: []aipapi.Part{aipapi.TextPart("hi")},
				}},
			}
			stream, err := ex.Execute(context.Background(), call)
			if err != nil {
				t.Fatalf("execute: %v", err)
			}
			if _, err := aipapi.Collect(context.Background(), stream); err != nil {
				t.Fatalf("collect: %v", err)
			}
			opensMu.Lock()
			got := append([]string(nil), opened...)
			opensMu.Unlock()
			if tc.name == "parallel" {
				if len(got) == 0 {
					t.Fatal("parallel selector must open at least one arm")
				}
				for _, id := range got {
					if id != "a" && id != "b" {
						t.Fatalf("parallel open %q unexpected", id)
					}
				}
			} else {
				if len(got) != len(tc.wantOpens) {
					t.Fatalf("opens: got %v want %v", got, tc.wantOpens)
				}
				for i, want := range tc.wantOpens {
					if got[i] != want {
						t.Fatalf("open[%d]: got %q want %q (all=%v)", i, got[i], want, got)
					}
				}
			}
			if tc.firstOnly {
				aleg, err := st.FetchALeg(context.Background(), call.Session.ALegID)
				if err != nil {
					t.Fatal(err)
				}
				if !aleg.WeightedFirstConsumed {
					t.Fatal("[first] must persist WeightedFirstConsumed with disabled interleaved")
				}
			}
			state, err := st.FetchInterleavedState(context.Background(), call.Session.ALegID)
			if err != nil {
				t.Fatalf("fetch interleaved state: %v", err)
			}
			if !state.IsEmpty() {
				t.Fatalf("disabled interleaved must not persist state, got %+v", state)
			}
		})
	}
}

func TestExecutor_EnabledInterleavedNonThinkerRoutesPreserveBehavior(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		selector  string
		wantOpens []string
	}{
		{name: "weighted", selector: "[weight=1]a:m^[weight=1]b:m", wantOpens: []string{"a"}},
		{name: "failover", selector: "a:m|b:m", wantOpens: []string{"a"}},
		{name: "parallel", selector: "a:m!b:m", wantOpens: nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var opensMu sync.Mutex
			var opened []string
			record := func(id string) func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return func(_ context.Context, call aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
					opensMu.Lock()
					opened = append(opened, id)
					opensMu.Unlock()
					if len(call.Tools) == 0 {
						t.Fatal("non-thinker route must keep tools")
					}
					if len(call.Instructions) != 0 {
						t.Fatalf("non-thinker route must not prepend thinker instructions")
					}
					return niTextStream(), nil
				}
			}
			caps := aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityTools)
			backends := map[string]execbackend.Backend{
				"a": {Caps: caps, Open: record("a")},
				"b": {Caps: caps, Open: record("b")},
			}
			ex, st := nonInterferenceExecutor(t, backends, true)
			call := interleavedBaseCall(tc.selector)
			stream, err := ex.Execute(context.Background(), call)
			if err != nil {
				t.Fatalf("execute: %v", err)
			}
			if _, err := aipapi.Collect(context.Background(), stream); err != nil {
				t.Fatalf("collect: %v", err)
			}
			opensMu.Lock()
			got := append([]string(nil), opened...)
			opensMu.Unlock()
			if tc.name == "parallel" {
				if len(got) == 0 {
					t.Fatal("parallel selector must open at least one arm")
				}
				for _, id := range got {
					if id != "a" && id != "b" {
						t.Fatalf("parallel open %q unexpected", id)
					}
				}
			} else {
				if len(got) != len(tc.wantOpens) {
					t.Fatalf("opens: got %v want %v", got, tc.wantOpens)
				}
				for i, want := range tc.wantOpens {
					if got[i] != want {
						t.Fatalf("open[%d]: got %q want %q", i, got[i], want)
					}
				}
			}
			state, err := st.FetchInterleavedState(context.Background(), call.Session.ALegID)
			if err != nil {
				t.Fatalf("fetch interleaved state: %v", err)
			}
			if !state.IsEmpty() {
				t.Fatalf("non-thinker selector must not persist interleaved state, got %+v", state)
			}
		})
	}
}

func TestExecutor_InterleavedExtensionOrdering_transformBeforeThinkerShaping(t *testing.T) {
	t.Parallel()
	caps := aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityTools)
	var gotMu sync.Mutex
	var gotCall aipapi.Call
	var thinkerOpens int
	captureThinker := func(c aipapi.Call) {
		gotMu.Lock()
		gotCall = c
		thinkerOpens++
		gotMu.Unlock()
	}
	backends := map[string]execbackend.Backend{
		"thinker-be": *interleavedBackendWithStream(caps, captureThinker, func() aipapi.ManagedEventStream {
			return thinkerMemoStream("plan")
		}),
		"exec-be": *interleavedBackendWithStream(caps, nil, func() aipapi.ManagedEventStream {
			return executorTextStream("answer")
		}),
	}
	ex, st := nonInterferenceSecureExecutor(t, backends, true)
	const selector = "[thinker]thinker-be:m^exec-be:m"
	first := interleavedBaseCall(selector)
	stream, err := ex.Execute(principalCtx("owner-ni"), first)
	if err != nil {
		t.Fatalf("first execute: %v", err)
	}
	if _, err := aipapi.Collect(context.Background(), stream); err != nil {
		t.Fatalf("first collect: %v", err)
	}
	second := interleavedBaseCall(selector)
	resumeInterleavedCall(first, second)
	stream, err = ex.Execute(principalCtx("owner-ni"), second)
	if err != nil {
		t.Fatalf("second execute: %v", err)
	}
	if _, err := aipapi.Collect(context.Background(), stream); err != nil {
		t.Fatalf("second collect: %v", err)
	}
	gotMu.Lock()
	shaped := gotCall
	opens := thinkerOpens
	gotMu.Unlock()
	if opens != 1 {
		t.Fatalf("thinker backend opens: got %d want 1", opens)
	}
	if got := niUserText(shaped); got != "pre:rtx:plan this" {
		t.Fatalf("extension stages must run before attempt shaping: user text %q", got)
	}
	if len(shaped.Tools) != 0 {
		t.Fatalf("thinker shaping must suppress tools after extensions, got %d", len(shaped.Tools))
	}
	if len(shaped.Instructions) == 0 || !strings.Contains(textOf(shaped.Instructions[0]), "Think step by step") {
		t.Fatal("thinker shaping must prepend instructions after extensions")
	}
	state, err := st.FetchInterleavedState(context.Background(), first.Session.ALegID)
	if err != nil {
		t.Fatalf("fetch interleaved state: %v", err)
	}
	if state.Cycle.IsEmpty() {
		t.Fatal("thinker cycle must persist after extension-ordered execute")
	}
}

func TestExecutor_InterleavedExtensionOrdering_nonThinkerPreservesExtensionsOnly(t *testing.T) {
	t.Parallel()
	var gotMu sync.Mutex
	var gotCall aipapi.Call
	capture := func(c aipapi.Call) {
		gotMu.Lock()
		gotCall = c
		gotMu.Unlock()
	}
	backends := map[string]execbackend.Backend{
		"stub": *interleavedBackend(
			aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityTools),
			capture,
		),
	}
	ex, st := nonInterferenceSecureExecutor(t, backends, true)
	call := interleavedBaseCall("stub:m")
	stream, err := ex.Execute(principalCtx("owner-ni"), call)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if _, err := aipapi.Collect(context.Background(), stream); err != nil {
		t.Fatalf("collect: %v", err)
	}
	gotMu.Lock()
	shaped := gotCall
	gotMu.Unlock()
	if got := niUserText(shaped); got != "pre:rtx:plan this" {
		t.Fatalf("extensions must mutate call: user text %q", got)
	}
	if len(shaped.Tools) != 1 {
		t.Fatalf("non-thinker must keep tools with interleaved enabled, got %d", len(shaped.Tools))
	}
	if len(shaped.Instructions) != 0 {
		t.Fatalf("non-thinker must not receive thinker instructions")
	}
	state, err := st.FetchInterleavedState(context.Background(), call.Session.ALegID)
	if err != nil {
		t.Fatalf("fetch interleaved state: %v", err)
	}
	if !state.IsEmpty() {
		t.Fatalf("non-thinker must not persist interleaved state, got %+v", state)
	}
}
