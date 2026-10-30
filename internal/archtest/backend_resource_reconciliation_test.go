package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

const backendResourceRuntimeBundle = "internal/infra/runtimebundle"

var backendResourcePrivateFiles = map[string]bool{
	"internal/infra/runtimebundle/backend_resource_identity.go": true,
	"internal/infra/runtimebundle/backend_resource_pool.go":     true,
	"internal/infra/runtimebundle/composition_root.go":          true,
	"internal/infra/runtimebundle/discovered_factories.go":      true,
	"internal/infra/runtimebundle/host_build.go":                true,
	"internal/infra/runtimebundle/plugin_catalog.go":            true,
	"internal/infra/runtimebundle/process_services.go":          true,
	"internal/infra/runtimebundle/process_services_types.go":    true,
	"internal/infra/runtimebundle/validate_distribution.go":     true,
}

func parseBackendResource(t *testing.T, filename, source string) *ast.File {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), filename, source, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", filename, err)
	}
	return f
}

func walkBackendResourceProduction(root string, fn func(string, *ast.File) error) error {
	for _, dir := range []string{"cmd", "internal", "pkg", "connectors", "connector-support"} {
		base := filepath.Join(root, dir)
		if _, err := os.Stat(base); err != nil {
			continue
		}
		if err := filepath.Walk(base, func(path string, info os.FileInfo, walkErr error) error {
			if walkErr != nil || info.IsDir() {
				if info != nil && info.IsDir() && (info.Name() == "vendor" || info.Name() == "testdata" || info.Name() == "node_modules") {
					return filepath.SkipDir
				}
				return walkErr
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			if strings.HasPrefix(rel, "internal/archtest/") {
				return nil
			}
			src, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			f, err := parser.ParseFile(token.NewFileSet(), path, src, parser.SkipObjectResolution)
			if err != nil {
				return err
			}
			return fn(rel, f)
		}); err != nil {
			return err
		}
	}
	return nil
}

func backendResourceIdentifier(name string) bool {
	lower := strings.ToLower(name)
	return strings.Contains(lower, "backendresource") || lower == "physicalidentity" ||
		lower == "framedsha256" || lower == "writeruntimepolicy" || lower == "normalizedallowedenvnames" ||
		lower == "fingerprintbackendresourcesecrets" || lower == "strippooledbackendlifecycle" ||
		lower == "pluginresourcepool" || lower == "resourcepool"
}

func topLevelNames(f *ast.File) []string {
	var names []string
	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			names = append(names, d.Name.Name)
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				if s, ok := spec.(*ast.TypeSpec); ok {
					names = append(names, s.Name.Name)
				} else if s, ok := spec.(*ast.ValueSpec); ok {
					for _, n := range s.Names {
						names = append(names, n.Name)
					}
				}
			}
		}
	}
	return names
}

func scanBackendResourcePrivateScope(rel string, f *ast.File) []string {
	var out []string
	if !backendResourcePrivateFiles[rel] {
		ast.Inspect(f, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok && backendResourceIdentifier(id.Name) {
				out = append(out, rel+": "+id.Name)
			}
			return true
		})
		return out
	}
	for _, name := range topLevelNames(f) {
		if backendResourceIdentifier(name) && ast.IsExported(name) {
			out = append(out, rel+": exported "+name)
		}
	}
	return out
}

func scanForbiddenRuntimebundleImport(rel string, f *ast.File) []string {
	if !strings.HasPrefix(rel, "internal/core/") && !strings.HasPrefix(rel, "pkg/aipapi/") &&
		!strings.HasPrefix(rel, "pkg/aipsdk/") &&
		!strings.HasPrefix(rel, "connectors/") && !strings.HasPrefix(rel, "connector-support/") {
		return nil
	}
	for _, imp := range f.Imports {
		path := strings.Trim(imp.Path.Value, `"`)
		if strings.Contains(path, "/internal/infra/runtimebundle") {
			return []string{rel + ": " + path}
		}
	}
	return nil
}

func genericResourceRegistryName(name string) bool {
	lower := strings.ToLower(name)
	if lower == "backendresourcepool" {
		return false
	}
	if strings.HasPrefix(lower, "backendresource") && (strings.HasSuffix(lower, "registry") || strings.HasSuffix(lower, "container") || strings.HasSuffix(lower, "locator")) {
		return true
	}
	return slices.Contains([]string{
		"resourcepool", "resourceregistry", "resourcecontainer", "resourcelocator", "resourceframework",
		"serviceregistry", "servicecontainer", "servicelocator", "dependencyregistry", "dependencycontainer",
		"componentregistry", "componentcontainer", "componentgraph", "getresource", "resolveresource",
		"registerresource", "acquireresource", "releaseresource",
	}, lower)
}

