package controltool

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// Bounded, content-free eligibility reasons. ReasonActive is the empty reason of
// an approved projection; every other value explains why an optional control
// feature stayed inactive without turning the condition into a candidate
// rejection.
const (
	ReasonActive                  = ""
	ReasonBackendToolsUnsupported = "backend_tools_unsupported"
	ReasonToolChoiceNone          = "tool_choice_none"
	ReasonToolChoiceConstrained   = "tool_choice_constrained"
	ReasonToolChoiceRequired      = "tool_choice_required"
	ReasonAllowedToolsConstrained = "allowed_tools_constrained"
	ReasonToolNameCollision       = "tool_name_collision"
	ReasonInstructionCollision    = "instruction_collision"
	// ReasonOutputFormatUnsupported marks a requested response format the
	// control protocol cannot publish without breaking the client's output
	// contract. The proxy synthesizes the published text from a tool argument,
	// so only plain-text contracts are eligible until format-aware publication
	// exists.
	ReasonOutputFormatUnsupported = "output_format_unsupported"
)

// Reassertion failures are candidate-fatal, not per-request best effort.
var (
	ErrProjectionInactive = errors.New("controltool: control projection is not active")
	ErrProjectionConflict = errors.New("controltool: control projection conflict")
)

// Projection is the trusted, in-process result of one initial projection. It is
// request/attempt-local owned data: never canonical Extensions/Invocation, never
// serialized into frontend or backend metadata, and never reconstructed from a
// tool name observed on the wire. All payload is deep-owned, so accessors hand
// out copies and callers cannot mutate the approved projection.
type Projection struct {
	active      bool
	reason      string
	itemAuth    bool
	tool        aipapi.ToolDef
	instruction Instruction
	toolChoice  aipapi.ToolChoice
	maxArgs     int
	// ordinaryTools is the deep-owned client tool catalog captured before the
	// control tool was appended. Reassertion proves the ordinary catalog is still
	// byte- and order-identical and that the control tool is its final append, so
	// a client tool name is never adopted as trusted ownership.
	ordinaryTools []aipapi.ToolDef
	// prefix is the small, deep-owned original instruction prefix the control
	// instruction was prepended to. It is proof, not history: user/assistant
	// history is never frozen.
	prefix instructionPrefix
}

// instructionPrefix is the bounded original instruction prefix behind the
// approved control instruction. Message authority owns the client's original
// instruction prefix; item authority owns the leading system/developer message
// items plus one stable anchor item, because without that anchor a changed
// control item and a legitimately reconstructed history are indistinguishable and
// reassertion could not fail closed.
type instructionPrefix struct {
	messages []aipapi.Message
	items    []aipapi.Item
}

// Active reports whether the control tool and instruction were approved for the
// projected candidate call.
func (p Projection) Active() bool { return p.active }

// Reason returns the bounded, content-free reason for an inactive projection
// and the empty string for an active one.
func (p Projection) Reason() string { return p.reason }

// ToolName returns the approved control tool name, or "" when inactive.
func (p Projection) ToolName() string { return p.tool.Name }

// MaxArgsBytes returns the approved bounded args budget for the control call, or
// 0 when inactive.
func (p Projection) MaxArgsBytes() int { return p.maxArgs }

// Tool returns a copy of the approved control tool definition with byte-stable
// schema bytes.
func (p Projection) Tool() aipapi.ToolDef { return cloneToolDef(p.tool) }

// Instruction returns a copy of the approved instruction.
func (p Projection) Instruction() Instruction { return p.instruction }

// ToolChoice returns a copy of the client tool choice that was approved, so a
// later reassertion can prove the constraint was never rewritten.
func (p Projection) ToolChoice() aipapi.ToolChoice { return cloneToolChoice(p.toolChoice) }

