package codex

// isStrictCompatibleSchema reports whether a JSON schema satisfies the Codex
// Responses API "strict" tool-schema requirements: every object must declare
// additionalProperties:false and list all of its properties in required. Schemas
// that do not comply must be sent with strict:false, otherwise the upstream
// rejects the request (e.g. "additionalProperties is required to be supplied
// and to be false"). Parameterless object schemas are normalized by
// addStrictAdditionalProperties to include additionalProperties:false (and an
// empty required list) so they remain strict-compatible instead of leaking a
// strict:true tool that the upstream rejects. The check is conservative: when
// in doubt it returns false, which only relaxes strict mode (safe) and never
// causes an upstream rejection.
func isStrictCompatibleSchema(schema map[string]any) bool {
	if hasRef(schema) || !strictCompatibleCompositions(schema) {
		return false
	}
	if !isObjectSchema(schema) {
		// Non-object root (array/primitive): only its array items must comply.
		return strictCompatibleArrayItems(schema)
	}
	ap, ok := schema["additionalProperties"]
	if !ok {
		return false
	}
	if asBool, ok := ap.(bool); !ok || asBool {
		return false
	}
	props, _ := schema["properties"].(map[string]any)
	reqRaw, _ := schema["required"].([]any)
	required := make(map[string]bool, len(reqRaw))
	for _, r := range reqRaw {
		if s, ok := r.(string); ok {
			required[s] = true
		}
	}
	for name, prop := range props {
		if !required[name] {
			return false
		}
		child, ok := prop.(map[string]any)
		if !ok {
			return false
		}
		if !isStrictCompatibleChild(child) {
			return false
		}
	}
	return strictCompatibleArrayItems(schema)
}

func normalizeToolSchemaForCodex(schema map[string]any) (map[string]any, bool) {
	addStrictAdditionalProperties(schema)
	return schema, isStrictCompatibleSchema(schema)
}

// isObjectSchema reports whether a node is a JSON object schema: either it
// declares type "object" or it carries a non-empty properties map. Empty
// properties maps without an explicit object type are not treated as objects so
// that a truly empty schema ({}) is left untouched.
func isObjectSchema(node map[string]any) bool {
	if t, _ := node["type"].(string); t == "object" {
		return true
	}
	props, ok := node["properties"].(map[string]any)
	return ok && len(props) > 0
}

func addStrictAdditionalProperties(v any) {
	switch x := v.(type) {
	case map[string]any:
		// Only descend through subschema-bearing keywords so non-schema payloads
		// (enum, default, examples, etc.) are not mutated with strict keywords.
		if props, ok := x["properties"].(map[string]any); ok {
			for k, child := range props {
				addStrictAdditionalProperties(child)
				props[k] = child
			}
		}
		if items, ok := x["items"]; ok {
			addStrictAdditionalProperties(items)
			x["items"] = items
		}
		for _, key := range []string{"oneOf", "anyOf", "allOf"} {
			if children, ok := x[key].([]any); ok {
				for i, child := range children {
					addStrictAdditionalProperties(child)
					children[i] = child
				}
			}
		}
		if isObjectSchema(x) {
			if _, ok := x["additionalProperties"]; !ok {
				x["additionalProperties"] = false
			}
			// Parameterless objects must also carry an explicit required:[] for the
			// Responses API strict mode; inject it only when no properties and no
			// required are already declared.
			if props, _ := x["properties"].(map[string]any); len(props) == 0 {
				if _, ok := x["required"]; !ok {
					x["required"] = []any{}
				}
			}
		}
	case []any:
		for i, child := range x {
			addStrictAdditionalProperties(child)
			x[i] = child
		}
	}
}

func isStrictCompatibleChild(node map[string]any) bool {
	if hasRef(node) || !strictCompatibleCompositions(node) {
		return false
	}
	if props, ok := node["properties"].(map[string]any); ok && len(props) > 0 {
		return isStrictCompatibleSchema(node)
	}
	switch t, _ := node["type"].(string); t {
	case "object":
		return isStrictCompatibleSchema(node)
	case "array":
		return strictCompatibleArrayItems(node)
	}
	return true
}

func hasRef(node map[string]any) bool {
	_, ok := node["$ref"]
	return ok
}

func strictCompatibleCompositions(node map[string]any) bool {
	for _, key := range []string{"oneOf", "anyOf", "allOf"} {
		raw, ok := node[key]
		if !ok {
			continue
		}
		items, ok := raw.([]any)
		if !ok || len(items) == 0 {
			return false
		}
		for _, item := range items {
			child, ok := item.(map[string]any)
			if !ok || !isStrictCompatibleChild(child) {
				return false
			}
		}
	}
	return true
}

func strictCompatibleArrayItems(node map[string]any) bool {
	raw, ok := node["items"]
	if !ok {
		return true
	}
	if child, ok := raw.(map[string]any); ok {
		return isStrictCompatibleChild(child)
	}
	items, ok := raw.([]any)
	if !ok {
		return false
	}
	for _, item := range items {
		child, ok := item.(map[string]any)
		if !ok || !isStrictCompatibleChild(child) {
			return false
		}
	}
	return true
}
