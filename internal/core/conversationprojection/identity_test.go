package conversationprojection_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aiproxer/aiproxer/internal/core/conversationprojection"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestMessageIdentity_FormatAndParsing(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		raw         string
		wantValid   bool
		wantVersion string
		wantDigest  string
	}{
		{
			name:        "valid v1 identity",
			raw:         "v1:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
			wantValid:   true,
			wantVersion: "v1",
			wantDigest:  "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		},
		{
			name:      "empty string",
			raw:       "",
			wantValid: false,
		},
		{
			name:      "missing prefix",
			raw:       "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
			wantValid: false,
		},
		{
			name:      "unsupported version",
			raw:       "v2:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
			wantValid: false,
		},
		{
			name:      "uppercase hex rejected",
			raw:       "v1:E3B0C44298FC1C149AFBF4C8996FB92427AE41E4649B934CA495991B7852B855",
			wantValid: false,
		},
		{
			name:      "too short digest",
			raw:       "v1:e3b0c44298fc1c14",
			wantValid: false,
		},
		{
			name:      "invalid hex characters",
			raw:       "v1:g3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b85z",
			wantValid: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			id := conversationprojection.MessageIdentity(tc.raw)
			if tc.wantValid {
				require.NoError(t, id.Validate())
				assert.True(t, id.IsValid())
				assert.Equal(t, tc.wantVersion, id.Version())
				assert.Equal(t, tc.wantDigest, id.Digest())
				assert.Equal(t, tc.raw, id.String())

				parsed, err := conversationprojection.ParseMessageIdentity(tc.raw)
				require.NoError(t, err)
				assert.Equal(t, id, parsed)
			} else {
				assert.Error(t, id.Validate())
				assert.False(t, id.IsValid())

				_, err := conversationprojection.ParseMessageIdentity(tc.raw)
				assert.Error(t, err)
			}
		})
	}
}