// Project computes the initial control projection for one candidate call. It
// first decides eligibility from the candidate's backend capabilities and the
// client tool-choice state, then clones the call and inserts exactly one control
// tool and one complete instruction into the backend-effective trajectory.
//
// Ineligibility is not an error: the candidate call is returned exactly as
// supplied, the client tool choice is never rewritten, and the projection
// carries the bounded reason. That includes an exact client declaration of the
// spec control instruction, which stays an untouched client instruction instead
// of being adopted. An invalid Spec is a programming/generation error: it is
// returned as an error and nothing is mutated.
//
// Project is the initial decision only. Because a same-named declaration is
// always a collision, re-projecting an already projected call reports
// ReasonToolNameCollision; every later projection point replays the approved
// projection with Reassert instead.
func Project(call aipapi.Call, spec Spec, caps aipapi.BackendCaps) (aipapi.Call, Projection, error) {
	if err := ValidateSpec(spec); err != nil {
		return call, Projection{}, err
	}
	if reason := eligibilityReason(call, spec, caps); reason != ReasonActive {
		return call, Projection{reason: reason}, nil
	}

	itemAuthority := call.HasItemAuthority()
	tool := cloneToolDef(spec.Tool)
	instruction := Instruction{Role: spec.Instruction.Role, Text: spec.Instruction.Text}

	out := cloneForProjection(call)
	prefix := freezeInstructionPrefix(out, itemAuthority)
	if itemAuthority {
		out.Items = prepend([]aipapi.Item{controlItem(instruction)}, out.Items)
	} else {
		out.Instructions = prepend([]aipapi.Message{controlMessage(instruction)}, out.Instructions)
	}
	out.Tools = append(out.Tools, cloneToolDef(tool))

	projection := Projection{
		active:        true,
		itemAuth:      itemAuthority,
		tool:          cloneToolDef(tool),
		instruction:   instruction,
		toolChoice:    cloneToolChoice(call.ToolChoice),
		maxArgs:       spec.MaxArgsBytes,
		ordinaryTools: frozenTools(call.Tools),
		prefix:        prefix,
	}
	return out, projection, nil
}

// Reassert replays an already approved projection after a legitimate final
// canonical reconstruction. It never re-decides eligibility and never rewrites
// client state: the approved tool choice is compared exactly, the ordinary tool
// catalog must still be the approved one with the control tool as its final
// append, and the approved control instruction must survive exactly once at the
// head of the trajectory with the frozen original instruction prefix intact
// behind it.
//
// A legitimate reconstruction that removed the control tool and/or the whole
// control prefix is restored, but only while the frozen ordinary catalog and
// original prefix prove the candidate is still the approved one. A duplicated,
// relocated, or altered control projection, and any ordinary tool catalog or
// instruction prefix that cannot be proved unchanged, fails closed.
func Reassert(call aipapi.Call, projection Projection) (aipapi.Call, error) {
	if !projection.active {
		return call, fmt.Errorf("%w: nothing was approved for this candidate", ErrProjectionInactive)
	}
	if !sameToolChoice(call.ToolChoice, projection.toolChoice) {
		return call, fmt.Errorf("%w: client tool choice changed after approval", ErrProjectionConflict)
	}
	if call.HasItemAuthority() != projection.itemAuth {
		return call, fmt.Errorf("%w: trajectory authority changed after approval", ErrProjectionConflict)
	}
	out := cloneForProjection(call)
	if err := reassertTool(&out, projection); err != nil {
		return call, err
	}
	if err := reassertInstruction(&out, projection); err != nil {
		return call, err
	}
	return out, nil
}

