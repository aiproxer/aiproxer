package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/connectors/opencode/internal/catalog"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/modelinventory"
)

func (i *instance) ListModels(ctx context.Context, limit uint32) (backendplugin.ListModelsResponse, error) {
	inv := catalog.NewInventoryProviderFromSource(i.source)
	snap, err := inv.LoadModels(ctx)
	if err != nil {
		return backendplugin.ListModelsResponse{}, err
	}
	out := make([]backendplugin.ModelDescriptor, 0, len(snap.Models))
	for _, m := range snap.Models {
		if limit > 0 && uint32(len(out)) >= limit {
			break
		}
		out = append(out, backendplugin.ModelDescriptor{
			CanonicalModelID: i.kind + "/" + m.CanonicalID,
			NativeModelID:    m.NativeID,
			FactoryKind:      i.kind,
			Capabilities: backendplugin.CapabilitySummary{
				Streaming: true, Tools: true, ParallelToolCalls: true,
			},
		})
	}
	if len(out) == 0 {
		return backendplugin.ListModelsResponse{}, fmt.Errorf("opencode: discovery returned no models")
	}
	source := i.kind
	if snap.Source == modelinventory.SourceStaticInline {
		source = string(snap.Source)
	}
	return backendplugin.ListModelsResponse{
		Models: out, InventorySource: source, FetchedUnixMS: time.Now().UnixMilli(),
	}, nil
}

func resolveRouteModel(kind string, inv backendplugin.Invocation) string {
	model := strings.TrimSpace(inv.CanonicalModelID)
	if model == "" {
		return ""
	}
	prefix := kind + "/"
	if raw, ok := strings.CutPrefix(model, prefix); ok {
		return strings.TrimSpace(raw)
	}
	return model
}

func (i *instance) resolveModel(ctx context.Context, inv backendplugin.Invocation) (catalog.ResolvedModel, error) {
	model := resolveRouteModel(i.kind, inv)
	if model == "" {
		return catalog.ResolvedModel{}, fmt.Errorf("%w: empty model", catalog.ErrUnknownModel)
	}
	return i.source.Resolve(ctx, model)
}
