package rewrite

// This file owns the reusable half of the payload engine: applying one compiled
// selector set to one canonical JSON payload and replacing the values the caller
// decides about, as a byte splice.
//
// It exists as an exported primitive for exactly one caller, the inbound expansion
// pass of design.md "7. Path Expansion Finalizer", and it exists because that pass
// must satisfy the SAME byte-for-byte property the outbound direction does. Task 4.1
// proved member order, duplicate keys, number spelling, escapes, whitespace, and
// empty-versus-null presence survive a rewrite because the mutation is a splice
// inside each selected string literal rather than a re-encode. Requirement 4.8 then
// asks the inbound direction for that same property. A second implementation of the
// splice would put it at the mercy of whichever copy drifted first, so there is one
// copy and both directions call it.
//
// What the primitive deliberately does NOT own is the decision. It never asks what a
// value means, never decides whether a value is a location, and never invents a
// refusal: the caller answers those per leaf through a ValueDecider, and this file
// only guarantees that exactly the leaves a compiled selector published are visited,
// in ascending byte order, exactly once each.
//
// The refusal semantics are the one thing it does enforce structurally, because
// getting them wrong is a security defect rather than a measurement drift: the FIRST
// refused leaf ends the pass and the payload is not published at all. A partially
// expanded argument document would hand the client one decided path beside one
// undecided alias, which is exactly the outcome design.md "Error Handling" forbids.

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization"
)

// PayloadOutcome is the bounded reason one payload pass did or did not reach its
// selected leaves.
//
// It is an enum because the value only ever reaches fixed-count observability
// dimensions and closed refusal vocabularies: it never carries path, tool-name,
// pointer, or payload bytes. The vocabulary is closed because the two callers
// project it onto their own closed sets and a state neither projects would have to
// be invented twice.
type PayloadOutcome uint8

const (
	// PayloadOutcomeReplaced marks a pass that read the payload, visited its
	// selected leaves, and published the result. Changed distinguishes a pass that
	// actually replaced something from one that visited leaves and replaced none.
	PayloadOutcomeReplaced PayloadOutcome = iota
	// PayloadOutcomeNoSelectors marks a pass handed an empty selector set, so no
	// location was selected and nothing was visited. It is the required answer for
	// a tool no policy layer claims (requirements.md 3.5, 3.8).
	PayloadOutcomeNoSelectors
	// PayloadOutcomeAbsent marks a payload field that carries no value at all: the
	// field is empty, or the JSON null literal. Neither spelling is materialized,
	// because no location exists to select (requirement 2.8's empty-versus-null
	// clause).
	PayloadOutcomeAbsent
	// PayloadOutcomeInvalid marks a payload that is not exactly one complete JSON
	// value. A payload that cannot be read whole is never read partially, which is
	// what lets an inbound caller distinguish "there is nothing here" from "there
	// may be anything here".
	PayloadOutcomeInvalid
	// PayloadOutcomeNotObject marks a payload that is present and readable but whose
	// root is not an object. A JSON Pointer can only name a member of an object, so
	// an array or scalar root has no selected location whatever was compiled.
	PayloadOutcomeNotObject
	// PayloadOutcomeNoLeaves marks a pass whose selectors all refused: every
	// compiled pointer either named nothing in this document or named a shape this
	// feature never rewrites. The per-pointer reasons are in DocumentPass.Selection.
	PayloadOutcomeNoLeaves
)

// String returns the fixed, low-cardinality label of an outcome. It is safe for
// content-free observability dimensions: it never contains path, tool-name,
// pointer, or payload bytes.
func (o PayloadOutcome) String() string {
	switch o {
	case PayloadOutcomeReplaced:
		return "replaced"
	case PayloadOutcomeNoSelectors:
		return "no_selectors"
	case PayloadOutcomeAbsent:
		return "payload_absent"
	case PayloadOutcomeInvalid:
		return "payload_invalid"
	case PayloadOutcomeNotObject:
		return "payload_not_object"
	case PayloadOutcomeNoLeaves:
		return "no_leaves"
	default:
		return "unknown"
	}
}

