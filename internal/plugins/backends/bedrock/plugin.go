package bedrock

import (
	"context"
	"fmt"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/plugins/features/codexclientcompat"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func defaultBackendCaps() aipapi.BackendCaps {
	return aipapi.NewBackendCaps(
		aipapi.CapabilityStreaming,
		aipapi.CapabilityTools,
		aipapi.CapabilityVision,
		aipapi.CapabilityDocuments,
		aipapi.CapabilityParallelToolCalls,
	)
}

// DefaultLoadConfigTimeout bounds AWS SDK default configuration loading during backend construction.
// Composition roots calling NewWithContext should wrap their
// context with context.WithTimeout using this duration (or shorter) unless it already carries a deadline.
const DefaultLoadConfigTimeout = 30 * time.Second

// ensureLoadConfigDeadline returns a context for awsconfig.LoadDefaultConfig. If ctx is nil, or
// has no deadline, it wraps with [DefaultLoadConfigTimeout] so config load cannot hang
// indefinitely. The caller must invoke the returned CancelFunc.
//
// The nil coercion is a deliberate, documented fallback: NewWithContext cannot return an
// error mid-construction, so a nil ctx is coerced rather than panicking. Production
// composition roots must always pass a context (see DefaultLoadConfigTimeout); the nil
// path exists for tests and defensive robustness, and Open still fails explicitly on a
// nil caller context with aipapi.ErrNilContext.
func ensureLoadConfigDeadline(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		return context.WithTimeout(context.Background(), DefaultLoadConfigTimeout)
	}
	if _, ok := ctx.Deadline(); ok {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, DefaultLoadConfigTimeout)
}

// NewWithContext returns a runtime backend, using ctx for awsconfig.LoadDefaultConfig.
// A deadline is always applied for the load step: either ctx's own deadline, or
// [DefaultLoadConfigTimeout] when ctx is nil or uncancelled without a deadline.
func NewWithContext(ctx context.Context, cfg Config) execbackend.Backend {
	loadCtx, cancel := ensureLoadConfigDeadline(ctx)
	defer cancel()
	cli, err := newRuntimeClient(loadCtx, cfg)
	if err != nil {
		// Surface construction errors at Open time via a backend that always fails.
		return execbackend.Backend{
			Caps:            defaultBackendCaps(),
			BackendPrefixes: []string{ID},
			ModelInventory:  newFoundationModelsProvider(loadCtx, cfg),
			ResolveCaps: func(_ context.Context, call aipapi.Call, cand routing.AttemptCandidate) aipapi.BackendCaps {
				return ModelCapabilities(resolveModelID(cand, call))
			},
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return nil, err
			},
		}
	}
	client := cli
	return execbackend.Backend{
		Caps:                                 defaultBackendCaps(),
		BackendPrefixes:                      []string{ID},
		EnforcesMaxOutputTokens:              true,
		IgnoresAuthorityMaxOutputTokensClamp: codexclientcompat.IgnoresClampViaCodexUnsupportedGenParams,
		ModelInventory:                       newFoundationModelsProvider(loadCtx, cfg),
		ResolveCaps: func(_ context.Context, call aipapi.Call, cand routing.AttemptCandidate) aipapi.BackendCaps {
			return ModelCapabilities(resolveModelID(cand, call))
		},
		Open: func(ctx context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			if ctx == nil {
				return nil, fmt.Errorf("%s: %w", ID, aipapi.ErrNilContext)
			}
			in, err := ConverseStreamInputForCall(&call, cand)
			if err != nil {
				return nil, err
			}
			out, err := client.ConverseStream(ctx, in)
			if err != nil {
				return nil, converseStreamOpenError(err)
			}
			return newConverseStream(out.GetStream(), call.MaxPendingWireEvents), nil
		},
	}
}

// converseStreamOpenError attributes a ConverseStream open failure to this backend.
// The open error is always pre-output (the stream has not been returned to the caller),
// so classified transient/auth classes are marked recoverable for core failover; stream
// recv failures after open stay non-recoverable (see converseStream.Recv).
func converseStreamOpenError(err error) error {
	werr := fmt.Errorf("bedrock: ConverseStream: %w", err)
	if classifyBedrockError(err) != failureNone {
		return aipapi.RecoverablePreOutputError(werr)
	}
	return werr
}
