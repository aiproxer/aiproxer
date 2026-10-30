package runtime

import (
	"context"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/usageevidence"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

// TestExecutor returns an empty executor for tests that assign fields via promoted
// grouped-runtime accessors after construction. Prefer [NewExecutor] with [ExecutorConfig]
// for new composition-root wiring.
type testTerminalSink struct {
	appendCall func(context.Context, usageevidence.CallUsageRecord) error
	appendLeg  func(context.Context, usageevidence.CallLegUsageRecord) error
}

func (s testTerminalSink) AppendCall(ctx context.Context, record usageevidence.CallUsageRecord) error {
	if s.appendCall == nil {
		return nil
	}
	return s.appendCall(ctx, record)
}

func (s testTerminalSink) AppendLeg(ctx context.Context, record usageevidence.CallLegUsageRecord) error {
	if s.appendLeg == nil {
		return nil
	}
	return s.appendLeg(ctx, record)
}

func TestExecutor() *Executor {
	ex := &Executor{}
	ex.SystemPromptLaunchSeq = NewRequestLaunchSequence()
	ex.TerminalUsageSink = testTerminalSink{}
	ex.ExecutionCompositionPolicy = config.ExecutionCompositionSafe
	ex.BackendExecutionResolver = routing.BackendExecutionResolverFunc(func(id string) (aipsdk.BackendExecutionClass, bool) {
		if ex.Backends != nil {
			if _, ok := ex.Backends[id]; ok {
				return aipsdk.BackendExecutionInference, true
			}
		}
		return aipsdk.BackendExecutionUnknown, false
	})
	return ex
}
