// Package rewrite implements the canonical outbound rewriter of design.md 231-252
// for B-leg path virtualization: the one pure step that replaces a real project
// root inside a request with the deterministic V1 alias, on both canonical
// authorities, before the request reaches a backend.
//
// One rewriter serves every outbound pass. The candidate attempt transform and the
// idempotent request-part hook call the same function with the same mapping, so
// reapplication cannot drift from the first application and a payload published by
// one pass is a no-op for the next (requirement 2.9).
//
// The package exists as a subpackage of the lexical core for one reason: the core's
// own host-authority guard proves that its non-test sources import no repository
// package at all, and a rewriter has to read the canonical request types from
// pkg/aipapi. Keeping the canonical-facing step here preserves that guard instead of
// weakening it, exactly as the schema-assisted inference step does:
//
//   - the lexical core in internal/plugins/features/pathvirtualization keeps no
//     repository import, so path flavor parsing provably holds no operating-system,
//     separator, or filesystem authority;
//   - design.md 51-53 allows pkg/aipapi as a dependency of this feature, and
//     consuming the real canonical types is what makes the rewriter see the bytes a
//     provider actually declared instead of a re-typed approximation that could
//     drift from them.
//
// The rewrite itself is deliberately narrow. It parses one selected JSON surface,
// mutates only the string and string-array leaves a compiled selector chose, and
// replaces only a segment-boundary real-root prefix, which the lexical core's
// Mapping already decides. It never scans a payload for the real root, never
// descends into a selected object, and never touches a value no selector proved to
// be a filesystem locator (requirements 2.3, 2.4, 3.1, 3.2).
//
// Two authorities are handled by the same code, and the distinction is the canonical
// surface shape rather than a flag a caller sets:
//
//   - item authority, when Call.Items is non-nil: a ToolCallItem's Arguments and a
//     ToolResultItem's structured JSON content parts;
//   - legacy message parts, otherwise: a PartJSON part that carries a canonical tool
//     name, whose Content is that tool's argument document, and a PartToolResult
//     part, whose Content is structured result JSON while its Text is opaque.
//
// Message role never decides. Anthropic delivers tool results in a user message and
// Gemini function calls with no call ID at all, so the part kind plus the canonical
// tool name is the only discrimination every dialect satisfies, and it is the same
// discrimination the canonical capability detector uses. A PartJSON part with no
// canonical tool name is assistant content or reasoning, which this feature never
// inspects.
//
// Opaque result text is left exactly as it arrived unless an exact tool profile
// declared a bounded opaque-result mode. ToolResultItem.Output, a PartToolResult text
// payload, and a text content part all reach the same conservative recognizers, and
// each one either comes back unchanged or is re-spelled whole-line (requirement 2.5,
// 2.6). The rule those recognizers apply is one line of the design: a line is
// rewritten only when every whitespace- or comma-delimited token on it is a path
// this mapping accepts, so source, diffs, shell command lines, logs, stack frames,
// embedded JSON, and prose are all refused rather than searched. No shipped built-in
// declares a mode at all, which is why opaque rewriting is reachable only through an
// operator profile that has made that assertion about one exact tool name
// (design.md 249-250).
//
// The rewriter is pure. It performs no I/O, holds no mutable state, and never
// modifies the call it is given: it publishes a new call only when it changed
// something, and even then every payload byte it did not select is carried across
// unchanged, so member order, number spelling, escapes, and empty-versus-null
// presence all survive exactly as the client sent them (requirement 2.8).
//
// The rewriter also runs in audit mode, the measurement mode of requirements 7.2
// and 7.3. Audit is not a second implementation: it is this same pass with the
// publication step switched off, so candidate detection, selector resolution, the
// mapping decision, and the opaque recognizers all run unconditionally and reach
// the same verdict in both modes. That is deliberate, because requirement 9.5 asks
// for realized savings per request/turn, and a number is only a claim about a
// rewrite if the measuring code and the mutating code are the same code. In audit
// mode the caller's canonical request is returned exactly as it arrived and the
// published statistics still report every eligible occurrence, every occurrence
// that would be rewritten, and the byte totals before, after, and saved. Byte
// totals are the decoded-value lengths in both modes, because that is the length
// the model would otherwise see.
package rewrite
