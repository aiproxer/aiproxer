package featureplanegen

import (
	"bytes"
	"fmt"
	"strings"
)

// emitRequestExecutionView generates RequestExecutionView struct and accessor methods.
func emitRequestExecutionView(buf *bytes.Buffer, planes []planeInfo) {
	buf.WriteString("// RequestExecutionView is an immutable borrowed view over request-materialized planes.\n")
	buf.WriteString("// Its returned slices must not be mutated.\n")
	buf.WriteString("type RequestExecutionView struct {\n")
	buf.WriteString("\tfrozen FrozenPlaneSet\n")
	buf.WriteString("}\n\n")

	buf.WriteString("// RequestExecution constructs a RequestExecutionView over the request-materialized planes in in.\n")
	buf.WriteString("func RequestExecution(in FrozenPlaneSet) RequestExecutionView {\n")
	buf.WriteString("\treturn RequestExecutionView{frozen: in}\n")
	buf.WriteString("}\n\n")

	for _, p := range planes {
		if !p.requestBorrow {
			continue
		}
		pascalName := strings.TrimPrefix(p.varName, "Plane")
		fmt.Fprintf(buf, "// %s returns the request-materialized %s without cloning.\n", pascalName, pascalName)
		buf.WriteString("// The returned slice is immutable borrowed storage and MUST NOT be mutated.\n")
		fmt.Fprintf(buf, "func (v RequestExecutionView) %s() %s {\n", pascalName, p.typeExpr)
		buf.WriteString("\tif v.frozen.frozen == nil {\n\t\treturn nil\n\t}\n")
		fmt.Fprintf(buf, "\treturn v.frozen.frozen.%s\n", p.fieldName)
		buf.WriteString("}\n\n")
	}
}

// emitGenerationBinderMethods generates Bind<Plane> and Replace<Plane> convenience methods on ContributionSet.
func emitGenerationBinderMethods(buf *bytes.Buffer, planes []planeInfo) {
	for _, p := range planes {
		if p.genBinderRule == "CombReplaceByIdentity" {
			policyVar := canonicalPolicyVar(p)
			accessVar := canonicalAccessVar(p)
			pascalName := strings.TrimPrefix(p.varName, "Plane")
			fmt.Fprintf(buf, "// Bind%s replaces %s under SourceGenerationBinder semantics.\n", pascalName, pascalName)
			fmt.Fprintf(buf, "func (s *ContributionSet) Bind%s(contributorID string, v %s) error {\n", pascalName, p.typeExpr)
			fmt.Fprintf(buf, "\treturn contributePolicy(s, %s, %s.contribute, %s.identity, SourceGenerationBinder, contributorID, v)\n", policyVar, accessVar, accessVar)
			buf.WriteString("}\n\n")

			fmt.Fprintf(buf, "// Replace%s replaces %s under SourceGenerationBinder semantics.\n", pascalName, pascalName)
			fmt.Fprintf(buf, "func (s *ContributionSet) Replace%s(contributorID string, v %s) error {\n", pascalName, p.typeExpr)
			fmt.Fprintf(buf, "\treturn contributePolicy(s, %s, %s.contribute, %s.identity, SourceGenerationBinder, contributorID, v)\n", policyVar, accessVar, accessVar)
			buf.WriteString("}\n\n")
		}
	}
}

