package diag

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/aiproxer/aiproxer/internal/core/config"
)

// InventorySnapshot is a JSON-serializable view of configured plugins for operators.
type InventorySnapshot struct {
	Frontends              []PluginRow            `json:"frontends"`
	Backends               []PluginRow            `json:"backends"`
	CompatibleBackends     []CompatibleBackendRow `json:"compatible_backends,omitempty"`
	InstanceDiagnostics    []InstanceDiagnostic   `json:"instance_diagnostics,omitempty"`
	OpenResponsesFrontends []InstanceDiagnostic   `json:"openresponses_frontends,omitempty"`
	Features               []PluginRow            `json:"features"`
	Extensions             InventoryExtensions    `json:"extensions"`
	// ServerLimits exposes effective decode/admission caps and configured pending-wire
	// (0 = unlimited) as numbers only; no payloads.
	ServerLimits InventoryServerLimits `json:"server_limits"`
}

// InventoryServerLimits is the operator-visible server admission/queue caps.
// Decode fields are effective (zero→finite defaults). MaxPendingWireEvents is as configured (0 = unlimited).
type InventoryServerLimits struct {
	MaxRequestBodyBytes    int64 `json:"max_request_body_bytes"`
	MaxConcurrentDecodes   int   `json:"max_concurrent_decodes"`
	MaxInflightDecodeBytes int64 `json:"max_inflight_decode_bytes"`
	MaxPendingWireEvents   int   `json:"max_pending_wire_events"`
}

// PluginRow is one config row (instance id + factory kind + enabled; config payloads stay opaque/private).
type PluginRow struct {
	ID          string `json:"id"`
	FactoryKind string `json:"factory_kind"`
	Enabled     bool   `json:"enabled"`
}

// InventorySnapshotForConfig builds the same operator inventory view as [InventoryHandler] without HTTP.
// CompatibleBackendProjector builds bounded compatible-backend rows for inventory.
type CompatibleBackendProjector func(cfg *config.Config) []CompatibleBackendRow

// InstanceDiagnosticProjector is an extension-owned, side-effect-free view.
type InstanceDiagnosticProjector func(cfg *config.Config) []InstanceDiagnostic

func InventorySnapshotForConfig(
	ctx context.Context,
	cfg *config.Config,
	extras *InventoryExtras,
) (InventorySnapshot, error) {
	if cfg == nil {
		return InventorySnapshot{}, errors.New("diag: inventory snapshot for config: nil config")
	}
	if ctx == nil {
		return InventorySnapshot{}, errors.New("diag: inventory snapshot for config: nil context")
	}
	snap := InventorySnapshot{
		Frontends:  rows(cfg.Plugins.Frontends),
		Backends:   rows(cfg.Plugins.Backends),
		Features:   rows(cfg.Plugins.Features),
		Extensions: buildInventoryExtensions(ctx, cfg, extras),
		ServerLimits: InventoryServerLimits{
			MaxRequestBodyBytes:    cfg.Server.EffectiveMaxRequestBodyBytesForBudget(),
			MaxConcurrentDecodes:   cfg.Server.EffectiveMaxConcurrentDecodes(),
			MaxInflightDecodeBytes: cfg.Server.EffectiveMaxInflightDecodeBytes(),
			MaxPendingWireEvents:   cfg.Server.EffectiveMaxPendingWireEvents(),
		},
	}
	if extras != nil && extras.Precomputed != nil {
		snap.CompatibleBackends = extras.Precomputed.CompatibleBackends
		snap.InstanceDiagnostics = extras.Precomputed.InstanceDiagnostics
		snap.OpenResponsesFrontends = extras.Precomputed.OpenResponsesFrontends
		return snap, nil
	}

	var compatible []CompatibleBackendRow
	var projectors []InstanceDiagnosticProjector
	if extras != nil {
		if extras.CompatibleBackends != nil {
			compatible = extras.CompatibleBackends(cfg)
		}
		projectors = extras.InstanceDiagnosticProjectors
	}
	projection := ProjectInventoryDiagnostics(cfg, compatible, projectors)
	snap.CompatibleBackends = projection.CompatibleBackends
	snap.InstanceDiagnostics = projection.InstanceDiagnostics
	snap.OpenResponsesFrontends = projection.OpenResponsesFrontends
	return snap, nil
}

// InventoryHandler serves GET JSON describing enabled plugin rows from cfg.
// Feature extension occupancy is read from compiled metadata supplied in extras.
func InventoryHandler(cfg *config.Config, extras *InventoryExtras) (http.Handler, error) {
	if cfg == nil {
		return nil, errors.New("diag: InventoryHandler: nil config")
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		snap, err := InventorySnapshotForConfig(r.Context(), cfg, extras)
		if err != nil {
			slog.ErrorContext(r.Context(), "diag: inventory snapshot", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		enc := json.NewEncoder(w)
		enc.SetEscapeHTML(true)
		if err := enc.Encode(snap); err != nil {
			slog.ErrorContext(r.Context(), "diag: inventory encode", "error", err)
		}
	}), nil
}

func rows(in []config.PluginConfig) []PluginRow {
	out := make([]PluginRow, 0, len(in))
	for _, p := range in {
		out = append(out, PluginRow{
			ID:          p.InstanceID(),
			FactoryKind: p.FactoryID(),
			Enabled:     p.Enabled,
		})
	}
	return out
}