// eligibilityReason implements the V1 matrix: the smallest safe activation set.
// Unknown or malformed client constraints are reported as constraints and never
// rewritten, a same-named client tool is always a collision even when its
// definition happens to be byte-identical, and an already declared control
// instruction is an ambiguous ownership condition that is never adopted.
func eligibilityReason(call aipapi.Call, spec Spec, caps aipapi.BackendCaps) string {
	if _, ok := caps[aipapi.CapabilityTools]; !ok {
		return ReasonBackendToolsUnsupported
	}
	if !plainTextOutputContract(call.Options.ResponseMIMEType) {
		return ReasonOutputFormatUnsupported
	}
	choice := call.ToolChoice
	switch choice.Mode {
	case aipapi.ToolChoiceNone:
		return ReasonToolChoiceNone
	case aipapi.ToolChoiceAny:
		return ReasonToolChoiceConstrained
	case aipapi.ToolChoiceRequired:
		return ReasonToolChoiceRequired
	case "", aipapi.ToolChoiceAuto:
	default:
		return ReasonToolChoiceConstrained
	}
	if choice.Name != "" {
		// A named requirement is never broadened by a hidden tool, whether or
		// not the mode that carries it is canonically consistent.
		return ReasonToolChoiceRequired
	}
	if len(choice.AllowedTools) > 0 {
		return ReasonAllowedToolsConstrained
	}
	for _, tool := range call.Tools {
		if tool.Name == spec.Tool.Name {
			return ReasonToolNameCollision
		}
	}
	if declaresControlInstruction(call, spec.Instruction) {
		return ReasonInstructionCollision
	}
	return ReasonActive
}

// plainTextOutputContract reports whether the requested response MIME type is
// a plain-text contract the control protocol may publish into. An unset type
// is the ordinary prose contract. Any other type (for example,
// application/json) is unsupported: the published text is synthesized by the
// proxy from a tool argument rather than formatted by the model, so publishing
// it would silently break the client's expected payload.
func plainTextOutputContract(mime string) bool {
	mime = strings.TrimSpace(mime)
	if mime == "" {
		return true
	}
	if cut, _, _ := strings.Cut(mime, ";"); cut != mime {
		mime = strings.TrimSpace(cut)
	}
	switch strings.ToLower(mime) {
	case "text/plain":
		return true
	default:
		return false
	}
}

// declaresControlInstruction reports whether the client trajectory already
// declares the exact control instruction this spec would project. A declared
// instruction the proxy did not write is an ambiguous ownership condition: it
// must never be adopted as the trusted control instruction, and a second copy
// must never be appended, so activation stays conservative. The comparison is
// exact, never by text, so a role, part, metadata, or wording look-alike remains
// ordinary client data.
//
// Message authority owns two model-visible message containers, and a system or
// developer message carries the same instruction authority in Messages as it
// does in Instructions. The scan therefore covers both: a client-owned copy in
// the history is the same ambiguity as a client-owned copy in the instruction
// envelope, and prepending the approved control instruction next to it would
// leave the model with two copies of a control instruction the proxy never
// wrote.
func declaresControlInstruction(call aipapi.Call, instruction Instruction) bool {
	if call.HasItemAuthority() {
		want := controlItem(instruction)
		for _, item := range call.Items {
			if sameControlItem(item, want) {
				return true
			}
		}
		return false
	}
	want := controlMessage(instruction)
	return hasControlMessage(call.Instructions, want) || hasControlMessage(call.Messages, want)
}

// hasControlMessage reports whether one message container carries an exact copy
// of the approved control instruction anywhere in it. Control ownership never
// follows a position, so both the frozen instruction envelope and the mutable
// message history are scanned with the same exact predicate.
func hasControlMessage(messages []aipapi.Message, want aipapi.Message) bool {
	for _, message := range messages {
		if sameControlMessage(message, want) {
			return true
		}
	}
	return false
}

// freezeInstructionPrefix deep-owns the small original instruction prefix the
// control instruction was prepended to. It reuses the projection candidate copy
// path so a later caller mutation of the candidate call cannot reach the
// approved state.
func freezeInstructionPrefix(call aipapi.Call, itemAuthority bool) instructionPrefix {
	if itemAuthority {
		return instructionPrefix{items: frozenItemPrefix(call.Items)}
	}
	return instructionPrefix{messages: frozenMessages(call.Instructions)}
}

// frozenItemPrefix owns the leading system/developer message items plus the
// first following history item. The anchor is a bounded, stable position marker,
// not a history freeze: everything after it may be legitimately reconstructed.
func frozenItemPrefix(items []aipapi.Item) []aipapi.Item {
	end := instructionRun(items)
	if end < len(items) {
		end++
	}
	return frozenItems(items[:end])
}

