// Package refbackend provides spec-shaped HTTP emulators for remote inference APIs.
// They are test-support utilities: production packages (cmd, core, plugins, pkg, etc.)
// must not list refbackend in their non-test dependency graph—integration and other
// tests may import it from *_test.go only (see internal/core/runtime/boundaries_test.go
// and task 10.0.7 in go-core-reimplementation-v1).
//
// Subpackages include openairesponses, openaichat, anthropicmessages, gemini, bedrock, and acp.
// Each implements the server-side wire shapes that official vendor clients expect,
// so integration tests can validate backend connectors without live providers.
// Normative contracts are documented in .kiro/specs/archive/go-core-reimplementation-v1/research.md.
package refbackend
