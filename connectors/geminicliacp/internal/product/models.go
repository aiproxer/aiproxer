package product

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/connector-support/acp"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/modelinventory"
)

// defaultDiscoveryTimeout bounds one-shot ACP model enumeration. It covers
// Node-based CLI cold start while staying inside the host per-backend fetch
// timeout so a stalled agent cannot strand inventory refresh.
const defaultDiscoveryTimeout = 30 * time.Second

// sessionModelsProvider enumerates Gemini CLI models by spawning the agent
// one-shot and reading the models state of the ACP session/new response. No
// prompt is sent. Successful enumeration is authoritative: exactly the
// advertised models are returned. Failures are explicit errors so the host
// runtime can apply its cached-enumeration fallback; this provider keeps no
// static model list.
type sessionModelsProvider struct {
	starter   acp.ProcessStarter
	spec      *geminiSpec
	workspace string
	timeout   time.Duration
}

func (p sessionModelsProvider) LoadModels(ctx context.Context) (modelinventory.Snapshot, error) {
	if ctx == nil {
		return modelinventory.Snapshot{}, modelinventory.ErrNilContext
	}
	if err := ctx.Err(); err != nil {
		return modelinventory.Snapshot{}, err
	}
	if p.spec == nil {
		return modelinventory.Snapshot{}, &modelinventory.OperationalError{
			Code: modelinventory.ErrorCodeUnavailable,
			Err:  errors.New("gemini models: nil connector spec"),
		}
	}
	cmd, cwd, env, err := p.spec.BuildCommand(p.spec.ResolveModel(""), p.workspace)
	if err != nil {
		return modelinventory.Snapshot{}, &modelinventory.OperationalError{
			Code: modelinventory.ErrorCodeUnavailable,
			Err:  fmt.Errorf("gemini models: build command: %w", err),
		}
	}
	timeout := p.timeout
	if timeout <= 0 {
		timeout = defaultDiscoveryTimeout
	}
	dctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	discovered, err := acp.DiscoverSessionModels(dctx, cmd, cwd, env, p.starter, p.spec.HandshakeProfile())
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return modelinventory.Snapshot{}, err
		}
		return modelinventory.Snapshot{}, &modelinventory.OperationalError{
			Code: modelinventory.ErrorCodeUnavailable,
			Err:  fmt.Errorf("gemini models: %w", err),
		}
	}
	models := make([]modelinventory.Model, 0, len(discovered.Available))
	for _, m := range discovered.Available {
		native := strings.TrimSpace(m.ModelID)
		if native == "" {
			continue
		}
		display := strings.TrimSpace(m.Name)
		if display == "" {
			display = native
		}
		models = append(models, modelinventory.Model{
			CanonicalID: vendorPrefix + "/" + native,
			NativeID:    native,
			DisplayName: display,
		})
	}
	return modelinventory.Snapshot{
		Source:   modelinventory.SourceRemote,
		LoadedAt: time.Now().UTC(),
		Models:   models,
		Warnings: []string{},
	}, nil
}