func isInstructionItem(item aipapi.Item) bool {
	if item.Kind != aipapi.ItemKindMessage {
		return false
	}
	return item.Role == aipapi.RoleSystem || item.Role == aipapi.RoleDeveloper
}

// reassertTool replays the approved control tool against the frozen ordinary
// client tool catalog. Ownership never follows a name: the ordinary catalog must
// still be exactly the approved one, and the control tool must be its final
// append. A missing control tool is restored only while the ordinary catalog is
// unchanged; any other change fails closed.
func reassertTool(out *aipapi.Call, projection Projection) error {
	ordinary := projection.ordinaryTools
	switch {
	case len(out.Tools) == len(ordinary):
		if !sameToolCatalog(out.Tools, ordinary) {
			return fmt.Errorf("%w: the ordinary client tool catalog changed after approval", ErrProjectionConflict)
		}
		out.Tools = append(out.Tools, cloneToolDef(projection.tool))
		return nil
	case len(out.Tools) == len(ordinary)+1:
		if !sameToolCatalog(out.Tools[:len(ordinary)], ordinary) {
			return fmt.Errorf("%w: the ordinary client tool catalog changed after approval", ErrProjectionConflict)
		}
		if !sameToolDef(out.Tools[len(ordinary)], projection.tool) {
			return fmt.Errorf("%w: control tool %q is not the approved final append", ErrProjectionConflict, projection.tool.Name)
		}
		return nil
	default:
		return fmt.Errorf("%w: the client tool catalog is no longer the approved catalog plus the control tool", ErrProjectionConflict)
	}
}

// sameToolCatalog compares the ordinary client tool catalog byte- and
// order-identically. The caller established that both slices are equally long
// before comparing them.
func sameToolCatalog(got, want []aipapi.ToolDef) bool {
	for i := range want {
		if !sameToolDef(got[i], want[i]) {
			return false
		}
	}
	return true
}

// reassertInstruction replays the approved control instruction under the
// trajectory authority that was approved.
func reassertInstruction(out *aipapi.Call, projection Projection) error {
	if projection.itemAuth {
		want := controlItem(projection.instruction)
		// An originally empty continuation proved nothing about a later
		// trajectory, so a removed control item is safely restorable only while
		// that trajectory is still empty. With the control item retained, the
		// scope check below rejects any new leading instruction while ordinary
		// history behind it stays reconstructible.
		if len(projection.prefix.items) == 0 && len(out.Items) != 0 && !sameControlItem(out.Items[0], want) {
			return fmt.Errorf("%w: an originally empty item continuation cannot be rebuilt from %d candidate items", ErrProjectionConflict, len(out.Items))
		}
		items, err := replayControlInstruction(out.Items, projection.prefix.items, want, "control instruction item",
			func(item aipapi.Item) bool { return sameControlItem(item, want) },
			func(rest []aipapi.Item) bool { return sameItemScope(rest, projection.prefix.items) })
		if err != nil {
			return err
		}
		out.Items = items
		return nil
	}
	want := controlMessage(projection.instruction)
	// The message history stays the client's to reconstruct and is never
	// compared, but an exact copy of the approved control instruction is owned
	// control data wherever it appears: relocated out of the envelope or
	// duplicated into the history it is a second model-visible projection, so it
	// fails closed before anything is restored or replayed.
	if hasControlMessage(out.Messages, want) {
		return fmt.Errorf("%w: the control instruction also appears in the message history", ErrProjectionConflict)
	}
	messages, err := replayControlInstruction(out.Instructions, projection.prefix.messages, want, "control instruction",
		func(message aipapi.Message) bool { return sameControlMessage(message, want) },
		func(rest []aipapi.Message) bool { return sameMessageScope(rest, projection.prefix.messages) })
	if err != nil {
		return err
	}
	out.Instructions = messages
	return nil
}

