package openresponses_test

import (
	"context"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/compactionfacts"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/frontendpipe"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openresponses"
	proto "github.com/aiproxer/aiproxer/internal/plugins/protocols/openresponses"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func runOpenResponsesCompileProof(t *testing.T, body string) (frontendpipe.ProofOutput, error) {
	t.Helper()
	prof := openresponses.NewProfile()
	in := defaultOpenResponsesProofInput([]byte(body), "gpt-4o", nil)
	return prof.CompileProof(context.Background(), in)
}

func canonicalExtractOpenResponses(t *testing.T, body string) compactionfacts.RequestFacts {
	t.Helper()
	_, call, err := proto.DecodeRequest([]byte(body), proto.DefaultLimits())
	require.NoError(t, err)
	call.Invocation = aipapi.Invocation{
		Operation:    aipapi.OperationOpenResponsesCreate,
		DeliveryMode: aipapi.DeliveryModeNonStreaming,
	}
	return compactionfacts.ExtractFactsFromCall(call)
}

func TestCompaction_Differential_UnicodeEscapesAndWhitespace(t *testing.T) {
	t.Parallel()

	body := `{"model":"gpt-4o","instructions":"  \u0043\u006f\u006e\u0074\u0065\u0078\u0074 \u0043\u0068\u0065\u0063\u006b\u0070\u006f\u0069\u006e\u0074 \u0043\u006f\u006d\u0070\u0061\u0063\u0074\u0069\u006f\u006e  ",` +
		`"input":"Hello \u4e16\u754c!   ","store":false}`

	// 1. Decode canonical
	exactFacts := canonicalExtractOpenResponses(t, body)

	// 2. Compile proof
	out, err := runOpenResponsesCompileProof(t, body)
	require.NoError(t, err)

	proof := out.State.Proof
	require.True(t, proof.CompactionComplete, "CompactionComplete must be true for valid request")

	// Differential assertions
	assert.Equal(t, exactFacts.ItemCount, proof.CompactionFacts.ItemCount)
	assert.Equal(t, exactFacts.ItemHashes, proof.CompactionFacts.ItemHashes)
	assert.Equal(t, exactFacts.TailHashes, proof.CompactionFacts.TailHashes)
	assert.Equal(t, exactFacts.PrefixHash, proof.CompactionFacts.PrefixHash)
	assert.Equal(t, exactFacts.StartRuleMatched, proof.CompactionFacts.StartRuleMatched)
	assert.Equal(t, exactFacts.StartRuleID, proof.CompactionFacts.StartRuleID)
	assert.Equal(t, exactFacts.StartRuleEvidence, proof.CompactionFacts.StartRuleEvidence)
	assert.Equal(t, exactFacts.ToolCount, proof.CompactionFacts.ToolCount)
}

func TestCompaction_Differential_FieldSplitMarkerNonmatch(t *testing.T) {
	t.Parallel()

	body := `{"model":"gpt-4o","instructions":"Here is some context","input":" checkpoint compaction was mentioned","store":false}`

	exactFacts := canonicalExtractOpenResponses(t, body)
	assert.False(t, exactFacts.StartRuleMatched, "Field boundary must prevent cross-field marker match")

	out, err := runOpenResponsesCompileProof(t, body)
	require.NoError(t, err)

	proof := out.State.Proof
	require.True(t, proof.CompactionComplete)
	assert.False(t, proof.CompactionFacts.StartRuleMatched, "Streaming proof must not match marker across fields")
	assert.Equal(t, exactFacts.ItemHashes, proof.CompactionFacts.ItemHashes)
}

func TestCompaction_Differential_MultiTurnItemArrayInput(t *testing.T) {
	t.Parallel()

	body := `{"model":"gpt-4o","instructions":"System prompt","store":false,` +
		`"input":[` +
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"turn 1"}]},` +
		`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"turn 2"}]},` +
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"turn 3"}]}` +
		`]}`

	exactFacts := canonicalExtractOpenResponses(t, body)

	out, err := runOpenResponsesCompileProof(t, body)
	require.NoError(t, err)

	proof := out.State.Proof
	require.True(t, proof.CompactionComplete)
	assert.Equal(t, exactFacts.ItemCount, proof.CompactionFacts.ItemCount)
	assert.Equal(t, exactFacts.ItemHashes, proof.CompactionFacts.ItemHashes)
	assert.Equal(t, exactFacts.TailHashes, proof.CompactionFacts.TailHashes)
	assert.Equal(t, exactFacts.PrefixHash, proof.CompactionFacts.PrefixHash)
	assert.Equal(t, exactFacts.StartRuleMatched, proof.CompactionFacts.StartRuleMatched)
}

func TestCompaction_Differential_ToolsAndToolCount(t *testing.T) {
	t.Parallel()

	body := `{"model":"gpt-4o","input":"search for weather","store":false,` +
		`"tools":[{"type":"function","name":"get_weather","description":"fetch weather","parameters":{"type":"object"}}]}`

	exactFacts := canonicalExtractOpenResponses(t, body)
	assert.Equal(t, 1, exactFacts.ToolCount)

	out, err := runOpenResponsesCompileProof(t, body)
	require.NoError(t, err)

	proof := out.State.Proof
	require.True(t, proof.CompactionComplete)
	assert.Equal(t, exactFacts.ToolCount, proof.CompactionFacts.ToolCount)
	assert.Equal(t, exactFacts.ItemHashes, proof.CompactionFacts.ItemHashes)
}

