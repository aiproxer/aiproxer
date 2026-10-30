package runtimebundle

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"golang.org/x/tools/go/packages"
)

const (
	runtimebundlePkgPath = "github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	runtimehostPkgPath   = "github.com/aiproxer/aiproxer/internal/infra/runtimehost"
	aipruntimePkgPath    = "github.com/aiproxer/aiproxer/pkg/aipruntime"
	aipstdPkgPath        = "github.com/aiproxer/aiproxer/cmd/aipstd"
	runtimeModulePattern = "github.com/aiproxer/aiproxer/..."
	callbackEscapeRoot   = "github.com/aiproxer/aiproxer/internal/infra/runtimebundle/testdata/callbackescape"
)

type callbackBuildContext struct {
	GOOS, GOARCH string
}

// Canonical owner-callback gates analyze at least Linux amd64 and Windows amd64.
var callbackSupportedBuildContexts = []callbackBuildContext{
	{GOOS: "linux", GOARCH: "amd64"},
	{GOOS: "windows", GOARCH: "amd64"},
}

func TestRuntimebundle_OwnerCallbackEscapeFixtures(t *testing.T) {
	t.Parallel()
	fixtureRoot, err := filepath.Abs(filepath.Join("testdata", "callbackescape"))
	if err != nil {
		t.Fatalf("resolve fixture root: %v", err)
	}

	t.Run("positive_aggregate", func(t *testing.T) {
		t.Parallel()
		pkg := loadTypedPackage(t, callbackEscapeRoot+"/positive/aggregate", fixtureOverlay(t, fixtureRoot, "positive/aggregate"))
		owners := resolveProtectedOwners(t, pkg)
		hits := findOwnerCallbackEscapes(pkg, owners)
		if len(hits) == 0 {
			t.Fatal("expected forbidden owner callback escapes to be rejected")
		}
		joined := strings.Join(hits, "\n")
		for _, want := range []string{
			"HostCB",           // alias split across files
			"UseRenamedImport", // renamed import
			"UseDotImport",     // dot import
			"HostPicker",       // named callback
			"UseImportedNamed", // imported named callback
			"UseOwnerPtr",      // defined pointer owner
			"bag",              // nested storage
			"factory",          // callback-returning-owner
			"localPick",        // function-local wrapper
			"OwnerCB",          // generic named callback
			"OwnerFn",          // ~func constraint
			"type assertion recovers complete-owner callback", // type assertions (func + interface + erased)
			"func(*runtimebundle.Host))",                      // erased immediately-invoked invoke assert
			"func() *runtimebundle.Host",                      // erased immediately-invoked acquire assert
			"hostBindOp",                                      // direct owner-bearing interface method
			"litEscape",                                       // immediate function literal
			"methExpr",                                        // transferred method expression
			"method value",                                    // transferred method value
			"Forward",                                         // generic forwarding/instantiation
			"UseDeepAlias",                                    // deep/cross-package alias identity
			"anonStored",                                      // callback-valued expression storage
			"LocalStoredMarker",                               // local function identifier stored through any
			"ImportedStoredMarker",                            // imported function identifier stored through any
			"ImportedCompositeMarker",                         // imported function in composite storage
			"ImportedArgMarker",                               // imported function passed as an argument
			"ImportedReturnMarker",                            // imported function returned through any
			"LocalAssignMarker",                               // local function assigned through any
			"ImportedGenericMarker",                           // imported function passed through inferred generic forwarding
			"call result carries",                             // inferred generic call produces an owner-callback value
			"SliceOwnerCallback",                              // owner nested under slice in a callback parameter
			"StructOwnerCallback",                             // owner nested under struct in a callback parameter
			"DoublePointerOwnerCallback",                      // owner behind multiple pointers
			"ValueOwnerCallback",                              // complete owner passed by value
			"Hostish",                                         // owner-preserving generic type set
			"UseConstrainedOwnerCallback",                     // constrained owner nested in callback
			"owner-capturing function literal",                // zero-arg closure capturing protected owner
		} {
			if !strings.Contains(joined, want) {
				t.Errorf("positive aggregate missing detection for %q; hits:\n%s", want, joined)
			}
		}
	})

	t.Run("negative_aggregate", func(t *testing.T) {
		t.Parallel()
		pkg := loadTypedPackage(t, callbackEscapeRoot+"/negative/aggregate", fixtureOverlay(t, fixtureRoot, "negative/aggregate"))
		owners := resolveProtectedOwners(t, pkg)
		if hits := findOwnerCallbackEscapes(pkg, owners); len(hits) != 0 {
			t.Fatalf("unrelated focused callback/direct owner use must pass; got:\n%s", strings.Join(hits, "\n"))
		}
	})

	t.Run("candidate_assembly_overlay", func(t *testing.T) {
		t.Parallel()
		overlay := fixtureOverlay(t, fixtureRoot, "")
		overlayPath := filepath.Join(fixtureRoot, "support", "runtimebundle", "escape_candidate_aggregate.go")
		overlay[overlayPath] = []byte(`package runtimebundle

type assemblyCB = func(*candidateAssembly) int

func useAssemblyCB(f assemblyCB) int { return f(nil) }

type assemblyPtr *candidateAssembly

func useAssemblyPtr(f func(assemblyPtr) int) int { return f(nil) }

type assemblyBind interface {
	apply(*candidateAssembly) error
}

var assemblyLit = func(a *candidateAssembly) int { return 0 }

func useAssemblyDirect(a *candidateAssembly) {}

type assemblyHolder struct{ a *candidateAssembly }

type ledgerCB func(*ResourceLedger) int

type ledgerBind interface {
	bind() *ResourceLedger
}

var ledgerLit = func(l *ResourceLedger) int { return 0 }

func useLedgerDirect(l *ResourceLedger) {}

type ledgerHolder struct{ l *ResourceLedger }
`)
		pkg := loadTypedPackage(t, callbackEscapeRoot+"/support/runtimebundle", overlay)
		owners := resolveProtectedOwners(t, pkg)
		hits := findOwnerCallbackEscapes(pkg, owners)
		if len(hits) == 0 {
			t.Fatal("expected candidateAssembly callback escapes to be rejected")
		}
		joined := strings.Join(hits, "\n")
		for _, want := range []string{"assemblyCB", "useAssemblyPtr", "assemblyBind", "assemblyLit", "ledgerCB", "ledgerBind", "ledgerLit"} {
			if !strings.Contains(joined, want) {
				t.Errorf("candidateAssembly aggregate missing detection for %q; hits:\n%s", want, joined)
			}
		}
		for _, forbid := range []string{"useAssemblyDirect", "assemblyHolder", "useLedgerDirect", "ledgerHolder"} {
			if strings.Contains(joined, forbid) {
				t.Errorf("direct candidateAssembly param/field must not be flagged (%s); hits:\n%s", forbid, joined)
			}
		}
	})

	t.Run("windows_overlay_escape", func(t *testing.T) {
		t.Parallel()
		overlay := fixtureOverlay(t, fixtureRoot, "")
		overlayPath := filepath.Join(fixtureRoot, "support", "runtimebundle", "callback_escape_windows_overlay.go")
		overlay[overlayPath] = []byte(`//go:build windows

package runtimebundle

type windowsOverlayHostCB = func(*Host) int

func useWindowsOverlayHostCB(f windowsOverlayHostCB) int { return f(nil) }
`)
		var mu sync.Mutex
		var hits []string
		var errs []error
		analyzed := map[string]bool{}
		analyzed[filepath.Clean(overlayPath)] = true
		var wg sync.WaitGroup
		for _, bc := range callbackSupportedBuildContexts {
			wg.Add(1)
			go func(bc callbackBuildContext) {
				defer wg.Done()
				pkg, err := typedPackageForContextE(callbackEscapeRoot+"/support/runtimebundle", bc, overlay)
				if err != nil {
					mu.Lock()
					errs = append(errs, err)
					mu.Unlock()
					return
				}
				owners, err := protectedOwnersE(pkg)
				if err != nil {
					mu.Lock()
					errs = append(errs, err)
					mu.Unlock()
					return
				}
				contextHits := findOwnerCallbackEscapes(pkg, owners)
				mu.Lock()
				defer mu.Unlock()
				for _, f := range pkg.CompiledGoFiles {
					analyzed[filepath.Clean(f)] = true
				}
				for _, f := range pkg.GoFiles {
					analyzed[filepath.Clean(f)] = true
				}
				hits = append(hits, contextHits...)
			}(bc)
		}
		wg.Wait()
		if len(errs) > 0 {
			t.Fatalf("windows overlay context load failed: %v", errs)
		}
		hits = dedupeStrings(hits)
		joined := strings.Join(hits, "\n")
		if !strings.Contains(joined, "windowsOverlayHostCB") {
			t.Fatalf("windows overlay protected callback escape must be detected; hits:\n%s", joined)
		}
		if !analyzed[filepath.Clean(overlayPath)] {
			t.Fatal("windows overlay must count as an inventory input")
		}
	})
}

