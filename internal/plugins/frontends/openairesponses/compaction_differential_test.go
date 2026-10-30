package openairesponses_test

import (
	"context"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/compactionfacts"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/frontendpipe"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openairesponses"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func runResponsesCompileProof(t *testing.T, body string) (frontendpipe.ProofOutput, error) {
	t.Helper()
	prof := openairesponses.NewProfile()
	in := defaultProofInput([]byte(body), "stub:gpt-4o", nil)
	return prof.CompileProof(context.Background(), in)
}

func TestCompaction_Differential_UnicodeEscapesAndWhitespace(t *testing.T) {
	t.Parallel()

	body := `{"model":"gpt-4o","instructions":"  \u0043\u006f\u006e\u0074\u0065\u0078\u0074 \u0043\u0068\u0065\u0063\u006b\u0070\u006f\u0069\u006e\u0074 \u0043\u006f\u006d\u0070\u0061\u0063\u0074\u0069\u006f\u006e  ",` +
		`"input":"Hello \u4e16\u754c!   "}`

	// 1. Decode canonical
	decoded, err := openairesponses.DecodeCreateRequest([]byte(body), openairesponses.DecodeOptions{RouteSelector: "stub:gpt-4o"})
	require.NoError(t, err)
	exactFacts := compactionfacts.ExtractFactsFromCall(*decoded.Call)

	// 2. Compile proof
	out, err := runResponsesCompileProof(t, body)
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

	// Instructions ends with "context", input starts with " checkpoint compaction"
	// Because they are distinct fields, "context checkpoint compaction" must NOT match!
	body := `{"model":"gpt-4o","instructions":"Here is some context","input":" checkpoint compaction was mentioned"}`

	decoded, err := openairesponses.DecodeCreateRequest([]byte(body), openairesponses.DecodeOptions{RouteSelector: "stub:gpt-4o"})
	require.NoError(t, err)
	exactFacts := compactionfacts.ExtractFactsFromCall(*decoded.Call)
	assert.False(t, exactFacts.StartRuleMatched, "Field boundary must prevent cross-field marker match")

	out, err := runResponsesCompileProof(t, body)
	require.NoError(t, err)

	proof := out.State.Proof
	require.True(t, proof.CompactionComplete)
	assert.False(t, proof.CompactionFacts.StartRuleMatched, "Streaming proof must not match marker across fields")
	assert.Equal(t, exactFacts.ItemHashes, proof.CompactionFacts.ItemHashes)
}

func TestCompaction_Differential_MultiTurnArrayInput(t *testing.T) {
	t.Parallel()

	body := `{"model":"gpt-4o","instructions":"System prompt",` +
		`"input":[` +
		`{"role":"user","content":"turn 1"},` +
		`{"role":"assistant","content":"turn 2"},` +
		`{"role":"user","content":"turn 3"}` +
		`]}`

	decoded, err := openairesponses.DecodeCreateRequest([]byte(body), openairesponses.DecodeOptions{RouteSelector: "stub:gpt-4o"})
	require.NoError(t, err)
	exactFacts := compactionfacts.ExtractFactsFromCall(*decoded.Call)

	out, err := runResponsesCompileProof(t, body)
	require.NoError(t, err)

	proof := out.State.Proof
	require.True(t, proof.CompactionComplete)
	assert.Equal(t, exactFacts.ItemCount, proof.CompactionFacts.ItemCount)
	assert.Equal(t, exactFacts.ItemHashes, proof.CompactionFacts.ItemHashes)
	assert.Equal(t, exactFacts.TailHashes, proof.CompactionFacts.TailHashes)
	assert.Equal(t, exactFacts.PrefixHash, proof.CompactionFacts.PrefixHash)
	assert.Equal(t, exactFacts.StartRuleMatched, proof.CompactionFacts.StartRuleMatched)
}

func TestCompaction_Differential_ToolCountAndTools(t *testing.T) {
	t.Parallel()

	body := `{"model":"gpt-4o","input":"search for weather",` +
		`"tools":[{"type":"function","name":"get_weather","description":"fetch weather","parameters":{"type":"object"}}]}`

	decoded, err := openairesponses.DecodeCreateRequest([]byte(body), openairesponses.DecodeOptions{RouteSelector: "stub:gpt-4o"})
	require.NoError(t, err)
	exactFacts := compactionfacts.ExtractFactsFromCall(*decoded.Call)
	assert.Equal(t, 1, exactFacts.ToolCount)

	out, err := runResponsesCompileProof(t, body)
	require.NoError(t, err)

	proof := out.State.Proof
	require.True(t, proof.CompactionComplete)
	assert.Equal(t, exactFacts.ToolCount, proof.CompactionFacts.ToolCount)
	assert.Equal(t, exactFacts.ItemHashes, proof.CompactionFacts.ItemHashes)
}

