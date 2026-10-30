package rewrite

// This file holds the one piece of state a rewrite pass keeps: a memo of the policy
// resolutions it has already made.
//
// It exists because of a measured production defect, not a theoretical one. Resolving a
// tool's policy is free for a tool a profile claims, and expensive for a tool none claims:
// the schema-assisted inference step re-reads and re-walks that tool's DECLARED argument
// schema, which is bounded at a quarter of a megabyte. A walk that resolved per OCCURRENCE
// therefore cost occurrences x declared-schema-size instead of occurrences, and at 1000
// occurrences of one unclaimed tool that measured 3.0x the time and 3.6x the allocations of
// a claimed name (223.0 against 62.0 allocations per occurrence). Requirements 9.3 and 9.4
// exist to bound exactly that cost, so the fix belongs here rather than in a caller.
//
// Three properties make it safe, and all three are structural rather than conventional:
//
//	THE MEMO IS PER PASS. It is a field on the walk, and the walk exists for one
//	    RewriteCall. Nothing on the Rewriter holds it, so one generation-shared rewriter
//	    cannot carry one request's answers into the next. Requirement 2.3 forbids a mutable
//	    store of the ROOT mapping; a cache of derived policy answers would be the same
//	    hazard one level down, and this is deliberately not that.
//
//	THE DECLARED SCHEMA IS A FUNCTION OF THE NAME WITHIN A PASS. The schema comes from the
//	    walk's own input, which the walk never modifies, so two occurrences of one name are
//	    handed byte-identical input to the resolution and would reach a byte-identical
//	    answer. That is what makes reusing the answer a memo rather than a guess, and it is
//	    why the key can be a name.
//
//	THE TWO RESOLUTION SITES STAY SEPARATE. A tool call offers the name's declared schema
//	    and a tool result offers none, so those are two DIFFERENT inputs to the same
//	    function and therefore two different answers for one name. Keying the memo on the
//	    name alone would hand a result surface the resolution built from a schema it never
//	    offered. The key therefore records which input a site offered rather than relying
//	    on today's result surfaces happening not to read the fields that differ.
//
// The bound is on the number of distinct names one walk may cache, and it is a memory
// guarantee rather than a correctness one: past it, a name is still resolved, it is simply
// resolved again next time. Dropping an entry can therefore only cost the repeated work
// this file exists to remove.

import (
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// maxMemoizedResolutions is how many distinct resolution inputs one walk may cache.
//
// It is the canonical item authority's own occurrence cap ([aipapi.MaxItems]), which is the
// largest number of tool-call surfaces the authoritative model permits a single call to
// carry. A walk over such a call with a distinct name per surface therefore caches
// everything it resolved, so the bound costs a legitimate request nothing; a call that
// exceeds it can only do so on the LEGACY message authority, whose part count is a product
// of two envelope limits rather than a single one, and where an unbounded memo would be
// sized by attacker-chosen tool names.
//
// The memory it permits is a function of that constant and of the canonical tool-name bound
// ([aipapi.MaxToolNameBytes]): at most one small record and one bounded name per entry.
const maxMemoizedResolutions = aipapi.MaxItems

// resolutionKey identifies one memoized resolution.
//
// The schema is not carried in the key because it is not a free input: within one walk it
// is determined by the name (see the file comment), so the name plus which input the site
// offered identifies the call exactly.
type resolutionKey struct {
	// name is the exact canonical tool name the resolution was asked about.
	name string
	// offersDeclaredSchema records that the site passed the name's declared argument
	// schema, as a tool-call surface does. A result surface passes no schema and so keys
	// separately from the same name's call surface.
	offersDeclaredSchema bool
}

// resolve answers one surface's policy question, consulting the resolution at most once
// per distinct input in this walk.
//
// declaredSchema is exactly the argument the caller would have handed
// [pathvirtualization.Resolver.Resolve] had it resolved directly, so removing the repeated
// resolution cannot change what any surface selects.
func (w *callWalker) resolve(toolName string, declaredSchema []byte) pathvirtualization.Resolved {
	key := resolutionKey{name: toolName, offersDeclaredSchema: len(declaredSchema) > 0}
	if resolved, memoized := w.resolved[key]; memoized {
		return resolved
	}
	resolved := w.rewriter.resolver.Resolve(toolName, declaredSchema)
	// The map is created on first use so a call that resolves one name pays for one
	// entry, and a call that resolves nothing - the nil mapping, the inactive alias - pays
	// for nothing at all.
	if w.resolved == nil {
		w.resolved = make(map[resolutionKey]pathvirtualization.Resolved)
	}
	if len(w.resolved) < maxMemoizedResolutions {
		w.resolved[key] = resolved
	}
	return resolved
}

// resolveToolCall answers a tool-call argument surface's policy question.
//
// It is the surface that offers its declared schema, which is what makes a safely inferred
// location reachable at all (requirements.md 2.1, 3.3).
func (w *callWalker) resolveToolCall(toolName string) pathvirtualization.Resolved {
	return w.resolve(toolName, w.declaredSchema(toolName))
}

// resolveToolResult answers a tool-result surface's policy question.
//
// A result has no declared schema, so nothing is offered to the inference step:
// requirement 3.2 keeps structured result selection explicit. The separate entry point
// exists so that "no schema" is stated at the call site rather than implied by the schema
// lookup happening to come back empty.
func (w *callWalker) resolveToolResult(toolName string) pathvirtualization.Resolved {
	return w.resolve(toolName, nil)
}
