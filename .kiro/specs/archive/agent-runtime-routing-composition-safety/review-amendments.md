# Normative Review Amendments

## Status and precedence

This document records the maintainer-side cross-check of CodeRabbit review feedback on PR #324. It is a **normative amendment** to the active `agent-runtime-routing-composition-safety` Kiro spec.

Where this document tightens or clarifies `requirements.md`, `design.md`, `design-review.md`, or `tasks.md`, the wording here takes precedence for implementation. It does not authorize implementation: all Kiro approvals remain `false` and `ready_for_implementation` remains `false`.

Review result: **7 findings accepted and incorporated as implementation constraints; 1 finding rejected as stated after checking the current runtime ordering.**

## A1. One execution-metadata authority per factory kind

**Status: accepted.**

Execution classification for one factory kind must have exactly one authority. A built-in/authoritative registry entry and a manifest-derived discovered export are mutually exclusive for the same kind.

The implementation must preserve the existing discovered-install collision invariant: when a factory kind is already registered, discovery must reject the duplicate rather than merge, override, or choose between execution profiles. Any duplicate/conflicting execution classification must therefore fail before generation assembly.

Required regression evidence:

- registry kind + discovered export of the same kind is rejected;
- conflicting execution classes cannot be merged silently;
- an authoritative registry kind continues to use its registry-owned metadata;
- no core provider-name override table is introduced.

This tightens Requirements 4.1, 4.8 and 8.1/8.9, Design §§4–7, and Tasks 1.4/2.1/2.4.

## A2. Configured-unknown versus absent backend; nil resolver must fail closed

**Status: accepted.**

The execution-class view must represent every configured backend instance explicitly. When a configured factory has omitted legacy execution metadata, the instance is present with effective class `unknown`; it must not be represented by a missing map entry.

For `runtime.NewExecutor` or the single constructor normalization seam used by it:

1. if `CoreRuntime.Backends` is non-empty and no execution-class resolver/view was supplied, synthesize an immutable view containing **every configured backend key** as effective `unknown`;
2. do not synthesize entries for backend names not present in `CoreRuntime.Backends`;
3. a direct route to a configured unknown backend remains allowed under `safe`;
4. a composite route containing configured unknown remains rejected under `safe`;
5. the pure validator must defensively fail closed with a bounded internal wiring/configuration error if a `safe` composite reaches it with an unavailable/nil resolver. It must not panic, treat configured backends as absent, or allow the selector;
6. test-only builders may explicitly mark ordinary fake backends as inference for test ergonomics, but production must not gain a nil-means-inference/unrestricted fallback.

Required regression evidence covers configured unknown, absent backend, nil resolver, and direct test-executor construction.

This is the authoritative interpretation of Requirement 4.9 and Tasks 1.3/2.4/3.2.

## A3. Typed unsafe-composition error must define Go matching semantics

**Status: accepted.**

The routing-owned error family must not merely describe fields; it must define how normal Go error wrapping works.

Conceptual contract:

```go
var ErrUnsafeExecutionComposition = errors.New(
    "routing: unsafe backend execution composition",
)

type UnsafeExecutionCompositionError struct {
    Composition string
    BackendID   string
    Class       lipsdk.BackendExecutionClass
    Policy      ExecutionCompositionPolicy
}

func (e *UnsafeExecutionCompositionError) Error() string {
    // bounded formatting; never include the raw selector or private runtime data
    ...
}

func (e *UnsafeExecutionCompositionError) Unwrap() error {
    return ErrUnsafeExecutionComposition
}
```

An equivalent reviewed `Is` implementation is acceptable only if it preserves the same semantics.

Required evidence:

- `errors.Is(err, ErrUnsafeExecutionComposition)` works through contextual wrapping;
- `errors.As` can recover `*UnsafeExecutionCompositionError` and its bounded fields;
- standard frontend execution-error mapping continues to recognize a wrapped unsafe-composition error and maps it into the invalid-request / HTTP-400 family;
- the raw selector, prompt, workspace, tool/MCP state, credentials, and hidden agent IDs are absent from error text.

This tightens Requirements 9.1–9.5, Design §15, and Tasks 3.2/4.4.

## A4. Closed-manifest evolution requires parser-first rollout

**Status: accepted.**

Because the executable connector manifest parser is closed (`DisallowUnknownFields`), an older host cannot parse a newly emitted `execution_class` field. The rollout contract is therefore parser-first for independently published official connectors:

1. host/parser support for optional `execution_class` is released/available first;
2. the new host must continue accepting previously published manifests that omit the field and classify them as `unknown`;
3. only after host support is available may independently published official connector manifests begin emitting explicit execution classes;
4. previously published no-field connector releases/artifacts remain immutable and valid rollback/mixed-version artifacts;
5. do **not** fabricate parallel duplicate legacy manifests for the same release unless existing release tooling proves such duplication necessary;
6. if repository release tooling proves host and connector publication is atomic and cannot be mixed, that proven atomicity may satisfy the same compatibility invariant.

