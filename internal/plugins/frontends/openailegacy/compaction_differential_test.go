package openailegacy_test

import (
	"context"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/compactionfacts"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/frontendpipe"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openailegacy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func runCompileProof(t *testing.T, body string) (frontendpipe.ProofOutput, error) {
	t.Helper()
	prof := openailegacy.NewProfile()
	in := defaultChatProofInput([]byte(body), "", nil)
	return prof.CompileProof(context.Background(), in)
}

func TestCompaction_Differential_UnicodeEscapesAndWhitespace(t *testing.T) {
	t.Parallel()

	// JSON payload with unicode escapes and whitespace variants
	body := `{"model":"gpt-4o","messages":[` +
		`{"role":"system","content":"  \u0043\u006f\u006e\u0074\u0065\u0078\u0074 \u0043\u0068\u0065\u0063\u006b\u0070\u006f\u0069\u006e\u0074 \u0043\u006f\u006d\u0070\u0061\u0063\u0074\u0069\u006f\u006e  "},` +
		`{"role":"user","content":"Hello \u4e16\u754c!   "}` +
		`]}`

	// 1. Decode canonical
	decoded, err := openailegacy.DecodeChatRequest([]byte(body), openailegacy.DecodeOptions{RouteSelector: "gpt-4o"})
	require.NoError(t, err)
	exactFacts := compactionfacts.ExtractFactsFromCall(*decoded.Call)

	// 2. Compile proof
	out, err := runCompileProof(t, body)
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

	// Message 1 ends with "context", Message 2 starts with " checkpoint compaction"
	// Because they are distinct fields, "context checkpoint compaction" must NOT match!
	body := `{"model":"gpt-4o","messages":[` +
		`{"role":"system","content":"Here is some context"},` +
		`{"role":"user","content":" checkpoint compaction was mentioned"}` +
		`]}`

	decoded, err := openailegacy.DecodeChatRequest([]byte(body), openailegacy.DecodeOptions{RouteSelector: "gpt-4o"})
	require.NoError(t, err)
	exactFacts := compactionfacts.ExtractFactsFromCall(*decoded.Call)
	assert.False(t, exactFacts.StartRuleMatched, "Field boundary must prevent cross-field marker match")

	out, err := runCompileProof(t, body)
	require.NoError(t, err)

	proof := out.State.Proof
	require.True(t, proof.CompactionComplete)
	assert.False(t, proof.CompactionFacts.StartRuleMatched, "Streaming proof must not match marker across fields")
	assert.Equal(t, exactFacts.ItemHashes, proof.CompactionFacts.ItemHashes)
}

func TestCompaction_Differential_DeveloperVsSystemRole(t *testing.T) {
	t.Parallel()

	// 1. System role is supported by CompileProof
	bodySystem := `{"model":"gpt-4o","messages":[{"role":"system","content":"You are a helpful assistant."}]}`
	decodedSystem, err := openailegacy.DecodeChatRequest([]byte(bodySystem), openailegacy.DecodeOptions{RouteSelector: "gpt-4o"})
	require.NoError(t, err)
	exactSystem := compactionfacts.ExtractFactsFromCall(*decodedSystem.Call)

	outSystem, err := runCompileProof(t, bodySystem)
	require.NoError(t, err)
	require.True(t, outSystem.State.Proof.CompactionComplete)
	assert.Equal(t, exactSystem.ItemHashes, outSystem.State.Proof.CompactionFacts.ItemHashes)

	// 2. Developer role declines CompileProof to canonical decode
	bodyDev := `{"model":"gpt-4o","messages":[{"role":"developer","content":"You are a helpful assistant."}]}`
	_, errDev := runCompileProof(t, bodyDev)
	require.Error(t, errDev, "Developer role requires canonical normalization and must decline CompileProof")

	// Canonical decoder successfully maps developer to system
	decodedDev, err := openailegacy.DecodeChatRequest([]byte(bodyDev), openailegacy.DecodeOptions{RouteSelector: "gpt-4o"})
	require.NoError(t, err)
	assert.Equal(t, exactSystem.ItemHashes, compactionfacts.ExtractFactsFromCall(*decodedDev.Call).ItemHashes)
}

func TestCompaction_Differential_ItemOrdering(t *testing.T) {
	t.Parallel()

	body := `{"model":"gpt-4o","messages":[` +
		`{"role":"system","content":"Instruction 1"},` +
		`{"role":"user","content":"Query 1"},` +
		`{"role":"assistant","content":"Answer 1"},` +
		`{"role":"user","content":"Query 2"}` +
		`]}`

	decoded, err := openailegacy.DecodeChatRequest([]byte(body), openailegacy.DecodeOptions{RouteSelector: "gpt-4o"})
	require.NoError(t, err)
	exactFacts := compactionfacts.ExtractFactsFromCall(*decoded.Call)

	out, err := runCompileProof(t, body)
	require.NoError(t, err)
	proof := out.State.Proof
	require.True(t, proof.CompactionComplete)
	assert.Equal(t, 4, len(proof.CompactionFacts.ItemHashes))
	assert.Equal(t, exactFacts.ItemHashes, proof.CompactionFacts.ItemHashes)
}

func TestCompaction_Differential_LargeChunkedStream_ExactFacts(t *testing.T) {
	t.Parallel()

	// Build a large chunked message body (> 512 KiB to trigger Pass 2 / Pass 3)
	// with a start rule marker embedded in text.
	var sb strings.Builder
	sb.WriteString(`{"model":"gpt-4o","messages":[{"role":"system","content":"context checkpoint compaction: `)
	// Append 600 KiB of text
	chunk := "The quick brown fox jumps over the lazy dog. "
	for sb.Len() < 600*1024 {
		sb.WriteString(chunk)
	}
	sb.WriteString(`"},{"role":"user","content":"Please continue"}]}`)
	body := sb.String()

	// 1. Decode canonical
	decoded, err := openailegacy.DecodeChatRequest([]byte(body), openailegacy.DecodeOptions{RouteSelector: "gpt-4o"})
	require.NoError(t, err)
	exactFacts := compactionfacts.ExtractFactsFromCall(*decoded.Call)

	// 2. Compile proof via streaming
	out, err := runCompileProof(t, body)
	require.NoError(t, err)

	proof := out.State.Proof
	require.True(t, proof.CompactionComplete, "Large chunked stream must produce complete compaction facts")

	assert.Equal(t, exactFacts.ItemCount, proof.CompactionFacts.ItemCount)
	assert.Equal(t, exactFacts.ItemHashes, proof.CompactionFacts.ItemHashes)
	assert.Equal(t, exactFacts.TailHashes, proof.CompactionFacts.TailHashes)
	assert.Equal(t, exactFacts.PrefixHash, proof.CompactionFacts.PrefixHash)
	assert.Equal(t, exactFacts.StartRuleMatched, proof.CompactionFacts.StartRuleMatched)
	assert.Equal(t, exactFacts.StartRuleID, proof.CompactionFacts.StartRuleID)
}

func TestCompaction_UnsupportedShape_DeclinesToCanonical(t *testing.T) {
	t.Parallel()

	// Legacy function_call or unsupported role in openailegacy messages declines CompileProof to canonical decode
	body := `{"model":"gpt-4o","messages":[{"role":"assistant","content":"hello","function_call":{"name":"foo","arguments":"{}"}}]}`
	_, err := runCompileProof(t, body)
	require.Error(t, err, "Unsupported shape (function_call) must decline proof compilation to canonical decode")

	// Canonical decoder successfully accepts legacy function_call
	decoded, err := openailegacy.DecodeChatRequest([]byte(body), openailegacy.DecodeOptions{RouteSelector: "gpt-4o"})
	require.NoError(t, err, "Canonical decoder must accept legacy function_call")
	require.NotNil(t, decoded.Call)
}

func TestCompaction_Differential_UTF8SplitAt32KiB_LargeStream(t *testing.T) {
	t.Parallel()

	// 4-byte UTF-8 emoji: 🌟 = \xf0\x9f\x8c\x9f
	// We construct a large JSON message body (> 32KiB, e.g. ~48KiB) where the 4-byte
	// UTF-8 sequence straddles byte offset 32768 (bytes 32766..32770).
	prefix := `{"model":"gpt-4o","messages":[{"role":"user","content":"`
	targetOffset := 32766
	padLen := targetOffset - len(prefix)
	require.Greater(t, padLen, 0)
	padding := strings.Repeat("A", padLen)

	utf8Char := "🌟" // 4 bytes: 0xf0, 0x9f, 0x8c, 0x9f
	require.Equal(t, 4, len([]byte(utf8Char)))

	// Suffix with additional 16 KiB of content so stream is ~48 KiB (> 32KiB)
	suffixPadding := strings.Repeat("B", 16*1024)
	suffix := ` and more content ` + suffixPadding + `"}]}`

	body := prefix + padding + utf8Char + suffix

	// Verify UTF-8 character straddles 32768:
	charStart := len(prefix) + len(padding)
	charEnd := charStart + len([]byte(utf8Char))
	require.Equal(t, 32766, charStart)
	require.Equal(t, 32770, charEnd)
	require.True(t, charStart < 32768 && charEnd > 32768, "UTF-8 sequence must straddle 32768 boundary")
	require.Greater(t, len(body), 32768, "total body must be larger than 32 KiB")

	// 1. Decode canonical
	decoded, err := openailegacy.DecodeChatRequest([]byte(body), openailegacy.DecodeOptions{RouteSelector: "gpt-4o"})
	require.NoError(t, err)
	exactFacts := compactionfacts.ExtractFactsFromCall(*decoded.Call)

	// 2. Compile proof via streaming
	out, err := runCompileProof(t, body)
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

func TestCompaction_MultiPartAssistantMessage_SkipsStreamingFacts(t *testing.T) {
	t.Parallel()

	// Assistant message with reasoning_content creates a multi-part message (ReasoningPart + TextPart).
	// The streaming compaction pass must NOT mark CompactionComplete, leaving it false for canonical handling.
	body := `{"model":"gpt-4o","messages":[` +
		`{"role":"user","content":"Hello"},` +
		`{"role":"assistant","reasoning_content":"thinking carefully","content":"world"}` +
		`]}`

	out, err := runCompileProof(t, body)
	require.NoError(t, err)

	proof := out.State.Proof
	assert.False(t, proof.CompactionComplete, "multi-part assistant message must leave CompactionComplete false")
}
