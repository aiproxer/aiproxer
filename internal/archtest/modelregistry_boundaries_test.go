package archtest

import "testing"

func TestCoreModelRegistryImportClosure(t *testing.T) {
	t.Parallel()
	assertDepsExcludeForbidden(t, []string{"./internal/core/modelregistry/..."}, []forbiddenDep{
		{
			Substr: "/internal/infra/modelcatalog",
			ErrMsg: "internal/core/modelregistry must not import model catalog infrastructure",
		},
		{
			Substr: "/internal/plugins/",
			ErrMsg: "internal/core/modelregistry must not import protocol plugins",
		},
		{
			Substr: "github.com/openai/openai-go",
			ErrMsg: "internal/core/modelregistry must not depend on OpenAI SDK",
		},
		{
			Substr: "github.com/anthropics/anthropic-sdk-go",
			ErrMsg: "internal/core/modelregistry must not depend on Anthropic SDK",
		},
		{
			Substr: "google.golang.org/genai",
			ErrMsg: "internal/core/modelregistry must not depend on Google GenAI SDK",
		},
		{
			Substr: "github.com/aws/aws-sdk-go-v2",
			ErrMsg: "internal/core/modelregistry must not depend on AWS SDK",
		},
	})
}
