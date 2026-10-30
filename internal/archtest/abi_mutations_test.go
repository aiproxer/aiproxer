package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestStructuralABIMutationGuards(t *testing.T) {
	t.Parallel()
	for _, source := range []string{
		`message Invocation { string prompt_cache_key = 20; }`,
		`message Invocation { bytes prompt_cache_key = 19; }`,
		`message OpenAIExtension { string arbitrary = 1; }`,
	} {
		if err := ValidateProtoSchema(source); err == nil {
			t.Fatalf("expected structural ABI mutation to fail: %s", source)
		}
	}
	for _, name := range []string{"AnthropicRequest", "ContosoInvocation", "FabrikamInvocation", "ContosoRequest", "FabrikamEvent", "ContosoSnakeField", "ContosoSchema", "ContosoDialect"} {
		if err := ValidatePublicBackendPluginABIMutation([]PublicABISymbol{{Category: "type", Name: name}}); err == nil {
			t.Fatalf("expected protocol-specific public ABI type mutation %q to fail", name)
		}
	}
	for _, source := range []string{
		`message ContosoRequest { string value = 1; }`,
		`message FabrikamEvent { string contoso_payload = 2; }`,
		`message FabrikamInvocation { string value = 1; }`,
	} {
		if err := ValidateProtoSchema(source + "\n" + stringMustReadBackendProto()); err == nil {
			t.Fatalf("expected arbitrary protocol proto mutation to fail: %s", source)
		}
	}
}

func TestProtoSchemaBaselineMutationMatrix(t *testing.T) {
	t.Parallel()
	path := "../../api/backendplugin/v1/backend.proto"
	baselineBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	baseline := string(baselineBytes)
	if err := ValidateProtoSchema(baseline); err != nil {
		t.Fatalf("real backend.proto baseline must validate first: %v", err)
	}
	mutations := []struct {
		name   string
		mutate func(string) string
	}{
		{"move", func(s string) string {
			s = strings.Replace(s, "string prompt_cache_key = 19;", "", 1)
			return strings.Replace(s, "message Invocation {", "message Moved { string prompt_cache_key = 19; }\n\nmessage Invocation {", 1)
		}},
		{"rename", func(s string) string {
			return strings.Replace(s, "string prompt_cache_key = 19;", "string prompt_cache_hint = 19;", 1)
		}},
		{"number", func(s string) string {
			return strings.Replace(s, "string prompt_cache_key = 19;", "string prompt_cache_key = 99;", 1)
		}},
		{"type", func(s string) string {
			return strings.Replace(s, "string prompt_cache_key = 19;", "bytes prompt_cache_key = 19;", 1)
		}},
		{"label", func(s string) string {
			return strings.Replace(s, "string prompt_cache_key = 19;", "optional string prompt_cache_key = 19;", 1)
		}},
		{"options", func(s string) string {
			return strings.Replace(s, "string prompt_cache_key = 19;", "string prompt_cache_key = 19 [deprecated = true];", 1)
		}},
		{"comment-smuggling", func(s string) string {
			return strings.Replace(s, "message Invocation {", "message Invocation { string openai_prompt_cache_key = 99; // smuggled protocol field\n", 1)
		}},
		{"multiline", func(s string) string {
			return strings.Replace(s, "message Invocation {", "message OpenAIExtension {\n  string value = 1;\n}\n\nmessage Invocation {", 1)
		}},
		{"protocol-message", func(s string) string {
			return strings.Replace(s, "message Invocation {", "message AnthropicExtension { string value = 1; }\n\nmessage Invocation {", 1)
		}},
		{"protocol-field", func(s string) string {
			return strings.Replace(s, "string prompt_cache_key = 19;", "string openai_prompt_cache_key = 19;", 1)
		}},
		{"allowed-semantic-extension", func(s string) string {
			return strings.Replace(s, "message Feature {", "message SemanticExtension { string namespace = 1; }\n\nmessage Feature {", 1)
		}},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			t.Parallel()
			if got := mutation.mutate(baseline); got == baseline {
				t.Fatal("mutation did not change the complete baseline")
			}
			if err := ValidateProtoSchema(mutation.mutate(baseline)); mutation.name == "allowed-semantic-extension" {
				if err != nil {
					t.Fatalf("allowed semantic extension rejected: %v", err)
				}
			} else if err == nil {
				t.Fatalf("expected specific ABI mutation %q to fail", mutation.name)
			}
		})
	}
}

