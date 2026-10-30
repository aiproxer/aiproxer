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

// Task 2.3: internal/core/configreload owns policy/history/sanitization/stage
// algorithms only. Contract vocabulary types must not be declared or aliased
// there — even as exact aliases of pkg/aipsdk/configreload. Exact public
// aliases remain allowed only at pkg/aipruntime/reload_aliases.go; the
// canonical owner pkg/aipsdk/configreload remains exempt.

var forbiddenInternalOwnerContractTypeNames = []string{
	"TriggerKind",
	"ResultCategory",
	"Trigger",
	"Result",
	"Status",
	"HistoryEntry",
	"ReloadTrigger",
	"ReloadResult",
	"ReloadStatus",
}

const pathInternalCoreConfigReload = "internal/core/configreload/"

// scanInternalOwnerContractTypeDecls reports type declarations/aliases of
// forbidden contract vocabulary names. Unlike scanReloadContractSource, this
// gate does not exempt canonical SDK aliases: the internal policy package must
// not re-export the contract surface at all.
func scanInternalOwnerContractTypeDecls(filename, src string) ([]string, error) {
	rel := slashPath(filename)
	if !strings.HasPrefix(rel, pathInternalCoreConfigReload) {
		return nil, nil
	}
	if strings.HasSuffix(rel, "_test.go") {
		return nil, nil
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filename, src, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	forbidden := map[string]bool{}
	for _, name := range forbiddenInternalOwnerContractTypeNames {
		forbidden[name] = true
	}
	var findings []string
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.TYPE {
			continue
		}
		for _, spec := range gd.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok || ts.Name == nil || !forbidden[ts.Name.Name] {
				continue
			}
			kind := "type"
			if ts.Assign.IsValid() {
				kind = "type-alias"
			}
			findings = append(findings,
				rel+": "+kind+" "+ts.Name.Name+" (contract vocabulary must not be owned/aliased in internal/core/configreload)")
		}
	}
	sort.Strings(findings)
	return findings, nil
}

func TestReloadInternalOwner_ContractTypeAliasesAbsentFromProduction(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	dir := filepath.Join(root, "internal", "core", "configreload")
	var findings []string
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		rel := pathInternalCoreConfigReload + e.Name()
		src, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		got, err := scanInternalOwnerContractTypeDecls(rel, string(src))
		if err != nil {
			t.Fatalf("scan %s: %v", rel, err)
		}
		findings = append(findings, got...)
	}
	sort.Strings(findings)
	if len(findings) > 0 {
		t.Fatalf("internal/core/configreload must not declare/alias reload contract vocabulary (use pkg/aipsdk/configreload directly; public aliases only at pkg/aipruntime/reload_aliases.go):\n%s",
			strings.Join(findings, "\n"))
	}
}

func TestReloadInternalOwner_SyntheticContractAliasesRejected(t *testing.T) {
	t.Parallel()
	src := `package configreload
import sdkreload "github.com/aiproxer/aiproxer/pkg/aipsdk/configreload"
type TriggerKind = sdkreload.TriggerKind
type ResultCategory = sdkreload.ResultCategory
type Trigger = sdkreload.Trigger
type Result = sdkreload.Result
type Status = sdkreload.Status
type HistoryEntry = sdkreload.HistoryEntry
type ReloadTrigger = sdkreload.Trigger
type ReloadResult = sdkreload.Result
type ReloadStatus = sdkreload.Status
`
	got, err := scanInternalOwnerContractTypeDecls("internal/core/configreload/model.go", src)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"HistoryEntry", "ReloadResult", "ReloadStatus", "ReloadTrigger",
		"Result", "ResultCategory", "Status", "Trigger", "TriggerKind",
	}
	for _, name := range want {
		found := false
		for _, f := range got {
			if strings.Contains(f, " "+name+" ") || strings.HasSuffix(f, " "+name+" (contract vocabulary must not be owned/aliased in internal/core/configreload)") {
				found = true
				break
			}
			// Match "type-alias Name (" form
			if strings.Contains(f, "type-alias "+name+" ") || strings.Contains(f, "type "+name+" ") {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("expected finding for %s, got %v", name, got)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("findings count=%d want %d: %v", len(got), len(want), got)
	}
}

func TestReloadInternalOwner_DefinedContractTypesAlsoRejected(t *testing.T) {
	t.Parallel()
	src := `package configreload
type TriggerKind string
type ResultCategory string
type Trigger struct{}
type Result struct{}
type Status struct{}
type HistoryEntry struct{}
type ReloadTrigger struct{}
type ReloadResult struct{}
type ReloadStatus struct{}
`
	got, err := scanInternalOwnerContractTypeDecls("internal/core/configreload/mirror.go", src)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 9 {
		t.Fatalf("expected 9 defined-type findings, got %d: %v", len(got), got)
	}
}

func TestReloadInternalOwner_PolicyTypesNotFlagged(t *testing.T) {
	t.Parallel()
	src := `package configreload
type StatusHistory struct{}
type SafeChange struct{}
type RestartRequiredError struct{}
type ChangeDisposition int
const (
	StageRead = "read"
	StagePublish = "publish"
	StagePanic = "panic"
)
func NewStatusHistory(n int) *StatusHistory { return nil }
func MapLoadFailure(err error) (string, string) { return "", "" }
func Classify() {}
`
	got, err := scanInternalOwnerContractTypeDecls("internal/core/configreload/policy.go", src)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("policy/history/stage symbols must not be flagged, got %v", got)
	}
}

func TestReloadInternalOwner_OutsideInternalPackageNotScanned(t *testing.T) {
	t.Parallel()
	src := `package aipruntime
import sdkreload "github.com/aiproxer/aiproxer/pkg/aipsdk/configreload"
type TriggerKind = sdkreload.TriggerKind
type ResultCategory = sdkreload.ResultCategory
type ReloadTrigger = sdkreload.Trigger
type ReloadResult = sdkreload.Result
type HistoryEntry = sdkreload.HistoryEntry
type ReloadStatus = sdkreload.Status
`
	got, err := scanInternalOwnerContractTypeDecls("pkg/aipruntime/reload_aliases.go", src)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("public facade aliases must remain allowed (gate scoped to internal owner), got %v", got)
	}
}

func TestReloadInternalOwner_CanonicalSDKPackageNotScanned(t *testing.T) {
	t.Parallel()
	src := `package configreload
type TriggerKind string
type ResultCategory string
type Trigger struct{}
type Result struct{}
type Status struct{}
type HistoryEntry struct{}
`
	got, err := scanInternalOwnerContractTypeDecls("pkg/aipsdk/configreload/contract.go", src)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("canonical SDK owner remains exempt from this gate, got %v", got)
	}
}
