package modelsdev

import "encoding/json"

// wireProvider is the decoded subset of a models.dev provider record used at runtime.
type wireProvider struct {
	ID     string          `json:"id"`
	Models json.RawMessage `json:"models"`
}

// wireModel is the decoded subset of a models.dev model record used at runtime.
type wireModel struct {
	ID               string          `json:"id"`
	Modalities       json.RawMessage `json:"modalities,omitempty"`
	Reasoning        *bool           `json:"reasoning,omitempty"`
	ToolCall         *bool           `json:"tool_call,omitempty"`
	StructuredOutput *bool           `json:"structured_output,omitempty"`
	Limit            *wireLimit      `json:"limit,omitempty"`
}

type wireLimit struct {
	Context json.Number `json:"context"`
	Input   json.Number `json:"input"`
	Output  json.Number `json:"output"`
}

type wireModalities struct {
	Input []string `json:"input"`
}
