// Package policydecision defines the protocol-neutral policy decision vocabulary,
// record model, observer contracts, and bounded evidence normalization shared by
// core extension runners, diagnostics, and tests.
//
// # Safety rules
//
// Records and contexts carry only safe scope values and bounded strings. Raw prompts,
// raw backend payloads, transport headers, credentials, resume tokens, and unvetted
// claims must never be placed in a Record or Context. ClientMessage and ClientCategory
// are the only fields intended for frontend use.
//
// # Dependency boundaries
//
// This package may depend only on other public SDK packages under pkg/aipsdk and the
// standard library. It must not import internal packages, frontend or backend plugin
// packages, provider SDKs, or transport wire/server packages.
package policydecision
