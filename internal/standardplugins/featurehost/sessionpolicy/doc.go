// Package sessionpolicy provides the standard featurehost-owned, bounded
// policy store for terminal-decision session overrides. It retains only scoped
// opaque identity and actor tri-state values; concrete feature semantics
// remain in plugins, while request admission consumes an immutable snapshot.
package sessionpolicy
