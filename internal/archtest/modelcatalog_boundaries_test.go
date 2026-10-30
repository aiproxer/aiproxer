package archtest

import "testing"

// TestCoreModelCatalogImportClosure enforces that core model catalog logic stays free of
// models.dev adapters, plugin packages, and vendor SDKs (spec model-capabilities-catalog 7.2).
func TestCoreModelCatalogImportClosure(t *testing.T) {
	t.Parallel()
	assertDepsExcludeForbidden(t, []string{"./internal/core/modelcatalog/..."}, []forbiddenDep{
		{
			Substr: "/internal/infra/modelcatalog",
			ErrMsg: "internal/core/modelcatalog must not import internal/infra/modelcatalog (use core ports; adapters live in infra)",
		},
		{
			Substr: "/internal/plugins/",
			ErrMsg: "internal/core/modelcatalog must not import protocol plugins",
		},
		{
			Substr: "github.com/openai/openai-go",
			ErrMsg: "internal/core/modelcatalog must not depend on OpenAI SDK",
		},
		{
			Substr: "github.com/anthropics/anthropic-sdk-go",
			ErrMsg: "internal/core/modelcatalog must not depend on Anthropic SDK",
		},
		{
			Substr: "google.golang.org/genai",
			ErrMsg: "internal/core/modelcatalog must not depend on Google GenAI SDK",
		},
	})
}

// TestInfraModelCatalogImportClosure keeps the models.dev adapter layer plugin- and SDK-free at the repository seam.
func TestInfraModelCatalogImportClosure(t *testing.T) {
	t.Parallel()
	assertDepsExcludeForbidden(t, []string{"./internal/infra/modelcatalog/..."}, []forbiddenDep{
		{
			Substr: "/internal/plugins/",
			ErrMsg: "internal/infra/modelcatalog must not import protocol plugins",
		},
		{
			Substr: "github.com/openai/openai-go",
			ErrMsg: "internal/infra/modelcatalog must not depend on OpenAI SDK",
		},
	})
}

// TestPkgAipapiDoesNotImportModelCatalogInfra documents that canonical contracts never pull models.dev wire packages.
func TestPkgAipapiDoesNotImportModelCatalogInfra(t *testing.T) {
	t.Parallel()
	assertDepsExcludeForbidden(t, []string{"./pkg/aipapi/..."}, []forbiddenDep{
		{
			Substr: "/internal/infra/modelcatalog",
			ErrMsg: "pkg/aipapi must not import model catalog infra (models.dev schema is non-contract)",
		},
	})
}
