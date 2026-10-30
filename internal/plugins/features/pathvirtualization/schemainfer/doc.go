// Package schemainfer implements the conservative schema-assisted selector
// inference step of design.md 197-229 for B-leg path virtualization.
//
// The step answers one question: which JSON Pointer locations inside a tool call's
// argument payload does that tool's own declared schema prove to be filesystem
// locators? It answers it from declared structure only, and it answers "none"
// whenever the declared structure is missing, unreadable, ambiguous, or larger than
// the feature's bounds. It never guesses a location from prose, from a name
// substring, or from a value it has not been given.
//
// The package is a schema-facing adapter. It is the one place in the feature that
// knows the canonical tool contract, so it consumes the real
// aipapi.ToolDef.Parameters bytes rather than a parallel schema type, and it hands
// back pointers validated by the lexical core in
// internal/plugins/features/pathvirtualization. Keeping the split this way is
// deliberate:
//
//   - The lexical core stays free of every repository import, so its own
//     host-authority guard can keep proving that path flavor parsing holds no
//     operating-system, separator, or filesystem authority. Schema reading is not
//     lexical path work, and it does not belong in that package.
//   - The canonical tool definition is a public contract, and design.md 51-53
//     allows pkg/aipapi as a dependency of this feature. Consuming the real field
//     is what makes the inference see the bytes a provider actually declared,
//     instead of a re-typed approximation that could drift from them.
//
// Nothing here reaches a request path yet: profile precedence, opaque-result modes,
// the canonical rewriter, and feature wiring are separate tasks. This package is a
// pure function over untrusted declared bytes, and every input class has exactly
// one bounded, content-free reason.
package schemainfer
