package archtest

import (
	"fmt"
	"go/ast"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// SymbolKind classifies a package-scope declaration.
type SymbolKind string

const (
	SymbolFunc   SymbolKind = "func"
	SymbolType   SymbolKind = "type"
	SymbolVar    SymbolKind = "var"
	SymbolConst  SymbolKind = "const"
	SymbolMethod SymbolKind = "method" // FuncDecl with receiver; Name is method name
)

// ForbiddenDeclRule forbids a package-scope declaration by exact name.
type ForbiddenDeclRule struct {
	Package  string     // repo-relative package dir (e.g. internal/infra/runtimebundle)
	Kind     SymbolKind // declaration kind
	Receiver string     // optional receiver base type name for SymbolMethod
	Name     string     // exact symbol name
	Reason   string     // short rationale
}

// RuleFinding is one violation of a permanent architecture rule.
type RuleFinding struct {
	Rule   string
	Path   string
	Detail string
}

func (f RuleFinding) String() string {
	return f.Path + ": " + f.Rule + ": " + f.Detail
}

// ForbiddenDeclarations is the permanent deleted-symbol inventory.
var ForbiddenDeclarations = []ForbiddenDeclRule{
	// Phase 8 deleted stream-time financial runtime surface
	{Package: "pkg/aipsdk/economics", Kind: SymbolType, Name: "RatingRequest", Reason: "stream-time rater contract deleted; billing owns monetary rating"},
	{Package: "pkg/aipsdk/economics", Kind: SymbolType, Name: "RatingResult", Reason: "stream-time rater contract deleted; billing owns monetary rating"},
	{Package: "pkg/aipsdk/economics", Kind: SymbolType, Name: "RaterRegistration", Reason: "stream-time rater registration deleted"},
	{Package: "pkg/aipsdk/economics", Kind: SymbolType, Name: "OutputLimitStatus", Reason: "legacy monetary output-limit quote contract deleted"},
	{Package: "pkg/aipsdk/economics", Kind: SymbolType, Name: "OutputLimitRequest", Reason: "legacy monetary output-limit quote contract deleted"},
	{Package: "pkg/aipsdk/economics", Kind: SymbolType, Name: "OutputLimitResult", Reason: "legacy monetary output-limit quote contract deleted"},
	{Package: "pkg/aipsdk/economics", Kind: SymbolType, Name: "OutputLimitQuoter", Reason: "legacy monetary output-limit quote contract deleted"},
	{Package: "internal/core/runtime", Kind: SymbolMethod, Name: "enrichUsageCost", Reason: "stream-time monetary price enrichment deleted"},
	{Package: "internal/core/runtime", Kind: SymbolMethod, Name: "recordTokenAccountingLedger", Reason: "direct runtime token-ledger write deleted"},
	{Package: "internal/core/runtime", Kind: SymbolMethod, Name: "recordPartialTokenAccountingLedger", Reason: "direct runtime token-ledger write deleted"},
	{Package: "internal/core/runtime", Kind: SymbolMethod, Name: "recordCancellationBillingMarker", Reason: "estimated stream-time billing marker deleted"},
	{Package: "internal/core/runtime", Kind: SymbolMethod, Name: "rateMonetaryExposure", Reason: "runtime economics money rating deleted; billing owns monetary rating"},
	{Package: "internal/core/runtime", Kind: SymbolFunc, Name: "rateMonetaryExposure", Reason: "runtime economics money rating deleted; billing owns monetary rating"},
	{Package: "internal/core/runtime", Kind: SymbolFunc, Name: "rateMonetaryExposureWith", Reason: "runtime economics money rating deleted; billing owns monetary rating"},
	{Package: "internal/infra/runtimebundle", Kind: SymbolFunc, Name: "openDurableAccountingLedger", Reason: "legacy financial token ledger composition deleted"},
	{Package: "internal/infra/runtimebundle", Kind: SymbolFunc, Name: "selectEconomicsRater", Reason: "EconomicsRater wiring deleted; BillingAdmission owns monetary admission"},
	// Compatibility Built / Build surface
	{Package: "internal/infra/runtimebundle", Kind: SymbolType, Name: "Built", Reason: "compatibility Built aggregate deleted"},
	{Package: "internal/infra/runtimebundle", Kind: SymbolFunc, Name: "Build", Reason: "compatibility runtimebundle.Build deleted"},
	{Package: "internal/infra/runtimebundle", Kind: SymbolFunc, Name: "requestPlaneAsBuilt", Reason: "canonical-to-legacy rehydration deleted"},
	{Package: "internal/infra/runtimebundle", Kind: SymbolType, Name: "RequestPlane", Reason: "broad RequestPlane aggregate deleted"},
	{Package: "internal/infra/runtimebundle", Kind: SymbolFunc, Name: "NewCompatRequestPlane", Reason: "RequestPlane compatibility ctor deleted"},
	{Package: "internal/infra/runtimebundle", Kind: SymbolFunc, Name: "ComposeRequestPlane", Reason: "ComposeRequestPlane deleted"},
	{Package: "internal/infra/runtimebundle", Kind: SymbolFunc, Name: "standardHTTPInputFromRequestPlane", Reason: "RequestPlane projector deleted"},
	{Package: "internal/infra/runtimebundle", Kind: SymbolType, Name: "CandidateRuntime", Reason: "use package-private candidateAssembly"},
	{Package: "internal/infra/runtimebundle", Kind: SymbolType, Name: "ReloadHost", Reason: "renamed to concrete Host"},
	{Package: "internal/infra/runtimebundle", Kind: SymbolMethod, Name: "LegacyClosers", Reason: "ResourceLedger closer projection deleted"},

	// Dual bootstrap / host attachment
	{Package: "internal/infra/runtimebundle", Kind: SymbolFunc, Name: "BuildBootstrap", Reason: "dual bootstrap path deleted"},
	{Package: "internal/infra/runtimebundle", Kind: SymbolFunc, Name: "AttachReloadHost", Reason: "host-attachment dual path deleted"},
	{Package: "internal/infra/runtimebundle", Kind: SymbolType, Name: "BootstrapResult", Reason: "bootstrap vocabulary deleted"},
	{Package: "internal/infra/runtimebundle", Kind: SymbolType, Name: "BootstrapMode", Reason: "bootstrap vocabulary deleted"},
	{Package: "internal/infra/runtimebundle", Kind: SymbolConst, Name: "BootstrapUnspecified", Reason: "bootstrap vocabulary deleted"},
	{Package: "internal/infra/runtimebundle", Kind: SymbolConst, Name: "BootstrapServe", Reason: "bootstrap vocabulary deleted"},
	{Package: "internal/infra/runtimebundle", Kind: SymbolFunc, Name: "LoadBootstrapEffective", Reason: "use LoadBootstrapEffectiveWithSource only"},
	{Package: "internal/infra/runtimebundle", Kind: SymbolFunc, Name: "NewReloadHost", Reason: "use BuildHost / concrete Host"},
	{Package: "internal/infra/runtimebundle", Kind: SymbolFunc, Name: "bindReloadHost", Reason: "legacy host bind deleted"},

	// Deleted serve surface
	{Package: "internal/stdhttp", Kind: SymbolFunc, Name: "RunWithRuntime", Reason: "deleted serve API"},
	{Package: "internal/stdhttp", Kind: SymbolFunc, Name: "NewStandardHandler", Reason: "deleted Built-era handler"},
	{Package: "internal/stdhttp", Kind: SymbolFunc, Name: "releaseBuiltResources", Reason: "deleted Built closer release"},
	{Package: "internal/stdhttp", Kind: SymbolFunc, Name: "runClosers", Reason: "deleted closer runner"},
	{Package: "internal/stdhttp", Kind: SymbolFunc, Name: "standardHTTPInputFromBuilt", Reason: "deleted Built projector"},
	{Package: "internal/stdhttp", Kind: SymbolFunc, Name: "shutdownGenerationHost", Reason: "Host.Close owns shutdown"},
	{Package: "internal/stdhttp", Kind: SymbolFunc, Name: "closeProcessServices", Reason: "Host.Close owns process close"},

	// Deleted aipstd tracing boundary
	{Package: "cmd/aipstd", Kind: SymbolFunc, Name: "tracingDeferred", Reason: "Host.Close owns tracing shutdown"},
	{Package: "cmd/aipstd", Kind: SymbolFunc, Name: "deferHostTracingShutdown", Reason: "Host.Close owns tracing shutdown"},

	// Legacy options quarantine
	{Package: "pkg/aipruntime", Kind: SymbolFunc, Name: "adaptLegacyOptions", Reason: "legacy options adapter deleted"},
	{Package: "pkg/aipruntime", Kind: SymbolFunc, Name: "prepareCanonicalProduction", Reason: "legacy options helper deleted"},
	{Package: "pkg/aipruntime", Kind: SymbolFunc, Name: "filterDescriptorsByFamily", Reason: "legacy options helper deleted"},
	{Package: "pkg/aipruntime", Kind: SymbolFunc, Name: "descriptorHasFamily", Reason: "legacy options helper deleted"},
	{Package: "pkg/aipruntime", Kind: SymbolFunc, Name: "legacyRequestRegistrations", Reason: "legacy options helper deleted"},
	{Package: "pkg/aipruntime", Kind: SymbolFunc, Name: "legacyAttemptRegistrations", Reason: "legacy options helper deleted"},
	{Package: "pkg/aipruntime", Kind: SymbolFunc, Name: "legacyConcurrencyRegistration", Reason: "legacy options helper deleted"},
	{Package: "pkg/aipruntime", Kind: SymbolConst, Name: "legacyProductionRaterID", Reason: "legacy options id deleted"},
	{Package: "pkg/aipruntime", Kind: SymbolFunc, Name: "mapTriggerIn", Reason: "reload field mapper deleted"},
	{Package: "pkg/aipruntime", Kind: SymbolFunc, Name: "mapTriggerOut", Reason: "reload field mapper deleted"},
	{Package: "pkg/aipruntime", Kind: SymbolFunc, Name: "mapCategoryOut", Reason: "reload field mapper deleted"},
	{Package: "pkg/aipruntime", Kind: SymbolFunc, Name: "mapResultOut", Reason: "reload field mapper deleted"},
	{Package: "pkg/aipruntime", Kind: SymbolFunc, Name: "mapHistoryOut", Reason: "reload field mapper deleted"},
	{Package: "pkg/aipruntime", Kind: SymbolFunc, Name: "mapStatusOut", Reason: "reload field mapper deleted"},

	// Internal reload contract ownership
	{Package: "internal/core/configreload", Kind: SymbolType, Name: "TriggerKind", Reason: "contract owned by pkg/aipsdk/configreload"},
	{Package: "internal/core/configreload", Kind: SymbolType, Name: "Trigger", Reason: "contract owned by pkg/aipsdk/configreload"},
	{Package: "internal/core/configreload", Kind: SymbolType, Name: "Result", Reason: "contract owned by pkg/aipsdk/configreload"},
	{Package: "internal/core/configreload", Kind: SymbolType, Name: "Status", Reason: "contract owned by pkg/aipsdk/configreload"},
	{Package: "internal/core/configreload", Kind: SymbolType, Name: "HistoryEntry", Reason: "contract owned by pkg/aipsdk/configreload"},
	{Package: "internal/core/configreload", Kind: SymbolType, Name: "ReloadTrigger", Reason: "contract owned by pkg/aipsdk/configreload"},
	{Package: "internal/core/configreload", Kind: SymbolType, Name: "ReloadResult", Reason: "contract owned by pkg/aipsdk/configreload"},
	{Package: "internal/core/configreload", Kind: SymbolType, Name: "ReloadStatus", Reason: "contract owned by pkg/aipsdk/configreload"},
	{Package: "internal/core/configreload", Kind: SymbolType, Name: "ResultCategory", Reason: "contract owned by pkg/aipsdk/configreload"},

	// Obsolete feature-merge APIs (Phase 4 / Task 4.3)
	{Package: "internal/featurebundle", Kind: SymbolType, Name: "Merged" + "Feature" + "Surface", Reason: "legacy feature" + " surface type deleted"},
	{Package: "internal/featurebundle", Kind: SymbolFunc, Name: "Merge" + "Bundles", Reason: "legacy unchecked" + " bundle merge deleted"},
	{Package: "internal/featurebundle", Kind: SymbolFunc, Name: "Merge" + "BundlesChecked", Reason: "legacy checked" + " bundle merge deleted"},
	{Package: "internal/featurebundle", Kind: SymbolFunc, Name: "Merge" + "FeatureSurface", Reason: "legacy singular" + " surface merge deleted"},
	{Package: "internal/featurebundle", Kind: SymbolFunc, Name: "Merge" + "FeatureSurfaces", Reason: "legacy plural" + " surface merge deleted"},
	{Package: "internal/featurebundle", Kind: SymbolFunc, Name: "Merge" + "BundlesViaGenerated", Reason: "legacy generated-to-legacy" + " bundle merge deleted"},
	{Package: "internal/featurebundle", Kind: SymbolFunc, Name: "Merge" + "FeatureSurfaceViaGenerated", Reason: "legacy generated-to-legacy" + " surface merge deleted"},
	{Package: "internal/featurebundle", Kind: SymbolMethod, Name: "To" + "Merged" + "Feature" + "Surface", Reason: "legacy surface" + " projection method deleted"},
	{Package: "internal/featurebundle", Kind: SymbolMethod, Receiver: "Merged" + "Feature" + "Surface", Name: "App" + "end", Reason: "legacy mutable append" + " method deleted"},
	{Package: "internal/infra/runtimebundle", Kind: SymbolFunc, Name: "extensions" + "FromMerged", Reason: "legacy runtime extension" + " projection deleted"},
	{Package: "internal/infra/runtimebundle", Kind: SymbolFunc, Name: "hooksConfig" + "FromMerged", Reason: "legacy hook config" + " projection deleted"},
	{Package: "internal/testkit/planeparity", Kind: SymbolFunc, Name: "Assert" + "MergedSurfacesEqual", Reason: "legacy surface equality" + " testkit helper deleted"},
	{Package: "internal/testkit/planeparity", Kind: SymbolFunc, Name: "Assert" + "DualPathParity", Reason: "legacy dual path" + " parity testkit helper deleted"},
	{Package: "internal/testkit/planeparity", Kind: SymbolFunc, Name: "Assert" + "GeneratedSurfaceInvariants", Reason: "superseded by Assert" + "GeneratedMergeInvariants"},
	// Interleaved thinking core retirement (Task 5.4, F1)
	{Package: "internal/core/interleavedstate", Kind: SymbolType, Name: "MemoRef", Reason: "memo reference owned by interleavedthinking feature"},
	{Package: "internal/core/config", Kind: SymbolConst, Name: "DefaultInterleavedInstructions", Reason: "prompt owned by interleavedthinking feature"},
	{Package: "internal/core/config", Kind: SymbolFunc, Name: "ResolveInterleavedInstructions", Reason: "instruction loading owned by interleavedthinking feature"},
	// Keepwarm prompt-cache maintenance core retirement (Task 6.3)
	{Package: "internal/core/config", Kind: SymbolType, Name: "PromptCacheConfig", Reason: "prompt-cache keepwarm configuration owned by keepwarm feature"},
	{Package: "internal/core/config", Kind: SymbolMethod, Receiver: "Config", Name: "EffectiveKeepwarm", Reason: "keepwarm configuration owned by keepwarm feature"},

	// Authority-clamp provider-semantics retirement: core keeps only the
	// provider-neutral IgnoresAuthorityMaxOutputTokensClamp callback seam, while
	// the Codex client-compat extension key and its decoder live in the feature
	// plugin that writes the extension.
	{Package: "internal/core/execbackend", Kind: SymbolConst, Name: "Codex" + "IgnoreUnsupportedGenParamsExt", Reason: "codex extension key owned by codexclientcompat feature"},
	{Package: "internal/core/execbackend", Kind: SymbolFunc, Name: "IgnoresClampVia" + "CodexUnsupportedGenParams", Reason: "codex extension decoder owned by codexclientcompat feature"},
}

// AbsentFiles must not exist in the production tree.
var AbsentFiles = []string{
	"pkg/aipruntime/legacy_options.go",
	"pkg/aipruntime/reload_map.go",
	"cmd/aipstd/tracing_shutdown.go",
	"internal/infra/runtimebundle/built.go",
	"internal/infra/runtimebundle/build.go",
	"internal/infra/tokenaccounting/ledgerstore/store.go",
	"internal/infra/tokenaccounting/ledgerstore/20260514000000_token_accounting_ledger_baseline.go",
}

// ScanForbiddenDeclarations reports package-scope declarations matching ForbiddenDeclarations
// in production Go files.
func ScanForbiddenDeclarations(root string) ([]RuleFinding, error) {
	return scanForbiddenDeclarationsWith(root, WalkProductionGoFiles)
}

// ScanForbiddenDeclarationsIncludingTests reports package-scope declarations matching ForbiddenDeclarations
// in all Go files, including tests (_test.go), excluding generated files.
func ScanForbiddenDeclarationsIncludingTests(root string) ([]RuleFinding, error) {
	return scanForbiddenDeclarationsWith(root, WalkGoFiles)
}

func scanForbiddenDeclarationsWith(root string, walker func(string, func(rel, abs string, src []byte) error) error) ([]RuleFinding, error) {
	index := make(map[string][]ForbiddenDeclRule)
	for _, r := range ForbiddenDeclarations {
		index[r.Package] = append(index[r.Package], r)
	}
	var out []RuleFinding
	err := walker(root, func(rel, abs string, src []byte) error {
		pkg := PackageDirFromRel(rel)
		rules := matchingPackageRules(index, pkg)
		if len(rules) == 0 {
			return nil
		}
		_, f, err := ParseGoSource(abs, src)
		if err != nil {
			return fmt.Errorf("%s: %w", rel, err)
		}
		if IsGeneratedFile(rel, src, f) {
			return nil
		}
		for _, r := range rules {
			if DeclExists(f, r) {
				detail := string(r.Kind) + " "
				if r.Receiver != "" {
					detail += "(" + r.Receiver + ")."
				}
				detail += r.Name + " (" + r.Reason + ")"
				out = append(out, RuleFinding{
					Rule:   "forbidden_decl",
					Path:   rel,
					Detail: detail,
				})
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out, nil
}

func matchingPackageRules(index map[string][]ForbiddenDeclRule, pkg string) []ForbiddenDeclRule {
	var out []ForbiddenDeclRule
	for prefix, rules := range index {
		if MatchPathPrefix(pkg, prefix) {
			out = append(out, rules...)
		}
	}
	return out
}

func receiverBaseTypeName(expr ast.Expr) string {
	for expr != nil {
		switch e := expr.(type) {
		case *ast.StarExpr:
			expr = e.X
		case *ast.ParenExpr:
			expr = e.X
		case *ast.IndexExpr:
			expr = e.X
		case *ast.IndexListExpr:
			expr = e.X
		case *ast.Ident:
			return e.Name
		case *ast.SelectorExpr:
			return e.Sel.Name
		default:
			return ""
		}
	}
	return ""
}

// DeclExists reports whether rule matches a declaration in f.
func DeclExists(f *ast.File, r ForbiddenDeclRule) bool {
	switch r.Kind {
	case SymbolFunc:
		for _, decl := range f.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if ok && fd.Recv == nil && fd.Name != nil && fd.Name.Name == r.Name {
				return true
			}
		}
	case SymbolMethod:
		for _, decl := range f.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if ok && fd.Recv != nil && len(fd.Recv.List) > 0 && fd.Name != nil && fd.Name.Name == r.Name {
				if r.Receiver == "" {
					return true
				}
				recvType := receiverBaseTypeName(fd.Recv.List[0].Type)
				if recvType == r.Receiver {
					return true
				}
			}
		}
	case SymbolType:
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.TYPE {
				continue
			}
			for _, spec := range gd.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if ok && ts.Name != nil && ts.Name.Name == r.Name {
					return true
				}
			}
		}
	case SymbolVar, SymbolConst:
		tok := token.VAR
		if r.Kind == SymbolConst {
			tok = token.CONST
		}
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != tok {
				continue
			}
			for _, spec := range gd.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for _, name := range vs.Names {
					if name != nil && name.Name == r.Name {
						return true
					}
				}
			}
		}
	}
	return false
}

