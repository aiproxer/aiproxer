package archtest

import (
	"fmt"
	"go/ast"
	"go/token"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const (
	betterLeaksModulePath  = "github.com/betterleaks/betterleaks/v2"
	secretGuardFeaturePath = "internal/plugins/features/secretguard"
)

func TestBetterLeaksArchitectureRatchetsRejectSyntheticViolations(t *testing.T) {
	t.Parallel()
	const betterLeaksModule = betterLeaksModulePath
	tests := []struct {
		name   string
		rel    string
		source string
		want   string
	}{
		{
			name: "subprocess import in production request path",
			rel:  "internal/plugins/features/secretguard/request_scan.go",
			source: `package secretguard

import "os/exec"

var _ = exec.Command
`,
			want: "process boundary",
		},
		{
			name: "analysis import",
			rel:  "internal/plugins/features/secretguard/request_scan.go",
			source: `package secretguard

import _ "` + betterLeaksModule + `/analyze"
`,
			want: "analysis",
		},
		{
			name: "pipeline import",
			rel:  "internal/plugins/features/secretguard/request_scan.go",
			source: `package secretguard

import _ "` + betterLeaksModule + `/pipeline"
`,
			want: "pipeline",
		},
		{
			name: "provider validation import",
			rel:  "internal/plugins/features/secretguard/request_scan.go",
			source: `package secretguard

import _ "` + betterLeaksModule + `/credential"
`,
			want: "provider validation",
		},
		{
			name: "source orchestration subpackage import",
			rel:  "internal/plugins/features/secretguard/request_scan.go",
			source: `package secretguard

import _ "` + betterLeaksModule + `/sources/github"
`,
			want: "source orchestration",
		},
		{
			name: "source auto discovery",
			rel:  "internal/plugins/features/secretguard/request_scan.go",
			source: `package secretguard

import blsources "` + betterLeaksModule + `/sources"

var _ = blsources.Auto
`,
			want: "source orchestration",
		},
		{
			name: "concrete type outside private adapter",
			rel:  "internal/plugins/features/secretguard/request_scan.go",
			source: `package secretguard

import "` + betterLeaksModule + `/report"

var _ report.Finding
`,
			want: "private adapter boundary",
		},
		{
			name: "concrete type in public sdk",
			rel:  "pkg/aipsdk/secretguard.go",
			source: `package aipsdk

import "` + betterLeaksModule + `/scan"

var _ *scan.Scanner
`,
			want: "private adapter boundary",
		},
		{
			name: "allow signature field",
			rel:  "internal/plugins/features/secretguard/config.go",
			source: `package secretguard

type Config struct {
	AllowSignatures []string
}
`,
			want: "allow signature",
		},
		{
			name: "allow signature option argument",
			rel:  "internal/plugins/features/secretguard/betterleaks_adapter.go",
			source: `package secretguard

import blscan "` + betterLeaksModule + `/scan"

var _ = blscan.WithAllowSignatures("operator-controlled")
`,
			want: "allow signature",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			findings, err := scanBetterLeaksArchitectureFile(test.rel, test.source)
			if err != nil {
				t.Fatalf("scanBetterLeaksArchitectureFile(%q): %v", test.rel, err)
			}
			for _, finding := range findings {
				if findingMatches(finding, test.want) {
					return
				}
			}
			t.Fatalf("scanBetterLeaksArchitectureFile(%q) did not report %q; findings=%v", test.rel, test.want, findings)
		})
	}
}

func TestBetterLeaksArchitectureRatchetsAllowOnlyPrivateAdapterFiles(t *testing.T) {
	t.Parallel()
	const source = `package secretguard

import blscan "github.com/betterleaks/betterleaks/v2/scan"
import blsources "github.com/betterleaks/betterleaks/v2/sources"

var _ *blscan.Scanner
var _ blsources.Reader
`
	for _, rel := range []string{
		"internal/plugins/features/secretguard/betterleaks_adapter.go",
		"internal/plugins/features/secretguard/betterleaks_adapter_test.go",
	} {
		t.Run(rel, func(t *testing.T) {
			findings, err := scanBetterLeaksArchitectureFile(rel, source)
			if err != nil {
				t.Fatalf("scanBetterLeaksArchitectureFile(%q): %v", rel, err)
			}
			if len(findings) != 0 {
				t.Fatalf("private adapter file %q was rejected: %v", rel, findings)
			}
		})
	}
}

