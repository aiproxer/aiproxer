---
name: golang-modernize
description: "Plan and apply evidence-based Go modernization for language, standard-library, test, and tooling changes. Use when upgrading a module toward Go 1.26, replacing deprecated APIs, adopting new library helpers, or reviewing modernization risk."
---

# Modernize Go deliberately

Modernization is a compatibility change, not a style sweep. Start from the module’s declared Go version, supported platforms, public API promises, and a measured workload.

## Focused review

For a review, keep the work read-only unless fixes are requested. Establish the review base, supported versions, and relevant contract; inspect changed code plus the callers and tests needed to assess it. Stay within this skill’s lens.

- Check the module go directive, file-level language/build constraints, toolchain policy, and oldest supported runtime before endorsing a newer syntax or API.
- Compare old/new semantics for loop variables, timer channels, JSON omission/presence, random sequences, HTTP routing, and error matching; an API rename can change behavior.
- Inspect generated callers, CGO/cross-platform builds, experimental features, and downstream modules. A successful build on the newest toolchain does not establish minimum-version support.
- Challenge dependency removals and automatic rewrites against deployment overrides and documented compatibility. Require measured evidence for compiler/runtime performance claims.

Report each actionable finding with severity, confidence, file/symbol, trigger, consequence, and smallest remedy. Separate introduced/worsened defects from pre-existing debt, and state executed checks versus inference. If none survives validation, say so and identify coverage gaps.

## Workflow

1. Read `go.mod`, toolchain files, build tags, CI, and release/support policy. Confirm the installed toolchain and whether the change may alter language or standard-library semantics.
2. Establish focused tests, benchmarks, and compatibility checks before editing. Search callers and generated code; do not mechanically replace names across text.
3. Prefer standard-library APIs whose availability matches the module’s `go` line. Preview automated rewrites with supported diff/dry-run modes or an isolated workspace; review each rewrite before applying it to the working tree.
4. Migrate in coherent, reviewable groups. Keep behavior, error identity, wire output, ordering, and allocation/latency assumptions under test.
5. Run `gofmt`, focused tests, `go vet`, and package/build checks. For performance changes, compare representative benchmark distributions with `benchstat`.

## Useful Go 1.26-era opportunities

Check whether `slices`, `maps`, `cmp`, `min`/`max`, range-over-integer, `any`, improved iterators, `testing.T.Context`, `testing/synctest`, `b.Loop`, and `testing` artifact support simplify code in the actual target module. Confirm the exact API in the installed toolchain before using it. Do not label older features as new: `httputil.ReverseProxy.Rewrite` has existed since Go 1.20, and `context.WithoutCancel` since Go 1.21.

Use `math/rand/v2` only after considering deterministic seeds, output compatibility, and the public behavior of the old generator. Use `os.Root` (where the module’s supported Go version permits) for directory-scoped file operations instead of inventing path-prefix security checks.

## PGO and toolchain changes

PGO profiles must come from the production-like binary and representative workload. Build the exact program with the profile, compare a baseline and optimized build under the same conditions, and keep the profile with the source/build process that produced it. Do not promise a fixed percentage gain and do not produce a generic profile from an unrelated benchmark.

Treat compiler, linter, dependency, and CI upgrades as supply-chain changes: review release notes, checksums, compatibility, and reproducibility. A modernization is complete only when tests and supported-platform builds pass and any behavior change is explicitly accepted. See [tooling and measurement](references/tooling.md) and [version notes](references/versions.md).
