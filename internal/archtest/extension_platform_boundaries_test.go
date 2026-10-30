package archtest

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

type goListPackage struct {
	ImportPath string   `json:"ImportPath"`
	Standard   bool     `json:"Standard"`
	Imports    []string `json:"Imports"`
}

// forbiddenDep is a substring match against ImportPath from `go list -deps -test=false`.
type forbiddenDep struct {
	Substr string
	ErrMsg string
}

// publicContractVendorSDKDeps: official provider SDKs must not appear in pkg/aipapi or pkg/aipsdk
// (extension platform + hexagonal public-contract rules).
var publicContractVendorSDKDeps = []forbiddenDep{
	{
		Substr: "github.com/anthropics/anthropic-sdk-go",
		ErrMsg: "public contract packages must not depend on Anthropic SDK",
	},
	{
		Substr: "github.com/openai/openai-go",
		ErrMsg: "public contract packages must not depend on OpenAI Go SDK",
	},
	{
		Substr: "google.golang.org/genai",
		ErrMsg: "public contract packages must not depend on Google GenAI SDK",
	},
	{
		Substr: "github.com/aws/aws-sdk-go-v2/service/bedrockruntime",
		ErrMsg: "public contract packages must not depend on Bedrock runtime SDK",
	},
}

// publicAipsdkForbiddenDeps is the full hexagonal public-contract surface for ./pkg/aipsdk/...
// (introduce-hexagonal-architecture task 2.1): no internal orchestration, wiring roots, or vendor SDKs.
var publicAipsdkForbiddenDeps = append([]forbiddenDep{
	{
		Substr: "github.com/aiproxer/aiproxer/internal/",
		ErrMsg: "pkg/aipsdk must not depend on internal packages " +
			"(orchestration lives in internal/core; same-module internal/ " +
			"imports are otherwise unconstrained by the compiler)",
	},
	{
		Substr: "/internal/pluginreg",
		ErrMsg: "pkg/aipsdk must not depend on pluginreg (composition root)",
	},
	{
		Substr: "/internal/infra/runtimebundle",
		ErrMsg: "pkg/aipsdk must not depend on runtimebundle (composition assembly)",
	},
	{
		Substr: "/internal/stdhttp",
		ErrMsg: "pkg/aipsdk must not depend on stdhttp (driving adapter / HTTP server layer)",
	},
}, publicContractVendorSDKDeps...)

// publicAipapiForbiddenDeps is the same policy for ./pkg/aipapi/... (canonical contract only).
var publicAipapiForbiddenDeps = append([]forbiddenDep{
	{
		Substr: "github.com/aiproxer/aiproxer/internal/",
		ErrMsg: "pkg/aipapi must not depend on internal packages " +
			"(contract surface only; same-module internal/ imports are " +
			"otherwise unconstrained by the compiler)",
	},
	{
		Substr: "/internal/pluginreg",
		ErrMsg: "pkg/aipapi must not depend on pluginreg (composition root)",
	},
	{
		Substr: "/internal/infra/runtimebundle",
		ErrMsg: "pkg/aipapi must not depend on runtimebundle (composition assembly)",
	},
	{
		Substr: "/internal/stdhttp",
		ErrMsg: "pkg/aipapi must not depend on stdhttp (driving adapter / HTTP server layer)",
	},
}, publicContractVendorSDKDeps...)

// TestPkgAipapiPublicContractDoesNotImportInternalOrWiring ensures the canonical public contract
// does not take dependencies on the policy core, composition roots, transport server, or provider SDKs
// (introduce-hexagonal-architecture, task 2.1).
func TestPkgAipapiPublicContractDoesNotImportInternalOrWiring(t *testing.T) {
	t.Parallel()
	assertDepsExcludeForbidden(t, []string{"./pkg/aipapi/..."}, publicAipapiForbiddenDeps)
}

// TestPkgAipsdkDoesNotDependOnVendorSDKs ensures the stable feature/plugin SDK does not pull
// official provider client modules (Q2 / design section 18), and (task 2.1) does not import internal/,
// composition roots, or stdhttp.
func TestPkgAipsdkDoesNotDependOnVendorSDKs(t *testing.T) {
	t.Parallel()
	assertDepsExcludeForbidden(t, []string{"./pkg/aipsdk/..."}, publicAipsdkForbiddenDeps)
}

// featurePluginsForbiddenDeps: official feature plugins must consume extension/runtime only through
// pkg/aipsdk and pkg/aipapi (introduce-hexagonal-architecture task 5.3).
var featurePluginsForbiddenDeps = []forbiddenDep{
	{
		Substr: "/internal/core/",
		ErrMsg: "internal/plugins/features must not depend on internal/core (use pkg/aipsdk contracts)",
	},
}