func TestCollectConstDetailsUsesEffectiveTypedValues(t *testing.T) {
	t.Parallel()
	check := func(source string) map[string]string {
		t.Helper()
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, "fixture.go", source, 0)
		if err != nil {
			t.Fatal(err)
		}
		got := make(map[string]string)
		if err := collectConstDetails([]*ast.File{file}, fset, got); err != nil {
			t.Fatal(err)
		}
		return got
	}
	baseline := `package fixture
const (
	RawJSONAbsent RawJSONState = iota
	RawJSONNull
	RawJSONValue
	Explicit uint32 = uint32(iota + 10)
	Inherited = Explicit
)
type RawJSONState int`
	base := check(baseline)
	want := map[string]string{
		"RawJSONAbsent": "RawJSONAbsent fixture.RawJSONState = 0",
		"RawJSONNull":   "RawJSONNull fixture.RawJSONState = 1",
		"RawJSONValue":  "RawJSONValue fixture.RawJSONState = 2",
		"Explicit":      "Explicit uint32 = 13",
		"Inherited":     "Inherited uint32 = 13",
	}
	for name, expected := range want {
		if base[name] != expected {
			t.Fatalf("%s: got %q want %q", name, base[name], expected)
		}
	}
	mutations := []string{
		strings.Replace(baseline, "RawJSONAbsent RawJSONState = iota", "RawJSONAbsent RawJSONState = 0", 1),
		strings.Replace(baseline, "RawJSONNull\n", "RawJSONNull = 99\n", 1),
		strings.Replace(baseline, "Explicit uint32 = uint32(iota + 10)", "Explicit uint32 = uint32(iota + 20)", 1),
		strings.Replace(baseline, "RawJSONNull\n\tRawJSONValue", "RawJSONValue\n\tRawJSONNull", 1),
	}
	for i, source := range mutations {
		got := check(source)
		if reflect.DeepEqual(base, got) {
			t.Fatalf("mutation %d did not change effective constant snapshot", i)
		}
	}
}

func TestProtocolVersionABIPolicyIsExact(t *testing.T) {
	t.Parallel()
	for _, name := range []string{
		"ProtocolMajorOpenAIWidgets", "ProtocolMinorOpenAIWidgets",
		"ProtocolMajorV1Suffix", "ProtocolMinorExactOpenResponsesFieldsSuffix",
	} {
		if err := ValidatePublicBackendPluginABIMutation([]PublicABISymbol{{Category: "const", Name: name, Detail: name + " uint32 = 1"}}); err == nil {
			t.Fatalf("near-miss protocol declaration %q was accepted", name)
		}
	}
	for _, symbol := range []PublicABISymbol{
		{Category: "const", Name: "ProtocolMajorV1", Detail: "ProtocolMajorV1 uint32 = 1"},
		{Category: "const", Name: "ProtocolMinorExactReasoningParts", Detail: "ProtocolMinorExactReasoningParts uint32 = 1"},
		{Category: "const", Name: "ProtocolMinorOrderedItems", Detail: "ProtocolMinorOrderedItems uint32 = 2"},
		{Category: "const", Name: "ProtocolMinorExactOpenResponsesFields", Detail: "ProtocolMinorExactOpenResponsesFields uint32 = 3"},
		{Category: "const", Name: "ProtocolMinorProxyOwnedSessionID", Detail: "ProtocolMinorProxyOwnedSessionID uint32 = 4"},
	} {
		if err := ValidatePublicBackendPluginABIMutation([]PublicABISymbol{symbol}); err != nil {
			t.Fatalf("legitimate protocol declaration %q rejected: %v", symbol.Name, err)
		}
	}
	for _, symbol := range []PublicABISymbol{
		{Category: "const", Name: "ProtocolMajorV1", Detail: "ProtocolMajorV1 string = 1"},
		{Category: "const", Name: "ProtocolMinorOrderedItems", Detail: "ProtocolMinorOrderedItems uint32 = 3"},
	} {
		if err := ValidatePublicBackendPluginABIMutation([]PublicABISymbol{symbol}); err == nil {
			t.Fatalf("wrong protocol declaration %q was accepted", symbol.Name)
		}
	}
}

