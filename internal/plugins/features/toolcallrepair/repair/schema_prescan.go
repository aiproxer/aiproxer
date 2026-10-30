package repair

import (
	"context"
	"strings"
	"unicode/utf8"
)

type preScannedSchema struct {
	document        any
	orderedDocument any
}

func preScanSchema(ctx context.Context, schema []byte, limits SchemaLimits) (*preScannedSchema, error) {
	if err := ctx.Err(); err != nil {
		return nil, schemaErr(SchemaKindInvalid, ReasonCanceled, "")
	}
	if len(schema) == 0 {
		return nil, schemaErr(SchemaKindMalformed, ReasonEmptySchema, "")
	}
	if len(schema) > limits.MaxSchemaBytes {
		return nil, schemaErr(SchemaKindLimitExceeded, ReasonSchemaTooLarge, "")
	}
	if !utf8.Valid(schema) {
		return nil, schemaErr(SchemaKindMalformed, ReasonMalformedUTF8, "")
	}
	if err := preflightSchemaJSON(ctx, schema, limits); err != nil {
		return nil, err
	}
	// The schema is decoded twice on purpose: unmarshalSchemaJSON produces the
	// plain document the jsonschema compiler consumes, while parseOrderedJSON
	// preserves key order and duplicate rejection for the repair document that
	// CompiledSchema retains. Both are bounded by preflightSchemaJSON above and
	// cached per schema digest; the retained ordered document adds at most one
	// schema-sized allocation to the LRU cache.
	doc, err := unmarshalSchemaJSON(schema)
	if err != nil {
		return nil, schemaErr(SchemaKindMalformed, ReasonMalformedJSON, "")
	}
	orderedDoc, err := parseOrderedJSON(schema)
	if err != nil {
		return nil, schemaErr(SchemaKindMalformed, ReasonMalformedJSON, "")
	}
	state := &prescanState{limits: limits, root: doc}
	if err := state.walk(ctx, doc, 0); err != nil {
		return nil, err
	}
	if err := state.checkDialect(); err != nil {
		return nil, err
	}
	if err := state.checkLocalRefs(ctx); err != nil {
		return nil, err
	}
	return &preScannedSchema{document: doc, orderedDocument: orderedDoc}, nil
}

type prescanState struct {
	limits SchemaLimits
	root   any
	nodes  int
	schema string
}

