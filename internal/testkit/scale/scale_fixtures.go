package scale

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/aiproxer/aiproxer/internal/standardplugins"
	"github.com/aiproxer/aiproxer/internal/testkit/gitscope"
)

// CreateDeterministicGitChange creates a temporary repository, commits before,
// applies after, and returns Git's own unified diff. Callers therefore test the
// same diff representation used by change-surface tooling rather than a hand-built string.
func CreateDeterministicGitChange(repoRoot, relativePath, before, after string) (string, error) {
	path := filepath.Join(repoRoot, filepath.FromSlash(relativePath))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(before), 0o644); err != nil {
		return "", err
	}
	git := func(args ...string) ([]byte, error) {
		cmd := exec.Command("git", args...)
		cmd.Dir = repoRoot
		// repoRoot is a caller-supplied fixture repository. Git exports GIT_DIR to
		// every hook it runs, so an inherited GIT_DIR initialises and commits inside
		// the ambient repository instead of the fixture, destroying its index and refs.
		cmd.Env = gitscope.Environ()
		return cmd.CombinedOutput()
	}
	if out, err := git("init", "--quiet"); err != nil {
		return "", fmt.Errorf("git init: %w: %s", err, out)
	}
	for _, args := range [][]string{{"config", "user.email", "scale@example.invalid"}, {"config", "user.name", "scale-fixture"}, {"config", "commit.gpgsign", "false"}} {
		if out, err := git(args...); err != nil {
			return "", fmt.Errorf("git config: %w: %s", err, out)
		}
	}
	if out, err := git("add", "-f", "--", relativePath); err != nil {
		return "", fmt.Errorf("git add baseline: %w: %s", err, out)
	}
	if out, err := git("commit", "--quiet", "-m", "baseline"); err != nil {
		return "", fmt.Errorf("git commit baseline: %w: %s", err, out)
	}
	if err := os.WriteFile(path, []byte(after), 0o644); err != nil {
		return "", err
	}
	out, err := git("diff", "HEAD", "--no-ext-diff", "--unified=3", "--", relativePath)
	if err != nil {
		return "", fmt.Errorf("git diff: %w: %s", err, out)
	}
	if len(out) == 0 {
		return "", fmt.Errorf("git produced an empty diff for %s", relativePath)
	}
	return string(out), nil
}

// SyntheticFrontendFixture represents a test frontend definition.
type SyntheticFrontendFixture struct {
	ID      string
	Profile string
}

// SyntheticProviderProfileFixture represents a test provider profile definition bound to a backend family.
type SyntheticProviderProfileFixture struct {
	ID       string
	FamilyID string
	Endpoint string
}

// FiveFrontendsFixture returns a standard set of 5 synthetic frontend fixtures.
func FiveFrontendsFixture() []SyntheticFrontendFixture {
	return []SyntheticFrontendFixture{
		{ID: "fe-openai-responses", Profile: "2026-04-24"},
		{ID: "fe-openai-legacy", Profile: "legacy"},
		{ID: "fe-anthropic", Profile: "v1"},
		{ID: "fe-gemini", Profile: "v1beta"},
		{ID: "fe-openresponses", Profile: "2026-04-24"},
	}
}

// ThousandProviderProfilesFixture returns 1,000 synthetic provider profiles bound across 5 backend families.
func ThousandProviderProfilesFixture() []SyntheticProviderProfileFixture {
	families := []string{
		"openai-responses-compatible",
		"openai-legacy-compatible",
		"anthropic-compatible",
		"openresponses-compatible",
		"systemone-compatible",
	}
	profiles := make([]SyntheticProviderProfileFixture, 1000)
	for i := range 1000 {
		family := families[i%len(families)]
		profiles[i] = SyntheticProviderProfileFixture{
			ID:       fmt.Sprintf("provider-profile-%04d", i+1),
			FamilyID: family,
			Endpoint: fmt.Sprintf("https://api.provider-%04d.example.com/v1", i+1),
		}
	}
	return profiles
}

