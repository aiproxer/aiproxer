package runtime_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/testkit"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/modelcatalog"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	accountingapp "github.com/aiproxer/aiproxer/internal/core/tokenaccounting/app"
	accountingpreflight "github.com/aiproxer/aiproxer/internal/core/tokenaccounting/preflight"
	"github.com/aiproxer/aiproxer/internal/featurebundle"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
	aipworkspace "github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
)

type postHookMutator struct {
	mode string
}

func (h postHookMutator) ID() string                      { return "post-hook-" + h.mode }
func (postHookMutator) Order() int                        { return 0 }
func (postHookMutator) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }
func (h postHookMutator) HandleRequestParts(_ context.Context, call *aipapi.Call, _ sdkhooks.PartMeta) error {
	if call == nil {
		return nil
	}
	switch h.mode {
	case "invalid":
		call.Messages = nil
	case "tools":
		call.Tools = append(call.Tools, aipapi.ToolDef{Name: "need-tools", Description: "x"})
	case "transport":
		call.Invocation.DeliveryMode = aipapi.DeliveryModeStreaming
	case "context":
		call.Messages = append(call.Messages, aipapi.Message{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart(strings.Repeat("z", 200))},
		})
	case "preflight":
		call.Messages = append(call.Messages, aipapi.Message{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart(strings.Repeat("p", 64))},
		})
	case "route":
		call.Route.Selector = "hijacked:evil-model"
	}
	return nil
}

func postHookBaseCall(sel string) *aipapi.Call {
	return &aipapi.Call{
		ID:    "post-hook-base",
		Route: aipapi.RouteIntent{Selector: sel},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hello")},
		}},
		Invocation: aipapi.Invocation{
			Operation:    aipapi.OperationOpenAIChatCompletions,
			DeliveryMode: aipapi.DeliveryModeNonStreaming,
		},
	}
}

