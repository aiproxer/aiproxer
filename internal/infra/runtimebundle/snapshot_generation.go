package runtimebundle

import (
	"context"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/snapshotgen"
)

// buildSnapshotGeneration constructs the atomic publisher and optional refresh
// controller. TestingOverrides return the override publisher with a nil controller.
// Initial publication runs through SnapshotController.Refresh so injectable source
// errors surface as degraded/unavailable posture instead of silently keeping a
// falsely ready static snapshot (requirements 11.3, 11.6, 11.7).
func buildSnapshotGeneration(cfg *config.Config, testing TestingOptions, prod ProductionOptions) (*snapshotgen.Publisher, *SnapshotController) {
	if testing.SnapshotPublisherOverride != nil {
		pub := testing.SnapshotPublisherOverride
		_, _ = PublishExecutableFromProduction(pub, cfg, prod, time.Now().UTC())
		return pub, nil
	}
	ctrl := newSnapshotController(cfg, testing, prod)
	// Discard Refresh error at Build: posture is already published on the generation.
	// Callers observe readiness via Current()/ReadinessReport; RefreshSnapshots returns errors later.
	_ = ctrl.Refresh(context.Background())
	_, _ = PublishExecutableFromProduction(ctrl.Publisher(), cfg, prod, time.Now().UTC())
	return ctrl.Publisher(), ctrl
}
