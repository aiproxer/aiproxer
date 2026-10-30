package aipapi

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"
)

// ItemKind classifies ordered canonical item types.
type ItemKind string

const (
	ItemKindMessage       ItemKind = "message"
	ItemKindItemReference ItemKind = "item_reference"
	ItemKindToolCall      ItemKind = "tool_call"
	ItemKindToolResult    ItemKind = "tool_result"
	ItemKindReasoning     ItemKind = "reasoning"
	ItemKindCompaction    ItemKind = "compaction"
	ItemKindExtension     ItemKind = "extension"
)

// ItemStatus identifies the lifecycle status of an item.
type ItemStatus string

const (
	ItemStatusInProgress ItemStatus = "in_progress"
	ItemStatusCompleted  ItemStatus = "completed"
	ItemStatusIncomplete ItemStatus = "incomplete"
)

// AssistantPhase identifies assistant output phase (commentary vs final answer).
type AssistantPhase string

const (
	AssistantPhaseCommentary  AssistantPhase = "commentary"
	AssistantPhaseFinalAnswer AssistantPhase = "final_answer"
)

// ContentPartKind classifies canonical content part forms inside items.
type ContentPartKind string

const (
	ContentPartText         ContentPartKind = "text"
	ContentPartImageRef     ContentPartKind = "image_ref"
	ContentPartFileRef      ContentPartKind = "file_ref"
	ContentPartVideoRef     ContentPartKind = "video_ref"
	ContentPartRefusal      ContentPartKind = "refusal"
	ContentPartReasoning    ContentPartKind = "reasoning"
	ContentPartSummary      ContentPartKind = "summary"
	ContentPartAnnotation   ContentPartKind = "annotation"
	ContentPartAssistantRef ContentPartKind = "assistant_ref"
	ContentPartJSON         ContentPartKind = "json"
	ContentPartToolResult   ContentPartKind = "tool_result"
	// ContentPartExtension is an opaque vendor-prefixed custom content part that
	// the canonical model cannot interpret but must preserve losslessly. Its
	// structured payload is carried as raw JSON (never stringified to text).
	ContentPartExtension ContentPartKind = "extension"
)

// AnnotationPart holds annotation metadata for text/content.
type AnnotationPart struct {
	Type string          `json:"type,omitempty"`
	Data json.RawMessage `json:"data,omitempty"`
}

// ExtensionContentPart carries a vendor-prefixed custom content part that the
// canonical model cannot interpret but must preserve losslessly. Type is the
// prefixed wire discriminator (for example "acme:input_file" or
// "acme.com/part"); Data is the full raw wire part object (including its type
// field) so encoding can emit it verbatim without stringifying the structured
// payload.
//
// Namespace and Implementor are the exact ExtensionRequirement identity of the
// part. When Namespace is empty it is derived deterministically from the
// prefixed Type (the leading segment before the first ':' or '/'), matching the
// operator dialect declarations used by exact admission. Namespace and
// Implementor are canonical metadata; the wire object in Data remains the
// authoritative lossless carrier, so an explicit namespace that diverges from
// the deterministic derivation is merged into the emitted wire object rather
// than silently dropped.
type ExtensionContentPart struct {
	Namespace   string          `json:"namespace,omitempty"`
	Type        string          `json:"type"`
	Implementor string          `json:"implementor,omitempty"`
	Data        json.RawMessage `json:"data,omitempty"`
}