func TestPostHookRederive_dimensionsTable(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		mode       string
		selector   string
		setup      func(*runtime.Executor, *atomic.Int64, *atomic.Int64)
		wantOpenA  int64
		wantOpenB  int64
		wantErrSub string
	}{
		{
			name:     "invalid_call",
			mode:     "invalid",
			selector: "a:m|b:m",
			setup: func(ex *runtime.Executor, a, b *atomic.Int64) {
				ex.Backends = map[string]execbackend.Backend{
					"a": streamingOpenCounter(a),
					"b": streamingOpenCounter(b),
				}
			},
			wantErrSub: "validation",
		},
		{
			name:     "tools_capability",
			mode:     "tools",
			selector: "a:m|b:m",
			setup: func(ex *runtime.Executor, a, b *atomic.Int64) {
				ex.Backends = map[string]execbackend.Backend{
					"a": {
						Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
						TransportCaps: aipapi.NewBackendTransportCaps(aipapi.OperationTransportSupport{
							Operation: aipapi.OperationOpenAIChatCompletions,
							Modes:     []aipapi.TransportMode{aipapi.TransportModeStreaming, aipapi.TransportModeNonStreaming},
						}),
						Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
							a.Add(1)
							return nil, errors.New("a must not open")
						},
					},
					"b": {
						Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityTools),
						TransportCaps: aipapi.NewBackendTransportCaps(aipapi.OperationTransportSupport{
							Operation: aipapi.OperationOpenAIChatCompletions,
							Modes:     []aipapi.TransportMode{aipapi.TransportModeStreaming, aipapi.TransportModeNonStreaming},
						}),
						Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
							b.Add(1)
							return aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventResponseFinished}}), nil
						},
					},
				}
			},
			wantOpenB: 1,
		},
		{
			name:     "unsupported_transport",
			mode:     "transport",
			selector: "a:m|b:m",
			setup: func(ex *runtime.Executor, a, b *atomic.Int64) {
				ex.Backends = map[string]execbackend.Backend{
					"a": {
						Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
						TransportCaps: aipapi.NewBackendTransportCaps(aipapi.OperationTransportSupport{
							Operation: aipapi.OperationOpenAIChatCompletions,
							Modes:     []aipapi.TransportMode{aipapi.TransportModeNonStreaming},
						}),
						Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
							a.Add(1)
							return nil, errors.New("a must not open")
						},
					},
					"b": {
						Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
						TransportCaps: aipapi.NewBackendTransportCaps(aipapi.OperationTransportSupport{
							Operation: aipapi.OperationOpenAIChatCompletions,
							Modes:     []aipapi.TransportMode{aipapi.TransportModeStreaming, aipapi.TransportModeNonStreaming},
						}),
						Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
							b.Add(1)
							return aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventResponseFinished}}), nil
						},
					},
				}
			},
			wantOpenB: 1,
		},
		{
			name:     "context_size",
			mode:     "context",
			selector: "smallctx:m|bigctx:m",
			setup: func(ex *runtime.Executor, a, b *atomic.Int64) {
				ex.CatalogResolver = contextLimitCatalogResolver{}
				ex.EligibilityResolver = modelcatalog.NewEligibilityResolver(modelcatalog.DefaultSizeEstimator{})
				ex.Backends = map[string]execbackend.Backend{
					"smallctx": {
						Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
						TransportCaps: aipapi.NewBackendTransportCaps(aipapi.OperationTransportSupport{
							Operation: aipapi.OperationOpenAIChatCompletions,
							Modes:     []aipapi.TransportMode{aipapi.TransportModeStreaming, aipapi.TransportModeNonStreaming},
						}),
						Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
							a.Add(1)
							return nil, errors.New("smallctx must not open")
						},
					},
					"bigctx": {
						Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
						TransportCaps: aipapi.NewBackendTransportCaps(aipapi.OperationTransportSupport{
							Operation: aipapi.OperationOpenAIChatCompletions,
							Modes:     []aipapi.TransportMode{aipapi.TransportModeStreaming, aipapi.TransportModeNonStreaming},
						}),
						Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
							b.Add(1)
							return aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventResponseFinished}}), nil
						},
					},
				}
			},
			wantOpenB: 1,
		},
		{
			name:     "preflight_accounting",
			mode:     "preflight",
			selector: "a:m",
			setup: func(ex *runtime.Executor, a, b *atomic.Int64) {
				ex.Preflight = accountingpreflight.NewChecker(
					accountingAdmissionCountFunc(func(context.Context, accountingapp.CountCallInput) (accountingapp.CountResult, error) {
						return accountingapp.CountResult{InputTokens: 100, OutputTokens: 1, TotalTokens: 101}, nil
					}),
					accountingpreflight.Config{Enabled: true, Mode: accountingpreflight.ModeStrict, MaxInputTokens: 8},
				)
				ex.Backends = map[string]execbackend.Backend{
					"a": {
						Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
						TransportCaps: aipapi.NewBackendTransportCaps(aipapi.OperationTransportSupport{
							Operation: aipapi.OperationOpenAIChatCompletions,
							Modes:     []aipapi.TransportMode{aipapi.TransportModeStreaming, aipapi.TransportModeNonStreaming},
						}),
						Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
							a.Add(1)
							return nil, errors.New("must not open")
						},
					},
				}
			},
			wantErrSub: "preflight",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
			if err != nil {
				t.Fatal(err)
			}
			var openedA, openedB atomic.Int64
			ex := runtime.TestExecutor()
			ex.Store = st
			ex.MaxAttempts = 4
			ex.Rand = routing.NewSeededRng(3)
			ex.Bus = hooks.New(hooks.Config{RequestPartHooks: []sdkhooks.RequestPartHook{postHookMutator{mode: tc.mode}}})
			tc.setup(ex, &openedA, &openedB)
			call := postHookBaseCall(tc.selector)
			stream, execErr := ex.Execute(t.Context(), call)
			if tc.wantErrSub != "" {
				if execErr == nil || !strings.Contains(strings.ToLower(execErr.Error()), strings.ToLower(tc.wantErrSub)) {
					t.Fatalf("want err containing %q, got %v", tc.wantErrSub, execErr)
				}
			} else if execErr != nil {
				t.Fatalf("execute: %v", execErr)
			}
			if stream != nil {
				_, _ = aipapi.Collect(t.Context(), stream)
			}
			if openedA.Load() != tc.wantOpenA || openedB.Load() != tc.wantOpenB {
				t.Fatalf("opens a=%d b=%d want a=%d b=%d", openedA.Load(), openedB.Load(), tc.wantOpenA, tc.wantOpenB)
			}
		})
	}
}

type accountingAdmissionCountFunc func(context.Context, accountingapp.CountCallInput) (accountingapp.CountResult, error)

func (f accountingAdmissionCountFunc) CountCall(ctx context.Context, in accountingapp.CountCallInput) (accountingapp.CountResult, error) {
	return f(ctx, in)
}

func streamingOpenCounter(n *atomic.Int64) execbackend.Backend {
	return execbackend.Backend{
		Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
		TransportCaps: aipapi.NewBackendTransportCaps(aipapi.OperationTransportSupport{
			Operation: aipapi.OperationOpenAIChatCompletions,
			Modes:     []aipapi.TransportMode{aipapi.TransportModeStreaming, aipapi.TransportModeNonStreaming},
		}),
		Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			n.Add(1)
			return aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventResponseFinished}}), nil
		},
	}
}

func TestPostHookRederive_pinsRouteIdentity(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var gotSel string
	var gotModel string
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{RequestPartHooks: []sdkhooks.RequestPartHook{postHookMutator{mode: "route"}}})
	ex.Backends = map[string]execbackend.Backend{
		"a": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			TransportCaps: aipapi.NewBackendTransportCaps(aipapi.OperationTransportSupport{
				Operation: aipapi.OperationOpenAIChatCompletions,
				Modes:     []aipapi.TransportMode{aipapi.TransportModeStreaming, aipapi.TransportModeNonStreaming},
			}),
			Open: func(_ context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				gotSel = call.Route.Selector
				gotModel = cand.Primary.Model
				return aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventResponseFinished}}), nil
			},
		},
	}
	stream, execErr := ex.Execute(t.Context(), postHookBaseCall("a:good-model"))
	if execErr != nil {
		t.Fatal(execErr)
	}
	_, _ = aipapi.Collect(t.Context(), stream)
	if gotSel != "a:good-model" {
		t.Fatalf("route selector hijacked: %q", gotSel)
	}
	if gotModel != "good-model" {
		t.Fatalf("candidate model=%q", gotModel)
	}
}

