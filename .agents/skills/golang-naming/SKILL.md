---
name: golang-naming
description: "Choose idiomatic, searchable Go names for packages, files, identifiers, receivers, methods, interfaces, errors, tests, and examples. Use when naming new APIs or reviewing names for clarity and compatibility."
---

# Go naming

Names should make the smallest useful promise. Prefer the vocabulary already used by the package and its callers; do not rename a public symbol merely to satisfy a preference.

## Focused review

For a review, keep the work read-only unless fixes are requested. Establish the review base, supported versions, and relevant contract; inspect changed code plus the callers and tests needed to assess it. Stay within this skill’s lens.

- Trace a name to its callers and contract; flag a misleading promise, ambiguous unit, ownership/mutation surprise, or domain collision with a concrete reading consequence.
- Check rename compatibility beyond Go callers: JSON/tags, reflection, config keys, generated code, metrics, CLI flags, plugins, examples, and external consumers.
- Inspect initialisms, stutter, package qualifiers, receiver consistency, and searchable vocabulary using local conventions; avoid inventing mandatory prefixes or mutation suffixes.
- Check test/example names are discoverable and build filename suffixes select intended targets. Separate correctness/compatibility findings from optional naming preferences.

Report each actionable finding with severity, confidence, file/symbol, trigger, consequence, and smallest remedy. Separate introduced/worsened defects from pre-existing debt, and state executed checks versus inference. If none survives validation, say so and identify coverage gaps.

## Identifiers

- Use mixedCaps, not underscores or Hungarian prefixes. Initialisms stay consistently capitalized (`URL`, `HTTP`, `ID`, `JSON`).
- Keep local names short when scope is short (`i`, `err`, `ctx`) and descriptive when values live longer or have similar peers.
- Avoid stutter: `http.Server`, not `http.HTTPServer`; `user.Name`, not `user.UserName` unless the distinction matters.
- Use `NewType` for constructors when that convention is useful, but do not create constructors just to validate a zero-value-safe type.
- Name booleans as predicates (`Enabled`, `HasItems`, `CanRetry`) and avoid double negatives.

Receiver names are short and consistent within a type; they are not part of the method’s API. A method named `Read` does not implement `io.Reader` by name alone—the exact `Read([]byte) (int, error)` signature and method set are required.

## Packages and files

Package names are lowercase, concise, and singular where natural; avoid generic names like `util`, `common`, or `misc`. A `cmd/` tree is a useful convention for commands, not a Go requirement. The `internal/` rule is lexical: code under an `internal` directory may be imported only by code within that directory’s parent tree, regardless of module boundaries.

Use file suffixes and build constraints to communicate platform or role (`_test.go`, `_linux.go`, `_windows.go`). Keep generated-file markers intact. File names do not determine package visibility.

## Types, methods, and errors

Prefer names that describe behavior or domain concepts, not implementation details. Interface names may use `-er` when the single behavior is clear (`io.Reader`), but multi-method interfaces need not be forced into that suffix. Keep interfaces small and define them near the consumer.

Sentinel errors use `Err` plus a meaningful noun/adjective (`ErrNotFound`). Custom error types describe the failure (`ParseError`) and expose fields only when callers need them. Test names state behavior (`TestParserRejectsEmptyInput`); subtest names should distinguish cases without encoding incidental implementation.

See [identifier details](references/identifiers.md), [functions and methods](references/functions-methods.md), [packages and files](references/packages-files.md), [types and errors](references/types-errors.md), and [testing names](references/testing.md).