func TestMessageIdentity_LegacyAndItemAuthorityEquivalence(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		legacy aipapi.Message
		item   aipapi.Item
		role   aipapi.Role
	}{
		{
			name: "plain text user message",
			legacy: aipapi.Message{
				Role: aipapi.RoleUser,
				Parts: []aipapi.Part{
					{Kind: aipapi.PartText, Text: "Hello, world!"},
				},
			},
			item: aipapi.Item{
				Kind:   aipapi.ItemKindMessage,
				ID:     "item-12345",
				Status: aipapi.ItemStatusCompleted,
				Role:   aipapi.RoleUser,
				Content: []aipapi.ContentPart{
					{Kind: aipapi.ContentPartText, Text: "Hello, world!"},
				},
			},
		},
		{
			name: "system instructions message",
			legacy: aipapi.Message{
				Role: aipapi.RoleSystem,
				Parts: []aipapi.Part{
					{Kind: aipapi.PartText, Text: "You are a helpful coding assistant."},
				},
			},
			item: aipapi.Item{
				Kind: aipapi.ItemKindMessage,
				Role: aipapi.RoleSystem,
				Content: []aipapi.ContentPart{
					{Kind: aipapi.ContentPartText, Text: "You are a helpful coding assistant."},
				},
			},
		},
		{
			name: "assistant commentary message with multiple text parts",
			legacy: aipapi.Message{
				Role: aipapi.RoleAssistant,
				Parts: []aipapi.Part{
					{Kind: aipapi.PartText, Text: "Thinking step 1..."},
					{Kind: aipapi.PartText, Text: "Concluding response."},
				},
			},
			item: aipapi.Item{
				Kind:  aipapi.ItemKindMessage,
				Role:  aipapi.RoleAssistant,
				Phase: aipapi.AssistantPhaseCommentary,
				Content: []aipapi.ContentPart{
					{Kind: aipapi.ContentPartText, Text: "Thinking step 1..."},
					{Kind: aipapi.ContentPartText, Text: "Concluding response."},
				},
			},
		},
		{
			name: "file ref message",
			legacy: aipapi.Message{
				Role: aipapi.RoleUser,
				Parts: []aipapi.Part{
					{Kind: aipapi.PartFileRef, FileRef: "file-ref-1", FileMIME: "text/plain", FileName: "doc.txt"},
				},
			},
			item: aipapi.Item{
				Kind: aipapi.ItemKindMessage,
				Role: aipapi.RoleUser,
				Content: []aipapi.ContentPart{
					{Kind: aipapi.ContentPartFileRef, FileRef: "file-ref-1", FileMIME: "text/plain", FileName: "doc.txt"},
				},
			},
		},
		{
			name: "tool result message",
			legacy: aipapi.Message{
				Role: aipapi.RoleTool,
				Parts: []aipapi.Part{
					{Kind: aipapi.PartToolResult, ToolCallID: "call_abc", ToolName: "calc", Text: "42"},
				},
			},
			item: aipapi.Item{
				Kind: aipapi.ItemKindMessage,
				Role: aipapi.RoleTool,
				Content: []aipapi.ContentPart{
					{Kind: aipapi.ContentPartToolResult, Text: "42"},
				},
			},
		},
		{
			name: "json content message",
			legacy: aipapi.Message{
				Role: aipapi.RoleUser,
				Parts: []aipapi.Part{
					{Kind: aipapi.PartJSON, Content: json.RawMessage(`{"status":"ok","code":200}`)},
				},
			},
			item: aipapi.Item{
				Kind: aipapi.ItemKindMessage,
				Role: aipapi.RoleUser,
				Content: []aipapi.ContentPart{
					{
						Kind:       aipapi.ContentPartJSON,
						Text:       `{"code":200,"status":"ok"}`,
						Annotation: &aipapi.AnnotationPart{Type: "json_content", Data: json.RawMessage(`{"code":200,"status":"ok"}`)},
					},
				},
			},
		},
		{
			name: "reasoning part assistant message",
			legacy: aipapi.Message{
				Role: aipapi.RoleAssistant,
				Parts: []aipapi.Part{
					{
						Kind: aipapi.PartReasoning,
						Reasoning: &aipapi.ReasoningPart{
							Dialect:   "standard",
							Text:      "internal thinking",
							Signature: "sig_abc",
						},
					},
				},
			},
			item: aipapi.Item{
				Kind: aipapi.ItemKindMessage,
				Role: aipapi.RoleAssistant,
				Content: []aipapi.ContentPart{
					{
						Kind: aipapi.ContentPartReasoning,
						Reasoning: &aipapi.ReasoningPart{
							Dialect:   "standard",
							Text:      "internal thinking",
							Signature: "sig_abc",
						},
					},
				},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			legacyID, err := conversationprojection.MessageIdentityOf(tc.legacy)
			require.NoError(t, err)
			require.True(t, legacyID.IsValid(), "legacy identity must be valid v1 identity")

			itemID, err := conversationprojection.ItemIdentityOf(tc.item)
			require.NoError(t, err)
			require.True(t, itemID.IsValid(), "item identity must be valid v1 identity")

			assert.Equal(t, legacyID, itemID, "legacy message and item message must produce identical semantic identity")
		})
	}
}

