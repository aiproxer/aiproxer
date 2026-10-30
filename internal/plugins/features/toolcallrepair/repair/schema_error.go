package repair

const (
	SchemaKindInvalid          = "invalid"
	SchemaKindUnsupported      = "unsupported"
	SchemaKindUnsafe           = "unsafe"
	SchemaKindExternalRef      = "external_ref"
	SchemaKindLimitExceeded    = "limit_exceeded"
	SchemaKindValidationFailed = "validation_failed"
	SchemaKindMalformed        = "malformed"
)

const (
	ReasonMalformedJSON      = "malformed_json"
	ReasonMalformedUTF8      = "malformed_utf8"
	ReasonUnsupportedDialect = "unsupported_dialect"
	ReasonExternalRef        = "external_ref_forbidden"
	ReasonUnsafeKeyword      = "unsafe_keyword"
	ReasonSchemaTooLarge     = "schema_too_large"
	ReasonNestingTooDeep     = "nesting_too_deep"
	ReasonTooManyNodes       = "too_many_nodes"
	ReasonTooManyProperties  = "too_many_properties"
	ReasonLocalRefTooDeep    = "local_ref_too_deep"
	ReasonInvalidSchema      = "invalid_schema"
	ReasonValidationFailed   = "validation_failed"
	ReasonCompilePanic       = "compile_panic"
	ReasonValidatePanic      = "validate_panic"
	ReasonCanceled           = "canceled"
	ReasonEmptySchema        = "empty_schema"
	ReasonArgsTooLargeShape  = "args_too_large"
)

const maxErrorPathRunes = 256

type SchemaError struct {
	Kind         string
	ReasonCode   string
	InstancePath string
}

func (e *SchemaError) Error() string {
	if e == nil {
		return "toolcallrepair: schema error"
	}
	msg := "toolcallrepair: schema " + e.Kind
	if e.ReasonCode != "" {
		msg += " reason=" + e.ReasonCode
	}
	if path := boundPath(e.InstancePath); path != "" {
		msg += " path=" + path
	}
	return msg
}

func boundPath(path string) string {
	if path == "" {
		return ""
	}
	runes := []rune(path)
	if len(runes) <= maxErrorPathRunes {
		return path
	}
	return string(runes[:maxErrorPathRunes])
}

func schemaErr(kind, reason, path string) *SchemaError {
	return &SchemaError{Kind: kind, ReasonCode: reason, InstancePath: boundPath(path)}
}
