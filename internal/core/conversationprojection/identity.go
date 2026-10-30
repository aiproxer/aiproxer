package conversationprojection

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// IdentityVersion1 is the pinned prefix for version 1 message identities.
const IdentityVersion1 = "v1"

// MessageIdentity is the versioned replay-stable semantic digest of a complete message.
// Format: "v1:<64-char lowercase hex sha256>".
type MessageIdentity string

func (id MessageIdentity) String() string {
	return string(id)
}

// Version returns the version prefix of this identity (e.g. "v1").
func (id MessageIdentity) Version() string {
	version, _, found := strings.Cut(string(id), ":")
	if !found {
		return ""
	}
	return version
}

// Digest returns the lowercase hex hash portion of this identity.
func (id MessageIdentity) Digest() string {
	_, digest, found := strings.Cut(string(id), ":")
	if !found {
		return ""
	}
	return digest
}

// Validate checks that id conforms to "v1:<64-char lowercase hex sha256>".
func (id MessageIdentity) Validate() error {
	s := string(id)
	if s == "" {
		return fmt.Errorf("%w: empty identity string", ErrInvalidMessageIdentity)
	}
	parts := strings.Split(s, ":")
	if len(parts) != 2 {
		return fmt.Errorf("%w: expected <version>:<digest>", ErrInvalidMessageIdentity)
	}
	if parts[0] != IdentityVersion1 {
		return fmt.Errorf("%w: unsupported version %q", ErrInvalidMessageIdentity, parts[0])
	}
	digest := parts[1]
	if len(digest) != 64 {
		return fmt.Errorf("%w: digest length %d (want 64)", ErrInvalidMessageIdentity, len(digest))
	}
	for _, c := range digest {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return fmt.Errorf("%w: invalid lowercase hex character in digest", ErrInvalidMessageIdentity)
		}
	}
	return nil
}

// IsValid reports whether id is a valid v1 message identity.
func (id MessageIdentity) IsValid() bool {
	return id.Validate() == nil
}

// ParseMessageIdentity parses and validates a raw identity string.
func ParseMessageIdentity(raw string) (MessageIdentity, error) {
	id := MessageIdentity(strings.TrimSpace(raw))
	if err := id.Validate(); err != nil {
		return "", err
	}
	return id, nil
}

// ContentKind classifies normalized content parts for semantic identity calculation.
type ContentKind string

const (
	ContentKindText         ContentKind = "text"
	ContentKindImageRef     ContentKind = "image_ref"
	ContentKindFileRef      ContentKind = "file_ref"
	ContentKindVideoRef     ContentKind = "video_ref"
	ContentKindRefusal      ContentKind = "refusal"
	ContentKindReasoning    ContentKind = "reasoning"
	ContentKindSummary      ContentKind = "summary"
	ContentKindAnnotation   ContentKind = "annotation"
	ContentKindAssistantRef ContentKind = "assistant_ref"
	ContentKindJSON         ContentKind = "json"
	ContentKindToolResult   ContentKind = "tool_result"
	ContentKindExtension    ContentKind = "extension"
)

// NormalizedAnnotation holds deterministic annotation data.
type NormalizedAnnotation struct {
	Type string          `json:"type,omitempty"`
	Data json.RawMessage `json:"data,omitempty"`
}

// NormalizedExtension carries vendor-prefixed custom content parts.
type NormalizedExtension struct {
	Namespace   string          `json:"namespace,omitempty"`
	Type        string          `json:"type"`
	Implementor string          `json:"implementor,omitempty"`
	Data        json.RawMessage `json:"data,omitempty"`
}

// NormalizedReasoning holds normalized reasoning metadata and payload.
type NormalizedReasoning struct {
	Dialect                 string          `json:"dialect,omitempty"`
	Text                    string          `json:"text,omitempty"`
	Signature               string          `json:"signature,omitempty"`
	Opaque                  json.RawMessage `json:"opaque,omitempty"`
	Summary                 json.RawMessage `json:"summary,omitempty"`
	Content                 json.RawMessage `json:"content,omitempty"`
	EncryptedContent        json.RawMessage `json:"encrypted_content,omitempty"`
	EncryptedContentPresent bool            `json:"encrypted_content_present,omitempty"`
}