// replayControlInstruction scans the whole trajectory and requires the approved
// control instruction to survive exactly once at the head, with the frozen
// original instruction scope intact directly behind it. A candidate that removed
// the control instruction is restored only when that scope proof holds; a
// duplicate, a relocation, or a scope that cannot be proved unchanged fails
// closed. Each authority supplies its own equivalence rule through scope, because
// instruction scope means different things for a message envelope and for a
// bounded item prefix.
func replayControlInstruction[T any](entries, prefix []T, want T, label string, matches func(T) bool, scope func([]T) bool) ([]T, error) {
	present := 0
	index := -1
	for i, entry := range entries {
		if !matches(entry) {
			continue
		}
		present++
		index = i
	}
	switch {
	case present > 1:
		return nil, fmt.Errorf("%w: %s is declared more than once", ErrProjectionConflict, label)
	case present == 1 && index != 0:
		return nil, fmt.Errorf("%w: %s is not the leading entry", ErrProjectionConflict, label)
	}

	rest := entries
	if present == 1 {
		rest = entries[1:]
	}
	if !scope(rest) {
		return nil, fmt.Errorf("%w: the original instruction scope behind the %s changed after approval", ErrProjectionConflict, label)
	}
	if present == 0 {
		return prepend([]T{want}, entries), nil
	}
	return entries, nil
}

// sameMessageScope is the message-authority equivalence rule. Every entry in
// Instructions is instruction scope, so the envelope behind the control
// instruction must still be the original one in length and content, including
// when the original envelope was empty. A new, dropped, or reordered instruction
// anywhere in it is semantic drift, and a missing control instruction is
// restored only after that exact comparison. The mutable Messages history is a
// separate authority and is deliberately not compared.
func sameMessageScope(rest, prefix []aipapi.Message) bool {
	return len(rest) == len(prefix) && sameMessages(rest, prefix)
}

func sameMessages(got, want []aipapi.Message) bool {
	for i := range want {
		if !reflect.DeepEqual(got[i], want[i]) {
			return false
		}
	}
	return true
}

// sameItemScope is the item-authority equivalence rule. The frozen scope is the
// client's complete leading system/developer run plus the optional first history
// anchor that followed it. The whole run must still be the original one in
// length and content, so a new leading instruction is drift even when the
// original run was empty or instruction-only. History after the bounded anchor is
// the client's to reconstruct, add, or remove, so it is never frozen. When the
// original scope carried no anchor, only the leading run is proof, and an
// originally empty continuation can be safely restored only while the candidate
// is still empty.
func sameItemScope(rest, prefix []aipapi.Item) bool {
	run := instructionRun(prefix)
	if instructionRun(rest) != run {
		return false
	}
	for i := range run {
		if !reflect.DeepEqual(rest[i], prefix[i]) {
			return false
		}
	}
	if run == len(prefix) {
		return true
	}
	return len(rest) > run && reflect.DeepEqual(rest[run], prefix[run])
}

// instructionRun reports the length of the leading system/developer message run:
// the bounded instruction scope item authority owns.
func instructionRun(items []aipapi.Item) int {
	end := 0
	for end < len(items) && isInstructionItem(items[end]) {
		end++
	}
	return end
}

// cloneForProjection copies the candidate call and restores every shape
// aipapi.CloneCall normalizes away: explicitly empty trajectory, tool,
// semantic-extension, and allowed-tools slices; explicitly empty client metadata
// maps; and empty-but-non-nil raw JSON carriers. Losing any of them would
// silently change trajectory, tool-choice, or client-metadata semantics, and a
// shared map or backing array would let a later caller mutate the input.
func cloneForProjection(call aipapi.Call) aipapi.Call {
	out := aipapi.CloneCall(call)
	out.Instructions = keepShape(out.Instructions, call.Instructions)
	out.Messages = keepShape(out.Messages, call.Messages)
	out.Items = keepShape(out.Items, call.Items)
	out.Tools = keepShape(out.Tools, call.Tools)
	out.SemanticExtensions = keepShape(out.SemanticExtensions, call.SemanticExtensions)
	out.ToolChoice.AllowedTools = keepShape(out.ToolChoice.AllowedTools, call.ToolChoice.AllowedTools)
	out.Extensions = cloneExtensions(call.Extensions)
	restoreMessageShapes(out.Instructions, call.Instructions)
	restoreMessageShapes(out.Messages, call.Messages)
	restoreItemShapes(out.Items, call.Items)
	restoreToolShapes(out.Tools, call.Tools)
	restoreSemanticExtensionShapes(out.SemanticExtensions, call.SemanticExtensions)
	return out
}