func TestMessageIdentity_LineEndingNormalization(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		crlfMsg     aipapi.Message
		lfMsg       aipapi.Message
		crMsg       aipapi.Message
		distinctMsg aipapi.Message
	}{
		{
			name: "text with CRLF vs LF vs CR",
			crlfMsg: aipapi.Message{
				Role:  aipapi.RoleUser,
				Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: "Line 1\r\nLine 2\r\nLine 3"}},
			},
			lfMsg: aipapi.Message{
				Role:  aipapi.RoleUser,
				Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: "Line 1\nLine 2\nLine 3"}},
			},
			crMsg: aipapi.Message{
				Role:  aipapi.RoleUser,
				Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: "Line 1\rLine 2\rLine 3"}},
			},
			distinctMsg: aipapi.Message{
				Role:  aipapi.RoleUser,
				Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: "Line 1 Line 2 Line 3"}},
			},
		},
		{
			name: "multiline reasoning text with CRLF vs LF",
			crlfMsg: aipapi.Message{
				Role: aipapi.RoleAssistant,
				Parts: []aipapi.Part{
					{
						Kind: aipapi.PartReasoning,
						Reasoning: &aipapi.ReasoningPart{
							Dialect: "standard",
							Text:    "First line\r\nSecond line\r\n",
						},
					},
				},
			},
			lfMsg: aipapi.Message{
				Role: aipapi.RoleAssistant,
				Parts: []aipapi.Part{
					{
						Kind: aipapi.PartReasoning,
						Reasoning: &aipapi.ReasoningPart{
							Dialect: "standard",
							Text:    "First line\nSecond line\n",
						},
					},
				},
			},
			crMsg: aipapi.Message{
				Role: aipapi.RoleAssistant,
				Parts: []aipapi.Part{
					{
						Kind: aipapi.PartReasoning,
						Reasoning: &aipapi.ReasoningPart{
							Dialect: "standard",
							Text:    "First line\rSecond line\r",
						},
					},
				},
			},
			distinctMsg: aipapi.Message{
				Role: aipapi.RoleAssistant,
				Parts: []aipapi.Part{
					{
						Kind: aipapi.PartReasoning,
						Reasoning: &aipapi.ReasoningPart{
							Dialect: "standard",
							Text:    "First line Second line",
						},
					},
				},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			crlfID, err := conversationprojection.MessageIdentityOf(tc.crlfMsg)
			require.NoError(t, err)

			lfID, err := conversationprojection.MessageIdentityOf(tc.lfMsg)
			require.NoError(t, err)

			crID, err := conversationprojection.MessageIdentityOf(tc.crMsg)
			require.NoError(t, err)

			distinctID, err := conversationprojection.MessageIdentityOf(tc.distinctMsg)
			require.NoError(t, err)

			assert.Equal(t, lfID, crlfID, "CRLF and LF must yield identical identity")
			assert.Equal(t, lfID, crID, "CR and LF must yield identical identity")
			assert.NotEqual(t, lfID, distinctID, "distinct text must yield distinct identity")
		})
	}
}

func TestMessageIdentity_WhitespaceAndUnicodePreservation(t *testing.T) {
	t.Parallel()

	// Whitespace other than CRLF/CR must be preserved strictly
	msg1 := aipapi.Message{Role: aipapi.RoleUser, Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: "Hello "}}}
	msg2 := aipapi.Message{Role: aipapi.RoleUser, Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: "Hello"}}}
	msg3 := aipapi.Message{Role: aipapi.RoleUser, Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: "Hello\tWorld"}}}
	msg4 := aipapi.Message{Role: aipapi.RoleUser, Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: "Hello World"}}}

	id1, err := conversationprojection.MessageIdentityOf(msg1)
	require.NoError(t, err)
	id2, err := conversationprojection.MessageIdentityOf(msg2)
	require.NoError(t, err)
	id3, err := conversationprojection.MessageIdentityOf(msg3)
	require.NoError(t, err)
	id4, err := conversationprojection.MessageIdentityOf(msg4)
	require.NoError(t, err)

	assert.NotEqual(t, id1, id2, "trailing whitespace difference must produce distinct identities")
	assert.NotEqual(t, id3, id4, "tab vs space must produce distinct identities")

	// Unicode characters preserved identically
	unicodeMsg1 := aipapi.Message{
		Role:  aipapi.RoleUser,
		Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: "你好，世界 🌍 Übergröße"}},
	}
	unicodeMsg2 := aipapi.Message{
		Role:  aipapi.RoleUser,
		Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: "你好，世界 🌍 Übergröße"}},
	}
	uID1, err := conversationprojection.MessageIdentityOf(unicodeMsg1)
	require.NoError(t, err)
	uID2, err := conversationprojection.MessageIdentityOf(unicodeMsg2)
	require.NoError(t, err)

	assert.Equal(t, uID1, uID2, "identical unicode messages must produce identical identities")
}

