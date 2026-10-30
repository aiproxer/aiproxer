package repair

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcall"
)

const maxRepairDepth = 64

type repairError struct {
	reason string
}

func (e *repairError) Error() string {
	if e == nil {
		return "toolcallrepair: repair error"
	}
	return "toolcallrepair: " + e.reason
}

type repairState struct {
	rootSchema any
	reason     string
	changed    bool
	ops        int
}

func (st *repairState) note(reason string) {
	st.changed = true
	if st.reason == "" {
		st.reason = reason
	}
}

func checkRepairWithContext(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return &repairError{reason: toolcall.ReasonCanceled}
	}
	return nil
}

func repairArgsJSON(ctx context.Context, args []byte, schema json.RawMessage, maxArgsBytes int, schemaLimits SchemaLimits) (out []byte, reason string, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, "", &repairError{reason: toolcall.ReasonCanceled}
	}
	if err := preflightArgsJSON(ctx, args, maxArgsBytes); err != nil {
		return nil, "", &repairError{reason: mapEngineArgsShapeReason(err)}
	}
	if err := preflightSchemaJSON(ctx, schema, schemaLimits); err != nil {
		if reason := mapEngineArgsShapeReason(err); reason == toolcall.ReasonCanceled {
			return nil, "", &repairError{reason: reason}
		}
		return nil, "", &repairError{reason: toolcall.ReasonSchemaInvalid}
	}
	schemaDoc, err := parseOrderedJSON(schema)
	if err != nil {
		return nil, "", &repairError{reason: toolcall.ReasonSchemaInvalid}
	}
	return repairPreflightedArgsJSONDocument(ctx, args, schemaDoc, maxArgsBytes)
}

// repairPreflightedArgsJSON materializes and repairs args/schema that have already
// passed preflightArgsJSON / preflightSchemaJSON (or schema cache compile) under
// the caller's effective policy. Callers must not skip those guards.
func repairPreflightedArgsJSON(ctx context.Context, args []byte, schema json.RawMessage) (out []byte, reason string, err error) {
	schemaDoc, err := parseOrderedJSON(schema)
	if err != nil {
		return nil, "", &repairError{reason: toolcall.ReasonSchemaInvalid}
	}
	return repairPreflightedArgsJSONDocument(ctx, args, schemaDoc, DefaultMaxArgsBytes)
}

func repairPreflightedArgsJSONDocument(ctx context.Context, args []byte, schemaDoc any, maxArgsBytes int) (out []byte, reason string, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, "", &repairError{reason: toolcall.ReasonCanceled}
	}
	if err := preflightArgsJSON(ctx, args, maxArgsBytes); err != nil {
		return nil, "", &repairError{reason: mapEngineArgsShapeReason(err)}
	}
	root, err := parseOrderedJSON(args)
	if err != nil {
		return nil, "", &repairError{reason: toolcall.ReasonUnrepairable}
	}
	if err := ctx.Err(); err != nil {
		return nil, "", &repairError{reason: toolcall.ReasonCanceled}
	}
	st := &repairState{rootSchema: schemaDoc}
	repaired, err := repairValue(ctx, root, schemaDoc, 0, st)
	if err != nil {
		return nil, "", err
	}
	if !st.changed {
		return args, "", nil
	}
	encoded, err := json.Marshal(repaired)
	if err != nil {
		return nil, "", &repairError{reason: toolcall.ReasonUnrepairable}
	}
	return encoded, st.reason, nil
}

func repairValue(ctx context.Context, v any, schema any, depth int, st *repairState) (any, error) {
	if err := checkRepairWithContext(ctx); err != nil {
		return nil, err
	}
	if depth > maxRepairDepth {
		return nil, &repairError{reason: toolcall.ReasonUnrepairable}
	}
	st.ops++
	if st.ops > 4096 {
		return nil, &repairError{reason: toolcall.ReasonUnrepairable}
	}
	sch, err := effectiveSchemaObject(schema, st.rootSchema, depth)
	if err != nil {
		return nil, err
	}
	if sch == nil {
		return v, nil
	}
	if err := checkScalarCoercion(v, sch); err != nil {
		return nil, err
	}
	switch typed := v.(type) {
	case orderedObject:
		return repairObject(ctx, typed, sch, depth, st)
	case []any:
		return repairArray(ctx, typed, sch, depth, st)
	default:
		return v, nil
	}
}

