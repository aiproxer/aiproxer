package diag

import (
	"encoding/json"
	"errors"
	"net/http"

	sdkreload "github.com/aiproxer/aiproxer/pkg/aipsdk/configreload"
)

// ReloadStatusSource is the query seam for protected reload diagnostics (req 14.1, 14.7).
type ReloadStatusSource interface {
	ReloadStatus() sdkreload.Status
}

// ReloadStatusHandler serves GET JSON of bounded reload status with config/model
// generation correlation. Wrap with [WrapDiagnosticsProtect] at the mount site.
func ReloadStatusHandler(src ReloadStatusSource) (http.Handler, error) {
	if src == nil {
		return nil, errors.New("diag: ReloadStatusHandler: nil source")
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		st := src.ReloadStatus()
		out := reloadStatusDTO(st)
		w.Header().Set("Content-Type", "application/json")
		enc := json.NewEncoder(w)
		enc.SetEscapeHTML(true)
		_ = enc.Encode(out)
	}), nil
}

type reloadDiagDTO struct {
	ActiveGeneration    int64                    `json:"active_generation"`
	ModelGeneration     string                   `json:"model_generation,omitempty"`
	SourceIntegrity     string                   `json:"source_integrity,omitempty"`
	RetainedGenerations int                      `json:"retained_generations"`
	RetentionPressure   bool                     `json:"retention_pressure,omitempty"`
	ControlDegraded     bool                     `json:"control_degraded,omitempty"`
	Busy                bool                     `json:"busy,omitempty"`
	LastSuccess         sdkreload.Result         `json:"last_success"`
	LastFailure         sdkreload.Result         `json:"last_failure"`
	LastResult          sdkreload.Result         `json:"last_result"`
	History             []sdkreload.HistoryEntry `json:"history,omitempty"`
}

func reloadStatusDTO(st sdkreload.Status) reloadDiagDTO {
	return reloadDiagDTO{
		ActiveGeneration:    st.ActiveGeneration,
		ModelGeneration:     st.ModelGeneration,
		SourceIntegrity:     st.SourceIntegrity,
		RetainedGenerations: st.RetainedGenerations,
		RetentionPressure:   st.RetentionPressure,
		ControlDegraded:     st.ControlDegraded,
		Busy:                st.Busy,
		LastSuccess:         st.LastSuccess,
		LastFailure:         st.LastFailure,
		LastResult:          st.LastResult,
		History:             st.History,
	}
}
