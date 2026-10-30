package openresponsescompat

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/routing"
	proto "github.com/aiproxer/aiproxer/internal/plugins/protocols/openresponses"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestEncodeOutboundRequest_RegressionAndOptions(t *testing.T) {
	t.Parallel()

	call := aipapi.Call{
		Tools: []aipapi.ToolDef{{
			Name:        "get_weather",
			Description: "Get weather details",
			Parameters:  json.RawMessage(`{"type":"object"}`),
		}},
		ToolChoice: aipapi.ToolChoice{
			Mode:         aipapi.ToolChoiceAuto,
			AllowedTools: []string{"get_weather"},
		},
		Options: aipapi.GenerationOptions{
			Temperature:       new(float64),
			TopP:              new(float64),
			MaxOutputTokens:   new(int),
			ParallelToolCalls: new(bool),
			ResponseMIMEType:  "application/json",
			ReasoningEffort:   "medium",
		},
		Items: []aipapi.Item{
			{
				Kind:   aipapi.ItemKindMessage,
				ID:     "item-1",
				Status: aipapi.ItemStatusCompleted,
				Role:   aipapi.RoleUser,
				Content: []aipapi.ContentPart{
					{Kind: aipapi.ContentPartText, Text: "Hello world"},
				},
			},
		},
		Extensions: map[string]json.RawMessage{
			"custom_field": json.RawMessage(`{"x":1}`),
		},
	}
	*call.Options.Temperature = 0.7
	*call.Options.TopP = 0.9
	*call.Options.MaxOutputTokens = 256
	*call.Options.ParallelToolCalls = true

	expectedStreamTrue := map[string]any{
		"model":  "my-model",
		"stream": true,
		"input": []any{
			map[string]any{
				"id":     "item-1",
				"type":   "message",
				"status": "completed",
				"role":   "user",
				"content": []any{
					map[string]any{
						"type": "input_text",
						"text": "Hello world",
					},
				},
			},
		},
		"tools": []any{
			map[string]any{
				"type":        "function",
				"name":        "get_weather",
				"description": "Get weather details",
				"parameters":  map[string]any{"type": "object"},
			},
		},
		"tool_choice": map[string]any{
			"type": "allowed_tools",
			"tools": []any{
				map[string]any{
					"type": "function",
					"name": "get_weather",
				},
			},
			"mode": "auto",
		},
		"temperature":         0.7,
		"top_p":               0.9,
		"max_output_tokens":   float64(256),
		"parallel_tool_calls": true,
		"text": map[string]any{
			"format": map[string]any{
				"type": "json_object",
			},
		},
		"reasoning": map[string]any{
			"effort": "medium",
		},
	}

	t.Run("StreamTrueIncludeExtensionsFalse", func(t *testing.T) {
		actualBytes, err := proto.EncodeOutboundRequest(call, proto.OutboundEncodeOptions{
			Model:             "my-model",
			Stream:            true,
			IncludeExtensions: false,
		})
		if err != nil {
			t.Fatalf("EncodeOutboundRequest failed: %v", err)
		}

		var actualMap map[string]any
		if err := json.Unmarshal(actualBytes, &actualMap); err != nil {
			t.Fatalf("unmarshal actual failed: %v", err)
		}

		if !reflect.DeepEqual(actualMap, expectedStreamTrue) {
			t.Errorf("JSON mismatch for Stream=true, IncludeExtensions=false!\nexpected: %+v\nactual:   %+v", expectedStreamTrue, actualMap)
		}
	})

	t.Run("StreamFalseIncludeExtensionsFalse", func(t *testing.T) {
		expectedStreamFalse := make(map[string]any)
		for k, v := range expectedStreamTrue {
			if k != "stream" {
				expectedStreamFalse[k] = v
			}
		}

		actualBytes, err := proto.EncodeOutboundRequest(call, proto.OutboundEncodeOptions{
			Model:             "my-model",
			Stream:            false,
			IncludeExtensions: false,
		})
		if err != nil {
			t.Fatalf("EncodeOutboundRequest failed: %v", err)
		}

		var actualMap map[string]any
		if err := json.Unmarshal(actualBytes, &actualMap); err != nil {
			t.Fatalf("unmarshal actual failed: %v", err)
		}

		if !reflect.DeepEqual(actualMap, expectedStreamFalse) {
			t.Errorf("JSON mismatch for Stream=false, IncludeExtensions=false!\nexpected: %+v\nactual:   %+v", expectedStreamFalse, actualMap)
		}
		if _, streamExists := actualMap["stream"]; streamExists {
			t.Errorf("expected stream key to be omitted when false, but got stream=%v", actualMap["stream"])
		}
	})

	t.Run("StreamFalseIncludeExtensionsTrue", func(t *testing.T) {
		actualBytes, err := proto.EncodeOutboundRequest(call, proto.OutboundEncodeOptions{
			Model:             "my-model",
			Stream:            false,
			IncludeExtensions: true,
		})
		if err != nil {
			t.Fatalf("EncodeOutboundRequest failed: %v", err)
		}

		var actualMap map[string]any
		if err := json.Unmarshal(actualBytes, &actualMap); err != nil {
			t.Fatalf("unmarshal actual failed: %v", err)
		}

		extVal, ok := actualMap["custom_field"]
		if !ok {
			t.Errorf("expected extension custom_field to be present when IncludeExtensions=true")
		} else {
			expectedExt := map[string]any{"x": float64(1)}
			if !reflect.DeepEqual(extVal, expectedExt) {
				t.Errorf("extension value mismatch: got %+v, want %+v", extVal, expectedExt)
			}
		}
	})

	t.Run("VerbosityRejection", func(t *testing.T) {
		badCall := call
		badCall.Options.Verbosity = "high"

		_, err := proto.EncodeOutboundRequest(badCall, proto.OutboundEncodeOptions{
			Model:             "my-model",
			Stream:            false,
			IncludeExtensions: false,
		})
		if err == nil {
			t.Errorf("expected error when options contain non-empty verbosity, got nil")
		}
	})
}