func TestCompaction_Differential_FieldPermutations(t *testing.T) {
	t.Parallel()

	body := `{"tools":[{"type":"function","name":"search","description":"web search","parameters":{"type":"object"}}],` +
		`"input":[{"role":"user","content":"hello world"}],` +
		`"model":"gpt-4o","instructions":"You are a helpful assistant."}`

	decoded, err := openairesponses.DecodeCreateRequest([]byte(body), openairesponses.DecodeOptions{RouteSelector: "stub:gpt-4o"})
	require.NoError(t, err)
	exactFacts := compactionfacts.ExtractFactsFromCall(*decoded.Call)

	out, err := runResponsesCompileProof(t, body)
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
		`"input":"User text with 🌟 sparkles and multi-byte utf8 \u2728"}`

	decoded, err := openairesponses.DecodeCreateRequest([]byte(body), openairesponses.DecodeOptions{RouteSelector: "stub:gpt-4o"})
	require.NoError(t, err)
	exactFacts := compactionfacts.ExtractFactsFromCall(*decoded.Call)

	out, err := runResponsesCompileProof(t, body)
	require.NoError(t, err)

	proof := out.State.Proof
	require.True(t, proof.CompactionComplete)
	assert.Equal(t, exactFacts.ItemCount, proof.CompactionFacts.ItemCount)
	assert.Equal(t, exactFacts.ItemHashes, proof.CompactionFacts.ItemHashes)
	assert.Equal(t, exactFacts.TailHashes, proof.CompactionFacts.TailHashes)
	assert.Equal(t, exactFacts.PrefixHash, proof.CompactionFacts.PrefixHash)
	assert.Equal(t, exactFacts.EstimatedTokens, proof.CompactionFacts.EstimatedTokens)
	assert.Equal(t, exactFacts.StartRuleMatched, proof.CompactionFacts.StartRuleMatched)
}

func TestCompaction_Differential_LargeChunkedStream_ExactFacts(t *testing.T) {
	t.Parallel()

	var sb strings.Builder
	sb.WriteString(`{"model":"gpt-4o","instructions":"context checkpoint compaction: system prompt","input":"`)
	chunk := "The quick brown fox jumps over the lazy dog. "
	for sb.Len() < 600*1024 {
		sb.WriteString(chunk)
	}
	sb.WriteString(`"}`)
	body := sb.String()

	decoded, err := openairesponses.DecodeCreateRequest([]byte(body), openairesponses.DecodeOptions{RouteSelector: "stub:gpt-4o"})
	require.NoError(t, err)
	exactFacts := compactionfacts.ExtractFactsFromCall(*decoded.Call)

	out, err := runResponsesCompileProof(t, body)
	require.NoError(t, err)

	proof := out.State.Proof
	require.True(t, proof.CompactionComplete)
	assert.Equal(t, exactFacts.ItemCount, proof.CompactionFacts.ItemCount)
	assert.Equal(t, exactFacts.ItemHashes, proof.CompactionFacts.ItemHashes)
	assert.Equal(t, exactFacts.TailHashes, proof.CompactionFacts.TailHashes)
	assert.Equal(t, exactFacts.PrefixHash, proof.CompactionFacts.PrefixHash)
	assert.Equal(t, exactFacts.EstimatedTokens, proof.CompactionFacts.EstimatedTokens)
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
	suffix := ` and more instructions ` + suffixPadding + `","input":"hello"}`

	body := prefix + padding + utf8Char + suffix

	charStart := len(prefix) + len(padding)
	charEnd := charStart + len([]byte(utf8Char))
	require.Equal(t, 32766, charStart)
	require.Equal(t, 32770, charEnd)
	require.True(t, charStart < 32768 && charEnd > 32768, "UTF-8 sequence must straddle 32768 boundary")
	require.Greater(t, len(body), 32768, "total body must be larger than 32 KiB")

	// 1. Decode canonical
	decoded, err := openairesponses.DecodeCreateRequest([]byte(body), openairesponses.DecodeOptions{RouteSelector: "stub:gpt-4o"})
	require.NoError(t, err)
	exactFacts := compactionfacts.ExtractFactsFromCall(*decoded.Call)

	// 2. Compile proof via streaming
	out, err := runResponsesCompileProof(t, body)
	require.NoError(t, err)

	proof := out.State.Proof
	require.True(t, proof.CompactionComplete, "CompileProof must succeed and be complete")

	// 3. Differential assertions
	assert.Equal(t, exactFacts.ItemCount, proof.CompactionFacts.ItemCount)
	assert.Equal(t, exactFacts.ItemHashes, proof.CompactionFacts.ItemHashes, "ItemHashes must match canonical exactly across 32KiB split")
	assert.Equal(t, exactFacts.TailHashes, proof.CompactionFacts.TailHashes)
	assert.Equal(t, exactFacts.PrefixHash, proof.CompactionFacts.PrefixHash)
	assert.Equal(t, exactFacts.EstimatedTokens, proof.CompactionFacts.EstimatedTokens)
	assert.Equal(t, exactFacts.StartRuleMatched, proof.CompactionFacts.StartRuleMatched)
}