// ValidateNonCartesianFixture proves the actual deterministic fixture has
// independent frontend and profile dimensions. Source is generated from the
// supplied fixture data, so callers cannot substitute an unrelated snippet as
// scalability evidence.
func ValidateNonCartesianFixture(frontends []SyntheticFrontendFixture, profiles []SyntheticProviderProfileFixture) error {
	if len(frontends) != 5 || len(profiles) != 1000 {
		return fmt.Errorf("fixture dimensions are %d frontends x %d profiles, want 5 x 1000", len(frontends), len(profiles))
	}
	if profiles[999].ID != "provider-profile-1000" {
		return fmt.Errorf("profile #1000 is not deterministic: %q", profiles[999].ID)
	}
	families := make(map[string]bool)
	var source strings.Builder
	source.WriteString("package generatedfixture\n\nvar frontendIDs = []string{\n")
	for _, frontend := range frontends {
		if frontend.ID == "" {
			return fmt.Errorf("frontend has incomplete identity: %+v", frontend)
		}
		fmt.Fprintf(&source, "%q,\n", frontend.ID)
	}
	source.WriteString("}\n\nvar profiles = []struct{ ID, Family, Endpoint string }{\n")
	for _, profile := range profiles {
		if profile.ID == "" || profile.FamilyID == "" || profile.Endpoint == "" {
			return fmt.Errorf("profile has incomplete identity: %+v", profile)
		}
		families[profile.FamilyID] = true
		fmt.Fprintf(&source, "{ID: %q, Family: %q, Endpoint: %q},\n", profile.ID, profile.FamilyID, profile.Endpoint)
	}
	source.WriteString("}\n\nvar profileFamilyBindings = map[string]string{\n")
	for family := range families {
		fmt.Fprintf(&source, "%q: %q,\n", family, family)
	}
	source.WriteString("}\n")
	if len(families) != 5 {
		return fmt.Errorf("profiles resolve to %d families, want 5", len(families))
	}
	findings, err := scanSource("generated-scale-fixture.go", []byte(source.String()))
	if err != nil {
		return fmt.Errorf("scan generated scale fixture: %w", err)
	}
	for _, finding := range findings {
		if finding.Category == DebtFrontendProfilePairs || finding.Category == DebtFrontendBackendPairs || finding.Category == DebtNestedPairMaterializer || finding.Category == DebtPerProfileFactory || finding.Category == DebtPerProfileRegistration || finding.Category == DebtPerProfileGoroutine || finding.Category == DebtCentralListMutation || finding.Category == DebtSentinelGrowth {
			return fmt.Errorf("generated non-Cartesian fixture contains %s: %s", finding.Category, finding.Detail)
		}
	}
	return nil
}

// RealSharedBoundaryInspector inspects real repository file paths and symbols
// to verify that provider profile changes do not mutate shared core, API, proto, or central tables.
type RealSharedBoundaryInspector struct{}

// SharedBoundaryFootprint contains actual change counts across repository zones.
type SharedBoundaryFootprint struct {
	ProfileID              string
	SharedCoreEdits        int
	SharedAPIEdits         int
	ProtoEdits             int
	CentralTableEdits      int
	EssentialListAdditions int
	SentinelPairGrowth     int
	StructuralViolations   []string
}

// ValidateZeroSharedFootprint asserts that a provider profile addition makes zero shared boundary edits.
func (f SharedBoundaryFootprint) ValidateZeroSharedFootprint() error {
	if f.SharedCoreEdits > 0 {
		return fmt.Errorf("profile %s mutated shared core (%d edits)", f.ProfileID, f.SharedCoreEdits)
	}
	if f.SharedAPIEdits > 0 {
		return fmt.Errorf("profile %s mutated pkg/aipapi (%d edits)", f.ProfileID, f.SharedAPIEdits)
	}
	if f.ProtoEdits > 0 {
		return fmt.Errorf("profile %s mutated backend.proto (%d edits)", f.ProfileID, f.ProtoEdits)
	}
	if f.CentralTableEdits > 0 {
		return fmt.Errorf("profile %s mutated central tables (%d edits)", f.ProfileID, f.CentralTableEdits)
	}
	if f.EssentialListAdditions > 0 {
		return fmt.Errorf("profile %s added entries to essential backend list (%d additions)", f.ProfileID, f.EssentialListAdditions)
	}
	if f.SentinelPairGrowth > 0 {
		return fmt.Errorf("profile %s caused sentinel pair growth (%d new pairs)", f.ProfileID, f.SentinelPairGrowth)
	}
	if len(f.StructuralViolations) > 0 {
		return fmt.Errorf("profile %s introduced prohibited structures: %s", f.ProfileID, strings.Join(f.StructuralViolations, "; "))
	}
	return nil
}