type protectedOwners struct {
	host        ownerID
	assembly    ownerID
	ledger      ownerID
	coordinator ownerID
}

type ownerID struct {
	pkgPath string
	name    string
}

func loadTypedPackage(t *testing.T, pattern string, overlay map[string][]byte) *packages.Package {
	t.Helper()
	pkgs := loadTypedPackages(t, pattern, overlay)
	if len(pkgs) != 1 {
		t.Fatalf("packages.Load(%q): want 1 package, got %d", pattern, len(pkgs))
	}
	return pkgs[0]
}

func loadTypedPackages(t *testing.T, pattern string, overlay map[string][]byte) []*packages.Package {
	t.Helper()
	absOverlay := map[string][]byte{}
	for name, src := range overlay {
		if filepath.IsAbs(name) {
			absOverlay[name] = src
			continue
		}
		pkgDir := packageDirForOverlay(t, pattern)
		absOverlay[filepath.Join(pkgDir, name)] = src
	}
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles |
			packages.NeedImports | packages.NeedTypes | packages.NeedTypesSizes |
			packages.NeedSyntax | packages.NeedTypesInfo | packages.NeedModule,
		Tests:   false,
		Overlay: absOverlay,
	}
	pkgs, err := packages.Load(cfg, pattern)
	if err != nil {
		t.Fatalf("packages.Load(%q): %v", pattern, err)
	}
	if packages.PrintErrors(pkgs) > 0 {
		t.Fatalf("packages.Load(%q): type-check/load errors (fail closed)", pattern)
	}
	if len(pkgs) == 0 {
		t.Fatalf("packages.Load(%q): returned no packages", pattern)
	}
	for _, pkg := range pkgs {
		if pkg.Types == nil || pkg.TypesInfo == nil {
			t.Fatalf("packages.Load(%q): package %s missing types info", pattern, pkg.PkgPath)
		}
	}
	sort.Slice(pkgs, func(i, j int) bool { return pkgs[i].PkgPath < pkgs[j].PkgPath })
	return pkgs
}

