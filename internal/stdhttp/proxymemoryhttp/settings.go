// The settings resource of design §4: the authenticated, owner-scoped read,
// update and delete of the caller's own per-phase overrides.
//
// The resource is mounted behind the same request-plane access-auth stack as
// the rest of the memory surface: authentication first, then eligibility,
// then routing. The owner is the authenticated principal of the frozen
// request scope, so the body and the query string carry no owner, tenant,
// principal or account authority at all.
package proxymemoryhttp

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/aiproxer/aiproxer/internal/plugins/features/proxymemory"
)

// settingsSegment is the settings resource's path segment under the memory
// mount. The classifier reads it before the per-scope item route, so a
// settings request is never parsed as a scope identifier.
const settingsSegment = "/settings"

// maxSettingsBodyBytes caps one settings write document. A settings document
// names two phases and three bounded tokens each, so the cap is generous and
// still bounded.
const maxSettingsBodyBytes = 16 << 10

// settingsJSON is the settings document on the wire (design §4).
type settingsJSON struct {
	// Overrides are the owner's stored, unprojected per-phase overrides: the
	// exact values the store holds.
	Overrides overridesJSON `json:"overrides"`
	// Phases carries the per-phase effective state in phase order.
	Phases []phaseSettingsJSON `json:"phases"`
}

// overridesJSON holds one owner's stored overrides per phase.
type overridesJSON struct {
	Extraction    phaseValuesJSON `json:"extraction"`
	Consolidation phaseValuesJSON `json:"consolidation"`
}

// phaseValuesJSON holds the three owner-selectable values of one phase.
type phaseValuesJSON struct {
	Backend         string `json:"backend"`
	Model           string `json:"model"`
	ReasoningEffort string `json:"reasoning_effort"`
}

// phaseSettingsJSON is one phase's effective state on the wire.
type phaseSettingsJSON struct {
	// Phase names the phase: extraction or consolidation.
	Phase string `json:"phase"`
	// Defaults are the administrator's baseline for the phase: the target the
	// phase runs with when the owner has no override, so the owner can tell
	// what clearing an override would restore.
	Defaults phaseValuesJSON `json:"defaults"`
	// Effective is the target the phase runs with: the locked operator
	// value, else the permitted owner value, else the operator default.
	Effective phaseValuesJSON `json:"effective"`
	// Locks are the operator's per-field lock flags.
	Locks locksJSON `json:"locks"`
}

// locksJSON holds the operator's per-field lock flags of one phase.
type locksJSON struct {
	Backend         bool `json:"backend"`
	Model           bool `json:"model"`
	ReasoningEffort bool `json:"reasoning_effort"`
}

// settingsBodyJSON is the settings write document.
//
// Every member is optional, and an absent member, a JSON null and an empty
// string all mean the same thing: the owner names no value for that field.
// That is the existing policy's empty-value semantics, where an unnamed field
// resolves to the operator default.
type settingsBodyJSON struct {
	Extraction    phaseBodyJSON `json:"extraction"`
	Consolidation phaseBodyJSON `json:"consolidation"`
}

// phaseBodyJSON holds the three owner-selectable values one phase may name.
type phaseBodyJSON struct {
	Backend         *string `json:"backend"`
	Model           *string `json:"model"`
	ReasoningEffort *string `json:"reasoning_effort"`
}

// overrides renders the phase's named values. An absent or null member names
// no value, exactly like an empty one.
func (p phaseBodyJSON) overrides() proxymemory.TargetOverrides {
	return proxymemory.TargetOverrides{
		Backend:         namedValue(p.Backend),
		Model:           namedValue(p.Model),
		ReasoningEffort: namedValue(p.ReasoningEffort),
	}
}

// namedValue returns the named value, or the empty value for an absent or
// null member.
func namedValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// handleSettings serves the read of the caller's own settings.
func (h *handler) handleSettings(w http.ResponseWriter, r *http.Request, p proxymemory.Principal) {
	projection, err := h.svc.Settings(r.Context(), p)
	if err != nil {
		h.writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toSettingsJSON(projection))
}

