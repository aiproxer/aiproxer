package aipapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
)

// ErrProjectionNotRepresentable is returned when a projector cannot represent call semantics in the target view.
var ErrProjectionNotRepresentable = errors.New("aipapi: projection not representable")

// ProjectionReason identifies stable projector rejection causes.
type ProjectionReason string

const (
	ProjectionReasonConflictingAuthority ProjectionReason = "conflicting_authority"
	ProjectionReasonAssistantPhase       ProjectionReason = "assistant_phase"
	ProjectionReasonItemReference        ProjectionReason = "item_reference"
	ProjectionReasonCompaction           ProjectionReason = "compaction"
	ProjectionReasonOpaqueExtension      ProjectionReason = "opaque_extension"
	ProjectionReasonVideoInput           ProjectionReason = "video_input"
	ProjectionReasonAnnotation           ProjectionReason = "annotation"
	ProjectionReasonAssistantMediaRef    ProjectionReason = "assistant_media_ref"
	ProjectionReasonReasoningReplay      ProjectionReason = "reasoning_replay"
	ProjectionReasonUnsupportedContent   ProjectionReason = "unsupported_content"
	ProjectionReasonUnsupportedItemKind  ProjectionReason = "unsupported_item_kind"
	ProjectionReasonRefusal              ProjectionReason = "refusal"
	ProjectionReasonSummary              ProjectionReason = "summary"
)

// ProjectionError records a deterministic all-or-nothing projector failure.
type ProjectionError struct {
	Reason ProjectionReason
	Field  string
	Detail string
}

func (e *ProjectionError) Error() string {
	if e == nil {
		return ErrProjectionNotRepresentable.Error()
	}
	msg := string(e.Reason)
	if e.Field != "" {
		msg = fmt.Sprintf("%s at %s", msg, e.Field)
	}
	if e.Detail != "" {
		msg = fmt.Sprintf("%s: %s", msg, e.Detail)
	}
	return msg
}

func (e *ProjectionError) Unwrap() error { return ErrProjectionNotRepresentable }

// IsProjectionError reports whether err is or wraps a ProjectionError.
func IsProjectionError(err error) bool {
	var pe *ProjectionError
	return errors.As(err, &pe)
}

func projectionErr(reason ProjectionReason, field, detail string) error {
	return &ProjectionError{Reason: reason, Field: field, Detail: detail}
}

// LegacyProjectionTarget describes the portable intersection a legacy-message backend can consume.
type LegacyProjectionTarget struct {
	Caps                     BackendCaps
	ReplaySupport            ReasoningReplaySupport
	SupportsPhase            bool
	SupportsItemReferences   bool
	SupportsCompaction       bool
	SupportsVideoInput       bool
	SupportsOpaqueExtensions bool
	SupportsAnnotations      bool
	SupportsAssistantRefs    bool
	SupportsSummaries        bool
	SupportedExtensions      []ExtensionRequirement
}

// LegacyProjectionResult is the deterministic legacy-message view produced from item authority.
type LegacyProjectionResult struct {
	Instructions []Message
	Messages     []Message
	Requirements ProtocolRequirements
}

// OrderedItemProjectionTarget describes the ordered-item surface an OpenResponses-style backend accepts.
type OrderedItemProjectionTarget struct {
	SupportsCallExtensions bool
	SupportedExtensions    []ExtensionRequirement
}

// DefaultLegacyProjectionTarget returns the portable intersection shared by existing legacy backends.
func DefaultLegacyProjectionTarget(caps BackendCaps, replay ReasoningReplaySupport) LegacyProjectionTarget {
	return LegacyProjectionTargetFromCaps(caps, replay)
}

