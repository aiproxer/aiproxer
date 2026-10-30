package expansion

// This file implements design.md "7. Path Expansion Finalizer" step for step.
//
// The eleven numbered steps of the design are, in this file's order:
//
//	 1. derive the current Mapping from meta.Workspace.ProjectRoot           -> decide
//	 2. resolve selectors using the exact tool name and current tool schema -> resolve
//	 3. if there are no selectors, pass                                     -> Finalize
//	 4. parse completed ArgsJSON                                            -> expand
//	 5. visit selected leaves only                                          -> expand
//	 6. parse a V1 reserved alias form before any expansion                 -> expandPath
//	 7. if the alias matches the current VirtualRoot, use the RealRoot      -> expandPath
//	 8. reject a malformed alias or a different tag/flavor/drive           -> expandPath
//	 9. validate the rewritten JSON and bound it against the canonical delta limit -> publish
//	10. the assembler synthesizes the canonical rewritten lifecycle         -> ActionRewrite
//	11. existing tool policies/reactors receive real paths                  -> design step 10
//
// Steps 6 through 8 are not re-implemented here. The lexical core's
// Mapping.ExpandPath already performs reserved-alias recognition BEFORE ordinary
// prefix matching and already answers with exactly the three states step 8 needs, so
// this file's decider is a projection of that answer onto the pass's own vocabulary
// and nothing more. That delegation is deliberate: a second reserved-alias decision
// is exactly the kind of drift that would let a stale alias rebind.
//
// Two properties are structural rather than checked:
//
//   - the mapping is DERIVED per call from the authoritative view and never cached, so
//     a changed root derives a different workspace tag on the very next call and an
//     alias a provider retained across the change fails closed (requirements.md 6.1,
//     6.5);
//   - the mutation is the shared byte splice, so requirement 4.8's "preserve JSON
//     validity and all non-selected argument fields" holds literally rather than by
//     re-encoding.
//
// The one place this file adds a rule of its own is the UNPARSEABLE-DOCUMENT
// REFUSAL, and it exists because design.md "Error Handling" demands it: when repair
// has declined to complete a malformed argument document, there is no structure left
// to walk and therefore no way to ask the selector layer which field was
// path-bearing. Releasing those bytes would release a possibly-alias-bearing argument,
// so a document that carries the fixed V1 reserved namespace is refused instead. The
// refusal is narrow on purpose - it needs the reserved marker at a segment boundary,
// the tool's own policy must name at least one argument location, and a document with
// no marker keeps requirement 4.7's pass-through - and the accepted residual it
// cannot avoid is documented on the decider itself.
//
// The second rule of its own is the OUTPUT BOUND of step 9. Syntax validity is the
// half of canonical validation that costs nothing to check; the size half is the half
// expansion itself can break, because the alias it substitutes is short by construction
// and the real root is not. The bound used is the runtime's own canonical delta limit
// rather than a limit invented here.

import (
	"context"
	"errors"
	"fmt"

	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization"
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/rewrite"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcall"
)

const (
	// FinalizerID is the stable identity this pass reports to the finalizer plane.
	//
	// It is a fixed string rather than a composed value so that a finalization
	// diagnostic, a refusal classification, and a metrics inventory all name the same
	// participant across builds and configuration changes. It carries no version,
	// workspace, tool name, or mode, so it stays low-cardinality.
	FinalizerID = "path-virtualization-expansion"

	// FinalizerOrder is this pass's position in the finalizer chain, which sorts
	// ascending by order, then identity, then registration index.
	//
	// It sits strictly above the shipped tool-call-repair default order, which is the
	// whole mechanism behind requirements.md 8.4's first clause. That clause asks
	// mandatory path expansion to receive VALID completed JSON, and numeric ordering
	// is the SDK's authoritative and only composition mechanism: the runtime
	// materializes the chain with toolcall.MaterializeSorted and then iterates it, and
	// no core rule reorders finalizers. Declaring above repair is therefore what makes
	// repair's rewrite reach this pass instead of the raw fragments.
	//
	// Ordering is NECESSARY BUT NOT SUFFICIENT, and the reason is stated here so no
	// later reader mistakes it for the whole fix: repair's order is an operator key
	// with no upper bound, so a generation can configure repair to sort after this
	// pass. The unparseable-document refusal below is what makes expansion
	// unconditional in that configuration, which is why it exists rather than trusting
	// the number. Task 9.1 additionally rejects publication of such a generation.
	FinalizerOrder = 41
)

