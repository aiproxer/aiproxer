package compactionfacts_test

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/aiproxer/aiproxer/internal/compactionfacts"
	compactiondetect "github.com/aiproxer/aiproxer/internal/infra/compactiondetect"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/compaction"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRulePrioritiesAndFamilies(t *testing.T) {
	t.Parallel()

	// 1. Protocol compaction takes precedence over text signatures even when text is present.
	callProtocolWithText := aipapi.Call{
		Invocation: aipapi.Invocation{
			Operation: aipapi.OperationContextCompaction,
		},
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{
				aipapi.TextPart("context checkpoint compaction and summarize objective"),
			}},
		},
	}
	facts := compactionfacts.ExtractFactsFromCall(callProtocolWithText)
	if !facts.StartRuleMatched {
		t.Fatalf("expected start rule matched for protocol compaction")
	}
	if facts.StartRuleID != compactionfacts.RuleProtocolContextCompaction {
		t.Errorf("RuleID: got %q, want %q", facts.StartRuleID, compactionfacts.RuleProtocolContextCompaction)
	}
	if facts.StartRuleEvidence != compactionfacts.EvidenceProtocolStrict {
		t.Errorf("Evidence: got %v, want %v", facts.StartRuleEvidence, compactionfacts.EvidenceProtocolStrict)
	}
	if facts.StartRuleMode != compactionfacts.RuleModeSingle {
		t.Errorf("Mode: got %v, want %v", facts.StartRuleMode, compactionfacts.RuleModeSingle)
	}

	// 2. Ordered test of every signature start rule
	sigTests := []struct {
		name     string
		call     aipapi.Call
		wantRule string
		wantMode compactionfacts.RuleMode
		wantEv   compactionfacts.Evidence
	}{
		{
			name: "codex_local_checkpoint",
			call: aipapi.Call{
				Messages: []aipapi.Message{
					{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("Context Checkpoint Compaction in progress")}},
				},
			},
			wantRule: compactionfacts.RuleCodexLocalCheckpoint,
			wantMode: compactionfacts.RuleModeSingle,
			wantEv:   compactionfacts.EvidenceSignatureStrict,
		},
		{
			name: "pi_openclaw_compaction_summary",
			call: aipapi.Call{
				Messages: []aipapi.Message{
					{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("<conversation> please summarize checkpoint </conversation>")}},
				},
			},
			wantRule: compactionfacts.RulePiOpenClawCompactionSummary,
			wantMode: compactionfacts.RuleModeSeries,
			wantEv:   compactionfacts.EvidenceSignatureStrict,
		},
		{
			name: "cline_agentic_compaction",
			call: aipapi.Call{
				Messages: []aipapi.Message{
					{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("continuation-note: please summarize progress")}},
				},
			},
			wantRule: compactionfacts.RuleClineAgenticCompaction,
			wantMode: compactionfacts.RuleModeSingle,
			wantEv:   compactionfacts.EvidenceSignatureStrict,
		},
		{
			name: "opencode_anchored_summary",
			call: aipapi.Call{
				Messages: []aipapi.Message{
					{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("<conversation> summarize objective and work state </conversation>")}},
				},
			},
			wantRule: compactionfacts.RuleOpenCodeAnchoredSummary,
			wantMode: compactionfacts.RuleModeSingle,
			wantEv:   compactionfacts.EvidenceSignatureStrict,
		},
		{
			name: "opencode_custom_compaction_history",
			call: aipapi.Call{
				Messages: []aipapi.Message{
					{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("The following is the conversation history:\n...")}},
				},
			},
			wantRule: compactionfacts.RuleOpenCodeCustomCompactionHistory,
			wantMode: compactionfacts.RuleModeSingle,
			wantEv:   compactionfacts.EvidenceSignatureStrict,
		},
		{
			name: "kilocode_anchored_summary",
			call: aipapi.Call{
				Messages: []aipapi.Message{
					{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("objective: x, important details: y, work state: z, next move: a, relevant files: b")}},
				},
			},
			wantRule: compactionfacts.RuleKiloCodeAnchoredSummary,
			wantMode: compactionfacts.RuleModeSingle,
			wantEv:   compactionfacts.EvidenceSignatureStrict,
		},
		{
			name: "claude_code_compaction_no_tools",
			call: aipapi.Call{
				Messages: []aipapi.Message{
					{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("text only compaction of conversation")}},
				},
			},
			wantRule: compactionfacts.RuleClaudeCodeCompaction,
			wantMode: compactionfacts.RuleModeSingle,
			wantEv:   compactionfacts.EvidenceSignatureStrict,
		},
		{
			name: "gemini_cli_state_snapshot_generate",
			call: aipapi.Call{
				Messages: []aipapi.Message{
					{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("state snapshot generate new turn")}},
				},
			},
			wantRule: compactionfacts.RuleGeminiCLIStateSnapshot,
			wantMode: compactionfacts.RuleModeSeries,
			wantEv:   compactionfacts.EvidenceSignatureStrict,
		},
		{
			name: "gemini_cli_state_snapshot_verify",
			call: aipapi.Call{
				Messages: []aipapi.Message{
					{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("state snapshot verify state")}},
				},
			},
			wantRule: compactionfacts.RuleGeminiCLIStateSnapshot,
			wantMode: compactionfacts.RuleModeSeries,
			wantEv:   compactionfacts.EvidenceSignatureStrict,
		},
		{
			name: "roo_code_condense",
			call: aipapi.Call{
				Messages: []aipapi.Message{
					{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("condense and summarize conversation")}},
				},
			},
			wantRule: compactionfacts.RuleRooCodeCondense,
			wantMode: compactionfacts.RuleModeSingle,
			wantEv:   compactionfacts.EvidenceSignatureStrict,
		},
		{
			name: "aider_chat_summary",
			call: aipapi.Call{
				Messages: []aipapi.Message{
					{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("# user\nquestion\n# assistant\nsummarize details")}},
				},
			},
			wantRule: compactionfacts.RuleAiderChatSummary,
			wantMode: compactionfacts.RuleModeSeries,
			wantEv:   compactionfacts.EvidenceSignatureStrict,
		},
		{
			name: "crush_session_summary",
			call: aipapi.Call{
				Messages: []aipapi.Message{
					{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("session summary to preserve context")}},
				},
			},
			wantRule: compactionfacts.RuleCrushSessionSummary,
			wantMode: compactionfacts.RuleModeSingle,
			wantEv:   compactionfacts.EvidenceSignatureStrict,
		},
	}

	for _, tc := range sigTests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := compactionfacts.ExtractFactsFromCall(tc.call)
			if !f.StartRuleMatched {
				t.Fatalf("expected start rule match for %s", tc.name)
			}
			if f.StartRuleID != tc.wantRule {
				t.Errorf("RuleID: got %q, want %q", f.StartRuleID, tc.wantRule)
			}
			if f.StartRuleMode != tc.wantMode {
				t.Errorf("Mode: got %v, want %v", f.StartRuleMode, tc.wantMode)
			}
			if f.StartRuleEvidence != tc.wantEv {
				t.Errorf("Evidence: got %v, want %v", f.StartRuleEvidence, tc.wantEv)
			}
		})
	}

	// Claude Code rule requires no tools; verify it does NOT match when tools exist
	claudeWithTools := aipapi.Call{
		Tools: []aipapi.ToolDef{{Name: "bash"}},
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("text only compaction of conversation")}},
		},
	}
	fTools := compactionfacts.ExtractFactsFromCall(claudeWithTools)
	if fTools.StartRuleMatched && fTools.StartRuleID == compactionfacts.RuleClaudeCodeCompaction {
		t.Fatalf("claude code compaction should not match when tools exist")
	}
}