func TestMessageIdentity_StructuredJSONCanonicalization(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw1 string
		raw2 string
	}{
		{
			name: "flat object with reversed keys",
			raw1: `{"b": 2, "a": 1}`,
			raw2: `{"a": 1, "b": 2}`,
		},
		{
			name: "nested object with whitespace differences",
			raw1: `{"outer": {"z": true, "a": [1, 2]}, "count": 10}`,
			raw2: "{\n  \"count\": 10,\n  \"outer\": {\n    \"a\": [1, 2],\n    \"z\": true\n  }\n}",
		},
		{
			name: "deeply nested objects",
			raw1: `{"d":{"c":{"b":{"a":100}}}}`,
			raw2: `{"d": { "c": { "b": { "a": 100 } } } }`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			msg1 := aipapi.Message{
				Role: aipapi.RoleUser,
				Parts: []aipapi.Part{
					{Kind: aipapi.PartJSON, Content: json.RawMessage(tc.raw1)},
				},
			}
			msg2 := aipapi.Message{
				Role: aipapi.RoleUser,
				Parts: []aipapi.Part{
					{Kind: aipapi.PartJSON, Content: json.RawMessage(tc.raw2)},
				},
			}

			id1, err := conversationprojection.MessageIdentityOf(msg1)
			require.NoError(t, err)
			id2, err := conversationprojection.MessageIdentityOf(msg2)
			require.NoError(t, err)

			assert.Equal(t, id1, id2, "semantically equivalent JSON with different key orders or whitespace must produce identical identity")
		})
	}
}

func TestMessageIdentity_TransientFieldExclusion(t *testing.T) {
	t.Parallel()

	// Base message
	baseItem := aipapi.Item{
		Kind:   aipapi.ItemKindMessage,
		ID:     "transport-item-1",
		Status: aipapi.ItemStatusInProgress,
		Phase:  aipapi.AssistantPhaseCommentary,
		Role:   aipapi.RoleAssistant,
		Content: []aipapi.ContentPart{
			{Kind: aipapi.ContentPartText, Text: "Assistant response"},
		},
	}

	// Message with different transport ID, completed status, final answer phase
	variantItem := aipapi.Item{
		Kind:   aipapi.ItemKindMessage,
		ID:     "different-generated-id-999",
		Status: aipapi.ItemStatusCompleted,
		Phase:  aipapi.AssistantPhaseFinalAnswer,
		Role:   aipapi.RoleAssistant,
		Content: []aipapi.ContentPart{
			{Kind: aipapi.ContentPartText, Text: "Assistant response"},
		},
	}

	idBase, err := conversationprojection.ItemIdentityOf(baseItem)
	require.NoError(t, err)

	idVariant, err := conversationprojection.ItemIdentityOf(variantItem)
	require.NoError(t, err)

	assert.Equal(t, idBase, idVariant, "transient carriers (ID, Status, Phase) must be excluded from semantic identity")

	// Legacy message with proxy metadata
	legacy1 := aipapi.Message{
		Role:     aipapi.RoleUser,
		Parts:    []aipapi.Part{{Kind: aipapi.PartText, Text: "Hello"}},
		Metadata: map[string]string{"proxy_trace_id": "tr-123", "b_leg": "bleg-456"},
	}
	legacy2 := aipapi.Message{
		Role:     aipapi.RoleUser,
		Parts:    []aipapi.Part{{Kind: aipapi.PartText, Text: "Hello"}},
		Metadata: nil,
	}

	idLeg1, err := conversationprojection.MessageIdentityOf(legacy1)
	require.NoError(t, err)
	idLeg2, err := conversationprojection.MessageIdentityOf(legacy2)
	require.NoError(t, err)

	assert.Equal(t, idLeg1, idLeg2, "Message.Metadata must be excluded from semantic identity")
}

