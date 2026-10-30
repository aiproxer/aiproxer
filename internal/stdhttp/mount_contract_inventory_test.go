package stdhttp

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// mountContractInventory is the Task 3.1 deterministic table of production mount
// helpers / input structs. Kept in sync with
// .kiro/specs/archive/runtime-architecture-convergence-and-shrinkage/mount-dependency-inventory.md
// by TestMountContract_InventoryComplete.
var mountContractInventory = []mountInventoryRow{
	{
		Helper: "mountMetrics", File: "mount_metrics.go", Input: "mountMetricsInput",
		BuiltFields: []string{"Metrics"}, DesiredGroups: []string{"Operations"},
		Lifecycle: "none", BehaviorTests: []string{"TestStackHTTPHandler_recoveredPanic_combinedMetricsAccessAndSafeBody", "TestMountContract_NilOptionalCapabilitiesSkipMounts"},
	},
	{
		Helper: "mountDiagnostics", File: "mount_diagnostics.go", Input: "mountDiagnosticsInput",
		BuiltFields: []string{"Store", "SecretGuardInventory"}, DesiredGroups: []string{"Operations", "Core"},
		Lifecycle: "none", BehaviorTests: []string{"TestStandardMiddlewareMountParity_ComposeStandardHTTPRouteSetAndStack"},
	},
	{
		Helper: "mountModelCatalogDiagnostics", File: "mount_diagnostics.go", Input: "diagnosticsMount",
		BuiltFields: []string{"CatalogRuntime"}, DesiredGroups: []string{"Models"},
		Lifecycle: "none", BehaviorTests: []string{"TestModelCatalogDiagnostics_protectAndRedact"},
	},
	{
		Helper: "mountModelInventoryDiagnostics", File: "mount_diagnostics.go", Input: "diagnosticsMount",
		BuiltFields: []string{"ModelRegistryRuntime"}, DesiredGroups: []string{"Models"},
		Lifecycle: "none", BehaviorTests: []string{"TestModelRegistryStatusHandler_nilRuntime"},
	},
	{
		Helper: "mountSecureSessionDiagnostics", File: "mount_securesession.go", Input: "mountSecureSessionDiagnosticsInput",
		BuiltFields: []string{"SecureSessionStore"}, DesiredGroups: []string{"Security"},
		Lifecycle: "none", BehaviorTests: []string{"TestSecureSessionDiagnostics_mount_matchesComposePattern"},
	},
	{
		Helper: "mountKeepwarmAdmin", File: "mount_admin.go", Input: "mountAccountingAdminInput",
		BuiltFields: []string{"KeepwarmAdmin", "KeepwarmAdminEnabled"}, DesiredGroups: []string{"Operations"},
		Lifecycle: "none", BehaviorTests: []string{"TestKeepwarmAdminMountedWithDiagnosticsSecret"},
	},
	{
		Helper: "mountAccountingAdmin", File: "mount_admin.go", Input: "mountAccountingAdminInput",
		BuiltFields: []string{"TokenAccountingAdmin", "Executor"}, DesiredGroups: []string{"Operations", "Core"},
		Lifecycle: "none", BehaviorTests: []string{"TestTokenAccountingAdminMountedWithDiagnosticsSecret"},
	},
	{
		Helper: "mountAdminMounts", File: "mount_admin.go", Input: "adminMountsInput",
		BuiltFields: []string{"AdminMounts"}, DesiredGroups: []string{"Operations"},
		Lifecycle: "none", BehaviorTests: []string{"TestAdminMountsMountedAndProtected"},
	},
	{
		Helper: "mountControlPlaneQuery", File: "mount_admin.go", Input: "controlPlaneQueryMount",
		BuiltFields: []string{"ControlPlaneQueries", "ReadinessReport"}, DesiredGroups: []string{"Operations"},
		Lifecycle: "none", BehaviorTests: []string{"TestControlPlaneQuery_MountedWhenEnabledAndProtected"},
	},
	{
		Helper: "mountAccountingAuthorityQuery", File: "mount_admin.go", Input: "accountingAuthorityQueryMount",
		BuiltFields: []string{"UsageAuthority", "ConcurrencyAuthority", "Executor"}, DesiredGroups: []string{"Security", "Core"},
		Lifecycle: "none", BehaviorTests: []string{"TestAccountingAuthorityQueryMountedAndProtected"},
	},
	{
		Helper: "mountRouteOverrideAdmin", File: "mount_admin.go", Input: "routeOverrideAdminMount",
		BuiltFields: []string{"RouteOverrideAdmin"}, DesiredGroups: []string{"Operations"},
		Lifecycle: "none", BehaviorTests: []string{"TestComposeStandardHTTP_routingOverrideAdminRequiresAccessAuth", "TestComposeStandardHTTP_routingOverrideAdminNotOnFrontendPaths"},
	},
	{
		Helper: "MountBundledFrontends", File: "mount.go", Input: "MountBundledFrontendsInput",
		BuiltFields: nil, DesiredGroups: []string{"Frontends"},
		Lifecycle: "none", BehaviorTests: []string{"TestMountBundledFrontends_geminiDoesNotRegisterRoot"},
	},
	{
		Helper: "mountALegCancel", File: "cancel.go", Input: "",
		BuiltFields: nil, DesiredGroups: []string{"Frontends", "Core"},
		Lifecycle: "none", BehaviorTests: []string{"TestMountBundledFrontends_geminiDoesNotRegisterRoot"},
	},
	{
		Helper: "stackHTTPHandler", File: "middleware.go", Input: "stackHTTPInput",
		BuiltFields: []string{"HTTPAuthProviders"}, DesiredGroups: []string{"Security"},
		Lifecycle: "none", BehaviorTests: []string{"TestStandardMiddlewareMountParity_StackOrderObservables", "TestMountContract_StackAuthBeforeInnerHandler"},
	},
	{
		Helper: "mountProxyMemory", File: "proxy_memory_mount.go", Input: "",
		BuiltFields: []string{"ProxyMemory"}, DesiredGroups: []string{"Operations"},
		Lifecycle: "none", BehaviorTests: []string{"TestComposeStandardHTTP_ProxyMemoryRoutesReachProjectedHandler", "TestComposeStandardHTTP_ProxyMemoryUnmountedWithoutHandler"},
	},
}