// LegacyProjectionTargetFromCaps derives projection feature flags from backend capabilities.
func LegacyProjectionTargetFromCaps(caps BackendCaps, replay ReasoningReplaySupport) LegacyProjectionTarget {
	return LegacyProjectionTarget{
		Caps:          caps,
		ReplaySupport: replay,
		// Legacy message projection has no phase carrier; never advertise phase support.
		SupportsPhase:            false,
		SupportsItemReferences:   capsHas(caps, CapabilityItemReferences),
		SupportsCompaction:       capsHas(caps, CapabilityCompaction),
		SupportsVideoInput:       capsHas(caps, CapabilityVideoInput),
		SupportsOpaqueExtensions: capsHas(caps, CapabilityOpaqueExtensions),
		SupportsAnnotations:      capsHas(caps, CapabilityAnnotations),
		SupportsAssistantRefs:    capsHas(caps, CapabilityAssistantMediaRefs),
		SupportsSummaries:        capsHas(caps, CapabilityStructuredOutputs),
	}
}

func capsHas(caps BackendCaps, targetCap Capability) bool {
	if caps == nil {
		return false
	}
	_, ok := caps[targetCap]
	return ok
}

// DefaultOrderedItemProjectionTarget returns the default OpenResponses ordered-item constructor target.
func DefaultOrderedItemProjectionTarget() OrderedItemProjectionTarget {
	return OrderedItemProjectionTarget{}
}

// OrderedItemProjectionTargetFromCaps derives ordered-item constructor flags from backend capabilities.
func OrderedItemProjectionTargetFromCaps(caps BackendCaps) OrderedItemProjectionTarget {
	return OrderedItemProjectionTarget{
		SupportsCallExtensions: capsHas(caps, CapabilityOpaqueExtensions),
	}
}

// ProjectItemsToLegacyView projects an item-authority call into a legacy message-authority view.
// It returns a complete representation or a stable ProjectionError; partial results are forbidden.
func ProjectItemsToLegacyView(call Call, target LegacyProjectionTarget) (LegacyProjectionResult, error) {
	if !call.HasItemAuthority() {
		return LegacyProjectionResult{}, projectionErr(ProjectionReasonConflictingAuthority, "Items", "item authority required")
	}
	if err := call.Validate(); err != nil {
		return LegacyProjectionResult{}, err
	}
	if len(call.SemanticExtensions) > 0 {
		return LegacyProjectionResult{}, projectionErr(ProjectionReasonOpaqueExtension, "SemanticExtensions", "semantic residual has no legacy projection authority")
	}
	var instructions, messages []Message
	inLeadingInstructions := true
	for i, item := range call.Items {
		field := fmt.Sprintf("Items[%d]", i)
		if err := checkItemLegacyRepresentable(item, field, target); err != nil {
			return LegacyProjectionResult{}, err
		}
		switch item.Kind {
		case ItemKindMessage:
			msg := Message{Role: item.Role}
			parts, err := contentPartsToLegacyParts(item.Content)
			if err != nil {
				return LegacyProjectionResult{}, projectionErr(ProjectionReasonUnsupportedContent, field, err.Error())
			}
			msg.Parts = parts
			if inLeadingInstructions && (item.Role == RoleSystem || item.Role == RoleDeveloper) {
				instructions = append(instructions, msg)
			} else {
				inLeadingInstructions = false
				messages = append(messages, msg)
			}
		case ItemKindToolCall:
			inLeadingInstructions = false
			if item.ToolCall == nil {
				return LegacyProjectionResult{}, projectionErr(ProjectionReasonUnsupportedItemKind, field, "missing tool call payload")
			}
			part := Part{
				Kind:       PartJSON,
				ToolCallID: item.ToolCall.CallID,
				ToolName:   item.ToolCall.Name,
				Content:    append(json.RawMessage(nil), item.ToolCall.Arguments...),
			}
			messages = append(messages, Message{Role: RoleAssistant, Parts: []Part{part}})
		case ItemKindToolResult:
			inLeadingInstructions = false
			if item.ToolResult == nil {
				return LegacyProjectionResult{}, projectionErr(ProjectionReasonUnsupportedItemKind, field, "missing tool result payload")
			}
			parts, err := toolResultToLegacyParts(*item.ToolResult)
			if err != nil {
				return LegacyProjectionResult{}, projectionErr(ProjectionReasonUnsupportedContent, field+".ToolResult", err.Error())
			}
			messages = append(messages, Message{Role: RoleTool, Parts: parts})
		case ItemKindReasoning:
			inLeadingInstructions = false
			if item.Reasoning == nil || item.Reasoning.Reasoning == nil {
				return LegacyProjectionResult{}, projectionErr(ProjectionReasonUnsupportedItemKind, field, "missing reasoning payload")
			}
			messages = append(messages, Message{
				Role:  RoleAssistant,
				Parts: []Part{{Kind: PartReasoning, Reasoning: cloneReasoningPart(item.Reasoning.Reasoning)}},
			})
		default:
			return LegacyProjectionResult{}, projectionErr(ProjectionReasonUnsupportedItemKind, field, string(item.Kind))
		}
	}
	out := LegacyProjectionResult{
		Instructions: append([]Message(nil), instructions...),
		Messages:     append([]Message(nil), messages...),
		Requirements: DeriveProtocolRequirements(call),
	}
	return out, nil
}