func packageByPath(t *testing.T, pkgs []*packages.Package, path string) *packages.Package {
	t.Helper()
	for _, pkg := range pkgs {
		if pkg.PkgPath == path {
			return pkg
		}
	}
	t.Fatalf("loaded package set missing %s", path)
	return nil
}

func loadOwnerReachablePackagesAcrossContexts(t *testing.T, overlay map[string][]byte) ([]*packages.Package, map[string]bool) {
	t.Helper()
	var mu sync.Mutex
	var all []*packages.Package
	var errs []error
	analyzed := map[string]bool{}
	var wg sync.WaitGroup
	for _, bc := range callbackSupportedBuildContexts {
		wg.Add(1)
		go func(bc callbackBuildContext) {
			defer wg.Done()
			typed, analyzedFiles, err := loadOwnerReachablePackagesForContext(bc, overlay)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, err)
				return
			}
			for path := range analyzedFiles {
				analyzed[path] = true
			}
			all = append(all, typed...)
		}(bc)
	}
	wg.Wait()
	if len(errs) > 0 {
		t.Fatalf("owner-reachable load failed: %v", errs)
	}
	for path := range overlay {
		analyzed[filepath.Clean(path)] = true
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].PkgPath == all[j].PkgPath {
			return all[i].Fset.Position(all[i].Syntax[0].Pos()).Filename < all[j].Fset.Position(all[j].Syntax[0].Pos()).Filename
		}
		return all[i].PkgPath < all[j].PkgPath
	})
	return all, analyzed
}

type ownerReachableCacheEntry struct {
	done     chan struct{}
	typed    []*packages.Package
	analyzed map[string]bool
	err      error
}

var (
	ownerReachableCacheMu sync.Mutex
	ownerReachableCache   = make(map[callbackBuildContext]*ownerReachableCacheEntry)
)

func loadOwnerReachablePackagesForContext(bc callbackBuildContext, overlay map[string][]byte) ([]*packages.Package, map[string]bool, error) {
	if len(overlay) == 0 {
		ownerReachableCacheMu.Lock()
		entry, ok := ownerReachableCache[bc]
		if !ok {
			entry = &ownerReachableCacheEntry{done: make(chan struct{})}
			ownerReachableCache[bc] = entry
			ownerReachableCacheMu.Unlock()

			typed, analyzed, err := loadOwnerReachablePackagesForContextUncached(bc, nil)
			entry.typed = typed
			entry.analyzed = analyzed
			entry.err = err
			close(entry.done)
		} else {
			ownerReachableCacheMu.Unlock()
			<-entry.done
		}
		if entry.err != nil {
			return nil, nil, entry.err
		}
		return entry.typed, maps.Clone(entry.analyzed), nil
	}
	return loadOwnerReachablePackagesForContextUncached(bc, overlay)
}

func loadOwnerReachablePackagesForContextUncached(bc callbackBuildContext, overlay map[string][]byte) ([]*packages.Package, map[string]bool, error) {
	analyzed := map[string]bool{}
	// This graph pass needs package metadata and imports only; NeedCompiledGoFiles
	// enters the build-action pipeline, which the typed load below needs separately.
	graph, err := packages.Load(&packages.Config{
		Mode: packages.NeedName | packages.NeedFiles |
			packages.NeedImports | packages.NeedDeps | packages.NeedModule,
		Tests: false, Env: callbackPackagesEnv(bc), Overlay: overlay,
	}, runtimeModulePattern)
	if err != nil || packages.PrintErrors(graph) > 0 || len(graph) == 0 {
		return nil, nil, fmt.Errorf("load module import graph (%s/%s): err=%v packages=%d (fail closed)", bc.GOOS, bc.GOARCH, err, len(graph))
	}
	memo, visiting := map[string]bool{}, map[string]bool{}
	var reachesOwner func(*packages.Package) bool
	reachesOwner = func(pkg *packages.Package) bool {
		if pkg == nil {
			return false
		}
		if pkg.PkgPath == runtimebundlePkgPath || pkg.PkgPath == runtimehostPkgPath {
			return true
		}
		if v, ok := memo[pkg.PkgPath]; ok {
			return v
		}
		if visiting[pkg.PkgPath] {
			return false
		}
		visiting[pkg.PkgPath] = true
		for _, imported := range pkg.Imports {
			if reachesOwner(imported) {
				visiting[pkg.PkgPath] = false
				memo[pkg.PkgPath] = true
				return true
			}
		}
		visiting[pkg.PkgPath] = false
		memo[pkg.PkgPath] = false
		return false
	}
	var paths []string
	for _, pkg := range graph {
		if reachesOwner(pkg) {
			paths = append(paths, pkg.PkgPath)
		}
	}
	if len(paths) == 0 {
		return nil, nil, fmt.Errorf("module import graph (%s/%s) contains no owner-reachable packages", bc.GOOS, bc.GOARCH)
	}
	sort.Strings(paths)
	typed, err := packages.Load(&packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles |
			packages.NeedImports | packages.NeedTypes | packages.NeedTypesSizes |
			packages.NeedSyntax | packages.NeedTypesInfo | packages.NeedModule,
		Tests: false, Env: callbackPackagesEnv(bc), Overlay: overlay,
	}, paths...)
	if err != nil || packages.PrintErrors(typed) > 0 || len(typed) != len(paths) {
		return nil, nil, fmt.Errorf("type-load owner-reachable packages (%s/%s): err=%v want=%d got=%d (fail closed)", bc.GOOS, bc.GOARCH, err, len(paths), len(typed))
	}
	for _, pkg := range typed {
		if pkg.Types == nil || pkg.TypesInfo == nil {
			return nil, nil, fmt.Errorf("owner-reachable package %s missing types info", pkg.PkgPath)
		}
		for _, path := range append(pkg.GoFiles, pkg.CompiledGoFiles...) {
			analyzed[filepath.Clean(path)] = true
		}
	}
	return typed, analyzed, nil
}