// emitCheckSourceAdmission generates checkSourceAdmission method on generatedFrozen.
func emitCheckSourceAdmission(buf *bytes.Buffer, planes []planeInfo) {
	buf.WriteString("// checkSourceAdmission checks whether all present planes in gf support source.\n")
	buf.WriteString("func (gf *generatedFrozen) checkSourceAdmission(source SourceKind, contributorID string) error {\n")
	buf.WriteString("\tif gf == nil {\n\t\treturn nil\n\t}\n")
	for _, p := range planes {
		policyVar := canonicalPolicyVar(p)
		if strings.HasPrefix(p.typeExpr, "[]") {
			fmt.Fprintf(buf, "\tif gf.%s != nil {\n", p.fieldName)
		} else if p.typeExpr == "int" {
			fmt.Fprintf(buf, "\tif gf.%s > 0 {\n", p.fieldName)
		} else {
			fmt.Fprintf(buf, "\tif gf.%s != nil {\n", p.fieldName)
		}
		fmt.Fprintf(buf, "\t\tif %s.rules.RuleFor(source) == CombUnsupported {\n", policyVar)
		fmt.Fprintf(buf, "\t\t\treturn &AttributedError{\n\t\t\t\tPluginID: contributorID,\n\t\t\t\tPlaneID:  %s.planeID,\n\t\t\t\tErr:      fmt.Errorf(\"%%w: source %%v is not supported on plane %%q\", ErrUnsupportedSource, source, %s.planeID),\n\t\t\t}\n", policyVar, policyVar)
		buf.WriteString("\t\t}\n\t}\n")
	}
	buf.WriteString("\treturn nil\n")
	buf.WriteString("}\n\n")
}

// emitCheckCandidateSourceAdmission generates checkCandidateSourceAdmission method on generatedFrozen.
func emitCheckCandidateSourceAdmission(buf *bytes.Buffer, planes []planeInfo) {
	buf.WriteString("// checkCandidateSourceAdmission checks whether all present candidate planes in gf support source.\n")
	buf.WriteString("func (gf *generatedFrozen) checkCandidateSourceAdmission(source SourceKind, contributorID string) error {\n")
	buf.WriteString("\tif gf == nil {\n\t\treturn nil\n\t}\n")
	for _, p := range planes {
		if !p.candidate {
			continue
		}
		policyVar := canonicalPolicyVar(p)
		if strings.HasPrefix(p.typeExpr, "[]") {
			fmt.Fprintf(buf, "\tif gf.%s != nil {\n", p.fieldName)
		} else if p.typeExpr == "int" {
			fmt.Fprintf(buf, "\tif gf.%s > 0 {\n", p.fieldName)
		} else {
			fmt.Fprintf(buf, "\tif gf.%s != nil {\n", p.fieldName)
		}
		fmt.Fprintf(buf, "\t\tif %s.rules.RuleFor(source) == CombUnsupported {\n", policyVar)
		fmt.Fprintf(buf, "\t\t\treturn &AttributedError{\n\t\t\t\tPluginID: contributorID,\n\t\t\t\tPlaneID:  %s.planeID,\n\t\t\t\tErr:      fmt.Errorf(\"%%w: source %%v is not supported on plane %%q\", ErrUnsupportedSource, source, %s.planeID),\n\t\t\t}\n", policyVar, policyVar)
		buf.WriteString("\t\t}\n\t}\n")
	}
	buf.WriteString("\treturn nil\n")
	buf.WriteString("}\n\n")
}

