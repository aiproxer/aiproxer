package jsonshape

// Limits bounds JSON size and structural complexity before decode/use.
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

const (
	defaultToolSchemaMaxBytes       int64 = 256 << 10
	defaultToolSchemaMaxDepth             = 32
	defaultToolSchemaMaxNodes             = 4096
	defaultToolSchemaMaxTokens            = min(8*defaultToolSchemaMaxNodes, int(defaultToolSchemaMaxBytes))
	defaultToolSchemaMaxArrayElems        = 4096
	defaultToolSchemaMaxObjectKeys        = 1024
	defaultToolSchemaMaxStringBytes       = 256 << 10
	defaultToolSchemaMaxKeyBytes          = 16 << 10
	defaultToolSchemaMaxNumberBytes       = 64

	defaultToolArgsMaxBytes       int64 = 64 << 10
	defaultToolArgsMaxDepth             = 64
	defaultToolArgsMaxTokens            = 16_384
	defaultToolArgsMaxArrayElems        = 4096
	defaultToolArgsMaxObjectKeys        = 1024
	defaultToolArgsMaxStringBytes       = 64 << 10
	defaultToolArgsMaxKeyBytes          = 16 << 10
	defaultToolArgsMaxNumberBytes       = 64
)

// ToolSchemaLimits matches tool-call-repair schema defaults (256 KiB / depth 32 /
// 4096 semantic nodes / 1024 members) and rejects duplicate member names.
func ToolSchemaLimits() Limits {
	return Limits{
		MaxBytes:             defaultToolSchemaMaxBytes,
		MaxDepth:             defaultToolSchemaMaxDepth,
		MaxTokens:            defaultToolSchemaMaxTokens,
		MaxArrayElems:        defaultToolSchemaMaxArrayElems,
		MaxObjectKeys:        defaultToolSchemaMaxObjectKeys,
		MaxStringBytes:       defaultToolSchemaMaxStringBytes,
		MaxKeyBytes:          defaultToolSchemaMaxKeyBytes,
		MaxNumberBytes:       defaultToolSchemaMaxNumberBytes,
		RejectDuplicateNames: true,
	}
}

// ToolArgumentsLimits matches the 64 KiB tool-arguments byte default with depth 64,
// bounded fan-out/number length, and RejectDuplicateNames=true.
func ToolArgumentsLimits() Limits {
	return Limits{
		MaxBytes:             defaultToolArgsMaxBytes,
		MaxDepth:             defaultToolArgsMaxDepth,
		MaxTokens:            defaultToolArgsMaxTokens,
		MaxArrayElems:        defaultToolArgsMaxArrayElems,
		MaxObjectKeys:        defaultToolArgsMaxObjectKeys,
		MaxStringBytes:       defaultToolArgsMaxStringBytes,
		MaxKeyBytes:          defaultToolArgsMaxKeyBytes,
		MaxNumberBytes:       defaultToolArgsMaxNumberBytes,
		RejectDuplicateNames: true,
	}
}

// NormalizeWithDefaults fills zero or negative numeric fields from defaults.
// RejectDuplicateNames is preserved from limits.
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