// ValueDecision is one selected leaf's answer.
//
// The three fields are deliberately independent: a caller reports what it
// accepted, what it would publish, and whether it must end the whole document, and
// this engine never infers one from another.
type ValueDecision struct {
	// Replacement is the value to publish in place of the leaf. It is read only
	// when Eligible is true, and a nil-looking refusal never publishes it.
	Replacement string

	// Eligible reports whether this leaf is a location the decision accepted. An
	// ineligible leaf keeps its own bytes and contributes no counters, which is how
	// a selected array can hold a mixture of replaced and untouched values without
	// losing either.
	Eligible bool

	// Refused reports that the document must not be published at all. It ends the
	// pass immediately and discards any replacement an earlier leaf produced.
	Refused bool
}

// ErrOutputOverLimit reports that a selected replacement would exceed the
// caller's output budget.
//
// It is returned instead of a partially built document, so a caller that must
// refuse an over-limit expansion never pays for the document it refuses. It is
// distinct from a decoder disagreement: the payload was readable, but
// publishing it would violate a size bound.
var ErrOutputOverLimit = errors.New("rewrite: selected replacement exceeds output budget")

// ValueDecider answers one selected leaf's decision from that leaf's decoded value.
//
// It is called exactly once per selected leaf, in ascending byte order, and never
// for a value no compiled pointer published. It must be deterministic and must
// perform no I/O: the value is untrusted model output and the answer reaches a
// client-facing decision.
type ValueDecider func(value string) ValueDecision

// DocumentPass is the content-free outcome of one payload pass.
//
// Every field is a count, a byte total, or a bounded code, so the whole value is
// safe to log, export, or use as a metric dimension (requirement 7.7). Byte totals
// are DECODED-VALUE lengths rather than raw wire lengths, because that is the length
// the model would otherwise see; a client that spelled a path with escapes therefore
// reports the same measurement as one that did not.
type DocumentPass struct {
	// Outcome is the bounded reason the pass did or did not reach its leaves.
	Outcome PayloadOutcome
	// Selection is the canonical selector layer's own resolution, forwarded
	// verbatim so both directions account for an unresolved location by the same
	// bounded reason instead of inventing their own.
	Selection pathvirtualization.Selection
	// Eligible counts the selected leaves the decider accepted.
	Eligible int
	// Replaced counts the eligible leaves whose published value differs from the
	// value the model sent. It is separate from Eligible so a measuring caller can
	// report eligibility without a mutation.
	Replaced int
	// BytesBefore is the total decoded length of the eligible values as the model
	// spelled them.
	BytesBefore int
	// BytesAfter is the total decoded length of the values this pass would publish.
	BytesAfter int
	// Changed reports whether the published bytes differ from the input. It is false
	// for every outcome that published nothing.
	Changed bool
	// Refused reports that a leaf ended the pass and the payload was not published.
	Refused bool
}

// ApplySelectedValues applies pointers to one canonical JSON payload and replaces
// every selected leaf through decide.
//
// It returns the published bytes only when something actually changed. Every other
// outcome, including a refusal, returns a nil payload, so a caller cannot publish a
// partially processed document by ignoring Changed.
//
// The only error it returns is a disagreement between two decoders over bytes that
// already decoded as one valid JSON value, which untrusted input cannot produce. It
// is reported rather than swallowed so a caller can apply its own failure policy -
// fail open with real paths on the outbound side, fail closed on the inbound side -
// instead of publishing a half-processed payload.
func ApplySelectedValues(
	raw []byte,
	pointers pathvirtualization.SelectorSet,
	decide ValueDecider,
) ([]byte, DocumentPass, error) {
	return applySelectedValues(raw, pointers, decide, 0)
}

