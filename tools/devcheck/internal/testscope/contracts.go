package testscope

import (
	"context"
	"errors"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

// ContractPlan selects existing cross-package checks and direct-package lint.
// It deliberately does not expand reverse dependencies or certify delivery.
type ContractPlan struct {
	Base    string
	Changed []string
	Tests   []string
	Groups  []ContractTestGroup
	Reasons []string
	Lint    []Module
	QA      []string
	Scripts []string
}

// ContractTestGroup selects named existing tests that live in one package
// outside ./internal/archtest, which is where registration surfaces certify
// themselves (standard-family composition, fixed-route collisions).
type ContractTestGroup struct {
	Package string
	Tests   []string
}

// BuildContracts includes branch, index, working-tree and untracked changes.
// Unlike default-test planning, an unresolved base is an error, not a full run.
func BuildContracts(ctx context.Context, root, base string) (ContractPlan, error) {
	if base == "" {
		base = "origin/main"
	}
	paths, comparison, err := changedPaths(ctx, root, base)
	if err != nil {
		return ContractPlan{}, err
	}
	modules, err := discoverModules(root)
	if err != nil {
		return ContractPlan{}, err
	}
	plan := ContractPlan{Base: comparison, Changed: paths}
	plan.Tests, plan.Reasons = selectContracts(paths)
	registrationTests, groups, registrationReasons := selectRegistrationContracts(paths)
	plan.Tests = append(plan.Tests, registrationTests...)
	plan.Groups = groups
	plan.Reasons = append(plan.Reasons, registrationReasons...)
	slices.Sort(plan.Tests)
	plan.Tests = slices.Compact(plan.Tests)
	slices.Sort(plan.Reasons)
	plan.Reasons = slices.Compact(plan.Reasons)
	plan.QA, plan.Scripts = selectAutomation(paths)
	packages := make(map[string][]string)
	for _, name := range paths {
		if !strings.HasSuffix(name, ".go") {
			continue
		}
		dir := path.Dir(name)
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(dir))); errors.Is(err, os.ErrNotExist) {
			plan.Reasons = append(plan.Reasons, "removed package has no lint target: "+dir)
			continue
		} else if err != nil {
			return ContractPlan{}, err
		}
		sources, err := filepath.Glob(filepath.Join(root, filepath.FromSlash(dir), "*.go"))
		if err != nil {
			return ContractPlan{}, err
		}
		if len(sources) == 0 {
			plan.Reasons = append(plan.Reasons, "removed package has no Go sources to lint: "+dir)
			continue
		}
		module := owningModule(name, modules)
		rel, err := filepath.Rel(filepath.Join(root, module), filepath.Join(root, dir))
		if err != nil {
			return ContractPlan{}, err
		}
		packages[module] = append(packages[module], "./"+filepath.ToSlash(rel))
	}
	for _, module := range modules {
		selected := packages[module]
		if len(selected) == 0 {
			continue
		}
		slices.Sort(selected)
		plan.Lint = append(plan.Lint, Module{Directory: module, Packages: slices.Compact(selected)})
	}
	return plan, nil
}

// Known automation inputs reuse their existing self-tests and consumer contracts.
// This is routing, not another source scanner or a full-suite fallback.
func selectAutomation(paths []string) ([]string, []string) {
	var qa, scripts []string
	for _, name := range paths {
		switch name {
		case "Makefile", "scripts/run-named-go-tests.sh", "scripts/test-run-named-go-tests.sh":
			scripts = append(scripts, "scripts/test-run-named-go-tests.sh")
		case "scripts/check-all-modules.sh", "scripts/backend-plugin-module-checks.sh":
			qa = append(qa, "TestCIModuleChecks_PerModuleFailuresPropagate")
		case "scripts/race-check.sh", "scripts/test-race-check.sh", "scripts/go-dev-guard.sh", "scripts/test-go-dev-guard.sh", "scripts/dev-host-policy.sh", "scripts/dev-cpu-defaults.sh", ".github/workflows/race-fuzz-nightly.yml", ".github/workflows/release.yml", ".github/workflows/backend-plugin-release-gates.yml":
			qa = append(qa, "TestRaceCheckDevHostGuard", "TestRaceCheckStagedScanPartitionsArchtestFromOrdinaryScopes")
			scripts = append(scripts, "scripts/test-race-check.sh")
		case "scripts/quality-checks.sh", "scripts/quality-gate.sh", "scripts/test-quality-checks.sh", "scripts/hooks/pre-commit", "scripts/staged-commit-checks.sh", ".githooks/pre-commit":
			scripts = append(scripts, "scripts/test-quality-checks.sh")
		}
		switch name {
		case "scripts/dev-host-policy.sh", "scripts/dev-cpu-defaults.sh":
			scripts = append(scripts, "scripts/test-quality-checks.sh", "scripts/test-configsource-fault-check.sh")
		case "scripts/configsource-fault-check.sh", "scripts/test-configsource-fault-check.sh":
			scripts = append(scripts, "scripts/test-configsource-fault-check.sh")
		}
		switch name {
		case "scripts/dev-setup.sh", "scripts/test-dev-setup.sh", "scripts/dev-host-policy.sh", "scripts/dev-cpu-defaults.sh", "scripts/go-dev-guard.sh", "scripts/setup-hooks.sh", "scripts/install-hooks.sh", "scripts/install-hooks.ps1", "scripts/check-worktree-layout.sh":
			scripts = append(scripts, "scripts/test-dev-setup.sh")
		}
		if strings.HasPrefix(name, ".github/workflows/") || name == "scripts/ci-scope.sh" {
			scripts = append(scripts, "scripts/ci-scope.sh --self-test")
			qa = append(qa, "TestQAFastPreflight_MainPushLaneScopes")
		}
		if strings.HasPrefix(name, ".github/workflows/") || name == "scripts/check-workflows.sh" {
			scripts = append(scripts, "scripts/check-workflows.sh")
		}
	}
	slices.Sort(qa)
	slices.Sort(scripts)
	return slices.Compact(qa), slices.Compact(scripts)
}