// InspectFileChanges analyzes a set of modified file paths for a provider-profile change
// and measures real shared-boundary footprint across actual repository packages.
func (r RealSharedBoundaryInspector) InspectFileChanges(profileID string, modifiedFiles []string, sentinelPairGrowth int) SharedBoundaryFootprint {
	footprint := SharedBoundaryFootprint{
		ProfileID:          profileID,
		SentinelPairGrowth: sentinelPairGrowth,
	}

	for _, file := range modifiedFiles {
		norm := filepath.ToSlash(file)
		if strings.HasPrefix(norm, "internal/core/") {
			footprint.SharedCoreEdits++
		}
		if strings.HasPrefix(norm, "pkg/aipapi/") {
			footprint.SharedAPIEdits++
		}
		if strings.Contains(norm, "backend.proto") {
			footprint.ProtoEdits++
		}
		if strings.HasPrefix(norm, "internal/standardplugins/") || strings.HasPrefix(norm, "internal/pluginreg/") {
			footprint.CentralTableEdits++
		}
	}

	return footprint
}

// InspectDiff parses complete unified diffs. It reconstructs the new file from
// the hunk body (using the repository version for omitted context), so additions
// in new files and partial hunks are checked structurally rather than by path text.
func (r RealSharedBoundaryInspector) InspectDiff(repoRoot, diff string) (SharedBoundaryFootprint, error) {
	type hunk struct {
		path string
		old  strings.Builder
		add  strings.Builder
	}
	var hunks []hunk
	var current *hunk
	flush := func() {
		if current != nil {
			hunks = append(hunks, *current)
		}
	}
	for line := range strings.SplitSeq(strings.ReplaceAll(diff, "\r\n", "\n"), "\n") {
		switch {
		case strings.HasPrefix(line, "+++ "):
			flush()
			path := strings.TrimPrefix(line, "+++ ")
			path = strings.TrimPrefix(path, "b/")
			if path == "/dev/null" {
				current = nil
				continue
			}
			current = &hunk{path: path}
		case current != nil && strings.HasPrefix(line, "@@"):
			// Hunk headers are consumed for validation/documentation; line order is
			// sufficient because complete files are accepted as additions.
		case current != nil && strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++"):
			current.add.WriteString(strings.TrimPrefix(line, "+"))
			current.add.WriteByte('\n')
		case current != nil && strings.HasPrefix(line, " "):
			current.old.WriteString(strings.TrimPrefix(line, " "))
			current.old.WriteByte('\n')
		}
	}
	flush()
	if len(hunks) == 0 {
		return SharedBoundaryFootprint{}, fmt.Errorf("diff contains no file headers")
	}
	footprint := SharedBoundaryFootprint{ProfileID: "diff-profile"}
	for _, h := range hunks {
		norm := filepath.ToSlash(h.path)
		if strings.HasPrefix(norm, "internal/core/") {
			footprint.SharedCoreEdits++
		}
		if strings.HasPrefix(norm, "pkg/aipapi/") {
			footprint.SharedAPIEdits++
		}
		if strings.Contains(norm, "backend.proto") {
			footprint.ProtoEdits++
		}
		if strings.HasPrefix(norm, "internal/standardplugins/") || strings.HasPrefix(norm, "internal/pluginreg/") {
			footprint.CentralTableEdits++
		}
		if !strings.HasSuffix(strings.ToLower(norm), ".go") {
			continue
		}
		source := h.add.String()
		if existing, err := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(norm))); err == nil {
			// A full-file addition is authoritative. For a partial hunk, preserve
			// the existing declaration context so the AST remains parseable.
			if !strings.Contains(source, "package ") || !strings.Contains(source, "func ") && !strings.Contains(source, "var ") && !strings.Contains(source, "type ") {
				source = string(existing) + "\n" + source
			}
		}
		if strings.TrimSpace(source) == "" {
			continue
		}
		findings, err := scanSource(norm, []byte(source))
		if err != nil {
			return footprint, fmt.Errorf("scan reconstructed Go content for %s: %w", norm, err)
		}
		for _, finding := range findings {
			footprint.StructuralViolations = append(footprint.StructuralViolations, finding.Category+": "+finding.Detail)
			switch finding.Category {
			case DebtCentralListMutation:
				footprint.CentralTableEdits++
			case DebtSentinelGrowth:
				footprint.SentinelPairGrowth++
			}
		}
	}
	return footprint, nil
}

