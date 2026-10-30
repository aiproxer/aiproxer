package repair

import (
	"encoding/json"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcall"
)

// deterministicRootPendingValue resolves only an exact root properties entry.
// The effective schema document is the same bounded, pre-scanned document held
// by the compiled schema, so pending repair and final validation share one schema
// interpretation and one cache compilation.
func deterministicRootPendingValue(compiled *CompiledSchema, property string) ([]byte, string, bool, error) {
	if compiled == nil || compiled.orderedDocument == nil {
		return nil, "", false, nil
	}
	effective, err := effectiveSchemaObject(compiled.orderedDocument, compiled.orderedDocument, 0)
	if err != nil {
		return nil, "", false, err
	}
	if effective == nil {
		return nil, "", false, nil
	}
	_, props, ok := objectFields(effective["properties"])
	if !ok {
		return nil, "", false, nil
	}
	propertySchema, ok := asSchemaMap(props[property])
	if !ok {
		return nil, "", false, nil
	}
	value, reason, ok, err := deterministicFill(propertySchema, compiled.orderedDocument, 0)
	if err != nil {
		return nil, "", false, err
	}
	if !ok {
		return nil, "", false, nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, "", false, &repairError{reason: toolcall.ReasonUnrepairable}
	}
	return encoded, reason, true, nil
}