func TestMessageIdentity_RoleAndOrderingSensitivity(t *testing.T) {
	t.Parallel()

	// Role difference
	userMsg := aipapi.Message{Role: aipapi.RoleUser, Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: "ping"}}}
	sysMsg := aipapi.Message{Role: aipapi.RoleSystem, Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: "ping"}}}
	asstMsg := aipapi.Message{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: "ping"}}}

	uID, err := conversationprojection.MessageIdentityOf(userMsg)
	require.NoError(t, err)
	sID, err := conversationprojection.MessageIdentityOf(sysMsg)
	require.NoError(t, err)
	aID, err := conversationprojection.MessageIdentityOf(asstMsg)
	require.NoError(t, err)

	assert.NotEqual(t, uID, sID, "different roles must produce different identities")
	assert.NotEqual(t, uID, aID, "different roles must produce different identities")
	assert.NotEqual(t, sID, aID, "different roles must produce different identities")

	// Part ordering sensitivity
	order1 := aipapi.Message{
		Role: aipapi.RoleUser,
		Parts: []aipapi.Part{
			{Kind: aipapi.PartText, Text: "Part 1"},
			{Kind: aipapi.PartText, Text: "Part 2"},
		},
	}
	order2 := aipapi.Message{
		Role: aipapi.RoleUser,
		Parts: []aipapi.Part{
			{Kind: aipapi.PartText, Text: "Part 2"},
			{Kind: aipapi.PartText, Text: "Part 1"},
		},
	}

	oID1, err := conversationprojection.MessageIdentityOf(order1)
	require.NoError(t, err)
	oID2, err := conversationprojection.MessageIdentityOf(order2)
	require.NoError(t, err)

	assert.NotEqual(t, oID1, oID2, "part ordering difference must produce different identities")
}

func TestMessageIdentity_NegativesAndRejection(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		item      aipapi.Item
		wantErrIs error
	}{
		{
			name: "tool call item rejected",
			item: aipapi.Item{
				Kind: aipapi.ItemKindToolCall,
				ToolCall: &aipapi.ToolCallItem{
					CallID:    "call_1",
					Name:      "test_tool",
					Arguments: json.RawMessage(`{}`),
				},
			},
			wantErrIs: conversationprojection.ErrNonMessageItem,
		},
		{
			name: "tool result item rejected",
			item: aipapi.Item{
				Kind: aipapi.ItemKindToolResult,
				ToolResult: &aipapi.ToolResultItem{
					CallID: "call_1",
					Name:   "test_tool",
					Output: "result",
				},
			},
			wantErrIs: conversationprojection.ErrNonMessageItem,
		},
		{
			name: "reasoning item rejected",
			item: aipapi.Item{
				Kind: aipapi.ItemKindReasoning,
				Reasoning: &aipapi.ReasoningItem{
					Reasoning: &aipapi.ReasoningPart{
						Dialect: "standard",
						Text:    "reasoning",
					},
				},
			},
			wantErrIs: conversationprojection.ErrNonMessageItem,
		},
		{
			name: "compaction item rejected",
			item: aipapi.Item{
				Kind: aipapi.ItemKindCompaction,
				Compaction: &aipapi.CompactionItem{
					EncapsulatedID: "comp-1",
				},
			},
			wantErrIs: conversationprojection.ErrNonMessageItem,
		},
		{
			name: "extension item rejected",
			item: aipapi.Item{
				Kind: aipapi.ItemKindExtension,
				Extension: &aipapi.OpaqueExtension{
					Namespace: "acme",
					Type:      "acme:ext",
				},
			},
			wantErrIs: conversationprojection.ErrNonMessageItem,
		},
		{
			name: "item reference rejected",
			item: aipapi.Item{
				Kind:      aipapi.ItemKindItemReference,
				Reference: &aipapi.ItemReference{ID: "msg-1"},
			},
			wantErrIs: conversationprojection.ErrNonMessageItem,
		},
		{
			name: "empty item kind rejected",
			item: aipapi.Item{
				Kind: "",
			},
			wantErrIs: conversationprojection.ErrNonMessageItem,
		},
		{
			name: "message item with empty content rejected",
			item: aipapi.Item{
				Kind:    aipapi.ItemKindMessage,
				Role:    aipapi.RoleUser,
				Content: []aipapi.ContentPart{},
			},
			wantErrIs: conversationprojection.ErrEmptyMessage,
		},
		{
			name: "message item with missing role rejected",
			item: aipapi.Item{
				Kind: aipapi.ItemKindMessage,
				Role: "",
				Content: []aipapi.ContentPart{
					{Kind: aipapi.ContentPartText, Text: "hello"},
				},
			},
			wantErrIs: conversationprojection.ErrInvalidRole,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := conversationprojection.ItemIdentityOf(tc.item)
			require.Error(t, err)
			if tc.wantErrIs != nil {
				assert.ErrorIs(t, err, tc.wantErrIs)
			}
		})
	}

	// Legacy empty message rejected
	_, err := conversationprojection.MessageIdentityOf(aipapi.Message{Role: aipapi.RoleUser, Parts: nil})
	require.ErrorIs(t, err, conversationprojection.ErrEmptyMessage)

	// Legacy empty role rejected
	_, err = conversationprojection.MessageIdentityOf(aipapi.Message{Role: "", Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: "hi"}}})
	require.ErrorIs(t, err, conversationprojection.ErrInvalidRole)
}