func (e *ExtensionContentPart) validate(field string) error {
	if e == nil {
		return &ValidationError{Field: field, Message: "extension content part is nil"}
	}
	if ns := strings.TrimSpace(e.Namespace); ns != "" {
		if strings.ContainsAny(ns, " \t\r\n") {
			return &ValidationError{Field: field + ".Namespace", Message: "extension namespace must not contain whitespace"}
		}
		if err := validateStringField(field+".Namespace", e.Namespace, MaxExtensionNamespaceBytes); err != nil {
			return err
		}
	}
	if err := validateStringField(field+".Implementor", e.Implementor, MaxExtensionImplementorBytes); err != nil {
		return err
	}
	if strings.TrimSpace(e.Type) == "" {
		return &ValidationError{Field: field + ".Type", Message: "extension type is required"}
	}
	if !strings.ContainsAny(e.Type, ":/") {
		return &ValidationError{Field: field + ".Type", Message: "extension type must be vendor-prefixed (contains ':' or '/')"}
	}
	if e.Type != strings.TrimSpace(e.Type) {
		return &ValidationError{Field: field + ".Type", Message: "extension type must not contain leading or trailing whitespace"}
	}
	if err := validateStringField(field+".Type", e.Type, MaxExtensionTypeBytes); err != nil {
		return err
	}
	if len(e.Data) > MaxExtensionDataBytes {
		return &ValidationError{Field: field + ".Data", Message: fmt.Sprintf("extension data exceeds %d bytes", MaxExtensionDataBytes)}
	}
	if len(e.Data) == 0 {
		return &ValidationError{Field: field + ".Data", Message: "extension content part requires Data"}
	}
	if !json.Valid(e.Data) {
		return &ValidationError{Field: field + ".Data", Message: "extension data must be valid JSON"}
	}
	if err := validateJSONDepth(e.Data, MaxJSONDepth); err != nil {
		return &ValidationError{Field: field + ".Data", Message: err.Error()}
	}
	var obj struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(e.Data, &obj); err != nil {
		return &ValidationError{Field: field + ".Data", Message: "extension data must be a JSON object"}
	}
	if obj.Type != e.Type {
		return &ValidationError{Field: field + ".Data", Message: "extension data type field must match Type"}
	}
	return nil
}

// ContentPart is one ordered content fragment within a canonical item.
type ContentPart struct {
	Kind ContentPartKind `json:"kind"`

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

	Reasoning *ReasoningPart `json:"reasoning,omitempty"`

	Summary string `json:"summary,omitempty"`

	Annotation *AnnotationPart `json:"annotation,omitempty"`

	AssistantRef string `json:"assistant_ref,omitempty"`

	// Extension carries a vendor-prefixed custom content part preserved
	// opaquely when Kind is ContentPartExtension.
	Extension *ExtensionContentPart `json:"extension,omitempty"`
}

// itemFieldPath names a canonical item field for diagnostics. Envelope
// validation visits every item, content part, and tool-result part of an agent
// history, so the label is rendered only from an error path: a valid request
// must not allocate one field string per check. It mirrors callElementPath in
// limits.go.
type itemFieldPath struct {
	container string // "Items" for a call envelope, "Item" for a standalone event item
	index     int    // element index, or -1 when the path names a lone item
	nested    string // ".Content" or ".ToolResult.Parts"; empty for item-level fields
	partIndex int
	suffix    string
}

// itemField names Items[index] for diagnostics.
func itemField(index int) itemFieldPath {
	return itemFieldPath{container: "Items", index: index}
}

// eventItemField names the standalone Item carried by an event envelope.
func eventItemField() itemFieldPath {
	return itemFieldPath{container: "Item", index: -1}
}

// withSuffix returns a copy of the path carrying a different trailing field.
func (p itemFieldPath) withSuffix(suffix string) itemFieldPath {
	p.suffix = suffix
	return p
}

// part returns a copy of the path naming an element of a nested part list.
func (p itemFieldPath) part(nested string, index int) itemFieldPath {
	p.nested = nested
	p.partIndex = index
	p.suffix = ""
	return p
}

// String renders the full field path. It runs only when a limit is violated.
func (p itemFieldPath) String() string {
	path := p.container
	if p.index >= 0 {
		path += "[" + strconv.Itoa(p.index) + "]"
	}
	if p.nested != "" {
		path += p.nested + "[" + strconv.Itoa(p.partIndex) + "]"
	}
	return path + p.suffix
}

// validateStringFieldAt is validateStringField for an item field path whose
// label is rendered only when the field exceeds its limit. The guard is the
// exact negation of validateStringField's own check, so the reported error is
// identical to one built from an eagerly rendered label.
func validateStringFieldAt(path itemFieldPath, suffix, s string, maxLen int) error {
	if maxLen <= 0 || len(s) <= maxLen {
		return nil
	}
	return validateStringField(path.withSuffix(suffix).String(), s, maxLen)
}

// validateExactStringFieldAt is validateExactStringField for an item field path
// whose label is rendered only when validation fails.
func validateExactStringFieldAt(path itemFieldPath, suffix, s string, maxLen int) error {
	if (maxLen <= 0 || len(s) <= maxLen) && (s == "" || strings.TrimSpace(s) == s) {
		return nil
	}
	return validateExactStringField(path.withSuffix(suffix).String(), s, maxLen)
}

