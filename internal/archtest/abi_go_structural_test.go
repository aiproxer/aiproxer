package archtest

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/constant"
	"go/format"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
)

type PublicABISymbol struct {
	Category string `json:"category"`
	Name     string `json:"name"`
	Detail   string `json:"detail,omitempty"`
}

// ScanPublicBackendPluginABI discovers the complete exported Go ABI surface,
// including constants, vars, funcs, types, struct fields, and interface methods.
func ScanPublicBackendPluginABI(repoRoot string) ([]PublicABISymbol, error) {
	dir := repoRoot + "/pkg/aipsdk/backendplugin"
	fset := token.NewFileSet()
	//nolint:staticcheck // Structural ABI scanner intentionally needs ParseDir's file map.
	pkgs, err := parser.ParseDir(fset, dir, nil, 0)
	if err != nil {
		return nil, err
	}
	pkg := pkgs["backendplugin"]
	if pkg == nil {
		return nil, fmt.Errorf("backendplugin package not found")
	}
	constDetails := make(map[string]string)
	files := make([]*ast.File, 0, len(pkg.Files))
	for fileName, file := range pkg.Files {
		if !strings.HasSuffix(fileName, "_test.go") {
			files = append(files, file)
		}
	}
	if err := collectConstDetails(files, fset, constDetails); err != nil {
		return nil, fmt.Errorf("type-check backendplugin constants: %w", err)
	}
	var symbols []PublicABISymbol
	addDetail := func(cat, name, detail string) {
		symbols = append(symbols, PublicABISymbol{Category: cat, Name: name, Detail: detail})
	}
	add := func(cat, name string, node ast.Node) {
		addDetail(cat, name, formatABIDeclaration(fset, node))
	}
	for _, pkg := range pkgs {
		for fileName, file := range pkg.Files {
			if strings.HasSuffix(fileName, "_test.go") {
				continue
			}
			for _, decl := range file.Decls {
				switch d := decl.(type) {
				case *ast.GenDecl:
					for _, spec := range d.Specs {
						switch s := spec.(type) {
						case *ast.TypeSpec:
							if !ast.IsExported(s.Name.Name) {
								continue
							}
							cat := "type"
							if _, ok := s.Type.(*ast.StructType); ok {
								cat = "struct"
							}
							add(cat, s.Name.Name, s)
							if st, ok := s.Type.(*ast.StructType); ok {
								for _, f := range st.Fields.List {
									for _, n := range f.Names {
										if ast.IsExported(n.Name) {
											add("field", s.Name.Name+"."+n.Name, f)
										}
									}
								}
							}
							if it, ok := s.Type.(*ast.InterfaceType); ok {
								for _, f := range it.Methods.List {
									for _, n := range f.Names {
										if ast.IsExported(n.Name) {
											add("method", s.Name.Name+"."+n.Name, f)
										}
									}
								}
							}
						case *ast.ValueSpec:
							for i, n := range s.Names {
								if ast.IsExported(n.Name) {
									cat := "var"
									if d.Tok == token.CONST {
										cat = "const"
									}
									valueSpec := *s
									if d.Tok != token.CONST {
										valueSpec.Values = nil
									} else if i < len(s.Values) {
										valueSpec.Names = []*ast.Ident{n}
										valueSpec.Values = []ast.Expr{s.Values[i]}
									}
									if cat == "const" {
										detail, ok := constDetails[n.Name]
										if !ok {
											return nil, fmt.Errorf("missing type-checked constant %s", n.Name)
										}
										addDetail(cat, n.Name, detail)
									} else {
										add(cat, n.Name, &valueSpec)
									}
								}
							}
						}
					}
				case *ast.FuncDecl:
					if d.Recv != nil && ast.IsExported(d.Name.Name) {
						add("method", receiverName(d.Recv)+"."+d.Name.Name, d)
					} else if d.Recv == nil && ast.IsExported(d.Name.Name) {
						add("func", d.Name.Name, d)
					}
				}
			}
		}
	}
	slices.SortFunc(symbols, func(a, b PublicABISymbol) int {
		if a.Category != b.Category {
			return strings.Compare(a.Category, b.Category)
		}
		return strings.Compare(a.Name, b.Name)
	})
	return symbols, nil
}

