---
name: golang-simplify
description: "Simplify existing Go code while preserving observable behavior, contracts, error identity, cleanup, concurrency, and performance characteristics. Use for focused refactors and staticcheck simplifications when the equivalence can be demonstrated."
---

# Simplify Go safely

For review-only requests, inspect candidates and report evidence without editing. Use this skill for a narrow, behavior-preserving cleanup. It is not a license to change an API, repair a bug, or redesign a package.

## Scope and guardrails

- Inspect `git status --short` and touch only requested or clearly targeted `.go` files. Preserve unrelated dirty changes.
- Exclude generated files (`*.pb.go`, `*_grpc.pb.go`, `*.pb.gw.go`, and files explicitly marked generated) unless asked.
- Treat externally visible behavior as part of the contract: return values, error wrapping and identity, log/metric events, ordering, nil/zero-value behavior, wire encoding, resource lifetime, lock/defer order, goroutine and channel timing, retries, and deadlines.
- Preserve exported signatures, serialization, existing test expectations, fixtures, and dependencies. Add focused characterization tests when needed to establish equivalence; do not weaken tests to accept changed behavior.
- A lint finding is a candidate, not proof that a rewrite is safe. Prefer a smaller diff when equivalence is uncertain.

## Workflow

1. Establish a baseline with focused tests and, when useful, `go vet` or `staticcheck -checks 'S*'` for affected packages. Record unrelated failures.
2. Identify one concrete simplification: early return, redundant branch, needless conversion, one-use private helper, or private one-use interface. State the invariant that must remain unchanged.
3. Apply the smallest patch. Do not combine simplification with formatting churn or speculative abstraction.
4. Inspect the diff and rerun the focused tests. Run `go build` or package tests that exercise callers; use the race detector for touched concurrent code when supported.
5. Report what changed, evidence, and any unverified assumption. If behavior cannot be shown equivalent, leave the code unchanged.

## High-risk areas

Do not flatten or reorder code around `defer`, locks, transactions, context cancellation, channel sends/receives, goroutine startup, cleanup, or error classification unless the equivalence is mechanically obvious. Before changing an error path, trace the source error through wrapping, `errors.Is`/`errors.As`, logging ownership, and transport mapping.

Inlining is safe only when it preserves argument evaluation order, variable lifetime, panic/recover scope, defer timing, nil checks, and resource ownership. Remove a private interface only when it is local, has one implementation and one consumer, and is not a test seam. Extract a helper only when the call site becomes clearer and the helper has a coherent responsibility.

Staticcheck `S*` suggestions still require this equivalence review. File size, parameter count, or aesthetic preference alone is not a defect.