// emitContributeCandidateTo generates contributeCandidateTo method on generatedFrozen.
func emitContributeCandidateTo(buf *bytes.Buffer, planes []planeInfo) {
	buf.WriteString("func (gf *generatedFrozen) contributeCandidateTo(gc *generatedContributions, source SourceKind, contributorID string) error {\n")
	buf.WriteString("\tif gf == nil || gc == nil {\n\t\treturn nil\n\t}\n")
	buf.WriteString("\tif err := gf.checkCandidateSourceAdmission(source, contributorID); err != nil {\n\t\treturn err\n\t}\n")
	for _, p := range planes {
		if !p.candidate {
			continue
		}
		policyVar := canonicalPolicyVar(p)
		accessVar := canonicalAccessVar(p)
		if strings.HasPrefix(p.typeExpr, "[]") {
			if p.hasIdentity {
				fmt.Fprintf(buf, "\tif gf.%s == nil {\n", p.fieldName)
				fmt.Fprintf(buf, "\t\tif gf.%sHasID || gf.%sID != \"\" {\n", p.fieldName, p.fieldName)
				fmt.Fprintf(buf, "\t\t\treturn &AttributedError{\n\t\t\t\tPluginID: contributorID,\n\t\t\t\tPlaneID:  %s.planeID,\n\t\t\t\tErr:      fmt.Errorf(\"%%w: malformed metadata without value\", ErrInvalidContribution),\n\t\t\t}\n", policyVar)
				fmt.Fprintf(buf, "\t\t}\n")
				fmt.Fprintf(buf, "\t} else {\n")
				fmt.Fprintf(buf, "\t\tif %s.validate != nil {\n", policyVar)
				fmt.Fprintf(buf, "\t\t\tif err := %s.validate(gf.%s); err != nil {\n", policyVar, p.fieldName)
				fmt.Fprintf(buf, "\t\t\t\treturn &AttributedError{\n\t\t\t\t\tPluginID: contributorID,\n\t\t\t\t\tPlaneID:  %s.planeID,\n\t\t\t\t\tErr:      fmt.Errorf(\"%%w: %%w\", ErrInvalidContribution, err),\n\t\t\t\t}\n", policyVar)
				fmt.Fprintf(buf, "\t\t\t}\n")
				fmt.Fprintf(buf, "\t\t}\n")
				fmt.Fprintf(buf, "\t\tif !gf.%sHasID {\n", p.fieldName)
				fmt.Fprintf(buf, "\t\t\tif gf.%sID != \"\" || len(gf.%s) > 0 {\n", p.fieldName, p.fieldName)
				fmt.Fprintf(buf, "\t\t\t\treturn &AttributedError{\n\t\t\t\t\tPluginID: contributorID,\n\t\t\t\t\tPlaneID:  %s.planeID,\n\t\t\t\t\tErr:      fmt.Errorf(\"%%w: missing cached identity\", ErrInvalidContribution),\n\t\t\t\t}\n", policyVar)
				fmt.Fprintf(buf, "\t\t\t}\n")
				fmt.Fprintf(buf, "\t\t} else {\n")
				fmt.Fprintf(buf, "\t\t\tif gf.%sID == \"\" {\n", p.fieldName)
				fmt.Fprintf(buf, "\t\t\t\treturn &AttributedError{\n\t\t\t\t\tPluginID: contributorID,\n\t\t\t\t\tPlaneID:  %s.planeID,\n\t\t\t\t\tErr:      fmt.Errorf(\"%%w: missing cached identity\", ErrInvalidContribution),\n\t\t\t\t}\n", policyVar)
				fmt.Fprintf(buf, "\t\t\t}\n")
				if p.hasValidateIdentity {
					fmt.Fprintf(buf, "\t\t\tif err := %s.validateIdentity(gf.%sID); err != nil {\n", policyVar, p.fieldName)
					fmt.Fprintf(buf, "\t\t\t\treturn &AttributedError{\n\t\t\t\t\tPluginID: contributorID,\n\t\t\t\t\tPlaneID:  %s.planeID,\n\t\t\t\t\tErr:      fmt.Errorf(\"%%w: %%w\", ErrInvalidContribution, err),\n\t\t\t\t}\n", policyVar)
					fmt.Fprintf(buf, "\t\t\t}\n")
				}
				fmt.Fprintf(buf, "\t\t}\n")
				fmt.Fprintf(buf, "\t\thadDestinationValue := len(gc.%s) > 0\n", p.fieldName)
				fmt.Fprintf(buf, "\t\texistingID := gc.%sID\n", p.fieldName)
				fmt.Fprintf(buf, "\t\texistingHasID := gc.%sHasID\n\n", p.fieldName)
				fmt.Fprintf(buf, "\t\tincoming := cloneSlice(gf.%s)\n", p.fieldName)
				fmt.Fprintf(buf, "\t\tcurrent := cloneSlice(gc.%s)\n", p.fieldName)
				fmt.Fprintf(buf, "\t\tcombined, err := %s.combine(source, current, incoming)\n", policyVar)
				buf.WriteString("\t\tif err != nil {\n")
				fmt.Fprintf(buf, "\t\t\treturn &AttributedError{\n\t\t\t\tPluginID: contributorID,\n\t\t\t\tPlaneID:  %s.planeID,\n\t\t\t\tErr:      fmt.Errorf(\"%%w: %%w\", ErrInvalidContribution, err),\n\t\t\t}\n", policyVar)
				buf.WriteString("\t\t}\n")
				fmt.Fprintf(buf, "\t\tif (gf.%s != nil || gc.%s != nil) && combined == nil {\n", p.fieldName, p.fieldName)
				fmt.Fprintf(buf, "\t\t\tcombined = make(%s, 0)\n", p.typeExpr)
				buf.WriteString("\t\t}\n")
				fmt.Fprintf(buf, "\t\tgc.%s = cloneSlice(combined)\n", p.fieldName)
				fmt.Fprintf(buf, "\t\tif len(gc.%s) == 0 {\n", p.fieldName)
				fmt.Fprintf(buf, "\t\t\tgc.%sID = \"\"\n", p.fieldName)
				fmt.Fprintf(buf, "\t\t\tgc.%sHasID = false\n", p.fieldName)
				buf.WriteString("\t\t} else if hadDestinationValue {\n")
				fmt.Fprintf(buf, "\t\t\tgc.%sID = existingID\n", p.fieldName)
				fmt.Fprintf(buf, "\t\t\tgc.%sHasID = existingHasID\n", p.fieldName)
				buf.WriteString("\t\t} else {\n")
				fmt.Fprintf(buf, "\t\t\tgc.%sID = gf.%sID\n", p.fieldName, p.fieldName)
				fmt.Fprintf(buf, "\t\t\tgc.%sHasID = gf.%sHasID\n", p.fieldName, p.fieldName)
				buf.WriteString("\t\t}\n")
				fmt.Fprintf(buf, "\t}\n")
			} else {
				fmt.Fprintf(buf, "\tif gf.%s != nil {\n", p.fieldName)
				fmt.Fprintf(buf, "\t\tif %s.validate != nil {\n", policyVar)
				fmt.Fprintf(buf, "\t\t\tif err := %s.validate(gf.%s); err != nil {\n", policyVar, p.fieldName)
				fmt.Fprintf(buf, "\t\t\t\treturn &AttributedError{\n\t\t\t\t\tPluginID: contributorID,\n\t\t\t\t\tPlaneID:  %s.planeID,\n\t\t\t\t\tErr:      fmt.Errorf(\"%%w: %%w\", ErrInvalidContribution, err),\n\t\t\t\t}\n", policyVar)
				fmt.Fprintf(buf, "\t\t\t}\n")
				fmt.Fprintf(buf, "\t\t}\n")
				fmt.Fprintf(buf, "\t\tif err := %s.contribute(gc, source, contributorID, gf.%s); err != nil {\n\t\t\treturn err\n\t\t}\n\t}\n", accessVar, p.fieldName)
			}
		} else if p.typeExpr == "int" {
			fmt.Fprintf(buf, "\tif gf.%s < 0 {\n", p.fieldName)
			fmt.Fprintf(buf, "\t\treturn &AttributedError{\n\t\t\tPluginID: contributorID,\n\t\t\tPlaneID:  %s.planeID,\n\t\t\tErr:      fmt.Errorf(\"%%w: must be >= 0, got %%d\", ErrInvalidContribution, gf.%s),\n\t\t}\n", policyVar, p.fieldName)
			buf.WriteString("\t}\n")
			fmt.Fprintf(buf, "\tif gf.%s > 0 {\n", p.fieldName)
			fmt.Fprintf(buf, "\t\tif %s.validate != nil {\n", policyVar)
			fmt.Fprintf(buf, "\t\t\tif err := %s.validate(gf.%s); err != nil {\n", policyVar, p.fieldName)
			fmt.Fprintf(buf, "\t\t\t\treturn &AttributedError{\n\t\t\t\t\tPluginID: contributorID,\n\t\t\t\t\tPlaneID:  %s.planeID,\n\t\t\t\t\tErr:      fmt.Errorf(\"%%w: %%w\", ErrInvalidContribution, err),\n\t\t\t\t}\n", policyVar)
			fmt.Fprintf(buf, "\t\t\t}\n")
			fmt.Fprintf(buf, "\t\t}\n")
			fmt.Fprintf(buf, "\t\tif err := %s.contribute(gc, source, contributorID, gf.%s); err != nil {\n\t\t\treturn err\n\t\t}\n\t}\n", accessVar, p.fieldName)
		} else if p.isExclusive {
			fmt.Fprintf(buf, "\tif gf.%s != nil {\n", p.fieldName)
			fmt.Fprintf(buf, "\t\tif !gf.%sHasID || gf.%sID == \"\" {\n", p.fieldName, p.fieldName)
			fmt.Fprintf(buf, "\t\t\treturn &AttributedError{\n\t\t\t\tPluginID: contributorID,\n\t\t\t\tPlaneID:  %s.planeID,\n\t\t\t\tErr:      fmt.Errorf(\"%%w: frozen exclusive identity is missing\", ErrInvalidContribution),\n\t\t\t}\n", policyVar)
			fmt.Fprintf(buf, "\t\t}\n")
			fmt.Fprintf(buf, "\tif gc.%sHasID {\n", p.fieldName)
			fmt.Fprintf(buf, "\t\t\treturn makeExclusiveConflictError(contributorID, %s.planeID, %s.exclusiveConflictError, gc.%sID, gf.%sID)\n", policyVar, policyVar, p.fieldName, p.fieldName)
			fmt.Fprintf(buf, "\t\t}\n")
			fmt.Fprintf(buf, "\t\tgc.%s = gf.%s\n", p.fieldName, p.fieldName)
			fmt.Fprintf(buf, "\t\tgc.%sID = gf.%sID\n", p.fieldName, p.fieldName)
			fmt.Fprintf(buf, "\t\tgc.%sHasID = true\n", p.fieldName)
			fmt.Fprintf(buf, "\t}\n")
		} else {
			fmt.Fprintf(buf, "\tif gf.%s != nil {\n", p.fieldName)
			fmt.Fprintf(buf, "\t\tif %s.validate != nil {\n", policyVar)
			fmt.Fprintf(buf, "\t\t\tif err := %s.validate(gf.%s); err != nil {\n", policyVar, p.fieldName)
			fmt.Fprintf(buf, "\t\t\t\treturn &AttributedError{\n\t\t\t\t\tPluginID: contributorID,\n\t\t\t\t\tPlaneID:  %s.planeID,\n\t\t\t\t\tErr:      fmt.Errorf(\"%%w: %%w\", ErrInvalidContribution, err),\n\t\t\t\t}\n", policyVar)
			fmt.Fprintf(buf, "\t\t\t}\n")
			fmt.Fprintf(buf, "\t\t}\n")
			fmt.Fprintf(buf, "\t\tif err := %s.contribute(gc, source, contributorID, gf.%s); err != nil {\n\t\t\treturn err\n\t\t}\n\t}\n", accessVar, p.fieldName)
		}
	}
	buf.WriteString("\treturn nil\n")
	buf.WriteString("}\n\n")
}