func (cp ContentPart) validate(path itemFieldPath) error {
	switch cp.Kind {
	case ContentPartText:
		if cp.Text == "" {
			return &ValidationError{Field: path.withSuffix(".Text").String(), Message: "text part requires non-empty Text"}
		}
		if len(cp.Text) > MaxPartTextBytes {
			return &ValidationError{Field: path.withSuffix(".Text").String(), Message: fmt.Sprintf("text exceeds %d bytes", MaxPartTextBytes)}
		}
	case ContentPartImageRef:
		if cp.ImageRef == "" {
			return &ValidationError{Field: path.withSuffix(".ImageRef").String(), Message: "image_ref part requires ImageRef"}
		}
		if err := validateStringFieldAt(path, ".ImageRef", cp.ImageRef, MaxRefStringBytes); err != nil {
			return err
		}
		if err := validateStringFieldAt(path, ".ImageMIME", cp.ImageMIME, MaxRefStringBytes); err != nil {
			return err
		}
	case ContentPartFileRef:
		if cp.FileRef == "" && cp.FileData == "" {
			return &ValidationError{Field: path.withSuffix(".FileRef").String(), Message: "file_ref part requires FileRef or FileData"}
		}
		if err := validateStringFieldAt(path, ".FileRef", cp.FileRef, MaxRefStringBytes); err != nil {
			return err
		}
		if len(cp.FileData) > MaxFileDataBytes {
			return &ValidationError{Field: path.withSuffix(".FileData").String(), Message: fmt.Sprintf("file_data exceeds %d bytes", MaxFileDataBytes)}
		}
		if err := validateStringFieldAt(path, ".FileMIME", cp.FileMIME, MaxRefStringBytes); err != nil {
			return err
		}
		if err := validateStringFieldAt(path, ".FileName", cp.FileName, MaxRefStringBytes); err != nil {
			return err
		}
	case ContentPartVideoRef:
		if cp.VideoRef == "" {
			return &ValidationError{Field: path.withSuffix(".VideoRef").String(), Message: "video_ref part requires VideoRef"}
		}
		if err := validateStringFieldAt(path, ".VideoRef", cp.VideoRef, MaxRefStringBytes); err != nil {
			return err
		}
		if err := validateStringFieldAt(path, ".VideoMIME", cp.VideoMIME, MaxRefStringBytes); err != nil {
			return err
		}
	case ContentPartRefusal:
		if cp.Refusal == "" {
			return &ValidationError{Field: path.withSuffix(".Refusal").String(), Message: "refusal part requires non-empty Refusal"}
		}
		if len(cp.Refusal) > MaxPartTextBytes {
			return &ValidationError{Field: path.withSuffix(".Refusal").String(), Message: fmt.Sprintf("refusal exceeds %d bytes", MaxPartTextBytes)}
		}
	case ContentPartReasoning:
		if cp.Reasoning == nil {
			return &ValidationError{Field: path.withSuffix(".Reasoning").String(), Message: "reasoning part requires Reasoning"}
		}
		if err := validateReasoningPart(cp.Reasoning); err != nil {
			return &ValidationError{Field: path.withSuffix(".Reasoning").String(), Message: err.Error()}
		}
	case ContentPartSummary:
		if cp.Summary == "" {
			return &ValidationError{Field: path.withSuffix(".Summary").String(), Message: "summary part requires non-empty Summary"}
		}
		if len(cp.Summary) > MaxPartTextBytes {
			return &ValidationError{Field: path.withSuffix(".Summary").String(), Message: fmt.Sprintf("summary exceeds %d bytes", MaxPartTextBytes)}
		}
	case ContentPartAnnotation:
		if cp.Annotation == nil {
			return &ValidationError{Field: path.withSuffix(".Annotation").String(), Message: "annotation part requires Annotation"}
		}
		if err := validateStringFieldAt(path, ".Annotation.Type", cp.Annotation.Type, MaxRefStringBytes); err != nil {
			return err
		}
		if len(cp.Annotation.Data) > MaxPartJSONBytes {
			return &ValidationError{Field: path.withSuffix(".Annotation.Data").String(), Message: fmt.Sprintf("annotation data exceeds %d bytes", MaxPartJSONBytes)}
		}
		if len(cp.Annotation.Data) > 0 {
			if !json.Valid(cp.Annotation.Data) {
				return &ValidationError{Field: path.withSuffix(".Annotation.Data").String(), Message: "annotation data must be valid JSON"}
			}
			if err := validateJSONDepth(cp.Annotation.Data, MaxJSONDepth); err != nil {
				return &ValidationError{Field: path.withSuffix(".Annotation.Data").String(), Message: err.Error()}
			}
		}
	case ContentPartAssistantRef:
		if cp.AssistantRef == "" {
			return &ValidationError{Field: path.withSuffix(".AssistantRef").String(), Message: "assistant_ref part requires AssistantRef"}
		}
		if err := validateStringFieldAt(path, ".AssistantRef", cp.AssistantRef, MaxRefStringBytes); err != nil {
			return err
		}
	case ContentPartJSON:
		if cp.Text == "" {
			return &ValidationError{Field: path.withSuffix(".Text").String(), Message: "json part requires non-empty Text"}
		}
		if len(cp.Text) > MaxPartJSONBytes {
			return &ValidationError{Field: path.withSuffix(".Text").String(), Message: fmt.Sprintf("json exceeds %d bytes", MaxPartJSONBytes)}
		}
		if !json.Valid([]byte(cp.Text)) {
			return &ValidationError{Field: path.withSuffix(".Text").String(), Message: "json part must contain valid JSON text"}
		}
	case ContentPartToolResult:
		if cp.Text == "" {
			return &ValidationError{Field: path.withSuffix(".Text").String(), Message: "tool_result part requires non-empty Text"}
		}
		if len(cp.Text) > MaxPartTextBytes {
			return &ValidationError{Field: path.withSuffix(".Text").String(), Message: fmt.Sprintf("tool_result exceeds %d bytes", MaxPartTextBytes)}
		}
	case ContentPartExtension:
		if cp.Extension == nil {
			return &ValidationError{Field: path.withSuffix(".Extension").String(), Message: "extension part requires Extension"}
		}
		if err := cp.Extension.validate(path.withSuffix(".Extension").String()); err != nil {
			return err
		}
	case "":
		return &ValidationError{Field: path.withSuffix(".Kind").String(), Message: "content part kind is required"}
	default:
		return &ValidationError{Field: path.withSuffix(".Kind").String(), Message: fmt.Sprintf("unknown content part kind %q", cp.Kind)}
	}
	return nil
}

