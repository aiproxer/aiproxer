package stdhttp

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/modelregistry"
	"github.com/aiproxer/aiproxer/internal/core/modelview"
)

type modelRegistryStatusResponse struct {
	Status             string                      `json:"status"`
	Generation         string                      `json:"generation"`
	RefreshedAt        time.Time                   `json:"refreshed_at"`
	ModelCount         int                         `json:"model_count"`
	BackendModelCounts map[string]int              `json:"backend_model_counts"`
	Discoveries        []modelRegistryDiscoveryRow `json:"discoveries"`
	// Aggregate model-view identity fields (req 9.6); omitted when unbound.
	ModelViewDigest    string `json:"model_view_digest,omitempty"`
	ConfigGeneration   string `json:"config_generation,omitempty"`
	ConfigFingerprint  string `json:"config_fingerprint,omitempty"`
	CatalogGeneration  string `json:"catalog_generation,omitempty"`
	RegistryGeneration string `json:"registry_generation,omitempty"`
}

type modelRegistryDiscoveryRow struct {
	BackendID  string `json:"backend_id"`
	Kind       string `json:"kind"`
	Status     string `json:"status"`
	Source     string `json:"source"`
	ModelCount int    `json:"model_count"`
	ErrorCode  string `json:"error_code"`
}

// ModelRegistryStatusHandler serves protected GET JSON for model-registry
// discovery diagnostics. Prefer the request-bound registry view when present;
// otherwise read the live runtime (legacy/direct constructors).
type ModelRegistryStatusHandler struct {
	rt *modelregistry.Runtime
}

var _ http.Handler = (*ModelRegistryStatusHandler)(nil)

// NewModelRegistryStatusHandler returns a concrete diagnostics status handler.
func NewModelRegistryStatusHandler(rt *modelregistry.Runtime) *ModelRegistryStatusHandler {
	return &ModelRegistryStatusHandler{rt: rt}
}

func (h *ModelRegistryStatusHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(true)
	var resp modelRegistryStatusResponse
	if r != nil {
		if bv, ok := modelregistry.BoundViewFromContext(r.Context()); ok {
			resp = buildModelRegistryStatusFromDiagnostics(bv.Diagnostics())
		} else {
			var rt *modelregistry.Runtime
			if h != nil {
				rt = h.rt
			}
			resp = buildModelRegistryStatus(rt)
		}
		if id, ok := modelview.FromContext(r.Context()); ok {
			fields := id.SafeFields()
			resp.ModelViewDigest = fields["model_view_digest"]
			resp.ConfigGeneration = fields["config_generation"]
			resp.ConfigFingerprint = fields["config_fingerprint"]
			resp.CatalogGeneration = fields["catalog_generation"]
			resp.RegistryGeneration = fields["registry_generation"]
		}
	}
	if err := enc.Encode(resp); err != nil {
		slog.Default().ErrorContext(r.Context(), "modelregistry: status encode", "error", err)
	}
}

func buildModelRegistryStatus(rt *modelregistry.Runtime) modelRegistryStatusResponse {
	if rt == nil {
		return buildModelRegistryStatusFromDiagnostics(modelregistry.Diagnostics{
			BackendModelCounts: map[string]int{},
			BackendDiscoveries: []modelregistry.BackendDiscovery{},
		})
	}
	return buildModelRegistryStatusFromDiagnostics(rt.Diagnostics())
}

func buildModelRegistryStatusFromDiagnostics(d modelregistry.Diagnostics) modelRegistryStatusResponse {
	out := modelRegistryStatusResponse{
		Status:             "unavailable",
		BackendModelCounts: map[string]int{},
		Discoveries:        []modelRegistryDiscoveryRow{},
	}
	if d.Active {
		out.Status = "active"
	}
	out.Generation = d.Generation
	out.RefreshedAt = d.RefreshedAt
	out.ModelCount = d.ModelCount
	if d.BackendModelCounts != nil {
		out.BackendModelCounts = d.BackendModelCounts
	}
	rows := make([]modelRegistryDiscoveryRow, 0, len(d.BackendDiscoveries))
	for _, b := range d.BackendDiscoveries {
		rows = append(rows, modelRegistryDiscoveryRow{
			BackendID:  b.BackendID,
			Kind:       b.Kind,
			Status:     string(b.Status),
			Source:     string(b.Source),
			ModelCount: b.ModelCount,
			ErrorCode:  b.ErrorCode,
		})
	}
	slices.SortFunc(rows, func(a, b modelRegistryDiscoveryRow) int {
		if c := strings.Compare(a.BackendID, b.BackendID); c != 0 {
			return c
		}
		return strings.Compare(a.Kind, b.Kind)
	})
	out.Discoveries = rows
	return out
}
