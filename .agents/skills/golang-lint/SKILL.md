---
name: golang-lint
description: "Run and review Go formatting, vet, golangci-lint configuration, diagnostics, suppressions, safe fixes, and CI enforcement."
---

# Go linting

Use lint as a fast, repeatable signal—not as a substitute for tests, review, or a threat model. Inspect the repository's Go version, existing configuration, generated code, build tags, and CI command before changing policy. Run formatting and checks on the same package set that CI and releases build.

## Focused review

For a review, keep the work read-only unless fixes are requested. Establish the review base, supported versions, and relevant contract; inspect changed code plus the callers and tests needed to assess it. Stay within this skill’s lens.

- Confirm the exact analyzer version, active configuration, build tags, modules, generated exclusions, and comparison base before interpreting diagnostics.
- Investigate source plus callers for each correctness warning. Show how the proposed fix preserves error identity, side effects, ownership, API shape, and platform behavior.
- Check suppression scope/reason, disabled checks, new-issues filters, and formatter exclusions for paths that bypass enforcement.
- Keep a review read-only; autofix and formatter writes need an editing task. Report unsupported configuration or missing checks separately from demonstrated code defects.

Report each actionable finding with severity, confidence, file/symbol, trigger, consequence, and smallest remedy. Separate introduced/worsened defects from pre-existing debt, and state executed checks versus inference. If none survives validation, say so and identify coverage gaps.

## Baseline

```sh
gofmt -l .
go vet ./...
go test ./...
golangci-lint run ./...
```

Use `gofmt -w` or the repository-approved formatter for files you own. `go vet` analyzers depend on Go version and flags; add separate analyzers such as `shadow` explicitly (it is not a current `go vet -shadow` flag). A linter configuration is optional and may use any filename/format supported by the installed `golangci-lint` version. Pin the tool in CI or via the repository's tool management policy.

## Choosing linters

Start with built-in analyzers and a small, maintained set. Typical categories include:

- correctness: `govet`, `staticcheck`, `unused`, `ineffassign`;
- error and resource handling: `errcheck`, `bodyclose`, `sqlclosecheck`;
- security: `gosec`, `govulncheck` (the latter is a vulnerability tool, not a linter);
- style: `revive`, `gofumpt`, `misspell`, when the project wants those conventions.

Enable a linter because it catches a defect or enforces a documented convention. A large list creates noise, slower feedback, and pressure to suppress warnings. Review new linters on representative packages before enforcing them repository-wide.

## Interpreting and fixing output

Read the complete diagnostic, source context, analyzer documentation, and whether generated/build-tagged code is included. Fix the root cause first. Do not make broad mechanical edits while another process is editing the same files; run autofix in a reviewed, isolated step, inspect the diff, and rerun tests and lint. `--fix` is a mutation, not a background task.

For a deliberate exception, prefer the narrowest documented suppression at the line or declaration. Include the linter name and a reason when the tool supports it, and do not suppress security or correctness warnings merely to make CI green. Keep generated-code exclusions explicit and review them when generators or tool versions change. See [linter reference](references/linter-reference.md) and [nolint directives](references/nolint-directives.md).

## CI policy

CI should run the pinned formatter/linter version, use the repository's build tags and module/workspace settings, and fail on diagnostics that the project has chosen to enforce. Run security and vulnerability checks as separate visible jobs when their semantics differ from lint. Cache tool downloads carefully and invalidate caches on version/config changes. A passing linter does not establish race freedom, API compatibility, or runtime security.

## Review checklist

- Is the command reproducible locally and in CI?
- Does it inspect all relevant modules, generated code, and build tags?
- Are fixes reviewed rather than applied concurrently with other edits?
- Are suppressions narrow, explained, and tracked?
- Do formatting, tests, vet, and security checks pass afterward?

Related local skills: `golang-continuous-integration`, `golang-security`, `golang-testing`, `golang-code-style`, and `golang-dependency-management`.
