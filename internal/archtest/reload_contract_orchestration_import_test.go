package archtest

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

const importInternalConfigReload = "github.com/aiproxer/aiproxer/internal/core/configreload"

// Contract vocabulary must come from pkg/aipsdk/configreload after Task 2.2.
// Internal configreload remains allowed only for policy/algorithm symbols.
var reloadContractVocabularySelectors = map[string]bool{
	"TriggerKind":             true,
	"Trigger":                 true,
	"TriggerSIGHUP":           true,
	"TriggerAPI":              true,
	"ResultCategory":          true,
	"Result":                  true,
	"ResultPublished":         true,
	"ResultNoop":              true,
	"ResultBusy":              true,
	"ResultRestartRequired":   true,
	"ResultRetentionBlocked":  true,
	"ResultInvalid":           true,
	"ResultSourceIntegrity":   true,
	"ResultCanceled":          true,
	"ResultPreparationFailed": true,
	"ResultInternalFailed":    true,
	"AllResultCategories":     true,
	"ResultCategories":        true,
	"Status":                  true,
	"HistoryEntry":            true,
	"ReloadTrigger":           true,
	"ReloadResult":            true,
	"ReloadStatus":            true,
	"IsKnownTriggerKind":      true,
	"NormalizeResultCategory": true,
	"CloneHistory":            true,
}

// Policy/algorithm symbols that may still be selected from internal/core/configreload
// inside orchestration/observability production files.
var reloadPolicySelectors = map[string]bool{
	"SafeChange":               true,
	"ChangeDisposition":        true,
	"ChangeReloadable":         true,
	"ChangeRestartRequired":    true,
	"MaxRestartRequiredFields": true,
	"RestartRequiredError":     true,
	"Classify":                 true,
	"ClassifyEffective":        true,
	"StatusHistory":            true,
	"NewStatusHistory":         true,
	"DefaultStatusHistoryCap":  true,
	"StageRead":                true,
	"StageLoad":                true,
	"StageNoop":                true,
	"StageClassify":            true,
	"StageCompile":             true,
	"StagePrepare":             true,
	"StageRetention":           true,
	"StagePublish":             true,
	"StageRollback":            true,
	"StageShutdown":            true,
	"StageBusy":                true,
	"StageCoalesce":            true,
	"StagePanic":               true,
	"MapLoadFailure":           true,
	"MapLoadCategory":          true,
	"SanitizeConfigKey":        true,
	"SanitizeDSN":              true,
	"SanitizeURL":              true,
	"SanitizeOpaqueYAML":       true,
	"SanitizeFailure":          true,
	"SanitizePanicValue":       true,
	"RedactedPlaceholder":      true,
}

func TestReloadContract_OrchestrationImportBoundaryProhibitsInternalVocabulary(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	targets := []string{
		filepath.Join(root, "internal", "infra", "runtimehost"),
		filepath.Join(root, "internal", "infra", "metrics"),
		filepath.Join(root, "internal", "core", "diag"),
	}
	var findings []string
	for _, dir := range targets {
		err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			if !orchestrationReloadProjectionPath(rel) {
				return nil
			}
			src, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			bad, err := scanInternalReloadContractSelectors(rel, string(src))
			if err != nil {
				return err
			}
			findings = append(findings, bad...)
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", dir, err)
		}
	}
	sort.Strings(findings)
	if len(findings) > 0 {
		t.Fatalf("orchestration/observability production files must not select reload contract vocabulary from internal/core/configreload (use pkg/aipsdk/configreload); policy/algorithm selectors remain allowed:\n%s",
			strings.Join(findings, "\n"))
	}
}

func TestReloadContract_OrchestrationImportBoundaryAllowsPolicyOnly(t *testing.T) {
	t.Parallel()
	// Synthetic: policy-only internal import must not be flagged.
	src := `package runtimehost
import "github.com/aiproxer/aiproxer/internal/core/configreload"
func f() {
	_ = configreload.NewStatusHistory(1)
	_ = configreload.StagePublish
	_ = configreload.SanitizePanicValue("x")
	_ = configreload.ClassifyEffective
	var _ []configreload.SafeChange
	var _ *configreload.RestartRequiredError
}
`
	got, err := scanInternalReloadContractSelectors("internal/infra/runtimehost/policy_only.go", src)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("policy-only selectors must be allowed, got %v", got)
	}
}

func TestReloadContract_OrchestrationImportBoundaryRejectsVocabulary(t *testing.T) {
	t.Parallel()
	src := `package runtimehost
import "github.com/aiproxer/aiproxer/internal/core/configreload"
func f(t configreload.ReloadTrigger) configreload.ReloadResult {
	return configreload.ReloadResult{Category: configreload.ResultPublished}
}
`
	got, err := scanInternalReloadContractSelectors("internal/infra/runtimehost/vocab.go", src)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 {
		t.Fatal("expected vocabulary selectors to be rejected")
	}
}

