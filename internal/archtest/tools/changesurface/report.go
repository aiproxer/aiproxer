// Package changesurface reports extension blast radius from Git paths.
// Classification is path-only and deterministic so it is safe on Windows and
// in CI without depending on checkout-specific absolute paths.
package changesurface

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

type Category string

const (
	ExtensionOwnedProduction Category = "extension-owned-production"
	ProviderProfileData      Category = "provider-profile-data"
	SharedComposition        Category = "shared-composition"
	CanonicalContract        Category = "canonical-contract"
	CoreRoutingRuntime       Category = "core-routing-runtime"
	BackendPluginABI         Category = "backendplugin-abi"
	Generated                Category = "generated"
	TestsReference           Category = "tests-reference"
	DocsSpec                 Category = "docs-spec"
	Other                    Category = "other"
)

var categoryOrder = []Category{
	ExtensionOwnedProduction, ProviderProfileData, SharedComposition,
	CanonicalContract, CoreRoutingRuntime, BackendPluginABI, Generated,
	TestsReference, DocsSpec, Other,
}

// Report is the stable machine-readable change-surface result.
type Report struct {
	Paths  map[Category][]string `json:"paths"`
	Counts map[Category]int      `json:"counts"`
}

// HasCodeChanges reports whether the diff needs the repository code QA job.
// Documentation, dedicated evidence, and generated artifacts are intentionally
// excluded; every other category is a code-surface change, including profile
// data, because profile validation remains part of the code QA contract.
func (r Report) HasCodeChanges() bool {
	for category, count := range r.Counts {
		if count == 0 {
			continue
		}
		switch category {
		case DocsSpec, TestsReference, Generated:
			continue
		default:
			return true
		}
	}
	return false
}

// IsProfileDataOnly reports whether the diff contains profile data and no
// protected production or unknown paths. It is the single policy used by CI
// and local profile-only checks.
func (r Report) IsProfileDataOnly() bool {
	return r.Counts[ProviderProfileData] > 0 && r.ValidateProfileOnly() == nil
}

// CIOutputs returns the stable key/value contract consumed by GitHub Actions.
// Keeping this derivation next to path classification prevents shell workflows
// from growing a second architecture policy.
func (r Report) CIOutputs() map[string]string {
	return map[string]string{
		"code":    fmt.Sprintf("%t", r.HasCodeChanges()),
		"profile": fmt.Sprintf("%t", r.IsProfileDataOnly()),
	}
}

func ClassifyPath(path string) Category {
	return classifyNormalizedPath(normalizePath(path))
}

func classifyNormalizedPath(path string) Category {
	lower := strings.ToLower(path)
	if isGenerated(lower) {
		return Generated
	}
	// Dedicated evidence/reference namespaces are explicit exceptions. Do not
	// let a generic .md suffix or fixture path hide a protected production zone.
	if isDedicatedTestOrReference(lower) {
		if cat := domainBoundaryCategory(lower); cat != Other {
			return cat
		}
		return TestsReference
	}
	if cat := domainBoundaryCategory(lower); cat != Other {
		return cat
	}
	if isDocs(lower) {
		return DocsSpec
	}
	if strings.HasSuffix(lower, "_test.go") {
		return TestsReference
	}
	if isProfile(lower) {
		return ProviderProfileData
	}
	// Unknown paths are deliberately fail-closed in ValidateProfileOnly. Do
	// not silently treat an unclassified production file as profile evidence.
	return Other
}

func domainBoundaryCategory(path string) Category {
	switch {
	case strings.HasPrefix(path, "pkg/aipapi/"):
		return CanonicalContract
	case strings.HasPrefix(path, "internal/core/"):
		return CoreRoutingRuntime
	case strings.HasPrefix(path, "api/backendplugin/"), strings.HasPrefix(path, "pkg/aipsdk/backendplugin/"):
		return BackendPluginABI
	case isSharedComposition(path):
		return SharedComposition
	case isExtensionOwned(path):
		return ExtensionOwnedProduction
	default:
		return Other
	}
}

func Build(paths []string) Report {
	r := Report{Paths: make(map[Category][]string), Counts: make(map[Category]int)}
	seen := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		path = normalizePath(path)
		if path == "" {
			continue
		}
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		category := classifyNormalizedPath(path)
		r.Paths[category] = append(r.Paths[category], path)
		r.Counts[category]++
	}
	for _, paths := range r.Paths {
		sort.Strings(paths)
	}
	return r
}

func (r Report) ValidateProfileOnly() error {
	// The allowlist is intentionally narrow: evidence/docs/profile data are
	// tolerated, while every production zone and every unknown path fails
	// closed until it receives an explicit classification.
	for _, category := range []Category{SharedComposition, CanonicalContract, CoreRoutingRuntime, BackendPluginABI, ExtensionOwnedProduction, Other} {
		if r.Counts[category] != 0 {
			return fmt.Errorf("profile-only change has forbidden %s paths: %v", category, r.Paths[category])
		}
	}
	return nil
}

// ValidateProfileOnlyPaths is useful to architecture fixtures that have a
// synthetic diff but do not need to construct an entire Report first.
func ValidateProfileOnlyPaths(paths []string) error {
	return Build(paths).ValidateProfileOnly()
}