// NormalizedContent is one ordered semantic content fragment in a message atom.
type NormalizedContent struct {
	Kind ContentKind `json:"kind"`

	Text string `json:"text,omitempty"`

	ImageRef  string `json:"image_ref,omitempty"`
	ImageMIME string `json:"image_mime,omitempty"`

	FileRef  string `json:"file_ref,omitempty"`
	FileData string `json:"file_data,omitempty"`
	FileMIME string `json:"file_mime,omitempty"`
	FileName string `json:"file_name,omitempty"`

	VideoRef  string `json:"video_ref,omitempty"`
	VideoMIME string `json:"video_mime,omitempty"`

	Refusal string `json:"refusal,omitempty"`

	Reasoning *NormalizedReasoning `json:"reasoning,omitempty"`

	Summary string `json:"summary,omitempty"`

	Annotation *NormalizedAnnotation `json:"annotation,omitempty"`

	AssistantRef string `json:"assistant_ref,omitempty"`

	JSON json.RawMessage `json:"json,omitempty"`

	Extension *NormalizedExtension `json:"extension,omitempty"`
}

// MessageAtomV1 is the internal representation-neutral projection of a complete message.
type MessageAtomV1 struct {
	Role    aipapi.Role         `json:"role"`
	Content []NormalizedContent `json:"content"`
}

// MessageIdentityOf computes the versioned semantic identity for a legacy message.
func MessageIdentityOf(msg aipapi.Message) (MessageIdentity, error) {
	atom, err := AtomOfMessage(msg)
	if err != nil {
		return "", err
	}
	return HashAtom(atom)
}

// ItemIdentityOf computes the versioned semantic identity for an item.
func ItemIdentityOf(item aipapi.Item) (MessageIdentity, error) {
	atom, err := AtomOfItem(item)
	if err != nil {
		return "", err
	}
	return HashAtom(atom)
}

// AtomOfMessage projects a legacy message to its normalized MessageAtomV1.
func AtomOfMessage(msg aipapi.Message) (MessageAtomV1, error) {
	role := aipapi.Role(strings.TrimSpace(string(msg.Role)))
	if role == "" {
		return MessageAtomV1{}, ErrInvalidRole
	}
	switch role {
	case aipapi.RoleSystem, aipapi.RoleDeveloper, aipapi.RoleUser, aipapi.RoleAssistant, aipapi.RoleTool:
	default:
		return MessageAtomV1{}, fmt.Errorf("%w: %q", ErrInvalidRole, role)
	}

	if len(msg.Parts) == 0 {
		return MessageAtomV1{}, ErrEmptyMessage
	}

	contents := make([]NormalizedContent, 0, len(msg.Parts))
	for _, p := range msg.Parts {
		norm, err := partToNormalized(p)
		if err != nil {
			return MessageAtomV1{}, err
		}
		contents = append(contents, norm)
	}

	return MessageAtomV1{
		Role:    role,
		Content: contents,
	}, nil
}

// AtomOfItem projects a message item to its normalized MessageAtomV1.
func AtomOfItem(item aipapi.Item) (MessageAtomV1, error) {
	if item.Kind != aipapi.ItemKindMessage {
		return MessageAtomV1{}, fmt.Errorf("%w: item kind is %q", ErrNonMessageItem, item.Kind)
	}

	if item.Reference != nil || item.ToolCall != nil || item.ToolResult != nil ||
		item.Reasoning != nil || item.Compaction != nil || item.Extension != nil {
		return MessageAtomV1{}, fmt.Errorf("%w: message item contains non-message variant fields", ErrNonMessageItem)
	}

	role := aipapi.Role(strings.TrimSpace(string(item.Role)))
	if role == "" {
		return MessageAtomV1{}, ErrInvalidRole
	}
	switch role {
	case aipapi.RoleSystem, aipapi.RoleDeveloper, aipapi.RoleUser, aipapi.RoleAssistant, aipapi.RoleTool:
	default:
		return MessageAtomV1{}, fmt.Errorf("%w: %q", ErrInvalidRole, role)
	}

	if len(item.Content) == 0 {
		return MessageAtomV1{}, ErrEmptyMessage
	}

	contents := make([]NormalizedContent, 0, len(item.Content))
	for _, cp := range item.Content {
		norm, err := contentPartToNormalized(cp)
		if err != nil {
			return MessageAtomV1{}, err
		}
		contents = append(contents, norm)
	}

	return MessageAtomV1{
		Role:    role,
		Content: contents,
	}, nil
}