func TestTokenEstimation(t *testing.T) {
	t.Parallel()

	// Multi-byte Unicode: 4 runes, 1 token
	text := "Café"
	if utf8.RuneCountInString(text) != 4 {
		t.Fatalf("expected 4 runes, got %d", utf8.RuneCountInString(text))
	}
	call := aipapi.Call{
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart(text)}},
		},
	}
	facts := compactionfacts.ExtractFactsFromCall(call)
	if facts.EstimatedTokens != 1 {
		t.Errorf("EstimatedTokens: got %d, want 1", facts.EstimatedTokens)
	}

	// 100 runes across multiple content parts and tool result output
	part1 := strings.Repeat("a", 40)
	part2 := strings.Repeat("b", 30)
	part3 := strings.Repeat("c", 30)
	call2 := aipapi.Call{
		Items: []aipapi.Item{
			{
				Kind: aipapi.ItemKindMessage,
				Role: aipapi.RoleUser,
				Content: []aipapi.ContentPart{
					{Kind: aipapi.ContentPartText, Text: part1},
					{Kind: aipapi.ContentPartText, Refusal: part2},
				},
			},
			{
				Kind: aipapi.ItemKindToolResult,
				Role: aipapi.RoleTool,
				ToolResult: &aipapi.ToolResultItem{
					Output: part3,
				},
			},
		},
	}
	facts2 := compactionfacts.ExtractFactsFromCall(call2)
	if facts2.EstimatedTokens != 25 { // 100 / 4 = 25
		t.Errorf("EstimatedTokens: got %d, want 25", facts2.EstimatedTokens)
	}
}