func typedPackageForContextE(pattern string, bc callbackBuildContext, overlay map[string][]byte) (*packages.Package, error) {
	pkgs, err := packages.Load(&packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles | packages.NeedImports |
			packages.NeedTypes | packages.NeedTypesSizes | packages.NeedSyntax | packages.NeedTypesInfo | packages.NeedModule,
		Tests: false, Env: callbackPackagesEnv(bc), Overlay: overlay,
	}, pattern)
	if err != nil || packages.PrintErrors(pkgs) > 0 || len(pkgs) != 1 || pkgs[0].Types == nil || pkgs[0].TypesInfo == nil {
		return nil, fmt.Errorf("packages.Load(%q, %s/%s): err=%v packages=%d (fail closed)", pattern, bc.GOOS, bc.GOARCH, err, len(pkgs))
	}
	return pkgs[0], nil
}

func callbackPackagesEnv(bc callbackBuildContext) []string {
	out := make([]string, 0, len(os.Environ())+3)
	for _, kv := range os.Environ() {
		key, _, ok := strings.Cut(kv, "=")
		if ok && (key == "GOOS" || key == "GOARCH" || key == "CGO_ENABLED") {
			continue
		}
		out = append(out, kv)
	}
	return append(out, "GOOS="+bc.GOOS, "GOARCH="+bc.GOARCH, "CGO_ENABLED=0")
}

func assertOwnerReachableProductionInventory(t *testing.T, pkgs []*packages.Package, analyzed map[string]bool) {
	t.Helper()
	dirs := map[string]bool{}
	for _, pkg := range pkgs {
		for _, path := range append(pkg.GoFiles, pkg.CompiledGoFiles...) {
			if path != "" {
				dirs[filepath.Dir(path)] = true
			}
		}
	}
	var missing []string
	for dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("read package dir %s: %v", dir, err)
		}
		for _, ent := range entries {
			name := ent.Name()
			if ent.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			path := filepath.Clean(filepath.Join(dir, name))
			if !analyzed[path] {
				missing = append(missing, path)
			}
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		t.Fatalf("production .go files not covered by supported build contexts:\n%s", strings.Join(missing, "\n"))
	}
}

func dedupeStrings(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

func fixtureOverlay(t *testing.T, fixtureRoot, rel string) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	targetCount := 0
	var roots []string
	if rel != "" {
		roots = append(roots, filepath.Join(fixtureRoot, filepath.FromSlash(rel)))
	}
	roots = append(roots, filepath.Join(fixtureRoot, "support"))
	for i, root := range roots {
		err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() || !strings.HasSuffix(path, ".go.fixture") {
				return nil
			}
			src, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			out[strings.TrimSuffix(path, ".fixture")] = src
			if rel != "" && i == 0 {
				targetCount++
			}
			return nil
		})
		if err != nil {
			t.Fatalf("read fixture tree %s: %v", root, err)
		}
	}
	if rel != "" && targetCount == 0 {
		t.Fatalf("no fixture sources for %s", rel)
	}
	return out
}

func packageDirForOverlay(t *testing.T, pattern string) string {
	t.Helper()
	if pattern != runtimebundlePkgPath && pattern != "." {
		t.Fatalf("relative overlay only supported for %s, got %s", runtimebundlePkgPath, pattern)
	}
	runtimebundleDirOnce.Do(func() {
		pkgs, err := packages.Load(&packages.Config{Mode: packages.NeedFiles | packages.NeedName, Tests: false}, runtimebundlePkgPath)
		if err != nil || len(pkgs) != 1 || len(pkgs[0].GoFiles) == 0 {
			errRuntimebundleDir = fmt.Errorf("resolve runtimebundle dir: err=%v pkgs=%v", err, pkgs)
			return
		}
		runtimebundleDir = filepath.Dir(pkgs[0].GoFiles[0])
	})
	if errRuntimebundleDir != nil {
		t.Fatal(errRuntimebundleDir)
	}
	return runtimebundleDir
}

var (
	runtimebundleDirOnce sync.Once
	runtimebundleDir     string
	errRuntimebundleDir  error
)

func resolveProtectedOwners(t *testing.T, pkg *packages.Package) protectedOwners {
	t.Helper()
	owners, err := protectedOwnersE(pkg)
	if err != nil {
		t.Fatal(err)
	}
	return owners
}