// ProjectLegacyToOrderedItems constructs ordered items from a legacy message-authority call.
func ProjectLegacyToOrderedItems(call Call, target OrderedItemProjectionTarget) ([]Item, ProtocolRequirements, error) {
	if call.HasItemAuthority() {
		return nil, ProtocolRequirements{}, projectionErr(ProjectionReasonConflictingAuthority, "Items", "legacy authority required")
	}
	if len(call.SemanticExtensions) > 0 {
		return nil, ProtocolRequirements{}, projectionErr(ProjectionReasonOpaqueExtension, "SemanticExtensions", "semantic residual has no ordered-item projection authority")
	}
	if len(call.Messages) == 0 && len(call.Instructions) == 0 {
		return nil, ProtocolRequirements{}, &ValidationError{Field: "Messages", Message: "at least one message is required"}
	}
	if err := call.Validate(); err != nil {
		return nil, ProtocolRequirements{}, err
	}
	filteredExt := filterInternalExtensions(call.Extensions)
	if len(filteredExt) > 0 && !target.SupportsCallExtensions {
		return nil, ProtocolRequirements{}, projectionErr(ProjectionReasonOpaqueExtension, "Extensions", "call extensions not supported")
	}
	extKeys := sortedMapKeys(filteredExt)
	for _, key := range extKeys {
		if err := matchExtensionRequirement(ExtensionRequirement{Namespace: "call", Type: key}, target.SupportedExtensions); err != nil {
			return nil, ProtocolRequirements{}, projectionErr(ProjectionReasonOpaqueExtension, "Extensions."+key, err.Error())
		}
	}

	var items []Item
	seq := 0
	nextID := func(prefix string) string {
		id := fmt.Sprintf("%s-%d", prefix, seq)
		seq++
		return id
	}
	appendMessageItem := func(role Role, parts []Part) {
		items = append(items, Item{
			Kind:    ItemKindMessage,
			ID:      nextID("msg"),
			Status:  ItemStatusCompleted,
			Role:    role,
			Content: partsToContentParts(parts),
		})
	}
	for _, msg := range call.Instructions {
		appendMessageItem(msg.Role, msg.Parts)
	}
	for mi, msg := range call.Messages {
		field := fmt.Sprintf("Messages[%d]", mi)
		switch msg.Role {
		case RoleTool:
			for _, p := range msg.Parts {
				if p.Kind != PartToolResult {
					return nil, ProtocolRequirements{}, projectionErr(ProjectionReasonUnsupportedContent, field, "tool role requires tool_result parts")
				}
				items = append(items, Item{
					Kind:   ItemKindToolResult,
					ID:     nextID("tool-result"),
					Status: ItemStatusCompleted,
					ToolResult: &ToolResultItem{
						CallID: p.ToolCallID,
						Name:   p.ToolName,
						Output: p.Text,
					},
				})
			}
		case RoleAssistant:
			var msgParts []Part
			for pi, p := range msg.Parts {
				pf := fmt.Sprintf("%s.Parts[%d]", field, pi)
				switch p.Kind {
				case PartJSON:
					if p.ToolCallID == "" || p.ToolName == "" {
						return nil, ProtocolRequirements{}, projectionErr(ProjectionReasonUnsupportedContent, pf, "tool call part requires id and name")
					}
					if len(msgParts) > 0 {
						appendMessageItem(RoleAssistant, msgParts)
						msgParts = nil
					}
					items = append(items, Item{
						Kind:   ItemKindToolCall,
						ID:     nextID("tool-call"),
						Status: ItemStatusCompleted,
						ToolCall: &ToolCallItem{
							CallID:    p.ToolCallID,
							Name:      p.ToolName,
							Arguments: append(json.RawMessage(nil), p.Content...),
						},
					})
				default:
					msgParts = append(msgParts, p)
				}
			}
			if len(msgParts) > 0 {
				appendMessageItem(RoleAssistant, msgParts)
			}
		default:
			appendMessageItem(msg.Role, msg.Parts)
		}
	}
	req := DeriveProtocolRequirements(call)
	return items, req, nil
}