func scanGenericResourceRegistryAPI(f *ast.File) []string {
	var out []string
	for _, name := range topLevelNames(f) {
		if genericResourceRegistryName(name) {
			out = append(out, name)
		}
	}
	return out
}

func scanPoolSupervisorViolations(f *ast.File) []string {
	var out []string
	for _, imp := range f.Imports {
		path := strings.Trim(imp.Path.Value, `"`)
		if path == "net" || path == "os/exec" || path == "google.golang.org/grpc" ||
			strings.Contains(path, "/internal/infra/backendplugins/processhost") {
			out = append(out, "supervisor import "+path)
		}
	}
	banned := " Activate Authenticate Configure Dial Launch Listen NewHost Reap Serve Spawn Start Stop Supervise "
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		name := ""
		switch fun := call.Fun.(type) {
		case *ast.Ident:
			name = fun.Name
		case *ast.SelectorExpr:
			name = fun.Sel.Name
		}
		if strings.Contains(banned, " "+name+" ") {
			out = append(out, "supervisor call "+name)
		}
		return true
	})
	return out
}

func backendResourceIdentName(expr ast.Expr) string {
	if id, ok := expr.(*ast.Ident); ok {
		return id.Name
	}
	return ""
}

func backendResourceSelector(expr ast.Expr) (string, string) {
	if sel, ok := expr.(*ast.SelectorExpr); ok {
		return backendResourceIdentName(sel.X), sel.Sel.Name
	}
	return "", ""
}

func backendResourceFunc(f *ast.File, name string) *ast.FuncDecl {
	for _, decl := range f.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name != nil && fn.Name.Name == name {
			return fn
		}
	}
	return nil
}

func scanPooledLifecycleContract(f *ast.File) []string {
	var out []string
	strip := backendResourceFunc(f, "stripPooledBackendLifecycle")
	want := map[string]bool{"Close": false, "Start": false, "Stop": false, "CleanupIdleTransports": false}
	if strip == nil {
		return []string{"missing stripPooledBackendLifecycle"}
	}
	leaseCleanup, physicalCleanup := false, false
	ast.Inspect(f, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.AssignStmt:
			if len(x.Lhs) == 1 && len(x.Rhs) == 1 {
				receiver, field := backendResourceSelector(x.Lhs[0])
				if receiver == "backend" {
					if _, known := want[field]; known {
						if backendResourceIdentName(x.Rhs[0]) != "nil" {
							out = append(out, field+" is not stripped")
						}
						want[field] = true
					}
				}
			}
		case *ast.CallExpr:
			receiver, method := backendResourceSelector(x.Fun)
			if receiver == "lease" && method == "release" {
				leaseCleanup = true
			} else if receiver == "entry" && method == "cleanup" {
				physicalCleanup = true
			} else if method == "Close" || method == "Start" || method == "Stop" || method == "CleanupIdleTransports" {
				out = append(out, "physical lifecycle bypass "+method)
			}
		case *ast.SelectorExpr:
			if receiver, method := backendResourceSelector(x); receiver == "lease" && method == "release" {
				leaseCleanup = true
			}
		}
		return true
	})
	for field, found := range want {
		if !found {
			out = append(out, field+" is not stripped")
		}
	}
	if !leaseCleanup {
		out = append(out, "pooled path lacks lease.release")
	}
	if !physicalCleanup {
		out = append(out, "entry cleanupPhysical lacks entry.cleanup")
	}
	return out
}

func buildBackendCalls(node ast.Node) []*ast.CallExpr {
	var out []*ast.CallExpr
	ast.Inspect(node, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok && backendResourceIdentName(call.Fun) == "buildDiscoveredBackend" {
			out = append(out, call)
		}
		return true
	})
	return out
}

func backendResourceLastArg(call *ast.CallExpr) string {
	if len(call.Args) == 0 {
		return ""
	}
	return backendResourceIdentName(call.Args[len(call.Args)-1])
}

func isPerInstanceCondition(expr ast.Expr) bool {
	bin, ok := expr.(*ast.BinaryExpr)
	if !ok || bin.Op.String() != "==" {
		return false
	}
	isModel := func(expr ast.Expr) bool { x, f := backendResourceSelector(expr); return x == "export" && f == "Model" }
	isPerInstance := func(expr ast.Expr) bool {
		x, f := backendResourceSelector(expr)
		return x == "processhost" && f == "ProcessModelPerInstance"
	}
	return (isModel(bin.X) && isPerInstance(bin.Y)) || (isModel(bin.Y) && isPerInstance(bin.X))
}