func TestBetterLeaksArchitectureRatchetsPermitOnlyZeroArgumentAllowConstruction(t *testing.T) {
	t.Parallel()
	source := `package secretguard

import blscan "github.com/betterleaks/betterleaks/v2/scan"

var _ = blscan.WithAllowSignatures()
`
	findings, err := scanBetterLeaksArchitectureFile(
		"internal/plugins/features/secretguard/betterleaks_adapter.go",
		source,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 0 {
		t.Fatalf("zero-argument allow-signature construction was rejected: %v", findings)
	}
}

func TestBetterLeaksArchitectureRatchetsProductionTree(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	entries, err := loadSourceFiles(root, true)
	if err != nil {
		t.Fatalf("loadSourceFiles: %v", err)
	}
	var violations []string
	for _, entry := range entries {
		findings, err := scanBetterLeaksArchitectureFile(entry.rel, string(entry.src))
		if err != nil {
			t.Fatalf("scanBetterLeaksArchitectureFile(%q): %v", entry.rel, err)
		}
		for _, finding := range findings {
			violations = append(violations, finding.String())
		}
	}
	if len(violations) != 0 {
		t.Fatalf("BetterLeaks architecture boundary violated (%d):\n%s", len(violations), strings.Join(violations, "\n"))
	}
}

func scanBetterLeaksArchitectureFile(rel, source string) ([]RuleFinding, error) {
	fset, file, err := ParseGoSource(rel, []byte(source))
	if err != nil {
		return nil, err
	}

	production := !strings.HasSuffix(SlashPath(rel), "_test.go")
	inSecretGuard := MatchPathPrefix(PackageDirFromRel(rel), secretGuardFeaturePath)
	privateAdapter := isBetterLeaksPrivateAdapterFile(rel)
	sourceAliases := make(map[string]struct{})
	processAliases := make(map[string]string)
	var findings []RuleFinding
	add := func(rule string, pos token.Pos, detail string) {
		line := fset.Position(pos).Line
		if line > 0 {
			detail = fmt.Sprintf("line %d: %s", line, detail)
		}
		findings = append(findings, RuleFinding{Rule: rule, Path: SlashPath(rel), Detail: detail})
	}

	for _, spec := range file.Imports {
		if spec.Path == nil {
			continue
		}
		imp, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			return nil, fmt.Errorf("%s import path: %w", rel, err)
		}

		if strings.HasPrefix(imp, betterLeaksModulePath) &&
			(imp == betterLeaksModulePath || strings.HasPrefix(imp, betterLeaksModulePath+"/")) {
			if !privateAdapter {
				add("betterleaks_private_adapter_boundary", spec.Pos(), "BetterLeaks concrete import is outside the private adapter")
			}
			if reason := forbiddenBetterLeaksImportReason(imp); reason != "" {
				add("betterleaks_forbidden_integration", spec.Pos(), reason+" import is forbidden on the request path")
			}
		}

		if inSecretGuard && production {
			alias := importAlias(spec, imp)
			switch imp {
			case "os", "syscall", "os/exec":
				if imp == "os/exec" {
					add("betterleaks_process_boundary", spec.Pos(), "os/exec subprocess integration is forbidden in the production feature tree")
				}
				if alias == "." {
					add("betterleaks_process_boundary", spec.Pos(), "dot-imported process package cannot be checked safely")
				} else if alias != "_" {
					processAliases[alias] = imp
				}
			}
		}

		if imp == betterLeaksModulePath+"/sources" {
			alias := importAlias(spec, imp)
			switch alias {
			case ".", "_":
				add("betterleaks_source_orchestration", spec.Pos(), "dot or side-effect source import cannot be proven to be the in-memory source contract")
			default:
				sourceAliases[alias] = struct{}{}
			}
		}
	}

	allowSignatureCall := make(map[token.Pos]struct{})
	if production {
		ast.Inspect(file, func(node ast.Node) bool {
			switch n := node.(type) {
			case *ast.StructType:
				for _, field := range n.Fields.List {
					if field.Tag == nil {
						continue
					}
					tagValue, err := strconv.Unquote(field.Tag.Value)
					if err != nil {
						continue
					}
					tag := reflect.StructTag(tagValue)
					for _, key := range []string{"json", "yaml", "toml", "mapstructure"} {
						value, ok := tag.Lookup(key)
						if !ok {
							continue
						}
						name, _, _ := strings.Cut(value, ",")
						if isAllowSignatureKey(name) {
							add("betterleaks_allow_signature_surface", field.Tag.Pos(), "allow-signature configuration tag is not exposed")
						}
					}
				}
			case *ast.CallExpr:
				if astNodeName(n.Fun) == "WithAllowSignatures" {
					if len(n.Args) != 0 {
						add("betterleaks_allow_signature_surface", n.Pos(), "WithAllowSignatures must be constructed with zero arguments")
					} else {
						allowSignatureCall[n.Fun.Pos()] = struct{}{}
						if selector, ok := n.Fun.(*ast.SelectorExpr); ok && selector.Sel != nil {
							allowSignatureCall[selector.Sel.Pos()] = struct{}{}
						}
					}
				}
			case *ast.SelectorExpr:
				if inSecretGuard && production && forbiddenProcessReference(n, processAliases) {
					add("betterleaks_process_boundary", n.Pos(), "process execution call is forbidden in the production feature tree")
				}
				if n.Sel == nil {
					return true
				}
				if n.Sel.Name == "WithAllowSignatures" {
					if _, ok := allowSignatureCall[n.Pos()]; !ok {
						add("betterleaks_allow_signature_surface", n.Pos(), "WithAllowSignatures is only permitted as a zero-argument construction option")
					}
				}
				if alias, ok := n.X.(*ast.Ident); ok {
					if _, isSource := sourceAliases[alias.Name]; isSource && !allowedSourceSelector(n.Sel.Name) {
						add("betterleaks_source_orchestration", n.Pos(), "source selector "+n.Sel.Name+" is outside the allowed in-memory source contract")
					}
				}
			case *ast.Ident:
				if n.Name == "WithAllowSignatures" {
					if _, ok := allowSignatureCall[n.Pos()]; !ok {
						add("betterleaks_allow_signature_surface", n.Pos(), "WithAllowSignatures is only permitted as a zero-argument construction option")
					}
				} else if isAllowSignatureIdentifier(n.Name) {
					add("betterleaks_allow_signature_surface", n.Pos(), "allow-signature configuration is not an exposed option")
				}
			case *ast.BasicLit:
				if n.Kind == token.STRING {
					value, err := strconv.Unquote(n.Value)
					if err == nil && isAllowSignatureKey(value) {
						add("betterleaks_allow_signature_surface", n.Pos(), "allow-signature configuration key is not exposed")
					}
				}
			}
			return true
		})
	}

	sort.Slice(findings, func(i, j int) bool { return findings[i].String() < findings[j].String() })
	return findings, nil
}

