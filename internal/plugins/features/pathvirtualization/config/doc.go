// Package config is the operator configuration surface of the B-leg path
// virtualization feature.
//
// It is the feature's whole decode, validation, and compilation layer, and it
// owns three properties that the rest of the feature depends on:
//
//   - DISABLED BY DEFAULT. An absent subtree, an empty document, an explicit
//     null, an empty mapping, and an `enabled: false` mapping all compile to an
//     inert Resolved that carries no resolver and no bound, so a stock
//     deployment constructs no attempt transform, no request-part hook, and no
//     expansion finalizer (requirements.md 7.1).
//   - FAIL-CLOSED VALIDATION. Every bound is decided by the layer that already
//     defines it, and any refusal rejects the WHOLE configuration, so no
//     partially validated profile, half-resolved vocabulary, or clamped
//     byte bound is ever published (requirements.md 7.5, 7.9).
//   - CONTENT-FREE DIAGNOSTICS. A rejection carries a closed reason label and a
//     fixed configuration location and nothing else; the published Resolved
//     carries bounded counts and compiled policy and no operator text at all
//     (requirements.md 7.7, 7.8).
//
// WHY THIS IS A SUBPACKAGE AND NOT THE LEXICAL CORE. The parent package holds
// the pure cross-platform path policy and imports no repository package, which
// its own host-authority guard proves by AST inspection of every production
// source. A configuration decoder necessarily imports the SDK, the selector
// compiler, the inference step, and the expansion policy, so it cannot live
// there. The same reasoning already moved the rewriter, the outbound passes, the
// expansion finalizer, and the inference step into sibling subpackages.
//
// WHAT THE COMPOSITION ROOT CALLS. Three entry points, in order of how much a
// caller needs to know:
//
//	featurePathVirtualization(n) -> config.Decode(n)      // decode + compile
//	disabled check               -> config.DecodeConfig(n) // typed subtree
//	cross-feature guard          -> config.ValidateGenerationComposition(regs)
//
// The third one is the only symbol here that needs to see more than this
// feature's own subtree, because its condition spans two registrations. See its
// own file for why it is not a core rule and why it cannot live in a subtree.
//
// The package holds no state: no globals, no init() registration, no caches, and
// no mutexes. Every exported value is immutable or is returned by value, so one
// compiled configuration is safe to share across every request of a generation.
package config