func scanFactoryPoolEligibility(f *ast.File) []string {
	var out []string
	install, private, build := backendResourceFunc(f, "InstallDiscoveredExports"), backendResourceFunc(f, "installDiscoveredExportsWithPool"), backendResourceFunc(f, "buildDiscoveredBackend")
	if install == nil || private == nil || build == nil {
		return []string{"missing discovered factory seam"}
	}
	delegates := false
	ast.Inspect(install.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if ok && backendResourceIdentName(call.Fun) == "installDiscoveredExportsWithPool" {
			delegates = true
			if backendResourceLastArg(call) != "nil" {
				out = append(out, "public install passes a pool")
			}
		}
		return true
	})
	if !delegates {
		out = append(out, "public install lacks private pool delegation")
	}
	var modelIf *ast.IfStmt
	ast.Inspect(private.Body, func(n ast.Node) bool {
		if modelIf == nil {
			if stmt, ok := n.(*ast.IfStmt); ok && isPerInstanceCondition(stmt.Cond) {
				modelIf = stmt
			}
		}
		return true
	})
	if modelIf == nil {
		return append(out, "missing per_instance eligibility branch")
	}
	thenCalls := buildBackendCalls(modelIf.Body)
	if len(thenCalls) == 0 || backendResourceLastArg(thenCalls[len(thenCalls)-1]) == "nil" {
		out = append(out, "per_instance branch lacks pool")
	}
	elseBlock, ok := modelIf.Else.(*ast.BlockStmt)
	elseCalls := buildBackendCalls(elseBlock)
	if !ok || len(elseCalls) == 0 || backendResourceLastArg(elseCalls[len(elseCalls)-1]) != "nil" {
		out = append(out, "shared_artifact branch enters pool")
	}
	acquires := 0
	ast.Inspect(build.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if ok {
			x, method := backendResourceSelector(call.Fun)
			if x == "resourcePool" && method == "Acquire" {
				acquires++
			}
		}
		return true
	})
	if acquires != 1 {
		out = append(out, "discovered construction must have one pool Acquire seam")
	}
	return out
}

func publicReconciliationKnob(name string) bool {
	lower := strings.ToLower(name)
	return strings.Contains(lower, "backendresource") || strings.Contains(lower, "pluginresourcepool") ||
		strings.Contains(lower, "resourcepool") || strings.Contains(lower, "resourcereconcil") ||
		(strings.Contains(lower, "incarnation") && (strings.Contains(lower, "backend") || strings.Contains(lower, "resource") || strings.Contains(lower, "pool")))
}

func scanPublicReconciliationSurface(rel string, f *ast.File) []string {
	if !strings.HasPrefix(rel, "pkg/") && !strings.HasPrefix(rel, "internal/plugins/") &&
		!strings.HasPrefix(rel, "connectors/") && !strings.HasPrefix(rel, "connector-support/") {
		return nil
	}
	var out []string
	ast.Inspect(f, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.Ident:
			if publicReconciliationKnob(x.Name) || backendResourceIdentifier(x.Name) {
				out = append(out, rel+": "+x.Name)
			}
		case *ast.Field:
			if x.Tag != nil {
				tag, _ := strconv.Unquote(x.Tag.Value)
				lower := strings.ToLower(tag)
				if strings.Contains(lower, "resource_pool") || strings.Contains(lower, "reconciliation") {
					out = append(out, rel+": reconciliation struct tag")
				}
			}
		}
		return true
	})
	return out
}

func scanSessionConcurrencySurface(f *ast.File) []string {
	var out []string
	var session *ast.StructType
	for _, decl := range f.Decls {
		if gd, ok := decl.(*ast.GenDecl); ok {
			for _, spec := range gd.Specs {
				if ts, ok := spec.(*ast.TypeSpec); ok && ts.Name.Name == "Session" {
					session, _ = ts.Type.(*ast.StructType)
				}
			}
		} else if d, ok := decl.(*ast.FuncDecl); ok && d.Recv != nil && strings.Contains(strings.ToLower(d.Name.Name), "concurr") {
			out = append(out, "Session concurrency method "+d.Name.Name)
		}
	}
	if session != nil {
		for _, field := range session.Fields.List {
			for _, name := range field.Names {
				if strings.Contains(strings.ToLower(name.Name), "concurr") || publicReconciliationKnob(name.Name) {
					out = append(out, "Session field "+name.Name)
				}
			}
		}
	}
	return out
}