func TestItemCanonicalHashing(t *testing.T) {
	t.Parallel()

	// 1. Tool Call item
	itTool := aipapi.Item{
		Kind: aipapi.ItemKindToolCall,
		Role: aipapi.RoleAssistant,
		ToolCall: &aipapi.ToolCallItem{
			CallID: "call_123",
			Name:   "search_db",
		},
	}
	h1 := compactionfacts.HashItem(itTool)
	var buf bytes.Buffer
	compactionfacts.WriteItemCanonical(&buf, itTool)
	if sha256.Sum256(buf.Bytes()) != h1 {
		t.Fatalf("HashItem and WriteItemCanonical produce different hashes")
	}

	// 2. Tool Result item
	itResult := aipapi.Item{
		Kind: aipapi.ItemKindToolResult,
		Role: aipapi.RoleTool,
		ToolResult: &aipapi.ToolResultItem{
			CallID: "call_123",
			Name:   "search_db",
			Output: "result data",
		},
	}
	h2 := compactionfacts.HashItem(itResult)
	buf.Reset()
	compactionfacts.WriteItemCanonical(&buf, itResult)
	if sha256.Sum256(buf.Bytes()) != h2 {
		t.Fatalf("HashItem and WriteItemCanonical produce different hashes for tool result")
	}

	// 3. Content parts: text, refusal, summary
	itContent := aipapi.Item{
		Kind: aipapi.ItemKindMessage,
		Role: aipapi.RoleAssistant,
		Content: []aipapi.ContentPart{
			{Kind: aipapi.ContentPartText, Text: "sample text"},
			{Kind: aipapi.ContentPartText, Refusal: "cannot answer"},
			{Kind: aipapi.ContentPartText, Summary: "summary info"},
		},
	}
	h3 := compactionfacts.HashItem(itContent)
	buf.Reset()
	compactionfacts.WriteItemCanonical(&buf, itContent)
	if sha256.Sum256(buf.Bytes()) != h3 {
		t.Fatalf("HashItem and WriteItemCanonical produce different hashes for content parts")
	}

	// 4. Legacy Instructions & Messages normalization parity
	legacyCall := aipapi.Call{
		Instructions: []aipapi.Message{
			{Role: aipapi.RoleSystem, Parts: []aipapi.Part{aipapi.TextPart("system prompt")}},
		},
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hello")}},
		},
	}
	itemsCall := aipapi.Call{
		Items: []aipapi.Item{
			{
				Kind:    aipapi.ItemKindMessage,
				Role:    aipapi.RoleSystem,
				Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "system prompt"}},
			},
			{
				Kind:    aipapi.ItemKindMessage,
				Role:    aipapi.RoleUser,
				Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "hello"}},
			},
		},
	}
	fLegacy := compactionfacts.ExtractFactsFromCall(legacyCall)
	fItems := compactionfacts.ExtractFactsFromCall(itemsCall)
	if len(fLegacy.ItemHashes) != len(fItems.ItemHashes) {
		t.Fatalf("legacy vs items hash count mismatch: %d vs %d", len(fLegacy.ItemHashes), len(fItems.ItemHashes))
	}
	for i := range fLegacy.ItemHashes {
		if fLegacy.ItemHashes[i] != fItems.ItemHashes[i] {
			t.Errorf("ItemHashes[%d] mismatch between legacy and items call", i)
		}
	}
	if fLegacy.PrefixHash != fItems.PrefixHash {
		t.Errorf("PrefixHash mismatch between legacy and items call")
	}
	if fLegacy.TailHashes != fItems.TailHashes {
		t.Errorf("TailHashes mismatch between legacy and items call")
	}
}

func TestIncrementalBuilder_MultiMiBTextInChunks(t *testing.T) {
	t.Parallel()

	// 5 MiB text payload across chunks
	chunkSize := 4096
	totalChunks := 1280 // 1280 * 4096 = 5,242,880 bytes (~5 MiB)
	chunkData := strings.Repeat("x", chunkSize)

	// Embed the start rule marker split across chunk 500 and 501
	marker := "context checkpoint compaction"
	partA := marker[:15]
	partB := marker[15:]

	builder := compactionfacts.NewBuilder(aipapi.OperationOpenAIChatCompletions, 100)

	// Item 1: system message with chunked text
	var fullText strings.Builder
	for i := range totalChunks {
		currentChunk := chunkData
		switch i {
		case 500:
			currentChunk = strings.Repeat("x", chunkSize-len(partA)) + partA
		case 501:
			currentChunk = partB + strings.Repeat("x", chunkSize-len(partB))
		}
		fullText.WriteString(currentChunk)
		builder.FeedTextChunk(currentChunk)
	}

	// Add hash for Item 1
	it1 := aipapi.Item{
		Kind:    aipapi.ItemKindMessage,
		Role:    aipapi.RoleSystem,
		Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: fullText.String()}},
	}
	if err := builder.AddItemHash(compactionfacts.HashItem(it1)); err != nil {
		t.Fatalf("AddItemHash: %v", err)
	}

	// Item 2: User follow-up
	it2 := aipapi.Item{
		Kind:    aipapi.ItemKindMessage,
		Role:    aipapi.RoleUser,
		Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "follow up"}},
	}
	builder.FeedTextChunk("follow up")
	if err := builder.AddItemHash(compactionfacts.HashItem(it2)); err != nil {
		t.Fatalf("AddItemHash: %v", err)
	}

	facts, err := builder.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	if !facts.StartRuleMatched {
		t.Fatalf("expected start rule match across chunk split")
	}
	if facts.StartRuleID != compactionfacts.RuleCodexLocalCheckpoint {
		t.Errorf("StartRuleID: got %q, want %q", facts.StartRuleID, compactionfacts.RuleCodexLocalCheckpoint)
	}
	expectedRunes := utf8.RuneCountInString(fullText.String()) + utf8.RuneCountInString("follow up")
	if facts.EstimatedTokens != expectedRunes/4 {
		t.Errorf("EstimatedTokens: got %d, want %d", facts.EstimatedTokens, expectedRunes/4)
	}
	if facts.ItemCount != 2 {
		t.Errorf("ItemCount: got %d, want 2", facts.ItemCount)
	}
	if len(facts.ItemHashes) != 2 {
		t.Errorf("ItemHashes len: got %d, want 2", len(facts.ItemHashes))
	}
	if facts.TailLen != 2 {
		t.Errorf("TailLen: got %d, want 2", facts.TailLen)
	}
}