func protectedOwnersE(pkg *packages.Package) (protectedOwners, error) {
	var hostPkg *types.Package
	var coordPkg *types.Package

	if pkg.PkgPath == runtimebundlePkgPath || (pkg.Imports != nil && pkg.Imports[runtimebundlePkgPath] != nil) {
		hp, err := typesPackageByPathE(pkg, runtimebundlePkgPath)
		if err != nil {
			return protectedOwners{}, err
		}
		hostPkg = hp
	}
	if pkg.PkgPath == runtimehostPkgPath || (pkg.Imports != nil && pkg.Imports[runtimehostPkgPath] != nil) {
		cp, err := typesPackageByPathE(pkg, runtimehostPkgPath)
		if err != nil {
			return protectedOwners{}, err
		}
		coordPkg = cp
	}

	if hostPkg == nil || coordPkg == nil {
		seen := map[*types.Package]bool{}
		var findTypes func(p *types.Package)
		findTypes = func(p *types.Package) {
			if p == nil || seen[p] {
				return
			}
			seen[p] = true
			if hostPkg == nil && (p.Name() == "runtimebundle" || strings.HasSuffix(p.Path(), "/runtimebundle")) && p.Scope().Lookup("Host") != nil && p.Scope().Lookup("candidateAssembly") != nil && p.Scope().Lookup("ResourceLedger") != nil {
				hostPkg = p
			}
			if coordPkg == nil && (p.Name() == "runtimehost" || strings.HasSuffix(p.Path(), "/runtimehost")) && p.Scope().Lookup("Coordinator") != nil {
				coordPkg = p
			}
			for _, imported := range p.Imports() {
				findTypes(imported)
			}
		}
		if pkg.Types != nil {
			findTypes(pkg.Types)
		}
		for _, imp := range pkg.Imports {
			if imp != nil && imp.Types != nil {
				findTypes(imp.Types)
			}
		}
	}

	if hostPkg == nil {
		return protectedOwners{}, fmt.Errorf("package %s or its imports missing Host owner types", pkg.PkgPath)
	}

	var hostID, assemblyID, ledgerID, coordID ownerID
	hostID = ownerID{pkgPath: hostPkg.Path(), name: "Host"}
	assemblyID = ownerID{pkgPath: hostPkg.Path(), name: "candidateAssembly"}
	ledgerID = ownerID{pkgPath: hostPkg.Path(), name: "ResourceLedger"}
	if coordPkg != nil {
		coordID = ownerID{pkgPath: coordPkg.Path(), name: "Coordinator"}
	}

	return protectedOwners{
		host:        hostID,
		assembly:    assemblyID,
		ledger:      ledgerID,
		coordinator: coordID,
	}, nil
}

func typesPackageByPathE(from *packages.Package, path string) (*types.Package, error) {
	if from.PkgPath == path && from.Types != nil {
		return from.Types, nil
	}
	if imp := from.Imports[path]; imp != nil && imp.Types != nil {
		return imp.Types, nil
	}
	for _, imp := range from.Imports {
		if imp == nil {
			continue
		}
		if imp.PkgPath == path && imp.Types != nil {
			return imp.Types, nil
		}
		if nested := imp.Imports[path]; nested != nil && nested.Types != nil {
			return nested.Types, nil
		}
	}
	loaded, err := packages.Load(&packages.Config{
		Mode:  packages.NeedName | packages.NeedTypes | packages.NeedImports,
		Tests: false,
	}, path)
	if err != nil || packages.PrintErrors(loaded) > 0 || len(loaded) != 1 || loaded[0].Types == nil {
		return nil, fmt.Errorf("resolve package %s: err=%v", path, err)
	}
	return loaded[0].Types, nil
}

// ownerPredicateCache memoizes the exact results of the three type predicates
// below when invoked with a fresh cycle guard (nil seen map). A fresh-seen
// traversal is a pure function of (owners, type): types.Type graphs are
// immutable after go/types construction, so caching preserves semantics while
// collapsing the per-expression re-traversal of large shared type graphs
// (previously the dominant cost of this gate at ~15s CPU per package load).
var ownerPredicateCache = struct {
	sync.Mutex
	results map[ownerPredicateCacheKey]bool
}{results: map[ownerPredicateCacheKey]bool{}}

type ownerPredicateCacheKey struct {
	kind   uint8 // 1=callback, 2=contains, 3=signature
	owners protectedOwners
	typ    types.Type
}

func cachedOwnerPredicate(kind uint8, t types.Type, owners protectedOwners, compute func() bool) bool {
	key := ownerPredicateCacheKey{kind: kind, owners: owners, typ: t}
	ownerPredicateCache.Lock()
	v, ok := ownerPredicateCache.results[key]
	ownerPredicateCache.Unlock()
	if ok {
		return v
	}
	v = compute()
	ownerPredicateCache.Lock()
	ownerPredicateCache.results[key] = v
	ownerPredicateCache.Unlock()
	return v
}

func memoCallbackTypeMentionsOwner(t types.Type, owners protectedOwners) bool {
	return cachedOwnerPredicate(1, t, owners, func() bool { return callbackTypeMentionsOwner(t, owners, nil) })
}

func memoTypeContainsProtectedOwner(t types.Type, owners protectedOwners) bool {
	return cachedOwnerPredicate(2, t, owners, func() bool { return typeContainsProtectedOwner(t, owners, nil) })
}

func memoSignatureMentionsOwner(t types.Type, owners protectedOwners) bool {
	return cachedOwnerPredicate(3, t, owners, func() bool { return signatureMentionsOwner(t, owners, nil) })
}

