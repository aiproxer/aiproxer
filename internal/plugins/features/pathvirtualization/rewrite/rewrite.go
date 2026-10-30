package rewrite

// This file implements the call walk of design.md 231-252: the one pure step that
// both outbound passes share, over both canonical authorities.
//
// The walk is driven by the canonical surface shape, never by a flag a caller sets,
// and it reads the input while writing a published copy. That is what makes the step
// pure: a rewrite either changes nothing and republishes the call it was given, or it
// publishes a deep copy in which only selected payloads differ, and it never modifies
// a byte the caller owns.
//
// A tool name is the only authority that selects anything, and it is exact: the
// canonical resolver answers a name or nothing, and a near-miss spelling reaches no
// profile. A tool call additionally offers its declared argument schema to the
// optional inference step, so requirement 2.1's "safely inferred" half is reachable
// here, while a result surface never does, because a result has no declared schema
// and requirement 3.2 keeps result selectors explicit.
//
// A result's opaque text is the one place where the resolved policy can reach bytes
// no selector names, and it stays exact in the same way: only the opaque mode an
// exact profile declared can turn it on, and the recognizer that mode selects then
// proves line by line which bytes are locations. Every other opaque surface, and
// every opaque surface of every tool no profile claims, comes back unchanged.

import (
	"bytes"
	"encoding/json"

	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// Rewriter is the pure canonical outbound rewriter.
//
// It holds one workspace-bound mapping and one compiled profile policy, both
// immutable after construction, so a single rewriter may be shared by every request
// of a generation without synchronization. The zero value and a nil pointer are both
// inactive: they publish the call they were given unchanged.
type Rewriter struct {
	// mapping decides what a value means as a filesystem locator. It is the only
	// authority on whether a real-root prefix is present and ends on a segment
	// boundary, and it is the only place an alias is ever produced.
	mapping pathvirtualization.Mapping
	// resolver answers the exact-name profile precedence for one tool. Its nil
	// receiver is safe and publishes no selector, which is the answer for a tool no
	// layer claims.
	resolver *pathvirtualization.Resolver
	// mode decides whether a detected replacement is published or only measured
	// (requirement 7.2). It is read in exactly one place — the publication gate —
	// so the two modes cannot drift apart in what they detect (requirement 7.3).
	mode Mode
}

// New binds one mapping to one compiled profile policy.
//
// It cannot fail. Both arguments are values with no failure mode: a mapping whose
// alias is not shorter than its root simply leaves every surface unchanged, and a
// nil resolver publishes no selector, so a caller that has no policy yet binds a
// rewriter that is off rather than one that guesses.
//
// The rewriter it returns is in rewrite mode, so this is the measuring deployment's
// one-line change to audit; NewWithMode is the same binding with the mode explicit.
func New(mapping pathvirtualization.Mapping, resolver *pathvirtualization.Resolver) *Rewriter {
	return NewWithMode(mapping, resolver, ModeRewrite)
}

// RewriteCall returns the call with every selected path value replaced by its alias.
//
// The returned call is the input itself when nothing changed, and a new deep copy
// when something did; the input is never modified either way, and the published copy
// shares no payload byte with it. A caller therefore owns whatever it gets back and
// can keep using the call it passed in.
//
// In audit mode no replacement is ever published, so the returned call is always the
// input call while the statistics still report what a rewrite would have done to it
// (requirement 7.3). The measurement is the rewrite's own: this walk is identical in
// both modes, so the two cannot disagree about the same input.
//
// On an unexpected transformation failure the returned call is the input, the
// statistics are zero, and the error is non-nil. That is the fail-open contract the
// outbound pass needs: a request whose paths could not be rewritten must still reach
// the backend with real paths rather than not reach it at all.
func (r *Rewriter) RewriteCall(call *aipapi.Call) (*aipapi.Call, Stats, error) {
	if call == nil {
		return nil, Stats{}, nil
	}
	if r == nil {
		// An inactive rewriter is a no-op rather than a failure: the call is already
		// canonical and the statistics are empty.
		return call, Stats{}, nil
	}
	walk := &callWalker{rewriter: r, in: call}
	walk.run()
	if walk.err != nil {
		return call, Stats{}, walk.err
	}
	stats := walk.acc.stats()
	// All surface handlers build the same candidate. Only this boundary can
	// publish it, including wrapped arguments and legacy envelopes.
	if walk.out == nil || !walk.publishes() {
		return call, stats, nil
	}
	return walk.out, stats, nil
}

// callWalker carries one rewrite across a call: the input it reads, the published
// copy it writes on first change, the running accounting, the resolutions it has
// already made, and the first failure.
//
// The resolution memo is the only field here that grows with the input, and it is
// deliberately walk-local rather than rewriter-local: see resolve.go.
type callWalker struct {
	rewriter *Rewriter
	in       *aipapi.Call
	out      *aipapi.Call
	acc      account
	resolved map[resolutionKey]pathvirtualization.Resolved
	err      error
	// callIDsIndexed records that the historical call-ID index below has been
	// built for this walk. It is built lazily, on the first unnamed result,
	// so trajectories whose results all name their tools pay nothing for it.
	callIDsIndexed bool
	// callIDNames maps one historical call ID to the exact tool name the
	// trajectory attributes to it. A call ID claimed by two different names
	// is ambiguous and lives in callIDAmbiguous instead, never here.
	callIDNames map[string]string
	// callIDAmbiguous marks call IDs two different tool names claim. Either
	// name could be right, so neither is used and the result keeps its
	// pass-through.
	callIDAmbiguous map[string]bool
}

// working returns the published call, cloning the input on first use.
//
// Reads always come from the input and writes always go to the clone, so the two
// stay in step: a surface already rewritten is never read again, and every surface
// the clone does not receive is a byte-for-byte copy of the input's.
func (w *callWalker) working() *aipapi.Call {
	if w.out == nil {
		// The canonical clone is a deep copy, so the published call can never be
		// reached through a payload byte the input still owns.
		clone := aipapi.CloneCall(*w.in)
		w.out = &clone
	}
	return w.out
}

// run walks the call's authoritative surface once.
//
// The two authorities are exclusive by construction: canonical validation refuses a
// call that carries both ordered items and raw message parts, so the item authority
// is taken from the call itself and otherwise the legacy message parts are walked.
func (w *callWalker) run() {
	if w.rewriter.mapping.VirtualRoot == "" {
		// Requirement 1.4: an alias that does not shorten the root leaves outbound
		// virtualization inactive. The state belongs to the whole call, so it is
		// recorded once instead of once per surface.
		w.acc.skip(SkipReasonMappingInactive)
		return
	}
	if w.in.HasItemAuthority() {
		w.rewriteItems()
		return
	}
	w.rewriteMessages()
}

// rewriteItems walks ordered item authority.
func (w *callWalker) rewriteItems() {
	for i := range w.in.Items {
		if w.err != nil {
			// One failure ends the walk: a caller that fails open publishes the input
			// call, so a partially rewritten result could never escape.
			return
		}
		item := &w.in.Items[i]
		switch {
		case item.Kind == aipapi.ItemKindToolCall && item.ToolCall != nil:
			w.rewriteToolCallItem(item.ToolCall.Name, item.ToolCall.Arguments,
				func(raw []byte) { w.working().Items[i].ToolCall.Arguments = raw })
		case item.Kind == aipapi.ItemKindToolResult && item.ToolResult != nil:
			w.rewriteToolResult(i, item.ToolResult)
		}
		// Every other item kind is ordinary conversation content: a message, a
		// reference, reasoning, compaction, or an extension. This feature never
		// inspects or rewrites any of it (requirement 2.7).
	}
}

// rewriteMessages walks legacy message-part authority.
//
// Message role is deliberately not consulted. Anthropic delivers tool results inside
// a user message and Gemini function calls with no call ID, so role is not a
// reliable discriminator across dialects, while the part kind plus the canonical tool
// name is exactly what every frontend that produces these parts sets.
func (w *callWalker) rewriteMessages() {
	for i := range w.in.Messages {
		parts := w.in.Messages[i].Parts
		for j := range parts {
			if w.err != nil {
				return
			}
			part := parts[j]
			switch part.Kind {
			case aipapi.PartJSON:
				if part.ToolName == "" {
					// A supported tool-call envelope carries its own tool
					// identity and argument bytes inside the content; anything
					// else is assistant content or a reasoning payload, not a
					// tool call, and is left alone without a reason
					// (requirement 2.7).
					w.rewriteEnvelopePart(i, j, part)
					continue
				}
				w.rewriteToolCall(part.ToolName, part.Content,
					func(raw []byte) { w.working().Messages[i].Parts[j].Content = raw })
			case aipapi.PartToolResult:
				w.rewriteLegacyToolResult(i, j, part)
			}
		}
	}
}

// rewriteToolCall resolves one tool's policy and rewrites its argument payload.
func (w *callWalker) rewriteToolCall(toolName string, arguments []byte, assign func([]byte)) {
	// Only an argument surface offers its declared schema to the inference step: it
	// is the one surface the tool contract declares, and it is what makes a safely
	// inferred location reachable at all (requirement 2.1). The resolution itself is
	// memoized per pass, so a tool no profile claims does not have its declared
	// schema re-walked once per occurrence (requirements.md 9.3, 9.4).
	resolved := w.resolveToolCall(toolName)
	w.rewritePayload(arguments, resolved.ArgPointers, assign)
}

// rewriteToolCallItem rewrites one item-authority tool call's arguments.
//
// Canonical item arguments arrive in two spellings: a raw argument document,
// and a JSON string wrapping one (the pinned wire form, which is always a
// string). Passing the wrapped spelling straight to the selector engine
// decodes a string rather than an object, so every configured selector is
// skipped. Unwrap the string, rewrite the document it carries, and re-wrap the
// result; a document that needs no rewrite keeps its original bytes either way,
// so the wrapped and unwrapped spellings both stay byte-identical when
// untouched.
func (w *callWalker) rewriteToolCallItem(toolName string, arguments []byte, assign func([]byte)) {
	if w.err != nil {
		return
	}
	trimmed := bytes.TrimSpace(arguments)
	var inner string
	if len(trimmed) == 0 || trimmed[0] != '"' || json.Unmarshal(trimmed, &inner) != nil {
		w.rewriteToolCall(toolName, arguments, assign)
		return
	}
	// Only a string that itself spells a document takes the unwrap path. A bare
	// string payload is not a wrapped document, and sending it through document
	// decoding would misreport it as invalid rather than as the non-object it
	// is.
	innerTrimmed := bytes.TrimSpace([]byte(inner))
	if len(innerTrimmed) == 0 || (innerTrimmed[0] != '{' && innerTrimmed[0] != '[') {
		w.rewriteToolCall(toolName, arguments, assign)
		return
	}
	resolved := w.resolveToolCall(toolName)
	rewritten, changed, err := w.rewriter.rewriteDocument([]byte(inner), resolved.ArgPointers, &w.acc)
	if err != nil {
		w.err = err
		return
	}
	if !changed {
		return
	}
	rewrapped, err := marshalJSONString(rewritten)
	if err != nil {
		w.err = err
		return
	}
	assign(rewrapped)
}

// isFunctionCallEnvelopeKey reports whether a key may appear in a supported
// legacy tool-call envelope. Anything else is ordinary content that merely
// resembles an envelope, and reaching into it would violate requirement 2.7.
//
// It is a function rather than a package-level set because this feature
// declares no package-level mutable state: a set literal would be one more
// place a prior-root dictionary could hide.
func isFunctionCallEnvelopeKey(key string) bool {
	switch key {
	case "type", "id", "call_id", "name", "arguments":
		return true
	default:
		return false
	}
}

// functionCallEnvelope is the canonical tool-call envelope a frontend decoder
// produces for a wire function call: the part carries no tool name or call ID
// of its own, and the envelope's arguments spell the wire's JSON-string form.
type functionCallEnvelope struct {
	Type      string          `json:"type"`
	ID        string          `json:"id,omitempty"`
	CallID    string          `json:"call_id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// decodeFunctionCallEnvelope reports whether content is a supported tool-call
// envelope and, when it is, the tool identity and argument bytes it carries.
//
// The shape test is deliberately narrow - an object, the function_call
// discriminator, no unknown keys, a non-empty name and call ID, and present
// arguments - because this is the one place the walker reads a tool name out
// of a payload rather than off the part. A near-miss stays ordinary content.
func decodeFunctionCallEnvelope(content []byte) (functionCallEnvelope, bool) {
	var env functionCallEnvelope
	trimmed := bytes.TrimSpace(content)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return env, false
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &keys); err != nil {
		return env, false
	}
	for key := range keys {
		if !isFunctionCallEnvelopeKey(key) {
			return env, false
		}
	}
	if err := json.Unmarshal(trimmed, &env); err != nil {
		return env, false
	}
	if env.Type != "function_call" || env.Name == "" || env.CallID == "" {
		return env, false
	}
	if _, wrapped := unwrapJSONString(env.Arguments); !wrapped {
		return env, false
	}
	return env, true
}

// rewriteEnvelopePart rewrites one legacy tool-call envelope's arguments.
//
// The envelope's arguments spell the wire's JSON-string form, so they are
// unwrapped before selector resolution exactly like item arguments, and the
// rewritten document is re-wrapped into the envelope's string slot. The
// envelope keeps its shape - the part still carries no tool name of its own -
// and an envelope the rewrite leaves unchanged keeps its original bytes.
func (w *callWalker) rewriteEnvelopePart(message, part int, value aipapi.Part) {
	if w.err != nil {
		return
	}
	env, ok := decodeFunctionCallEnvelope(value.Content)
	if !ok {
		return
	}
	inner, _ := unwrapJSONString(env.Arguments)
	resolved := w.resolveToolCall(env.Name)
	rewritten, changed, err := w.rewriter.rewriteDocument(inner, resolved.ArgPointers, &w.acc)
	if err != nil {
		w.err = err
		return
	}
	if !changed {
		return
	}
	// Splice only the arguments string; keep outer whitespace, member order,
	// escaping, and unrelated identity literals exactly as received.
	selector, _ := pathvirtualization.ParseSelector("/arguments")
	out, pass, err := ApplySelectedValues(value.Content, pathvirtualization.SelectorSet{selector}, func(string) ValueDecision {
		return ValueDecision{Replacement: string(rewritten), Eligible: true}
	})
	if err != nil {
		w.err = err
		return
	}
	if pass.Changed && !pass.Refused {
		w.working().Messages[message].Parts[part].Content = out
	}
}

// unwrapJSONString reports whether raw is a JSON string and, when it is, the
// document it wraps. A non-string payload is returned unchanged with false, so
// callers that only special-case the wrapped spelling fall through to the
// ordinary path.
func unwrapJSONString(raw []byte) ([]byte, bool) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '"' {
		return raw, false
	}
	var inner string
	if err := json.Unmarshal(trimmed, &inner); err != nil {
		return raw, false
	}
	return []byte(inner), true
}

// marshalJSONString renders rewritten argument bytes back into the wire's
// JSON-string form, propagating a failure to the walk.
//
// It exists to mirror the item-argument re-wrap, which propagates the same
// operation's error into w.err. An earlier version of this helper answered a
// failure with the empty string instead, which contradicted its own stated policy
// ("failing the walk is the safe direction") and would have let the walk report
// SUCCESS while publishing an envelope whose arguments were entirely erased - and
// the outbound accounting would still have counted the replacement.
//
// The input is a document the engine just produced, so marshaling cannot fail
// today; that is precisely why the quiet direction is the wrong one to leave in
// place. A helper that cannot report failure is a helper whose failure path is
// unreviewable.
func marshalJSONString(raw []byte) (json.RawMessage, error) {
	out, err := json.Marshal(string(raw))
	if err != nil {
		return nil, err
	}
	return out, nil
}

// toolNameForCallID resolves an unnamed result's tool through the historical
// call carrying its call ID.
//
// The index is built lazily from this walk's own input - item tool calls,
// legacy tool-call parts, and supported tool-call envelopes - so it can never
// disagree with the trajectory being rewritten. A call ID claimed by two
// different tool names is ambiguous and resolves to nothing, and an unknown
// call ID resolves to nothing: both keep the result's pass-through rather
// than guessing a profile.
func (w *callWalker) toolNameForCallID(callID string) (string, bool) {
	if callID == "" {
		return "", false
	}
	if !w.callIDsIndexed {
		w.callIDsIndexed = true
		w.buildCallIDIndex()
	}
	if w.callIDAmbiguous[callID] {
		return "", false
	}
	name, ok := w.callIDNames[callID]
	return name, ok
}

// buildCallIDIndex records every exact tool name this trajectory attributes to
// a call ID. The walk's input is never modified, so the index is a read-only
// view of it; the entry count is bounded by the trajectory's own item and part
// counts, which canonical validation already bounds.
func (w *callWalker) buildCallIDIndex() {
	claim := func(callID, name string) {
		if callID == "" || name == "" {
			return
		}
		if w.callIDNames == nil {
			w.callIDNames = make(map[string]string)
		}
		if w.callIDAmbiguous == nil {
			w.callIDAmbiguous = make(map[string]bool)
		}
		if prev, seen := w.callIDNames[callID]; seen {
			if prev != name {
				w.callIDAmbiguous[callID] = true
				delete(w.callIDNames, callID)
			}
			return
		}
		w.callIDNames[callID] = name
	}
	for i := range w.in.Items {
		item := &w.in.Items[i]
		if item.Kind == aipapi.ItemKindToolCall && item.ToolCall != nil {
			claim(item.ToolCall.CallID, item.ToolCall.Name)
		}
	}
	for i := range w.in.Messages {
		for j := range w.in.Messages[i].Parts {
			part := &w.in.Messages[i].Parts[j]
			if part.Kind != aipapi.PartJSON {
				continue
			}
			if part.ToolName != "" {
				claim(part.ToolCallID, part.ToolName)
				continue
			}
			if env, ok := decodeFunctionCallEnvelope(part.Content); ok {
				claim(env.CallID, env.Name)
			}
		}
	}
}

// Its Output field and every text-bearing content part are opaque, so they reach only
// the bounded recognizer of the tool's declared mode and are otherwise left exactly
// as they arrived. A JSON content part is the structured surface requirement 2.2 and
// 3.2 address, and it is rewritten only through an explicitly selected location.
// rewriteToolResult handles the item-authoritative result surfaces.
func (w *callWalker) rewriteToolResult(index int, result *aipapi.ToolResultItem) {
	if w.err != nil {
		return
	}
	// A result has no declared schema, so nothing is offered to the inference step:
	// requirement 3.2 keeps structured result selection explicit. The memo entry is
	// therefore separate from the same name's argument-surface entry; see resolve.go.
	//
	// A result that names no tool resolves through its historical call ID when
	// exactly one call in this trajectory carries it. Decoders for several
	// frontends produce results with a call ID and no tool name; without the
	// lookup, configured structured-result and opaque-result rewriting silently
	// misses those histories.
	name := result.Name
	if name == "" {
		name, _ = w.toolNameForCallID(result.CallID)
	}
	resolved := w.resolveToolResult(name)
	if result.Output != "" {
		w.rewriteOpaque(result.Output, resolved.OpaqueResultMode,
			func(raw string) { w.working().Items[index].ToolResult.Output = raw })
	}
	for j := range result.Parts {
		part := result.Parts[j]
		switch part.Kind {
		case aipapi.ContentPartJSON:
			w.rewritePayload([]byte(part.Text), resolved.ResultJSONPointers,
				func(raw []byte) { w.working().Items[index].ToolResult.Parts[j].Text = string(raw) })
		case aipapi.ContentPartToolResult, aipapi.ContentPartText:
			if part.Text != "" {
				w.rewriteOpaque(part.Text, resolved.OpaqueResultMode,
					func(raw string) { w.working().Items[index].ToolResult.Parts[j].Text = raw })
			}
		default:
			// An image, file, or extension part carries no structured result
			// surface and no text payload, so there is nothing to select or to
			// recognize.
		}
	}
}

// rewriteLegacyToolResult handles the legacy result part surfaces.
//
// The part's Content is the structured result JSON a frontend decoded from a wire
// result object, so it is the structured surface this feature may rewrite, and its
// Text is an opaque payload a client or provider spelled as prose.
func (w *callWalker) rewriteLegacyToolResult(message, part int, value aipapi.Part) {
	if w.err != nil {
		return
	}
	name := value.ToolName
	if name == "" {
		name, _ = w.toolNameForCallID(value.ToolCallID)
	}
	resolved := w.resolveToolResult(name)
	if !isAbsentOrNullPayload(value.Content) {
		w.rewritePayload(value.Content, resolved.ResultJSONPointers,
			func(raw []byte) { w.working().Messages[message].Parts[part].Content = raw })
	}
	if value.Text != "" {
		w.rewriteOpaque(value.Text, resolved.OpaqueResultMode,
			func(raw string) { w.working().Messages[message].Parts[part].Text = raw })
	}
}

// rewritePayload applies one selector set to one payload and publishes the result.
//
// A surface with no selector at all is reported as such: that is the required answer
// for an unknown tool, and it is what keeps requirement 3.8's refusal of opaque
// rewriting for unknown tools visible without inspecting anything.
func (w *callWalker) rewritePayload(payload []byte, pointers pathvirtualization.SelectorSet, assign func([]byte)) {
	if w.err != nil {
		return
	}
	if len(pointers) == 0 {
		w.acc.skip(SkipReasonNoSelectors)
		return
	}
	rewritten, changed, err := w.rewriter.rewriteDocument(payload, pointers, &w.acc)
	if err != nil {
		w.err = err
		return
	}
	if changed {
		assign(rewritten)
	}
}

// rewriteOpaque applies the bounded recognizer of a declared opaque mode to one
// opaque result payload and publishes the result.
//
// The mode is the whole authority: it comes from the exact profile that claimed this
// tool, the disabled value rewrites nothing, and the recognizer itself decides which
// lines it can prove. A payload with no unambiguous location records its bounded
// reason inside the recognizer and is published unchanged.
func (w *callWalker) rewriteOpaque(text string, mode pathvirtualization.OpaqueResultMode, assign func(string)) {
	if w.err != nil {
		return
	}
	rewritten, changed := w.rewriter.rewriteOpaqueText(text, mode, &w.acc)
	if changed {
		assign(rewritten)
	}
}

// publishes is the single outbound publication policy. Undefined modes measure
// without publishing, just like audit.
func (w *callWalker) publishes() bool { return w.rewriter.mode == ModeRewrite }

// declaredSchema returns the declared argument schema of the exact-named tool, or
// nil when the call declares no such tool.
//
// The comparison is byte-exact, matching the authority the profile layers use, so a
// near-miss spelling never hands one tool's schema to the inference step on behalf of
// another. A tool that declares no schema gives the step nothing to prove, which is
// what keeps an unknown tool from acquiring selectors by accident.
func (w *callWalker) declaredSchema(toolName string) []byte {
	if toolName == "" {
		return nil
	}
	for _, tool := range w.in.Tools {
		if tool.Name == toolName {
			return tool.Parameters
		}
	}
	return nil
}