func (s *prescanState) walk(ctx context.Context, v any, depth int) error {
	if err := ctx.Err(); err != nil {
		return schemaErr(SchemaKindInvalid, ReasonCanceled, "")
	}
	if depth > s.limits.MaxNestingDepth {
		return schemaErr(SchemaKindLimitExceeded, ReasonNestingTooDeep, "")
	}
	s.nodes++
	if s.nodes > s.limits.MaxNodes {
		return schemaErr(SchemaKindLimitExceeded, ReasonTooManyNodes, "")
	}
	switch n := v.(type) {
	case map[string]any:
		if len(n) > s.limits.MaxProperties {
			return schemaErr(SchemaKindLimitExceeded, ReasonTooManyProperties, "")
		}
		if sch, ok := n["$schema"].(string); ok && s.schema == "" {
			s.schema = sch
		}
		if err := s.checkKeywords(n); err != nil {
			return err
		}
		for _, child := range n {
			if err := s.walk(ctx, child, depth+1); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range n {
			if err := s.walk(ctx, child, depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *prescanState) checkKeywords(obj map[string]any) error {
	if ref, ok := obj["$ref"].(string); ok && !isLocalRef(ref) {
		return schemaErr(SchemaKindExternalRef, ReasonExternalRef, "")
	}
	if ref, ok := obj["$dynamicRef"].(string); ok {
		if !isLocalRef(ref) {
			return schemaErr(SchemaKindExternalRef, ReasonExternalRef, "")
		}
		return schemaErr(SchemaKindUnsafe, ReasonUnsafeKeyword, "")
	}
	if ref, ok := obj["$recursiveRef"].(string); ok {
		if !isLocalRef(ref) {
			return schemaErr(SchemaKindExternalRef, ReasonExternalRef, "")
		}
		return schemaErr(SchemaKindUnsafe, ReasonUnsafeKeyword, "")
	}
	if _, ok := obj["$dynamicAnchor"]; ok {
		return schemaErr(SchemaKindUnsafe, ReasonUnsafeKeyword, "")
	}
	if _, ok := obj["$recursiveAnchor"]; ok {
		return schemaErr(SchemaKindUnsafe, ReasonUnsafeKeyword, "")
	}
	return nil
}

func (s *prescanState) checkDialect() error {
	if s.schema == "" {
		return nil
	}
	if !isSupportedDialect(s.schema) {
		return schemaErr(SchemaKindUnsupported, ReasonUnsupportedDialect, "")
	}
	return nil
}

func (s *prescanState) checkLocalRefs(ctx context.Context) error {
	return walkLocalRefDepth(ctx, s.root, s.root, 0, s.limits.MaxLocalRefDepth, map[string]struct{}{})
}

func walkLocalRefDepth(ctx context.Context, root, v any, depth, maxDepth int, stack map[string]struct{}) error {
	if err := ctx.Err(); err != nil {
		return schemaErr(SchemaKindInvalid, ReasonCanceled, "")
	}
	switch n := v.(type) {
	case map[string]any:
		if ref, ok := n["$ref"].(string); ok && isLocalRef(ref) {
			if depth+1 > maxDepth {
				return schemaErr(SchemaKindLimitExceeded, ReasonLocalRefTooDeep, "")
			}
			if _, cycling := stack[ref]; cycling {
				return nil
			}
			target, ok := resolveLocalPointer(root, ref)
			if !ok {
				return schemaErr(SchemaKindInvalid, ReasonInvalidSchema, "")
			}
			stack[ref] = struct{}{}
			if err := walkLocalRefDepth(ctx, root, target, depth+1, maxDepth, stack); err != nil {
				delete(stack, ref)
				return err
			}
			delete(stack, ref)
		}
		for k, child := range n {
			if k == "$ref" {
				continue
			}
			if err := walkLocalRefDepth(ctx, root, child, depth, maxDepth, stack); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range n {
			if err := walkLocalRefDepth(ctx, root, child, depth, maxDepth, stack); err != nil {
				return err
			}
		}
	}
	return nil
}

func isLocalRef(ref string) bool {
	return strings.HasPrefix(ref, "#")
}

func isSupportedDialect(schemaURL string) bool {
	u := strings.TrimSpace(schemaURL)
	u = strings.TrimSuffix(u, "#")
	u = strings.TrimSuffix(u, "/")
	switch u {
	case "http://json-schema.org/draft-04/schema",
		"https://json-schema.org/draft-04/schema",
		"http://json-schema.org/draft-06/schema",
		"https://json-schema.org/draft-06/schema",
		"http://json-schema.org/draft-07/schema",
		"https://json-schema.org/draft-07/schema",
		"https://json-schema.org/draft/2019-09/schema",
		"http://json-schema.org/draft/2019-09/schema",
		"https://json-schema.org/draft/2020-12/schema",
		"http://json-schema.org/draft/2020-12/schema":
		return true
	default:
		return false
	}
}

func resolveLocalPointer(root any, ref string) (any, bool) {
	if ref == "#" || ref == "#/" {
		return root, true
	}
	if !strings.HasPrefix(ref, "#/") {
		if ref == "#" {
			return root, true
		}
		// Plain "#fragment" name anchors are not walked as JSON pointers here.
		return nil, false
	}
	cur := root
	for part := range strings.SplitSeq(ref[2:], "/") {
		part = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
		switch n := cur.(type) {
		case map[string]any:
			next, ok := n[part]
			if !ok {
				return nil, false
			}
			cur = next
		case []any:
			// Array index pointers are uncommon in tool schemas; reject for safety.
			return nil, false
		default:
			return nil, false
		}
	}
	return cur, true
}
