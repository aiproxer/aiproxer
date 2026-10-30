package archtest

import (
	"fmt"
	"sort"
	"strings"
)

// ForbiddenImportRule forbids an import edge from source packages to a target pattern.
type ForbiddenImportRule struct {
	SourcePattern string // repo-relative package dir pattern
	TargetPattern string // import path (exact package) or substring marker starting with /
	Reason        string
	ExceptPrefix  []string // allowed import path prefixes
}

// ForbiddenImports is the permanent package-level import deny-list.
var ForbiddenImports = []ForbiddenImportRule{
	{SourcePattern: "internal/compactionfacts", TargetPattern: "/internal/core", Reason: "compactionfacts must remain neutral and not import internal/core"},
	{SourcePattern: "internal/compactionfacts", TargetPattern: "/internal/infra", Reason: "compactionfacts must remain neutral and not import internal/infra"},
	{SourcePattern: "internal/compactionfacts", TargetPattern: "/internal/plugins", Reason: "compactionfacts must remain neutral and not import internal/plugins"},
	{SourcePattern: "internal/capabilityfacts", TargetPattern: "/internal/core", Reason: "capabilityfacts must remain neutral and not import internal/core"},
	{SourcePattern: "internal/capabilityfacts", TargetPattern: "/internal/infra", Reason: "capabilityfacts must remain neutral and not import internal/infra"},
	{SourcePattern: "internal/capabilityfacts", TargetPattern: "/internal/plugins", Reason: "capabilityfacts must remain neutral and not import internal/plugins"},
	{SourcePattern: "internal/stdhttp/contract", TargetPattern: "github.com/aiproxer/aiproxer/internal/infra/runtimebundle", Reason: "contract package must stay cycle-neutral"},
	{SourcePattern: "internal/stdhttp/contract", TargetPattern: "github.com/aiproxer/aiproxer/internal/stdhttp", Reason: "contract must not import root stdhttp"},
	{
		SourcePattern: "internal/infra/runtimebundle",
		TargetPattern: "github.com/aiproxer/aiproxer/internal/stdhttp",
		Reason:        "runtimebundle must import stdhttp/contract, not root stdhttp",
		ExceptPrefix: []string{
			"github.com/aiproxer/aiproxer/internal/stdhttp/",
		},
	},
	{
		SourcePattern: "pkg/aipruntime",
		TargetPattern: "github.com/aiproxer/aiproxer/internal/core/configreload",
		Reason:        "public facade uses pkg/aipsdk/configreload",
	},
	{
		SourcePattern: "internal/core/runtime",
		TargetPattern: "/internal/core/tokenaccounting/ledger",
		Reason:        "runtime must not write the legacy token ledger; sealed TUR/LUR owns financial evidence",
	},
	{
		SourcePattern: "internal/core/runtime",
		TargetPattern: "/enterprise/internal/infra/billingstore",
		Reason:        "runtime must not import journal settlement; post-turn billing owns monetary truth",
	},
	{
		SourcePattern: "internal/core/billing",
		TargetPattern: "github.com/openai/openai-go",
		Reason:        "billing must not import provider SDKs",
	},
	{
		SourcePattern: "internal/core/billing",
		TargetPattern: "github.com/anthropics/anthropic-sdk-go",
		Reason:        "billing must not import provider SDKs",
	},
	{
		SourcePattern: "internal/core/billing",
		TargetPattern: "google.golang.org/genai",
		Reason:        "billing must not import provider SDKs",
	},
	{
		SourcePattern: "internal/core/billing",
		TargetPattern: "github.com/aws/aws-sdk-go-v2",
		Reason:        "billing must not import provider SDKs",
	},
	{
		SourcePattern: "internal/core/billing",
		TargetPattern: "/pkg/aipapi",
		Reason:        "billing evidence stays provider-neutral and aipapi-free",
	},
	{
		SourcePattern: "internal/core",
		TargetPattern: "/internal/infra/runtimebundle",
		Reason:        "core must not import composition runtimebundle",
	},
	{
		SourcePattern: "internal/core",
		TargetPattern: "/internal/infra/runtimehost",
		Reason:        "core must not import composition runtimehost",
	},
	{
		SourcePattern: "internal/core",
		TargetPattern: "/internal/infra/configsource",
		Reason:        "core must not import configsource watcher adapter",
	},
	{
		SourcePattern: "internal/core",
		TargetPattern: "/internal/stdhttp",
		Reason:        "core must not import stdhttp driving adapters",
	},
	{
		SourcePattern: "internal/core",
		TargetPattern: "os/signal",
		Reason:        "core must not import signal driving adapters",
	},
	{
		SourcePattern: "internal/core",
		TargetPattern: "*fsnotify*",
		Reason:        "core must not import filesystem watchers",
	},
	{
		SourcePattern: "internal/core",
		TargetPattern: "github.com/rjeczalik/notify",
		Reason:        "core must not import filesystem watchers",
	},
	{
		SourcePattern: "internal/core",
		TargetPattern: "/internal/plugins/protocols/openresponses",
		Reason:        "core must not import protocol wire codecs",
	},
	{
		SourcePattern: "internal/plugins/frontends",
		TargetPattern: "/internal/refclient",
		Reason:        "production frontend adapters must not import reference clients",
	},
	{
		SourcePattern: "internal/plugins/backends",
		TargetPattern: "/internal/refbackend",
		Reason:        "production backend adapters must not import reference backends",
	},
	{
		SourcePattern: "internal/plugins/protocols/openresponses",
		TargetPattern: "/internal/refclient",
		Reason:        "production wire codec must remain independent of reference clients",
	},
	{
		SourcePattern: "internal/plugins/protocols/openresponses",
		TargetPattern: "/internal/refbackend",
		Reason:        "production wire codec must remain independent of reference backends",
	},
	{
		SourcePattern: "internal/plugins/protocols/openresponses",
		TargetPattern: "github.com/openai/openai-go",
		Reason:        "openresponses must not import provider SDKs",
	},
	{
		SourcePattern: "internal/plugins/protocols/openresponses",
		TargetPattern: "github.com/anthropics/anthropic-sdk-go",
		Reason:        "openresponses must not import provider SDKs",
	},
	{
		SourcePattern: "internal/plugins/protocols/openresponses",
		TargetPattern: "github.com/google/generative-ai-go",
		Reason:        "openresponses must not import provider SDKs",
	},
	{
		SourcePattern: "internal/plugins/protocols/openresponses",
		TargetPattern: "github.com/aws/aws-sdk-go-v2",
		Reason:        "openresponses must not import provider SDKs",
	},
	{
		SourcePattern: "pkg/aipapi",
		TargetPattern: "/internal/plugins/protocols/openresponses",
		Reason:        "public canonical SDK must not import protocol wire codecs",
	},
	{
		SourcePattern: "pkg/aipsdk",
		TargetPattern: "/internal/plugins/protocols/openresponses",
		Reason:        "public plugin SDK must not import protocol wire codecs",
	},
	{
		SourcePattern: "internal/refclient/openresponses",
		TargetPattern: "/internal/plugins/protocols/openresponses",
		Reason:        "reference client emulator must not import production OpenResponses codecs",
	},
	{
		SourcePattern: "internal/refclient/openresponses",
		TargetPattern: "/internal/plugins/frontends/openresponses",
		Reason:        "reference client emulator must not import production OpenResponses frontend",
	},
	{
		SourcePattern: "internal/refclient/openresponses",
		TargetPattern: "/internal/plugins/backends/openresponsescompat",
		Reason:        "reference client emulator must not import production OpenResponses backend",
	},
	{
		SourcePattern: "internal/refclient/openresponses",
		TargetPattern: "/internal/refbackend",
		Reason:        "reference client emulator must not import reference backend emulators",
	},
	{
		SourcePattern: "internal/refclient/openresponses",
		TargetPattern: "/internal/testkit/conformance",
		Reason:        "reference client emulator must not import the conformance matrix",
	},
	{
		SourcePattern: "internal/refbackend/openresponses",
		TargetPattern: "/internal/plugins/protocols/openresponses",
		Reason:        "reference backend emulator must not import production OpenResponses codecs",
	},
	{SourcePattern: "internal/refbackend/openresponses", TargetPattern: "/internal/plugins/frontends/openresponses", Reason: "reference backend emulator must not import production OpenResponses frontend"},
	{SourcePattern: "internal/refbackend/openresponses", TargetPattern: "/internal/plugins/backends/openresponsescompat", Reason: "reference backend emulator must not import production OpenResponses backend"},
	{SourcePattern: "internal/refbackend/openresponses", TargetPattern: "/internal/refclient", Reason: "reference backend emulator wire code must not import reference client wire types"},
	{SourcePattern: "internal/refbackend/openresponses", TargetPattern: "/internal/testkit/conformance", Reason: "reference backend emulator must not import the conformance matrix"},
	{SourcePattern: "internal/refbackend/openresponses", TargetPattern: "/internal/testkit/openresponses", Reason: "reference backend emulator must not import testkit wire contracts"},
	{SourcePattern: "internal/plugins/frontends/openresponses", TargetPattern: "/internal/refbackend", Reason: "production frontend must not import reference backend emulators"},
	{SourcePattern: "internal/plugins/backends/openresponsescompat", TargetPattern: "/internal/refbackend", Reason: "production backend must not import reference backend emulators"},
	{SourcePattern: "internal/testkit/openresponses", TargetPattern: "/internal/plugins/protocols/openresponses", Reason: "testkit contracts must not import production OpenResponses codecs"},
	{SourcePattern: "internal/plugins/backends/openresponsescompat", TargetPattern: "github.com/openai/openai-go", Reason: "generic OpenResponses backend must not import provider SDKs"},
	{SourcePattern: "internal/plugins/backends/openresponsescompat", TargetPattern: "/connectors/", Reason: "generic OpenResponses backend must not import provider connectors"},
	{SourcePattern: "internal/plugins/backends/openresponsescompat", TargetPattern: "/connector-support/", Reason: "generic OpenResponses backend must not import provider connector support"},
	{SourcePattern: "internal/plugins/frontends", TargetPattern: "/internal/core/routeoverride", Reason: "frontend plugins must not import route-override state"},
	{SourcePattern: "internal/plugins/backends", TargetPattern: "/internal/core/routeoverride", Reason: "backend plugins must not import route-override state"},
	{SourcePattern: "pkg/aipapi", TargetPattern: "/internal/core/routeoverride", Reason: "canonical contracts must not import route-override state"},
	{SourcePattern: "pkg/aipsdk", TargetPattern: "/internal/core/routeoverride", Reason: "public SDK must not import route-override state"},
	{
		SourcePattern: "internal/core",
		TargetPattern: "/internal/plugins/features/",
		Reason:        "internal/core must not depend on concrete feature plugins",
	},
	{
		SourcePattern: "internal/core",
		TargetPattern: "/internal/standardplugins/",
		Reason:        "core must not import the standard plugin composition tree, which hosts concrete feature adapters and their vendor wire code",
	},
	{
		SourcePattern: "internal/core/config",
		TargetPattern: "/internal/plugins/features/",
		Reason:        "core config must not depend on concrete feature plugins",
	},
	{
		SourcePattern: "internal/infra/runtimebundle",
		TargetPattern: "/internal/plugins/features/",
		Reason:        "runtimebundle must not depend on concrete feature plugins",
	},
	{
		SourcePattern: "*",
		TargetPattern: "/internal/core/toolcallrepair",
		Reason:        "internal/core/toolcallrepair has been retired; use internal/plugins/features/toolcallrepair/repair",
	},
	{
		SourcePattern: "internal/plugins/features/toolcallrepair",
		TargetPattern: "/internal/core",
		Reason:        "toolcallrepair feature tree must not depend on internal/core (use pkg/aipsdk contracts)",
	},
	{
		SourcePattern: "internal/plugins/features/toolcallrepair",
		TargetPattern: "/internal/infra/runtimebundle",
		Reason:        "toolcallrepair feature tree must not depend on runtimebundle",
	},
	{
		SourcePattern: "internal/plugins/features/toolcallrepair",
		TargetPattern: "/internal/plugins/frontends",
		Reason:        "toolcallrepair feature tree must not depend on frontend plugins",
	},
	{
		SourcePattern: "internal/plugins/features/toolcallrepair",
		TargetPattern: "/internal/plugins/backends",
		Reason:        "toolcallrepair feature tree must not depend on backend plugins",
	},
	{
		SourcePattern: "internal/plugins/features/toolcallrepair",
		TargetPattern: "/internal/plugins/features/",
		Reason:        "toolcallrepair feature tree must not depend on other feature plugins",
		ExceptPrefix: []string{
			"github.com/aiproxer/aiproxer/internal/plugins/features/toolcallrepair",
		},
	},
	{
		SourcePattern: "internal/plugins/features/toolcallrepair",
		TargetPattern: "/internal/stdhttp",
		Reason:        "toolcallrepair feature tree must not depend on stdhttp",
	},
	{
		SourcePattern: "internal/plugins/features/toolcallrepair",
		TargetPattern: "/internal/pluginreg",
		Reason:        "toolcallrepair feature tree must not depend on pluginreg",
	},
	{
		SourcePattern: "*",
		TargetPattern: "/internal/core/secretguard",
		Reason:        "internal/core/secretguard has been retired; use internal/plugins/features/secretguard/engine",
	},
	{
		SourcePattern: "*",
		TargetPattern: "/internal/core/compactiondetect",
		Reason:        "internal/core/compactiondetect has been retired; use internal/infra/compactiondetect",
	},
	{
		SourcePattern: "*",
		TargetPattern: "/internal/core/compactioncontinuity",
		Reason:        "internal/core/compactioncontinuity has been retired; use internal/plugins/features/compactioncontinuity/state",
	},
	{
		SourcePattern: "*",
		TargetPattern: "/internal/core/conversationview",
		Reason:        "internal/core/conversationview has been retired; use internal/core/conversationprojection and internal/infra/conversationview",
	},
	{
		SourcePattern: "internal/core/conversationprojection",
		TargetPattern: "/internal/infra",
		Reason:        "conversationprojection core must not depend on infra",
	},
	{
		SourcePattern: "internal/plugins/features/secretguard",
		TargetPattern: "/internal/core",
		Reason:        "secretguard feature tree must not depend on internal/core (use pkg/aipsdk contracts)",
	},
	{
		SourcePattern: "internal/plugins/features/secretguard",
		TargetPattern: "/internal/infra/runtimebundle",
		Reason:        "secretguard feature tree must not depend on runtimebundle",
	},
	{
		SourcePattern: "internal/plugins/features/secretguard",
		TargetPattern: "/internal/plugins/frontends",
		Reason:        "secretguard feature tree must not depend on frontend plugins",
	},
	{
		SourcePattern: "internal/plugins/features/secretguard",
		TargetPattern: "/internal/plugins/backends",
		Reason:        "secretguard feature tree must not depend on backend plugins",
	},
	{
		SourcePattern: "internal/plugins/features/secretguard",
		TargetPattern: "/internal/plugins/features/",
		Reason:        "secretguard feature tree must not depend on other feature plugins",
		ExceptPrefix: []string{
			"github.com/aiproxer/aiproxer/internal/plugins/features/secretguard",
		},
	},
	{
		SourcePattern: "internal/plugins/features/secretguard",
		TargetPattern: "/internal/stdhttp",
		Reason:        "secretguard feature tree must not depend on stdhttp",
	},
	{
		SourcePattern: "internal/plugins/features/secretguard",
		TargetPattern: "/internal/pluginreg",
		Reason:        "secretguard feature tree must not depend on pluginreg",
	},
	{SourcePattern: "internal/plugins/features/compactioncontinuity", TargetPattern: "/internal/core", Reason: "compactioncontinuity feature tree must not depend on internal/core (use pkg/aipsdk contracts)"},
	{SourcePattern: "internal/plugins/features/compactioncontinuity", TargetPattern: "/internal/infra/runtimebundle", Reason: "compactioncontinuity feature tree must not depend on runtimebundle"},
	{SourcePattern: "internal/plugins/features/compactioncontinuity", TargetPattern: "/internal/plugins/frontends", Reason: "compactioncontinuity feature tree must not depend on frontend plugins"},
	{SourcePattern: "internal/plugins/features/compactioncontinuity", TargetPattern: "/internal/plugins/backends", Reason: "compactioncontinuity feature tree must not depend on backend plugins"},
	{
		SourcePattern: "internal/plugins/features/compactioncontinuity",
		TargetPattern: "/internal/plugins/features/",
		Reason:        "compactioncontinuity feature tree must not depend on other feature plugins",
		ExceptPrefix: []string{
			"github.com/aiproxer/aiproxer/internal/plugins/features/compactioncontinuity",
		},
	},
	{SourcePattern: "internal/plugins/features/compactioncontinuity", TargetPattern: "/internal/stdhttp", Reason: "compactioncontinuity feature tree must not depend on stdhttp"},
	{SourcePattern: "internal/plugins/features/compactioncontinuity", TargetPattern: "/internal/pluginreg", Reason: "compactioncontinuity feature tree must not depend on pluginreg"},
	{SourcePattern: "*", TargetPattern: "/internal/core/interleavedthinking", Reason: "internal/core/interleavedthinking has been retired; use internal/plugins/features/interleavedthinking"},
	{SourcePattern: "internal/plugins/features/interleavedthinking", TargetPattern: "/internal/core", Reason: "interleavedthinking feature tree must not depend on internal/core (use pkg/aipsdk contracts)"},
	{SourcePattern: "internal/plugins/features/interleavedthinking", TargetPattern: "/internal/infra/runtimebundle", Reason: "interleavedthinking feature tree must not depend on runtimebundle"},
	{SourcePattern: "internal/plugins/features/interleavedthinking", TargetPattern: "/internal/plugins/frontends", Reason: "interleavedthinking feature tree must not depend on frontend plugins"},
	{SourcePattern: "internal/plugins/features/interleavedthinking", TargetPattern: "/internal/plugins/backends", Reason: "interleavedthinking feature tree must not depend on backend plugins"},
	{
		SourcePattern: "internal/plugins/features/interleavedthinking",
		TargetPattern: "/internal/plugins/features/",
		Reason:        "interleavedthinking feature tree must not depend on other feature plugins",
		ExceptPrefix: []string{
			"github.com/aiproxer/aiproxer/internal/plugins/features/interleavedthinking",
		},
	},
	{SourcePattern: "internal/plugins/features/interleavedthinking", TargetPattern: "/internal/stdhttp", Reason: "interleavedthinking feature tree must not depend on stdhttp"},
	{SourcePattern: "internal/plugins/features/interleavedthinking", TargetPattern: "/internal/pluginreg", Reason: "interleavedthinking feature tree must not depend on pluginreg"},
	{SourcePattern: "*", TargetPattern: "/internal/core/keepwarm", Reason: "internal/core/keepwarm has been retired; use internal/plugins/features/keepwarm"},
	{SourcePattern: "internal/plugins/features/keepwarm", TargetPattern: "/internal/core", Reason: "keepwarm feature tree must not depend on internal/core (use pkg/aipsdk contracts)"},
	{SourcePattern: "internal/plugins/features/keepwarm", TargetPattern: "/internal/infra/runtimebundle", Reason: "keepwarm feature tree must not depend on runtimebundle"},
	{SourcePattern: "internal/plugins/features/keepwarm", TargetPattern: "/internal/plugins/frontends", Reason: "keepwarm feature tree must not depend on frontend plugins"},
	{SourcePattern: "internal/plugins/features/keepwarm", TargetPattern: "/internal/plugins/backends", Reason: "keepwarm feature tree must not depend on backend plugins"},
	{
		SourcePattern: "internal/plugins/features/keepwarm",
		TargetPattern: "/internal/plugins/features/",
		Reason:        "keepwarm feature tree must not depend on other feature plugins",
		ExceptPrefix: []string{
			"github.com/aiproxer/aiproxer/internal/plugins/features/keepwarm",
		},
	},
	{SourcePattern: "internal/plugins/features/keepwarm", TargetPattern: "/internal/stdhttp", Reason: "keepwarm feature tree must not depend on stdhttp"},
	{SourcePattern: "internal/plugins/features/keepwarm", TargetPattern: "/internal/pluginreg", Reason: "keepwarm feature tree must not depend on pluginreg"},
	{SourcePattern: "*", TargetPattern: "/internal/core/terminaldecisionpolicy", Reason: "internal/core/terminaldecisionpolicy has been retired; use internal/standardplugins/featurehost/sessionpolicy"},
	{SourcePattern: "*", TargetPattern: "/internal/reasoningreplay", Reason: "internal/reasoningreplay has been retired; use internal/plugins/features/reasoningpreservation/reasoningreplay"},
	{SourcePattern: "*", TargetPattern: "/internal/infra/compactioncompose", Reason: "internal/infra/compactioncompose has been retired; use internal/standardplugins/featurehost/compaction and internal/infra/auxiliary"},
	{SourcePattern: "*", TargetPattern: "/internal/infra/reasoningcompose", Reason: "internal/infra/reasoningcompose has been retired; use internal/standardplugins/featurehost/reasoning"},
	{SourcePattern: "*", TargetPattern: "/internal/infra/secretguardcompose", Reason: "internal/infra/secretguardcompose has been retired; use internal/standardplugins/featurehost/secretguard"},
	{SourcePattern: "*", TargetPattern: "/internal/infra/secretaudit", Reason: "internal/infra/secretaudit has been retired; use internal/standardplugins/featurehost/secretguard"},
}