func TestIncrementalBuilder_BudgetOverflow(t *testing.T) {
	t.Parallel()

	// 1. Budget with explicit items
	budget := 3
	builder := compactionfacts.NewBuilder(aipapi.OperationOpenAIChatCompletions, budget)

	it := aipapi.Item{
		Kind:    aipapi.ItemKindMessage,
		Role:    aipapi.RoleUser,
		Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "test"}},
	}
	h := compactionfacts.HashItem(it)

	for i := range budget {
		require.NoError(t, builder.AddItemHash(h), "item %d within budget", i)
	}

	// Next item exceeds budget -> bound checked BEFORE allocation!
	err := builder.AddItemHash(h)
	require.ErrorIs(t, err, compactionfacts.ErrFactBudgetExceeded)

	// Sticky error: subsequent calls must also return ErrFactBudgetExceeded
	err2 := builder.AddItemHash(h)
	require.ErrorIs(t, err2, compactionfacts.ErrFactBudgetExceeded)

	err3 := builder.AddItem(it)
	require.ErrorIs(t, err3, compactionfacts.ErrFactBudgetExceeded)

	// Build must return empty facts and sticky error (NO consumable partial facts!)
	facts, buildErr := builder.Build()
	require.ErrorIs(t, buildErr, compactionfacts.ErrFactBudgetExceeded)
	assert.Equal(t, compactionfacts.RequestFacts{}, facts, "overflow must return empty RequestFacts without consumable partial state")

	// 2. Budget derived from byte budget: 64 bytes -> 64 / 32 = 2 items
	byteBuilder := compactionfacts.NewBuilderWithByteBudget(aipapi.OperationOpenAIChatCompletions, 64)
	require.NoError(t, byteBuilder.AddItemHash(h))
	require.NoError(t, byteBuilder.AddItemHash(h))
	require.ErrorIs(t, byteBuilder.AddItemHash(h), compactionfacts.ErrFactBudgetExceeded)
}

func TestFieldBoundary_DistinctFieldsVsChunksWithinField(t *testing.T) {
	t.Parallel()

	// 1. Separate fields: field1='context checkpoint comp', field2='action' MUST NOT match.
	t.Run("separate_fields_must_not_match", func(t *testing.T) {
		t.Parallel()
		builder := compactionfacts.NewBuilder(aipapi.OperationOpenAIChatCompletions, 10)
		builder.FeedField("context checkpoint comp")
		builder.FeedField("action")
		facts, err := builder.Build()
		require.NoError(t, err)
		assert.False(t, facts.StartRuleMatched, "separate fields must not match across canonical newline boundary")
		assert.Empty(t, facts.StartRuleID)

		// Also verify canonical Call path
		call := aipapi.Call{
			Invocation: aipapi.Invocation{Operation: aipapi.OperationOpenAIChatCompletions},
			Messages: []aipapi.Message{
				{Role: aipapi.RoleUser, Parts: []aipapi.Part{
					aipapi.TextPart("context checkpoint comp"),
					aipapi.TextPart("action"),
				}},
			},
		}
		callFacts := compactionfacts.ExtractFactsFromCall(call)
		assert.False(t, callFacts.StartRuleMatched, "canonical call across separate parts must not match")
		assert.Empty(t, callFacts.StartRuleID)
	})

	// 2. Same string split into chunks within ONE field MUST match.
	t.Run("chunks_within_one_field_must_match", func(t *testing.T) {
		t.Parallel()
		builder := compactionfacts.NewBuilder(aipapi.OperationOpenAIChatCompletions, 10)
		builder.FeedTextChunk("context checkpoint comp")
		builder.FeedTextChunk("action")
		builder.EndField()
		facts, err := builder.Build()
		require.NoError(t, err)
		assert.True(t, facts.StartRuleMatched, "chunks within one field must match across chunk split")
		assert.Equal(t, compactionfacts.RuleCodexLocalCheckpoint, facts.StartRuleID)
		assert.Equal(t, compactionfacts.EvidenceSignatureStrict, facts.StartRuleEvidence)
	})
}

