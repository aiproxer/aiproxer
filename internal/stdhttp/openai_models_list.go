package stdhttp

import (
	"github.com/aiproxer/aiproxer/internal/core/modelregistry"
)

// openAIModelList aliases the core OpenAI list shape for local tests/handlers.
type openAIModelList = modelregistry.OpenAIModelList

func buildOpenAIModelsList(models []modelregistry.BackendModel) openAIModelList {
	return modelregistry.BuildOpenAIModelsList(models)
}