func findOwnerCallbackEscapes(pkg *packages.Package, owners protectedOwners) []string {
	var hits []string
	seen := map[string]bool{}
	report := func(pos token.Pos, msg string) {
		p := pkg.Fset.Position(pos)
		key := fmt.Sprintf("%s:%d:%s", filepath.Base(p.Filename), p.Line, msg)
		if seen[key] {
			return
		}
		seen[key] = true
		hits = append(hits, key)
	}

	callFuns := map[ast.Expr]bool{}
	selectorSels := map[*ast.Ident]bool{}
	parents := map[ast.Node]ast.Node{}
	var markDirectCallFun func(ast.Expr)
	markDirectCallFun = func(expr ast.Expr) {
		if expr == nil || callFuns[expr] {
			return
		}
		callFuns[expr] = true
		switch expr := expr.(type) {
		case *ast.ParenExpr:
			markDirectCallFun(expr.X)
		case *ast.IndexExpr:
			markDirectCallFun(expr.X)
		case *ast.IndexListExpr:
			markDirectCallFun(expr.X)
		}
	}
	for _, file := range pkg.Syntax {
		var stack []ast.Node
		ast.Inspect(file, func(n ast.Node) bool {
			if n == nil {
				stack = stack[:len(stack)-1]
				return false
			}
			if len(stack) > 0 {
				parents[n] = stack[len(stack)-1]
			}
			stack = append(stack, n)
			return true
		})
		ast.Inspect(file, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.CallExpr:
				markDirectCallFun(n.Fun)
			case *ast.SelectorExpr:
				selectorSels[n.Sel] = true
			}
			return true
		})
	}

	for _, obj := range pkg.TypesInfo.Defs {
		if obj == nil || obj.Pkg() != pkg.Types {
			continue
		}
		switch obj := obj.(type) {
		case *types.TypeName:
			if memoCallbackTypeMentionsOwner(obj.Type(), owners) || ownerConstraintMentionsOwner(obj.Type(), owners) {
				report(obj.Pos(), fmt.Sprintf("named type %s is a complete-owner callback escape", obj.Name()))
			}
			if named, ok := obj.Type().(*types.Named); ok {
				if tparams := named.TypeParams(); tparams != nil {
					for tp := range tparams.TypeParams() {
						if memoCallbackTypeMentionsOwner(tp.Constraint(), owners) {
							report(tp.Obj().Pos(), fmt.Sprintf("type parameter %s constraint is a complete-owner callback escape", tp.Obj().Name()))
						}
					}
				}
			}
		case *types.Var:
			if memoCallbackTypeMentionsOwner(obj.Type(), owners) {
				report(obj.Pos(), fmt.Sprintf("variable/field/parameter %s has complete-owner callback type", obj.Name()))
			}
		case *types.Func:
			sig, _ := obj.Type().(*types.Signature)
			if sig == nil {
				continue
			}
			if tparams := sig.TypeParams(); tparams != nil {
				for tp := range tparams.TypeParams() {
					if memoCallbackTypeMentionsOwner(tp.Constraint(), owners) {
						report(tp.Obj().Pos(), fmt.Sprintf("func %s type parameter %s constraint is a complete-owner callback escape", obj.Name(), tp.Obj().Name()))
					}
				}
			}
			scanTupleNested := func(tup *types.Tuple, kind string) {
				if tup == nil {
					return
				}
				for v := range tup.Variables() {
					if memoCallbackTypeMentionsOwner(v.Type(), owners) {
						report(v.Pos(), fmt.Sprintf("func %s %s has complete-owner callback type", obj.Name(), kind))
					}
				}
			}
			scanTupleNested(sig.Params(), "parameter")
			scanTupleNested(sig.Results(), "result")
		}
	}

	for _, file := range pkg.Syntax {
		ast.Inspect(file, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.Ident:
				if selectorSels[n] || callFuns[n] {
					return true
				}
				if expressionCallbackMentionsOwner(pkg, n, owners) {
					report(n.Pos(), fmt.Sprintf("transferred owner-callback identifier %s", n.Name))
				}
			case *ast.FuncLit:
				if callFuns[n] {
					return true // immediately invoked literal remains legal
				}
				if expressionCallbackMentionsOwner(pkg, n, owners) {
					report(n.Pos(), "immediate function literal is a complete-owner callback escape")
				} else if funcLitCapturesProtectedOwner(pkg, n, owners) && funcLitEscapes(pkg, n, parents, callFuns) {
					report(n.Pos(), "owner-capturing function literal is a complete-owner callback escape")
				}
			case *ast.CallExpr:
				if !callFuns[n] && expressionCallbackMentionsOwner(pkg, n, owners) {
					report(n.Pos(), "call result carries complete-owner callback authority")
				}
			case *ast.TypeAssertExpr:
				// Dynamic callback recovery is an escape even when the asserted
				// value is immediately invoked. Direct-callee exemptions apply
				// only to statically resolved callees, never to TypeAssertExpr.
				if n.Type == nil {
					return true
				}
				tv, ok := pkg.TypesInfo.Types[n.Type]
				if !ok || tv.Type == nil {
					return true
				}
				if memoSignatureMentionsOwner(tv.Type, owners) || memoCallbackTypeMentionsOwner(tv.Type, owners) {
					report(n.Type.Pos(), fmt.Sprintf(
						"type assertion recovers complete-owner callback (%s)",
						types.TypeString(tv.Type, func(other *types.Package) string {
							if other == nil || other == pkg.Types {
								return ""
							}
							return other.Name()
						}),
					))
				}
			case *ast.IndexExpr:
				scanGenericInstantiation(pkg, n, callFuns[n], owners, report)
			case *ast.IndexListExpr:
				scanGenericInstantiation(pkg, n, callFuns[n], owners, report)
			case *ast.SelectorExpr:
				if callFuns[n] {
					return true
				}
				if expressionCallbackMentionsOwner(pkg, n, owners) {
					report(n.Sel.Pos(), fmt.Sprintf("transferred owner-callback identifier %s", n.Sel.Name))
					return true
				}
				if sel, ok := pkg.TypesInfo.Selections[n]; ok {
					switch sel.Kind() {
					case types.MethodVal, types.MethodExpr:
						if recvIsProtectedOwner(sel.Recv(), owners) {
							report(n.Sel.Pos(), fmt.Sprintf("transferred method value/expression %s of protected owner", n.Sel.Name))
						}
					}
				}
			}
			return true
		})
	}
	return hits
}

