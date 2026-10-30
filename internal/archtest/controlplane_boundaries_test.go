package archtest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestControlPlaneSDKDoesNotImportCoreOrInfra enforces the public control-plane
// SDK contract boundary: pkg/aipsdk/controlplane must not depend on internal/core,
// internal/infra, internal/plugins, internal/stdhttp, SQL/Bun, net/http, or
// provider SDK packages (requirements 9.5, 10.5; design "Allowed Dependencies";
// task 6.5).
func TestControlPlaneSDKDoesNotImportCoreOrInfra(t *testing.T) {
	t.Parallel()
	assertDepsExcludeForbidden(t, []string{"./pkg/aipsdk/controlplane"}, []forbiddenDep{
		{Substr: "/internal/core", ErrMsg: "control-plane SDK must not depend on internal/core"},
		{Substr: "/internal/infra", ErrMsg: "control-plane SDK must not depend on internal/infra"},
		{Substr: "/internal/plugins", ErrMsg: "control-plane SDK must not depend on concrete plugins"},
		{Substr: "/internal/stdhttp", ErrMsg: "control-plane SDK must not depend on stdhttp"},
		{Substr: "/internal/pluginreg", ErrMsg: "control-plane SDK must not depend on pluginreg"},
		{Substr: "database/sql", ErrMsg: "control-plane SDK must not depend on database/sql"},
		{Substr: "uptrace/bun", ErrMsg: "control-plane SDK must not depend on Bun"},
		{Substr: "modernc.org/sqlite", ErrMsg: "control-plane SDK must not depend on sqlite driver"},
		{Substr: "net/http", ErrMsg: "control-plane SDK must not depend on net/http"},
		{Substr: "github.com/openai/openai-go", ErrMsg: "control-plane SDK must not depend on OpenAI SDK"},
		{Substr: "github.com/anthropics/anthropic-sdk-go", ErrMsg: "control-plane SDK must not depend on Anthropic SDK"},
		{Substr: "google.golang.org/genai", ErrMsg: "control-plane SDK must not depend on Gemini SDK"},
		{Substr: "github.com/aws/aws-sdk-go-v2", ErrMsg: "control-plane SDK must not depend on AWS SDK"},
	})
}

// TestCoreControlPlaneDoesNotImportProviderSDKsOrConcretePlugins enforces that
// the core control-plane package stays free of provider SDKs, concrete plugins,
// SQL/Bun, net/http, and composition-root packages (requirements 9.5, 10.5;
// task 6.5).
func TestCoreControlPlaneDoesNotImportProviderSDKsOrConcretePlugins(t *testing.T) {
	t.Parallel()
	assertDepsExcludeForbidden(t, []string{"./internal/core/controlplane"}, []forbiddenDep{
		{Substr: "/internal/plugins", ErrMsg: "core control-plane must not depend on concrete plugins"},
		{Substr: "/internal/stdhttp", ErrMsg: "core control-plane must not depend on stdhttp"},
		{Substr: "/internal/pluginreg", ErrMsg: "core control-plane must not depend on pluginreg"},
		{Substr: "/internal/infra", ErrMsg: "core control-plane must not depend on internal/infra"},
		{Substr: "database/sql", ErrMsg: "core control-plane must not depend on database/sql"},
		{Substr: "uptrace/bun", ErrMsg: "core control-plane must not depend on Bun"},
		{Substr: "modernc.org/sqlite", ErrMsg: "core control-plane must not depend on sqlite driver"},
		{Substr: "net/http", ErrMsg: "core control-plane must not depend on net/http"},
		{Substr: "github.com/openai/openai-go", ErrMsg: "core control-plane must not depend on OpenAI SDK"},
		{Substr: "github.com/anthropics/anthropic-sdk-go", ErrMsg: "core control-plane must not depend on Anthropic SDK"},
		{Substr: "google.golang.org/genai", ErrMsg: "core control-plane must not depend on Gemini SDK"},
		{Substr: "github.com/aws/aws-sdk-go-v2", ErrMsg: "core control-plane must not depend on AWS SDK"},
	})
}

// TestControlPlaneObserversDoNotImportProtocolTranslators enforces the no-pairwise-
// protocol-translator and no-provider-telemetry-forwarding guardrails: source
// adapter packages must not import frontend or backend protocol plugin packages,
// runtimebundle, stdhttp, or provider SDKs (requirements 5.1, 5.6, 9.5, 10.5,
// 10.7; task 6.5).
func TestControlPlaneObserversDoNotImportProtocolTranslators(t *testing.T) {
	t.Parallel()
	assertDepsExcludeForbidden(t, []string{"./internal/infra/controlplane/observers"}, []forbiddenDep{
		{Substr: "/internal/plugins/frontends", ErrMsg: "control-plane observers must not import frontend protocol plugins (no pairwise translator)"},
		{Substr: "/internal/plugins/backends", ErrMsg: "control-plane observers must not import backend protocol plugins (no pairwise translator)"},
		{Substr: "/internal/plugins/features", ErrMsg: "control-plane observers must not import feature plugins"},
		{Substr: "/internal/infra/runtimebundle", ErrMsg: "control-plane observers must not import runtimebundle"},
		{Substr: "/internal/stdhttp", ErrMsg: "control-plane observers must not import stdhttp"},
		{Substr: "database/sql", ErrMsg: "control-plane observers must not depend on database/sql"},
		{Substr: "uptrace/bun", ErrMsg: "control-plane observers must not depend on Bun"},
		{Substr: "github.com/openai/openai-go", ErrMsg: "control-plane observers must not depend on OpenAI SDK"},
		{Substr: "github.com/anthropics/anthropic-sdk-go", ErrMsg: "control-plane observers must not depend on Anthropic SDK"},
		{Substr: "google.golang.org/genai", ErrMsg: "control-plane observers must not depend on Gemini SDK"},
		{Substr: "github.com/aws/aws-sdk-go-v2", ErrMsg: "control-plane observers must not depend on AWS SDK"},
	})
}

