package repair

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

const schemaResourceURL = "mem://toolcallrepair/schema.json"

type CompiledSchema struct {
	schema          *jsonschema.Schema
	orderedDocument any
	digest          string
	bytes           int
}

type rejectingURLLoader struct{}

func (rejectingURLLoader) Load(url string) (any, error) {
	return nil, schemaErr(SchemaKindExternalRef, ReasonExternalRef, "")
}

func compileSchema(ctx context.Context, schema json.RawMessage, limits SchemaLimits) (*CompiledSchema, error) {
	if err := ctx.Err(); err != nil {
		return nil, schemaErr(SchemaKindInvalid, ReasonCanceled, "")
	}
	scanned, err := preScanSchema(ctx, schema, limits)
	if err != nil {
		return nil, err
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	compiler.UseLoader(rejectingURLLoader{})
	if err := compiler.AddResource(schemaResourceURL, scanned.document); err != nil {
		return nil, mapCompileError(err)
	}
	sch, err := compiler.Compile(schemaResourceURL)
	if err != nil {
		return nil, mapCompileError(err)
	}
	return &CompiledSchema{
		schema:          sch,
		orderedDocument: scanned.orderedDocument,
		digest:          schemaDigest(schema),
		bytes:           len(schema),
	}, nil
}

func (s *CompiledSchema) Validate(argsJSON []byte) error {
	return s.ValidateWithContext(context.Background(), argsJSON)
}

func (s *CompiledSchema) ValidateWithContext(ctx context.Context, argsJSON []byte) error {
	return s.validateWithMaxArgs(ctx, argsJSON, DefaultMaxArgsBytes)
}

func (s *CompiledSchema) validateWithMaxArgs(ctx context.Context, argsJSON []byte, maxArgsBytes int) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = schemaErr(SchemaKindInvalid, ReasonValidatePanic, "")
		}
	}()
	if s == nil || s.schema == nil {
		return schemaErr(SchemaKindInvalid, ReasonInvalidSchema, "")
	}
	if err := ctx.Err(); err != nil {
		return schemaErr(SchemaKindInvalid, ReasonCanceled, "")
	}
	if maxArgsBytes <= 0 {
		maxArgsBytes = DefaultMaxArgsBytes
	}
	if err := preflightArgsJSON(ctx, argsJSON, maxArgsBytes); err != nil {
		return err
	}
	if !json.Valid(argsJSON) {
		return schemaErr(SchemaKindMalformed, ReasonMalformedJSON, "")
	}
	inst, err := unmarshalSchemaJSON(argsJSON)
	if err != nil {
		return schemaErr(SchemaKindMalformed, ReasonMalformedJSON, "")
	}
	if err := s.schema.Validate(inst); err != nil {
		return mapValidationError(err)
	}
	return nil
}

func unmarshalSchemaJSON(raw []byte) (any, error) {
	return jsonschema.UnmarshalJSON(bytes.NewReader(raw))
}

func mapCompileError(err error) error {
	if err == nil {
		return nil
	}
	if se, ok := errors.AsType[*SchemaError](err); ok {
		return se
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "unsupported draft"), strings.Contains(msg, "metaschema"):
		return schemaErr(SchemaKindUnsupported, ReasonUnsupportedDialect, "")
	case strings.Contains(msg, "external_ref"), strings.Contains(msg, "loadurl"), strings.Contains(msg, "failing loading"):
		return schemaErr(SchemaKindExternalRef, ReasonExternalRef, "")
	default:
		return schemaErr(SchemaKindInvalid, ReasonInvalidSchema, "")
	}
}

func mapValidationError(err error) error {
	if err == nil {
		return nil
	}
	if ve, ok := errors.AsType[*jsonschema.ValidationError](err); ok {
		path := validationInstancePath(ve)
		return schemaErr(SchemaKindValidationFailed, ReasonValidationFailed, path)
	}
	return schemaErr(SchemaKindValidationFailed, ReasonValidationFailed, "")
}

func validationInstancePath(ve *jsonschema.ValidationError) string {
	if ve == nil {
		return ""
	}
	leaf := ve
	for len(leaf.Causes) > 0 {
		leaf = leaf.Causes[0]
	}
	if len(leaf.InstanceLocation) == 0 {
		return ""
	}
	var b strings.Builder
	for _, tok := range leaf.InstanceLocation {
		b.WriteByte('/')
		b.WriteString(escapeJSONPointerToken(tok))
	}
	return b.String()
}

func escapeJSONPointerToken(tok string) string {
	tok = strings.ReplaceAll(tok, "~", "~0")
	tok = strings.ReplaceAll(tok, "/", "~1")
	return tok
}

func schemaDigest(schema []byte) string {
	sum := sha256Sum(schema)
	return hex.EncodeToString(sum[:])
}