type fixedWorkspaceResolver struct {
	view aipworkspace.WorkspaceView
}

func (f fixedWorkspaceResolver) Resolve(context.Context) (aipworkspace.WorkspaceView, error) {
	out := f.view
	out.Markers = append([]string(nil), f.view.Markers...)
	return out, nil
}

func TestAttemptMeta_completenessAndDefensiveCopies(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	xform := &metaCaptureTransform{}
	bundle := contributeAttemptTransformBundle(t, xform)
	gen, err := featurebundle.MergeBundlesGenerated(bundle)
	if err != nil {
		t.Fatal(err)
	}
	bus := hooks.New(hooks.Config{})
	wsView := aipworkspace.WorkspaceView{ID: "ws-1", ProjectRoot: "/proj", Markers: []string{"m1"}}
	snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		Workspace: fixedWorkspaceResolver{view: wsView},
		FeaturePlanes: testkit.FreezeTestBundle(testkit.TestFeatureBundle{
			AttemptTransforms: aipfeature.Get(gen.Frozen, aipfeature.PlaneAttemptTransforms),
		}),
	})
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = bus
	ex.RuntimeSnapshot = snap
	prefixes := []string{"family-a", "family-b"}
	dialects := []aipapi.ReasoningDialect{aipapi.ReasoningDialectOpenAIChatTextV1}
	ex.Backends = map[string]execbackend.Backend{
		"a": {
			Caps:            aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			BackendPrefixes: prefixes,
			ReplaySupport:   aipapi.ReasoningReplaySupport{Dialects: dialects},
			TransportCaps: aipapi.NewBackendTransportCaps(aipapi.OperationTransportSupport{
				Operation: aipapi.OperationOpenAIChatCompletions,
				Modes:     []aipapi.TransportMode{aipapi.TransportModeStreaming, aipapi.TransportModeNonStreaming},
			}),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventResponseFinished}}), nil
			},
		},
	}
	stream, execErr := ex.Execute(t.Context(), postHookBaseCall("a:m"))
	if execErr != nil {
		t.Fatal(execErr)
	}
	_, _ = aipapi.Collect(t.Context(), stream)
	if xform.meta.BackendID != "a" || xform.meta.CandidateKey == "" || xform.meta.Model != "m" {
		t.Fatalf("identity incomplete: %+v", xform.meta)
	}
	if xform.meta.TraceID == "" || xform.meta.ALegID == "" {
		t.Fatalf("trace/aleg empty: %+v", xform.meta)
	}
	if len(xform.meta.BackendPrefixes) != 2 || xform.meta.BackendPrefixes[0] != "family-a" {
		t.Fatalf("prefixes=%v", xform.meta.BackendPrefixes)
	}
	if len(xform.meta.ReplaySupport.Dialects) != 1 {
		t.Fatalf("replay=%v", xform.meta.ReplaySupport)
	}
	if xform.meta.Workspace.ID != "ws-1" || xform.meta.Workspace.ProjectRoot != "/proj" {
		t.Fatalf("workspace=%+v", xform.meta.Workspace)
	}
	if xform.meta.Scope.PrincipalID.String() == "" || xform.meta.Session.AuthoritativeSessionID == "" {
		t.Fatalf("scope/session incomplete: scope=%+v session=%+v", xform.meta.Scope, xform.meta.Session)
	}
	xform.meta.BackendPrefixes[0] = "mutated"
	xform.meta.ReplaySupport.Dialects[0] = "mutated"
	xform.meta.Workspace.Markers[0] = "mutated"
	be := ex.Backends["a"]
	if be.BackendPrefixes[0] != "family-a" {
		t.Fatal("backend prefixes mutated via AttemptMeta")
	}
	if be.ReplaySupport.Dialects[0] != aipapi.ReasoningDialectOpenAIChatTextV1 {
		t.Fatal("backend replay dialects mutated via AttemptMeta")
	}
}

type metaCaptureTransform struct {
	meta request.AttemptMeta
}

func (m *metaCaptureTransform) ID() string                      { return "meta-capture" }
func (*metaCaptureTransform) Order() int                        { return 0 }
func (*metaCaptureTransform) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }
func (m *metaCaptureTransform) HandleAttempt(_ context.Context, _ *aipapi.Call, meta request.AttemptMeta, _ request.Services) (request.AttemptDecision, error) {
	m.meta = meta
	return request.AttemptDecision{Kind: request.AttemptContinue}, nil
}
