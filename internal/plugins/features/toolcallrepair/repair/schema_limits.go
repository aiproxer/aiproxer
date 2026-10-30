package repair

type SchemaLimits struct {
	MaxSchemaBytes   int
	MaxNestingDepth  int
	MaxNodes         int
	MaxProperties    int
	MaxLocalRefDepth int
	MaxCacheEntries  int
	MaxCacheBytes    int
}

// DefaultSchemaLimits returns engine/cache defaults. Values are mirrored in the
// feature YAML package and locked by TestDefaultSchemaLimitsMatchCore.
func DefaultSchemaLimits() SchemaLimits {
	return SchemaLimits{
		MaxSchemaBytes:   256 * 1024,
		MaxNestingDepth:  32,
		MaxNodes:         4096,
		MaxProperties:    1024,
		MaxLocalRefDepth: 32,
		MaxCacheEntries:  64,
		MaxCacheBytes:    4 * 1024 * 1024,
	}
}

func (l SchemaLimits) normalized() SchemaLimits {
	d := DefaultSchemaLimits()
	if l.MaxSchemaBytes <= 0 {
		l.MaxSchemaBytes = d.MaxSchemaBytes
	}
	if l.MaxNestingDepth <= 0 {
		l.MaxNestingDepth = d.MaxNestingDepth
	}
	if l.MaxNodes <= 0 {
		l.MaxNodes = d.MaxNodes
	}
	if l.MaxProperties <= 0 {
		l.MaxProperties = d.MaxProperties
	}
	if l.MaxLocalRefDepth <= 0 {
		l.MaxLocalRefDepth = d.MaxLocalRefDepth
	}
	if l.MaxCacheEntries <= 0 {
		l.MaxCacheEntries = d.MaxCacheEntries
	}
	if l.MaxCacheBytes <= 0 {
		l.MaxCacheBytes = d.MaxCacheBytes
	}
	return l
}