// ItemReference references a previously produced or existing item by ID.
type ItemReference struct {
	ID string `json:"id"`
}

// ToolCallItem represents a function/tool invocation request in an item trajectory.
type ToolCallItem struct {
	CallID    string          `json:"call_id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

// ToolResultItem represents function output in an item trajectory.
type ToolResultItem struct {
	CallID string        `json:"call_id"`
	Name   string        `json:"name"`
	Output string        `json:"output,omitempty"`
	Parts  []ContentPart `json:"parts,omitempty"`
}

// ReasoningItem encapsulates reasoning payload within an item.
type ReasoningItem struct {
	Reasoning *ReasoningPart `json:"reasoning,omitempty"`
}

// CompactionItem encapsulates a context compaction item within an item trajectory.
type CompactionItem struct {
	EncapsulatedID string `json:"encapsulated_id,omitempty"`
	Dialect        string `json:"dialect,omitempty"`
	Implementor    string `json:"implementor,omitempty"`
	// EncryptedContent carries the provider compaction blob (pinned profile
	// encrypted_content). It is protocol-neutral opaque data that the
	// OpenResponses wire encoder renders verbatim on response.compaction output.
	EncryptedContent string          `json:"encrypted_content,omitempty"`
	Opaque           json.RawMessage `json:"opaque,omitempty"`
}

// OpaqueExtension carries namespaced extensions attached to items or calls.
type OpaqueExtension struct {
	Namespace   string          `json:"namespace"`
	Type        string          `json:"type"`
	Implementor string          `json:"implementor,omitempty"`
	Direction   string          `json:"direction,omitempty"`
	Data        json.RawMessage `json:"data,omitempty"`
}

func (e *OpaqueExtension) validate(field string) error {
	if e == nil {
		return &ValidationError{Field: field, Message: "extension record is nil"}
	}
	ns := strings.TrimSpace(e.Namespace)
	if ns == "" {
		return &ValidationError{Field: field + ".Namespace", Message: "extension namespace is required"}
	}
	if strings.ContainsAny(ns, " \t\r\n") {
		return &ValidationError{Field: field + ".Namespace", Message: "extension namespace must not contain whitespace"}
	}
	if err := validateStringField(field+".Namespace", e.Namespace, MaxExtensionNamespaceBytes); err != nil {
		return err
	}
	if strings.TrimSpace(e.Type) == "" {
		return &ValidationError{Field: field + ".Type", Message: "extension type is required"}
	}
	if err := validateStringField(field+".Type", e.Type, MaxExtensionTypeBytes); err != nil {
		return err
	}
	if err := validateStringField(field+".Implementor", e.Implementor, MaxExtensionImplementorBytes); err != nil {
		return err
	}
	if err := validateStringField(field+".Direction", e.Direction, MaxExtensionDirectionBytes); err != nil {
		return err
	}
	if len(e.Data) > MaxExtensionDataBytes {
		return &ValidationError{Field: field + ".Data", Message: fmt.Sprintf("extension data exceeds %d bytes", MaxExtensionDataBytes)}
	}
	if len(e.Data) > 0 {
		if !json.Valid(e.Data) {
			return &ValidationError{Field: field + ".Data", Message: "extension data must be valid JSON"}
		}
		if err := validateJSONDepth(e.Data, MaxJSONDepth); err != nil {
			return &ValidationError{Field: field + ".Data", Message: err.Error()}
		}
	}
	return nil
}

// Item is one canonical ordered item in a conversation trajectory.
type Item struct {
	Kind       ItemKind         `json:"kind"`
	ID         string           `json:"id,omitempty"`
	Status     ItemStatus       `json:"status,omitempty"`
	Role       Role             `json:"role,omitempty"`
	Phase      AssistantPhase   `json:"phase,omitempty"`
	Content    []ContentPart    `json:"content,omitempty"`
	Reference  *ItemReference   `json:"reference,omitempty"`
	ToolCall   *ToolCallItem    `json:"tool_call,omitempty"`
	ToolResult *ToolResultItem  `json:"tool_result,omitempty"`
	Reasoning  *ReasoningItem   `json:"reasoning,omitempty"`
	Compaction *CompactionItem  `json:"compaction,omitempty"`
	Extension  *OpaqueExtension `json:"extension,omitempty"`
}

// Structural storage sizes for Item PayloadBytes memory accounting, computed
// once so the hot path stays reflection-free. reasoningPartSize lives in
// events.go alongside the other Event-level sizes.
var (
	contentPartSize          = int(reflect.TypeFor[ContentPart]().Size())
	itemReferenceSize        = int(reflect.TypeFor[ItemReference]().Size())
	toolCallItemSize         = int(reflect.TypeFor[ToolCallItem]().Size())
	toolResultItemSize       = int(reflect.TypeFor[ToolResultItem]().Size())
	reasoningItemSize        = int(reflect.TypeFor[ReasoningItem]().Size())
	compactionItemSize       = int(reflect.TypeFor[CompactionItem]().Size())
	opaqueExtensionSize      = int(reflect.TypeFor[OpaqueExtension]().Size())
	annotationPartSize       = int(reflect.TypeFor[AnnotationPart]().Size())
	extensionContentPartSize = int(reflect.TypeFor[ExtensionContentPart]().Size())
)

// PayloadBytes is a bounded conservative estimate of this item's retained
// string and byte fields, without JSON marshaling. The estimate includes
// element backing storage for Content and ToolResult parts and pointed-to
// struct storage for each non-nil nested pointer.
func (item *Item) PayloadBytes() int {
	if item == nil {
		return 0
	}
	n := len(item.ID) + len(item.Content)*contentPartSize
	for i := range item.Content {
		n += item.Content[i].payloadBytes()
	}
	if item.Reference != nil {
		n += itemReferenceSize + len(item.Reference.ID)
	}
	if item.ToolCall != nil {
		n += toolCallItemSize + len(item.ToolCall.CallID) + len(item.ToolCall.Name) + len(item.ToolCall.Arguments)
	}
	if item.ToolResult != nil {
		n += toolResultItemSize + len(item.ToolResult.CallID) + len(item.ToolResult.Name) + len(item.ToolResult.Output) +
			len(item.ToolResult.Parts)*contentPartSize
		for i := range item.ToolResult.Parts {
			n += item.ToolResult.Parts[i].payloadBytes()
		}
	}
	if item.Reasoning != nil {
		n += reasoningItemSize
		if item.Reasoning.Reasoning != nil {
			n += reasoningPartSize + len(item.Reasoning.Reasoning.Dialect) + ReasoningPayloadBytes(item.Reasoning.Reasoning)
		}
	}
	if item.Compaction != nil {
		n += compactionItemSize + len(item.Compaction.EncapsulatedID) + len(item.Compaction.Dialect) +
			len(item.Compaction.Implementor) + len(item.Compaction.EncryptedContent) + len(item.Compaction.Opaque)
	}
	if item.Extension != nil {
		n += opaqueExtensionSize + len(item.Extension.Namespace) + len(item.Extension.Type) + len(item.Extension.Implementor) +
			len(item.Extension.Direction) + len(item.Extension.Data)
	}
	return n
}

func (cp ContentPart) payloadBytes() int {
	n := len(cp.Text) + len(cp.ImageRef) + len(cp.ImageMIME) + len(cp.FileRef) + len(cp.FileData) +
		len(cp.FileMIME) + len(cp.FileName) + len(cp.VideoRef) + len(cp.VideoMIME) + len(cp.Refusal) +
		len(cp.Summary) + len(cp.AssistantRef)
	if cp.Reasoning != nil {
		n += reasoningPartSize + len(cp.Reasoning.Dialect) + ReasoningPayloadBytes(cp.Reasoning)
	}
	if cp.Annotation != nil {
		n += annotationPartSize + len(cp.Annotation.Type) + len(cp.Annotation.Data)
	}
	if cp.Extension != nil {
		n += extensionContentPartSize + len(cp.Extension.Namespace) + len(cp.Extension.Type) + len(cp.Extension.Implementor) + len(cp.Extension.Data)
	}
	return n
}

func (item Item) validate(path itemFieldPath) error {
	if err := validateStringFieldAt(path, ".ID", item.ID, MaxItemReferenceIDBytes); err != nil {
		return err
	}
	if item.Status != "" {
		switch item.Status {
		case ItemStatusInProgress, ItemStatusCompleted, ItemStatusIncomplete:
		default:
			return &ValidationError{Field: path.withSuffix(".Status").String(), Message: fmt.Sprintf("invalid item status %q", item.Status)}
		}
	}
	if item.Phase != "" {
		switch item.Phase {
		case AssistantPhaseCommentary, AssistantPhaseFinalAnswer:
		default:
			return &ValidationError{Field: path.withSuffix(".Phase").String(), Message: fmt.Sprintf("invalid assistant phase %q", item.Phase)}
		}
		if item.Kind != ItemKindMessage || item.Role != RoleAssistant {
			return &ValidationError{Field: path.withSuffix(".Phase").String(), Message: "phase is only allowed on assistant message items"}
		}
	}

	switch item.Kind {
	case ItemKindMessage:
		if item.Reference != nil || item.ToolCall != nil || item.ToolResult != nil || item.Reasoning != nil || item.Compaction != nil || item.Extension != nil {
			return &ValidationError{Field: path.String(), Message: "message item must not contain non-message variant fields"}
		}
		if item.Role == "" {
			return &ValidationError{Field: path.withSuffix(".Role").String(), Message: "role is required for message item"}
		}
		switch item.Role {
		case RoleSystem, RoleDeveloper, RoleUser, RoleAssistant, RoleTool:
		default:
			return &ValidationError{Field: path.withSuffix(".Role").String(), Message: fmt.Sprintf("invalid role %q", item.Role)}
		}
		if len(item.Content) == 0 {
			return &ValidationError{Field: path.withSuffix(".Content").String(), Message: "at least one content part is required for message item"}
		}
		if len(item.Content) > MaxContentPartsPerItem {
			return &ValidationError{Field: path.withSuffix(".Content").String(), Message: fmt.Sprintf("at most %d content parts per item", MaxContentPartsPerItem)}
		}
		for j, cp := range item.Content {
			if err := cp.validate(path.part(".Content", j)); err != nil {
				return err
			}
		}
	case ItemKindItemReference:
		if len(item.Content) > 0 || item.Role != "" || item.ToolCall != nil || item.ToolResult != nil || item.Reasoning != nil || item.Compaction != nil || item.Extension != nil {
			return &ValidationError{Field: path.String(), Message: "item_reference must not contain non-reference variant fields"}
		}
		if item.Reference == nil || strings.TrimSpace(item.Reference.ID) == "" {
			return &ValidationError{Field: path.withSuffix(".Reference.ID").String(), Message: "reference ID is required for item_reference"}
		}
		if err := validateStringFieldAt(path, ".Reference.ID", item.Reference.ID, MaxItemReferenceIDBytes); err != nil {
			return err
		}
	case ItemKindToolCall:
		if len(item.Content) > 0 || item.Role != "" || item.Reference != nil || item.ToolResult != nil || item.Reasoning != nil || item.Compaction != nil || item.Extension != nil {
			return &ValidationError{Field: path.String(), Message: "tool_call item must not contain non-tool_call variant fields"}
		}
		if item.ToolCall == nil {
			return &ValidationError{Field: path.withSuffix(".ToolCall").String(), Message: "tool call data is required for tool_call item"}
		}
		if strings.TrimSpace(item.ToolCall.CallID) == "" {
			return &ValidationError{Field: path.withSuffix(".ToolCall.CallID").String(), Message: "call ID is required"}
		}
		if strings.TrimSpace(item.ToolCall.Name) == "" {
			return &ValidationError{Field: path.withSuffix(".ToolCall.Name").String(), Message: "tool name is required"}
		}
		if err := validateStringFieldAt(path, ".ToolCall.CallID", item.ToolCall.CallID, MaxRefStringBytes); err != nil {
			return err
		}
		if err := validateExactStringFieldAt(path, ".ToolCall.Name", item.ToolCall.Name, MaxToolNameBytes); err != nil {
			return err
		}
		if len(item.ToolCall.Arguments) > MaxToolParametersBytes {
			return &ValidationError{Field: path.withSuffix(".ToolCall.Arguments").String(), Message: fmt.Sprintf("exceeds %d bytes", MaxToolParametersBytes)}
		}
		if len(item.ToolCall.Arguments) > 0 {
			if !json.Valid(item.ToolCall.Arguments) {
				return &ValidationError{Field: path.withSuffix(".ToolCall.Arguments").String(), Message: "arguments must be valid JSON"}
			}
			if err := validateJSONDepth(item.ToolCall.Arguments, MaxJSONDepth); err != nil {
				return &ValidationError{Field: path.withSuffix(".ToolCall.Arguments").String(), Message: err.Error()}
			}
		}
	case ItemKindToolResult:
		if len(item.Content) > 0 || item.Role != "" || item.Reference != nil || item.ToolCall != nil || item.Reasoning != nil || item.Compaction != nil || item.Extension != nil {
			return &ValidationError{Field: path.String(), Message: "tool_result item must not contain non-tool_result variant fields"}
		}
		if item.ToolResult == nil {
			return &ValidationError{Field: path.withSuffix(".ToolResult").String(), Message: "tool result data is required for tool_result item"}
		}
		if strings.TrimSpace(item.ToolResult.CallID) == "" {
			return &ValidationError{Field: path.withSuffix(".ToolResult.CallID").String(), Message: "call ID is required"}
		}
		if err := validateStringFieldAt(path, ".ToolResult.CallID", item.ToolResult.CallID, MaxRefStringBytes); err != nil {
			return err
		}
		if err := validateExactStringFieldAt(path, ".ToolResult.Name", item.ToolResult.Name, MaxToolNameBytes); err != nil {
			return err
		}
		hasOutput := item.ToolResult.Output != ""
		hasParts := len(item.ToolResult.Parts) > 0
		if hasOutput && hasParts {
			return &ValidationError{Field: path.withSuffix(".ToolResult").String(), Message: "tool result must specify output or parts, not both"}
		}
		if !hasOutput && !hasParts {
			return &ValidationError{Field: path.withSuffix(".ToolResult").String(), Message: "tool result must specify output or parts"}
		}
		if len(item.ToolResult.Output) > MaxPartTextBytes {
			return &ValidationError{Field: path.withSuffix(".ToolResult.Output").String(), Message: fmt.Sprintf("output exceeds %d bytes", MaxPartTextBytes)}
		}
		if len(item.ToolResult.Parts) > MaxContentPartsPerItem {
			return &ValidationError{Field: path.withSuffix(".ToolResult.Parts").String(), Message: fmt.Sprintf("at most %d parts per tool result", MaxContentPartsPerItem)}
		}
		for j, cp := range item.ToolResult.Parts {
			if err := cp.validate(path.part(".ToolResult.Parts", j)); err != nil {
				return err
			}
		}
	case ItemKindReasoning:
		if len(item.Content) > 0 || item.Role != "" || item.Reference != nil || item.ToolCall != nil || item.ToolResult != nil || item.Compaction != nil || item.Extension != nil {
			return &ValidationError{Field: path.String(), Message: "reasoning item must not contain non-reasoning variant fields"}
		}
		if item.Reasoning == nil || item.Reasoning.Reasoning == nil {
			return &ValidationError{Field: path.withSuffix(".Reasoning").String(), Message: "reasoning payload is required for reasoning item"}
		}
		if err := validateReasoningPart(item.Reasoning.Reasoning); err != nil {
			return &ValidationError{Field: path.withSuffix(".Reasoning").String(), Message: err.Error()}
		}
	case ItemKindCompaction:
		if len(item.Content) > 0 || item.Role != "" || item.Reference != nil || item.ToolCall != nil || item.ToolResult != nil || item.Reasoning != nil || item.Extension != nil {
			return &ValidationError{Field: path.String(), Message: "compaction item must not contain non-compaction variant fields"}
		}
		if item.Compaction == nil {
			return &ValidationError{Field: path.withSuffix(".Compaction").String(), Message: "compaction data is required for compaction item"}
		}
		if err := validateStringFieldAt(path, ".Compaction.EncapsulatedID", item.Compaction.EncapsulatedID, MaxRefStringBytes); err != nil {
			return err
		}
		if err := validateStringFieldAt(path, ".Compaction.Dialect", item.Compaction.Dialect, MaxCompactionDialectBytes); err != nil {
			return err
		}
		if err := validateStringFieldAt(path, ".Compaction.Implementor", item.Compaction.Implementor, MaxExtensionImplementorBytes); err != nil {
			return err
		}
		if err := validateStringFieldAt(path, ".Compaction.EncryptedContent", item.Compaction.EncryptedContent, MaxCompactionEncryptedContentBytes); err != nil {
			return err
		}
		if len(item.Compaction.Opaque) > MaxPartJSONBytes {
			return &ValidationError{Field: path.withSuffix(".Compaction.Opaque").String(), Message: fmt.Sprintf("opaque compaction data exceeds %d bytes", MaxPartJSONBytes)}
		}
		if len(item.Compaction.Opaque) > 0 {
			if !json.Valid(item.Compaction.Opaque) {
				return &ValidationError{Field: path.withSuffix(".Compaction.Opaque").String(), Message: "opaque compaction data must be valid JSON"}
			}
			if err := validateJSONDepth(item.Compaction.Opaque, MaxJSONDepth); err != nil {
				return &ValidationError{Field: path.withSuffix(".Compaction.Opaque").String(), Message: err.Error()}
			}
		}
	case ItemKindExtension:
		if len(item.Content) > 0 || item.Role != "" || item.Reference != nil || item.ToolCall != nil || item.ToolResult != nil || item.Reasoning != nil || item.Compaction != nil {
			return &ValidationError{Field: path.String(), Message: "extension item must not contain non-extension variant fields"}
		}
		if item.Extension == nil {
			return &ValidationError{Field: path.withSuffix(".Extension").String(), Message: "extension record is required for extension item"}
		}
		if err := item.Extension.validate(path.withSuffix(".Extension").String()); err != nil {
			return err
		}
	case "":
		return &ValidationError{Field: path.withSuffix(".Kind").String(), Message: "item kind is required"}
	default:
		return &ValidationError{Field: path.withSuffix(".Kind").String(), Message: fmt.Sprintf("unknown item kind %q", item.Kind)}
	}
	return nil
}