// keepShape returns the already cloned slice, or a fresh empty slice of its own
// whenever the input was explicitly empty. An explicitly empty input never needs
// its elements copied, and the replacement never shares the input backing
// array, so a later append cannot reach the input.
func keepShape[T any](cloned, original []T) []T {
	if original == nil || len(original) > 0 {
		return cloned
	}
	return make([]T, 0)
}

// keepCarrier returns the already cloned raw JSON, or an owned copy of the
// original bytes when the input carried an explicit empty carrier that the clone
// normalized away or passed through by slice header.
func keepCarrier(cloned, original json.RawMessage) json.RawMessage {
	if original == nil || len(original) > 0 {
		return cloned
	}
	return bytes.Clone(original)
}

// cloneExtensions owns the client extension map whenever the call carries one.
// aipapi.CloneCall only copies a non-empty map, so an explicitly empty map would
// otherwise stay shared with the input.
func cloneExtensions(in map[string]json.RawMessage) map[string]json.RawMessage {
	if in == nil {
		return nil
	}
	out := make(map[string]json.RawMessage, len(in))
	for key, value := range in {
		out[key] = bytes.Clone(value)
	}
	return out
}

func restoreMessageShapes(out, in []aipapi.Message) {
	for i := range min(len(out), len(in)) {
		out[i].Parts = keepShape(out[i].Parts, in[i].Parts)
		for j := range min(len(out[i].Parts), len(in[i].Parts)) {
			out[i].Parts[j].Content = keepCarrier(out[i].Parts[j].Content, in[i].Parts[j].Content)
		}
	}
}

func restoreItemShapes(out, in []aipapi.Item) {
	for i := range min(len(out), len(in)) {
		restoreItemShape(&out[i], in[i])
	}
}

func restoreItemShape(out *aipapi.Item, in aipapi.Item) {
	out.Content = keepShape(out.Content, in.Content)
	restoreContentPartShapes(out.Content, in.Content)
	if out.ToolCall != nil {
		out.ToolCall.Arguments = keepCarrier(out.ToolCall.Arguments, in.ToolCall.Arguments)
	}
	if out.ToolResult != nil {
		out.ToolResult.Parts = keepShape(out.ToolResult.Parts, in.ToolResult.Parts)
		restoreContentPartShapes(out.ToolResult.Parts, in.ToolResult.Parts)
	}
	if out.Compaction != nil {
		out.Compaction.Opaque = keepCarrier(out.Compaction.Opaque, in.Compaction.Opaque)
	}
	if out.Extension != nil {
		out.Extension.Data = keepCarrier(out.Extension.Data, in.Extension.Data)
	}
}

func restoreContentPartShapes(out, in []aipapi.ContentPart) {
	for i := range min(len(out), len(in)) {
		out[i].Annotation = keepAnnotation(out[i].Annotation, in[i].Annotation)
		out[i].Extension = keepContentExtension(out[i].Extension, in[i].Extension)
	}
}

func keepAnnotation(out, in *aipapi.AnnotationPart) *aipapi.AnnotationPart {
	if out != nil && in != nil {
		out.Data = keepCarrier(out.Data, in.Data)
	}
	return out
}

func keepContentExtension(out, in *aipapi.ExtensionContentPart) *aipapi.ExtensionContentPart {
	if out != nil && in != nil {
		out.Data = keepCarrier(out.Data, in.Data)
	}
	return out
}

func restoreToolShapes(out, in []aipapi.ToolDef) {
	for i := range min(len(out), len(in)) {
		out[i].Parameters = keepCarrier(out[i].Parameters, in[i].Parameters)
	}
}

