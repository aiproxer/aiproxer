package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/connectors/codex/internal/catalog"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/modelinventory"
)

func (i *instance) ListModels(ctx context.Context, limit uint32) (backendplugin.ListModelsResponse, error) {
	var inv modelinventory.Provider
	switch {
	case i.http != nil:
		inv = i.http.Inventory()
	case i.app != nil:
		inv = i.app.Inventory()
	default:
		return backendplugin.ListModelsResponse{}, fmt.Errorf("codex connector: not configured")
	}
	if inv == nil {
		return backendplugin.ListModelsResponse{}, fmt.Errorf("codex connector: no inventory")
	}
	snap, err := inv.LoadModels(ctx)
	if err != nil {
		return backendplugin.ListModelsResponse{}, err
	}
	out := make([]backendplugin.ModelDescriptor, 0, len(snap.Models))
	for _, m := range snap.Models {
		if limit > 0 && uint32(len(out)) >= limit {
			break
		}
		var caps backendplugin.CapabilitySummary
		if i.kind == FactoryKindHTTP {
			caps = backendplugin.CapabilitySummary{
				Streaming: true, Tools: true, Vision: true, Documents: true,
				ParallelToolCalls: true, Reasoning: true, ReasoningReplay: true,
			}
		} else {
			caps = backendplugin.CapabilitySummary{Streaming: true, Tools: true, Vision: true, Reasoning: true}
		}
		out = append(out, backendplugin.ModelDescriptor{
			CanonicalModelID: m.CanonicalID,
			NativeModelID:    m.NativeID,
			FactoryKind:      i.kind,
			Capabilities:     caps,
		})
	}
	if len(out) == 0 {
		return backendplugin.ListModelsResponse{}, fmt.Errorf("codex connector: discovery returned no models")
	}
	source := string(i.catalogSrc)
	if source == "" {
		source = i.kind
	}
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

func inventorySourceLabel(src catalog.Source, kind string) string {
	if src == catalog.SourceDiscovered {
		return "discovered"
	}
	if src == catalog.SourceShippedFallback {
		return "shipped_fallback"
	}
	if src == catalog.SourceOverrideFallback {
		return "override_fallback"
	}
	return kind
}
