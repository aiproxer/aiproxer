package modelregistry

import (
	"encoding/json"
	"slices"
	"strings"
)

// OpenAIModelList is the minimal OpenAI-compatible GET /v1/models response body.
type OpenAIModelList struct {
	Object string        `json:"object"`
	Data   []OpenAIModel `json:"data"`
}

// OpenAIModel is one advertised model row. IDs are instance-pinned canonical
// selectors (`<backend-instance>:<canonicalID>`). Native IDs are never exposed.
type OpenAIModel struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	OwnedBy string `json:"owned_by"`
}

func openAIModelID(backendID, canonicalID string) string {
	return strings.TrimSpace(backendID) + ":" + strings.TrimSpace(canonicalID)
}

// BuildOpenAIModelsList converts registry rows into a stable, deduplicated
// OpenAI list. Ordering is by id then owned_by. owned_by is the backend kind.
func BuildOpenAIModelsList(models []BackendModel) OpenAIModelList {
	type key struct{ id, ownedBy string }
	seen := make(map[key]struct{}, len(models))
	data := make([]OpenAIModel, 0, len(models))
	for _, m := range models {
		backendID := strings.TrimSpace(m.BackendID)
		canonical := strings.TrimSpace(m.CanonicalID)
		if backendID == "" || canonical == "" {
			continue
		}
		ownedBy := strings.TrimSpace(m.Kind)
		if ownedBy == "" {
			ownedBy = backendID
		}
		id := openAIModelID(backendID, canonical)
		k := key{id: id, ownedBy: ownedBy}
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		data = append(data, OpenAIModel{ID: id, Object: "model", OwnedBy: ownedBy})
	}
	slices.SortFunc(data, func(a, b OpenAIModel) int {
		if c := strings.Compare(a.ID, b.ID); c != 0 {
			return c
		}
		return strings.Compare(a.OwnedBy, b.OwnedBy)
	})
	if data == nil {
		data = []OpenAIModel{}
	}
	return OpenAIModelList{Object: "list", Data: data}
}

// MarshalOpenAIModelsListJSON returns the JSON body for GET /v1/models.
func MarshalOpenAIModelsListJSON(models []BackendModel) ([]byte, error) {
	return json.Marshal(BuildOpenAIModelsList(models))
}