// emitReplayAllPlanesTo generates replayAllPlanesTo method on generatedFrozen.
func emitReplayAllPlanesTo(buf *bytes.Buffer, planes []planeInfo) {
	buf.WriteString("func (gf *generatedFrozen) replayAllPlanesTo(gc *generatedContributions, source SourceKind, contributorID string) error {\n")
	buf.WriteString("\tif gf == nil || gc == nil {\n\t\treturn nil\n\t}\n")
	buf.WriteString("\tif err := gf.checkSourceAdmission(source, contributorID); err != nil {\n\t\treturn err\n\t}\n")
	for _, p := range planes {
		policyVar := canonicalPolicyVar(p)
		accessVar := canonicalAccessVar(p)
		if strings.HasPrefix(p.typeExpr, "[]") {
			if p.hasIdentity {
				fmt.Fprintf(buf, "\tif gf.%s != nil {\n", p.fieldName)
				fmt.Fprintf(buf, "\t\thadDestinationValue := len(gc.%s) > 0\n", p.fieldName)
				fmt.Fprintf(buf, "\t\texistingID := gc.%sID\n", p.fieldName)
				fmt.Fprintf(buf, "\t\texistingHasID := gc.%sHasID\n\n", p.fieldName)
				fmt.Fprintf(buf, "\t\tincoming := cloneSlice(gf.%s)\n", p.fieldName)
				fmt.Fprintf(buf, "\t\tcurrent := cloneSlice(gc.%s)\n", p.fieldName)
				fmt.Fprintf(buf, "\t\tcombined, err := %s.combine(source, current, incoming)\n", policyVar)
				buf.WriteString("\t\tif err != nil {\n")
				fmt.Fprintf(buf, "\t\t\treturn &AttributedError{\n\t\t\t\tPluginID: contributorID,\n\t\t\t\tPlaneID:  %s.planeID,\n\t\t\t\tErr:      fmt.Errorf(\"%%w: %%w\", ErrInvalidContribution, err),\n\t\t\t}\n", policyVar)
				buf.WriteString("\t\t}\n")
				fmt.Fprintf(buf, "\t\tif (gf.%s != nil || gc.%s != nil) && combined == nil {\n", p.fieldName, p.fieldName)
				fmt.Fprintf(buf, "\t\t\tcombined = make(%s, 0)\n", p.typeExpr)
				buf.WriteString("\t\t}\n")
				fmt.Fprintf(buf, "\t\tgc.%s = cloneSlice(combined)\n", p.fieldName)
				fmt.Fprintf(buf, "\t\tif len(gc.%s) == 0 {\n", p.fieldName)
				fmt.Fprintf(buf, "\t\t\tgc.%sID = \"\"\n", p.fieldName)
				fmt.Fprintf(buf, "\t\t\tgc.%sHasID = false\n", p.fieldName)
				buf.WriteString("\t\t} else if hadDestinationValue {\n")
				fmt.Fprintf(buf, "\t\t\tgc.%sID = existingID\n", p.fieldName)
				fmt.Fprintf(buf, "\t\t\tgc.%sHasID = existingHasID\n", p.fieldName)
				buf.WriteString("\t\t} else {\n")
				fmt.Fprintf(buf, "\t\t\tgc.%sID = gf.%sID\n", p.fieldName, p.fieldName)
				fmt.Fprintf(buf, "\t\t\tgc.%sHasID = gf.%sHasID\n", p.fieldName, p.fieldName)
				buf.WriteString("\t\t}\n")
				fmt.Fprintf(buf, "\t}\n")
			} else {
				fmt.Fprintf(buf, "\tif gf.%s != nil {\n", p.fieldName)
				fmt.Fprintf(buf, "\t\tif %s.validate != nil {\n", policyVar)
				fmt.Fprintf(buf, "\t\t\tif err := %s.validate(gf.%s); err != nil {\n", policyVar, p.fieldName)
				fmt.Fprintf(buf, "\t\t\t\treturn &AttributedError{\n\t\t\t\t\tPluginID: contributorID,\n\t\t\t\t\tPlaneID:  %s.planeID,\n\t\t\t\t\tErr:      fmt.Errorf(\"%%w: %%w\", ErrInvalidContribution, err),\n\t\t\t\t}\n", policyVar)
				fmt.Fprintf(buf, "\t\t\t}\n")
				fmt.Fprintf(buf, "\t\t}\n")
				fmt.Fprintf(buf, "\t\tif err := %s.contribute(gc, source, contributorID, gf.%s); err != nil {\n\t\t\treturn err\n\t\t}\n\t}\n", accessVar, p.fieldName)
			}
		} else if p.typeExpr == "int" {
			fmt.Fprintf(buf, "\tif gf.%s > 0 {\n", p.fieldName)
			fmt.Fprintf(buf, "\t\tif %s.validate != nil {\n", policyVar)
			fmt.Fprintf(buf, "\t\t\tif err := %s.validate(gf.%s); err != nil {\n", policyVar, p.fieldName)
			fmt.Fprintf(buf, "\t\t\t\treturn &AttributedError{\n\t\t\t\t\tPluginID: contributorID,\n\t\t\t\t\tPlaneID:  %s.planeID,\n\t\t\t\t\tErr:      fmt.Errorf(\"%%w: %%w\", ErrInvalidContribution, err),\n\t\t\t\t}\n", policyVar)
			fmt.Fprintf(buf, "\t\t\t}\n")
			fmt.Fprintf(buf, "\t\t}\n")
			fmt.Fprintf(buf, "\t\tif err := %s.contribute(gc, source, contributorID, gf.%s); err != nil {\n\t\t\treturn err\n\t\t}\n\t}\n", accessVar, p.fieldName)
		} else if p.isExclusive {
			fmt.Fprintf(buf, "\tif gf.%s != nil {\n", p.fieldName)
			fmt.Fprintf(buf, "\t\tif !gf.%sHasID || gf.%sID == \"\" {\n", p.fieldName, p.fieldName)
			fmt.Fprintf(buf, "\t\t\treturn &AttributedError{\n\t\t\t\tPluginID: contributorID,\n\t\t\t\tPlaneID:  %s.planeID,\n\t\t\t\tErr:      fmt.Errorf(\"%%w: frozen exclusive identity is missing\", ErrInvalidContribution),\n\t\t\t}\n", policyVar)
			fmt.Fprintf(buf, "\t\t}\n")
			fmt.Fprintf(buf, "\tif gc.%sHasID {\n", p.fieldName)
			fmt.Fprintf(buf, "\t\t\treturn makeExclusiveConflictError(contributorID, %s.planeID, %s.exclusiveConflictError, gc.%sID, gf.%sID)\n", policyVar, policyVar, p.fieldName, p.fieldName)
			fmt.Fprintf(buf, "\t\t}\n")
			fmt.Fprintf(buf, "\t\tgc.%s = gf.%s\n", p.fieldName, p.fieldName)
			fmt.Fprintf(buf, "\t\tgc.%sID = gf.%sID\n", p.fieldName, p.fieldName)
			fmt.Fprintf(buf, "\t\tgc.%sHasID = true\n", p.fieldName)
			fmt.Fprintf(buf, "\t}\n")
		} else {
			fmt.Fprintf(buf, "\tif gf.%s != nil {\n", p.fieldName)
			fmt.Fprintf(buf, "\t\tif %s.validate != nil {\n", policyVar)
			fmt.Fprintf(buf, "\t\t\tif err := %s.validate(gf.%s); err != nil {\n", policyVar, p.fieldName)
			fmt.Fprintf(buf, "\t\t\t\treturn &AttributedError{\n\t\t\t\t\tPluginID: contributorID,\n\t\t\t\t\tPlaneID:  %s.planeID,\n\t\t\t\t\tErr:      fmt.Errorf(\"%%w: %%w\", ErrInvalidContribution, err),\n\t\t\t\t}\n", policyVar)
			fmt.Fprintf(buf, "\t\t\t}\n")
			fmt.Fprintf(buf, "\t\t}\n")
			fmt.Fprintf(buf, "\t\tif err := %s.contribute(gc, source, contributorID, gf.%s); err != nil {\n\t\t\treturn err\n\t\t}\n\t}\n", accessVar, p.fieldName)
		}
	}
	buf.WriteString("\treturn nil\n")
	buf.WriteString("}\n\n")
}
