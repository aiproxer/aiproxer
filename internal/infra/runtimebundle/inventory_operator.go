package runtimebundle

import (
	"context"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

func InventorySnapshotForOperator(ctx context.Context, cfg *config.Config, reg *pluginreg.Registry, registrations []aipsdk.Registration) (diag.InventorySnapshot, error) {
	var live *inventoryLiveSnapshot
	if snap, err := tryLoadInventoryLiveSnapshot(ctx, cfg, reg); err == nil {
		live = snap
		defer func() { _ = live.Close(ctx) }()
	}
	return inventorySnapshotForOperator(ctx, cfg, reg, registrations, live)
}
