package aipapi

import "slices"

// ReconcileToolChoiceAfterToolListChange adjusts ToolChoice after tools were removed or reordered
// so [Call.Validate] can succeed (R9). It mutates only c.ToolChoice.
//
// Rules (deterministic):
//   - ToolChoiceNone with remaining tools → ToolChoiceAuto (tools survived filtering).
//   - ToolChoiceRequired with no tools, or required name missing from Tools → ToolChoiceAuto, Name cleared.
//   - Empty Mode → treated as ToolChoiceAuto after normalization.
func ReconcileToolChoiceAfterToolListChange(c *Call) {
	if c == nil {
		return
	}
	tools := c.Tools
	n := len(tools)
	if len(c.ToolChoice.AllowedTools) > 0 {
		// The allowed_tools subset must stay consistent with the surviving tool
		// list: drop removed names, and when none survive the subset is vacuous
		// so only the mode is retained.
		kept := make([]string, 0, len(c.ToolChoice.AllowedTools))
		for _, name := range c.ToolChoice.AllowedTools {
			if slices.ContainsFunc(tools, func(t ToolDef) bool { return t.Name == name }) {
				kept = append(kept, name)
			}
		}
		if len(kept) == 0 {
			c.ToolChoice = ToolChoice{Mode: c.ToolChoice.Mode}
			return
		}
		c.ToolChoice.AllowedTools = kept
		return
	}
	mode := c.ToolChoice.Mode
	if mode == "" {
		mode = ToolChoiceAuto
	}
	switch mode {
	case ToolChoiceNone:
		if n > 0 {
			c.ToolChoice = ToolChoice{Mode: ToolChoiceAuto}
		}
	case ToolChoiceRequired:
		if n == 0 {
			c.ToolChoice = ToolChoice{Mode: ToolChoiceAuto}
			return
		}
		name := c.ToolChoice.Name
		if name == "" {
			c.ToolChoice = ToolChoice{Mode: ToolChoiceAuto}
			return
		}
		if !slices.ContainsFunc(tools, func(t ToolDef) bool { return t.Name == name }) {
			c.ToolChoice = ToolChoice{Mode: ToolChoiceAuto}
		}
	default:
		// auto, any, unknown handled by Validate for unknown; keep as-is for auto/any
	}
}
