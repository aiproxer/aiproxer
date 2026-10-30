package codex

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"sync"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

type toolPayload struct {
	Type        string         `json:"type"`
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters"`
	Strict      bool           `json:"strict"`
}

func buildTools(tools []aipapi.ToolDef, toolStrictDisabled bool) ([]toolPayload, error) {
	out := make([]toolPayload, 0, len(tools))
	for _, t := range tools {
		var schema map[string]any
		if len(t.Parameters) > 0 {
			if err := json.Unmarshal(t.Parameters, &schema); err != nil {
				return nil, fmt.Errorf("%s: tool %q parameters: %w", ID, t.Name, err)
			}
		}
		if schema == nil {
			schema = map[string]any{}
		}
		schema, strict := normalizeToolSchemaForCodex(schema)
		strict = strict && !toolStrictDisabled
		if codexToolDebugEnabled() {
			slog.Debug("codex.tool_schema", "tool", t.Name, "strict", strict, "schema_keys", sortedSchemaKeys(schema))
		}
		out = append(out, toolPayload{
			Type:        "function",
			Name:        t.Name,
			Description: t.Description,
			Parameters:  schema,
			Strict:      strict,
		})
	}
	return out, nil
}

func sortedSchemaKeys(schema map[string]any) []string {
	keys := make([]string, 0, len(schema))
	for k := range schema {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

var (
	codexToolDebugEnabledValue = sync.OnceValue(func() bool {
		return os.Getenv("AIP_CODEX_DEBUG_TOOLS") == "1"
	})
	codexToolDeltaDebugEnabledValue = sync.OnceValue(func() bool {
		return os.Getenv("AIP_CODEX_DEBUG_TOOL_DELTAS") == "1"
	})
)

func codexToolDebugEnabled() bool {
	return codexToolDebugEnabledValue()
}

func codexToolDeltaDebugEnabled() bool {
	return codexToolDeltaDebugEnabledValue()
}