func selectContracts(paths []string) ([]string, []string) {
	var tests, reasons []string
	add := func(reason string, names ...string) {
		reasons = append(reasons, reason)
		tests = append(tests, names...)
	}
	for _, name := range paths {
		if strings.HasPrefix(name, "connectors/") && strings.HasSuffix(name, ".go") {
			add("connector host import boundary (including tests)", "TestConnectorSubtree_CannotImportHostSecurityAuthority")
		}
		if !strings.HasSuffix(name, ".go") || isTestFile(name) {
			continue
		}
		if strings.HasPrefix(name, "internal/core/") || strings.HasPrefix(name, "internal/infra/") || strings.HasPrefix(name, "pkg/") || strings.HasPrefix(name, "internal/standardplugins/") {
			add("shared boundary", "TestForbiddenImportsAbsent", "TestCriticalFileBudgets", "TestLineComplexityBudgets", "TestClosedPlaneArchitectureRatchets_ProductionClean")
		}
		if strings.HasPrefix(name, "internal/core/") || strings.HasPrefix(name, "internal/infra/runtime") {
			add("request state/ownership", "TestRequestAttemptStateRatchetsPassOnCurrentCode")
		}
		if strings.HasPrefix(name, "internal/standardplugins/") || strings.HasPrefix(name, "internal/pluginreg/") || strings.HasPrefix(name, "internal/infra/runtime") || strings.HasPrefix(name, "internal/stdhttp/") || strings.HasPrefix(name, "cmd/aipstd/") || strings.HasPrefix(name, "pkg/aipruntime/") {
			add("composition facade", "TestGenerationFacadeShape", "TestFacadeNamedTypesStable", "TestRuntimeBundleFeatureIdentifierBoundary", "TestRecvFacadeRatchet", "TestShrinkage_NetReductionMeetsRequirement115")
		}
		// Persistence implementations live throughout core and infra; select the
		// existing catalog scanner conservatively instead of guessing store names.
		if strings.HasPrefix(name, "internal/core/") || strings.HasPrefix(name, "internal/infra/") || strings.HasPrefix(name, "internal/testkit/dbparity/") {
			add("persistence capabilities/catalog", "TestDatabaseParity_CatalogIntegrity", "TestDatabaseParity_DiscoveredMigrationRootsMatchCatalog", "TestDatabaseParity_DialectSensitiveSourcesAreCataloged", "TestDatabaseParity_StoreContractsCompileTimeAssertions", "TestDatabaseParity_StableWrapperCoverage", "TestDatabaseParity_CapabilityEvidenceAnchors")
		}
		if strings.HasPrefix(name, "pkg/") || strings.HasPrefix(name, "api/backendplugin/") {
			add("public contract", "TestStructuralABIMutationGuards")
		}
	}
	slices.Sort(tests)
	slices.Sort(reasons)
	return slices.Compact(tests), slices.Compact(reasons)
}

// selectRegistrationContracts turns a change to any plugin registration
// surface into the existing checks that certify it: backend lifecycle and
// family invariants in ./internal/archtest, the standard-composition backend
// TCK, and the fixed-route collision proof. A new kind, family or route fails
// these before review rather than during a later full gate.
func selectRegistrationContracts(paths []string) ([]string, []ContractTestGroup, []string) {
	registration := false
	for _, name := range paths {
		if !strings.HasSuffix(name, ".go") || isTestFile(name) {
			continue
		}
		if strings.HasPrefix(name, "internal/standardplugins/") ||
			strings.HasPrefix(name, "internal/pluginreg/") ||
			strings.HasPrefix(name, "internal/plugins/backends/") ||
			strings.HasPrefix(name, "internal/plugins/frontends/") ||
			strings.HasPrefix(name, "internal/providerprofiles/") {
			registration = true
			break
		}
	}
	if !registration {
		return nil, nil, nil
	}
	return []string{
			"TestOfficialBackendsHaveLifecycleContractTests",
			"TestNonCartesianScale_ThousandProfilesDoNotMultiplyCartesianPairs",
		},
		[]ContractTestGroup{
			{Package: "./internal/testkit/contract/backend", Tests: []string{"TestStandardComposition_CertifiesEveryInProcessFamily"}},
			{Package: "./internal/stdhttp/selfdefense", Tests: []string{"TestBuiltInImpossiblePathSetNeverMakesAPublishedRouteUnreachable"}},
		},
		[]string{"plugin registration surface"}
}
