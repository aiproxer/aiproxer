package featureplanegen

import (
	"bytes"
	"fmt"
)

func emitProjectDiagnostics(buf *bytes.Buffer, planes []planeInfo) {
	buf.WriteString("// ProjectDiagnostics projects diagnostic occupants and privileges from a frozen plane set.\n")
	buf.WriteString("func ProjectDiagnostics(in FrozenPlaneSet) []DiagnosticPlaneProjection {\n")
	buf.WriteString("\tif in.IsZero() {\n\t\treturn nil\n\t}\n")
	buf.WriteString("\tgf := in.frozen\n")
	buf.WriteString("\tif gf == nil {\n\t\treturn nil\n\t}\n")
	buf.WriteString("\tvar projections []DiagnosticPlaneProjection\n\n")

	for _, p := range planes {
		if !p.hasDiagStageID {
			continue
		}
		policyVar := canonicalPolicyVar(p)
		fmt.Fprintf(buf, "\t// Project %s\n", p.varName)
		buf.WriteString("\t{\n")
		fmt.Fprintf(buf, "\t\tval := gf.%s\n", p.fieldName)
		fmt.Fprintf(buf, "\t\tocc := %s.materializeOccupants(val)\n", policyVar)
		fmt.Fprintf(buf, "\t\tpriv := %s.projectPrivileges(val)\n", policyVar)
		buf.WriteString("\t\tif len(occ) > 0 || len(priv.Flags) > 0 {\n")
		buf.WriteString("\t\t\tvar occCopy []DiagnosticOccupant\n")
		buf.WriteString("\t\t\tif len(occ) > 0 {\n")
		buf.WriteString("\t\t\t\toccCopy = make([]DiagnosticOccupant, len(occ))\n")
		buf.WriteString("\t\t\t\tfor i := 0; i < len(occ); i++ {\n")
		buf.WriteString("\t\t\t\t\to := occ[i]\n")
		buf.WriteString("\t\t\t\t\tvar pCopy []string\n")
		buf.WriteString("\t\t\t\t\tif len(o.Privileges) > 0 {\n")
		buf.WriteString("\t\t\t\t\t\tpCopy = append([]string(nil), o.Privileges...)\n")
		buf.WriteString("\t\t\t\t\t}\n")
		buf.WriteString("\t\t\t\t\toccCopy[i] = DiagnosticOccupant{\n")
		buf.WriteString("\t\t\t\t\t\tLabel:      o.Label,\n")
		buf.WriteString("\t\t\t\t\t\tPluginID:   o.PluginID,\n")
		buf.WriteString("\t\t\t\t\t\tPrivileges: pCopy,\n")
		buf.WriteString("\t\t\t\t\t}\n")
		buf.WriteString("\t\t\t\t}\n")
		buf.WriteString("\t\t\t}\n")
		buf.WriteString("\t\t\tvar privCopy []string\n")
		buf.WriteString("\t\t\tif len(priv.Flags) > 0 {\n")
		buf.WriteString("\t\t\t\tprivCopy = append([]string(nil), priv.Flags...)\n")
		buf.WriteString("\t\t\t}\n")
		buf.WriteString("\t\t\tprojections = append(projections, DiagnosticPlaneProjection{\n")
		fmt.Fprintf(buf, "\t\t\t\tPlaneID:       %s.planeID,\n", policyVar)
		fmt.Fprintf(buf, "\t\t\t\tStageID:       %s.diagStageID,\n", policyVar)
		fmt.Fprintf(buf, "\t\t\t\tCoalesceGroup: %s.diagCoalesceGroup,\n", policyVar)
		fmt.Fprintf(buf, "\t\t\t\tOrder:         %s.diagOrder,\n", policyVar)
		buf.WriteString("\t\t\t\tOccupants:     occCopy,\n")
		buf.WriteString("\t\t\t\tPrivileges:    PrivilegeProjection{Flags: privCopy},\n")
		buf.WriteString("\t\t\t})\n")
		buf.WriteString("\t\t}\n")
		buf.WriteString("\t}\n\n")
	}

	buf.WriteString("\treturn projections\n")
	buf.WriteString("}\n\n")
}
