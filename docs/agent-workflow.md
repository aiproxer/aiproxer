# Agent workflow reference

Read the section matching the operation; root `AGENTS.md` retains safety and architecture boundaries.

## Dependabot PRs

- Dependabot groups are per dependency family (`.github/dependabot.yml`). Handle each PR on its own; do not bundle families into one rescue PR.
- A failing family is skipped, not fixed in place: merge the green PRs, comment on the failing one, and track the incompatibility in an issue. Add an `ignore` rule only with that issue linked.
- Dependabot cannot sync consumer/fixture modules. If "All Go modules tidy and test" fails on drift, run `bash scripts/tidy-all-modules.sh` on the PR branch and push the result as one `build(deps): synchronize module metadata` commit.
- Keep each Dependabot PR to its own slice; do not wait for unrelated PRs.

## Skill routing

Load skills before the corresponding implementation or review:

| Task | Skills |
| --- | --- |
| Architecture, package boundaries, feature design | `golang-hexagonal-architecture`, `golang-design-patterns`, `golang-project-layout` |
| Constructors, lifecycle, interfaces | `golang-dependency-injection`, `golang-structs-interfaces` |
| Tests, conformance, regressions | `golang-testing`; testify: `golang-stretchr-testify`; benchmarks: `golang-benchmark` |
| Streaming, concurrency, cancellation | `golang-concurrency`, `golang-context` |
| Style, naming, safety, lint | `golang-code-style`, `golang-naming`, `golang-safety`, `golang-lint` |
| Behavior-preserving cleanup | `golang-simplify` |
| SOLID, change-cost, interface review | `golang-solid-principle-review` |
| Radical simplification, strict review, maintainability audit | `golang-thermonuclear-code-review` |
| Modern Go, data structures | `golang-modernize`, `golang-data-structures` |
| Library selection | `golang-popular-libraries` |
| Architecture navigation, implementations, call paths, blast radius | `codegraph` |
| Kiro task review, before accepting a task | `kiro-review-panel` |
| PR triage, submission, CI, merge, main verification, cleanup | `aip-pr-delivery` |

For error/security/observability/database/CLI/gRPC/performance/dependencies/documentation/CI/troubleshooting, load the focused `golang-*` skill from `.agents/catalog.json`. Repository steering overrides generic skill defaults.

## Exploration

- Choose the tool by the question: use exact files and distinctive symbols for code relationships; use file/text tools for documents, configuration and logs.
- Reuse retrieved source while its identity is unchanged. If output is truncated, read the missing range from the saved result rather than fetching the source again. Truncated or filtered text is partial evidence, not a complete read.
- For large collections, inspect available size metadata before printing; retain retrieved content and emit bounded batches. Keep full check logs in scratch and display a summary plus the log path; preserve the command's exit status.

## Canonical skill catalog

- `.agents/skills/` is the single tracked catalog for Codex, OpenCode, Pi, Antigravity CLI, Cursor, and Codebuff. Git worktrees receive it automatically.
- `.agents/catalog.json` is the curated inventory. Add or update a skill there in the same change as its directory.
- Keep one source rather than same-name copies under `.codex/skills`, `.cursor/skills`, `.kiro/skills`, `.opencode/skills`, or `.pi/skills`; duplicate discovery has agent-specific precedence and causes drift.
- Portable frontmatter contains `name`, `description`, optional `license`, and optional `metadata`. Folder and frontmatter names match.
- Run `pwsh -NoProfile -File scripts/check-agent-skill-catalog.ps1` after catalog changes.

## Verification

- `git commit` gates staged packages (build, vet, tests, lint); PR CI checks the complete suite and consumers. Run commits in background with no timeout so the gate survives long checks. Keep hooks enabled; a failed gate requires diagnosis, not `--no-verify`.
- Background commands notify completion. End the turn when waiting; do not poll with sleeps or repeated output reads.
- Setup, missing tools, Go guard installation, resource queues, scratch or database readiness: use [development setup](development-iteration.md#development-setup-and-readiness) and `make dev-doctor`.
- Keep scratch outside the repository. Root `.gitignore` can hide new files from `git status`; the root Markdown hygiene check accepts only `AGENTS.md` and `README.md`.
- Use [development iteration](development-iteration.md) for scoped feedback, comprehensive gates, contract/lint selection, delivery-slice reports and command lookup. Scoped feedback is not delivery certification.
- [Handoffs](agent-handoffs.md) define source-bound evidence and restart recovery. Artifact integrity does not confer semantic review approval.
- [Closeout](agent-closeout.md) defines the linked inventory and final checker gate. Publication, merge, verification and full completion remain distinct.
- `make pr-status PR=<n>` and `make pr-watch PR=<n>` are read-only. Exit 0 means ready-to-merge, 4 means stale revision, and 5 means already merged without claiming CI/main verification. Remote changes use `aip-pr-delivery`.
- Concurrency/streaming changes use focused non-race checks locally. Agents never run race checks locally, even when remote CI cannot run: scheduled remote scans own race coverage and a dedicated monitoring agent triages their findings. Missing race evidence never blocks a PR merge or work acceptance. Preserve the development-host guard, including direct Go commands. Absolute toolchains, alternate PATH, host spoofing, a `ci-stand-in` host policy and `AIP_ALLOW_RACE_ON_DEV` are not workarounds. Fake-toolchain guard tests live in `scripts/test-go-dev-guard.sh`; installation and current CI/platform commands are in development iteration and workflows.
- Fuzz parser/decoder changes where practical; use development iteration for commands.