func checkItemLegacyRepresentable(item Item, field string, target LegacyProjectionTarget) error {
	switch item.Kind {
	case ItemKindItemReference:
		if !target.SupportsItemReferences {
			return projectionErr(ProjectionReasonItemReference, field, "item references not supported")
		}
	case ItemKindCompaction:
		if !target.SupportsCompaction {
			return projectionErr(ProjectionReasonCompaction, field, "compaction not supported")
		}
	case ItemKindExtension:
		if !target.SupportsOpaqueExtensions {
			return projectionErr(ProjectionReasonOpaqueExtension, field, "opaque extensions not supported")
		}
		if err := matchExtensionRequirement(extensionFromItem(item.Extension), target.SupportedExtensions); err != nil {
			return projectionErr(ProjectionReasonOpaqueExtension, field, err.Error())
		}
	}
	if item.Phase != "" {
		return projectionErr(ProjectionReasonAssistantPhase, field+".Phase", string(item.Phase))
	}
	for j, cp := range item.Content {
		cf := fmt.Sprintf("%s.Content[%d]", field, j)
		switch cp.Kind {
		case ContentPartVideoRef:
			if !target.SupportsVideoInput {
				return projectionErr(ProjectionReasonVideoInput, cf, "video input not supported")
			}
		case ContentPartExtension:
			return projectionErr(ProjectionReasonOpaqueExtension, cf, "opaque extension content parts have no legacy carrier")
		case ContentPartAnnotation:
			if !target.SupportsAnnotations {
				return projectionErr(ProjectionReasonAnnotation, cf, "annotations not supported")
			}
		case ContentPartAssistantRef:
			if !target.SupportsAssistantRefs {
				return projectionErr(ProjectionReasonAssistantMediaRef, cf, "assistant media refs not supported")
			}
		case ContentPartRefusal:
			return projectionErr(ProjectionReasonRefusal, cf, "refusal has no legacy carrier")
		case ContentPartSummary:
			return projectionErr(ProjectionReasonSummary, cf, "summary has no legacy carrier")
		case ContentPartReasoning:
			if cp.Reasoning != nil {
				if err := checkReasoningReplay(cp.Reasoning.Dialect, target.ReplaySupport); err != nil {
					return projectionErr(ProjectionReasonReasoningReplay, cf+".Reasoning", err.Error())
				}
			}
		default:
		}
	}
	if item.Reasoning != nil && item.Reasoning.Reasoning != nil {
		if err := checkReasoningReplay(item.Reasoning.Reasoning.Dialect, target.ReplaySupport); err != nil {
			return projectionErr(ProjectionReasonReasoningReplay, field+".Reasoning", err.Error())
		}
	}
	if item.ToolResult != nil {
		for j, cp := range item.ToolResult.Parts {
			if cp.Kind == ContentPartReasoning && cp.Reasoning != nil {
				cf := fmt.Sprintf("%s.ToolResult.Parts[%d].Reasoning", field, j)
				if err := checkReasoningReplay(cp.Reasoning.Dialect, target.ReplaySupport); err != nil {
					return projectionErr(ProjectionReasonReasoningReplay, cf, err.Error())
				}
			}
		}
	}
	return nil
}