// compositionRootInventory covers composition roots documented separately from
// mount helpers in mount-dependency-inventory.md. Completeness validates both.
// MigrationStatus annotates Task 3.2 vs later retirement — not all roots must
// accept StandardHTTPInput in Task 3.2.
var compositionRootInventory = []mountInventoryRow{
	{
		Helper: "prepareStandardHandler", File: "handler.go", Input: "",
		BuiltFields:   []string{"Executor", "EffectiveDefaultRoute", "PluginRegistry", "DecodeAdmission", "RuntimeSnapshot", "RoutePrefixes", "ModelRegistryRuntime"},
		DesiredGroups: []string{"StandardHTTPInput"},
		Lifecycle:     "owned_above", BehaviorTests: []string{"TestComposeStandardHTTP_openAIModelsAndModelRegistryDiagMounted"},
		MigrationStatus: "strict_task_32",
	},
	{
		Helper: "ComposeStandardHTTP", File: "request_plane.go", Input: "StandardHTTPInput",
		BuiltFields: nil, DesiredGroups: []string{"StandardHTTPInput"},
		Lifecycle: "none", BehaviorTests: []string{"TestComposeStandardHTTP_RouteConflictRejects", "TestComposeStandardHTTP_ManagementRoutesNotMounted", "TestStandardMiddlewareMountParity_ComposeStandardHTTPRouteSetAndStack"},
		MigrationStatus: "canonical_task34",
	},
}

type mountInventoryRow struct {
	Helper          string
	File            string
	Input           string
	BuiltFields     []string
	DesiredGroups   []string
	Lifecycle       string
	BehaviorTests   []string
	MigrationStatus string // composition roots only; empty for mount helpers
}

// expectedProductionMountHelpers validates the known current mount surface is
// still discovered. It must NOT suppress unknown discoveries — completeness
// requires every discovered mount-scoped name to be inventoried.
var expectedProductionMountHelpers = map[string]bool{
	"mountMetrics":                   true,
	"mountDiagnostics":               true,
	"mountModelCatalogDiagnostics":   true,
	"mountModelInventoryDiagnostics": true,
	"mountSecureSessionDiagnostics":  true,
	"mountKeepwarmAdmin":             true,
	"mountAccountingAdmin":           true,
	"mountAdminMounts":               true,
	"mountControlPlaneQuery":         true,
	"mountAccountingAuthorityQuery":  true,
	"mountRouteOverrideAdmin":        true,
	"MountBundledFrontends":          true,
	"mountALegCancel":                true,
	"stackHTTPHandler":               true,
	"mountProxyMemory":               true,
}

var expectedCompositionRoots = map[string]bool{
	"prepareStandardHandler": true,
	"ComposeStandardHTTP":    true,
}

// migrationStatusMarkdownPhrase maps composition-root MigrationStatus codes to
// distinctive phrases that must appear in mount-dependency-inventory.md.
var migrationStatusMarkdownPhrase = map[string]string{
	"strict_task_32":   "strict Task 3.2 target",
	"canonical_task34": "canonical Task 3.4 composer",
}