func (r Report) JSON() ([]byte, error) {
	return json.MarshalIndent(r, "", "  ")
}

func FormatHuman(r Report) string {
	var b strings.Builder
	b.WriteString("Extension change-surface report\n")
	for _, category := range categoryOrder {
		fmt.Fprintf(&b, "%-29s %d\n", category+":", r.Counts[category])
	}
	if err := r.ValidateProfileOnly(); err != nil {
		b.WriteString("profile-only: FAIL (shared/extension boundary edits detected)\n")
		fmt.Fprintf(&b, "reason: %v\n", err)
	} else {
		b.WriteString("profile-only: PASS (generated/dedicated tests/reference breadth is not coupling)\n")
	}
	return b.String()
}

// ParsePorcelainZ accepts `git status --porcelain=v1 -z` records. Rename
// records contain the new/target path in record[2:] followed by the old/source
// path in the next NUL-delimited field. Both are retained so a rename cannot
// hide a boundary edit.
func ParsePorcelainZ(data []byte) ([]string, error) {
	fields := strings.Split(string(data), "\x00")
	var paths []string
	for i := 0; i < len(fields); i++ {
		record := fields[i]
		if record == "" {
			continue
		}
		if len(record) < 3 {
			return nil, fmt.Errorf("invalid porcelain record %q", record)
		}
		status := record[:2]
		path := strings.TrimPrefix(record[2:], " ")
		if strings.Contains(status, "R") || strings.Contains(status, "C") {
			if i+1 >= len(fields) || fields[i+1] == "" {
				return nil, fmt.Errorf("rename/copy record %q has no source path", record)
			}
			newPath := normalizePath(path)
			oldPath := normalizePath(fields[i+1])
			paths = append(paths, newPath, oldPath)
			i++
			continue
		}
		paths = append(paths, normalizePath(path))
	}
	sort.Strings(paths)
	return paths, nil
}

// normalizePath canonicalizes repository-relative paths. Git porcelain and
// `git diff --name-only` already return repository-relative paths, so a leading
// `a/` or `b/` is a real directory name here and must not be stripped.
func normalizePath(path string) string {
	path = strings.ReplaceAll(path, "\\", "/")
	return strings.TrimPrefix(path, "./")
}

// NormalizeUnifiedDiffPath removes the marker prefix from a unified-diff
// header. It is intentionally separate from repository path normalization:
// porcelain and name-only paths may legitimately begin with a/ or b/.
func NormalizeUnifiedDiffPath(path string) string {
	path = normalizePath(path)
	if path, ok := strings.CutPrefix(path, "a/"); ok {
		return path
	}
	if path, ok := strings.CutPrefix(path, "b/"); ok {
		return path
	}
	return path
}

func isGenerated(path string) bool {
	return strings.HasPrefix(path, "generated/") ||
		strings.HasPrefix(path, "gen/") ||
		strings.Contains(path, "/generated/") ||
		strings.Contains(path, "/gen/") ||
		strings.HasSuffix(path, ".gen.go") ||
		strings.HasSuffix(path, ".pb.go") ||
		strings.HasSuffix(path, "_generated.go")
}

func isDocs(path string) bool {
	return strings.HasPrefix(path, "docs/") || strings.HasPrefix(path, ".kiro/") || strings.HasPrefix(path, ".github/") || strings.HasSuffix(path, ".md")
}

func isDedicatedTestOrReference(path string) bool {
	return strings.HasPrefix(path, "internal/archtest/") ||
		strings.HasPrefix(path, "internal/qa/") ||
		strings.HasPrefix(path, "internal/testkit/") ||
		strings.HasPrefix(path, "internal/refclient/") ||
		strings.HasPrefix(path, "internal/refbackend/") ||
		strings.HasPrefix(path, "internal/ref") ||
		strings.HasPrefix(path, "testdata/") ||
		strings.HasPrefix(path, "fixtures/") ||
		strings.Contains(path, "/testdata/") ||
		strings.Contains(path, "/fixtures/")
}

func isProfile(path string) bool {
	return isProfileDataPath(path, "provider-profiles/") ||
		path == "internal/providerprofiles/catalog.json" ||
		isProfileDataPath(path, "internal/providerprofiles/catalog/")
}

func isProfileDataPath(path, prefix string) bool {
	if !strings.HasPrefix(path, prefix) {
		return false
	}
	return strings.HasSuffix(path, ".json") || strings.HasSuffix(path, ".yaml") || strings.HasSuffix(path, ".yml")
}

func isSharedComposition(path string) bool {
	return path == "makefile" || strings.HasPrefix(path, "internal/standardplugins/") || strings.HasPrefix(path, "internal/pluginreg/") || strings.HasPrefix(path, "internal/infra/runtimebundle/") || strings.HasPrefix(path, "internal/stdhttp/") || strings.HasPrefix(path, "cmd/aipstd/")
}

func isExtensionOwned(path string) bool {
	return strings.HasPrefix(path, "internal/plugins/frontends/") || strings.HasPrefix(path, "internal/plugins/backends/") || strings.HasPrefix(path, "internal/plugins/features/") || strings.HasPrefix(path, "connectors/") || strings.HasPrefix(path, "connector-support/")
}