func checkReasoningReplay(d ReasoningDialect, replay ReasoningReplaySupport) error {
	d = NormalizeReasoningDialect(d)
	if d == "" {
		return errors.New("reasoning dialect required")
	}
	if slices.Contains(NormalizeReasoningDialects(replay.Dialects), d) {
		return nil
	}
	return fmt.Errorf("unsupported reasoning dialect %q", d)
}

func extensionFromItem(ext *OpaqueExtension) ExtensionRequirement {
	if ext == nil {
		return ExtensionRequirement{}
	}
	return ExtensionRequirement{
		Namespace:   ext.Namespace,
		Type:        ext.Type,
		Implementor: ext.Implementor,
	}
}

func matchExtensionRequirement(req ExtensionRequirement, supported []ExtensionRequirement) error {
	norm := normalizeExtensionRequirements([]ExtensionRequirement{req})
	if len(norm) == 0 {
		return errors.New("invalid extension requirement")
	}
	req = norm[0]
	for _, s := range normalizeExtensionRequirements(supported) {
		if extensionKey(s) == extensionKey(req) {
			return nil
		}
	}
	return fmt.Errorf("unsupported extension %s/%s", req.Namespace, req.Type)
}

func contentPartsToLegacyParts(parts []ContentPart) ([]Part, error) {
	out := make([]Part, 0, len(parts))
	for i, cp := range parts {
		field := fmt.Sprintf("Content[%d]", i)
		switch cp.Kind {
		case ContentPartText:
			out = append(out, TextPart(cp.Text))
		case ContentPartImageRef:
			p := Part{Kind: PartImageRef, ImageRef: cp.ImageRef, ImageMIME: cp.ImageMIME}
			if cp.Annotation != nil && len(cp.Annotation.Data) > 0 {
				p.Content = append(json.RawMessage(nil), cp.Annotation.Data...)
			}
			out = append(out, p)
		case ContentPartFileRef:
			if cp.FileRef == "" {
				return nil, fmt.Errorf("%s: inline file data has no legacy carrier", field)
			}
			out = append(out, FilePart(cp.FileRef, cp.FileMIME, cp.FileName))
		case ContentPartRefusal:
			return nil, fmt.Errorf("%s: refusal has no legacy carrier", field)
		case ContentPartReasoning:
			out = append(out, Part{Kind: PartReasoning, Reasoning: cloneReasoningPart(cp.Reasoning)})
		case ContentPartJSON:
			out = append(out, Part{Kind: PartJSON, Content: json.RawMessage(cp.Text)})
		case ContentPartSummary:
			return nil, fmt.Errorf("%s: summary has no legacy carrier", field)
		case ContentPartToolResult:
			out = append(out, Part{Kind: PartToolResult, Text: cp.Text})
		case ContentPartExtension:
			return nil, fmt.Errorf("%s: opaque extension content part has no legacy carrier", field)
		default:
			return nil, fmt.Errorf("%s: unsupported content kind %q", field, cp.Kind)
		}
	}
	return out, nil
}