func TestPublicABISnapshotIgnoresFunctionBodies(t *testing.T) {
	t.Parallel()
	parse := func(source string) (*ast.FuncDecl, *token.FileSet) {
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, "fixture.go", source, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range file.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok {
				return fn, fset
			}
		}
		t.Fatal("fixture contains no function declaration")
		return nil, fset
	}
	beforeDecl, beforeSet := parse("package p; func Exported() int { return 1 }")
	afterDecl, afterSet := parse("package p; func Exported() int { return 999; panic(\"body\") }")
	if before := formatABIDeclaration(beforeSet, beforeDecl); before != formatABIDeclaration(afterSet, afterDecl) {
		t.Fatalf("body-only mutation changed free-function ABI declaration")
	}
	beforeDecl, beforeSet = parse("package p; type S struct{}; func (S) Exported() int { return 1 }")
	afterDecl, afterSet = parse("package p; type S struct{}; func (S) Exported() int { return 999; panic(\"body\") }")
	if before := formatABIDeclaration(beforeSet, beforeDecl); before != formatABIDeclaration(afterSet, afterDecl) {
		t.Fatalf("body-only mutation changed receiver-method ABI declaration")
	}
}

// snapshotFixtureABI is deliberately independent from the repository scanner:
// it snapshots a single parsed fixture and is used only for mutation evidence.
func snapshotFixtureABI(t *testing.T, source string) []PublicABISymbol {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "fixture.go", source, 0)
	if err != nil {
		t.Fatal(err)
	}
	var out []PublicABISymbol
	add := func(category, name string, node ast.Node) {
		out = append(out, PublicABISymbol{Category: category, Name: name, Detail: formatABIDeclaration(fset, node)})
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
					category := "type"
					if _, ok := s.Type.(*ast.StructType); ok {
						category = "struct"
					}
					add(category, s.Name.Name, s)
					if st, ok := s.Type.(*ast.StructType); ok {
						for _, field := range st.Fields.List {
							for _, name := range field.Names {
								if ast.IsExported(name.Name) {
									add("field", s.Name.Name+"."+name.Name, field)
								}
							}
						}
					}
					if it, ok := s.Type.(*ast.InterfaceType); ok {
						for _, field := range it.Methods.List {
							for _, name := range field.Names {
								if ast.IsExported(name.Name) {
									add("method", s.Name.Name+"."+name.Name, field)
								}
							}
						}
					}
				case *ast.ValueSpec:
					for i, name := range s.Names {
						if !ast.IsExported(name.Name) {
							continue
						}
						value := *s
						if d.Tok != token.CONST {
							value.Values = nil
						} else if i < len(s.Values) {
							value.Names = []*ast.Ident{name}
							value.Values = []ast.Expr{s.Values[i]}
						}
						category := "var"
						if d.Tok == token.CONST {
							category = "const"
						}
						add(category, name.Name, &value)
					}
				}
			}
		case *ast.FuncDecl:
			if !ast.IsExported(d.Name.Name) {
				continue
			}
			if d.Recv != nil {
				add("method", receiverName(d.Recv)+"."+d.Name.Name, d)
			} else {
				add("func", d.Name.Name, d)
			}
		}
	}
	slices.SortFunc(out, func(a, b PublicABISymbol) int {
		if a.Category != b.Category {
			return strings.Compare(a.Category, b.Category)
		}
		return strings.Compare(a.Name, b.Name)
	})
	return out
}

