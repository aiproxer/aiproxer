package repair

import (
	"context"
	"errors"

	"github.com/aiproxer/aiproxer/internal/plugins/features/toolcallrepair/repair/jsonshape"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcall"
)

func schemaShapeLimits(limits SchemaLimits) jsonshape.Limits {
	limits = limits.normalized()
	base := jsonshape.ToolSchemaLimits()
	out := jsonshape.NormalizeWithDefaults(jsonshape.Limits{
		MaxBytes:       int64(limits.MaxSchemaBytes),
		MaxDepth:       limits.MaxNestingDepth,
		MaxTokens:      min(8*limits.MaxNodes, limits.MaxSchemaBytes),
		MaxArrayElems:  min(limits.MaxNodes, base.MaxArrayElems),
		MaxObjectKeys:  limits.MaxProperties,
		MaxStringBytes: limits.MaxSchemaBytes,
		MaxKeyBytes:    min(base.MaxKeyBytes, limits.MaxSchemaBytes),
		MaxNumberBytes: base.MaxNumberBytes,
	}, base)
	out.RejectDuplicateNames = base.RejectDuplicateNames
	return out
}

func argsShapeLimits(maxArgsBytes int) jsonshape.Limits {
	if maxArgsBytes <= 0 {
		maxArgsBytes = DefaultMaxArgsBytes
	}
	base := jsonshape.ToolArgumentsLimits()
	out := jsonshape.NormalizeWithDefaults(jsonshape.Limits{
		MaxBytes:       int64(maxArgsBytes),
		MaxDepth:       base.MaxDepth,
		MaxTokens:      min(base.MaxTokens, maxArgsBytes),
		MaxArrayElems:  base.MaxArrayElems,
		MaxObjectKeys:  base.MaxObjectKeys,
		MaxStringBytes: maxArgsBytes,
		MaxKeyBytes:    min(base.MaxKeyBytes, maxArgsBytes),
		MaxNumberBytes: base.MaxNumberBytes,
	}, base)
	out.RejectDuplicateNames = base.RejectDuplicateNames
	return out
}

func preflightSchemaJSON(ctx context.Context, schema []byte, limits SchemaLimits) error {
	limits = limits.normalized()
	_, err := jsonshape.PreflightWithContext(ctx, schema, schemaShapeLimits(limits))
	return mapSchemaJSONShapeErr(err, limits)
}

func preflightArgsJSON(ctx context.Context, args []byte, maxArgsBytes int) error {
	_, err := jsonshape.PreflightWithContext(ctx, args, argsShapeLimits(maxArgsBytes))
	return mapArgsJSONShapeErr(err)
}

func mapSchemaJSONShapeErr(err error, limits SchemaLimits) error {
	if err == nil {
		return nil
	}
	var je *jsonshape.Error
	if !errors.As(err, &je) {
		return schemaErr(SchemaKindMalformed, ReasonMalformedJSON, "")
	}
	switch je.Kind {
	case jsonshape.KindCanceled:
		return schemaErr(SchemaKindInvalid, ReasonCanceled, "")
	case jsonshape.KindTooLarge:
		return schemaErr(SchemaKindLimitExceeded, ReasonSchemaTooLarge, "")
	case jsonshape.KindTooDeep:
		return schemaErr(SchemaKindLimitExceeded, ReasonNestingTooDeep, "")
	case jsonshape.KindTooManyTokens:
		return schemaErr(SchemaKindLimitExceeded, ReasonTooManyNodes, "")
	case jsonshape.KindTooManyItems:
		if je.Limit == limits.MaxNodes && limits.MaxNodes != limits.MaxProperties {
			return schemaErr(SchemaKindLimitExceeded, ReasonTooManyNodes, "")
		}
		return schemaErr(SchemaKindLimitExceeded, ReasonTooManyProperties, "")
	case jsonshape.KindStringTooLong, jsonshape.KindKeyTooLong, jsonshape.KindNumberTooLong:
		return schemaErr(SchemaKindLimitExceeded, ReasonSchemaTooLarge, "")
	case jsonshape.KindDuplicateName:
		return schemaErr(SchemaKindMalformed, ReasonMalformedJSON, "")
	case jsonshape.KindInvalidUTF8:
		return schemaErr(SchemaKindMalformed, ReasonMalformedUTF8, "")
	default:
		return schemaErr(SchemaKindMalformed, ReasonMalformedJSON, "")
	}
}

func mapArgsJSONShapeErr(err error) error {
	if err == nil {
		return nil
	}
	var je *jsonshape.Error
	if !errors.As(err, &je) {
		return schemaErr(SchemaKindMalformed, ReasonMalformedJSON, "")
	}
	switch je.Kind {
	case jsonshape.KindCanceled:
		return schemaErr(SchemaKindInvalid, ReasonCanceled, "")
	case jsonshape.KindTooLarge:
		return schemaErr(SchemaKindLimitExceeded, ReasonArgsTooLargeShape, "")
	case jsonshape.KindTooDeep:
		return schemaErr(SchemaKindLimitExceeded, ReasonNestingTooDeep, "")
	case jsonshape.KindTooManyTokens:
		return schemaErr(SchemaKindLimitExceeded, ReasonTooManyNodes, "")
	case jsonshape.KindTooManyItems:
		return schemaErr(SchemaKindLimitExceeded, ReasonTooManyProperties, "")
	case jsonshape.KindStringTooLong, jsonshape.KindKeyTooLong, jsonshape.KindNumberTooLong:
		return schemaErr(SchemaKindLimitExceeded, ReasonArgsTooLargeShape, "")
	case jsonshape.KindDuplicateName:
		return schemaErr(SchemaKindMalformed, ReasonMalformedJSON, "")
	case jsonshape.KindInvalidUTF8:
		return schemaErr(SchemaKindMalformed, ReasonMalformedUTF8, "")
	default:
		return schemaErr(SchemaKindMalformed, ReasonMalformedJSON, "")
	}
}

func mapEngineArgsShapeReason(err error) string {
	if err == nil {
		return toolcall.ReasonUnrepairable
	}
	var se *SchemaError
	if errors.As(err, &se) && se != nil {
		switch se.ReasonCode {
		case ReasonCanceled:
			return toolcall.ReasonCanceled
		case ReasonArgsTooLargeShape:
			return toolcall.ReasonArgsTooLarge
		default:
			return toolcall.ReasonUnrepairable
		}
	}
	if je, ok := errors.AsType[*jsonshape.Error](err); ok {
		switch je.Kind {
		case jsonshape.KindCanceled:
			return toolcall.ReasonCanceled
		case jsonshape.KindTooLarge:
			return toolcall.ReasonArgsTooLarge
		}
	}
	return toolcall.ReasonUnrepairable
}