func TestMessageIdentity_NoPlaintextLeakedInErrorsOrString(t *testing.T) {
	t.Parallel()

	secretText := "SUPER_SECRET_PLAINTEXT_TOKEN_XYZ123"
	msg := aipapi.Message{
		Role: aipapi.RoleUser,
		Parts: []aipapi.Part{
			{Kind: aipapi.PartText, Text: secretText},
		},
	}

	id, err := conversationprojection.MessageIdentityOf(msg)
	require.NoError(t, err)

	idStr := id.String()
	assert.False(t, strings.Contains(idStr, secretText), "MessageIdentity.String() must not contain plaintext")
	assert.True(t, strings.HasPrefix(idStr, "v1:"), "MessageIdentity.String() must start with v1:")
}

func TestMessageIdentity_UnknownPartKindRejected(t *testing.T) {
	t.Parallel()
	// Unknown kind with non-empty Text must now be rejected explicitly, not coerced to text.
	secret := "SUPER_SECRET_PLAINTEXT_SHOULD_NOT_LEAK_12345"
	bogusKind := aipapi.PartKind("bogus_unknown_kind")
	msg := aipapi.Message{
		Role: aipapi.RoleUser,
		Parts: []aipapi.Part{
			{Kind: bogusKind, Text: secret},
		},
	}
	_, err := conversationprojection.MessageIdentityOf(msg)
	require.Error(t, err)
	// Error must contain bounded kind, not plaintext.
	assert.Contains(t, err.Error(), string(bogusKind), "error must contain bounded kind")
	assert.False(t, strings.Contains(err.Error(), secret), "error must not leak plaintext")
	// Also via AtomOfMessage directly.
	_, err2 := conversationprojection.AtomOfMessage(msg)
	require.Error(t, err2)
	assert.Contains(t, err2.Error(), string(bogusKind))
	assert.False(t, strings.Contains(err2.Error(), secret))

	// Unknown kind with empty Text also rejects with same shape.
	msgEmpty := aipapi.Message{
		Role: aipapi.RoleUser,
		Parts: []aipapi.Part{
			{Kind: aipapi.PartKind("another_bogus"), Text: ""},
		},
	}
	_, err3 := conversationprojection.MessageIdentityOf(msgEmpty)
	require.Error(t, err3)
	assert.Contains(t, err3.Error(), "another_bogus")
	assert.False(t, strings.Contains(err3.Error(), secret))

	// Valid kinds remain unchanged – control that we didn't break semantic identity format.
	validMsg := aipapi.Message{
		Role: aipapi.RoleUser,
		Parts: []aipapi.Part{
			{Kind: aipapi.PartText, Text: "hello valid"},
		},
	}
	id, err := conversationprojection.MessageIdentityOf(validMsg)
	require.NoError(t, err)
	assert.True(t, id.IsValid(), "valid part kind must still produce valid identity")
	// Same for item authority with valid content part.
	item := aipapi.Item{
		Kind:    aipapi.ItemKindMessage,
		Role:    aipapi.RoleUser,
		Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "hello valid"}},
	}
	itemID, err := conversationprojection.ItemIdentityOf(item)
	require.NoError(t, err)
	assert.Equal(t, id, itemID, "legacy and item valid text identities must remain equivalent")
}