// Policy is the construction-time configuration of one expansion pass.
//
// It is a value with no decode path and no struct tags: decoding the operator's
// spelling is Task 9.1's surface, and keeping this type free of tags is what lets the
// requirement 7.4 guard keep proving the feature publishes no namespace and version
// knob of its own.
//
// The rollout mode is NOT a field here. It is a separate constructor argument, and
// that is deliberate: [rewrite.Mode]'s zero value is audit, so a mode field would make
// the zero Policy silently mean "measure only" - and a pass that never expands is the
// exact failure requirements.md 4.1 and 4.4 exist to prevent. Making the mode
// unmissable at the call site is the same choice the outbound rewriter makes when it
// spells out ModeRewrite rather than relying on a zero value.
type Policy struct {
	// MandatoryMaxArgsBytes is the declared completeness requirement: the largest
	// completed tool call whose whole arguments this pass needs in order to decide.
	// Zero selects toolcall.DefaultMandatoryMaxArgsBytes. Any other value must lie
	// inside the configurable range, which toolcall.BufferingSpec.Validate is the
	// single definition of, and an out-of-range value fails generation compilation
	// rather than silently degrading a mandatory requirement into optional handling
	// (requirement 7.5).
	//
	// Task 9.1 owns decoding the operator's `mandatory_max_args_bytes` key into this
	// field and is required to reject an unsupported value there rather than let a
	// composition root reach this constructor with one.
	MandatoryMaxArgsBytes int
}

// Finalizer is the feature's completed-tool-call path expansion pass.
//
// One instance is safe to share across every request of a generation: it holds one
// immutable resolver and one immutable declared bound, derives everything else per
// call, and keeps no per-request state. The nil pointer is also safe and passes every
// call through unchanged, matching the shared resolver's own nil-receiver behaviour.
type Finalizer struct {
	// resolver is the compiled exact-name profile policy, or nil when the generation
	// published no policy at all. A nil resolver selects nothing for any tool, which is
	// the required answer for an unproved surface (requirements.md 3.5, 3.8).
	resolver *pathvirtualization.Resolver
	// mode is the rollout mode this generation was compiled with. It is decided at
	// configuration time and is immutable here, so no request can observe a different
	// mode from another.
	mode rewrite.Mode
	// spec is the declared mandatory completeness requirement the assembler reads. It
	// is stored exactly as validated so the value the assembler sees is the value this
	// pass validated, and neither can drift.
	spec toolcall.BufferingSpec
	// report receives this pass's bounded, content-free outcome. It may be nil, in
	// which case the pass records nothing and still performs the whole decision.
	report Reporter
}

var (
	_ toolcall.Finalizer            = (*Finalizer)(nil)
	_ toolcall.BufferingRequirement = (*Finalizer)(nil)
	// Without this assertion the pass would silently keep the assembler-wide
	// interpretation of its declared bound, and every test in this package would
	// still pass because none of them assembles the pass into a real core
	// assembler. The capability is only real once something checks it.
	_ toolcall.BufferingApplicability = (*Finalizer)(nil)
)

// NewFinalizer builds the feature's completed-tool-call expansion pass.
//
// The mode is a required argument rather than a field, for the reason [Policy] states:
// the safe direction for this pass is to expand, so no zero value may silently turn it
// into a measurement pass. Pass [rewrite.ModeAudit] to measure without mutating
// (requirement 7.3) and [rewrite.ModeRewrite] to expand. Any value this build does not
// define fails closed toward measuring, which is the shared engine's own rule.
//
// The resolver may be nil, which means "no policy published". That is not an error,
// because the answer it produces - no selector for any tool - is the same answer a
// tool no layer claims produces, and requirement 3.5 requires exactly that rather than
// a failure.
//
// It DOES fail for a mandatory bound outside the configurable range. The declared
// bound is a completeness requirement: silently accepting an unusable one would
// downgrade a mandatory requirement into optional handling, which requirements.md 4.5,
// 4.6, and 8.3 all forbid, and which the assembler cannot detect on its own because a
// malformed declaration deliberately reports itself as absent. Failing here makes the
// unusable configuration a generation-compilation error (requirement 7.5), which is
// where an operator can still fix it.
func NewFinalizer(
	resolver *pathvirtualization.Resolver,
	mode rewrite.Mode,
	policy Policy,
	opts ...Option,
) (*Finalizer, error) {
	bound := policy.MandatoryMaxArgsBytes
	if bound == 0 {
		bound = toolcall.DefaultMandatoryMaxArgsBytes
	}
	// Measurement must not enforce. Audit mode needs the same complete document
	// to detect and measure, so it declares the same bound; past that bound it
	// declares pass-through rather than refusal, and it declares the bound
	// BEST-EFFORT rather than binding, so the requirement can never refuse a
	// call. Both halves are load-bearing. The pass-through policy alone is not
	// enough, because a binding requirement refuses the call whenever it is
	// still undecided - including when some UNRELATED finalizer fails first,
	// which would make turning measurement on into a new hard rejection of
	// traffic it was only ever meant to watch (requirements.md 7.3).
	//
	// Declaring no requirement at all would also stop enforcement, but it would
	// shrink the measurement window from this configured bound back to the
	// shared assembly default, so audit would silently never see the large calls
	// an operator most wants measured. Only rewrite mode binds.
	overflow := toolcall.OverflowPassThrough
	completeness := toolcall.CompletenessBestEffort
	if mode == rewrite.ModeRewrite {
		overflow = toolcall.OverflowReject
		completeness = toolcall.CompletenessMandatory
	}
	spec := toolcall.BufferingSpec{MaxArgsBytes: bound, Overflow: overflow, Completeness: completeness}
	if err := spec.Validate(); err != nil {
		return nil, fmt.Errorf("path_virtualization: invalid mandatory expansion bound: %w", err)
	}
	fin := &Finalizer{resolver: resolver, mode: mode, spec: spec}
	for _, opt := range opts {
		if opt != nil {
			opt(fin)
		}
	}
	return fin, nil
}