Required manifest/release tests cover new-host + old-manifest, new-host + new-manifest, invalid class, and the release-order gate.

This is a normative addition to Requirements 8.2–8.3 and 10.11, Design §5/§20, and Tasks 2.2/2.3/6.3.

## A5. Absent backend must never masquerade as configured-unknown execution class

**Status: accepted with brownfield qualification.**

The composition validator must preserve the distinction already stated by Requirements 2.11 and 4.5:

- **absent backend identity**: defer to the existing entry-point-specific missing/unknown-backend authority;
- **configured backend with omitted execution metadata**: present + class `unknown`.

The validator must never convert an absent backend into `ErrUnsafeExecutionComposition`.

Required tests include both direct and composite selectors with an absent backend. Where an entry point already performs unknown-backend preflight (for example generation/admin preflight), its existing missing-backend error must retain precedence. The feature must not invent a new global unknown-backend semantic merely to satisfy this test.

This tightens Requirement 10.2 and Tasks 1.2/3.2/5.3.

## A6. A-leg allocation comment: reject as stated; clarify the actual safety boundary

**Status: requested change rejected as architecturally incorrect; boundary clarification accepted.**

CodeRabbit suggested requiring unsafe-composition rejection before A-leg allocation. That is not compatible with the current authoritative request flow.

Current runtime ordering is intentionally:

```text
secure-session BeginTurn
  -> authoritative A-leg/session resolution
  -> FetchALeg
  -> snapshot A-leg route override
  -> request/submit preparation
  -> buildRoutePlan
       -> CompileSelector
       -> [new] ValidateExecutionComposition
       -> native/dynamic planning
  -> billing authorization
  -> B-leg/backend Open
```

The A-leg and route-override snapshot are required to determine the **authoritative selector** for the turn. Moving composition validation before A-leg resolution would either validate a possibly superseded client selector before an admin override is known, or create a second pre-A-leg selector-authority path. Both would violate existing routing/secure-session ownership.

The normative side-effect boundary is therefore:

- authoritative secure-session/A-leg preparation **may occur first**;
- execution-composition validation occurs immediately after the authoritative selector is compiled in `buildRoutePlan`;
- rejection must occur before weighted RNG / `[first]` consumption, affinity planning mutation, interleaved planning-state access required by route selection, billing authorization, B-leg allocation, backend `Open`, connector execution, upstream request, or model usage;
- tests must prove zero **B-leg/provider/backend-attributable** work, not zero A-leg/session preparation;
- no second pre-A-leg routing authority is introduced.

This is the authoritative interpretation of Requirement 5.4 and Tasks 1.3/4.1.

## A7. Task validation commands must be executable, not prose-bearing shell lines

**Status: accepted.**

Where `tasks.md` currently appends conditional prose after a command, implementation execution must split the command from the note.

For Task 2.3, treat the validation as:

```text
go test ./internal/standardplugins/... ./internal/infra/backendplugins/... ./internal/archtest/...
```

Then separately run the repository-supported connector test command(s) for affected connector modules. The conditional sentence is guidance, not shell syntax.

For Task 6.2, treat the deterministic validation as:

```text
make quality-checks && make test
```

Then separately run `make test-race` where supported and `make qa` (or the documented equivalent release gate) as required by repository policy. Environment-gated tests must be reported as blocked/skipped rather than embedded as prose in a shell command.

## A8. Final task trace must cover the complete existing requirements

**Status: accepted.**

Task 6.4's final trace is corrected normatively to include criteria already present in the spec but omitted from the range shorthand:

```text
Requirements:
1.1-1.8,
2.1-2.11,
3.1-3.6,
4.1-4.9,
5.1-5.8,
6.1-6.9,
7.1-7.7,
8.1-8.9,
9.1-9.7,
10.1-10.14
```

The final implementation review must explicitly rerun or cite evidence for:

- configured-unknown and nil-view fail-closed behavior;
- absent-backend non-masquerading/precedence;
- typed `errors.Is` / `errors.As` mapping;
- parser-first mixed-version manifest compatibility;
- Codex dual-export classification;
- zero B-leg/provider dispatch for unsafe composition;
- alias/admin-override/reload behavior.

## Review closure

These amendments preserve the core design conclusion of the original spec. They do **not** change selector grammar, `pkg/lipapi`, normal inference routing algorithms, output-commitment rules, or the backend-plugin runtime gRPC protocol.

The external review therefore does not require a design reset. It tightens metadata ownership, fail-closed construction, Go error mechanics, manifest rollout, missing-backend precedence, test command hygiene, and traceability, while rejecting one suggestion that conflicts with current authoritative A-leg/route-override sequencing.
