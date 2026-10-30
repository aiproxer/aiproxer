package archtest

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// DiscoverMigrationRootsIn walks the given repo-relative roots for versioned Bun
// migration files and returns the discovered migration roots with their
// repo-relative migration file lists. It is the parameterized form of
// DiscoverMigrationRoots, which delegates with the open-source production scan
// roots; the Enterprise usage-authority proofs scan their own roots with the
// same rules and the same parser.
func DiscoverMigrationRootsIn(repoRoot string, roots []string) (map[string][]string, error) {
	discovered := make(map[string][]string)
	err := walkGoFilesIn(repoRoot, roots, func(rel, abs string) error {
		if !versionedMigrationRegex.MatchString(filepath.Base(rel)) {
			return nil
		}
		dir := filepath.ToSlash(filepath.Dir(rel))
		discovered[dir] = append(discovered[dir], rel)
		return nil
	})
	if err != nil {
		return nil, err
	}
	for dir := range discovered {
		sort.Strings(discovered[dir])
	}
	return discovered, nil
}

// DiscoverDialectSensitivePackagesIn scans the given repo-relative roots for
// dialect-sensitive production code and groups the findings by package
// directory. It is the parameterized form of DiscoverDialectSensitivePackages:
// the same detector and the same production-only rule, applied to the roots the
// caller owns. The open-source discovery path keeps its cached scan.
func DiscoverDialectSensitivePackagesIn(repoRoot string, roots []string) (map[string][]DialectFinding, error) {
	findingsByPkg := make(map[string][]DialectFinding)
	err := walkGoFilesIn(repoRoot, roots, func(rel, abs string) error {
		if isTestSupportPackage(rel) {
			return nil
		}
		src, err := os.ReadFile(abs)
		if err != nil {
			return err
		}
		findings, err := inspectFileDialectIndicators(abs, rel, src)
		if err != nil {
			return fmt.Errorf("%s: %w", rel, err)
		}
		if len(findings) > 0 {
			pkgDir := PackageDirFromRel(rel)
			findingsByPkg[pkgDir] = append(findingsByPkg[pkgDir], findings...)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return findingsByPkg, nil
}

// walkGoFilesIn walks the given repo-relative roots and calls fn for every
// production (non-test) Go file, with the repo-relative slash path and the
// absolute path.
func walkGoFilesIn(repoRoot string, roots []string, fn func(rel, abs string) error) error {
	for _, top := range roots {
		base := filepath.Join(repoRoot, top)
		if _, err := os.Stat(base); err != nil {
			continue
		}
		err := filepath.Walk(base, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				name := info.Name()
				if name == "vendor" || name == "testdata" || name == "node_modules" {
					return filepath.SkipDir
				}
				return nil
			}
			filename := info.Name()
			if !strings.HasSuffix(filename, ".go") || strings.HasSuffix(filename, "_test.go") {
				return nil
			}
			rel, err := filepath.Rel(repoRoot, path)
			if err != nil {
				return err
			}
			return fn(filepath.ToSlash(rel), path)
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// isTestSupportPackage reports whether a repo-relative path belongs to a
// test-support package. Test-support packages hold harness code, never
// production persistence code, so they are excluded from dialect-ownership
// classification.
func isTestSupportPackage(rel string) bool {
	return strings.HasPrefix(rel, "internal/testkit/") ||
		strings.HasPrefix(rel, "internal/archtest/") ||
		strings.HasPrefix(rel, "pkg/testkit/") ||
		strings.HasPrefix(rel, "enterprise/internal/testkit/") ||
		strings.HasPrefix(rel, "enterprise/internal/archtest/")
}
