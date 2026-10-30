package secretguard

import (
	"bytes"
	"encoding/json"
	"errors"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// validateJSONMirrors admits only mirrors of the authoritative Text payload.
// Ignore whitespace but preserve key order, duplicates and escapes: decoding to
// a map could hide a secret-bearing duplicate key in an inconsistent mirror.
// This is a guard-boundary check, not scanning unrelated annotation metadata.
func validateJSONMirrors(call *aipapi.Call) error {
	if !call.HasItemAuthority() {
		return nil
	}
	for _, item := range call.Items {
		var parts []aipapi.ContentPart
		switch item.Kind {
		case aipapi.ItemKindMessage:
			if item.Role != aipapi.RoleUser && item.Role != aipapi.RoleTool {
				continue
			}
			parts = item.Content
		case aipapi.ItemKindToolResult:
			if item.ToolResult != nil {
				parts = item.ToolResult.Parts
			}
		}
		for _, part := range parts {
			if part.Kind != aipapi.ContentPartJSON || part.Annotation == nil || part.Annotation.Type != "json_content" {
				continue
			}
			var text, mirror bytes.Buffer
			if json.Compact(&text, []byte(part.Text)) != nil || json.Compact(&mirror, part.Annotation.Data) != nil || !bytes.Equal(text.Bytes(), mirror.Bytes()) {
				return errors.New("secretguard: inconsistent JSON content mirror")
			}
		}
	}
	return nil
}
