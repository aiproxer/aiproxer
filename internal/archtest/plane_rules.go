package archtest

import (
	"fmt"
	"go/ast"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// MigrationWave represents the progressive wave boundary for plane consolidation.
type MigrationWave int

const (
	Wave0_Baseline           MigrationWave = 0
	Wave1_HookBus            MigrationWave = 1
	Wave2_Observers          MigrationWave = 2
	Wave3_RequestShaping     MigrationWave = 3
	Wave4_Tools              MigrationWave = 4
	Wave5a_GuardsCompaction  MigrationWave = 5
	Wave5b_LocalTurnTerminal MigrationWave = 6
	Wave5c_Residual          MigrationWave = 7
)

func (w MigrationWave) String() string {
	switch w {
	case Wave0_Baseline:
		return "W0_Baseline"
	case Wave1_HookBus:
		return "W1_HookBus"
	case Wave2_Observers:
		return "W2_Observers"
	case Wave3_RequestShaping:
		return "W3_RequestShaping"
	case Wave4_Tools:
		return "W4_Tools"
	case Wave5a_GuardsCompaction:
		return "W5a_GuardsCompaction"
	case Wave5b_LocalTurnTerminal:
		return "W5b_LocalTurnTerminal"
	case Wave5c_Residual:
		return "W5c_Residual"
	default:
		return fmt.Sprintf("Wave(%d)", int(w))
	}
}

// ActiveMigrationWave defines the currently active migration wave ratchet.
// As migration waves complete, advance this constant to lock in forbidden mirror rules.
const ActiveMigrationWave = Wave5c_Residual

// MirrorShapeKind classifies the forbidden hand-authored mirror pattern.
type MirrorShapeKind string

const (
	MirrorFeatureBundleField          MirrorShapeKind = "FeatureBundleField"
	MirrorNamedTransportField         MirrorShapeKind = "NamedTransportField"
	MirrorAppendBranch                MirrorShapeKind = "AppendBranch"
	MirrorProjectionBranch            MirrorShapeKind = "ProjectionBranch"
	MirrorGenerationOpField           MirrorShapeKind = "GenerationOperationField"
	MirrorRequestRuntimeSnapshotField MirrorShapeKind = "RequestRuntimeSnapshotField"
	MirrorDiagArm                     MirrorShapeKind = "DiagnosticsArm"
	MirrorStageConsumer               MirrorShapeKind = "StageConsumer"
	MirrorClosedPlaneStorageMap       MirrorShapeKind = "ClosedPlaneStorageMap"
	MirrorClosedPlaneMapLogic         MirrorShapeKind = "ClosedPlaneMapLogic"
	MirrorClosedPlaneReflection       MirrorShapeKind = "ClosedPlaneReflection"
)

// MirrorFinding is one violation where a forbidden mirror exists past its wave.
type MirrorFinding struct {
	File       string
	Line       int
	ShapeKind  MirrorShapeKind
	PlaneID    string
	Identifier string
	Detail     string
	Wave       MigrationWave
}

func (f MirrorFinding) String() string {
	return fmt.Sprintf("%s:%d: [%s] %s (%s, wave %s): %s",
		f.File, f.Line, f.ShapeKind, f.Identifier, f.PlaneID, f.Wave, f.Detail)
}

// PlaneFieldMetadata holds canonical plane metadata for mirror detection.
type PlaneFieldMetadata struct {
	PlaneID string
	Wave    MigrationWave
}

// QualifiedSymbol returns the fully-qualified symbol name for a FuncDecl given its relative file path.
// For methods: "pkg/path.(*ReceiverType).MethodName" or "pkg/path.(ReceiverType).MethodName"
// For functions: "pkg/path.FunctionName"
func QualifiedSymbol(relPath string, fd *ast.FuncDecl) string {
	if fd == nil {
		return ""
	}
	dir := filepath.ToSlash(filepath.Dir(relPath))
	if dir == "." {
		dir = ""
	}
	var recvStr string
	if fd.Recv != nil && len(fd.Recv.List) > 0 {
		t := fd.Recv.List[0].Type
		if star, ok := t.(*ast.StarExpr); ok {
			if id, ok := star.X.(*ast.Ident); ok {
				recvStr = "(*" + id.Name + ")."
			}
		} else if id, ok := t.(*ast.Ident); ok {
			recvStr = "(" + id.Name + ")."
		}
	}
	if dir != "" {
		return dir + "." + recvStr + fd.Name.Name
	}
	if recvStr != "" {
		return recvStr + fd.Name.Name
	}
	return fd.Name.Name
}

// ForbiddenMirrorPredicate inspects an AST node in a file and returns a finding if forbidden.
type ForbiddenMirrorPredicate func(relPath string, node ast.Node, fset *token.FileSet, maxCompletedWave MigrationWave) (MirrorFinding, bool)

var generatedHeaderRegex = regexp.MustCompile(`^// Code generated .* DO NOT EDIT\.$`)

// IsGeneratedFile reports whether f (or src/path) contains a canonical generated code header
// comment before the package clause. In accordance with the Go generated-code convention,
// the marker must be a single-line comment matching '^// Code generated .* DO NOT EDIT\.$'
// occurring before the package declaration. Filename suffixes alone are not exempt.
func IsGeneratedFile(path string, src []byte, f *ast.File) bool {
	if f == nil {
		if len(src) == 0 {
			if path == "" {
				return false
			}
			var err error
			src, err = os.ReadFile(path)
			if err != nil {
				return false
			}
		}
		_, parsed, err := ParseGoSource(path, src)
		if err != nil || parsed == nil {
			return false
		}
		f = parsed
	}

	for _, cg := range f.Comments {
		if cg.Pos() >= f.Package {
			continue
		}
		for _, c := range cg.List {
			if generatedHeaderRegex.MatchString(c.Text) {
				return true
			}
		}
	}
	return false
}

func isDiagnosticsTargetFile(relPath string) bool {
	return filepath.ToSlash(relPath) == "internal/core/diag/inventory_extensions.go"
}

// ScanForbiddenMirrors walks all production files and finds forbidden mirrors past maxCompletedWave.
func ScanForbiddenMirrors(root string, maxCompletedWave MigrationWave) ([]MirrorFinding, error) {
	var findings []MirrorFinding
	err := WalkProductionGoFiles(root, func(rel, abs string, src []byte) error {
		fset, f, err := ParseGoSource(abs, src)
		if err != nil {
			return err
		}
		fileFindings := ScanFileForForbiddenMirrors(rel, src, fset, f, maxCompletedWave)
		findings = append(findings, fileFindings...)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(findings, func(i, j int) bool {
		if findings[i].File != findings[j].File {
			return findings[i].File < findings[j].File
		}
		if findings[i].Line != findings[j].Line {
			return findings[i].Line < findings[j].Line
		}
		return findings[i].PlaneID < findings[j].PlaneID
	})
	return findings, nil
}

// ScanFileForForbiddenMirrors inspects a single parsed Go file.
func ScanFileForForbiddenMirrors(relPath string, src []byte, fset *token.FileSet, f *ast.File, maxCompletedWave MigrationWave) []MirrorFinding {
	var findings []MirrorFinding
	seen := make(map[string]bool)
	addFinding := func(kind MirrorShapeKind, ident, planeID, detail string, line int, wave MigrationWave) {
		key := fmt.Sprintf("%s:%d:%s:%s", relPath, line, kind, planeID)
		if seen[key] {
			return
		}
		seen[key] = true
		findings = append(findings, MirrorFinding{
			File:       relPath,
			Line:       line,
			ShapeKind:  kind,
			PlaneID:    planeID,
			Identifier: ident,
			Detail:     detail,
			Wave:       wave,
		})
	}

	if maxCompletedWave >= Wave5c_Residual {
		closedFindings := ScanFileClosedPlaneViolations(relPath, fset, f)
		for _, cf := range closedFindings {
			var shapeKind MirrorShapeKind
			switch cf.Rule {
			case RuleClosedPlaneNoArbitraryValueMaps:
				shapeKind = MirrorClosedPlaneStorageMap
			case RuleClosedPlaneNoReflectionFallback:
				shapeKind = MirrorClosedPlaneReflection
			default:
				shapeKind = MirrorClosedPlaneMapLogic
			}
			line := 1
			if pos := strings.LastIndex(cf.Path, ":"); pos != -1 {
				_, _ = fmt.Sscanf(cf.Path[pos+1:], "%d", &line)
			}
			addFinding(shapeKind, "closed_plane", "closed_plane", cf.Detail, line, Wave5c_Residual)
		}
	}

	if IsGeneratedFile(relPath, src, f) {
		return findings
	}

	ast.Inspect(f, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.TypeSpec:
			if strings.Contains(relPath, "internal/testkit/") || strings.Contains(relPath, "internal/refbackend/") || strings.Contains(relPath, "internal/refclient/") {
				return true
			}
			st, ok := node.Type.(*ast.StructType)
			if !ok || st.Fields == nil {
				return true
			}
			structName := node.Name.Name
			pkgDir := PackageDirFromRel(relPath)
			qualStruct := structName
			if pkgDir != "" {
				qualStruct = pkgDir + "." + structName
			}

			// Wave ratchet exception for FeatureBundle SDK contract before Wave5c
			if (qualStruct == "pkg/aipsdk/feature.FeatureBundle" || structName == "FeatureBundle") && maxCompletedWave < Wave5c_Residual {
				return true
			}

			allowedFields, isAllowlisted := AllowedStructFields[qualStruct]
			if !isAllowlisted {
				allowedFields = AllowedStructFields[structName]
			}

			var shapeKind MirrorShapeKind
			switch structName {
			case "FeatureBundle":
				shapeKind = MirrorFeatureBundleField
			case "generationOperations":
				shapeKind = MirrorGenerationOpField
			case "RequestRuntimeSnapshot":
				shapeKind = MirrorRequestRuntimeSnapshotField
			default:
				shapeKind = MirrorNamedTransportField
			}

			for _, field := range st.Fields.List {
				for _, name := range field.Names {
					if allowedFields != nil && allowedFields[name.Name] {
						continue
					}
					meta, exists := KnownPlaneFields[name.Name]
					if !exists && len(name.Name) > 0 {
						exported := strings.ToUpper(name.Name[:1]) + name.Name[1:]
						meta, exists = KnownPlaneFields[exported]
					}
					if exists && meta.Wave <= maxCompletedWave {
						pos := fset.Position(name.Pos())
						addFinding(shapeKind, name.Name, meta.PlaneID,
							fmt.Sprintf("hand-authored plane field %q on struct %q is forbidden in wave %s", name.Name, structName, meta.Wave),
							pos.Line, meta.Wave)
					}
				}
			}

		case *ast.FuncDecl:
			funcName := node.Name.Name
			qualSym := QualifiedSymbol(relPath, node)
			if isDiagnosticsTargetFile(relPath) {
				inspectDiagnosticsBody(f, node, fset, maxCompletedWave, addFinding)
			} else if funcName == "Append" {
				inspectAppendBody(node, fset, maxCompletedWave, addFinding)
			} else if IsAllowedObserverProjection(qualSym) {
				// Exact qualified symbol allowlist: Observer view projection via Get is allowed
			} else if strings.Contains(strings.ToLower(funcName), "fromtransport") ||
				strings.Contains(strings.ToLower(funcName), "projecthook") ||
				strings.Contains(strings.ToLower(funcName), "hooksconfig") ||
				strings.Contains(relPath, "internal/infra/runtimebundle/build_feature_hooks.go") {
				inspectProjectionBody(node, fset, maxCompletedWave, addFinding)
			} else if isGenerationOpMethod(node) {
				inspectGenerationOpMethod(relPath, f, node, fset, maxCompletedWave, addFinding)
			} else if strings.Contains(relPath, "internal/testkit/") || strings.Contains(relPath, "internal/refbackend/") || strings.Contains(relPath, "internal/refclient/") {
				// Test harness and emulator packages are allowed to read planes via Get
			} else {
				inspectStageConsumers(relPath, f, node, fset, maxCompletedWave, addFinding)
			}
		}
		return true
	})

	return findings
}