func scanProtoReconciliationFields(src string) []string {
	var out []string
	for line := range strings.SplitSeq(src, "\n") {
		line = strings.TrimSpace(strings.SplitN(line, "//", 2)[0])
		if !strings.Contains(line, "=") {
			continue
		}
		parts := strings.Fields(strings.TrimSpace(strings.SplitN(line, "=", 2)[0]))
		if len(parts) == 0 {
			continue
		}
		name := strings.Trim(parts[len(parts)-1], "[]")
		if publicReconciliationKnob(name) || strings.Contains(strings.ToLower(name), "resource_pool") || strings.Contains(strings.ToLower(name), "reconciliation") {
			out = append(out, "ABI field "+name)
		}
	}
	return out
}

func requireBackendResourceRejected(t *testing.T, label, source string, scan func(*ast.File) []string) {
	t.Helper()
	if len(scan(parseBackendResource(t, "fixture.go", source))) == 0 {
		t.Fatalf("%s mutation was not rejected", label)
	}
}

func TestBackendResourceReconciliation_ArchitectureFences(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	var violations []string
	err := walkBackendResourceProduction(root, func(rel string, f *ast.File) error {
		violations = append(violations, scanBackendResourcePrivateScope(rel, f)...)
		violations = append(violations, scanForbiddenRuntimebundleImport(rel, f)...)
		for _, name := range scanGenericResourceRegistryAPI(f) {
			violations = append(violations, rel+": generic API "+name)
		}
		violations = append(violations, scanPublicReconciliationSurface(rel, f)...)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	readAST := func(path string) *ast.File {
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return parseBackendResource(t, path, string(src))
	}
	poolPath := filepath.Join(root, filepath.FromSlash(backendResourceRuntimeBundle), "backend_resource_pool.go")
	poolFile := readAST(poolPath)
	violations = append(violations, scanPoolSupervisorViolations(poolFile)...)
	violations = append(violations, scanPooledLifecycleContract(poolFile)...)
	factoryPath := filepath.Join(root, filepath.FromSlash(backendResourceRuntimeBundle), "discovered_factories.go")
	violations = append(violations, scanFactoryPoolEligibility(readAST(factoryPath))...)
	sessionPath := filepath.Join(root, "pkg", "aipsdk", "backendplugin", "host", "session.go")
	violations = append(violations, scanSessionConcurrencySurface(readAST(sessionPath))...)
	protoSrc, err := os.ReadFile(filepath.Join(root, "api", "backendplugin", "v1", "backend.proto"))
	if err != nil {
		t.Fatal(err)
	}
	violations = append(violations, scanProtoReconciliationFields(string(protoSrc))...)
	if len(violations) != 0 {
		t.Fatalf("backend resource reconciliation architecture violations:\n%s", strings.Join(violations, "\n"))
	}

	// Representative RED fixtures keep the fences mutation-sensitive.
	requireBackendResourceRejected(t, "private-scope", "package core\ntype BackendResourcePool struct{}\n", func(f *ast.File) []string { return scanBackendResourcePrivateScope("internal/core/fixture.go", f) })
	requireBackendResourceRejected(t, "request-path import", "package core\nimport \"github.com/aiproxer/aiproxer/internal/infra/runtimebundle\"\n", func(f *ast.File) []string { return scanForbiddenRuntimebundleImport("internal/core/fixture.go", f) })
	requireBackendResourceRejected(t, "generic registry", "package runtimebundle\ntype ResourceRegistry struct{}\n", scanGenericResourceRegistryAPI)
	requireBackendResourceRejected(t, "pool supervision", "package runtimebundle\nimport \"net\"\nfunc f() { _, _ = net.Dial(\"tcp\", \"x\") }\n", scanPoolSupervisorViolations)
	requireBackendResourceRejected(t, "lifecycle bypass", "package runtimebundle\nfunc stripPooledBackendLifecycle(backend Backend) Backend { backend.Close = closePhysical; return backend }\nfunc await() Result { return Result{Cleanup: entry.cleanup} }\nfunc cleanupPhysical() {}\n", scanPooledLifecycleContract)
	factoryFixture := `package runtimebundle
func InstallDiscoveredExports() { installDiscoveredExportsWithPool(Export{}, pool) }
func installDiscoveredExportsWithPool(export Export, pool any) { if export.Model == processhost.ProcessModelPerInstance { buildDiscoveredBackend(pool) } else { buildDiscoveredBackend(pool) } }
func buildDiscoveredBackend(pool any) {}
`
	requireBackendResourceRejected(t, "shared-artifact/builtin pool", factoryFixture, scanFactoryPoolEligibility)
	requireBackendResourceRejected(t, "Session concurrency", "package host\ntype Session struct { ResourcePool any }\nfunc (*Session) SetConcurrency(int) {}\n", scanSessionConcurrencySurface)
}