// handleSettingsUpdate serves the update of the caller's own settings.
//
// The write replaces the caller's settings: the document carries the
// caller's complete set, and a phase or a field the document does not name
// resolves to the operator default. A refused write persists nothing.
func (h *handler) handleSettingsUpdate(w http.ResponseWriter, r *http.Request, p proxymemory.Principal) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxSettingsBodyBytes))
	if err != nil {
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			writeError(w, http.StatusRequestEntityTooLarge, "request_too_large")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid_input")
		return
	}
	proposed, err := decodeSettingsBody(body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_input")
		return
	}
	projection, err := h.svc.UpdateSettings(r.Context(), p, proposed)
	if err != nil {
		h.writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toSettingsJSON(projection))
}

// handleSettingsDelete serves the deletion of the caller's own settings.
func (h *handler) handleSettingsDelete(w http.ResponseWriter, r *http.Request, p proxymemory.Principal) {
	if err := h.svc.DeleteSettings(r.Context(), p); err != nil {
		h.writeServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// decodeSettingsBody decodes one settings write document.
//
// The document is one JSON object and nothing else, and an unknown member is
// refused: the body can therefore never carry an owner, a tenant, an account
// or any other member the resource does not own.
func decodeSettingsBody(body []byte) (proxymemory.OwnerOverrides, error) {
	var in *settingsBodyJSON
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&in); err != nil {
		return proxymemory.OwnerOverrides{}, err
	}
	if in == nil {
		return proxymemory.OwnerOverrides{}, errors.New("proxymemoryhttp: the settings document must be an object")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return proxymemory.OwnerOverrides{}, errors.New("proxymemoryhttp: the settings document must be one object")
	}
	return proxymemory.OwnerOverrides{
		Extraction:    in.Extraction.overrides(),
		Consolidation: in.Consolidation.overrides(),
	}, nil
}

// toSettingsJSON maps the settings projection onto the wire document.
func toSettingsJSON(projection proxymemory.SettingsProjection) settingsJSON {
	out := settingsJSON{
		Overrides: overridesJSON{
			Extraction:    toOverridesJSON(projection.Stored.Extraction),
			Consolidation: toOverridesJSON(projection.Stored.Consolidation),
		},
		Phases: make([]phaseSettingsJSON, 0, len(projection.Phases)),
	}
	for _, phase := range projection.Phases {
		out.Phases = append(out.Phases, phaseSettingsJSON{
			Phase:     string(phase.Phase),
			Defaults:  toValuesJSON(phase.Defaults),
			Effective: toValuesJSON(phase.Effective),
			Locks: locksJSON{
				Backend:         phase.Locks.Backend,
				Model:           phase.Locks.Model,
				ReasoningEffort: phase.Locks.ReasoningEffort,
			},
		})
	}
	return out
}

// toValuesJSON maps the three owner-selectable values of one phase onto the
// wire. An unnamed value is the empty value, which is what the stored row
// holds and what the existing policy reads as "the owner named none".
func toValuesJSON(values proxymemory.PhaseTarget) phaseValuesJSON {
	return phaseValuesJSON{
		Backend:         values.Backend,
		Model:           values.Model,
		ReasoningEffort: values.ReasoningEffort,
	}
}

// toOverridesJSON maps the stored overrides of one phase onto the wire.
func toOverridesJSON(overrides proxymemory.TargetOverrides) phaseValuesJSON {
	return phaseValuesJSON{
		Backend:         overrides.Backend,
		Model:           overrides.Model,
		ReasoningEffort: overrides.ReasoningEffort,
	}
}

// writePolicyError writes one bounded policy refusal.
//
// The body names the phase and the field from the closed vocabulary and
// never the value, so the refusal can be logged without carrying an
// owner-supplied string. A refusal whose phase or field is outside the closed
// vocabulary is answered with the content-free body instead.
func writePolicyError(w http.ResponseWriter, err *proxymemory.PolicyError) {
	if !knownPolicyPhase(err.Phase) || !knownPolicyField(err.Field) {
		writeError(w, http.StatusConflict, "policy_refused")
		return
	}
	writeJSON(w, http.StatusConflict, map[string]string{
		"error": "policy_refused",
		"phase": string(err.Phase),
		"field": err.Field,
	})
}

// knownPolicyPhase reports whether phase is one of the two governed phases.
func knownPolicyPhase(phase proxymemory.Phase) bool {
	return phase == proxymemory.PhaseExtraction || phase == proxymemory.PhaseConsolidation
}

// knownPolicyField reports whether field is one of the governed fields.
func knownPolicyField(field string) bool {
	switch field {
	case proxymemory.FieldBackend, proxymemory.FieldModel, proxymemory.FieldReasoningEffort, proxymemory.FieldRoute:
		return true
	}
	return false
}