// ApplySelectedValuesLimited is [ApplySelectedValues] with a bound on the
// published document.
//
// A positive maxOutputBytes aborts the splice with [ErrOutputOverLimit] as soon
// as the output provably exceeds it, instead of building a document the caller
// would then refuse; a non-positive value means no bound. The bound is on the
// published bytes, not on the input: a caller that must refuse an over-limit
// publication uses it to avoid paying for the document it refuses.
//
// The abort is driven by an exact lower bound on the finished size, so it fires
// only for documents that really are over the bound. It never rejects a document
// whose published size would have fit.
func ApplySelectedValuesLimited(
	raw []byte,
	pointers pathvirtualization.SelectorSet,
	decide ValueDecider,
	maxOutputBytes int,
) ([]byte, DocumentPass, error) {
	return applySelectedValues(raw, pointers, decide, maxOutputBytes)
}

func applySelectedValues(
	raw []byte,
	pointers pathvirtualization.SelectorSet,
	decide ValueDecider,
	maxOutputBytes int,
) ([]byte, DocumentPass, error) {
	var pass DocumentPass
	if len(pointers) == 0 {
		pass.Outcome = PayloadOutcomeNoSelectors
		return nil, pass, nil
	}
	if isAbsentOrNullPayload(raw) {
		pass.Outcome = PayloadOutcomeAbsent
		return nil, pass, nil
	}
	document, err := decodePayload(raw)
	if err != nil {
		pass.Outcome = PayloadOutcomeInvalid
		return nil, pass, nil
	}
	if _, object := document.(map[string]any); !object {
		pass.Outcome = PayloadOutcomeNotObject
		return nil, pass, nil
	}

	// The canonical selector layer decides which leaves are eligible and refuses
	// every location it cannot prove, so this step never guesses a shape.
	pass.Selection = pointers.Resolve(document)
	if len(pass.Selection.Leaves) == 0 {
		pass.Outcome = PayloadOutcomeNoLeaves
		return nil, pass, nil
	}

	spans, err := findSelectedStringSpans(raw, selectedLeafKeys(pass.Selection.Leaves))
	if err != nil {
		return nil, DocumentPass{Outcome: PayloadOutcomeInvalid}, fmt.Errorf("locate selected leaves: %w", err)
	}
	pass.Outcome = PayloadOutcomeReplaced
	published, changed, refused, err := splice(raw, spans, decide, &pass, maxOutputBytes)
	if err != nil {
		if errors.Is(err, ErrOutputOverLimit) {
			return nil, DocumentPass{Outcome: PayloadOutcomeReplaced}, err
		}
		return nil, DocumentPass{Outcome: PayloadOutcomeInvalid}, err
	}
	pass.Changed = changed
	pass.Refused = refused
	if refused || !changed {
		return nil, pass, nil
	}
	return published, pass, nil
}