// TestControlPlaneHTTPAdapterIsNotClientFacingLLMProtocol enforces that the
// protected control-plane HTTP query adapter does not import frontend wire
// packages or pkg/aipapi request/response contracts, so it cannot become a
// client-facing LLM protocol response path (requirements 5.1, 9.5, 10.5; task 6.5).
func TestControlPlaneHTTPAdapterIsNotClientFacingLLMProtocol(t *testing.T) {
	t.Parallel()
	assertDirectImportsExclude(t, "./internal/stdhttp/admin/controlplane", "/internal/plugins/frontends",
		"control-plane HTTP adapter must not import frontend plugins (not a client-facing LLM protocol path)")
	assertDirectImportsExclude(t, "./internal/stdhttp/admin/controlplane", "/internal/plugins/backends",
		"control-plane HTTP adapter must not import backend plugins")
	assertDirectImportsExclude(t, "./internal/stdhttp/admin/controlplane", "github.com/openai/openai-go",
		"control-plane HTTP adapter must not import OpenAI SDK")
	assertDirectImportsExclude(t, "./internal/stdhttp/admin/controlplane", "github.com/anthropics/anthropic-sdk-go",
		"control-plane HTTP adapter must not import Anthropic SDK")
	assertDirectImportsExclude(t, "./internal/stdhttp/admin/controlplane", "/pkg/aipapi",
		"control-plane HTTP adapter must not import pkg/aipapi request/response contracts")
}

// TestControlPlanePackagesHaveNoInitFunctions enforces the no-hidden-background-
// worker guardrail: no non-test control-plane source file may declare init(),
// so the capability is constructed explicitly by the composition root and never
// self-starts goroutines or registries (requirements 5.1, 5.6, 5.7, 10.7; task 6.5).
func TestControlPlanePackagesHaveNoInitFunctions(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	dirs := []string{
		filepath.Join(root, "pkg", "aipsdk", "controlplane"),
		filepath.Join(root, "internal", "core", "controlplane"),
		filepath.Join(root, "internal", "infra", "controlplane"),
		filepath.Join(root, "internal", "stdhttp", "admin", "controlplane"),
	}
	for _, dir := range dirs {
		t.Run(strings.TrimPrefix(dir, root+string(filepath.Separator)), func(t *testing.T) {
			t.Parallel()
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
				if hasInitFunc(path) {
					t.Fatalf("forbid init() in control-plane path (explicit construction only): %s", path)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

// controlPlaneEvidenceImportSubstrs are the control-plane evidence/recording/query
// packages that must never leak into provider, protocol, or client-facing paths.
// Protocol plugins and the LLM proxy request handler are not control-plane
// consumers; only the composition root (runtimebundle) and the protected operator
// admin handler may wire them (requirements 5.1, 5.6, 9.5, 10.5, 10.7; task 6.5,
// phase 6 risk closure).
var controlPlaneEvidenceImportSubstrs = []string{
	"/internal/core/controlplane",
	"/internal/infra/controlplane",
	"/internal/stdhttp/admin/controlplane",
	"/pkg/aipsdk/controlplane",
}

// TestProtocolPluginsDoNotImportControlPlaneEvidence enforces the reverse
// direction of the control-plane boundary: official frontend, backend, and
// feature plugins must not directly import control-plane evidence recorders,
// durable stores, observers, the operator query adapter, or the control-plane
// query SDK. This proves the feature introduced no pairwise protocol translator
// or provider-telemetry-forwarding path that funnels client protocol traffic
// into control-plane evidence (requirements 5.1, 5.6, 9.5, 10.5, 10.7; task 6.5).
func TestProtocolPluginsDoNotImportControlPlaneEvidence(t *testing.T) {
	t.Parallel()
	patterns := []string{
		"./internal/plugins/frontends/...",
		"./internal/plugins/backends/...",
		"./internal/plugins/features/...",
	}
	for _, p := range patterns {
		t.Run(p, func(t *testing.T) {
			t.Parallel()
			for _, substr := range controlPlaneEvidenceImportSubstrs {
				assertDirectImportsExclude(t, p, substr,
					"protocol plugins must not import control-plane evidence/query packages (no pairwise translator, no provider telemetry forwarding)")
			}
		})
	}
}

// TestClientFacingStdhttpDoesNotImportControlPlaneEvidence enforces that the
// LLM proxy driving adapter (internal/stdhttp) does not directly import
// control-plane evidence recorders or durable stores/observers. The operator
// admin handler (internal/stdhttp/admin/controlplane) is the only legitimate
// control-plane consumer in the HTTP layer and is excluded; the client-facing
// request path must not touch evidence sinks so control-plane records cannot
// leak into provider/protocol/client-facing responses (requirements 5.1, 9.5,
// 10.5, 10.7; task 6.5, phase 6 risk closure).
func TestClientFacingStdhttpDoesNotImportControlPlaneEvidence(t *testing.T) {
	t.Parallel()
	for _, substr := range []string{
		"/internal/core/controlplane",
		"/internal/infra/controlplane",
	} {
		assertDirectImportsExcludeExcept(t, "./internal/stdhttp/...", substr, "/stdhttp/admin/controlplane",
			"client-facing stdhttp request path must not import control-plane evidence/recorders directly (route via admin handler/composition root only)")
	}
}
