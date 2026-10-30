package archtest

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"golang.org/x/tools/go/packages"
)

const runtimebundlePackagePath = "github.com/aiproxer/aiproxer/internal/infra/runtimebundle"

type archBuildContext struct {
	GOOS, GOARCH string
	CGOEnabled   bool
	Trimpath     bool
}

// Canonical gates analyze at least Linux amd64 and Windows amd64.
var archSupportedBuildContexts = []archBuildContext{
	{GOOS: "linux", GOARCH: "amd64"},
	{GOOS: "windows", GOARCH: "amd64"},
}

func bindHostCallersAcrossContexts(t *testing.T, overlay map[string][]byte) (callers []string, analyzed map[string]bool) {
	t.Helper()
	return bindHostCallersInContexts(t, archSupportedBuildContexts, overlay)
}

func bindHostCallersInContexts(t *testing.T, contexts []archBuildContext, overlay map[string][]byte) (callers []string, analyzed map[string]bool) {
	t.Helper()
	seenCaller := map[string]bool{}
	analyzed = map[string]bool{}
	for _, bc := range contexts {
		pkg := loadRuntimebundleForContext(t, bc, overlay)
		for _, f := range pkg.CompiledGoFiles {
			analyzed[filepath.Clean(f)] = true
		}
		for _, f := range pkg.GoFiles {
			analyzed[filepath.Clean(f)] = true
		}
		for path := range overlay {
			// Overlays participate in inventory wherever they apply.
			analyzed[filepath.Clean(path)] = true
		}
		bindHost, ok := pkg.Types.Scope().Lookup("bindHost").(*types.Func)
		if !ok || bindHost == nil {
			t.Fatalf("%s/%s: runtimebundle.bindHost function not found", bc.GOOS, bc.GOARCH)
		}
		for _, file := range pkg.Syntax {
			regions := functionRegions(pkg.Fset, file)
			ast.Inspect(file, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok || calledObject(pkg.TypesInfo, call.Fun) != bindHost {
					return true
				}
				caller := "<package scope>"
				bestSpan := token.Pos(^uint(0) >> 1)
				for _, region := range regions {
					if region.start <= call.Pos() && call.End() <= region.end && region.end-region.start < bestSpan {
						caller = region.name
						bestSpan = region.end - region.start
					}
				}
				filename := filepath.Base(pkg.Fset.Position(call.Pos()).Filename)
				key := filename + ":" + caller
				if !seenCaller[key] {
					seenCaller[key] = true
					callers = append(callers, key)
				}
				return true
			})
		}
	}
	return callers, analyzed
}

type runtimebundleCacheEntry struct {
	done chan struct{}
	pkg  *packages.Package
	err  error
}

var (
	runtimebundleCacheMu   sync.Mutex
	runtimebundleCache     = make(map[archBuildContext]*runtimebundleCacheEntry)
	runtimebundleDirOnce   sync.Once
	runtimebundleDirCached string
)

func loadRuntimebundleForContext(t *testing.T, bc archBuildContext, overlay map[string][]byte) *packages.Package {
	t.Helper()
	if len(overlay) == 0 {
		runtimebundleCacheMu.Lock()
		entry, ok := runtimebundleCache[bc]
		if !ok {
			entry = &runtimebundleCacheEntry{done: make(chan struct{})}
			runtimebundleCache[bc] = entry
			runtimebundleCacheMu.Unlock()

			cfg := &packages.Config{
				Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles |
					packages.NeedImports | packages.NeedTypes | packages.NeedSyntax | packages.NeedTypesInfo,
				Tests:      false,
				Env:        bc.env(),
				BuildFlags: []string{bc.trimpathFlag()},
			}
			pkgs, err := packages.Load(cfg, runtimebundlePackagePath)
			if err == nil && (packages.PrintErrors(pkgs) > 0 || len(pkgs) != 1 || pkgs[0].Types == nil || pkgs[0].TypesInfo == nil) {
				err = fmt.Errorf("packages=%d (fail closed)", len(pkgs))
			}
			if err == nil {
				entry.pkg = pkgs[0]
			}
			entry.err = err
			close(entry.done)
		} else {
			runtimebundleCacheMu.Unlock()
			<-entry.done
		}
		if entry.err != nil {
			t.Fatalf("load runtimebundle (%s/%s) for bindHost caller graph: %v", bc.GOOS, bc.GOARCH, entry.err)
		}
		return entry.pkg
	}
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles |
			packages.NeedImports | packages.NeedTypes | packages.NeedSyntax | packages.NeedTypesInfo,
		Tests:      false,
		Env:        bc.env(),
		Overlay:    overlay,
		BuildFlags: []string{bc.trimpathFlag()},
	}
	pkgs, err := loadArchOverlay(cfg, runtimebundlePackagePath)
	if err != nil {
		t.Fatalf("load runtimebundle (%s/%s): %v", bc.GOOS, bc.GOARCH, err)
	}
	if packages.PrintErrors(pkgs) > 0 || len(pkgs) != 1 || pkgs[0].Types == nil || pkgs[0].TypesInfo == nil {
		t.Fatalf("load runtimebundle (%s/%s) for bindHost caller graph: packages=%d (fail closed)", bc.GOOS, bc.GOARCH, len(pkgs))
	}
	return pkgs[0]
}