// TestOfficialFeaturePluginsDoNotDependOnInternalCore ensures feature plugin packages do not pull
// orchestration or extension wiring from internal/core; contract tests keep the seam replaceable.
func TestOfficialFeaturePluginsDoNotDependOnInternalCore(t *testing.T) {
	t.Parallel()
	assertDepsExcludeForbidden(t, []string{"./internal/plugins/features/..."}, featurePluginsForbiddenDeps)
}

func TestProtocolAdaptersDoNotDependOnToolCallRepair(t *testing.T) {
	t.Parallel()
	assertDepsExcludeForbidden(t, []string{
		"./internal/plugins/frontends/...",
		"./internal/plugins/backends/...",
	}, []forbiddenDep{
		{
			Substr: "/internal/core/toolcallrepair",
			ErrMsg: "protocol adapters must not depend on the canonical tool-call repair engine",
		},
		{
			Substr: "/internal/plugins/features/toolcallrepair",
			ErrMsg: "protocol adapters must not depend on the tool-call repair feature plugin",
		},
	})
}

// TestToolCallRepairFeatureTreeBoundaries enforces that the recursive toolcallrepair feature tree
// depends only on canonical/SDK contracts, standard library, and feature-local code (Requirements 2.4, 7.1).
func TestToolCallRepairFeatureTreeBoundaries(t *testing.T) {
	t.Parallel()
	assertDepsExcludeForbidden(t, []string{"./internal/plugins/features/toolcallrepair/..."}, []forbiddenDep{
		{
			Substr: "/internal/core/",
			ErrMsg: "toolcallrepair feature tree must not depend on internal/core",
		},
		{
			Substr: "/internal/infra/runtimebundle",
			ErrMsg: "toolcallrepair feature tree must not depend on runtimebundle",
		},
		{
			Substr: "/internal/plugins/frontends/",
			ErrMsg: "toolcallrepair feature tree must not depend on frontend plugins",
		},
		{
			Substr: "/internal/plugins/backends/",
			ErrMsg: "toolcallrepair feature tree must not depend on backend plugins",
		},
		{
			Substr: "/internal/stdhttp",
			ErrMsg: "toolcallrepair feature tree must not depend on stdhttp",
		},
		{
			Substr: "/internal/pluginreg",
			ErrMsg: "toolcallrepair feature tree must not depend on pluginreg",
		},
	})
}

// TestSecretGuardFeatureTreeBoundaries enforces that the recursive secretguard feature tree
// depends only on canonical/SDK contracts, standard library, and feature-local code (Requirements 3.5, 7.1).
func TestSecretGuardFeatureTreeBoundaries(t *testing.T) {
	t.Parallel()
	assertDepsExcludeForbidden(t, []string{"./internal/plugins/features/secretguard/..."}, []forbiddenDep{
		{
			Substr: "/internal/core/",
			ErrMsg: "secretguard feature tree must not depend on internal/core",
		},
		{
			Substr: "/internal/infra/runtimebundle",
			ErrMsg: "secretguard feature tree must not depend on runtimebundle",
		},
		{
			Substr: "/internal/plugins/frontends/",
			ErrMsg: "secretguard feature tree must not depend on frontend plugins",
		},
		{
			Substr: "/internal/plugins/backends/",
			ErrMsg: "secretguard feature tree must not depend on backend plugins",
		},
		{
			Substr: "/internal/stdhttp",
			ErrMsg: "secretguard feature tree must not depend on stdhttp",
		},
		{
			Substr: "/internal/pluginreg",
			ErrMsg: "secretguard feature tree must not depend on pluginreg",
		},
	})
}

// TestInterleavedThinkingFeatureTreeBoundaries enforces that the recursive interleavedthinking feature tree
// depends only on canonical/SDK contracts, standard library, and feature-local code (Requirements 5.2, 5.6, 12.1).
func TestInterleavedThinkingFeatureTreeBoundaries(t *testing.T) {
	t.Parallel()
	assertDepsExcludeForbidden(t, []string{"./internal/plugins/features/interleavedthinking/..."}, []forbiddenDep{
		{
			Substr: "/internal/core/",
			ErrMsg: "interleavedthinking feature tree must not depend on internal/core",
		},
		{
			Substr: "/internal/infra/runtimebundle",
			ErrMsg: "interleavedthinking feature tree must not depend on runtimebundle",
		},
		{
			Substr: "/internal/plugins/frontends/",
			ErrMsg: "interleavedthinking feature tree must not depend on frontend plugins",
		},
		{
			Substr: "/internal/plugins/backends/",
			ErrMsg: "interleavedthinking feature tree must not depend on backend plugins",
		},
		{
			Substr: "/internal/stdhttp",
			ErrMsg: "interleavedthinking feature tree must not depend on stdhttp",
		},
		{
			Substr: "/internal/pluginreg",
			ErrMsg: "interleavedthinking feature tree must not depend on pluginreg",
		},
	})
}

