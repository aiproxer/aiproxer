package repair

import (
	"context"
	"encoding/json"
)

func ValidateArgsAgainstSchema(argsJSON []byte, schema json.RawMessage) error {
	return ValidateArgsAgainstSchemaWithContext(context.Background(), argsJSON, schema)
}

func ValidateArgsAgainstSchemaWithContext(ctx context.Context, argsJSON []byte, schema json.RawMessage) error {
	compiled, err := packageSchemaCache().GetOrCompileWithContext(ctx, schema)
	if err != nil {
		return err
	}
	return compiled.ValidateWithContext(ctx, argsJSON)
}