// fileScopedImportRule restricts specific production files.
type fileScopedImportRule struct {
	File          string
	TargetPattern string
	Reason        string
}

var fileScopedImportRules = []fileScopedImportRule{
	{File: "internal/infra/runtimebundle/inspect.go", TargetPattern: "github.com/aiproxer/aiproxer/internal/infra/runtimehost", Reason: "inspect purity"},
	{File: "internal/infra/runtimebundle/inspect.go", TargetPattern: "github.com/aiproxer/aiproxer/internal/stdhttp", Reason: "inspect purity"},
	{File: "internal/infra/runtimebundle/inspect.go", TargetPattern: "log/slog", Reason: "inspect purity"},
	{File: "internal/infra/runtimebundle/validate_distribution.go", TargetPattern: "github.com/aiproxer/aiproxer/internal/infra/runtimehost", Reason: "validation purity"},
	{File: "internal/infra/runtimebundle/validate_distribution.go", TargetPattern: "github.com/aiproxer/aiproxer/internal/stdhttp", Reason: "validation purity"},
	{File: "internal/stdhttp/generation_host.go", TargetPattern: "github.com/aiproxer/aiproxer/internal/infra/runtimebundle", Reason: "serve adapter drops ownership imports"},
	{File: "internal/stdhttp/generation_host.go", TargetPattern: "github.com/aiproxer/aiproxer/internal/infra/runtimehost", Reason: "serve adapter drops ownership imports"},
}

