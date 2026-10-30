package archtest

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"golang.org/x/tools/go/packages"
)

const runtimehostPackagePath = "github.com/aiproxer/aiproxer/internal/infra/runtimehost"

var coordinatorReloadStageForbidden = map[string]bool{
	"ReadStable":          true,
	"LoadEffective":       true,
	"Compile":             true,
	"PrepareRequestPlane": true,
	"Publish":             true,
}

type callerInvariant struct {
	name      string
	target    func(t *testing.T, pkg *packages.Package) types.Object
	wantSites int
	allow     func(site string) bool
}

func runtimehostOwnershipInvariants() []callerInvariant {
	return []callerInvariant{
		{
			name: "newReloadState sole construction site",
			target: func(t *testing.T, pkg *packages.Package) types.Object {
				t.Helper()
				return lookupPkgFunc(pkg, "newReloadState")
			},
			wantSites: 1,
			allow:     exactCaller("coordinator.go:NewCoordinator"),
		},
		{
			name: "newAttemptRunner sole construction site",
			target: func(t *testing.T, pkg *packages.Package) types.Object {
				t.Helper()
				return lookupPkgFunc(pkg, "newAttemptRunner")
			},
			wantSites: 1,
			allow:     exactCaller("coordinator.go:NewCoordinator"),
		},
		{
			name: "newAttemptGate sole construction site",
			target: func(t *testing.T, pkg *packages.Package) types.Object {
				t.Helper()
				return lookupPkgFunc(pkg, "newAttemptGate")
			},
			wantSites: 1,
			allow:     exactCaller("coordinator.go:NewCoordinator"),
		},
		{
			name: "retireGeneration Manager-only scheduling",
			target: func(t *testing.T, pkg *packages.Package) types.Object {
				t.Helper()
				return lookupPkgFunc(pkg, "retireGeneration")
			},
			wantSites: 2,
			allow: func(site string) bool {
				return site == "manager.go:RetireGeneration" || site == "manager.go:scheduleRetire"
			},
		},
		{
			name: "attemptRunner.Run only from Coordinator.Reload",
			target: func(t *testing.T, pkg *packages.Package) types.Object {
				t.Helper()
				return lookupNamedMethod(pkg, "attemptRunner", "Run")
			},
			wantSites: 1,
			allow:     coordinatorMethod("coordinator.go", "Reload"),
		},
		{
			name: "ReloadState.ActiveInput only from Coordinator.Reload",
			target: func(t *testing.T, pkg *packages.Package) types.Object {
				t.Helper()
				return lookupNamedMethod(pkg, "ReloadState", "ActiveInput")
			},
			wantSites: 1,
			allow:     coordinatorMethod("coordinator.go", "Reload"),
		},
		{
			name: "ReloadState.Apply only from Coordinator.Reload",
			target: func(t *testing.T, pkg *packages.Package) types.Object {
				t.Helper()
				return lookupNamedMethod(pkg, "ReloadState", "Apply")
			},
			wantSites: 1,
			allow:     coordinatorMethod("coordinator.go", "Reload"),
		},
		{
			name: "ReloadState.Snapshot only from Coordinator.Status",
			target: func(t *testing.T, pkg *packages.Package) types.Object {
				t.Helper()
				return lookupNamedMethod(pkg, "ReloadState", "Snapshot")
			},
			wantSites: 1,
			allow:     coordinatorMethod("coordinator.go", "Status"),
		},
		{
			name: "attemptGate.TryStart only from Coordinator.Reload",
			target: func(t *testing.T, pkg *packages.Package) types.Object {
				t.Helper()
				return lookupNamedMethod(pkg, "attemptGate", "TryStart")
			},
			wantSites: 1,
			allow:     coordinatorMethod("coordinator.go", "Reload"),
		},
		{
			name: "attemptLease.Complete only from Coordinator.Reload",
			target: func(t *testing.T, pkg *packages.Package) types.Object {
				t.Helper()
				return lookupNamedMethod(pkg, "attemptLease", "Complete")
			},
			wantSites: 1,
			allow:     coordinatorMethod("coordinator.go", "Reload"),
		},
		{
			name: "attemptLease.Abandon only from Coordinator.Reload",
			target: func(t *testing.T, pkg *packages.Package) types.Object {
				t.Helper()
				return lookupNamedMethod(pkg, "attemptLease", "Abandon")
			},
			wantSites: 1,
			allow:     coordinatorReloadLiteral,
		},
		{
			name: "attemptGate.WaitForIdle only from Coordinator.WaitForIdle",
			target: func(t *testing.T, pkg *packages.Package) types.Object {
				t.Helper()
				return lookupNamedMethod(pkg, "attemptGate", "WaitForIdle")
			},
			wantSites: 1,
			allow:     coordinatorMethod("coordinator.go", "WaitForIdle"),
		},
		{
			name: "attemptGate.Snapshot only from Coordinator.Status",
			target: func(t *testing.T, pkg *packages.Package) types.Object {
				t.Helper()
				return lookupNamedMethod(pkg, "attemptGate", "Snapshot")
			},
			wantSites: 1,
			allow:     coordinatorMethod("coordinator.go", "Status"),
		},
		{
			name: "attemptGate.BeginShutdown from Coordinator shutdown paths",
			target: func(t *testing.T, pkg *packages.Package) types.Object {
				t.Helper()
				return lookupNamedMethod(pkg, "attemptGate", "BeginShutdown")
			},
			wantSites: 2,
			allow: func(site string) bool {
				return site == "coordinator.go:BeginShutdown" || site == "coordinator.go:Reload"
			},
		},
	}
}

