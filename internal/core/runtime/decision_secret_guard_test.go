package runtime_test

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
)

func TestDecisionSecretGuard_DeniesBeforeGuardOrUpstream(t *testing.T) {
	t.Parallel()
	for _, guarded := range []bool{true, false} {
		t.Run(map[bool]string{true: "guarded", false: "unguarded"}[guarded], func(t *testing.T) {
			t.Parallel()
			var evaluations, opens atomic.Int32
			var guards []secretguard.Guard
			if guarded {
				guards = []secretguard.Guard{&blockingSecretGuard{evals: &evaluations}}
			}
			ex := runtime.TestExecutor()
			var err error
			ex.Store, err = b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
			if err != nil {
				t.Fatal(err)
			}
			ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(nil, extensions.SnapshotOptions{
				FeaturePlanes: testkit.FreezeTestBundle(testkit.TestFeatureBundle{SecretGuards: guards}),
			})
			ex.Bus = ex.RuntimeSnapshot.HookBus()
			ex.Backends = map[string]execbackend.Backend{"decision": {
				Caps: aipapi.NewBackendCaps(aipapi.CapabilityDecisions),
				Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
					opens.Add(1)
					return aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventResponseStarted}, {Kind: aipapi.EventResponseFinished}}), nil
				},
			}}
			call := &aipapi.Call{
				Route:      aipapi.RouteIntent{Selector: "decision:jev"},
				Decision:   &aipapi.DecisionRequest{Evidence: json.RawMessage(`"evidence"`), Questions: []aipapi.DecisionQuestion{{ID: "q", Kind: aipapi.DecisionKindNoul}}},
				Invocation: aipapi.Invocation{Operation: aipapi.OperationDecisionEvaluate, DeliveryMode: aipapi.DeliveryModeNonStreaming},
			}
			stream, err := ex.Execute(t.Context(), call)
			if stream != nil {
				defer func() { _ = stream.Close() }()
			}
			if guarded {
				if !aipapi.IsPolicyDenied(err) || evaluations.Load() != 0 || opens.Load() != 0 {
					t.Fatalf("guarded decision: err=%v evaluations=%d opens=%d", err, evaluations.Load(), opens.Load())
				}
			} else if err != nil || opens.Load() != 1 {
				t.Fatalf("unguarded decision: err=%v opens=%d", err, opens.Load())
			}
		})
	}
}
