package featureplanegen

import (
	"bytes"
	"fmt"
)

// emitHookConfig generates HookConfig struct and ProjectHookConfig function
// when at least one plane declares HookTarget metadata.
func emitHookConfig(buf *bytes.Buffer, planes []planeInfo) {
	var hookPlanes []planeInfo
	var hookPkg string
	for _, p := range planes {
		if p.hookTarget != "" {
			hookPlanes = append(hookPlanes, p)
			if hookPkg == "" {
				hookPkg = p.hookPkg
			}
		}
	}
	if len(hookPlanes) == 0 {
		return
	}
	if hookPkg == "" {
		panic("internal error: hook planes present but hookPkg is empty")
	}

	buf.WriteString("// HookConfig contains the projected hook slices and error policy for core execution.\n")
	buf.WriteString("type HookConfig struct {\n")
	for _, p := range hookPlanes {
		fmt.Fprintf(buf, "\t%s %s\n", p.hookTarget, p.typeExpr)
	}
	fmt.Fprintf(buf, "\tToolReactorErrorPolicy %s.ToolReactorErrorPolicy\n", hookPkg)
	buf.WriteString("}\n\n")

	buf.WriteString("// ProjectHookConfig projects a FrozenPlaneSet into typed HookConfig.\n")
	fmt.Fprintf(buf, "func ProjectHookConfig(frozen FrozenPlaneSet, policy %s.ToolReactorErrorPolicy) HookConfig {\n", hookPkg)
	buf.WriteString("\treturn HookConfig{\n")
	for _, p := range hookPlanes {
		fmt.Fprintf(buf, "\t\t%s: %s.get(frozen.frozen),\n", p.hookTarget, canonicalAccessVar(p))
	}
	buf.WriteString("\t\tToolReactorErrorPolicy: policy,\n")
	buf.WriteString("\t}\n")
	buf.WriteString("}\n\n")
}
