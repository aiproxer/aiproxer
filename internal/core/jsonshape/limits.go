package jsonshape

// Limits bounds JSON size and structural complexity before decode/use.
// Zero or negative numeric fields are filled by NormalizeLimits / NormalizeWithDefaults.
// RejectDuplicateNames is never normalized: false means accept duplicate object
// member names (encoding/json last-wins semantics); true means reject them.
// Full profiles set the flag explicitly; partial Limits keep the zero value (false).
type Limits struct {
	MaxBytes             int64
	MaxDepth             int
	MaxTokens            int
	MaxArrayElems        int
	MaxObjectKeys        int
	MaxStringBytes       int
	MaxKeyBytes          int
	MaxNumberBytes       int
	RejectDuplicateNames bool
}

// Result reports basic facts gathered during token-level scanning.
type Result struct {
	Bytes    int
	Tokens   int
	MaxDepth int
}

// NormalizeLimits fills zero or negative numeric fields from RequestEnvelopeLimits.
// Frontend request-envelope callers may keep using this. Schema and tool-argument
// call sites must instead normalize with ToolSchemaLimits or ToolArgumentsLimits
// via NormalizeWithDefaults or Limits.Normalized. RejectDuplicateNames is left as-is
// (RequestEnvelope defaults false when callers omit it).
func NormalizeLimits(limits Limits) Limits {
	return NormalizeWithDefaults(limits, RequestEnvelopeLimits())
}

// NormalizeWithDefaults fills zero or negative numeric fields from defaults.
// RejectDuplicateNames is not copied from defaults: callers that need rejection
// must set it on the input Limits (profiles do). Pass ToolSchemaLimits or
// ToolArgumentsLimits as defaults for those profiles; do not rely on
// NormalizeLimits (envelope) for schema/args paths.
func NormalizeWithDefaults(limits, defaults Limits) Limits {
	if limits.MaxBytes <= 0 {
		limits.MaxBytes = defaults.MaxBytes
	}
	if limits.MaxDepth <= 0 {
		limits.MaxDepth = defaults.MaxDepth
	}
	if limits.MaxTokens <= 0 {
		limits.MaxTokens = defaults.MaxTokens
	}
	if limits.MaxArrayElems <= 0 {
		limits.MaxArrayElems = defaults.MaxArrayElems
	}
	if limits.MaxObjectKeys <= 0 {
		limits.MaxObjectKeys = defaults.MaxObjectKeys
	}
	if limits.MaxStringBytes <= 0 {
		limits.MaxStringBytes = defaults.MaxStringBytes
	}
	if limits.MaxKeyBytes <= 0 {
		limits.MaxKeyBytes = defaults.MaxKeyBytes
	}
	if limits.MaxNumberBytes <= 0 {
		limits.MaxNumberBytes = defaults.MaxNumberBytes
	}
	return limits
}

// Normalized returns limits with zero or negative numeric fields filled from defaults.
func (limits Limits) Normalized(defaults Limits) Limits {
	return NormalizeWithDefaults(limits, defaults)
}
