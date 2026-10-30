package localstub

import (
	"context"
	"fmt"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/modelinventory"
	"gopkg.in/yaml.v3"
)

// ID is the registered backend factory kind for the standard distribution.
const ID = "local-stub"

// NewFromYAML builds a backend from opaque plugin YAML (composition root).
func NewFromYAML(n yaml.Node) (execbackend.Backend, error) {
	cfg, err := ParseConfig(n)
	if err != nil {
		return execbackend.Backend{}, fmt.Errorf("local-stub: from yaml: %w", err)
	}
	return New(cfg), nil
}

// New returns an exec backend that emits a fixed canonical stream from cfg.
func New(cfg Config) execbackend.Backend {
	caps := aipapi.NewBackendCaps(aipapi.CapabilityStreaming)
	if cfg.ToolName != "" {
		caps[aipapi.CapabilityTools] = struct{}{}
	}
	return execbackend.Backend{
		Caps:            caps,
		BackendPrefixes: []string{ID},
		ResolveCaps: func(context.Context, aipapi.Call, routing.AttemptCandidate) aipapi.BackendCaps {
			return caps
		},
		ModelInventory: modelinventory.StaticProvider{
			Source: modelinventory.SourceStaticBuiltin,
			Models: []modelinventory.Model{{
				CanonicalID: "local-stub/stub-default",
				NativeID:    "stub-default",
				DisplayName: "Local Stub Default",
			}},
		},
		Open: func(ctx context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			if ctx == nil {
				return nil, fmt.Errorf("%s: %w", ID, aipapi.ErrNilContext)
			}
			_ = call
			_ = cand
			return eventStreamForConfig(cfg), nil
		},
	}
}