func findOwnerCallbackEscapesInPackages(pkgs []*packages.Package, owners protectedOwners) []string {
	var hits []string
	for _, pkg := range pkgs {
		for _, hit := range findOwnerCallbackEscapes(pkg, owners) {
			hits = append(hits, pkg.PkgPath+":"+hit)
		}
	}
	return dedupeStrings(hits)
}

func expressionCallbackMentionsOwner(pkg *packages.Package, expr ast.Expr, owners protectedOwners) bool {
	var typ types.Type
	if tv, ok := pkg.TypesInfo.Types[expr]; ok {
		typ = tv.Type
	}
	if typ == nil {
		switch expr := expr.(type) {
		case *ast.Ident:
			if obj := pkg.TypesInfo.Uses[expr]; obj != nil {
				typ = obj.Type()
			}
		case *ast.SelectorExpr:
			if obj := pkg.TypesInfo.Uses[expr.Sel]; obj != nil {
				typ = obj.Type()
			}
		}
	}
	return typ != nil && (memoSignatureMentionsOwner(typ, owners) || memoCallbackTypeMentionsOwner(typ, owners))
}

// funcLitCapturesProtectedOwner reports whether a non-immediately-invoked
// function literal closes over a value whose go/types identity reaches a
// protected owner, even when the literal's own signature mentions none.
func funcLitCapturesProtectedOwner(pkg *packages.Package, lit *ast.FuncLit, owners protectedOwners) bool {
	if lit == nil || lit.Body == nil || pkg.TypesInfo == nil {
		return false
	}
	captured := false
	ast.Inspect(lit.Body, func(n ast.Node) bool {
		if captured {
			return false
		}
		switch n := n.(type) {
		case *ast.FuncLit:
			// Nested literals are examined by the file-level walk on their own node.
			return false
		case *ast.Ident:
			obj := pkg.TypesInfo.Uses[n]
			if obj == nil {
				return true
			}
			v, ok := obj.(*types.Var)
			if !ok || v == nil {
				return true
			}
			// Definitions introduced inside this literal (params/locals) are not captures.
			if lit.Pos() <= v.Pos() && v.Pos() < lit.End() {
				return true
			}
			if memoTypeContainsProtectedOwner(v.Type(), owners) {
				captured = true
				return false
			}
		}
		return true
	})
	return captured
}

// funcLitEscapes distinguishes stored/transferred closures from local helpers
// whose binding is used only as a statically direct callee.
func funcLitEscapes(pkg *packages.Package, lit *ast.FuncLit, parents map[ast.Node]ast.Node, callFuns map[ast.Expr]bool) bool {
	parent := parents[lit]
	if _, ok := parent.(*ast.CallExpr); ok {
		return false
	}
	var binding types.Object
	switch p := parent.(type) {
	case *ast.AssignStmt:
		for i, rhs := range p.Rhs {
			if rhs == lit && i < len(p.Lhs) {
				if id, ok := p.Lhs[i].(*ast.Ident); ok {
					binding = pkg.TypesInfo.Defs[id]
					if binding == nil {
						binding = pkg.TypesInfo.Uses[id]
					}
				}
			}
		}
	case *ast.ValueSpec:
		for i, value := range p.Values {
			if value == lit && i < len(p.Names) {
				binding = pkg.TypesInfo.Defs[p.Names[i]]
			}
		}
	default:
		return true
	}
	if binding == nil || binding.Parent() == pkg.Types.Scope() {
		return true
	}
	for id, used := range pkg.TypesInfo.Uses {
		if used == binding && !callFuns[id] {
			return true
		}
	}
	return false
}

func scanGenericInstantiation(pkg *packages.Package, n ast.Expr, isCallFun bool, owners protectedOwners, report func(token.Pos, string)) {
	if isCallFun {
		return
	}
	tv, ok := pkg.TypesInfo.Types[n]
	if !ok || tv.Type == nil {
		return
	}
	if memoCallbackTypeMentionsOwner(tv.Type, owners) || memoSignatureMentionsOwner(tv.Type, owners) {
		report(n.Pos(), "generic instantiation carries complete-owner callback type")
	}
}

func recvIsProtectedOwner(recv types.Type, owners protectedOwners) bool {
	recv = types.Unalias(recv)
	if ptr, ok := recv.(*types.Pointer); ok {
		recv = types.Unalias(ptr.Elem())
	}
	named, ok := recv.(*types.Named)
	if !ok {
		return false
	}
	return isProtectedOwner(named.Obj(), owners)
}

func ownerIDOf(tn *types.TypeName) ownerID {
	if tn == nil || tn.Pkg() == nil {
		return ownerID{}
	}
	return ownerID{pkgPath: tn.Pkg().Path(), name: tn.Name()}
}

func isProtectedOwner(tn *types.TypeName, owners protectedOwners) bool {
	id := ownerIDOf(tn)
	if id.pkgPath == "" || id.name == "" {
		return false
	}
	return (owners.host.pkgPath != "" && id == owners.host) ||
		(owners.assembly.pkgPath != "" && id == owners.assembly) ||
		(owners.ledger.pkgPath != "" && id == owners.ledger) ||
		(owners.coordinator.pkgPath != "" && id == owners.coordinator)
}

func signatureMentionsOwner(t types.Type, owners protectedOwners, seen map[types.Type]bool) bool {
	t = types.Unalias(t)
	for {
		switch u := t.(type) {
		case *types.Signature:
			if seen == nil {
				seen = map[types.Type]bool{}
			}
			if seen[u] {
				return false
			}
			seen[u] = true
			return tupleContainsProtectedOwner(u.Params(), owners, seen) || tupleContainsProtectedOwner(u.Results(), owners, seen)
		case *types.Named:
			t = types.Unalias(u.Underlying())
		default:
			return false
		}
	}
}

