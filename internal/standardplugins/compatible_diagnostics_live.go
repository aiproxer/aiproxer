package standardplugins

import (
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/modelregistry"
)

const compatibleInventorySampleLimit = modelregistry.MaxInventoryModelSample

// CompatibleLiveInputs carries active-generation inventory state for live projection.
type CompatibleLiveInputs struct {
	Backends map[string]execbackend.Backend
	Registry *modelregistry.Registry
	Runtime  *modelregistry.Runtime
}

// ProjectCompatibleBackendRowsLive merges config-derived compatible rows with live
// model-registry snapshots. No credential resolution or provider requests occur here.
func ProjectCompatibleBackendRowsLive(cfg *config.Config, live CompatibleLiveInputs) []diag.CompatibleBackendRow {
	rows := ProjectCompatibleBackendRows(cfg)
	if cfg == nil || live.Runtime == nil {
		return rows
	}
	diagRT := live.Runtime.Diagnostics()
	discByBackend := map[string]modelregistry.BackendDiscovery{}
	for _, d := range diagRT.BackendDiscoveries {
		discByBackend[d.BackendID] = d
	}
	modelsByBackend := groupCompatibleModels(live.Registry, cfg)
	retainLastGood := diagRT.LastRefreshErrorCategory != modelregistry.RefreshFailureNone
	refreshedAt := ""
	if !diagRT.RefreshedAt.IsZero() {
		refreshedAt = diagRT.RefreshedAt.UTC().Format(time.RFC3339)
	}
	for i := range rows {
		id := rows[i].InstanceID
		if !IsCustomCompatibleBackendKind(rows[i].FactoryKind) {
			continue
		}
		if be, ok := live.Backends[id]; ok && rows[i].TokenizerID == "" {
			rows[i].TokenizerID = strings.TrimSpace(be.TokenizerID)
		}
		health := liveCompatibleHealth(discByBackend[id], modelsByBackend[id], refreshedAt, retainLastGood)
		rows[i].InventoryHealth = &health
	}
	return rows
}

func groupCompatibleModels(reg *modelregistry.Registry, cfg *config.Config) map[string][]modelregistry.BackendModel {
	out := map[string][]modelregistry.BackendModel{}
	if reg == nil || cfg == nil {
		return out
	}
	compatible := map[string]struct{}{}
	for _, row := range cfg.Plugins.Backends {
		if row.Enabled && IsCustomCompatibleBackendKind(row.FactoryID()) {
			compatible[row.InstanceID()] = struct{}{}
		}
	}
	for _, m := range reg.All() {
		if _, ok := compatible[m.BackendID]; !ok {
			continue
		}
		out[m.BackendID] = append(out[m.BackendID], m)
	}
	return out
}

func liveCompatibleHealth(
	disc modelregistry.BackendDiscovery,
	models []modelregistry.BackendModel,
	refreshedAt string,
	retainLastGood bool,
) diag.CompatibleInventoryHealth {
	health := diag.CompatibleInventoryHealth{
		Status:      string(disc.Status),
		Source:      string(disc.Source),
		ModelCount:  disc.ModelCount,
		ErrorCode:   disc.ErrorCode,
		RefreshedAt: refreshedAt,
	}
	if disc.BackendID == "" && len(models) == 0 {
		health.Status = "unconfigured"
	}
	if len(models) > 0 {
		health.SampleModels = sampleCompatibleModels(models)
		health.ModelCount = max(health.ModelCount, len(models))
		if health.Source == "" {
			health.Source = string(models[0].Source)
		}
	}
	health.LastSuccessHeld = retainLastGood && len(models) > 0
	return health
}

func sampleCompatibleModels(models []modelregistry.BackendModel) []diag.CompatibleInventoryModelSample {
	limit := min(len(models), compatibleInventorySampleLimit)
	out := make([]diag.CompatibleInventoryModelSample, 0, limit)
	for _, m := range models[:limit] {
		out = append(out, diag.CompatibleInventoryModelSample{
			InstanceID:       m.BackendID,
			FactoryKind:      m.Kind,
			Prefix:           m.Prefix,
			CanonicalID:      m.CanonicalID,
			NativeID:         m.NativeID,
			Source:           string(m.Source),
			CapabilitySource: m.CapabilitySource,
		})
	}
	return out
}
