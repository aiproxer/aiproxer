// Package backendplugin defines the public authoring, validation, and wire-conversion
// contracts for executable backend connector plugins.
//
// Types in this package do not import internal/... packages. Wire messages live in
// api/backendplugin/v1. Domain protocol negotiation is independent of the process substrate.
//
// Task 1.2 covers contracts, validation, and DTO↔proto conversion only. Live servers,
// process hosting, manifests, and conformance executables are later tasks.
package backendplugin
