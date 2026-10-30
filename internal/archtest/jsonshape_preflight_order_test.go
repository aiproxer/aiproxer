package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestFrontendServeHTTPPreflightBeforeDecode locks shared create-pipeline call order by
// source position (not CFG dominance): reqbody.ReadAll -> jsonguard.PreflightWithContext ->
// decodeqos.TryAdmit -> decode (via spec.Decode inside decodeqos.Guard).
//
// A body-dependent route extraction is a full-body JSON parse, so it is decode-admission
// work: the pre-admission helper must not parse the body, and the body-model helper may
// only be called from inside the decode guard. The custom body hook, when present,
// is authoritative and must also run only inside the guard. Frontends delegate ServeHTTP to
// frontendpipe.ServeHTTP.
func TestFrontendServeHTTPPreflightBeforeDecode(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	path := filepath.Join(root, "internal", "plugins", "frontends", "frontendpipe", "pipe.go")
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	var serve *ast.FuncDecl
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name == nil || fn.Name.Name != "ServeHTTP" || fn.Body == nil {
			continue
		}
		serve = fn
		break
	}
	if serve == nil {
		t.Fatalf("frontendpipe: ServeHTTP not found")
	}
	var readAllPos, preflightPos, tryAdmitPos, decodeGuardPos token.Pos
	var preAdmissionRouteCalls []token.Pos
	var bodyModelRouteCalls []token.Pos
	var bodyHookCalls []token.Pos
	var bodyHookRefs []token.Pos
	var guardClosure *ast.FuncLit
	ast.Inspect(serve.Body, func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok && sel.Sel != nil && sel.Sel.Name == "ResolveBodyRoute" {
			bodyHookRefs = append(bodyHookRefs, sel.Pos())
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		pkg, cname := qualifiedCall(call.Fun)
		switch {
		case pkg == "reqbody" && cname == "ReadAll":
			if readAllPos == 0 {
				readAllPos = call.Pos()
			}
		case pkg == "jsonguard" && cname == "PreflightWithContext":
			if preflightPos == 0 {
				preflightPos = call.Pos()
			}
		case pkg == "decodeqos" && cname == "TryAdmit":
			if tryAdmitPos == 0 {
				tryAdmitPos = call.Pos()
			}
		case cname == "resolvePreAdmissionRoute":
			preAdmissionRouteCalls = append(preAdmissionRouteCalls, call.Pos())
		case cname == "resolveBodyModelRoute":
			bodyModelRouteCalls = append(bodyModelRouteCalls, call.Pos())
		case cname == "ResolveBodyRoute":
			bodyHookCalls = append(bodyHookCalls, call.Pos())
		case pkg == "decodeqos" && cname == "Guard":
			if decodeGuardPos == 0 {
				decodeGuardPos = call.Pos()
				for _, arg := range call.Args {
					if lit, ok := arg.(*ast.FuncLit); ok {
						guardClosure = lit
					}
				}
			}
		}
		return true
	})
	if readAllPos == 0 || preflightPos == 0 || tryAdmitPos == 0 || decodeGuardPos == 0 {
		t.Fatalf("frontendpipe ServeHTTP: missing ReadAll(%v) PreflightWithContext(%v) TryAdmit(%v) decodeqos.Guard(%v)",
			readAllPos != 0, preflightPos != 0, tryAdmitPos != 0, decodeGuardPos != 0)
	}
	if readAllPos >= preflightPos || preflightPos >= tryAdmitPos || tryAdmitPos >= decodeGuardPos {
		t.Fatalf("frontendpipe ServeHTTP: want ReadAll < PreflightWithContext < TryAdmit < decodeqos.Guard; positions %d %d %d %d",
			readAllPos, preflightPos, tryAdmitPos, decodeGuardPos)
	}
	if len(preAdmissionRouteCalls) == 0 {
		t.Fatal("frontendpipe ServeHTTP: pre-admission route resolution is missing")
	}
	for _, pos := range preAdmissionRouteCalls {
		if pos >= preflightPos {
			t.Fatalf("frontendpipe ServeHTTP: pre-admission route must run before preflight; call=%d preflight=%d",
				pos, preflightPos)
		}
		if guardClosure != nil && pos >= guardClosure.Pos() && pos <= guardClosure.End() {
			t.Fatalf("frontendpipe ServeHTTP: pre-admission route must not run inside the decode guard; call=%d guard=[%d,%d]",
				pos, guardClosure.Pos(), guardClosure.End())
		}
	}
	if len(bodyModelRouteCalls) == 0 && len(bodyHookCalls) == 0 {
		t.Fatal("frontendpipe ServeHTTP: body route resolution is missing")
	}
	if guardClosure == nil {
		t.Fatal("frontendpipe ServeHTTP: decode guard has no closure to bound the body route")
	}
	for _, pos := range bodyModelRouteCalls {
		if pos < guardClosure.Pos() || pos > guardClosure.End() {
			t.Fatalf("frontendpipe ServeHTTP: body-model route resolution must run inside the decode guard; guard=[%d,%d] call=%d",
				guardClosure.Pos(), guardClosure.End(), pos)
		}
	}
	for _, pos := range bodyHookCalls {
		if pos < guardClosure.Pos() || pos > guardClosure.End() {
			t.Fatalf("frontendpipe ServeHTTP: custom body route must run inside the decode guard; guard=[%d,%d] call=%d",
				guardClosure.Pos(), guardClosure.End(), pos)
		}
	}
	// Catch aliasing: hook := spec.ResolveBodyRoute before admission would
	// evade the direct-call check above. Any reference to the hook selector in
	// ServeHTTP must also be inside the guard.
	for _, pos := range bodyHookRefs {
		if pos < guardClosure.Pos() || pos > guardClosure.End() {
			t.Fatalf("frontendpipe ServeHTTP: body route hook reference must stay inside the decode guard; guard=[%d,%d] ref=%d",
				guardClosure.Pos(), guardClosure.End(), pos)
		}
	}
}