// TestReloadContract_DotImportCannotBypassInternalReloadVocabularyGuard locks the
// Hermes-reproduced bypass: a dot-import of internal/core/configreload makes
// contract vocabulary appear as unqualified identifiers, which the selector
// scanner cannot attribute safely. Fail closed with one explicit finding.
func TestReloadContract_DotImportCannotBypassInternalReloadVocabularyGuard(t *testing.T) {
	t.Parallel()
	src := `package runtimehost
import . "github.com/aiproxer/aiproxer/internal/core/configreload"
func f(t ReloadTrigger) ReloadResult {
	return ReloadResult{Category: ResultPublished}
}
`
	got, err := scanInternalReloadContractSelectors("internal/infra/runtimehost/dot_bypass.go", src)
	if err != nil {
		t.Fatal(err)
	}
	want := "internal/infra/runtimehost/dot_bypass.go: forbidden dot-import of internal configreload"
	if len(got) != 1 || got[0] != want {
		t.Fatalf("dot-import bypass findings=%v want exactly [%q]", got, want)
	}
}

func TestReloadContract_BlankImportOfInternalConfigReloadForbidden(t *testing.T) {
	t.Parallel()
	src := `package runtimehost
import _ "github.com/aiproxer/aiproxer/internal/core/configreload"
func f() {}
`
	got, err := scanInternalReloadContractSelectors("internal/infra/runtimehost/blank_import.go", src)
	if err != nil {
		t.Fatal(err)
	}
	want := "internal/infra/runtimehost/blank_import.go: forbidden blank-import of internal configreload"
	if len(got) != 1 || got[0] != want {
		t.Fatalf("blank-import findings=%v want exactly [%q]", got, want)
	}
}

func TestReloadContract_UnrelatedDotImportIgnoredByReloadScanner(t *testing.T) {
	t.Parallel()
	src := `package runtimehost
import . "fmt"
func f() { Println("ok") }
`
	got, err := scanInternalReloadContractSelectors("internal/infra/runtimehost/unrelated_dot.go", src)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("unrelated package dot-import must be ignored, got %v", got)
	}
}

func TestReloadContract_RenamedPolicyOnlyInternalImportAllowed(t *testing.T) {
	t.Parallel()
	src := `package runtimehost
import cfgreload "github.com/aiproxer/aiproxer/internal/core/configreload"
func f() {
	_ = cfgreload.NewStatusHistory(1)
	_ = cfgreload.StagePublish
	_ = cfgreload.SanitizePanicValue("x")
}
`
	got, err := scanInternalReloadContractSelectors("internal/infra/runtimehost/renamed_policy.go", src)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("renamed policy-only internal import must be allowed, got %v", got)
	}
}

func TestReloadContract_RenamedVocabularyUseStillRejected(t *testing.T) {
	t.Parallel()
	src := `package runtimehost
import cfgreload "github.com/aiproxer/aiproxer/internal/core/configreload"
func f(t cfgreload.ReloadTrigger) cfgreload.ReloadResult {
	return cfgreload.ReloadResult{Category: cfgreload.ResultPublished}
}
`
	got, err := scanInternalReloadContractSelectors("internal/infra/runtimehost/renamed_vocab.go", src)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 {
		t.Fatal("renamed vocabulary selectors must still be rejected")
	}
	for _, f := range got {
		if !strings.Contains(f, "forbidden internal contract selector") &&
			!strings.Contains(f, "unrecognized internal configreload selector") {
			t.Fatalf("unexpected finding shape %q", f)
		}
	}
}

func orchestrationReloadProjectionPath(rel string) bool {
	rel = filepath.ToSlash(rel)
	switch {
	case strings.HasPrefix(rel, "internal/infra/runtimehost/"):
		return true
	case rel == "internal/infra/metrics/reload_prom.go":
		return true
	case rel == "internal/core/diag/reload_status.go":
		return true
	default:
		return false
	}
}

func scanInternalReloadContractSelectors(filename, src string) ([]string, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filename, src, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	aliases := map[string]string{} // local name -> import path
	var findings []string
	for _, imp := range f.Imports {
		path := strings.Trim(imp.Path.Value, `"`)
		name := ""
		if imp.Name != nil {
			name = imp.Name.Name
		} else {
			// Default local name is the final path segment (Go import convention).
			parts := strings.Split(path, "/")
			name = parts[len(parts)-1]
		}
		if path == importInternalConfigReload {
			// Dot/blank imports hide symbol provenance; reject fail-closed.
			// A single finding is enough: selectors cannot be attributed safely.
			switch name {
			case ".":
				findings = append(findings, filename+": forbidden dot-import of internal configreload")
				continue
			case "_":
				findings = append(findings, filename+": forbidden blank-import of internal configreload")
				continue
			}
		}
		if name == "." || name == "_" {
			// Unrelated package dot/blank imports are irrelevant to this scanner.
			continue
		}
		aliases[name] = path
	}
	var localNames []string
	for local, path := range aliases {
		if path == importInternalConfigReload {
			localNames = append(localNames, local)
		}
	}
	if len(localNames) == 0 {
		sort.Strings(findings)
		return findings, nil
	}
	localSet := map[string]bool{}
	for _, n := range localNames {
		localSet[n] = true
	}
	ast.Inspect(f, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok || !localSet[pkg.Name] {
			return true
		}
		name := sel.Sel.Name
		if reloadContractVocabularySelectors[name] {
			findings = append(findings, filename+": forbidden internal contract selector configreload."+name)
			return true
		}
		if !reloadPolicySelectors[name] {
			// Unknown selector through internal import: fail closed so new
			// contract aliases cannot sneak in without updating the allowlist.
			findings = append(findings, filename+": unrecognized internal configreload selector "+name+" (allow as policy or migrate to pkg/aipsdk/configreload)")
		}
		return true
	})
	sort.Strings(findings)
	return findings, nil
}