func repairObject(ctx context.Context, obj orderedObject, sch map[string]any, depth int, st *repairState) (any, error) {
	propsVal := sch["properties"]
	_, props, propsOK := objectFields(propsVal)
	if !propsOK {
		props = map[string]any{}
	}
	normCounts := make(map[string]int, len(props))
	normFirst := make(map[string]string, len(props))
	for name := range props {
		n := NormalizeASCIIName(name)
		if n == "" {
			continue
		}
		normCounts[n]++
		if _, ok := normFirst[n]; !ok {
			normFirst[n] = name
		}
	}
	uniqueNorm := make(map[string]string, len(normFirst))
	for n, count := range normCounts {
		if count == 1 {
			uniqueNorm[n] = normFirst[n]
		}
	}

	addProps, hasAddProps := sch["additionalProperties"]
	addPropsFalse := hasAddProps && addProps == false
	patternProps := sch["patternProperties"]

	out := orderedObject{values: make(map[string]any, len(obj.keys))}
	seen := make(map[string]struct{}, len(obj.keys))

	for _, key := range obj.keys {
		if err := checkRepairWithContext(ctx); err != nil {
			return nil, err
		}
		val := obj.values[key]
		canon := key
		propSchema, known := props[key]
		if !known {
			if n := NormalizeASCIIName(key); n != "" {
				if count := normCounts[n]; count > 1 {
					return nil, &repairError{reason: toolcall.ReasonAmbiguousProperty}
				}
				if match, ok := uniqueNorm[n]; ok {
					canon = match
					propSchema = props[match]
					known = true
					if canon != key {
						st.note(toolcall.ReasonPropertyRenamed)
					}
				}
			}
		}
		if !known {
			matched, err := matchPatternSchemas(key, patternProps)
			if err != nil {
				return nil, err
			}
			switch len(matched) {
			case 0:
				if addPropsFalse {
					st.note(toolcall.ReasonAdditionalPropertyRemoved)
					continue
				}
				if addSchema, ok := asSchemaMap(addProps); ok {
					repaired, err := repairValue(ctx, val, addSchema, depth+1, st)
					if err != nil {
						return nil, err
					}
					val = repaired
				}
			case 1:
				repaired, err := repairValue(ctx, val, matched[0], depth+1, st)
				if err != nil {
					return nil, err
				}
				val = repaired
			default:
				same, err := schemasJSONEqual(matched)
				if err != nil || !same {
					return nil, &repairError{reason: toolcall.ReasonUnrepairable}
				}
				repaired, err := repairValue(ctx, val, matched[0], depth+1, st)
				if err != nil {
					return nil, err
				}
				val = repaired
			}
			if _, exists := out.values[canon]; !exists {
				out.keys = append(out.keys, canon)
			}
			out.values[canon] = val
			seen[canon] = struct{}{}
			continue
		}
		repaired, err := repairValue(ctx, val, propSchema, depth+1, st)
		if err != nil {
			return nil, err
		}
		if _, exists := out.values[canon]; !exists {
			out.keys = append(out.keys, canon)
		} else if canon != key {
			return nil, &repairError{reason: toolcall.ReasonUnrepairable}
		}
		out.values[canon] = repaired
		seen[canon] = struct{}{}
	}

	insertOrder := propertyInsertOrder(sch, props)
	for _, name := range insertOrder {
		if _, ok := seen[name]; ok {
			continue
		}
		propSchema, ok := asSchemaMap(props[name])
		if !ok {
			continue
		}
		val, reason, ok, err := deterministicFill(propSchema, st.rootSchema, 0)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		out.keys = append(out.keys, name)
		out.values[name] = val
		seen[name] = struct{}{}
		st.note(reason)
	}
	return out, nil
}

func matchPatternSchemas(key string, patternProps any) ([]any, error) {
	keys, values, ok := objectFields(patternProps)
	if !ok || len(keys) == 0 {
		return nil, nil
	}
	sorted := append([]string(nil), keys...)
	slices.Sort(sorted)
	var matched []any
	for _, pat := range sorted {
		re, err := regexp.Compile(pat)
		if err != nil {
			return nil, &repairError{reason: toolcall.ReasonUnrepairable}
		}
		if re.MatchString(key) {
			matched = append(matched, values[pat])
		}
	}
	return matched, nil
}