func TestUnicodeChunks_CaseFold_ExactRuneCount(t *testing.T) {
	t.Parallel()

	// Multi-byte Unicode string:
	// "Résumé: [CONTEXT COMPACTION \u2014 REFERENCE ONLY] \U0001F600"
	// 'é' is 2 bytes (0xC3, 0xA9)
	// '\u2014' is 3 bytes (0xE2, 0x80, 0x94)
	// '\U0001F600' is 4 bytes (0xF0, 0x9F, 0x98, 0x80)
	raw := "Résumé: [CONTEXT COMPACTION \u2014 REFERENCE ONLY] \U0001F600"
	expectedRunes := utf8.RuneCountInString(raw)

	rawBytes := []byte(raw)

	// Split raw bytes into chunks dividing multi-byte runes:
	// "R" (1 byte), "é" (starts at index 1: 0xC3, 0xA9) -> split at 2 (after 0xC3)
	split1 := 2
	idxDash := strings.Index(raw, "\u2014")
	split2 := idxDash + 2 // include 0xE2, 0x80, split before 0x94
	idxEmoji := strings.Index(raw, "\U0001F600")
	split3 := idxEmoji + 2 // include 0xF0, 0x9F, split before 0x98, 0x80

	chunk0 := string(rawBytes[:split1])
	chunk1 := string(rawBytes[split1:split2])
	chunk2 := string(rawBytes[split2:split3])
	chunk3 := string(rawBytes[split3:])

	builder := compactionfacts.NewBuilder(aipapi.OperationOpenAIChatCompletions, 10)
	builder.FeedTextChunk(chunk0)
	builder.FeedTextChunk(chunk1)
	builder.FeedTextChunk(chunk2)
	builder.FeedTextChunk(chunk3)
	builder.EndField()

	facts, err := builder.Build()
	require.NoError(t, err)

	assert.Equal(t, expectedRunes/4, facts.EstimatedTokens, "rune count and token estimation must be exact despite split UTF-8 runes")

	// Matcher must have properly casefolded and found marker without UTF-8 corruption replacement characters
	tm := compactionfacts.NewTextMatcher()
	tm.FeedChunk(chunk0)
	tm.FeedChunk(chunk1)
	tm.FeedChunk(chunk2)
	tm.FeedChunk(chunk3)
	tm.EndField()
	assert.True(t, tm.HasText(compactionfacts.MarkerHermesRefOnly), "marker with multi-byte em-dash must match across split UTF-8 boundaries")
}

func TestCanonical_HighItemCount_NoSilentTruncation(t *testing.T) {
	t.Parallel()

	// Canonical Call path with >4096 items (e.g. 5000 items).
	// Must NOT silently truncate at fixed maxItems.
	totalItems := 5000
	items := make([]aipapi.Item, totalItems)
	for i := range totalItems {
		items[i] = aipapi.Item{
			Kind:    aipapi.ItemKindMessage,
			Role:    aipapi.RoleUser,
			Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: fmt.Sprintf("msg-%d", i)}},
		}
	}
	call := aipapi.Call{
		Invocation: aipapi.Invocation{Operation: aipapi.OperationOpenAIChatCompletions},
		Items:      items,
	}

	facts := compactionfacts.ExtractFactsFromCall(call)
	assert.Equal(t, totalItems, facts.ItemCount, "ItemCount must be exactly 5000 without truncation")
	assert.Equal(t, totalItems, len(facts.ItemHashes), "ItemHashes slice must retain all 5000 hashes")
	assert.Equal(t, compactionfacts.HeuristicTailItems, facts.TailLen)
	assert.Equal(t, compactionfacts.HeuristicPrefixItems, facts.PrefixItems)
}