// splice replaces the literals the spans cover and returns the published payload.
//
// Spans are applied in ascending offset order, copying the untouched bytes between
// them verbatim, so no replacement is written at an offset an earlier one moved and
// no two replacements can overlap: they are distinct literals of one document.
//
// It works in two passes, and the split is what makes the output bound affordable.
// The first pass DECIDES every leaf and measures the document; the second builds
// it, in one allocation of the measured size. Deciding is unavoidable - a leaf's
// replacement is only known once the decider has run - but building is not, so an
// over-limit document is refused for the cost of its decisions alone instead of
// for a progressively doubled output buffer it was always going to throw away.
//
// A refused leaf ends the pass with nothing published. That is the whole
// fail-closed contract, and it is enforced here rather than in each caller so the
// inbound and outbound directions cannot disagree about it.
func splice(
	document []byte,
	spans []stringSpan,
	decide ValueDecider,
	pass *DocumentPass,
	maxOutputBytes int,
) ([]byte, bool, bool, error) {
	ordered := slices.Clone(spans)
	slices.SortFunc(ordered, func(a, b stringSpan) int { return cmp.Compare(a.start, b.start) })

	// Pass one: decide every selected leaf once, in ascending byte order, and
	// accumulate the size of the document those decisions would produce.
	eligible := make([]bool, len(ordered))
	values := make([]string, len(ordered))
	total := 0
	cursor := 0
	changed := false
	for index, span := range ordered {
		if span.start < cursor || span.end > len(document) || span.start >= span.end {
			// Overlapping or out-of-range spans cannot come from one decoded
			// document, and writing them would corrupt the payload.
			return nil, false, false, errors.New("selected leaves overlap in the payload")
		}
		decision := decide(span.value)
		if decision.Refused {
			// Nothing is published, so a half-expanded payload cannot escape even
			// when an earlier leaf in the same document was acceptable.
			return nil, false, true, nil
		}
		total += span.start - cursor
		if decision.Eligible {
			// Eligibility is the decision's answer, not this step's: a value counts
			// only when the caller proved it is a location.
			eligible[index] = true
			values[index] = decision.Replacement
			pass.Eligible++
			pass.BytesBefore += len(span.value)
			pass.BytesAfter += len(decision.Replacement)
			if decision.Replacement != span.value {
				pass.Replaced++
				changed = true
			}
			// The decoded length is a floor, not the final length: the encoded
			// literal adds at least the two quotes, and possibly escapes. Under-
			// counting by a bounded per-leaf constant is safe, because the pass-two
			// check below is the authoritative one.
			total += len(decision.Replacement)
		} else {
			// A leaf the decider does not accept keeps its own bytes, so it cannot
			// grow the document at all. This is the case an average-growth estimate
			// got wrong: thousands of selected, inert leaves beside one alias that
			// grows, where multiplying that one growth across the rest of the array
			// predicts a document many times its real size and refuses a valid one.
			total += span.end - span.start
		}
		cursor = span.end
		if maxOutputBytes > 0 && total > maxOutputBytes {
			// The document is already provably over the caller's bound, so the
			// remaining leaves cannot bring it back under. Abort here rather than
			// visiting them: refusing an over-limit document never pays for deciding
			// the rest of it.
			return nil, false, false, ErrOutputOverLimit
		}
	}
	if !changed {
		return nil, false, false, nil
	}
	total += len(document) - cursor
	if maxOutputBytes > 0 && total > maxOutputBytes {
		return nil, false, false, ErrOutputOverLimit
	}

	// Pass two: build. Nothing above can grow the document past what was measured,
	// so this buffer is allocated once at its finished size.
	published := make([]byte, 0, total)
	cursor = 0
	for index, span := range ordered {
		replacement := document[span.start:span.end]
		if eligible[index] {
			literal, err := encodeSelectedValue(values[index])
			if err != nil {
				return nil, false, false, fmt.Errorf("encode selected value: %w", err)
			}
			replacement = literal
		}
		published = append(published, document[cursor:span.start]...)
		published = append(published, replacement...)
		cursor = span.end
	}
	published = append(published, document[cursor:]...)
	if maxOutputBytes > 0 && len(published) > maxOutputBytes {
		return nil, false, false, ErrOutputOverLimit
	}
	return published, true, false, nil
}

// virtualizeDeciders is the outbound direction's decider: it asks the mapping what
// a real-root prefix means and publishes the alias in its place.
func virtualizeDecider(mapping pathvirtualization.Mapping) ValueDecider {
	return func(value string) ValueDecision {
		virtualized, matched := mapping.VirtualizePath(value)
		return ValueDecision{Replacement: virtualized, Eligible: matched}
	}
}

// PublishedJSONValid reports whether one published payload is still exactly one
// complete JSON value.
//
// A splice of a decoded document cannot break the grammar, so this is a defensive
// invariant rather than a reachable branch, and it deliberately lives beside the
// splice that would be at fault. design.md "7. Path Expansion Finalizer" step 9
// makes validating the rewritten JSON an explicit step of the inbound algorithm,
// and requirement 8.5 asks for canonical validation to hold after every mutation: a
// rule that is only stated is a rule nobody can point at when it stops holding.
func PublishedJSONValid(published []byte) bool {
	return json.Valid(published)
}