func restoreSemanticExtensionShapes(out, in []aipapi.SemanticExtension) {
	for i := range min(len(out), len(in)) {
		out[i].Data = keepCarrier(out[i].Data, in[i].Data)
	}
}

// frozenTools, frozenMessages, and frozenItems own the approved provenance
// through the same candidate copy path, so the caller cannot reach the approved
// state by mutating the candidate call it passed in.
func frozenTools(in []aipapi.ToolDef) []aipapi.ToolDef {
	return cloneForProjection(aipapi.Call{Tools: in}).Tools
}

func frozenMessages(in []aipapi.Message) []aipapi.Message {
	return cloneForProjection(aipapi.Call{Instructions: in}).Instructions
}

func frozenItems(in []aipapi.Item) []aipapi.Item {
	return cloneForProjection(aipapi.Call{Items: in}).Items
}

func prepend[T any](prefix, existing []T) []T {
	out := make([]T, 0, len(prefix)+len(existing))
	out = append(out, prefix...)
	return append(out, existing...)
}

func controlMessage(in Instruction) aipapi.Message {
	return aipapi.Message{Role: in.Role, Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: in.Text}}}
}

func controlItem(in Instruction) aipapi.Item {
	return aipapi.Item{
		Kind:    aipapi.ItemKindMessage,
		Role:    in.Role,
		Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: in.Text}},
	}
}

func sameControlMessage(got, want aipapi.Message) bool {
	if got.Role != want.Role || got.Metadata != nil || len(got.Parts) != 1 {
		return false
	}
	part := got.Parts[0]
	return part.Kind == aipapi.PartText && part.Text == want.Parts[0].Text &&
		part.ImageRef == "" && part.ImageMIME == "" && part.FileRef == "" && part.FileMIME == "" &&
		part.FileName == "" && part.ToolCallID == "" && part.ToolName == "" &&
		len(part.Content) == 0 && part.Reasoning == nil
}

func sameControlItem(got, want aipapi.Item) bool {
	if got.Kind != want.Kind || got.Role != want.Role || got.ID != "" || got.Status != "" || got.Phase != "" {
		return false
	}
	if got.Reference != nil || got.ToolCall != nil || got.ToolResult != nil ||
		got.Reasoning != nil || got.Compaction != nil || got.Extension != nil {
		return false
	}
	return len(got.Content) == 1 && got.Content[0] == want.Content[0]
}

// sameToolDef compares a tool definition exactly, including the explicit
// nil-versus-empty shape of its schema bytes.
func sameToolDef(got, want aipapi.ToolDef) bool {
	return got.Name == want.Name && got.Description == want.Description &&
		(got.Parameters == nil) == (want.Parameters == nil) &&
		bytes.Equal(got.Parameters, want.Parameters)
}

// sameToolChoice compares the approved client tool choice exactly: the mode is
// never rewritten between an omitted mode and explicit auto, an explicitly empty
// allowed-tools subset is never treated as absent, and subset order matters.
// Reassertion proves the client's constraint survived untouched, so normalizing
// any of these representations would hide a real client-visible change.
func sameToolChoice(got, want aipapi.ToolChoice) bool {
	return got.Mode == want.Mode && got.Name == want.Name &&
		(got.AllowedTools == nil) == (want.AllowedTools == nil) &&
		equalOrderedStrings(got.AllowedTools, want.AllowedTools)
}

func equalOrderedStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range want {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// cloneToolDef owns the schema bytes, preserving the nil-versus-empty shape.
// bytes.Clone already returns nil for nil and an independent empty slice for an
// empty carrier.
func cloneToolDef(tool aipapi.ToolDef) aipapi.ToolDef {
	tool.Parameters = bytes.Clone(tool.Parameters)
	return tool
}

func cloneToolChoice(choice aipapi.ToolChoice) aipapi.ToolChoice {
	if choice.AllowedTools != nil {
		choice.AllowedTools = append([]string{}, choice.AllowedTools...)
	}
	return choice
}