// ScanFileForbiddenImports checks a single file's source for forbidden imports.
func ScanFileForbiddenImports(rel, abs string, src []byte) ([]RuleFinding, error) {
	pkg := PackageDirFromRel(rel)
	_, f, err := ParseGoSource(abs, src)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", rel, err)
	}
	var out []RuleFinding
	imports := FileImportPaths(f)
	for _, rule := range allForbiddenImportRules() {
		if !MatchPathPrefix(pkg, rule.SourcePattern) {
			continue
		}
		for _, imp := range imports {
			if importExemptPrefix(imp, rule.ExceptPrefix) {
				continue
			}
			if matchImportTarget(imp, rule.TargetPattern) {
				out = append(out, RuleFinding{
					Rule:   "forbidden_import",
					Path:   rel,
					Detail: "import " + imp + " (" + rule.Reason + ")",
				})
			}
		}
	}
	for _, rule := range fileScopedImportRules {
		if rel != rule.File {
			continue
		}
		for _, imp := range imports {
			if matchImportTarget(imp, rule.TargetPattern) {
				// Root stdhttp forbid should not match stdhttp/contract subpath.
				if rule.TargetPattern == "github.com/aiproxer/aiproxer/internal/stdhttp" &&
					strings.HasPrefix(imp, "github.com/aiproxer/aiproxer/internal/stdhttp/") {
					continue
				}
				out = append(out, RuleFinding{
					Rule:   "forbidden_import",
					Path:   rel,
					Detail: "import " + imp + " (" + rule.Reason + ")",
				})
			}
		}
	}
	return out, nil
}