// TestSpecRouteHooksPhaseBoundary proves the callback contract is phased:
// the pre-admission hook must be incapable of receiving a request body, and
// the old raw-body pre-admission callback must be gone. A BodyRoute hook may
// exist, but ServeHTTP already proves it only runs inside the decode guard.
func TestSpecRouteHooksPhaseBoundary(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	path := filepath.Join(root, "internal", "plugins", "frontends", "frontendpipe", "pipe.go")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, content, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	var specStruct *ast.StructType
	for _, decl := range f.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok {
			continue
		}
		for _, sp := range gen.Specs {
			ts, ok := sp.(*ast.TypeSpec)
			if !ok || ts.Name == nil || ts.Name.Name != "Spec" {
				continue
			}
			// Spec is generic: type Spec[Opts any] struct {...}
			if st, ok := ts.Type.(*ast.StructType); ok {
				specStruct = st
			}
		}
	}
	if specStruct == nil {
		t.Fatal("frontendpipe: Spec struct not found")
	}
	var hasPreAdmission, hasBodyRoute bool
	for _, field := range specStruct.Fields.List {
		for _, name := range field.Names {
			switch name.Name {
			case "ResolveRouteSelector":
				t.Fatal("frontendpipe.Spec must not retain ResolveRouteSelector: raw-body pre-admission callback is forbidden")
			case "ResolvePreAdmissionRoute":
				hasPreAdmission = true
				if err := validatePreAdmissionFieldType(field); err != nil {
					t.Fatalf("frontendpipe.Spec.ResolvePreAdmissionRoute: %v", err)
				}
			case "ResolveBodyRoute":
				hasBodyRoute = true
			}
		}
	}
	if !hasPreAdmission {
		t.Fatal("frontendpipe.Spec must declare ResolvePreAdmissionRoute")
	}
	if !hasBodyRoute {
		t.Fatal("frontendpipe.Spec must declare ResolveBodyRoute")
	}
	_ = content
	impls, violations, err := findPreAdmissionImpls(root)
	if err != nil {
		t.Fatalf("find pre-admission impls: %v", err)
	}
	if len(violations) > 0 {
		t.Fatalf("uninspectable pre-admission implementations:\n%s", strings.Join(violations, "\n"))
	}
	if len(impls) == 0 {
		t.Fatal("no ResolvePreAdmissionRoute implementations found: expected at least the Gemini frontend")
	}
	for _, lit := range impls {
		if lit.Type != nil && len(lit.Type.Params.List) == 2 {
			if err := validatePreAdmissionFieldType(&ast.Field{Type: lit.Type}); err != nil {
				t.Fatalf("pre-admission implementation signature: %v", err)
			}
		}
		if err := validatePreAdmissionImpl(lit.Body); err != nil {
			t.Fatalf("pre-admission implementation: %v", err)
		}
	}
}