// mountDiscoveryExclusions is intentionally empty: every discovered mount-scoped
// helper must appear in the inventory. Prefer keeping this empty; only add a
// name with a justifying comment if a true non-contract false positive appears.
var mountDiscoveryExclusions = map[string]bool{
	// none
}

const mountDependencyInventoryRel = ".kiro/specs/archive/runtime-architecture-convergence-and-shrinkage/mount-dependency-inventory.md"

// TestMountContract_BehaviorTestsExist proves every inventoried BehaviorTests
// reference resolves to a live Test* function under internal/stdhttp (Task 4.3:
// stale RunWithRuntime/NewStandardHandler inventory strings must not survive).
func TestMountContract_BehaviorTestsExist(t *testing.T) {
	t.Parallel()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	live, err := discoverStdhttpTestFuncs(dir)
	if err != nil {
		t.Fatalf("discover test funcs: %v", err)
	}
	var missing []string
	for _, row := range append(append([]mountInventoryRow{}, mountContractInventory...), compositionRootInventory...) {
		for _, name := range row.BehaviorTests {
			if !live[name] {
				missing = append(missing, row.Helper+":"+name)
			}
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("Task 4.3 RED: inventory BehaviorTests must name live stdhttp tests; missing %v", missing)
	}
}

func discoverStdhttpTestFuncs(pkgDir string) (map[string]bool, error) {
	entries, err := os.ReadDir(pkgDir)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	fset := token.NewFileSet()
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		src, err := os.ReadFile(filepath.Join(pkgDir, e.Name()))
		if err != nil {
			return nil, err
		}
		f, err := parser.ParseFile(fset, e.Name(), src, parser.SkipObjectResolution)
		if err != nil {
			return nil, err
		}
		for _, decl := range f.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Name == nil || fd.Recv != nil {
				continue
			}
			if strings.HasPrefix(fd.Name.Name, "Test") {
				out[fd.Name.Name] = true
			}
		}
	}
	return out, nil
}

// TestMountContract_InventoryComplete proves the Task 3.1 inventory table matches
// AST-discovered production mount helpers and composition roots and cannot silently stale.
func TestMountContract_InventoryComplete(t *testing.T) {
	t.Parallel()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	discoveredMounts, discoveredRoots, err := discoverProductionMountSurface(dir)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}

	mountInv := inventoryIndex(t, mountContractInventory, "mount", false)
	rootInv := inventoryIndex(t, compositionRootInventory, "composition-root", true)

	assertInventoryCompleteForRows(t, "mount helper", discoveredMounts, mountInv, mountDiscoveryExclusions)
	assertInventoryCompleteForRows(t, "composition root", discoveredRoots, rootInv, nil)

	// Known expected sets may only validate presence — never suppress unknowns.
	for name := range expectedProductionMountHelpers {
		if !discoveredMounts[name] {
			t.Fatalf("expected mount helper %q not discovered in production stdhttp", name)
		}
	}
	for name := range expectedCompositionRoots {
		if !discoveredRoots[name] {
			t.Fatalf("expected composition root %q not discovered in production stdhttp", name)
		}
	}

	// Markdown inventory must mention every inventoried helper so the artifact cannot drift.
	root := dir
	for range 8 {
		if _, err := os.Stat(filepath.Join(root, "go.mod")); err == nil {
			break
		}
		root = filepath.Dir(root)
	}
	mdPath := filepath.Join(root, filepath.FromSlash(mountDependencyInventoryRel))
	raw, err := os.ReadFile(mdPath)
	if err != nil {
		t.Fatalf("read inventory markdown: %v", err)
	}
	md := string(raw)
	for name := range mountInv {
		if !strings.Contains(md, "`"+name+"`") {
			t.Fatalf("markdown inventory missing mount helper %q", name)
		}
	}
	for name, row := range rootInv {
		if !strings.Contains(md, "`"+name+"`") {
			t.Fatalf("markdown inventory missing composition root %q", name)
		}
		if row.MigrationStatus == "" {
			t.Fatalf("composition root %q missing MigrationStatus annotation", name)
		}
		phrase, ok := migrationStatusMarkdownPhrase[row.MigrationStatus]
		if !ok {
			t.Fatalf("composition root %q has unknown MigrationStatus %q", name, row.MigrationStatus)
		}
		if !strings.Contains(md, phrase) {
			t.Fatalf("markdown inventory missing migration status phrase %q for %s", phrase, name)
		}
	}
	// Guard against stale "all roots must take StandardHTTPInput in 3.2" wording.
	if strings.Contains(md, "must not pass broad bags after Task 3.2") &&
		strings.Contains(md, "Composition roots (consume mounts; must not pass broad bags") {
		t.Fatal("markdown must not require all composition roots to abandon broad source inputs in Task 3.2")
	}
}