// packagesLoadEnv deterministically overrides GOOS/GOARCH/CGO_ENABLED without
// relying on duplicate-variable last-wins ordering.
func packagesLoadEnv(goos, goarch string) []string {
	return (archBuildContext{GOOS: goos, GOARCH: goarch}).env()
}

func (bc archBuildContext) trimpathFlag() string {
	if bc.Trimpath {
		return "-trimpath=true"
	}
	return "-trimpath=false"
}

func (bc archBuildContext) env() []string {
	out := make([]string, 0, 32)
	for _, kv := range os.Environ() {
		key, _, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		switch key {
		case "GOOS", "GOARCH", "CGO_ENABLED":
			continue
		default:
			out = append(out, kv)
		}
	}
	cgo := "CGO_ENABLED=0"
	if bc.CGOEnabled {
		cgo = "CGO_ENABLED=1"
	}
	return append(
		out,
		"GOOS="+bc.GOOS,
		"GOARCH="+bc.GOARCH,
		cgo,
	)
}

func runtimebundleDir(t *testing.T) string {
	t.Helper()
	runtimebundleDirOnce.Do(func() {
		pkgs, err := packages.Load(&packages.Config{
			Mode:  packages.NeedName | packages.NeedFiles,
			Tests: false,
			Env:   packagesLoadEnv("linux", "amd64"),
		}, runtimebundlePackagePath)
		if err != nil || packages.PrintErrors(pkgs) > 0 || len(pkgs) != 1 || len(pkgs[0].GoFiles) == 0 {
			return
		}
		runtimebundleDirCached = filepath.Dir(pkgs[0].GoFiles[0])
	})
	if runtimebundleDirCached == "" {
		t.Fatalf("resolve runtimebundle dir failed")
	}
	return runtimebundleDirCached
}

// assertProductionGoInventory fails closed when a non-test production .go file
// under an analyzed package directory is not present in the union of
// compiled/analyzed files across the configured context matrix (including overlays).
func assertProductionGoInventory(t *testing.T, pkgDirs []string, analyzed map[string]bool) {
	t.Helper()
	var missing []string
	for _, dir := range pkgDirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("read package dir %s: %v", dir, err)
		}
		for _, ent := range entries {
			if ent.IsDir() {
				continue
			}
			name := ent.Name()
			if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			path := filepath.Clean(filepath.Join(dir, name))
			if analyzed[path] {
				continue
			}
			missing = append(missing, path)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		t.Fatalf("production .go files not covered by supported build contexts:\n%s", strings.Join(missing, "\n"))
	}
}

type functionRegion struct {
	name       string
	start, end token.Pos
}

func functionRegions(fset *token.FileSet, file *ast.File) []functionRegion {
	var regions []functionRegion
	ast.Inspect(file, func(node ast.Node) bool {
		switch node := node.(type) {
		case *ast.FuncDecl:
			if node.Body != nil {
				regions = append(regions, functionRegion{name: node.Name.Name, start: node.Body.Pos(), end: node.Body.End()})
			}
		case *ast.FuncLit:
			line := fset.Position(node.Pos()).Line
			regions = append(regions, functionRegion{name: "<func literal at line " + strconv.Itoa(line) + ">", start: node.Body.Pos(), end: node.Body.End()})
		}
		return true
	})
	return regions
}

func calledObject(info *types.Info, expr ast.Expr) types.Object {
	for {
		switch e := expr.(type) {
		case *ast.ParenExpr:
			expr = e.X
		case *ast.Ident:
			return info.Uses[e]
		case *ast.SelectorExpr:
			return info.Uses[e.Sel]
		default:
			return nil
		}
	}
}