// ID implements toolcall.Finalizer. See [FinalizerID].
//
// The nil receiver answers the same identity, so a composition root that holds a nil
// pass still names one stable participant.
func (*Finalizer) ID() string { return FinalizerID }

// Order implements toolcall.Finalizer. See [FinalizerOrder].
//
// The nil receiver answers the same order, so a composition root that holds a nil pass
// still sorts where every other participant expects it.
func (*Finalizer) Order() int { return FinalizerOrder }

// ToolCallBufferingRequirement implements toolcall.BufferingRequirement.
//
// The declaration is read during composition, so it is derived from immutable
// construction state only and never from request state: the same call must produce the
// same bound on every request of a generation, or the assembler's effective assembly
// limit would move underneath it.
func (f *Finalizer) ToolCallBufferingRequirement() toolcall.BufferingSpec {
	if f == nil {
		// The nil pass still declares what the shipped pass declares, so a composition
		// root that holds a nil finalizer assembles the complete document rather than
		// degrading to the legacy pass-through.
		return toolcall.BufferingSpec{
			MaxArgsBytes: toolcall.DefaultMandatoryMaxArgsBytes,
			Overflow:     toolcall.OverflowReject,
		}
	}
	return f.spec
}

// Finalize implements toolcall.Finalizer: design.md "7. Path Expansion Finalizer".
//
// It returns no Go error on any input. Every condition it can meet is a bounded reason
// on a Result, because the assembler's own error path replays the ORIGINAL fragments,
// which is precisely the outcome requirements.md 4.4 and 8.3 forbid for a call this
// pass applies to. Reporting a decision rather than an error also keeps a payload byte
// out of any error text.
//
// The result shape is always one of:
//
//   - ActionPass with a bounded reason and no arguments: requirements.md 4.7's
//     preserved behavior, covering no usable root, no selectors, nothing to expand, and
//     audit mode;
//   - ActionRewrite with the expanded document: requirements.md 4.1's success, which the
//     assembler turns into the canonical rewritten lifecycle;
//   - ActionReject with a bounded reason and no arguments: requirements.md 4.4's
//     fail-closed refusal, which releases nothing at all.
func (f *Finalizer) Finalize(
	_ context.Context,
	call toolcall.CompletedCall,
	tool aipapi.ToolDef,
	catalog []aipapi.ToolDef,
	meta toolcall.Meta,
) (toolcall.Result, error) {
	if f == nil {
		// An inactive pass is a no-op rather than a failure: the call is already
		// canonical and this feature has nothing to say about it.
		return passResult(ReasonNoSelectors), nil
	}
	// Requirements.md 8.5 and design.md section 7 step 9: the document this pass
	// publishes must still be exactly one complete JSON value, because the assembler
	// validates every finalizer's rewrite before the next finalizer sees it and before
	// anything is released. The check runs BEFORE the single report, so one decision
	// produces exactly one report whatever it turns out to be; see
	// [decision.withValidatedPublication].
	decision := f.decide(call, tool, meta).
		withOverDeclaredBound(len(call.ArgsJSON) > f.spec.MaxArgsBytes).
		withValidatedPublication()
	// Audit retains the hypothetical decision for reporting, but cannot publish
	// either a rewrite or a refusal to the runtime.
	if !f.publishes() {
		if decision.reason == ReasonExpanded {
			decision = decision.withReason(ReasonAuditMode).withPublished(nil)
		}
		f.record(decision)
		return passResult(decision.reason), nil
	}
	f.record(decision)
	if decision.reason.rejects() {
		// A refusal publishes nothing: no arguments, no tool name, no document. The
		// assembler turns the bounded reason into a client-facing refusal, and the
		// original fragments are dropped rather than replayed.
		return toolcall.Result{Action: toolcall.ActionReject, ReasonCode: decision.reason.String()}, nil
	}
	if decision.published == nil {
		return passResult(decision.reason), nil
	}
	return toolcall.Result{
		Action:     toolcall.ActionRewrite,
		ToolName:   call.ToolName,
		ArgsJSON:   decision.published,
		ReasonCode: decision.reason.String(),
	}, nil
}

