package backendplugin

import (
	"fmt"
	"slices"
	"strings"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

const (
	defaultMaxInvocationItems       = aipapi.MaxItems
	defaultMaxInvocationContent     = aipapi.MaxContentPartsPerItem
	defaultMaxInvocationToolParts   = aipapi.MaxContentPartsPerItem
	defaultMaxInvocationStringBytes = aipapi.MaxPartTextBytes
)

var knownInvocationItemKinds = map[string]struct{}{
	"message": {}, "tool_call": {}, "tool_result": {}, "item_reference": {},
	"reasoning": {}, "compaction": {}, "extension": {},
}

var knownInvocationItemStatuses = map[string]struct{}{
	string(aipapi.ItemStatusInProgress): {}, string(aipapi.ItemStatusCompleted): {}, string(aipapi.ItemStatusIncomplete): {},
}

var knownAssistantPhases = map[string]struct{}{
	string(aipapi.AssistantPhaseCommentary): {}, string(aipapi.AssistantPhaseFinalAnswer): {},
}

var knownInvocationRoles = map[Role]struct{}{
	RoleSystem: {}, RoleDeveloper: {}, RoleUser: {}, RoleAssistant: {}, RoleTool: {},
}

func validateInvocationWire(inv Invocation) error {
	if err := validateInvocationSemanticAuthority(inv); err != nil {
		return err
	}
	for i, ext := range inv.SemanticExtensions {
		if err := validateSemanticExtension(ext); err != nil {
			return fmt.Errorf("semantic extension %d: %w", i, err)
		}
	}
	if err := validateToolChoiceWire(inv.ToolChoice); err != nil {
		return err
	}
	if inv.ItemAuthority {
		if len(inv.Items) == 0 {
			return ErrInvalidInvocation
		}
		if len(inv.Items) > defaultMaxInvocationItems {
			return ErrOversizedMessage
		}
		seenIDs := make(map[string]struct{}, len(inv.Items))
		for i, item := range inv.Items {
			if err := validateInvocationItem(item, fmt.Sprintf("Items[%d]", i)); err != nil {
				return err
			}
			id := strings.TrimSpace(item.ID)
			if id != "" {
				if _, dup := seenIDs[id]; dup {
					return fmt.Errorf("%w: duplicate item id %q", ErrInvalidInvocation, id)
				}
				seenIDs[id] = struct{}{}
			}
		}
		if err := validateProtocolRequirements(inv.ProtocolRequirements); err != nil {
			return err
		}
	}
	return nil
}

func validateProtocolRequirements(req ProtocolRequirements) error {
	if len(req.Capabilities) > defaultMaxInvocationItems {
		return ErrOversizedMessage
	}
	for _, c := range req.Capabilities {
		if strings.TrimSpace(c) == "" {
			return ErrInvalidInvocation
		}
		if !isKnownCapabilityName(c) {
			return fmt.Errorf("%w: unknown capability %q", ErrInvalidInvocation, c)
		}
	}
	slices := []struct {
		want string
		in   []DialectRequirementDTO
	}{
		{want: "item", in: req.ItemDialects},
		{want: "reasoning", in: req.ReasoningDialects},
		{want: "compaction", in: req.CompactionDialects},
	}
	for _, sl := range slices {
		if len(sl.in) > defaultMaxInvocationItems {
			return ErrOversizedMessage
		}
		if err := validateDialectSliceKinds(sl.in, sl.want); err != nil {
			return err
		}
		for _, d := range sl.in {
			if strings.TrimSpace(d.Kind) == "" || strings.TrimSpace(d.Dialect) == "" {
				return ErrInvalidInvocation
			}
		}
	}
	if len(req.ExtensionTypes) > defaultMaxInvocationItems {
		return ErrOversizedMessage
	}
	seenExt := make(map[string]struct{}, len(req.ExtensionTypes))
	for _, e := range req.ExtensionTypes {
		if strings.TrimSpace(e.Namespace) == "" || strings.TrimSpace(e.Type) == "" {
			return ErrInvalidInvocation
		}
		key := strings.TrimSpace(e.Namespace) + "\x00" + strings.ToLower(strings.TrimSpace(e.Type)) + "\x00" + strings.ToLower(strings.TrimSpace(e.Implementor))
		if _, dup := seenExt[key]; dup {
			return fmt.Errorf("%w: duplicate extension requirement", ErrInvalidInvocation)
		}
		seenExt[key] = struct{}{}
	}
	return nil
}

func validateDialectSliceKinds(slice []DialectRequirementDTO, wantKind string) error {
	wantKind = strings.ToLower(strings.TrimSpace(wantKind))
	for _, d := range slice {
		got := strings.ToLower(strings.TrimSpace(d.Kind))
		if got != wantKind {
			return fmt.Errorf("%w: dialect slice requires kind %q, got %q", ErrInvalidInvocation, wantKind, d.Kind)
		}
	}
	return nil
}

func isKnownCapabilityName(name string) bool {
	switch aipapi.Capability(name) {
	case aipapi.CapabilityStreaming, aipapi.CapabilityTools, aipapi.CapabilityVision,
		aipapi.CapabilityDocuments, aipapi.CapabilityReasoning, aipapi.CapabilityReasoningReplay,
		aipapi.CapabilityStructuredOutputs, aipapi.CapabilityOrderedItems, aipapi.CapabilityItemReferences,
		aipapi.CapabilityCompaction, aipapi.CapabilityOpaqueExtensions, aipapi.CapabilityAnnotations,
		aipapi.CapabilityAssistantMediaRefs, aipapi.CapabilityAssistantPhase, aipapi.CapabilityVideoInput,
		aipapi.CapabilityParallelToolCalls:
		return true
	default:
		return false
	}
}

func validateInvocationItem(item InvocationItem, field string) error {
	kind := strings.ToLower(strings.TrimSpace(item.Kind))
	if kind == "" {
		return fmt.Errorf("%w: %s requires kind", ErrInvalidInvocation, field)
	}
	if _, ok := knownInvocationItemKinds[kind]; !ok {
		return fmt.Errorf("%w: %s unknown item kind %q", ErrInvalidInvocation, field, item.Kind)
	}
	if strings.TrimSpace(item.ID) == "" {
		return fmt.Errorf("%w: %s requires id", ErrInvalidInvocation, field)
	}
	if len(item.Kind) > aipapi.MaxItemKindBytes || len(item.Status) > aipapi.MaxItemStatusBytes {
		return ErrOversizedMessage
	}
	if item.Status != "" {
		if _, ok := knownInvocationItemStatuses[item.Status]; !ok {
			return fmt.Errorf("%w: %s unknown status %q", ErrInvalidInvocation, field, item.Status)
		}
	}
	if item.Phase != "" {
		if _, ok := knownAssistantPhases[item.Phase]; !ok {
			return fmt.Errorf("%w: %s unknown phase %q", ErrInvalidInvocation, field, item.Phase)
		}
	}
	if item.Role != "" {
		if _, ok := knownInvocationRoles[item.Role]; !ok {
			return fmt.Errorf("%w: %s unknown role %q", ErrInvalidInvocation, field, item.Role)
		}
	}
	if item.Phase != "" && (kind != "message" || item.Role != RoleAssistant) {
		return fmt.Errorf("%w: %s phase only allowed on assistant message items", ErrInvalidInvocation, field)
	}
	if len(item.Content) > defaultMaxInvocationContent {
		return ErrOversizedMessage
	}
	for j, cp := range item.Content {
		if err := validateInvocationContentPart(cp, fmt.Sprintf("%s.Content[%d]", field, j)); err != nil {
			return err
		}
	}
	if item.ToolCall != nil {
		if err := validateInvocationToolCall(*item.ToolCall, field+".ToolCall"); err != nil {
			return err
		}
	}
	if item.ToolResult != nil {
		if err := validateInvocationToolResult(*item.ToolResult, field+".ToolResult"); err != nil {
			return err
		}
	}
	if item.ItemReference != nil && len(item.ItemReference.ID) > aipapi.MaxItemReferenceIDBytes {
		return ErrOversizedMessage
	}
	if item.Reasoning != nil {
		if err := validateInvocationReasoningItem(*item.Reasoning, field+".Reasoning"); err != nil {
			return err
		}
	}
	if item.Compaction != nil {
		if err := validateInvocationCompactionItem(*item.Compaction, field+".Compaction"); err != nil {
			return err
		}
	}
	if item.Extension != nil {
		if err := validateInvocationExtensionItem(*item.Extension, field+".Extension"); err != nil {
			return err
		}
	}
	return validateInvocationItemUnion(item, field)
}

func validateInvocationItemUnion(item InvocationItem, field string) error {
	switch strings.ToLower(strings.TrimSpace(item.Kind)) {
	case "message":
		if item.ToolCall != nil || item.ToolResult != nil || item.ItemReference != nil ||
			item.Reasoning != nil || item.Compaction != nil || item.Extension != nil {
			return fmt.Errorf("%w: %s message item has conflicting payloads", ErrInvalidInvocation, field)
		}
		if item.Role == "" {
			return fmt.Errorf("%w: %s message item requires role", ErrInvalidInvocation, field)
		}
		if len(item.Content) == 0 {
			return fmt.Errorf("%w: %s message item requires content", ErrInvalidInvocation, field)
		}
	case "tool_call":
		if item.ToolCall == nil || len(item.Content) > 0 || item.ToolResult != nil ||
			item.ItemReference != nil || item.Reasoning != nil || item.Compaction != nil || item.Extension != nil {
			return fmt.Errorf("%w: %s tool_call item shape", ErrInvalidInvocation, field)
		}
	case "tool_result":
		if item.ToolResult == nil || len(item.Content) > 0 || item.ToolCall != nil ||
			item.ItemReference != nil || item.Reasoning != nil || item.Compaction != nil || item.Extension != nil {
			return fmt.Errorf("%w: %s tool_result item shape", ErrInvalidInvocation, field)
		}
		if item.Role != "" {
			return fmt.Errorf("%w: %s tool_result item must not carry role", ErrInvalidInvocation, field)
		}
	case "item_reference":
		if item.ItemReference == nil || len(item.Content) > 0 || item.ToolCall != nil || item.ToolResult != nil ||
			item.Reasoning != nil || item.Compaction != nil || item.Extension != nil {
			return fmt.Errorf("%w: %s item_reference item shape", ErrInvalidInvocation, field)
		}
	case "reasoning":
		if item.Reasoning == nil || len(item.Content) > 0 || item.ToolCall != nil || item.ToolResult != nil ||
			item.ItemReference != nil || item.Compaction != nil || item.Extension != nil {
			return fmt.Errorf("%w: %s reasoning item shape", ErrInvalidInvocation, field)
		}
	case "compaction":
		if item.Compaction == nil || len(item.Content) > 0 || item.ToolCall != nil || item.ToolResult != nil ||
			item.ItemReference != nil || item.Reasoning != nil || item.Extension != nil {
			return fmt.Errorf("%w: %s compaction item shape", ErrInvalidInvocation, field)
		}
	case "extension":
		if item.Extension == nil || len(item.Content) > 0 || item.ToolCall != nil || item.ToolResult != nil ||
			item.ItemReference != nil || item.Reasoning != nil || item.Compaction != nil {
			return fmt.Errorf("%w: %s extension item shape", ErrInvalidInvocation, field)
		}
	}
	return nil
}

func validateInvocationToolCall(tc InvocationToolCall, field string) error {
	if strings.TrimSpace(tc.CallID) == "" || strings.TrimSpace(tc.Name) == "" {
		return fmt.Errorf("%w: %s requires call id and name", ErrInvalidInvocation, field)
	}
	if tc.Name != strings.TrimSpace(tc.Name) {
		return fmt.Errorf("%w: %s name must not contain leading or trailing whitespace", ErrInvalidInvocation, field)
	}
	return tc.Arguments.Validate(DefaultMaxRawJSONBytes)
}

func validateInvocationToolResult(tr InvocationToolResult, field string) error {
	if strings.TrimSpace(tr.CallID) == "" || strings.TrimSpace(tr.Name) == "" {
		return fmt.Errorf("%w: %s requires call id and name", ErrInvalidInvocation, field)
	}
	if tr.Name != strings.TrimSpace(tr.Name) {
		return fmt.Errorf("%w: %s name must not contain leading or trailing whitespace", ErrInvalidInvocation, field)
	}
	if tr.Output != nil && len(*tr.Output) > int(defaultMaxInvocationStringBytes) {
		return ErrOversizedMessage
	}
	if len(tr.StructuredParts) > defaultMaxInvocationToolParts {
		return ErrOversizedMessage
	}
	for j, cp := range tr.StructuredParts {
		if err := validateInvocationContentPart(cp, fmt.Sprintf("%s.StructuredParts[%d]", field, j)); err != nil {
			return err
		}
	}
	if tr.Output == nil && len(tr.StructuredParts) == 0 {
		return fmt.Errorf("%w: %s requires output or structured parts", ErrInvalidInvocation, field)
	}
	if tr.Output != nil && len(tr.StructuredParts) > 0 {
		return fmt.Errorf("%w: %s requires exactly one of output or structured parts", ErrInvalidInvocation, field)
	}
	return nil
}

func validateInvocationReasoningItem(r InvocationReasoningItem, field string) error {
	if r.Dialect == nil || strings.TrimSpace(*r.Dialect) == "" {
		return fmt.Errorf("%w: %s requires normalized reasoning dialect", ErrInvalidInvocation, field)
	}
	if r.Text != nil && len(*r.Text) > int(aipapi.MaxReasoningTextBytes) {
		return ErrOversizedMessage
	}
	if r.Signature != nil && len(*r.Signature) > int(aipapi.MaxReasoningSignatureBytes) {
		return ErrOversizedMessage
	}
	if err := r.Opaque.Validate(DefaultMaxRawJSONBytes); err != nil {
		return err
	}
	return validateReasoningExactFields(r.Summary, r.Content, r.EncryptedContent, field)
}

func validateInvocationCompactionItem(c InvocationCompactionItem, field string) error {
	if len(c.Dialect) > aipapi.MaxCompactionDialectBytes {
		return ErrOversizedMessage
	}
	if len(c.EncapsulatedID) > aipapi.MaxRefStringBytes {
		return ErrOversizedMessage
	}
	if len(c.EncryptedContent) > aipapi.MaxCompactionEncryptedContentBytes {
		return ErrOversizedMessage
	}
	return c.Opaque.Validate(DefaultMaxRawJSONBytes)
}

func validateInvocationExtensionItem(e InvocationExtensionItem, field string) error {
	if len(e.Namespace) > aipapi.MaxExtensionNamespaceBytes || len(e.Type) > aipapi.MaxExtensionTypeBytes {
		return ErrOversizedMessage
	}
	if len(e.Direction) > aipapi.MaxExtensionDirectionBytes {
		return ErrOversizedMessage
	}
	return e.Opaque.Validate(DefaultMaxRawJSONBytes)
}

func validateInvocationContentPart(cp InvocationContentPart, field string) error {
	if err := validatePartKind(cp.Kind); err != nil {
		return fmt.Errorf("%w: %s: %w", ErrInvalidInvocation, field, err)
	}
	if cp.Text != nil && len(*cp.Text) > int(defaultMaxInvocationStringBytes) {
		return ErrOversizedMessage
	}
	for _, ref := range []*string{cp.ImageRef, cp.FileRef, cp.VideoRef, cp.Refusal, cp.Summary, cp.AssistantRef} {
		if ref != nil && len(*ref) > int(aipapi.MaxRefStringBytes) {
			return ErrOversizedMessage
		}
	}
	for _, mime := range []*string{cp.ImageMIME, cp.FileMIME, cp.VideoMIME} {
		if mime != nil && len(*mime) > int(aipapi.MaxRefStringBytes) {
			return ErrOversizedMessage
		}
	}
	if cp.FileName != nil && len(*cp.FileName) > int(aipapi.MaxRefStringBytes) {
		return ErrOversizedMessage
	}
	if cp.FileData != nil && len(*cp.FileData) > int(aipapi.MaxFileDataBytes) {
		return ErrOversizedMessage
	}
	if cp.ExtensionType != nil && len(*cp.ExtensionType) > int(aipapi.MaxExtensionTypeBytes) {
		return ErrOversizedMessage
	}
	if cp.ExtensionNamespace != nil && len(*cp.ExtensionNamespace) > int(aipapi.MaxExtensionNamespaceBytes) {
		return ErrOversizedMessage
	}
	if cp.ExtensionImplementor != nil && len(*cp.ExtensionImplementor) > int(aipapi.MaxExtensionImplementorBytes) {
		return ErrOversizedMessage
	}
	if err := cp.ExtensionData.Validate(DefaultMaxRawJSONBytes); err != nil {
		return err
	}
	if cp.Reasoning != nil {
		if cp.Reasoning.Dialect == nil || strings.TrimSpace(*cp.Reasoning.Dialect) == "" {
			return fmt.Errorf("%w: %s requires normalized reasoning dialect", ErrInvalidInvocation, field)
		}
		if cp.Reasoning.Text != nil && len(*cp.Reasoning.Text) > int(aipapi.MaxReasoningTextBytes) {
			return ErrOversizedMessage
		}
		if cp.Reasoning.Signature != nil && len(*cp.Reasoning.Signature) > int(aipapi.MaxReasoningSignatureBytes) {
			return ErrOversizedMessage
		}
		if err := cp.Reasoning.Opaque.Validate(DefaultMaxRawJSONBytes); err != nil {
			return err
		}
		if err := validateReasoningExactFields(cp.Reasoning.Summary, cp.Reasoning.Content, cp.Reasoning.EncryptedContent, field+".Reasoning"); err != nil {
			return err
		}
	}
	if err := cp.AnnotationData.Validate(DefaultMaxRawJSONBytes); err != nil {
		return err
	}
	return validateInvocationContentPartUnion(cp, field)
}

func validatePartKind(k PartKind) error {
	switch k {
	case PartKindText, PartKindJSON, PartKindToolResult, PartKindImageRef, PartKindFileRef,
		PartKindVideoRef, PartKindReasoning, PartKindRefusal, PartKindSummary,
		PartKindAnnotation, PartKindAssistantRef, PartKindExtension:
		return nil
	default:
		return ErrUnknownEnum
	}
}

// validateReasoningExactFields enforces canonical bounds and presence rules for
// the exact OpenAI Responses reasoning-item fields: summary/content must be JSON
// arrays (null rejected), encrypted_content may be absent/null/value.
func validateReasoningExactFields(summary, content, encrypted RawJSON, field string) error {
	if err := validateExactReasoningRawFields(summary, content, encrypted, field); err != nil {
		return err
	}
	for _, r := range []struct {
		name string
		raw  RawJSON
	}{{"summary", summary}, {"content", content}} {
		switch r.raw.State() {
		case RawJSONNull:
			return fmt.Errorf("%w: %s %s must not be null", ErrInvalidInvocation, field, r.name)
		case RawJSONValue:
			// Shape and bounds are checked by validateExactReasoningRawFields.
		}
	}
	return encrypted.Validate(DefaultMaxRawJSONBytes)
}

func validateInvocationContentPartUnion(cp InvocationContentPart, field string) error {
	switch cp.Kind {
	case PartKindText, PartKindJSON, PartKindToolResult:
		if cp.Text == nil || strings.TrimSpace(*cp.Text) == "" {
			return fmt.Errorf("%w: %s requires text payload", ErrInvalidInvocation, field)
		}
		if hasConflictingContentFields(cp, "text") {
			return fmt.Errorf("%w: %s has conflicting content payloads", ErrInvalidInvocation, field)
		}
	case PartKindImageRef:
		if cp.ImageRef == nil || strings.TrimSpace(*cp.ImageRef) == "" {
			return fmt.Errorf("%w: %s requires image_ref", ErrInvalidInvocation, field)
		}
		if hasConflictingContentFields(cp, "image_ref") {
			return fmt.Errorf("%w: %s has conflicting content payloads", ErrInvalidInvocation, field)
		}
	case PartKindFileRef:
		if cp.FileRef == nil && cp.FileData == nil {
			return fmt.Errorf("%w: %s requires file_ref or file_data", ErrInvalidInvocation, field)
		}
		if hasConflictingContentFields(cp, "file_ref", "file_data") {
			return fmt.Errorf("%w: %s has conflicting content payloads", ErrInvalidInvocation, field)
		}
	case PartKindVideoRef:
		if cp.VideoRef == nil || strings.TrimSpace(*cp.VideoRef) == "" {
			return fmt.Errorf("%w: %s requires video_ref", ErrInvalidInvocation, field)
		}
		if hasConflictingContentFields(cp, "video_ref") {
			return fmt.Errorf("%w: %s has conflicting content payloads", ErrInvalidInvocation, field)
		}
	case PartKindReasoning:
		if cp.Reasoning == nil {
			return fmt.Errorf("%w: %s requires reasoning payload", ErrInvalidInvocation, field)
		}
		if hasConflictingContentFields(cp, "reasoning") {
			return fmt.Errorf("%w: %s has conflicting content payloads", ErrInvalidInvocation, field)
		}
	case PartKindRefusal:
		if cp.Refusal == nil || strings.TrimSpace(*cp.Refusal) == "" {
			return fmt.Errorf("%w: %s requires refusal", ErrInvalidInvocation, field)
		}
		if hasConflictingContentFields(cp, "refusal") {
			return fmt.Errorf("%w: %s has conflicting content payloads", ErrInvalidInvocation, field)
		}
	case PartKindSummary:
		if cp.Summary == nil || strings.TrimSpace(*cp.Summary) == "" {
			return fmt.Errorf("%w: %s requires summary", ErrInvalidInvocation, field)
		}
		if hasConflictingContentFields(cp, "summary") {
			return fmt.Errorf("%w: %s has conflicting content payloads", ErrInvalidInvocation, field)
		}
	case PartKindAnnotation:
		if cp.AnnotationType == nil || strings.TrimSpace(*cp.AnnotationType) == "" {
			return fmt.Errorf("%w: %s requires annotation type", ErrInvalidInvocation, field)
		}
		if hasConflictingContentFields(cp, "annotation") {
			return fmt.Errorf("%w: %s has conflicting content payloads", ErrInvalidInvocation, field)
		}
	case PartKindAssistantRef:
		if cp.AssistantRef == nil || strings.TrimSpace(*cp.AssistantRef) == "" {
			return fmt.Errorf("%w: %s requires assistant_ref", ErrInvalidInvocation, field)
		}
		if hasConflictingContentFields(cp, "assistant_ref") {
			return fmt.Errorf("%w: %s has conflicting content payloads", ErrInvalidInvocation, field)
		}
	case PartKindExtension:
		if cp.ExtensionType == nil || strings.TrimSpace(*cp.ExtensionType) == "" {
			return fmt.Errorf("%w: %s requires extension type", ErrInvalidInvocation, field)
		}
		if cp.ExtensionData.State() != RawJSONValue {
			return fmt.Errorf("%w: %s requires extension data", ErrInvalidInvocation, field)
		}
		if cp.ExtensionNamespace != nil && strings.TrimSpace(*cp.ExtensionNamespace) == "" {
			return fmt.Errorf("%w: %s extension namespace must not be empty", ErrInvalidInvocation, field)
		}
		if hasConflictingContentFields(cp, "extension") {
			return fmt.Errorf("%w: %s has conflicting content payloads", ErrInvalidInvocation, field)
		}
	}
	return nil
}

func hasConflictingContentFields(cp InvocationContentPart, keep ...string) bool {
	checks := map[string]bool{
		"text":          cp.Text != nil && strings.TrimSpace(*cp.Text) != "",
		"image_ref":     cp.ImageRef != nil && strings.TrimSpace(*cp.ImageRef) != "",
		"file_ref":      cp.FileRef != nil && strings.TrimSpace(*cp.FileRef) != "",
		"file_data":     cp.FileData != nil && strings.TrimSpace(*cp.FileData) != "",
		"video_ref":     cp.VideoRef != nil && strings.TrimSpace(*cp.VideoRef) != "",
		"reasoning":     cp.Reasoning != nil,
		"refusal":       cp.Refusal != nil && strings.TrimSpace(*cp.Refusal) != "",
		"summary":       cp.Summary != nil && strings.TrimSpace(*cp.Summary) != "",
		"annotation":    cp.AnnotationType != nil && strings.TrimSpace(*cp.AnnotationType) != "",
		"assistant_ref": cp.AssistantRef != nil && strings.TrimSpace(*cp.AssistantRef) != "",
		"extension":     cp.ExtensionType != nil && strings.TrimSpace(*cp.ExtensionType) != "",
	}
	for k, set := range checks {
		if set && !slices.Contains(keep, k) {
			return true
		}
	}
	return false
}