// HashAtom returns the versioned MessageIdentity for atom.
func HashAtom(atom MessageAtomV1) (MessageIdentity, error) {
	b, err := CanonicalAtomBytes(atom)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return MessageIdentity(IdentityVersion1 + ":" + hex.EncodeToString(sum[:])), nil
}

// CanonicalAtomBytes returns the deterministic JSON bytes for atom.
func CanonicalAtomBytes(atom MessageAtomV1) ([]byte, error) {
	return json.Marshal(atom)
}

// CanonicalJSON recursively parses raw JSON and re-encodes it with sorted map keys and no insignificant whitespace.
func CanonicalJSON(raw []byte) ([]byte, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var v any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("canonical json decode error: %w", err)
	}
	return json.Marshal(v)
}

func normalizeLineEndings(s string) string {
	if !strings.ContainsRune(s, '\r') {
		return s
	}
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "\n")
}

func normalizeReasoning(rp *aipapi.ReasoningPart) (*NormalizedReasoning, error) {
	if rp == nil {
		return nil, nil
	}
	nr := &NormalizedReasoning{
		Dialect:                 string(aipapi.NormalizeReasoningDialect(rp.Dialect)),
		Text:                    normalizeLineEndings(rp.Text),
		Signature:               rp.Signature,
		EncryptedContentPresent: rp.EncryptedContentPresent,
	}
	if len(rp.Opaque) > 0 {
		canon, err := CanonicalJSON(rp.Opaque)
		if err != nil {
			return nil, err
		}
		nr.Opaque = canon
	}
	if len(rp.Summary) > 0 {
		canon, err := CanonicalJSON(rp.Summary)
		if err != nil {
			return nil, err
		}
		nr.Summary = canon
	}
	if len(rp.Content) > 0 {
		canon, err := CanonicalJSON(rp.Content)
		if err != nil {
			return nil, err
		}
		nr.Content = canon
	}
	if len(rp.EncryptedContent) > 0 {
		canon, err := CanonicalJSON(rp.EncryptedContent)
		if err != nil {
			return nil, err
		}
		nr.EncryptedContent = canon
	}
	return nr, nil
}

func normalizeAnnotation(ap *aipapi.AnnotationPart) (*NormalizedAnnotation, error) {
	if ap == nil {
		return nil, nil
	}
	na := &NormalizedAnnotation{
		Type: ap.Type,
	}
	if len(ap.Data) > 0 {
		canon, err := CanonicalJSON(ap.Data)
		if err != nil {
			return nil, err
		}
		na.Data = canon
	}
	return na, nil
}

func normalizeExtension(ep *aipapi.ExtensionContentPart) (*NormalizedExtension, error) {
	if ep == nil {
		return nil, nil
	}
	ne := &NormalizedExtension{
		Namespace:   ep.Namespace,
		Type:        ep.Type,
		Implementor: ep.Implementor,
	}
	if len(ep.Data) > 0 {
		canon, err := CanonicalJSON(ep.Data)
		if err != nil {
			return nil, err
		}
		ne.Data = canon
	}
	return ne, nil
}

func partToNormalized(p aipapi.Part) (NormalizedContent, error) {
	switch p.Kind {
	case aipapi.PartText:
		return NormalizedContent{
			Kind: ContentKindText,
			Text: normalizeLineEndings(p.Text),
		}, nil
	case aipapi.PartImageRef:
		var ann *NormalizedAnnotation
		if len(p.Content) > 0 {
			canon, err := CanonicalJSON(p.Content)
			if err != nil {
				return NormalizedContent{}, err
			}
			ann = &NormalizedAnnotation{Type: "image_detail", Data: canon}
		}
		return NormalizedContent{
			Kind:       ContentKindImageRef,
			ImageRef:   p.ImageRef,
			ImageMIME:  p.ImageMIME,
			Annotation: ann,
		}, nil
	case aipapi.PartFileRef:
		return NormalizedContent{
			Kind:     ContentKindFileRef,
			FileRef:  p.FileRef,
			FileMIME: p.FileMIME,
			FileName: p.FileName,
		}, nil
	case aipapi.PartToolResult:
		return NormalizedContent{
			Kind: ContentKindToolResult,
			Text: normalizeLineEndings(p.Text),
		}, nil
	case aipapi.PartJSON:
		canon, err := CanonicalJSON(p.Content)
		if err != nil {
			return NormalizedContent{}, err
		}
		ann := &NormalizedAnnotation{Type: "json_content", Data: canon}
		return NormalizedContent{
			Kind:       ContentKindJSON,
			Text:       string(canon),
			Annotation: ann,
			JSON:       canon,
		}, nil
	case aipapi.PartReasoning:
		nr, err := normalizeReasoning(p.Reasoning)
		if err != nil {
			return NormalizedContent{}, err
		}
		return NormalizedContent{
			Kind:      ContentKindReasoning,
			Reasoning: nr,
		}, nil
	default:
		return NormalizedContent{}, fmt.Errorf("unsupported part kind: %q", p.Kind)
	}
}