func toolResultToLegacyParts(tr ToolResultItem) ([]Part, error) {
	if tr.Output != "" {
		return []Part{{Kind: PartToolResult, ToolCallID: tr.CallID, ToolName: tr.Name, Text: tr.Output}}, nil
	}
	var out []Part
	for i, cp := range tr.Parts {
		switch cp.Kind {
		case ContentPartText:
			out = append(out, Part{Kind: PartToolResult, ToolCallID: tr.CallID, ToolName: tr.Name, Text: cp.Text})
		default:
			return nil, fmt.Errorf("toolresult.parts[%d]: unsupported structured tool output kind %q", i, cp.Kind)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("tool result must specify output or representable text parts")
	}
	return out, nil
}

// RequiresProjectionAdaptation reports whether call authority differs from the candidate view.
func RequiresProjectionAdaptation(call Call, caps BackendCaps) bool {
	return call.HasItemAuthority() != capsHas(caps, CapabilityOrderedItems)
}

// AdaptCallForCandidate projects the call into the candidate's authority/view when required.
func AdaptCallForCandidate(call Call, target LegacyProjectionTarget) (Call, error) {
	if !RequiresProjectionAdaptation(call, target.Caps) {
		return call, nil
	}
	if call.HasItemAuthority() {
		proj, err := ProjectItemsToLegacyView(call, target)
		if err != nil {
			return Call{}, err
		}
		out := CloneCall(call)
		out.Items = nil
		out.Instructions = append([]Message(nil), proj.Instructions...)
		out.Messages = append([]Message(nil), proj.Messages...)
		return out, nil
	}
	orderedTarget := OrderedItemProjectionTargetFromCaps(target.Caps)
	orderedTarget.SupportedExtensions = append([]ExtensionRequirement(nil), target.SupportedExtensions...)
	orderedTarget.SupportsCallExtensions = target.SupportsOpaqueExtensions
	items, _, err := ProjectLegacyToOrderedItems(call, orderedTarget)
	if err != nil {
		return Call{}, err
	}
	out := CloneCall(call)
	out.Messages = nil
	out.Instructions = nil
	out.Items = append([]Item(nil), items...)
	return out, nil
}

// CheckProjectionFeasibility verifies that call semantics can be projected into target before upstream work.
func CheckProjectionFeasibility(call Call, target LegacyProjectionTarget) error {
	if !RequiresProjectionAdaptation(call, target.Caps) {
		return nil
	}
	if call.HasItemAuthority() {
		_, err := ProjectItemsToLegacyView(call, target)
		return err
	}
	orderedTarget := OrderedItemProjectionTargetFromCaps(target.Caps)
	orderedTarget.SupportedExtensions = append([]ExtensionRequirement(nil), target.SupportedExtensions...)
	orderedTarget.SupportsCallExtensions = target.SupportsOpaqueExtensions
	_, _, err := ProjectLegacyToOrderedItems(call, orderedTarget)
	return err
}

func sortedMapKeys(m map[string]json.RawMessage) []string {
	if len(m) == 0 {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func filterInternalExtensions(m map[string]json.RawMessage) map[string]json.RawMessage {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]json.RawMessage, len(m))
	for k, v := range m {
		if isNonProtocolExtensionKey(k) {
			continue
		}
		out[k] = v
	}
	return out
}

// RequirementsRejectError records hard rejects from exact requirement matching.
type RequirementsRejectError struct {
	MissingCaps       []Capability
	MissingDialects   []DialectRequirement
	MissingExtensions []ExtensionRequirement
}

func (e *RequirementsRejectError) Error() string {
	parts := []string{"protocol requirements not satisfied"}
	if len(e.MissingCaps) > 0 {
		names := make([]string, 0, len(e.MissingCaps))
		for _, c := range e.MissingCaps {
			names = append(names, string(c))
		}
		slices.Sort(names)
		parts = append(parts, "missing capabilities: "+strings.Join(names, ", "))
	}
	if len(e.MissingDialects) > 0 {
		parts = append(parts, fmt.Sprintf("missing dialects: %d", len(e.MissingDialects)))
	}
	if len(e.MissingExtensions) > 0 {
		parts = append(parts, fmt.Sprintf("missing extensions: %d", len(e.MissingExtensions)))
	}
	return strings.Join(parts, "; ")
}

func (e *RequirementsRejectError) Unwrap() error { return ErrCapabilityReject }