func schemasJSONEqual(schemas []any) (bool, error) {
	if len(schemas) < 2 {
		return true, nil
	}
	first, err := json.Marshal(schemas[0])
	if err != nil {
		return false, err
	}
	for i := 1; i < len(schemas); i++ {
		next, err := json.Marshal(schemas[i])
		if err != nil {
			return false, err
		}
		if !bytes.Equal(first, next) {
			return false, nil
		}
	}
	return true, nil
}

func propertyInsertOrder(sch map[string]any, props map[string]any) []string {
	order := make([]string, 0, len(props))
	seen := make(map[string]struct{}, len(props))
	if req, ok := sch["required"].([]any); ok {
		for _, r := range req {
			name, ok := r.(string)
			if !ok {
				continue
			}
			if _, ok := props[name]; !ok {
				continue
			}
			if _, ok := seen[name]; ok {
				continue
			}
			seen[name] = struct{}{}
			order = append(order, name)
		}
	}
	rest := make([]string, 0, len(props))
	for name := range props {
		if _, ok := seen[name]; ok {
			continue
		}
		rest = append(rest, name)
	}
	slices.Sort(rest)
	return append(order, rest...)
}

func deterministicFill(propSchema map[string]any, root any, depth int) (any, string, bool, error) {
	if depth > maxRepairDepth {
		return nil, "", false, &repairError{reason: toolcall.ReasonUnrepairable}
	}
	if ref, ok := propSchema["$ref"].(string); ok {
		if hasRefSiblings(propSchema) {
			return nil, "", false, &repairError{reason: toolcall.ReasonUnrepairable}
		}
		resolved, err := resolveLocalRef(root, ref)
		if err != nil {
			return nil, "", false, &repairError{reason: toolcall.ReasonUnrepairable}
		}
		resolvedMap, ok := asSchemaMap(resolved)
		if !ok {
			return nil, "", false, &repairError{reason: toolcall.ReasonUnrepairable}
		}
		return deterministicFill(resolvedMap, root, depth+1)
	}
	var raw any
	var reason string
	switch {
	case hasKey(propSchema, "const"):
		raw = propSchema["const"]
		reason = toolcall.ReasonConstInserted
	default:
		if enum, ok := propSchema["enum"].([]any); ok && len(enum) == 1 {
			raw = enum[0]
			reason = toolcall.ReasonEnumInserted
		} else if hasKey(propSchema, "default") {
			raw = propSchema["default"]
			reason = toolcall.ReasonDefaultInserted
		} else {
			return nil, "", false, nil
		}
	}
	val, err := materializeFillValue(raw)
	if err != nil {
		return nil, "", false, &repairError{reason: toolcall.ReasonUnrepairable}
	}
	return val, reason, true, nil
}

func hasKey(m map[string]any, key string) bool {
	_, ok := m[key]
	return ok
}

func materializeFillValue(v any) (any, error) {
	switch v.(type) {
	case orderedObject, []any, string, bool, nil, json.Number, float64:
		return v, nil
	case map[string]any:
		b, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		return parseOrderedJSON(b)
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		return parseOrderedJSON(b)
	}
}

func repairArray(ctx context.Context, arr []any, sch map[string]any, depth int, st *repairState) (any, error) {
	items, ok := sch["items"]
	if !ok {
		return arr, nil
	}
	out := make([]any, len(arr))
	for i, el := range arr {
		if err := checkRepairWithContext(ctx); err != nil {
			return nil, err
		}
		repaired, err := repairValue(ctx, el, items, depth+1, st)
		if err != nil {
			return nil, err
		}
		out[i] = repaired
	}
	return out, nil
}