func contentPartToNormalized(cp aipapi.ContentPart) (NormalizedContent, error) {
	switch cp.Kind {
	case aipapi.ContentPartText:
		return NormalizedContent{
			Kind: ContentKindText,
			Text: normalizeLineEndings(cp.Text),
		}, nil
	case aipapi.ContentPartImageRef:
		ann, err := normalizeAnnotation(cp.Annotation)
		if err != nil {
			return NormalizedContent{}, err
		}
		return NormalizedContent{
			Kind:       ContentKindImageRef,
			ImageRef:   cp.ImageRef,
			ImageMIME:  cp.ImageMIME,
			Annotation: ann,
		}, nil
	case aipapi.ContentPartFileRef:
		return NormalizedContent{
			Kind:     ContentKindFileRef,
			FileRef:  cp.FileRef,
			FileData: cp.FileData,
			FileMIME: cp.FileMIME,
			FileName: cp.FileName,
		}, nil
	case aipapi.ContentPartVideoRef:
		return NormalizedContent{
			Kind:      ContentKindVideoRef,
			VideoRef:  cp.VideoRef,
			VideoMIME: cp.VideoMIME,
		}, nil
	case aipapi.ContentPartRefusal:
		return NormalizedContent{
			Kind:    ContentKindRefusal,
			Refusal: normalizeLineEndings(cp.Refusal),
		}, nil
	case aipapi.ContentPartReasoning:
		nr, err := normalizeReasoning(cp.Reasoning)
		if err != nil {
			return NormalizedContent{}, err
		}
		return NormalizedContent{
			Kind:      ContentKindReasoning,
			Reasoning: nr,
		}, nil
	case aipapi.ContentPartSummary:
		return NormalizedContent{
			Kind:    ContentKindSummary,
			Summary: normalizeLineEndings(cp.Summary),
		}, nil
	case aipapi.ContentPartAnnotation:
		ann, err := normalizeAnnotation(cp.Annotation)
		if err != nil {
			return NormalizedContent{}, err
		}
		return NormalizedContent{
			Kind:       ContentKindAnnotation,
			Annotation: ann,
		}, nil
	case aipapi.ContentPartAssistantRef:
		return NormalizedContent{
			Kind:         ContentKindAssistantRef,
			AssistantRef: cp.AssistantRef,
		}, nil
	case aipapi.ContentPartJSON:
		canon, err := CanonicalJSON([]byte(cp.Text))
		if err != nil {
			return NormalizedContent{}, err
		}
		ann, err := normalizeAnnotation(cp.Annotation)
		if err != nil {
			return NormalizedContent{}, err
		}
		return NormalizedContent{
			Kind:       ContentKindJSON,
			Text:       string(canon),
			Annotation: ann,
			JSON:       canon,
		}, nil
	case aipapi.ContentPartToolResult:
		return NormalizedContent{
			Kind: ContentKindToolResult,
			Text: normalizeLineEndings(cp.Text),
		}, nil
	case aipapi.ContentPartExtension:
		ext, err := normalizeExtension(cp.Extension)
		if err != nil {
			return NormalizedContent{}, err
		}
		return NormalizedContent{
			Kind:      ContentKindExtension,
			Extension: ext,
		}, nil
	default:
		return NormalizedContent{}, fmt.Errorf("unsupported content part kind: %q", cp.Kind)
	}
}
