package catalog

import (
	"encoding/json"
	"fmt"
	"strings"
)

type modelsResponse struct {
	Data   []modelRecord `json:"data"`
	Models []modelRecord `json:"models"`
}

type modelRecord struct {
	ID           string          `json:"id"`
	Created      json.RawMessage `json:"created"`
	Object       string          `json:"object"`
	OwnedBy      string          `json:"owned_by"`
	Endpoint     string          `json:"endpoint"`
	AISDKPackage string          `json:"ai_sdk_package"`
	NPM          string          `json:"npm"`
	Name         string          `json:"name"`
	DisplayName  string          `json:"display_name"`
}

func ParseModelsResponse(body []byte) ([]ModelEntry, error) {
	body = []byte(strings.TrimSpace(string(body)))
	if len(body) == 0 {
		return nil, fmt.Errorf("opencode: empty models response")
	}

	var records []modelRecord
	if body[0] == '[' {
		if err := json.Unmarshal(body, &records); err != nil {
			return nil, fmt.Errorf("opencode: decode models array: %w", err)
		}
	} else {
		var payload modelsResponse
		if err := json.Unmarshal(body, &payload); err != nil {
			return nil, fmt.Errorf("opencode: decode models response: %w", err)
		}
		switch {
		case len(payload.Data) > 0:
			records = payload.Data
		case len(payload.Models) > 0:
			records = payload.Models
		}
	}

	entries := make([]ModelEntry, 0, len(records))
	for _, row := range records {
		entry, ok := modelEntryFromRecord(row)
		if !ok {
			continue
		}
		entries = append(entries, entry)
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("opencode: models response contained no usable models")
	}
	return entries, nil
}

func modelEntryFromRecord(row modelRecord) (ModelEntry, bool) {
	rawID := strings.TrimSpace(row.ID)
	if rawID == "" || isSchemaPlaceholderModel(row) {
		return ModelEntry{}, false
	}
	display := strings.TrimSpace(row.DisplayName)
	if display == "" {
		display = strings.TrimSpace(row.Name)
	}
	if display == "" {
		display = rawID
	}
	aiSDK := strings.TrimSpace(row.AISDKPackage)
	if aiSDK == "" {
		aiSDK = strings.TrimSpace(row.NPM)
	}
	return ModelEntry{
		RawID:        rawID,
		Endpoint:     strings.TrimSpace(row.Endpoint),
		AISDKPackage: aiSDK,
		DisplayName:  display,
	}, true
}

func isSchemaPlaceholderModel(row modelRecord) bool {
	return strings.TrimSpace(row.ID) == "string" &&
		strings.TrimSpace(row.Object) == "string" &&
		strings.TrimSpace(row.OwnedBy) == "string" &&
		jsonStringEquals(row.Created, "int")
}

func jsonStringEquals(raw json.RawMessage, want string) bool {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return false
	}
	return strings.TrimSpace(s) == want
}