func exactCaller(site string) func(string) bool {
	return func(got string) bool { return got == site }
}

func coordinatorMethod(file, method string) func(string) bool {
	want := file + ":" + method
	return func(got string) bool { return got == want }
}

func coordinatorReloadLiteral(site string) bool {
	return strings.HasPrefix(site, "coordinator.go:") &&
		(strings.HasSuffix(site, ":Reload") || strings.Contains(site, "func literal"))
}

func checkCallerInvariant(t *testing.T, pkg *packages.Package, inv callerInvariant) []string {
	t.Helper()
	target := inv.target(t, pkg)
	if target == nil {
		return []string{fmt.Sprintf("%s: target not found in package", inv.name)}
	}
	sites := collectCallSitesForObject(pkg, target)
	var allowed, disallowed []string
	for _, site := range sites {
		if inv.allow(site) {
			allowed = append(allowed, site)
			continue
		}
		disallowed = append(disallowed, site)
	}
	var violations []string
	for _, site := range disallowed {
		violations = append(violations, fmt.Sprintf("%s: unexpected caller %s", inv.name, site))
	}
	if len(allowed) != inv.wantSites {
		violations = append(violations, fmt.Sprintf("%s: want exactly %d allowed call site(s); got %d (%v)",
			inv.name, inv.wantSites, len(allowed), allowed))
	}
	return violations
}

func collectCallSitesForObject(pkg *packages.Package, target types.Object) []string {
	seen := map[string]bool{}
	var sites []string
	for _, file := range pkg.Syntax {
		regions := functionRegions(pkg.Fset, file)
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok || calledObject(pkg.TypesInfo, call.Fun) != target {
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
			if !seen[key] {
				seen[key] = true
				sites = append(sites, key)
			}
			return true
		})
	}
	sort.Strings(sites)
	return sites
}

func scanCoordinatorForbiddenReloadStages(pkg *packages.Package) []string {
	var violations []string
	for _, file := range pkg.Syntax {
		filename := filepath.Base(pkg.Fset.Position(file.Pos()).Filename)
		if !strings.HasPrefix(filename, "coordinator") || strings.HasSuffix(filename, "_test.go") {
			continue
		}
		regions := functionRegions(pkg.Fset, file)
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel == nil || !coordinatorReloadStageForbidden[sel.Sel.Name] {
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
			pos := pkg.Fset.Position(call.Pos())
			violations = append(violations, fmt.Sprintf("%s:%d: %s calls forbidden reload stage %s",
				filename, pos.Line, caller, sel.Sel.Name))
			return true
		})
	}
	sort.Strings(violations)
	return violations
}

func lookupPkgFunc(pkg *packages.Package, name string) *types.Func {
	obj := pkg.Types.Scope().Lookup(name)
	if obj == nil {
		return nil
	}
	fn, _ := obj.(*types.Func)
	return fn
}

func lookupNamedMethod(pkg *packages.Package, typeName, methodName string) *types.Func {
	obj := pkg.Types.Scope().Lookup(typeName)
	if obj == nil {
		return nil
	}
	named, ok := obj.Type().(*types.Named)
	if !ok {
		return nil
	}
	m := types.NewMethodSet(types.NewPointer(named)).Lookup(nil, methodName)
	if m == nil {
		return nil
	}
	fn, _ := m.Obj().(*types.Func)
	return fn
}

type runtimehostCacheEntry struct {
	done chan struct{}
	pkg  *packages.Package
	err  error
}

var (
	runtimehostCacheMu   sync.Mutex
	runtimehostCache     = make(map[archBuildContext]*runtimehostCacheEntry)
	runtimehostDirOnce   sync.Once
	runtimehostDirCached string
)

