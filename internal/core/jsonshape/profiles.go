package jsonshape

import "github.com/aiproxer/aiproxer/pkg/aipapi"

const (
	defaultRequestMaxBytes       int64 = 8 << 20
	defaultRequestMaxDepth             = 128
	defaultRequestMaxTokens            = 1_000_000
	defaultRequestMaxArrayElems        = 100_000
	defaultRequestMaxObjectKeys        = 100_000
	defaultRequestMaxKeyBytes          = 16 << 10
	defaultRequestMaxNumberBytes       = 1 << 20

	defaultToolSchemaMaxBytes int64 = 256 << 10
	defaultToolSchemaMaxDepth       = 32
	defaultToolSchemaMaxNodes       = 4096
	// Token stream cost exceeds node count (keys, delimiters, scalars). Budget
	// 8x MaxNodes, still bounded by MaxSchemaBytes for tiny-token flood cases.
	defaultToolSchemaMaxTokens      = min(8*defaultToolSchemaMaxNodes, int(defaultToolSchemaMaxBytes))
	defaultToolSchemaMaxArrayElems  = 4096
	defaultToolSchemaMaxObjectKeys  = 1024
	defaultToolSchemaMaxStringBytes = 256 << 10
	defaultToolSchemaMaxKeyBytes    = 16 << 10
	defaultToolSchemaMaxNumberBytes = 64

	defaultToolArgsMaxBytes       int64 = 64 << 10
	defaultToolArgsMaxDepth             = 64
	defaultToolArgsMaxTokens            = 16_384
	defaultToolArgsMaxArrayElems        = 4096
	defaultToolArgsMaxObjectKeys        = 1024
	defaultToolArgsMaxStringBytes       = 64 << 10
	defaultToolArgsMaxKeyBytes          = 16 << 10
	defaultToolArgsMaxNumberBytes       = 64
)

// RequestEnvelopeLimits matches historical frontend jsonguard defaults: 8 MiB,
// depth 128, high fan-out, and RejectDuplicateNames=false so accepted multimodal
// envelopes keep encoding/json last-wins duplicate-member semantics.
func RequestEnvelopeLimits() Limits {
	return Limits{
		MaxBytes:             defaultRequestMaxBytes,
		MaxDepth:             defaultRequestMaxDepth,
		MaxTokens:            defaultRequestMaxTokens,
		MaxArrayElems:        defaultRequestMaxArrayElems,
		MaxObjectKeys:        defaultRequestMaxObjectKeys,
		MaxStringBytes:       min(aipapi.MaxPartTextBytes, int(defaultRequestMaxBytes)),
		MaxKeyBytes:          defaultRequestMaxKeyBytes,
		MaxNumberBytes:       defaultRequestMaxNumberBytes,
		RejectDuplicateNames: false,
	}
}

// ToolSchemaLimits matches tool-call-repair schema defaults (256 KiB / depth 32 /
// 4096 semantic nodes / 1024 members) and rejects duplicate member names.
// MaxTokens is an 8x-nodes token budget; structural node caps remain in
// toolcallrepair pre-scan. Local $ref depth stays separate.
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
// toolcallrepair adapts MaxBytes from MaxArgsBytes and keeps MaxDepth from this profile.
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
