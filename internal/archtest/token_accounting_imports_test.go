package archtest

import "testing"

var tokenAccountingForbiddenDeps = []forbiddenDep{
	{
		Substr: "github.com/pkoukk/tiktoken-go",
		ErrMsg: "guarded packages must not depend on tiktoken-go before adapter-scoped token accounting implementation",
	},
	{
		Substr: "github.com/tiktoken-go/tokenizer",
		ErrMsg: "guarded packages must not depend on tokenizer-family packages before adapter-scoped token accounting implementation",
	},
	{
		Substr: "github.com/daulet/tokenizers",
		ErrMsg: "guarded packages must not depend on Hugging Face tokenizers bindings before adapter-scoped token accounting implementation",
	},
	{
		Substr: "github.com/sugarme/tokenizer",
		ErrMsg: "guarded packages must not depend on tokenizer packages before adapter-scoped token accounting implementation",
	},
	{
		Substr: "github.com/huggingface/tokenizers",
		ErrMsg: "guarded packages must not depend on Hugging Face tokenizer packages before adapter-scoped token accounting implementation",
	},
	{
		Substr: "github.com/huggingface/tokenizers-go",
		ErrMsg: "guarded packages must not depend on Hugging Face tokenizer Go wrappers before adapter-scoped token accounting implementation",
	},
}

// TestPublicContractsDoNotDependOnTokenizerLibraries keeps tokenizer dependencies out of
// stable public contracts for the token-accounting design baseline.
func TestPublicContractsDoNotDependOnTokenizerLibraries(t *testing.T) {
	t.Parallel()
	assertDepsExcludeForbidden(t, []string{"./pkg/aipapi/...", "./pkg/aipsdk/..."}, tokenAccountingForbiddenDeps)
}

// TestInternalCoreDoesNotDependOnTokenizerLibraries keeps tokenizer dependencies out of
// current core packages until later phases add explicit adapter-scoped allowlists.
func TestInternalCoreDoesNotDependOnTokenizerLibraries(t *testing.T) {
	t.Parallel()
	assertDepsExcludeForbidden(t, []string{"./internal/core/..."}, tokenAccountingForbiddenDeps)
}