// ScanForbiddenImports reports production import edges matching ForbiddenImports
// and file-scoped purity rules.
func ScanForbiddenImports(root string) ([]RuleFinding, error) {
	var out []RuleFinding
	err := WalkProductionGoFiles(root, func(rel, abs string, src []byte) error {
		findings, err := ScanFileForbiddenImports(rel, abs, src)
		if err != nil {
			return err
		}
		out = append(out, findings...)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out, nil
}

func importExemptPrefix(imp string, except []string) bool {
	for _, e := range except {
		if imp == e || strings.HasPrefix(imp, e+"/") {
			return true
		}
	}
	return false
}

func matchImportTarget(imp, pattern string) bool {
	switch {
	case pattern == "":
		return false
	case strings.HasPrefix(pattern, "/") && strings.Contains(pattern, "/internal/"):
		trimmed := strings.TrimSuffix(pattern, "/")
		return strings.Contains(imp, trimmed+"/") || strings.HasSuffix(imp, trimmed) || strings.Contains(imp, trimmed+" ") || strings.Contains(imp, trimmed+"\"") || strings.Contains(imp, trimmed+")")
	case strings.HasPrefix(pattern, "*") && strings.HasSuffix(pattern, "*"):
		return strings.Contains(imp, strings.Trim(pattern, "*"))
	case imp == pattern:
		return true
	case strings.HasPrefix(imp, pattern+"/"):
		// Exact package forbid does not include subpackages.
		return false
	default:
		return MatchImportPattern(imp, pattern)
	}
}