// InspectProfileAgainstCentralLists inspects actual central backend lists in standardplugins
// to ensure a given profile ID is not registered as a separate essential backend kind.
func (r RealSharedBoundaryInspector) InspectProfileAgainstCentralLists(profileID string) SharedBoundaryFootprint {
	footprint := SharedBoundaryFootprint{
		ProfileID: profileID,
	}
	for _, essential := range standardplugins.EssentialBackendKinds() {
		if essential == profileID {
			footprint.EssentialListAdditions++
		}
	}
	return footprint
}

// CartesianDebtFinding is one structurally identified scalability debt.
type CartesianDebtFinding struct {
	Category string
	File     string
	Detail   string
}

const (
	DebtFrontendBackendPairs   = "frontend_backend_pairs"
	DebtFrontendProfilePairs   = "frontend_profile_pairs"
	DebtNestedPairMaterializer = "nested_pair_materializer"
	DebtPerProfileFactory      = "per_profile_factory"
	DebtPerProfileRegistration = "per_profile_registration"
	DebtPerProfileGoroutine    = "per_profile_goroutine"
	DebtCentralListMutation    = "central_list_mutation"
	DebtSentinelGrowth         = "sentinel_growth"
)

// CartesianDebtReport contains evidence of current FE x BE debt and provider-profile structures.
type CartesianDebtReport struct {
	TotalCells               int
	AllCellsCallsDiscovered  int
	NestedLoopsDiscovered    int
	CentralTableEntriesCount int
	FrontendBackendPairs     int
	FrontendProfilePairs     int
	NestedPairMaterializers  int
	PerProfileFactories      int
	PerProfileRegistrations  int
	PerProfileGoroutines     int
	CentralListMutations     int
	SentinelGrowth           int
	Findings                 []CartesianDebtFinding
}

func identName(expr ast.Expr) string {
	switch x := expr.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.SelectorExpr:
		return identName(x.X) + "." + x.Sel.Name
	case *ast.CallExpr:
		return identName(x.Fun)
	default:
		return ""
	}
}

func containsTerm(name string, terms ...string) bool {
	lower := strings.ToLower(name)
	for _, term := range terms {
		if strings.Contains(lower, strings.ToLower(term)) {
			return true
		}
	}
	return false
}

func dimensionName(name string) bool {
	return containsTerm(name, "frontend", "backend", "profile", "provider", "family")
}

func identityCollection(name string) bool {
	return dimensionName(name) || containsTerm(name, "cells", "pairs", "matrix", "sentinel", "registration", "factory")
}

func appendFindings(dst *[]CartesianDebtFinding, src ...CartesianDebtFinding) {
	seen := make(map[string]bool, len(*dst))
	for _, f := range *dst {
		seen[f.Category+"|"+f.File+"|"+f.Detail] = true
	}
	for _, f := range src {
		key := f.Category + "|" + f.File + "|" + f.Detail
		if !seen[key] {
			*dst = append(*dst, f)
			seen[key] = true
		}
	}
}