func TestBuildCreateRequest_LegacyRegression(t *testing.T) {
	t.Parallel()

	call := aipapi.Call{
		Tools: []aipapi.ToolDef{{
			Name:        "get_weather",
			Description: "Get weather details",
			Parameters:  json.RawMessage(`{"type":"object"}`),
		}},
		ToolChoice: aipapi.ToolChoice{
			Mode:         aipapi.ToolChoiceAuto,
			AllowedTools: []string{"get_weather"},
		},
		Options: aipapi.GenerationOptions{
			Temperature:       new(float64),
			TopP:              new(float64),
			MaxOutputTokens:   new(int),
			ParallelToolCalls: new(bool),
			ResponseMIMEType:  "application/json",
			ReasoningEffort:   "medium",
		},
		Items: []aipapi.Item{
			{
				Kind:   aipapi.ItemKindMessage,
				ID:     "item-1",
				Status: aipapi.ItemStatusCompleted,
				Role:   aipapi.RoleUser,
				Content: []aipapi.ContentPart{
					{Kind: aipapi.ContentPartText, Text: "Hello world"},
				},
			},
		},
		Extensions: map[string]json.RawMessage{
			"custom_field": json.RawMessage(`{"x":1}`),
		},
	}
	*call.Options.Temperature = 0.7
	*call.Options.TopP = 0.9
	*call.Options.MaxOutputTokens = 256
	*call.Options.ParallelToolCalls = true

	spec := testSpec()
	cand := routing.AttemptCandidate{
		Primary: routing.Primary{
			Model: "my-model",
		},
	}

	body, err := buildCreateRequest("test-id", spec, call, cand)
	if err != nil {
		t.Fatalf("buildCreateRequest failed: %v", err)
	}

	var actualMap map[string]any
	if err := json.Unmarshal(body, &actualMap); err != nil {
		t.Fatalf("unmarshal actual failed: %v", err)
	}

	expectedMap := map[string]any{
		"model": "my-model",
		"input": []any{
			map[string]any{
				"id":     "item-1",
				"type":   "message",
				"status": "completed",
				"role":   "user",
				"content": []any{
					map[string]any{
						"type": "input_text",
						"text": "Hello world",
					},
				},
			},
		},
		"tools": []any{
			map[string]any{
				"type":        "function",
				"name":        "get_weather",
				"description": "Get weather details",
				"parameters":  map[string]any{"type": "object"},
			},
		},
		"tool_choice": map[string]any{
			"type": "allowed_tools",
			"tools": []any{
				map[string]any{
					"type": "function",
					"name": "get_weather",
				},
			},
			"mode": "auto",
		},
		"temperature":         0.7,
		"top_p":               0.9,
		"max_output_tokens":   float64(256),
		"parallel_tool_calls": true,
		"text": map[string]any{
			"format": map[string]any{
				"type": "json_object",
			},
		},
		"reasoning": map[string]any{
			"effort": "medium",
		},
	}

	if !reflect.DeepEqual(actualMap, expectedMap) {
		t.Errorf("buildCreateRequest output mismatch!\nexpected: %+v\nactual:   %+v", expectedMap, actualMap)
	}
}