func TestPublicABISnapshotIndependentMutationMatrix(t *testing.T) {
	t.Parallel()
	baseline := "package fixture\n" +
		"const ExportedConst int = 1\nvar ExportedVar string\n" +
		"func Exported[T any](T) T { return *new(T) }\n" +
		"type Embedded struct{}\n" +
		"type Named[T any] struct { Field string `json:\"field\"`; Embedded }\n" +
		"type Alias = Named[int]\n" +
		"type API interface { Method(string) error; Embedded }\n" +
		"func (Named[T]) Method(V int) error { return nil }\n"
	base := snapshotFixtureABI(t, baseline)
	cases := []struct {
		name, source, category, symbol string
		same                           bool
	}{
		{"const-value", strings.Replace(baseline, "= 1", "= 2", 1), "const", "ExportedConst", false},
		{"const-type", strings.Replace(baseline, "ExportedConst int", "ExportedConst int64", 1), "const", "ExportedConst", false},
		{"var-initializer", strings.Replace(baseline, "var ExportedVar string", "var ExportedVar string = \"initialized\"", 1), "var", "ExportedVar", true},
		{"var-type", strings.Replace(baseline, "var ExportedVar string", "var ExportedVar []byte", 1), "var", "ExportedVar", false},
		{"func-signature", strings.Replace(baseline, "func Exported[T any](T) T", "func Exported[T any](T, string) T", 1), "func", "Exported", false},
		{"func-type-params", strings.Replace(baseline, "func Exported[T any]", "func Exported[T interface{ ~string }]", 1), "func", "Exported", false},
		{"named-underlying", strings.Replace(baseline, "type Named[T any] struct", "type Named[T any] []string //", 1), "type", "Named", false},
		{"named-type-params", strings.Replace(baseline, "type Named[T any]", "type Named[T ~string]", 1), "type", "Named", false},
		{"alias-target", strings.Replace(baseline, "type Alias = Named[int]", "type Alias = Embedded", 1), "type", "Alias", false},
		{"struct-field-type", strings.Replace(baseline, "Field string", "Field []byte", 1), "field", "Named.Field", false},
		{"struct-field-tag", strings.Replace(baseline, "json:\"field\"", "json:\"changed\"", 1), "field", "Named.Field", false},
		{"struct-field-add", strings.Replace(baseline, "; Embedded }", "; Added int; Embedded }", 1), "struct", "Named", false},
		{"struct-field-remove", strings.Replace(baseline, "Field string `json:\"field\"`; ", "", 1), "struct", "Named", false},
		{"struct-embedding", strings.Replace(baseline, "; Embedded }", "; Extra; Embedded }", 1), "struct", "Named", false},
		{"interface-signature", strings.Replace(baseline, "Method(string) error", "Method([]byte) error", 1), "method", "API.Method", false},
		{"interface-embedding", strings.Replace(baseline, "type API interface { Method(string) error; Embedded }", "type API interface { Method(string) error; Embedded; Extra }", 1), "type", "API", false},
		{"receiver-signature", strings.Replace(baseline, "Method(V int)", "Method(V string)", 1), "method", "Named.Method", false},
		{"receiver-substitution", strings.Replace(baseline, "func (Named[T]) Method", "func (Embedded) Method", 1), "method", "Embedded.Method", false},
		{"generic-add", baseline + "type Added[T any] struct{}\n", "type", "Added", false},
		{"generic-remove", strings.Replace(baseline, "type Embedded struct{}\n", "", 1), "type", "Embedded", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := snapshotFixtureABI(t, tc.source)
			if tc.same {
				if !reflect.DeepEqual(base, got) {
					t.Fatal("initializer-only mutation changed ABI snapshot")
				}
				return
			}
			if reflect.DeepEqual(base, got) {
				t.Fatal("mutation produced no ABI snapshot delta")
			}
			var before, after string
			for _, symbol := range base {
				if symbol.Category == tc.category && symbol.Name == tc.symbol {
					before = symbol.Detail
				}
			}
			for _, symbol := range got {
				if symbol.Category == tc.category && symbol.Name == tc.symbol {
					after = symbol.Detail
				}
			}
			if before != "" && before == after {
				t.Fatalf("mutation %s did not change exact snapshot", tc.name)
			}
		})
	}
	for _, symbol := range []string{"AnthropicRequest", "GeminiEvent", "ProtocolOpenAIField"} {
		if err := ValidatePublicBackendPluginABIMutation([]PublicABISymbol{{Category: "type", Name: symbol}}); err == nil {
			t.Fatalf("arbitrary protocol symbol %q was accepted", symbol)
		}
	}
	allowed := []PublicABISymbol{
		{Category: "const", Name: "FeatureExactOpenResponsesFields", Detail: `FeatureExactOpenResponsesFields untyped string = "exact_openresponses_fields"`},
		{Category: "func", Name: "RequireExactOpenResponsesABISupport", Detail: "func RequireExactOpenResponsesABISupport(neg Negotiation, call aipapi.Call) error"},
		{Category: "var", Name: "ErrExactOpenResponsesUnsupported", Detail: "ErrExactOpenResponsesUnsupported"},
	}
	for _, symbol := range allowed {
		t.Run(symbol.Name, func(t *testing.T) {
			t.Parallel()
			if err := ValidatePublicBackendPluginABIMutation([]PublicABISymbol{symbol}); err != nil {
				t.Fatalf("allowed OpenResponses boundary %q rejected: %v", symbol.Name, err)
			}
		})
	}
}

func stringMustReadBackendProto() string {
	b, _ := os.ReadFile("../../api/backendplugin/v1/backend.proto")
	return string(b)
}