// decision is one call's complete verdict: the bounded reason, the document to
// publish when there is one, the engine's content-free statistics, and the
// mandatory-buffer observation.
//
// It is a value so a decision can be re-labelled once - which is what the
// invalid-rewrite branch does - without recomputing anything.
type decision struct {
	reason     Reason
	rootReason pathvirtualization.SkipReason
	published  []byte
	stats      rewrite.Stats
	// overDeclaredBound is the [Report.ArgsOverDeclaredBound] observation. It is
	// carried on the decision rather than computed in record so the comparison happens
	// exactly once per call, next to the pass's other per-call observations.
	overDeclaredBound bool
}

// withOverDeclaredBound returns the decision carrying the mandatory-buffer observation.
//
// The comparison is against this pass's OWN declared bound, not against a constant: the
// bound is a validated constructor argument (requirement 7.5), so a generation that
// configured one compares against the number its operator chose. An absent ArgsJSON is
// not an overflow - a call with no arguments is smaller than every bound in the
// configurable range, and treating it as one would report an overflow that cannot exist.
func (d decision) withOverDeclaredBound(over bool) decision {
	d.overDeclaredBound = over
	return d
}

// withReason returns the same decision carrying a different bounded reason.
func (d decision) withReason(reason Reason) decision {
	d.reason = reason
	return d
}

// withValidatedPublication returns the decision as it will be both RECORDED and acted
// on, applying design.md section 7 step 9's canonical-validation requirement
// (requirements.md 8.5) before either.
//
// The ordering is the whole point of this step. A document that fails validation must
// be re-labelled here rather than at the point of refusal, because recording first
// would make one decision produce TWO reports: outcome() derives OutcomeExpanded from
// a non-nil published document, so the first report would publish a call that is
// released as nothing as a successful expansion - with its eligible, rewritten, and
// byte figures - and the second would publish the refusal it actually was. That is
// the one-report-per-decision contract on Reporter violated in the fail-closed cases,
// which are exactly the accounting an operator has to be able to trust.
//
// A decision that publishes nothing is left alone: there is no document to validate,
// and a pass-through reason must reach the client as itself.
func (d decision) withValidatedPublication() decision {
	if d.published == nil || rewrite.PublishedJSONValid(d.published) {
		return d
	}
	return d.withReason(ReasonInvalidRewrite)
}