func effectiveSchemaObject(schema any, root any, depth int) (map[string]any, error) {
	if schema == nil {
		return nil, nil
	}
	sch, ok := asSchemaMap(schema)
	if !ok {
		return nil, nil
	}
	if depth > maxRepairDepth {
		return nil, &repairError{reason: toolcall.ReasonUnrepairable}
	}
	if ref, ok := sch["$ref"].(string); ok {
		if hasRefSiblings(sch) {
			return nil, &repairError{reason: toolcall.ReasonUnrepairable}
		}
		resolved, err := resolveLocalRef(root, ref)
		if err != nil {
			return nil, &repairError{reason: toolcall.ReasonUnrepairable}
		}
		return effectiveSchemaObject(resolved, root, depth+1)
	}
	for _, key := range []string{"oneOf", "anyOf"} {
		if branch, ok := sch[key].([]any); ok {
			if len(branch) != 1 {
				return nil, &repairError{reason: toolcall.ReasonUnrepairable}
			}
			return effectiveSchemaObject(branch[0], root, depth+1)
		}
	}
	if allOf, ok := sch["allOf"].([]any); ok {
		if len(allOf) != 1 {
			return nil, &repairError{reason: toolcall.ReasonUnrepairable}
		}
		return effectiveSchemaObject(allOf[0], root, depth+1)
	}
	return sch, nil
}

func asSchemaMap(v any) (map[string]any, bool) {
	_, values, ok := objectFields(v)
	return values, ok
}

func hasRefSiblings(sch map[string]any) bool {
	for k := range sch {
		if k != "$ref" {
			return true
		}
	}
	return false
}

func resolveLocalRef(root any, ref string) (any, error) {
	if ref == "#" {
		return root, nil
	}
	if len(ref) < 2 || ref[0] != '#' || ref[1] != '/' {
		return nil, fmt.Errorf("non-local ref")
	}
	cur := root
	parts := splitJSONPointer(ref[1:])
	for _, p := range parts {
		_, values, ok := objectFields(cur)
		if !ok {
			return nil, fmt.Errorf("ref path")
		}
		next, ok := values[p]
		if !ok {
			return nil, fmt.Errorf("ref missing")
		}
		cur = next
	}
	return cur, nil
}

func splitJSONPointer(ptr string) []string {
	if ptr == "" || ptr == "/" {
		return nil
	}
	if ptr[0] == '/' {
		ptr = ptr[1:]
	}
	raw := make([]string, 0, 4)
	start := 0
	for i := 0; i <= len(ptr); i++ {
		if i == len(ptr) || ptr[i] == '/' {
			raw = append(raw, unescapePointer(ptr[start:i]))
			start = i + 1
		}
	}
	return raw
}

func unescapePointer(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '~' && i+1 < len(s) {
			switch s[i+1] {
			case '1':
				out = append(out, '/')
				i++
				continue
			case '0':
				out = append(out, '~')
				i++
				continue
			}
		}
		out = append(out, s[i])
	}
	return string(out)
}

func checkScalarCoercion(v any, sch map[string]any) error {
	types := schemaTypes(sch)
	if len(types) == 0 {
		return nil
	}
	if valueMatchesTypes(v, types) {
		return nil
	}
	if coercionWouldBeRequired(v, types) {
		return &repairError{reason: toolcall.ReasonScalarCoercionDisabled}
	}
	return nil
}

func schemaTypes(sch map[string]any) []string {
	typeVal, ok := sch["type"]
	if !ok {
		return nil
	}
	switch t := typeVal.(type) {
	case string:
		return []string{t}
	case []any:
		out := make([]string, 0, len(t))
		for _, el := range t {
			if s, ok := el.(string); ok {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

func valueMatchesTypes(v any, types []string) bool {
	for _, want := range types {
		if valueMatchesType(v, want) {
			return true
		}
	}
	return false
}

func valueMatchesType(v any, want string) bool {
	switch want {
	case "string":
		_, ok := v.(string)
		return ok
	case "integer", "number":
		switch v.(type) {
		case json.Number, float64:
			return true
		default:
			return false
		}
	case "boolean":
		_, ok := v.(bool)
		return ok
	case "null":
		return v == nil
	case "object":
		_, ok := v.(orderedObject)
		if ok {
			return true
		}
		_, ok = v.(map[string]any)
		return ok
	case "array":
		_, ok := v.([]any)
		return ok
	default:
		return false
	}
}

func coercionWouldBeRequired(v any, types []string) bool {
	if !isJSONScalar(v) {
		return false
	}
	wantScalar := false
	for _, want := range types {
		switch want {
		case "string", "integer", "number", "boolean", "null":
			wantScalar = true
		}
	}
	return wantScalar
}

func isJSONScalar(v any) bool {
	switch v.(type) {
	case string, bool, json.Number, float64, nil:
		return true
	default:
		return false
	}
}
