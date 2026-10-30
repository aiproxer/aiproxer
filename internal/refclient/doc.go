// Package refclient provides official-SDK-based reference client emulators for
// integration and conformance tests. They are test-support utilities only: they must
// not be imported from the core runtime (internal/core) or protocol codec plugins.
//
// Each subpackage wraps the vendor’s published Go client so requests and response
// parsing follow the same shapes as real applications. Normative HTTP/API contracts are
// documented in .kiro/specs/archive/go-core-reimplementation-v1/research.md under “Official API
// specification references”; task-level coverage is recorded in
// refclient-spec-matrix.md in the same spec directory.
//
// Multimodal scenarios use shared binary fixtures under testdata/refclient at the module root.
package refclient