func isBetterLeaksPrivateAdapterFile(rel string) bool {
	switch SlashPath(rel) {
	case "internal/plugins/features/secretguard/betterleaks_adapter.go",
		"internal/plugins/features/secretguard/betterleaks_adapter_test.go":
		return true
	default:
		return false
	}
}

func importAlias(spec *ast.ImportSpec, imp string) string {
	if spec.Name != nil {
		return spec.Name.Name
	}
	if index := strings.LastIndexByte(imp, '/'); index >= 0 {
		return imp[index+1:]
	}
	return imp
}

func forbiddenBetterLeaksImportReason(imp string) string {
	switch {
	case strings.HasPrefix(imp, betterLeaksModulePath+"/analyze"):
		return "analysis"
	case strings.HasPrefix(imp, betterLeaksModulePath+"/pipeline"):
		return "pipeline"
	case strings.HasPrefix(imp, betterLeaksModulePath+"/credential"):
		return "provider validation"
	case strings.HasPrefix(imp, betterLeaksModulePath+"/validate") ||
		strings.HasPrefix(imp, betterLeaksModulePath+"/validation"):
		return "provider validation"
	case strings.HasPrefix(imp, betterLeaksModulePath+"/internal/provider"):
		return "provider validation"
	case strings.HasPrefix(imp, betterLeaksModulePath+"/cmd"):
		return "CLI"
	case strings.HasPrefix(imp, betterLeaksModulePath+"/revoke"):
		return "revocation"
	case strings.HasPrefix(imp, betterLeaksModulePath+"/sources/"):
		return "source orchestration"
	default:
		return ""
	}
}