// ScanAbsentFiles reports paths from AbsentFiles that still exist.
func ScanAbsentFiles(root string) ([]RuleFinding, error) {
	var out []RuleFinding
	for _, rel := range AbsentFiles {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if _, err := os.Stat(path); err == nil {
			out = append(out, RuleFinding{Rule: "absent_file", Path: rel, Detail: "deleted production file must not reappear"})
		} else if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
	}
	return out, nil
}

// RetiredPackageDirs lists retired package directory trees that must not contain any production Go files.
var RetiredPackageDirs = []string{
	"internal/core/toolcallrepair",
	"internal/core/secretguard",
	"internal/core/compactiondetect",
	"internal/core/compactioncontinuity",
	"internal/core/conversationview",
	"internal/core/interleavedthinking",
	"internal/core/keepwarm",
	"internal/core/terminaldecisionpolicy",
	// The billing spool and the billing admin handlers moved to the Enterprise
	// tree with open-core-billing-split task 6.2.
	"internal/infra/billingspool",
	"internal/stdhttp/admin/billing",
	// The billing domain, composition and admission moved with task 6.5.
	"internal/core/billing",
	"internal/infra/billingcompose",
	"internal/infra/billingadmission",
	"internal/reasoningreplay",
	"internal/infra/reasoningcompose",
	"internal/infra/secretguardcompose",
	"internal/infra/secretaudit",
	"internal/infra/compactioncompose",
	"internal/infra/usageauthority",
	// The usage-authority tree moved to the Enterprise tree with task 6.1
	// Slice B; the open-source tree keeps only the usage-limit port.
	"internal/core/usageauthority",
}

// ScanFileRetiredPackage checks if a repo-relative file path belongs to a retired package.
func ScanFileRetiredPackage(rel string) *RuleFinding {
	pkg := PackageDirFromRel(rel)
	for _, retired := range RetiredPackageDirs {
		if pkg == retired || strings.HasPrefix(pkg, retired+"/") {
			return &RuleFinding{
				Rule:   "retired_package",
				Path:   rel,
				Detail: "retired production package " + retired + " must not contain Go files",
			}
		}
	}
	return nil
}

// ScanRetiredPackages reports any production Go files located under RetiredPackageDirs.
func ScanRetiredPackages(root string) ([]RuleFinding, error) {
	var out []RuleFinding
	err := WalkProductionGoFiles(root, func(rel, abs string, src []byte) error {
		if f := ScanFileRetiredPackage(rel); f != nil {
			out = append(out, *f)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out, nil
}