// TestSpecRouteHooksPhaseBoundary_NegativeMutation proves the validator
// itself rejects body-bearing signatures and body-parsing implementations.
// It exercises validatePreAdmissionFieldType and validatePreAdmissionImpl
// directly, so a bypass that evades the production check fails here.
func TestSpecRouteHooksPhaseBoundary_NegativeMutation(t *testing.T) {
	t.Parallel()
	parseField := func(src string) *ast.Field {
		t.Helper()
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, "synthetic.go", "package p\ntype S struct { F "+src+" }", 0)
		if err != nil {
			t.Fatalf("parse synthetic field: %v", err)
		}
		gen, ok := f.Decls[0].(*ast.GenDecl)
		if !ok {
			t.Fatal("synthetic decl is not a GenDecl")
		}
		ts, ok := gen.Specs[0].(*ast.TypeSpec)
		if !ok {
			t.Fatal("synthetic spec is not a TypeSpec")
		}
		st, ok := ts.Type.(*ast.StructType)
		if !ok {
			t.Fatal("synthetic type is not a StructType")
		}
		return st.Fields.List[0]
	}
	good := parseField("func(h http.Header, pm PathMatch) routing.ResolvedRoute")
	if err := validatePreAdmissionFieldType(good); err != nil {
		t.Fatalf("good signature rejected: %v", err)
	}
	for _, bad := range []string{
		"func(m RouteMetadata, pm PathMatch) routing.ResolvedRoute",
		"func(r *http.Request, pm PathMatch) routing.ResolvedRoute",
		"func(h http.Header, pm PathMatch, extra []byte) routing.ResolvedRoute",
		"func(headers []byte, pm PathMatch) routing.ResolvedRoute",
		"func(h http.Header, body []byte) routing.ResolvedRoute",
	} {
		if err := validatePreAdmissionFieldType(parseField(bad)); err == nil {
			t.Fatalf("bad signature accepted: %s", bad)
		}
	}
	parseFunc := func(src string) *ast.FuncLit {
		t.Helper()
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, "synthetic.go", "package p\nvar _ = "+src, 0)
		if err != nil {
			t.Fatalf("parse synthetic func: %v", err)
		}
		gen, ok := f.Decls[0].(*ast.GenDecl)
		if !ok {
			t.Fatal("synthetic decl is not a GenDecl")
		}
		vs, ok := gen.Specs[0].(*ast.ValueSpec)
		if !ok {
			t.Fatal("synthetic spec is not a ValueSpec")
		}
		lit, ok := vs.Values[0].(*ast.FuncLit)
		if !ok {
			t.Fatal("synthetic value is not a func literal")
		}
		return lit
	}
	if err := validatePreAdmissionImpl(parseFunc("func(h http.Header, pm PathMatch) routing.ResolvedRoute { return routing.ResolvedRoute{} }").Body); err != nil {
		t.Fatalf("good impl rejected: %v", err)
	}
	for _, badImpl := range []string{
		`func(h http.Header, pm PathMatch) routing.ResolvedRoute { _ = json.Unmarshal(body, &v); return routing.ResolvedRoute{} }`,
		`func(h http.Header, pm PathMatch) routing.ResolvedRoute { return prefixes.ResolveFromModelOrDefault(body, d) }`,
	} {
		if err := validatePreAdmissionImpl(parseFunc(badImpl).Body); err == nil {
			t.Fatalf("bad impl accepted: %s", badImpl)
		}
	}
}