func TestExtractFactsFromCall_PreservesAllWalkCallTextsSources(t *testing.T) {
	t.Parallel()

	call := aipapi.Call{
		Invocation: aipapi.Invocation{Operation: aipapi.OperationOpenAIChatCompletions},
		Tools:      []aipapi.ToolDef{{Name: "calculator"}},
		Items: []aipapi.Item{
			{
				Kind: aipapi.ItemKindMessage,
				Role: aipapi.RoleUser,
				Content: []aipapi.ContentPart{
					{Kind: aipapi.ContentPartText, Text: "content text"},
					{Kind: aipapi.ContentPartText, Refusal: "refusal text"},
					{Kind: aipapi.ContentPartSummary, Summary: "summary text"},
					{Kind: aipapi.ContentPartReasoning, Reasoning: &aipapi.ReasoningPart{Text: "reasoning in content"}},
				},
				ToolResult: &aipapi.ToolResultItem{
					Output: "tool output",
					Parts: []aipapi.ContentPart{
						{Kind: aipapi.ContentPartText, Text: "part text"},
						{Kind: aipapi.ContentPartText, Refusal: "part refusal"},
						{Kind: aipapi.ContentPartSummary, Summary: "part summary"},
						{Kind: aipapi.ContentPartReasoning, Reasoning: &aipapi.ReasoningPart{Text: "part reasoning"}},
					},
				},
				Reasoning: &aipapi.ReasoningItem{
					Reasoning: &aipapi.ReasoningPart{Text: "item reasoning"},
				},
			},
		},
	}

	expectedRunes := 0
	_ = aipapi.WalkCallTexts(call, func(_ string, text string) error {
		expectedRunes += utf8.RuneCountInString(text)
		return nil
	})

	facts := compactionfacts.ExtractFactsFromCall(call)
	assert.Equal(t, expectedRunes/4, facts.EstimatedTokens)
	assert.Equal(t, 1, facts.ItemCount)
	assert.Equal(t, 1, len(facts.ItemHashes))
	assert.Equal(t, 1, facts.ToolCount)
}

func TestDetector_MultiTurnAndResponseCompletion(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	meta := compaction.PreservationMeta{
		TraceID:   "trace-det-1",
		SessionID: "sess-det-1",
		ALegID:    "aleg-det-1",
		BLegID:    "bleg-det-1",
	}

	d := compactiondetect.New(compactiondetect.Config{
		Now: func() time.Time { return now },
	})

	// Turn 1: large baseline (>8000 tokens)
	bigText := strings.Repeat("substantial context tokens exceeding eight thousand threshold. ", 600)
	tail1 := "tail 1 message"
	tail2 := "tail 2 message"
	turn1Call := aipapi.Call{
		Messages: []aipapi.Message{
			{Role: aipapi.RoleSystem, Parts: []aipapi.Part{aipapi.TextPart(bigText)}},
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart(tail1)}},
			{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{aipapi.TextPart(tail2)}},
		},
	}
	events1 := d.RequestOpened(meta, turn1Call)
	if len(events1) != 0 {
		t.Fatalf("turn 1 should emit 0 events, got %d", len(events1))
	}

	// Turn 2: significantly reduced context with preserved tail -> heuristic completion event!
	meta2 := meta
	meta2.TraceID = "trace-det-2"
	turn2Call := aipapi.Call{
		Messages: []aipapi.Message{
			{Role: aipapi.RoleSystem, Parts: []aipapi.Part{aipapi.TextPart("compacted summary")}},
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart(tail1)}},
			{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{aipapi.TextPart(tail2)}},
		},
	}
	events2 := d.RequestOpened(meta2, turn2Call)
	if len(events2) != 1 {
		t.Fatalf("turn 2 expected 1 completion event, got %d", len(events2))
	}
	if events2[0].Phase != compaction.PhaseCompleted {
		t.Errorf("Phase: got %v, want PhaseCompleted", events2[0].Phase)
	}
	if events2[0].RuleID != compactiondetect.HeuristicRuleID {
		t.Errorf("RuleID: got %q, want %q", events2[0].RuleID, compactiondetect.HeuristicRuleID)
	}
	if events2[0].Evidence != compaction.EvidenceHistoryHeuristic {
		t.Errorf("Evidence: got %v, want %v", events2[0].Evidence, compaction.EvidenceHistoryHeuristic)
	}

	// Turn 3: Start rule (cline agentic) + released response completion marker
	meta3 := meta
	meta3.TraceID = "trace-det-3"
	turn3Call := aipapi.Call{
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("continuation-note: summarize progress")}},
		},
	}
	startEvents := d.RequestOpened(meta3, turn3Call)
	if len(startEvents) != 1 {
		t.Fatalf("turn 3 expected 1 start event, got %d", len(startEvents))
	}
	if startEvents[0].Phase != compaction.PhaseStarted {
		t.Errorf("Phase: got %v, want PhaseStarted", startEvents[0].Phase)
	}
	if startEvents[0].RuleID != "cline.agentic_compaction.v1" {
		t.Errorf("RuleID: got %q, want cline.agentic_compaction.v1", startEvents[0].RuleID)
	}

	// Release response text with post marker
	respEvent := aipapi.Event{
		Kind:  aipapi.EventTextDelta,
		Delta: "Done. Context summary: summary content here",
	}
	compEvents := d.ResponseReleased(meta3, respEvent)
	if len(compEvents) != 1 {
		t.Fatalf("expected 1 response completion event, got %d", len(compEvents))
	}
	if compEvents[0].Phase != compaction.PhaseCompleted {
		t.Errorf("Phase: got %v, want PhaseCompleted", compEvents[0].Phase)
	}
	if compEvents[0].RuleID != "cline.agentic_compaction.v1" {
		t.Errorf("RuleID: got %q, want cline.agentic_compaction.v1", compEvents[0].RuleID)
	}
	if compEvents[0].TransactionID != startEvents[0].TransactionID {
		t.Errorf("TransactionID mismatch: %q vs %q", compEvents[0].TransactionID, startEvents[0].TransactionID)
	}
}