// decide performs design.md section 7 steps 1 through 9 and returns the verdict.
//
// Each branch below is one step, and the ordering is the design's: the mapping is
// derived before anything is inspected, the selectors are resolved before the document
// is read, and the document is read before any leaf is visited.
func (f *Finalizer) decide(call toolcall.CompletedCall, tool aipapi.ToolDef, meta toolcall.Meta) decision {
	// Step 1. The authoritative workspace view is the ONLY authority read here. It is
	// the same view the outbound attempt transform reads for the same turn, and it is
	// derived per call rather than cached, which is what keeps every retry, race
	// participant, and failover candidate of one logical turn on the same alias
	// (requirements.md 5.6, 6.2) and what makes a changed root take effect
	// immediately (requirement 6.5).
	mapping, rootReason := pathvirtualization.DeriveMapping(meta.Workspace.ProjectRoot)
	if rootReason != pathvirtualization.SkipReasonNone {
		// design.md "Error Handling": an unsupported or malformed project root means no
		// mapping and no mutation. This pass-through is safe rather than merely
		// convenient, and the reason is worth stating because it is the one place a
		// reserved alias can reach the client:
		//
		//   - the authoritative workspace view is projected from the same per-turn
		//     request snapshot the OUTBOUND attempt transform reads, and both planes
		//     consume one frozen view. When it is absent, the outbound pass had no
		//     project root either and therefore minted no alias, so no alias can be in
		//     flight to expand;
		//   - an unusable root that IS present is a root this build cannot parse, so no
		//     alias of the fixed V1 derivation could have been minted from it either -
		//     every alias spells a root the derivation accepted.
		//
		// A model could still EMIT a reserved alias on its own initiative. Where
		// aliases came from is not the question requirement 4.4 asks: it asks what
		// happens when a path-bearing argument DOES carry the reserved namespace and no
		// mapping can resolve it, and the answer is fail closed.
		//
		// Selector applicability comes before any whole-payload refusal. A tool with
		// no argument selector is outside this pass whether or not its payload
		// happens to spell the marker in an ordinary content field, so it keeps
		// requirement 4.7/4.9 pass-through as ReasonNoSelectors. Only a tool with a
		// selected argument surface can have a path-bearing argument that requirement
		// 4.4 governs.
		pointers := f.resolver.Resolve(call.ToolName, declaredSchema(tool, call.ToolName)).ArgPointers
		if len(pointers) == 0 {
			return decision{reason: ReasonNoSelectors, rootReason: rootReason}
		}
		// A reserved-namespace COLLISION root is deliberately exempt. There the project
		// root really does occupy the reserved namespace, requirement 1.8 disables
		// virtualization for it, and a path beneath it is an ordinary real path rather
		// than an undecidable alias, so refusing would break a supported deployment
		// instead of protecting one.
		if rootReason == pathvirtualization.SkipReasonReservedNamespaceCollision {
			return decision{reason: ReasonRootUnusable, rootReason: rootReason}
		}
		// With selectors established, inspect only the selected leaves when the
		// payload can be read. An alias-shaped string in an unselected content,
		// patch, script, or note field of a selected tool is not a path-bearing
		// argument, so it must not fail the call. An unparseable payload leaves no
		// structure to read; then the whole-payload marker is the only answer, and
		// that accepted residual is documented on the unreadable-document branch.
		published, pass, err := rewrite.ApplySelectedValues(call.ArgsJSON, pointers, reservedNamespaceLeafDecider)
		_ = published
		if err != nil {
			if pathvirtualization.ScanReservedAlias(call.ArgsJSON) == pathvirtualization.ReservedAliasAbsent {
				return decision{reason: ReasonRootUnusable, rootReason: rootReason}
			}
			return decision{reason: ReasonWorkspaceMismatch, rootReason: rootReason}
		}
		switch pass.Outcome {
		case rewrite.PayloadOutcomeInvalid:
			if pathvirtualization.ScanReservedAlias(call.ArgsJSON) == pathvirtualization.ReservedAliasAbsent {
				return decision{reason: ReasonRootUnusable, rootReason: rootReason}
			}
			return decision{reason: ReasonWorkspaceMismatch, rootReason: rootReason}
		case rewrite.PayloadOutcomeNoSelectors:
			return decision{reason: ReasonNoSelectors, rootReason: rootReason}
		case rewrite.PayloadOutcomeAbsent, rewrite.PayloadOutcomeNotObject, rewrite.PayloadOutcomeNoLeaves:
			return decision{reason: ReasonRootUnusable, rootReason: rootReason}
		case rewrite.PayloadOutcomeReplaced:
			if pass.Refused {
				return decision{reason: ReasonWorkspaceMismatch, rootReason: rootReason}
			}
			return decision{reason: ReasonRootUnusable, rootReason: rootReason}
		default:
			return decision{reason: ReasonWorkspaceMismatch, rootReason: rootReason}
		}
	}

	// Step 2. Selector resolution reads the tool name this call ACTUALLY carries and
	// the CURRENT declared schema for it, and nothing else.
	//
	// Do not reconcile this byte-exact choice with the assembler's canonical one;
	// they answer different questions and are meant to disagree. The assembler asks
	// "which tool is this?" to size the buffering window, so it canonicalizes, and
	// widening that window fails safe. This step asks "which tool's SELECTORS govern
	// these bytes?", which is a policy question, so it stays byte-exact. See
	// [toolCallAssembler.deriveCallRequirements] for the assembler's side.
	//
	// The name is resolved BYTE-EXACTLY, with no pre-normalization, because this is
	// the security boundary: a near-miss spelling must not inherit another tool's
	// profile. Canonicalizing here would let a call named `read-file` reach
	// `read_file`'s selectors and share its policy purely because the catalog
	// happens to make that normalization unique - widening the set of calls this
	// feature governs from "the exact tools an operator named" to "any name
	// tool-call repair could hypothetically have rewritten", which requirements.md
	// 3.6 forbids and which no operator configuration asked for.
	//
	// Repair does not need this pass to anticipate it. Repair runs FIRST
	// (DefaultFinalizerOrder 40 against expansion's FinalizerOrder 41) and returns
	// the normalized name on its rewrite result, which the assembler installs
	// before the next finalizer is invoked. So by the time this step runs,
	// call.ToolName already IS the repaired name whenever repair repaired
	// anything, and it is the original name whenever repair declined. The TOCTOU
	// this pass used to close by canonicalizing is closed by that ordering instead,
	// which keeps the authority on the name that really reached expansion.
	//
	// Only the ARGUMENT selectors are read. The resolution's structured-result
	// pointers and its bounded opaque-result mode are deliberately ignored: a
	// completed tool call carries arguments, and a result payload is model-visible
	// content this feature must not reach for (requirements.md 2.5, 4.9). Reading
	// them here would be the only way this pass could ever touch prose.
	resolved := f.resolver.Resolve(call.ToolName, declaredSchema(tool, call.ToolName))
	pointers := resolved.ArgPointers

	// Step 3. No selector means nothing was proven path-bearing, so nothing is
	// inspected at all. This is requirements.md 3.5's and 3.8's required answer for an
	// unknown tool, and it is what keeps a content, patch, or script field out of this
	// feature's reach unless an operator named its exact field (requirement 4.9).
	if len(pointers) == 0 {
		return decision{reason: ReasonNoSelectors}
	}

	// Steps 4 and 5, plus the unreadable-document refusal. The shared engine parses the
	// completed document, visits selected leaves only, and never publishes anything when
	// a leaf refuses.
	visit := &leafVisitor{mapping: mapping}
	published, pass, err := rewrite.ApplySelectedValuesLimited(call.ArgsJSON, pointers, visit.decide, aipapi.MaxEventDeltaBytes)
	if err != nil {
		if errors.Is(err, rewrite.ErrOutputOverLimit) {
			// Refuse an over-limit candidate without building it. Finalize
			// reports this hypothetical refusal but passes in audit mode.
			return decision{reason: ReasonExpandedTooLarge}
		}
		// The engine's error is a decoder disagreement over bytes that already decoded
		// as one valid JSON value, which untrusted input cannot produce. Fail closed:
		// releasing the originals would release possibly alias-bearing arguments
		// (requirements.md 4.4, 8.3).
		return decision{reason: ReasonArgsUnparseable}
	}
	accounted := decision{stats: pass.Stats()}

	switch pass.Outcome {
	case rewrite.PayloadOutcomeInvalid:
		// Step 4 could not complete. This is the one state where no selector layer can
		// run, so "which field was path-bearing" is unanswerable, and the answer has to
		// be given without it. It is given by the marker: if these bytes spell the fixed
		// V1 reserved namespace anywhere, they may be an alias this pass failed to
		// expand, and they may not reach the client.
		//
		// The scan is over the WHOLE payload because there is no structure left to read.
		// That is an accepted residual: a reserved alias spelled inside a content field
		// of an UNREADABLE document is refused even though requirement 4.9 forbids
		// expanding such a field. Nothing is expanded, so 4.9 is not violated; the call
		// is refused. The residue is unreachable in correct client data, because
		// requirement 1.8 refuses to derive a mapping from a root spelled inside the
		// reserved namespace, so a real path can never carry this byte pattern.
		//
		// The refusal is conditional on the tool's own policy naming at least one
		// argument location, which is this pass's test for "a call path virtualization
		// applies to" (requirements.md 4.6). It is answerable here because selector
		// resolution reads the profile layer and the declared schema, never the payload.
		if pathvirtualization.ScanReservedAlias(call.ArgsJSON) == pathvirtualization.ReservedAliasAbsent {
			return accounted.withReason(reasonForNoAlias(mapping))
		}
		return accounted.withReason(ReasonArgsUnparseable)
	case rewrite.PayloadOutcomeAbsent:
		return accounted.withReason(ReasonArgsAbsent)
	case rewrite.PayloadOutcomeNotObject:
		return accounted.withReason(ReasonPayloadNotObject)
	case rewrite.PayloadOutcomeNoSelectors:
		return accounted.withReason(ReasonNoSelectors)
	case rewrite.PayloadOutcomeNoLeaves:
		// Every compiled pointer refused, so no selected location exists in this
		// document and therefore no location can hold an alias.
		return accounted.withReason(reasonForNoAlias(mapping))
	case rewrite.PayloadOutcomeReplaced:
		if pass.Refused {
			// Steps 6 through 8: a selected leaf spelled the reserved namespace and could
			// not be mapped to the current workspace. The engine discarded every
			// replacement, so nothing is published and the whole call fails closed.
			return accounted.withReason(visit.refusalReason())
		}
		if !pass.Changed {
			// Every selected leaf was visited and none carried this mapping's alias.
			return accounted.withReason(reasonForNoAlias(mapping))
		}
		// Step 9's SIZE half. The assembler publishes this document as ONE canonical
		// tool-call args delta, and canonical event validation bounds that delta, so a
		// document that fits the declared argument bound is still not automatically
		// publishable: expansion substitutes the real root for the alias in every
		// selected leaf at once, and the root is longer than the alias by construction.
		// Publishing an over-limit document would hand the assembler a lifecycle the
		// runtime itself rejects, and the call would fail downstream of the feature that
		// produced it.
		//
		// The bound is the runtime's own canonical constant rather than a number chosen
		// here, so this check and the validator it exists for cannot drift apart. It runs
		// before publication. Finalize applies the rollout gate, so audit records
		// the same decision without failing anything.
		//
		// Refusing is the only safe answer. A partial expansion would release one decided
		// path beside one undecided alias (requirements.md 4.4), and passing the original
		// document through would release the very namespace this pass exists to expand
		// (requirements.md 4.1).
		if len(published) > aipapi.MaxEventDeltaBytes {
			return accounted.withReason(ReasonExpandedTooLarge)
		}
		return accounted.withReason(ReasonExpanded).withPublished(published)
	default:
		// An outcome outside the closed vocabulary is treated as the safe direction: a
		// caller must never be able to release a possibly alias-bearing argument by
		// supplying a value this build does not define.
		return accounted.withReason(ReasonArgsUnparseable)
	}
}

