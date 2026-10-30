package repair

type DiagnosticEvent struct {
	TraceID       string
	BLegID        string
	ToolNameHash  string
	CatalogName   string
	SchemaDigest  string
	ArgsByteCount int
	Action        string
	ReasonCode    string
}

// Attrs returns a bounded, payload-free attribute map suitable for logs/metrics.
// Raw argument bytes and schema bodies are never included.
func (d DiagnosticEvent) Attrs() map[string]any {
	return map[string]any{
		"trace_id":        d.TraceID,
		"b_leg_id":        d.BLegID,
		"tool_name_hash":  d.ToolNameHash,
		"catalog_name":    d.CatalogName,
		"schema_digest":   d.SchemaDigest,
		"args_byte_count": d.ArgsByteCount,
		"action":          d.Action,
		"reason_code":     d.ReasonCode,
	}
}