// TestResolveRouteHelperDoesNotParseTheBody proves the pre-admission route
// helper never performs a body-dependent parse: a full-body JSON parse belongs
// to the decode guard, and hiding it behind a helper would bypass decode
// admission without changing ServeHTTP's call order.
func TestResolveRouteHelperDoesNotParseTheBody(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	path := filepath.Join(root, "internal", "plugins", "frontends", "frontendpipe", "route_resolution.go")
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	forbidden := map[string]struct{}{
		"FromModelOrDefault":        {},
		"ResolveFromModelOrDefault": {},
		"ResolveInlineOrDefault":    {},
		"Unmarshal":                 {},
	}
	seenPreAdmission := false
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name == nil || fn.Body == nil {
			continue
		}
		if fn.Name.Name == "resolveRoute" {
			t.Fatalf("frontendpipe.resolveRoute must not exist: pre-admission helper is resolvePreAdmissionRoute")
		}
		if fn.Name.Name != "resolvePreAdmissionRoute" {
			continue
		}
		seenPreAdmission = true
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			_, cname := qualifiedCall(call.Fun)
			if _, bad := forbidden[cname]; bad {
				t.Fatalf("frontendpipe.resolvePreAdmissionRoute must not parse the request body: calls %s at %d",
					cname, call.Pos())
			}
			return true
		})
	}
	if !seenPreAdmission {
		t.Fatal("frontendpipe.resolvePreAdmissionRoute not found")
	}
}

// TestToolCallRepairMaterializeAfterPreflight requires parseOrderedJSON /
// unmarshalSchemaJSON call sites (outside exempt helpers) to be preceded in the
// same function by preflightArgsJSON or preflightSchemaJSON (source order only).
// repairPreflightedArgsJSON is exempt: engine calls it only after args preflight
// and schema cache compile. materializeFillValue re-parses trusted in-memory fills.
func TestToolCallRepairMaterializeAfterPreflight(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	dir := filepath.Join(root, "internal", "plugins", "features", "toolcallrepair", "repair")
	paths, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	fset := token.NewFileSet()
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil || fn.Name == nil {
				continue
			}
			fname := fn.Name.Name
			switch fname {
			case "parseOrderedJSON", "unmarshalSchemaJSON", "materializeFillValue", "repairPreflightedArgsJSON":
				continue
			}
			var preflightPos token.Pos
			var materializeCalls []token.Pos
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				_, cname := qualifiedCall(call.Fun)
				switch cname {
				case "preflightArgsJSON", "preflightSchemaJSON":
					if preflightPos == 0 {
						preflightPos = call.Pos()
					}
				case "parseOrderedJSON", "unmarshalSchemaJSON":
					materializeCalls = append(materializeCalls, call.Pos())
				}
				return true
			})
			for _, mp := range materializeCalls {
				if preflightPos == 0 || preflightPos >= mp {
					t.Fatalf("%s:%s: must call preflightArgsJSON/preflightSchemaJSON before materialize",
						filepath.Base(path), fname)
				}
			}
		}
	}
}

func qualifiedCall(fun ast.Expr) (pkg, name string) {
	switch f := fun.(type) {
	case *ast.Ident:
		return "", f.Name
	case *ast.SelectorExpr:
		name = ""
		if f.Sel != nil {
			name = f.Sel.Name
		}
		if id, ok := f.X.(*ast.Ident); ok {
			return id.Name, name
		}
		return "", name
	}
	return "", ""
}