func collectConstDetails(files []*ast.File, fset *token.FileSet, out map[string]string) error {
	// Load the real module package so go/types resolves all generated and external
	// imports; parser-only checking cannot resolve module-local import paths.
	if len(files) > 0 {
		name := filepath.Base(filepath.Dir(fset.Position(files[0].Pos()).Filename))
		if name == "backendplugin" {
			cfg := &packages.Config{Mode: packages.NeedName | packages.NeedTypes | packages.NeedTypesInfo, Dir: filepath.Dir(fset.Position(files[0].Pos()).Filename)}
			loaded, err := packages.Load(cfg, ".")
			if err != nil {
				return err
			}
			if len(loaded) == 1 && loaded[0].TypesInfo != nil {
				for ident, object := range loaded[0].TypesInfo.Defs {
					if c, ok := object.(*types.Const); ok && ident != nil && ast.IsExported(ident.Name) {
						out[ident.Name] = formatTypedConst(ident.Name, c)
					}
				}
				return nil
			}
		}
	}
	// Fixture tests use a standalone package and intentionally have no module
	// imports, so a local checker is sufficient and deterministic there.
	info := &types.Info{Defs: make(map[*ast.Ident]types.Object)}
	checker := types.Config{IgnoreFuncBodies: true, Error: func(error) {}}
	_, _ = checker.Check("fixture", fset, files, info)
	for ident, object := range info.Defs {
		if c, ok := object.(*types.Const); ok && ident != nil && ast.IsExported(ident.Name) {
			out[ident.Name] = formatTypedConst(ident.Name, c)
		}
	}
	return nil
}

func formatTypedConst(name string, c *types.Const) string {
	value := c.Val().String()
	if c.Val().Kind() == constant.String {
		value = strconv.Quote(constant.StringVal(c.Val()))
	}
	qualifier := func(pkg *types.Package) string {
		if pkg == nil {
			return ""
		}
		return pkg.Name()
	}
	typ := types.TypeString(c.Type(), qualifier)
	return name + " " + typ + " = " + value
}

func formatExpr(expr ast.Expr) string {
	var buf strings.Builder
	_ = format.Node(&buf, token.NewFileSet(), expr)
	return strings.TrimSpace(buf.String())
}

func receiverName(field *ast.FieldList) string {
	if field == nil || len(field.List) == 0 {
		return ""
	}
	var buf strings.Builder
	_ = format.Node(&buf, token.NewFileSet(), field.List[0].Type)
	return strings.TrimPrefix(buf.String(), "*")
}

// formatABIDeclaration emits declaration structure only.
func formatABIDeclaration(fset *token.FileSet, node ast.Node) string {
	var expr ast.Node
	switch n := node.(type) {
	case *ast.TypeSpec:
		expr = n
	case *ast.Field:
		expr = n
	case *ast.FuncDecl:
		copied := *n
		copied.Body = nil
		expr = &copied
	case *ast.ValueSpec:
		// Const initializer expressions are ABI-significant. Var initializers
		// are cleared by the scanner before reaching this formatter.
		expr = n
	default:
		return ""
	}
	var buf strings.Builder
	if err := format.Node(&buf, fset, expr); err != nil {
		return ""
	}
	return strings.TrimSpace(buf.String())
}

// legacyGoABISymbols is the exact v1.3 declaration allowlist.
var legacyGoABISymbols = map[string]PublicABISymbol{
	"const:FeatureExactOpenResponsesFields":         {Category: "const", Name: "FeatureExactOpenResponsesFields", Detail: `FeatureExactOpenResponsesFields untyped string = "exact_openresponses_fields"`},
	"func:RequireExactOpenResponsesABISupport":      {Category: "func", Name: "RequireExactOpenResponsesABISupport", Detail: "func RequireExactOpenResponsesABISupport(neg Negotiation, call aipapi.Call) error"},
	"func:RequireExactOpenResponsesEventABISupport": {Category: "func", Name: "RequireExactOpenResponsesEventABISupport", Detail: "func RequireExactOpenResponsesEventABISupport(neg Negotiation, ev *CanonicalEvent) error"},
	"var:ErrExactOpenResponsesUnsupported":          {Category: "var", Name: "ErrExactOpenResponsesUnsupported", Detail: "ErrExactOpenResponsesUnsupported"},
}