// publishes reports whether a detected replacement may reach the client.
//
// Only ModeRewrite publishes, which is the shared engine's own fail-closed rule
// reused rather than restated: a value this build does not define measures rather than
// mutates, so a misconfigured generation cannot publish an expansion under an
// unstated policy.
func (f *Finalizer) publishes() bool { return f.mode == rewrite.ModeRewrite }

// withPublished returns the decision carrying the document to publish.
func (d decision) withPublished(published []byte) decision {
	d.published = published
	return d
}

// reservedNamespaceLeafDecider answers one selected leaf on the unusable-root
// branch: refuse exactly when that leaf's decoded value carries the reserved
// namespace, accept everything else without publishing.
//
// It inspects the decoded leaf value rather than the whole payload, so an
// alias-shaped string in an unselected content, patch, script, or note field of
// an otherwise selected tool does not fail the call. A malformed and a
// well-formed answer both refuse, because both mean a namespace is present that
// no mapping can resolve.
func reservedNamespaceLeafDecider(value string) rewrite.ValueDecision {
	if pathvirtualization.ScanReservedAlias([]byte(value)) == pathvirtualization.ReservedAliasAbsent {
		return rewrite.ValueDecision{}
	}
	return rewrite.ValueDecision{Refused: true}
}

// leafVisitor is the decider the shared engine calls once per selected leaf, plus the
// one thing the engine's boolean refusal cannot carry: WHY a leaf refused.
//
// It is created per call and never shared, so the recorded refusal cannot leak between
// two tool calls even when one finalizer instance serves every request of a
// generation.
type leafVisitor struct {
	// mapping is the workspace-bound translation this call derived.
	mapping pathvirtualization.Mapping
	// refusal is the FIRST refusal this call recorded, as the lexical core's own closed
	// answer. The first one is kept because the pass refuses the whole document anyway,
	// so every later refusal would only add a second reason to the same bounded label.
	refusal pathvirtualization.ExpandResult
}

