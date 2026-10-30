package repair

import (
	"context"
	"encoding/json"
)

func ExportPreflightSchemaJSON(ctx context.Context, schema []byte, limits SchemaLimits) error {
	return preflightSchemaJSON(ctx, schema, limits)
}

func ExportPreflightArgsJSON(ctx context.Context, args []byte, maxArgsBytes int) error {
	return preflightArgsJSON(ctx, args, maxArgsBytes)
}

func ExportParseOrderedJSON(data []byte) (any, error) {
	return parseOrderedJSON(data)
}

func ExportRepairArgsJSON(ctx context.Context, args []byte, schema json.RawMessage, maxArgsBytes int, schemaLimits SchemaLimits) (out []byte, reason string, err error) {
	return repairArgsJSON(ctx, args, schema, maxArgsBytes, schemaLimits)
}

func ExportMapEngineSchemaReason(err error) string {
	return mapEngineSchemaReason(err)
}

func ExportSchemaShapeLimits(limits SchemaLimits) (maxArrayElems, maxNodes, maxProperties int) {
	l := schemaShapeLimits(limits)
	return l.MaxArrayElems, limits.normalized().MaxNodes, limits.normalized().MaxProperties
}

// ExportSetSchemaCacheCompile replaces one cache's compiler for a regression
// test that must hold a shared compile open across caller cancellation.
func ExportSetSchemaCacheCompile(c *SchemaCache, fn func(context.Context, json.RawMessage, SchemaLimits) (*CompiledSchema, error)) {
	if c != nil {
		c.compile = fn
	}
}

// ExportCompileSchema exposes the production compiler to the compile seam so a
// test can still produce a real compiled schema while it observes the context.
func ExportCompileSchema(ctx context.Context, schema json.RawMessage, limits SchemaLimits) (*CompiledSchema, error) {
	return compileSchema(ctx, schema, limits)
}