func allowedSourceSelector(name string) bool {
	switch name {
	case "Reader", "Source", "Fragment", "FragmentsFunc", "PrefilterFunc":
		return true
	default:
		return false
	}
}

func forbiddenProcessReference(sel *ast.SelectorExpr, aliases map[string]string) bool {
	if sel == nil || sel.Sel == nil {
		return false
	}
	qualifier, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	packagePath, ok := aliases[qualifier.Name]
	if !ok {
		return false
	}
	switch packagePath + "." + sel.Sel.Name {
	case "os/exec.Command", "os/exec.CommandContext", "os/exec.LookPath", "os.StartProcess", "syscall.ForkExec", "syscall.StartProcess", "syscall.Exec":
		return true
	default:
		return false
	}
}

func astNodeName(node ast.Expr) string {
	switch n := node.(type) {
	case *ast.Ident:
		return n.Name
	case *ast.SelectorExpr:
		if n.Sel != nil {
			return n.Sel.Name
		}
	}
	return ""
}

func isAllowSignatureIdentifier(name string) bool {
	normalized := strings.NewReplacer("_", "", "-", "").Replace(strings.ToLower(name))
	return normalized == "allowsignatures" || normalized == "allowsignature"
}

func isAllowSignatureKey(value string) bool {
	return isAllowSignatureIdentifier(value)
}

// findingMatches reports whether a finding's rule and detail describe the
// expected boundary, normalizing separators so "private_adapter" matches
// "private adapter".
func findingMatches(finding RuleFinding, want string) bool {
	description := strings.NewReplacer("-", " ", "_", " ").Replace(strings.ToLower(finding.Rule + " " + finding.Detail))
	return strings.Contains(description, strings.ToLower(want))
}

// TestBetterLeaksArchitectureRatchetsRejectBypassAttempts is the negative
// counterpart of the synthetic-violation table: each attempted bypass must be
// reported by the rule that owns it, not merely by some finding.
func TestBetterLeaksArchitectureRatchetsRejectBypassAttempts(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, rel, src, want string }{
		{"adapter suffix outside the private adapter", "internal/plugins/features/secretguard/betterleaks_adapter_public.go", `package secretguard; import "github.com/betterleaks/betterleaks/v2/report"; var _ report.Finding`, "private adapter boundary"},
		{"aliased os process call", "internal/plugins/features/secretguard/request_scan.go", `package secretguard; import process "os"; func launch(){ process.StartProcess("betterleaks", nil, nil) }`, "process boundary"},
		{"aliased syscall fork exec", "internal/plugins/features/secretguard/request_scan.go", `package secretguard; import process "syscall"; func launch(){ process.ForkExec("betterleaks", nil, nil) }`, "process boundary"},
		{"aliased syscall start process value", "internal/plugins/features/secretguard/request_scan.go", `package secretguard; import process "syscall"; var launch = process.StartProcess`, "process boundary"},
		{"aliased syscall exec value", "internal/plugins/features/secretguard/request_scan.go", `package secretguard; import process "syscall"; var launch = process.Exec`, "process boundary"},
		{"json tagged allow signature field", "internal/plugins/features/secretguard/config.go", "package secretguard; type Config struct { Suppress []string `json:\"allow_signatures\"` }", "allow signature"},
		{"source orchestration selector in the private adapter", "internal/plugins/features/secretguard/betterleaks_adapter.go", `package secretguard; import "github.com/betterleaks/betterleaks/v2/sources"; var _ = sources.ResolveRemote`, "source orchestration"},
		{"unknown source selector", "internal/plugins/features/secretguard/betterleaks_adapter.go", `package secretguard; import bl "github.com/betterleaks/betterleaks/v2/sources"; var _ = bl.URL`, "source orchestration"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			findings, err := scanBetterLeaksArchitectureFile(test.rel, test.src)
			if err != nil {
				t.Fatal(err)
			}
			for _, finding := range findings {
				if findingMatches(finding, test.want) {
					return
				}
			}
			t.Errorf("bypass accepted or reported without %q: %s (findings=%v)", test.want, test.name, findings)
		})
	}
}