func tupleContainsProtectedOwner(tup *types.Tuple, owners protectedOwners, seen map[types.Type]bool) bool {
	if tup == nil {
		return false
	}
	for v := range tup.Variables() {
		if typeContainsProtectedOwner(v.Type(), owners, seen) {
			return true
		}
	}
	return false
}

// typeContainsProtectedOwner follows every shape that can carry an owner once
// the surrounding value is known to be callable. It is deliberately separate
// from callbackTypeMentionsOwner so ordinary direct owner params/fields remain
// legal while func([]*Host), func(**Host), and func(T constrained to *Host) do not.
func typeContainsProtectedOwner(t types.Type, owners protectedOwners, seen map[types.Type]bool) bool {
	if t == nil {
		return false
	}
	t = types.Unalias(t)
	if seen == nil {
		seen = map[types.Type]bool{}
	}
	if seen[t] {
		return false
	}
	seen[t] = true

	switch t := t.(type) {
	case *types.Named:
		if isProtectedOwner(t.Obj(), owners) {
			return true
		}
		if targs := t.TypeArgs(); targs != nil {
			for arg := range targs.Types() {
				if typeContainsProtectedOwner(arg, owners, seen) {
					return true
				}
			}
		}
		return typeContainsProtectedOwner(t.Underlying(), owners, seen)
	case *types.Pointer:
		return typeContainsProtectedOwner(t.Elem(), owners, seen)
	case *types.Slice:
		return typeContainsProtectedOwner(t.Elem(), owners, seen)
	case *types.Array:
		return typeContainsProtectedOwner(t.Elem(), owners, seen)
	case *types.Map:
		return typeContainsProtectedOwner(t.Key(), owners, seen) || typeContainsProtectedOwner(t.Elem(), owners, seen)
	case *types.Chan:
		return typeContainsProtectedOwner(t.Elem(), owners, seen)
	case *types.Struct:
		for field := range t.Fields() {
			if typeContainsProtectedOwner(field.Type(), owners, seen) {
				return true
			}
		}
	case *types.Signature:
		return tupleContainsProtectedOwner(t.Params(), owners, seen) || tupleContainsProtectedOwner(t.Results(), owners, seen)
	case *types.Interface:
		for method := range t.Methods() {
			if signatureMentionsOwner(method.Type(), owners, seen) {
				return true
			}
		}
		for emb := range t.EmbeddedTypes() {
			if typeContainsProtectedOwner(emb, owners, seen) {
				return true
			}
		}
	case *types.Union:
		for term := range t.Terms() {
			if typeContainsProtectedOwner(term.Type(), owners, seen) {
				return true
			}
		}
	case *types.TypeParam:
		return typeContainsProtectedOwner(t.Constraint(), owners, seen)
	}
	return false
}

func ownerConstraintMentionsOwner(t types.Type, owners protectedOwners) bool {
	t = types.Unalias(t)
	if named, ok := t.(*types.Named); ok {
		t = types.Unalias(named.Underlying())
	}
	iface, ok := t.(*types.Interface)
	if !ok {
		return false
	}
	for emb := range iface.EmbeddedTypes() {
		if memoTypeContainsProtectedOwner(emb, owners) {
			return true
		}
	}
	return false
}

func callbackTypeMentionsOwner(t types.Type, owners protectedOwners, seen map[types.Type]bool) bool {
	if t == nil {
		return false
	}
	t = types.Unalias(t)
	if seen == nil {
		seen = map[types.Type]bool{}
	}
	if seen[t] {
		return false
	}
	seen[t] = true

	switch t := t.(type) {
	case *types.Signature:
		return memoSignatureMentionsOwner(t, owners)
	case *types.Named:
		if isProtectedOwner(t.Obj(), owners) {
			return false
		}
		if targs := t.TypeArgs(); targs != nil {
			for arg := range targs.Types() {
				if callbackTypeMentionsOwner(arg, owners, seen) {
					return true
				}
			}
		}
		return callbackTypeMentionsOwner(t.Underlying(), owners, seen)
	case *types.Struct:
		for field := range t.Fields() {
			if callbackTypeMentionsOwner(field.Type(), owners, seen) {
				return true
			}
		}
	case *types.Slice:
		return callbackTypeMentionsOwner(t.Elem(), owners, seen)
	case *types.Array:
		return callbackTypeMentionsOwner(t.Elem(), owners, seen)
	case *types.Pointer:
		return callbackTypeMentionsOwner(t.Elem(), owners, seen)
	case *types.Map:
		return callbackTypeMentionsOwner(t.Key(), owners, seen) || callbackTypeMentionsOwner(t.Elem(), owners, seen)
	case *types.Chan:
		return callbackTypeMentionsOwner(t.Elem(), owners, seen)
	case *types.Interface:
		for method := range t.Methods() {
			if memoSignatureMentionsOwner(method.Type(), owners) {
				return true
			}
		}
		for emb := range t.EmbeddedTypes() {
			if callbackTypeMentionsOwner(emb, owners, seen) {
				return true
			}
		}
	case *types.Union:
		for term := range t.Terms() {
			if callbackTypeMentionsOwner(term.Type(), owners, seen) {
				return true
			}
		}
	case *types.TypeParam:
		return callbackTypeMentionsOwner(t.Constraint(), owners, seen)
	}
	return false
}
