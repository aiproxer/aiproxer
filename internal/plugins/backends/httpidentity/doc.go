// Package httpidentity applies B-leg User-Agent identity policy at the final
// HTTP wire boundary for approved hosted connectors.
//
// It clones requests before mutation, resolves identity from
// [identity.FieldPolicy] plus optional call-scoped client UA in context, and
// never mutates the shared upstream client passed from the composition root.
package httpidentity