// TestMountContract_InventoryCompletenessPredicate_ReportsUnknownDiscovery proves
// an unknown newly discovered mount helper is reported missing (and unrelated
// non-mount names stay out of scope).
func TestMountContract_InventoryCompletenessPredicate_ReportsUnknownDiscovery(t *testing.T) {
	t.Parallel()
	discovered := map[string]bool{
		"mountMetrics":    true,
		"mountNewSurface": true,
	}
	inventoried := map[string]bool{
		"mountMetrics": true,
	}
	missing := inventoryGaps(discovered, inventoried, nil)
	if len(missing) != 1 || missing[0] != "mountNewSurface" {
		t.Fatalf("gaps=%v want [mountNewSurface]", missing)
	}
	// Unrelated helpers not in mount naming/scope must remain ignored by discovery predicates.
	if isMountHelperName("helperUnrelated") || isCompositionRootName("helperUnrelated") {
		t.Fatal("unrelated helper must not be in mount/composition scope")
	}
	if !isMountHelperName("mountNewSurface") {
		t.Fatal("mountNewSurface must be in mount helper naming scope")
	}
	if isCompositionRootName("mountNewSurface") {
		t.Fatal("mountNewSurface must not be classified as composition root")
	}
}

func inventoryIndex(t *testing.T, rows []mountInventoryRow, kind string, allowOwnedAbove bool) map[string]mountInventoryRow {
	t.Helper()
	inv := map[string]mountInventoryRow{}
	for _, row := range rows {
		if _, dup := inv[row.Helper]; dup {
			t.Fatalf("duplicate %s inventory helper %q", kind, row.Helper)
		}
		inv[row.Helper] = row
		switch row.Lifecycle {
		case "none":
		case "owned_above":
			if !allowOwnedAbove {
				t.Fatalf("%s %s: lifecycle at mount must be none, got %q", kind, row.Helper, row.Lifecycle)
			}
		default:
			t.Fatalf("%s %s: unexpected lifecycle %q", kind, row.Helper, row.Lifecycle)
		}
		if len(row.DesiredGroups) == 0 {
			t.Fatalf("%s %s: desired cohesive group(s) required", kind, row.Helper)
		}
		if len(row.BehaviorTests) == 0 {
			t.Fatalf("%s %s: behavior test reference(s) required", kind, row.Helper)
		}
	}
	return inv
}

// assertInventoryCompleteForRows checks discovery ↔ inventory bijection (minus exclusions).
func assertInventoryCompleteForRows(t *testing.T, label string, discovered map[string]bool, inv map[string]mountInventoryRow, exclusions map[string]bool) {
	t.Helper()
	inventoried := map[string]bool{}
	for name := range inv {
		inventoried[name] = true
	}
	missing := inventoryGaps(discovered, inventoried, exclusions)
	if len(missing) > 0 {
		t.Fatalf("discovered %s missing from inventory: %v", label, missing)
	}
	var stale []string
	for name := range inventoried {
		if !discovered[name] {
			stale = append(stale, name)
		}
	}
	sort.Strings(stale)
	if len(stale) > 0 {
		t.Fatalf("inventory lists %s not discovered in production stdhttp: %v", label, stale)
	}
}

func inventoryGaps(discovered, inventoried, exclusions map[string]bool) []string {
	var missing []string
	for name := range discovered {
		if exclusions[name] {
			continue
		}
		if !inventoried[name] {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	return missing
}

func discoverProductionMountSurface(pkgDir string) (mounts, roots map[string]bool, err error) {
	entries, err := os.ReadDir(pkgDir)
	if err != nil {
		return nil, nil, err
	}
	mounts = map[string]bool{}
	roots = map[string]bool{}
	fset := token.NewFileSet()
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		src, err := os.ReadFile(filepath.Join(pkgDir, e.Name()))
		if err != nil {
			return nil, nil, err
		}
		f, err := parser.ParseFile(fset, e.Name(), src, parser.SkipObjectResolution)
		if err != nil {
			return nil, nil, err
		}
		for _, decl := range f.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Name == nil || fd.Recv != nil {
				continue
			}
			name := fd.Name.Name
			if isCompositionRootName(name) {
				roots[name] = true
				continue
			}
			if isMountHelperName(name) {
				mounts[name] = true
			}
		}
	}
	return mounts, roots, nil
}

func isCompositionRootName(name string) bool {
	switch name {
	case "prepareStandardHandler", "ComposeStandardHTTP":
		return true
	default:
		return false
	}
}

func isMountHelperName(name string) bool {
	if name == "stackHTTPHandler" {
		return true
	}
	if strings.HasPrefix(name, "mount") {
		return true
	}
	if strings.HasPrefix(name, "Mount") && !strings.Contains(name, "Spec") {
		return true
	}
	return false
}
