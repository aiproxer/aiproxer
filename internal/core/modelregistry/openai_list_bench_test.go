package modelregistry

import "testing"

func BenchmarkBuildOpenAIModelsList(b *testing.B) {
	models := make([]BackendModel, 200)
	for i := range models {
		models[i] = BackendModel{
			CanonicalID: "openai/gpt-" + string(rune('a'+i%26)),
			NativeID:    "n",
			BackendID:   "backend",
			Kind:        "openai-responses",
		}
	}
	b.ReportAllocs()
	for b.Loop() {
		_ = BuildOpenAIModelsList(models)
	}
}