// TestKeepwarmFeatureTreeBoundaries enforces that the recursive keepwarm feature tree
// depends only on canonical/SDK contracts, standard library, and feature-local code.
func TestKeepwarmFeatureTreeBoundaries(t *testing.T) {
	t.Parallel()
	assertDepsExcludeForbidden(t, []string{"./internal/plugins/features/keepwarm/..."}, []forbiddenDep{
		{
			Substr: "/internal/core/",
			ErrMsg: "keepwarm feature tree must not depend on internal/core",
		},
		{
			Substr: "/internal/infra/runtimebundle",
			ErrMsg: "keepwarm feature tree must not depend on runtimebundle",
		},
		{
			Substr: "/internal/plugins/frontends/",
			ErrMsg: "keepwarm feature tree must not depend on frontend plugins",
		},
		{
			Substr: "/internal/plugins/backends/",
			ErrMsg: "keepwarm feature tree must not depend on backend plugins",
		},
		{
			Substr: "/internal/stdhttp",
			ErrMsg: "keepwarm feature tree must not depend on stdhttp",
		},
		{
			Substr: "/internal/pluginreg",
			ErrMsg: "keepwarm feature tree must not depend on pluginreg",
		},
	})
}

// TestInternalCoreDoesNotDependOnStdhttpOrProtocolPlugins keeps orchestration free of the HTTP
// server layer, official protocol plugins, and transport-labeled SDK paths (introduce-hexagonal
// task 4.1). Principal context uses [github.com/.../pkg/aipsdk/execview] from core instead.
func TestInternalCoreDoesNotDependOnStdhttpOrProtocolPlugins(t *testing.T) {
	t.Parallel()
	assertDepsExcludeForbidden(t, []string{"./internal/core/..."}, []forbiddenDep{
		{
			Substr: "/internal/stdhttp",
			ErrMsg: "internal/core must not depend on stdhttp (transport/server layer)",
		},
		{
			Substr: "/internal/plugins/frontends/",
			ErrMsg: "internal/core must not depend on concrete frontend plugins",
		},
		{
			Substr: "/internal/plugins/backends/",
			ErrMsg: "internal/core must not depend on concrete backend plugins",
		},
		{
			Substr: "/internal/plugins/features/",
			ErrMsg: "internal/core must not depend on concrete feature plugins",
		},
		{
			Substr: "/pkg/aipsdk/transport/",
			ErrMsg: "internal/core must not depend on pkg/aipsdk/transport " +
				"(driving-adapter/transport layer; use execview principal " +
				"context from core)",
		},
	})
}

// TestInternalCoreRuntimeDoesNotImportNetHTTP keeps protocol decode, encode, and wire handling in
// driving adapters (introduce-hexagonal-architecture 4.2): the executor path must not depend
// on net/http directly. Subpackages of internal/core/runtime are the same [runtime] module package.
func TestInternalCoreRuntimeDoesNotImportNetHTTP(t *testing.T) {
	t.Parallel()
	out, err := cachedGoList(t, "-json", "-test=false", "./internal/core/runtime")
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	dec := json.NewDecoder(bytes.NewReader(out))
	if !dec.More() {
		t.Fatal("go list: empty output")
	}
	var pkg goListPackage
	if err := dec.Decode(&pkg); err != nil {
		t.Fatalf("decode: %v", err)
	}
	const wantPath = "github.com/aiproxer/aiproxer/internal/core/runtime"
	if pkg.ImportPath != wantPath {
		t.Fatalf("unexpected package: got %q want %q", pkg.ImportPath, wantPath)
	}
	for _, imp := range pkg.Imports {
		if imp == "net/http" {
			t.Fatalf(
				"internal/core/runtime must not import net/http (keep HTTP at driving adapters); "+
					"found direct import in %s",
				pkg.ImportPath,
			)
		}
	}
}

func assertDepsExcludeForbidden(t *testing.T, patterns []string, rules []forbiddenDep) {
	t.Helper()
	args := append([]string{"-deps", "-test=false", "-json"}, patterns...)
	out, err := cachedGoList(t, args...)
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	dec := json.NewDecoder(bytes.NewReader(out))
	for dec.More() {
		var pkg goListPackage
		if err := dec.Decode(&pkg); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if pkg.Standard {
			continue
		}
		for _, r := range rules {
			if strings.Contains(pkg.ImportPath, r.Substr) {
				t.Fatalf("%s: %s", r.ErrMsg, pkg.ImportPath)
			}
		}
	}
}