// decide answers one selected leaf.
//
// Steps 6, 7, and 8 in one projection: the lexical core recognizes a reserved alias
// form BEFORE ordinary prefix matching, parses its flavor, drive, and tag, and answers
// with exactly the three states step 8 names. This decider adds nothing to that answer;
// it only turns a refusal into the engine's refusal signal and an accepted value into
// the engine's eligible replacement.
//
// Note what it does NOT do: it never inspects a value for the reserved marker outside the
// mapping's own recognition, and it never expands a value the mapping did not accept. A
// selected leaf holding ordinary content, or a real path outside the virtual root, keeps
// its own bytes and continues into existing tool policy unchanged (design.md "Security
// Considerations").
func (v *leafVisitor) decide(value string) rewrite.ValueDecision {
	expanded, result := v.mapping.ExpandPath(value)
	switch result {
	case pathvirtualization.ExpandResultExpanded:
		return rewrite.ValueDecision{Replacement: expanded, Eligible: true}
	case pathvirtualization.ExpandResultNotApplicable:
		// Not a location of this mapping, so it keeps its own bytes. Eligibility is the
		// mapping's answer, never this decider's.
		return rewrite.ValueDecision{}
	case pathvirtualization.ExpandResultMalformedReservedAlias, pathvirtualization.ExpandResultWorkspaceMismatch:
		if v.refusal == pathvirtualization.ExpandResultNotApplicable {
			v.refusal = result
		}
		return rewrite.ValueDecision{Refused: true}
	default:
		// An answer outside the core's closed vocabulary fails closed rather than
		// releasing a value the core could not classify, and is reported under the
		// stale-workspace label because that is the refusal the caller must treat as
		// "do not expand this against the current root".
		if v.refusal == pathvirtualization.ExpandResultNotApplicable {
			v.refusal = pathvirtualization.ExpandResultWorkspaceMismatch
		}
		return rewrite.ValueDecision{Refused: true}
	}
}

// refusalReason projects the core's closed expansion answer onto this pass's own
// bounded refusal labels.
//
// The projection is total over the core's closed vocabulary, so no reserved alias the
// core refuses can reach the client under a label that reads as a pass-through.
func (v *leafVisitor) refusalReason() Reason {
	switch v.refusal {
	case pathvirtualization.ExpandResultMalformedReservedAlias:
		return ReasonMalformedReservedAlias
	default:
		return ReasonWorkspaceMismatch
	}
}

