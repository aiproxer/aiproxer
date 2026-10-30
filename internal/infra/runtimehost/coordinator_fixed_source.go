package runtimehost

// FixedSourcePath is the HTTP-only fixed startup source capability.
// Paths stay off the canonical Status contract; management adapters map this
// into transport DTOs only.
func (c *Coordinator) FixedSourcePath() string {
	if c == nil || c.source == nil {
		return ""
	}
	return c.source.AbsolutePath()
}