// scanSource performs semantic AST inspection and reports only pair/factory/list
// structures tied to frontend, backend, profile, or provider identities.
func scanSource(path string, source []byte) ([]CartesianDebtFinding, error) {
	file, err := parser.ParseFile(token.NewFileSet(), path, source, parser.ParseComments)
	if err != nil {
		return nil, err
	}
	var findings []CartesianDebtFinding
	add := func(category, detail string) {
		appendFindings(&findings, CartesianDebtFinding{Category: category, File: path, Detail: detail})
	}
	var visit func(ast.Node, []string)
	visit = func(node ast.Node, ranges []string) {
		ast.Inspect(node, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.ValueSpec:
				for i, name := range x.Names {
					if i >= len(x.Values) {
						continue
					}
					dims := append(expressionDimensions(x.Values[i]), strings.ToLower(name.Name))
					if len(dims) >= 2 && hasDimensionPair(dims, "frontend", "profile", "provider") {
						add(DebtFrontendProfilePairs, name.Name+" materializes frontend/profile dimensions")
					}
					if len(dims) >= 2 && hasDimensionPair(dims, "frontend", "backend") {
						add(DebtFrontendBackendPairs, name.Name+" materializes frontend/backend dimensions")
					}
					if containsTerm(name.Name, "sentinel") && (containsTerm(name.Name, "count", "member", "pair") || len(dims) > 0) {
						add(DebtSentinelGrowth, name.Name+" changes sentinel membership or count")
					}
				}
			case *ast.CallExpr:
				name := identName(x.Fun)
				if name == "AllCells" {
					add(DebtFrontendBackendPairs, "AllCells materializes the frontend/backend product")
				}
				if name == "make" && len(x.Args) >= 3 {
					dims := expressionDimensions(x.Args[2])
					if hasDimensionPair(dims, "frontend", "profile", "provider") {
						add(DebtFrontendProfilePairs, "make capacity multiplies frontend and profile dimensions")
					}
					if hasDimensionPair(dims, "frontend", "backend") {
						add(DebtFrontendBackendPairs, "make capacity multiplies frontend and backend dimensions")
					}
				}
				if name == "append" && len(x.Args) > 1 && (containsTerm(identName(x.Args[0]), "pair", "cell", "sentinel") || containsTerm(identName(x.Args[1]), "frontend", "backend", "profile", "provider")) {
					add(DebtNestedPairMaterializer, "append grows a pair/sentinel collection")
				}
				if len(ranges) > 0 {
					for _, r := range ranges {
						if !containsTerm(r, "profile", "provider") {
							continue
						}
						switch {
						case containsTerm(name, "register"):
							add(DebtPerProfileRegistration, "registration called while ranging over "+r)
						case containsTerm(name, "factory", "newbackend", "newprovider"):
							add(DebtPerProfileFactory, "factory called while ranging over "+r)
						}
					}
				}
			case *ast.RangeStmt:
				collection := exprText(x.X)
				varName := exprText(x.Value)
				if varName == "" {
					varName = exprText(x.Key)
				}
				next := append(append([]string(nil), ranges...), varName+"="+collection)
				if collection == "AllCells" {
					add(DebtFrontendBackendPairs, "range over AllCells")
				}
				if hasDimensionPair(expressionDimensions(x.X), "frontend", "profile", "provider") {
					add(DebtFrontendProfilePairs, "range over frontend/profile collection")
				}
				if hasDimensionPair(expressionDimensions(x.X), "frontend", "backend") {
					add(DebtFrontendBackendPairs, "range over frontend/backend collection")
				}
				if len(ranges) > 0 {
					for _, r := range ranges {
						if containsTerm(r, "frontend") && containsTerm(collection, "backend") || containsTerm(r, "backend") && containsTerm(collection, "frontend") {
							add(DebtNestedPairMaterializer, "nested frontend/backend pair materialization")
						}
						if containsTerm(r, "frontend") && containsTerm(collection, "profile", "provider") || containsTerm(r, "profile", "provider") && containsTerm(collection, "frontend") {
							add(DebtNestedPairMaterializer, "nested frontend/profile pair materialization")
						}
						if containsTerm(r, "frontend") && containsTerm(collection, "profile", "provider") {
							add(DebtFrontendProfilePairs, "nested frontend/profile materialization")
						}
						if containsTerm(r, "frontend") && containsTerm(collection, "backend") {
							add(DebtFrontendBackendPairs, "nested frontend/backend materialization")
						}
					}
				}
				visit(x.Body, next)
				return false
			case *ast.ForStmt:
				if hasDimensionPair(expressionDimensions(x.Cond), "frontend", "profile", "provider") || hasDimensionPair(expressionDimensions(x.Cond), "frontend", "backend") {
					add(DebtNestedPairMaterializer, "indexed loop spans frontend/backend or profile dimensions")
				}
			case *ast.GoStmt:
				if containsTerm(identName(x.Call.Fun), "provider", "profile", "backend") {
					add(DebtPerProfileGoroutine, "provider/profile activation starts a goroutine")
				}
			}
			return true
		})
	}
	visit(file, nil)
	if containsTerm(path, "standardplugins", "pluginreg") && hasASTName(file, "EssentialBackendKinds", "CompatibleBackendKinds") {
		add(DebtCentralListMutation, "authoritative essential/compatible backend list")
	}
	if containsTerm(path, "sentinel", "integration") && hasASTName(file, "Sentinel", "sentinel") {
		add(DebtSentinelGrowth, "sentinel declaration or membership changed")
	}
	return findings, nil
}