var exactProtocolVersionSymbols = map[string]string{
	"ProtocolMajorV1":                          "ProtocolMajorV1 uint32 = 1",
	"ProtocolMinorExactReasoningParts":         "ProtocolMinorExactReasoningParts uint32 = 1",
	"ProtocolMinorOrderedItems":                "ProtocolMinorOrderedItems uint32 = 2",
	"ProtocolMinorExactOpenResponsesFields":    "ProtocolMinorExactOpenResponsesFields uint32 = 3",
	"ProtocolMinorProxyOwnedSessionID":         "ProtocolMinorProxyOwnedSessionID uint32 = 4",
	"ProtocolMinorAccountingEvidence":          "ProtocolMinorAccountingEvidence uint32 = 5",
	"ProtocolMinorSemanticExtensions":          "ProtocolMinorSemanticExtensions uint32 = 6",
	"ProtocolMinorPromptCacheResidency":        "ProtocolMinorPromptCacheResidency uint32 = 7",
	"ProtocolMinorCancellationHandshake":       "ProtocolMinorCancellationHandshake uint32 = 8",
	"ProtocolMinorAccountingEvidenceV2":        "ProtocolMinorAccountingEvidenceV2 uint32 = 9",
	"ProtocolMinorEconomicsV2":                 "ProtocolMinorEconomicsV2 uint32 = 9",
	"ProtocolMinorEconomicEvidenceV2":          "ProtocolMinorEconomicEvidenceV2 uint32 = 9",
	"ProtocolMinorHostBoundEconomicEvidenceV2": "ProtocolMinorHostBoundEconomicEvidenceV2 uint32 = 10",
	"ProtocolMinorReasoningEffortValues":       "ProtocolMinorReasoningEffortValues uint32 = 11",
}

func protocolSpecificABISymbol(name string) bool {
	value := strings.ToLower(name)
	if strings.HasPrefix(name, "ProtocolMajor") || strings.HasPrefix(name, "ProtocolMinor") {
		return true
	}
	for _, marker := range []string{"openai", "openresponses", "anthropic", "gemini", "bedrock", "codex", "acp", "claude"} {
		if strings.Contains(value, marker) {
			return true
		}
	}
	// A protocol qualifier is not defined by a finite provider denylist. Treat
	// an unknown leading vocabulary word before a semantic carrier as a
	// protocol-owned ABI name (for example ContosoRequest or FabrikamEvent).
	words := identifierWords(name)
	if len(words) < 2 || neutralABITerms[words[0]] {
		return false
	}
	for _, word := range words[1:] {
		switch word {
		case "invocation", "request", "response", "event", "message", "field", "fields", "schema", "feature", "features", "capability", "capabilities", "dialect", "extension", "extensions", "payload", "carrier":
			return true
		}
	}
	return false
}

func ValidatePublicBackendPluginABIMutation(symbols []PublicABISymbol) error {
	for _, s := range symbols {
		if !protocolSpecificABISymbol(s.Name) {
			continue
		}
		if s.Category == "const" && strings.HasPrefix(s.Name, "ProtocolMajor") || s.Category == "const" && strings.HasPrefix(s.Name, "ProtocolMinor") {
			if want, ok := exactProtocolVersionSymbols[s.Name]; ok && s.Detail == want {
				continue
			}
			return fmt.Errorf("protocol version constant %q is not an exact known declaration: got %q", s.Name, s.Detail)
		}
		key := s.Category + ":" + s.Name
		allowed, ok := legacyGoABISymbols[key]
		if !ok || allowed.Detail != s.Detail {
			return fmt.Errorf("protocol-specific exported ABI symbol %s %q is not an exact legacy declaration: got %q want %q", s.Category, s.Name, s.Detail, allowed.Detail)
		}
	}
	return nil
}

func TestPublicBackendPluginABIBaselineIsExact(t *testing.T) {
	t.Parallel()
	actual, err := ScanPublicBackendPluginABI("../..")
	if err != nil {
		t.Fatalf("scan public backend-plugin ABI: %v", err)
	}
	if os.Getenv("UPDATE_BASELINE") == "1" {
		data, err := json.MarshalIndent(actual, "", "  ")
		if err != nil {
			t.Fatalf("marshal actual ABI baseline: %v", err)
		}
		if err := os.WriteFile("testdata/backend_plugin_go_abi_baseline.json", append(data, '\n'), 0o644); err != nil {
			t.Fatalf("write ABI baseline: %v", err)
		}
		return
	}
	raw, err := os.ReadFile("testdata/backend_plugin_go_abi_baseline.json")
	if err != nil {
		t.Fatalf("read checked-in ABI baseline: %v", err)
	}
	var expected []PublicABISymbol
	if err := json.Unmarshal(raw, &expected); err != nil {
		t.Fatalf("decode checked-in ABI baseline: %v", err)
	}
	if !slices.Equal(actual, expected) {
		for i := 0; i < len(actual) && i < len(expected); i++ {
			if actual[i] != expected[i] {
				t.Fatalf("public backend-plugin Go ABI differs at %d: got=%+v want=%+v (counts got %d want %d)", i, actual[i], expected[i], len(actual), len(expected))
			}
		}
		t.Fatalf("public backend-plugin Go ABI differs: got %d declarations, want %d", len(actual), len(expected))
	}
}