// reasonForNoAlias names the no-op reason for a call that needs no expansion.
//
// A usable root whose alias is not strictly shorter leaves virtualization inactive for
// that root (requirement 1.4), which is a property of the deployment rather than of
// this call, so the two are reported separately: requirement 7.6 accounts for them
// differently and a deployment that only ever sees no_alias would never learn its
// virtualization was inert.
func reasonForNoAlias(mapping pathvirtualization.Mapping) Reason {
	if mapping.VirtualRoot == "" {
		return ReasonMappingInactive
	}
	return ReasonNoAlias
}

// declaredSchema returns the declared argument schema to offer the inference step, or
// nil when the supplied tool definition is not this call's tool.
//
// The comparison is byte-exact, matching the authority the profile layers use, so a
// near-miss spelling never hands one tool's schema to the inference step on behalf of
// another (requirement 3.6). A tool that declares no schema gives the step nothing to
// prove, which is what keeps an unknown tool from acquiring selectors by accident.
// ToolCallBufferingApplies implements [toolcall.BufferingApplicability].
//
// This pass declares a mandatory completeness requirement because it has to see a
// WHOLE argument document before it can decide: the reserved namespace is
// recognised lexically across the payload, so a document truncated at any point
// can hide an alias behind the cut. That need is real only for calls this pass
// actually acts on.
//
// Without this answer the assembler had to assume the requirement governed every
// call it was invoked for, because it reads this BEFORE the arguments are
// complete and cannot wait to find out. So enabling path virtualization refused
// unrelated oversized tool calls outright, which requirements 4.6 and 4.7 forbid:
// a call carrying no path surface has to keep its existing pass-through.
//
// The answer is derived from the tool name, the tool definition, and the catalog
// only, exactly as the contract requires, and it is the SAME question step 3 of
// [Finalizer.decide] asks - "does this tool have at least one argument location
// this pass reads?" - so the bound that gets buffered and the pass that consumes
// it cannot disagree about which calls they cover.
//
// A nil resolver selects nothing, so a nil pass applies to nothing. That is the
// narrow answer, and it is safe here for the same reason it is safe in decide:
// with no selectors this pass would answer ReasonNoSelectors and hand the call
// through untouched anyway.
func (f *Finalizer) ToolCallBufferingApplies(toolName string, tool aipapi.ToolDef, _ []aipapi.ToolDef) bool {
	if f == nil || f.resolver == nil {
		return false
	}
	// Use the same exact-name authority as decide. Speculative normalization
	// would attach binding requirements to calls this finalizer does not govern.
	return len(f.resolver.Resolve(toolName, declaredSchema(tool, toolName)).ArgPointers) > 0
}

func declaredSchema(tool aipapi.ToolDef, toolName string) []byte {
	if tool.Name != toolName || toolName == "" {
		return nil
	}
	return tool.Parameters
}

// passResult builds the pre-existing pass-through result for one bounded reason.
func passResult(reason Reason) toolcall.Result {
	return toolcall.Result{Action: toolcall.ActionPass, ReasonCode: reason.String()}
}

// record hands one bounded report to the configured reporter.
//
// The call is skipped entirely for an absent reporter, so a deployment that wants no
// observability pays nothing and a nil reporter can never be a nil-pointer hazard on
// the hot path.
func (f *Finalizer) record(d decision) {
	if f == nil || f.report == nil {
		return
	}
	f.report(Report{
		Outcome:               d.outcome(),
		Reason:                d.reason,
		RootReason:            d.rootReason,
		Stats:                 d.stats,
		ArgsOverDeclaredBound: d.overDeclaredBound,
	})
}

// outcome maps a bounded reason onto the pass-level shape it implies.
//
// It is derived rather than stored so the two vocabularies cannot disagree about what
// happened to the call, in either direction.
func (d decision) outcome() Outcome {
	switch {
	case d.reason.rejects():
		return OutcomeRejected
	case d.published != nil:
		return OutcomeExpanded
	default:
		return OutcomeNoop
	}
}

// Option configures one expansion pass at construction.
type Option func(*Finalizer)

// WithReporter installs the sink that receives this pass's bounded reports.
//
// It is optional: a pass built without one still performs the whole decision and
// simply records nothing. Making the sink an explicit construction parameter rather
// than package state is what keeps one instance safe to share across every request of a
// generation, and keeps this package free of any observable mutable state: no init()
// registration, no globals.
//
// A reporter may be invoked concurrently, so an implementation that keeps counters must
// be safe for concurrent use.
func WithReporter(reporter Reporter) Option {
	return func(f *Finalizer) { f.report = reporter }
}

// Reporter receives one content-free report per expansion decision.
//
// It is declared here, by the consumer, so this package never depends on the feature's
// telemetry, metrics, or diagnostics machinery: requirement 9.3 wires the real bounded
// counters, and this pass only has to make the outcome and the reason reachable and
// content-free. An absent reporter is legal and simply discards the record.
type Reporter func(Report)