func loadRuntimehostForContext(t *testing.T, bc archBuildContext, overlay map[string][]byte) *packages.Package {
	t.Helper()
	if len(overlay) == 0 {
		runtimehostCacheMu.Lock()
		entry, ok := runtimehostCache[bc]
		if !ok {
			entry = &runtimehostCacheEntry{done: make(chan struct{})}
			runtimehostCache[bc] = entry
			runtimehostCacheMu.Unlock()

			cfg := &packages.Config{
				Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles |
					packages.NeedImports | packages.NeedTypes | packages.NeedSyntax | packages.NeedTypesInfo,
				Tests:      false,
				Env:        bc.env(),
				BuildFlags: []string{bc.trimpathFlag()},
			}
			pkgs, err := packages.Load(cfg, runtimehostPackagePath)
			if err == nil && (packages.PrintErrors(pkgs) > 0 || len(pkgs) != 1 || pkgs[0].Types == nil || pkgs[0].TypesInfo == nil) {
				err = fmt.Errorf("packages=%d (fail closed)", len(pkgs))
			}
			if err == nil {
				entry.pkg = pkgs[0]
			}
			entry.err = err
			close(entry.done)
		} else {
			runtimehostCacheMu.Unlock()
			<-entry.done
		}
		if entry.err != nil {
			t.Fatalf("load runtimehost (%s/%s): %v", bc.GOOS, bc.GOARCH, entry.err)
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
	pkgs, err := loadArchOverlay(cfg, runtimehostPackagePath)
	if err != nil {
		t.Fatalf("load runtimehost (%s/%s): %v", bc.GOOS, bc.GOARCH, err)
	}
	if packages.PrintErrors(pkgs) > 0 || len(pkgs) != 1 || pkgs[0].Types == nil || pkgs[0].TypesInfo == nil {
		t.Fatalf("load runtimehost (%s/%s): packages=%d (fail closed)", bc.GOOS, bc.GOARCH, len(pkgs))
	}
	return pkgs[0]
}

func runtimehostDir(t *testing.T) string {
	t.Helper()
	runtimehostDirOnce.Do(func() {
		pkgs, err := packages.Load(&packages.Config{
			Mode:  packages.NeedName | packages.NeedFiles,
			Tests: false,
			Env:   packagesLoadEnv("linux", "amd64"),
		}, runtimehostPackagePath)
		if err != nil || packages.PrintErrors(pkgs) > 0 || len(pkgs) != 1 || len(pkgs[0].GoFiles) == 0 {
			return
		}
		runtimehostDirCached = filepath.Dir(pkgs[0].GoFiles[0])
	})
	if runtimehostDirCached == "" {
		t.Fatalf("resolve runtimehost dir failed")
	}
	return runtimehostDirCached
}

func assertRuntimehostOwnershipForContexts(t *testing.T, contexts []archBuildContext) {
	t.Helper()
	t.Parallel()
	for _, inv := range runtimehostOwnershipInvariants() {
		t.Run(inv.name, func(t *testing.T) {
			t.Parallel()
			for _, bc := range contexts {
				pkg := loadRuntimehostForContext(t, bc, nil)
				if violations := checkCallerInvariant(t, pkg, inv); len(violations) > 0 {
					t.Fatalf("%s/%s: %s", bc.GOOS, bc.GOARCH, strings.Join(violations, "\n"))
				}
			}
		})
	}
	t.Run("coordinator_no_direct_reload_stage_execution", func(t *testing.T) {
		t.Parallel()
		for _, bc := range contexts {
			pkg := loadRuntimehostForContext(t, bc, nil)
			if got := scanCoordinatorForbiddenReloadStages(pkg); len(got) > 0 {
				t.Fatalf("%s/%s: Coordinator must not directly execute reload stages:\n%s",
					bc.GOOS, bc.GOARCH, strings.Join(got, "\n"))
			}
		}
	})
}

func assertRuntimehostRogueConstructorForContexts(t *testing.T, contexts []archBuildContext) {
	t.Helper()
	t.Parallel()
	dir := runtimehostDir(t)
	overlayPath := filepath.Join(dir, "rogue_gate_overlay.go")
	overlay := map[string][]byte{
		overlayPath: []byte(`package runtimehost

func rogueExtraGateCaller() {
	_ = newAttemptGate()
}
`),
	}
	var violations []string
	for _, bc := range contexts {
		pkg := loadRuntimehostForContext(t, bc, overlay)
		violations = append(violations, checkCallerInvariant(t, pkg, callerInvariant{
			name: "newAttemptGate sole construction site",
			target: func(t *testing.T, pkg *packages.Package) types.Object {
				t.Helper()
				return lookupPkgFunc(pkg, "newAttemptGate")
			},
			wantSites: 1,
			allow: func(site string) bool {
				return site == "coordinator.go:NewCoordinator"
			},
		})...)
	}
	if len(violations) == 0 {
		t.Fatal("rogue newAttemptGate caller overlay must be detected by ownership gate")
	}
	wantExtra := "rogue_gate_overlay.go:rogueExtraGateCaller"
	found := false
	for _, v := range violations {
		if strings.Contains(v, wantExtra) {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected overlay caller %q in violations; got %v", wantExtra, violations)
	}
}