func TestCompaction_Differential_FieldPermutations(t *testing.T) {
	t.Parallel()

	body := `{"tools":[{"type":"function","name":"search","description":"web search","parameters":{"type":"object"}}],` +
		`"store":false,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hello world"}]}],` +
		`"model":"gpt-4o","instructions":"You are a helpful assistant."}`

	exactFacts := canonicalExtractOpenResponses(t, body)
	out, err := runOpenResponsesCompileProof(t, body)
	require.NoError(t, err)

	proof := out.State.Proof
	require.True(t, proof.CompactionComplete)
	assert.Equal(t, exactFacts.ItemCount, proof.CompactionFacts.ItemCount)
	assert.Equal(t, exactFacts.ItemHashes, proof.CompactionFacts.ItemHashes)
	assert.Equal(t, exactFacts.TailHashes, proof.CompactionFacts.TailHashes)
	assert.Equal(t, exactFacts.PrefixHash, proof.CompactionFacts.PrefixHash)
	assert.Equal(t, exactFacts.ToolCount, proof.CompactionFacts.ToolCount)
	assert.Equal(t, exactFacts.StartRuleMatched, proof.CompactionFacts.StartRuleMatched)
}

func TestCompaction_Differential_UTF8MultiByteSplit(t *testing.T) {
	t.Parallel()

	body := `{"model":"gpt-4o","instructions":"System with emoji 🚀 and characters: 日本語, Español, Français",` +
		`"store":false,"input":"User text with 🌟 sparkles and multi-byte utf8 \u2728"}`

	exactFacts := canonicalExtractOpenResponses(t, body)
	out, err := runOpenResponsesCompileProof(t, body)
	require.NoError(t, err)

	proof := out.State.Proof
	require.True(t, proof.CompactionComplete)
	assert.Equal(t, exactFacts.ItemCount, proof.CompactionFacts.ItemCount)
	assert.Equal(t, exactFacts.ItemHashes, proof.CompactionFacts.ItemHashes)
	assert.Equal(t, exactFacts.TailHashes, proof.CompactionFacts.TailHashes)
	assert.Equal(t, exactFacts.PrefixHash, proof.CompactionFacts.PrefixHash)
	assert.Equal(t, exactFacts.StartRuleMatched, proof.CompactionFacts.StartRuleMatched)
}

func TestCompaction_Differential_LargeChunkedStream_ExactFacts(t *testing.T) {
	t.Parallel()

	var sb strings.Builder
	sb.WriteString(`{"model":"gpt-4o","instructions":"context checkpoint compaction: system prompt","store":false,"input":"`)
	chunk := "The quick brown fox jumps over the lazy dog. "
	for sb.Len() < 600*1024 {
		sb.WriteString(chunk)
	}
	sb.WriteString(`"}`)
	body := sb.String()

	exactFacts := canonicalExtractOpenResponses(t, body)
	out, err := runOpenResponsesCompileProof(t, body)
	require.NoError(t, err)

	proof := out.State.Proof
	require.True(t, proof.CompactionComplete)
	assert.Equal(t, exactFacts.ItemCount, proof.CompactionFacts.ItemCount)
	assert.Equal(t, exactFacts.ItemHashes, proof.CompactionFacts.ItemHashes)
	assert.Equal(t, exactFacts.TailHashes, proof.CompactionFacts.TailHashes)
	assert.Equal(t, exactFacts.PrefixHash, proof.CompactionFacts.PrefixHash)
	assert.Equal(t, exactFacts.StartRuleMatched, proof.CompactionFacts.StartRuleMatched)
	assert.Equal(t, exactFacts.StartRuleID, proof.CompactionFacts.StartRuleID)
}

func TestCompaction_Differential_UTF8SplitAt32KiB_LargeStream(t *testing.T) {
	t.Parallel()

	prefix := `{"model":"gpt-4o","instructions":"`
	targetOffset := 32766
	padLen := targetOffset - len(prefix)
	require.Greater(t, padLen, 0)
	padding := strings.Repeat("A", padLen)

	utf8Char := "🌟" // 4 bytes: 0xf0, 0x9f, 0x8c, 0x9f
	require.Equal(t, 4, len([]byte(utf8Char)))

	suffixPadding := strings.Repeat("B", 16*1024)
	suffix := ` and more instructions ` + suffixPadding + `","input":"hello","store":false}`

	body := prefix + padding + utf8Char + suffix

	charStart := len(prefix) + len(padding)
	charEnd := charStart + len([]byte(utf8Char))
	require.Equal(t, 32766, charStart)
	require.Equal(t, 32770, charEnd)
	require.True(t, charStart < 32768 && charEnd > 32768, "UTF-8 sequence must straddle 32768 boundary")
	require.Greater(t, len(body), 32768, "total body must be larger than 32 KiB")

	// 1. Decode canonical
	exactFacts := canonicalExtractOpenResponses(t, body)

	// 2. Compile proof via streaming
	out, err := runOpenResponsesCompileProof(t, body)
	require.NoError(t, err)

	proof := out.State.Proof
	require.True(t, proof.CompactionComplete, "CompileProof must succeed and be complete")

	// 3. Differential assertions
	assert.Equal(t, exactFacts.ItemCount, proof.CompactionFacts.ItemCount)
	assert.Equal(t, exactFacts.ItemHashes, proof.CompactionFacts.ItemHashes, "ItemHashes must match canonical exactly across 32KiB split")
	assert.Equal(t, exactFacts.TailHashes, proof.CompactionFacts.TailHashes)
	assert.Equal(t, exactFacts.PrefixHash, proof.CompactionFacts.PrefixHash)
	assert.Equal(t, exactFacts.StartRuleMatched, proof.CompactionFacts.StartRuleMatched)
}