func exprText(expr ast.Expr) string {
	switch x := expr.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.SelectorExpr:
		return exprText(x.X) + "." + x.Sel.Name
	case *ast.CallExpr:
		return identName(x.Fun)
	case *ast.BasicLit:
		return x.Value
	}
	return ""
}

func expressionDimensions(expr ast.Expr) []string {
	if expr == nil {
		return nil
	}
	var dims []string
	ast.Inspect(expr, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && dimensionName(id.Name) {
			dims = append(dims, strings.ToLower(id.Name))
		}
		return true
	})
	return dims
}

func hasDimensionPair(dims []string, first string, rest ...string) bool {
	hasFirst := false
	for _, dim := range dims {
		if containsTerm(dim, first) {
			hasFirst = true
		}
		for _, other := range rest {
			if containsTerm(dim, other) && hasFirst {
				return true
			}
		}
	}
	return false
}

func hasASTName(file *ast.File, names ...string) bool {
	found := false
	ast.Inspect(file, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok {
			for _, name := range names {
				if id.Name == name {
					found = true
				}
			}
		}
		return !found
	})
	return found
}

// ScanRepository inspects source that actually exists under repoRoot. It does
// not accept a fabricated path list as evidence.
func ScanRepository(repoRoot string) (CartesianDebtReport, error) {
	report := CartesianDebtReport{}
	err := filepath.Walk(repoRoot, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.Contains(filepath.ToSlash(path), "/vendor/") {
			return nil
		}
		source, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		findings, err := scanSource(path, source)
		if err != nil {
			return err
		}
		for _, finding := range findings {
			report.Findings = append(report.Findings, finding)
			switch finding.Category {
			case DebtFrontendBackendPairs:
				report.FrontendBackendPairs++
				report.AllCellsCallsDiscovered++
			case DebtFrontendProfilePairs:
				report.FrontendProfilePairs++
			case DebtNestedPairMaterializer:
				report.NestedPairMaterializers++
				report.NestedLoopsDiscovered++
			case DebtPerProfileFactory:
				report.PerProfileFactories++
			case DebtPerProfileRegistration:
				report.PerProfileRegistrations++
			case DebtPerProfileGoroutine:
				report.PerProfileGoroutines++
			case DebtCentralListMutation:
				report.CentralListMutations++
			case DebtSentinelGrowth:
				report.SentinelGrowth++
			}
		}
		return nil
	})
	if err != nil {
		return report, err
	}
	if _, err := os.Stat(filepath.Join(repoRoot, "internal", "standardplugins")); err == nil {
		report.CentralTableEntriesCount = len(standardplugins.EssentialBackendKinds())
	}
	return report, nil
}

func ScanCurrentCartesianDebt(repoRoot string) (CartesianDebtReport, error) {
	return ScanRepository(repoRoot)
}