func TestTextMatcher_ExactUnicodeToLower_KelvinAndLongS(t *testing.T) {
	t.Parallel()

	// 1. Kelvin sign \u212A (UTF-8: 0xE2 0x84 0xAA) folds to 'k'.
	// In single chunk:
	m1 := compactionfacts.NewTextMatcher()
	// "checkpoint" marker requires 'k' in "checkpoint". Let's test with "\u212A" in place of 'K' in "checKpoint".
	runes1 := m1.FeedChunk("chec\u212Apoint")
	assert.Equal(t, utf8.RuneCountInString("chec\u212Apoint"), runes1)
	assert.True(t, m1.HasText("checkpoint"), "Kelvin sign must fold to 'k' and match 'checkpoint'")

	// 2. Kelvin sign split across chunk boundary:
	// Chunk 1 has 0xE2 0x84 (2 bytes)
	// Chunk 2 has 0xAA (1 byte) + "point"
	m2 := compactionfacts.NewTextMatcher()
	rA := m2.FeedBytesChunk([]byte{'c', 'h', 'e', 'c', 0xE2, 0x84})
	assert.Equal(t, 4, rA, "Pending incomplete UTF-8 bytes must not be counted as complete runes yet")
	rB := m2.FeedBytesChunk([]byte{0xAA, 'p', 'o', 'i', 'n', 't'})
	assert.Equal(t, 6, rB, "Completed UTF-8 sequence must be counted with remaining runes")
	assert.Equal(t, 10, rA+rB)
	assert.True(t, m2.HasText("checkpoint"), "Split Kelvin sign must be assembled across chunks and match 'checkpoint'")

	// 3. Long S \u017F (UTF-8: 0xC5 0xBF) does NOT fold to 's' under canonical strings.ToLower.
	// "preserve" marker: "pre\u017Ferve" -> stays "pre\u017Ferve" (not "preserve")
	m3 := compactionfacts.NewTextMatcher()
	runes3 := m3.FeedChunk("pre\u017Ferve")
	assert.Equal(t, utf8.RuneCountInString("pre\u017Ferve"), runes3)
	assert.False(t, m3.HasText("preserve"), "Long S must NOT fold to 's' under canonical strings.ToLower")
	assert.False(t, strings.Contains(strings.ToLower("pre\u017Ferve"), "preserve"), "strings.ToLower must also not fold Long S to 's'")

	// 4. Long S split across chunks:
	m4 := compactionfacts.NewTextMatcher()
	rC := m4.FeedBytesChunk([]byte{'p', 'r', 'e', 0xC5})
	assert.Equal(t, 3, rC)
	rD := m4.FeedBytesChunk([]byte{0xBF, 'e', 'r', 'v', 'e'})
	assert.Equal(t, 5, rD)
	assert.Equal(t, 8, rC+rD)
	assert.False(t, m4.HasText("preserve"), "Split Long S must not match 'preserve'")

	// 5. Pin against base canonical strings.ToLower marker matching and rune counts
	text := "  Context Checkpoint Compaction With \u212A and \u017F  "
	expectedLower := strings.ToLower(text)
	expectedRunes := utf8.RuneCountInString(text)

	m5 := compactionfacts.NewTextMatcher()
	countedRunes := m5.FeedChunk(text)
	assert.Equal(t, expectedRunes, countedRunes)
	assert.True(t, strings.Contains(expectedLower, "context checkpoint compaction"))
	assert.True(t, m5.HasText("context checkpoint compaction"))

	// 6. Field separator: EndField feeds '\n', preventing cross-field match
	m6 := compactionfacts.NewTextMatcher()
	m6.FeedChunk("context")
	m6.EndField()
	m6.FeedChunk("checkpoint compaction")
	assert.False(t, m6.HasText("context checkpoint compaction"), "Field separator must prevent cross-field marker match")

	// 7. Invalid UTF-8 handling pinned against standard behavior:
	// A chunk containing invalid bytes (0xFF, 0xFE, solitary continuation 0x80)
	// should decode each invalid byte as RuneError, not crash, and maintain accurate rune counts.
	invalidChunk := []byte{'c', 'o', 'n', 't', 'e', 'x', 't', ' ', 0xFF, 'c', 'h', 'e', 'c', 'k', 'p', 'o', 'i', 'n', 't'}
	m7 := compactionfacts.NewTextMatcher()
	r7 := m7.FeedBytesChunk(invalidChunk)
	assert.Equal(t, utf8.RuneCount(invalidChunk), r7)
	assert.True(t, m7.HasText("context"), "Marker before invalid byte must be detected")
	assert.True(t, m7.HasText("checkpoint"), "Marker after invalid byte must be detected")

	// 8. Incomplete UTF-8 followed by invalid byte across chunks:
	// Chunk 1 ends with leading byte 0xC2 (incomplete 2-byte sequence)
	// Chunk 2 starts with 0xFF (invalid continuation) followed by "checkpoint"
	m8 := compactionfacts.NewTextMatcher()
	r8a := m8.FeedBytesChunk([]byte{'p', 'r', 'e', 0xC2})
	assert.Equal(t, 3, r8a, "Trailing incomplete byte held in pending")
	r8b := m8.FeedBytesChunk([]byte{0xFF, 'c', 'h', 'e', 'c', 'k', 'p', 'o', 'i', 'n', 't'})
	// 0xC2 + 0xFF is decoded as 2 RuneErrors (0xC2 is invalid, 0xFF is invalid)
	assert.Equal(t, 2+len("checkpoint"), r8b)
	assert.True(t, m8.HasText("checkpoint"))
}

func TestRequestFacts_Clone_DeepCopiesItemHashes(t *testing.T) {
	t.Parallel()

	h1 := sha256.Sum256([]byte("item-1"))
	h2 := sha256.Sum256([]byte("item-2"))

	orig := compactionfacts.RequestFacts{
		Operation:   aipapi.OperationOpenAIChatCompletions,
		ItemCount:   2,
		ItemHashes:  [][32]byte{h1, h2},
		StartRuleID: "codex.local_checkpoint.v1",
	}

	cloned := orig.Clone()
	require.Equal(t, orig.ItemHashes, cloned.ItemHashes)

	// Mutate original slice
	orig.ItemHashes[0][0] ^= 0xFF
	assert.NotEqual(t, orig.ItemHashes[0], cloned.ItemHashes[0], "Mutating original must not alter cloned slice")
}

func TestCompactionFacts_Digest_CoversAllBehaviorFieldsAndCompleteness(t *testing.T) {
	t.Parallel()

	h1 := sha256.Sum256([]byte("hash-1"))
	base := compactionfacts.RequestFacts{
		Operation:         aipapi.OperationOpenAIChatCompletions,
		ToolCount:         1,
		EstimatedTokens:   100,
		ItemCount:         1,
		ItemHashes:        [][32]byte{h1},
		TailHashes:        [compactionfacts.HeuristicTailItems][32]byte{h1},
		TailLen:           1,
		PrefixHash:        h1,
		PrefixItems:       1,
		StartRuleMatched:  true,
		StartRuleID:       "rule-1",
		StartRuleMode:     compactionfacts.RuleModeSingle,
		StartRuleEvidence: compactionfacts.EvidenceSignatureStrict,
	}

	baseDigest := compactionfacts.Digest(base, true)
	assert.NotEqual(t, [32]byte{}, baseDigest)

	// Completeness mutation
	assert.NotEqual(t, baseDigest, compactionfacts.Digest(base, false), "Completeness change must alter digest")

	// Operation mutation
	fOp := base
	fOp.Operation = aipapi.OperationOpenAIResponses
	assert.NotEqual(t, baseDigest, compactionfacts.Digest(fOp, true))

	// ToolCount mutation
	fTools := base
	fTools.ToolCount = 2
	assert.NotEqual(t, baseDigest, compactionfacts.Digest(fTools, true))

	// EstimatedTokens mutation
	fTokens := base
	fTokens.EstimatedTokens = 200
	assert.NotEqual(t, baseDigest, compactionfacts.Digest(fTokens, true))

	// ItemCount mutation
	fCount := base
	fCount.ItemCount = 2
	assert.NotEqual(t, baseDigest, compactionfacts.Digest(fCount, true))

	// ItemHashes mutation
	fHashes := base.Clone()
	fHashes.ItemHashes[0][0] ^= 0xFF
	assert.NotEqual(t, baseDigest, compactionfacts.Digest(fHashes, true))

	// StartRuleID mutation
	fRule := base
	fRule.StartRuleID = "rule-2"
	assert.NotEqual(t, baseDigest, compactionfacts.Digest(fRule, true))

	// StartRuleMatched mutation
	fMatched := base
	fMatched.StartRuleMatched = false
	assert.NotEqual(t, baseDigest, compactionfacts.Digest(fMatched, true))

	// StartRuleMode mutation
	fMode := base
	fMode.StartRuleMode = compactionfacts.RuleModeSeries
	assert.NotEqual(t, baseDigest, compactionfacts.Digest(fMode, true))

	// StartRuleEvidence mutation
	fEv := base
	fEv.StartRuleEvidence = compactionfacts.EvidenceProtocolStrict
	assert.NotEqual(t, baseDigest, compactionfacts.Digest(fEv, true))
}
