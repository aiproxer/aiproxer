# Agent Rules

## Product Vision

[`VISION.md`](VISION.md) is the owner-ratified product vision. It overrides any conflicting statement in this repository: agent instructions, steering, specs, docs, and code comments. Read it before design, review, or scope decisions. Never edit `VISION.md` or the block below; propose changes in the PR description instead. The block is a verbatim copy of the vision's decision principles, kept in sync by `TestVisionDigest_AgentsMatchesVision` in `internal/qa`.

<!-- vision-digest:begin -->
**Lines never crossed**
1. **No fabricated history.** Never present a model with a conversation that did not happen: never reorder model-authored content, never invent turns.
2. **No leakage.** No secret or credential leaks across the client boundary, or between users and tenants.
3. **No hidden degradation.** Every degradation is explicit: logged, observable, and configurable by the operator.

**Priorities when goals conflict (highest first)**
1. **Interoperability and session continuity.** When a backend cannot represent a request exactly, the default policy is to serve it with explicit, honest degradation rather than refuse it. Operators can switch to a strict policy that refuses instead. Never break a user's session or agent loop when a safe alternative exists.
2. **Fidelity.** Preserve the client's intent and content as exactly as the target backend allows.
3. **Efficiency.** Cost, tokens, and latency.
4. **Simplicity of the core.** Prefer the smallest mechanism that serves the priorities above.

**Applying them**
- A change that narrows which clients, backends, or combinations work must name the priority above that justifies it, or be escalated to the owner.
- Vision items are direction, not committed scope. Specs and PRs still ship small slices.
<!-- vision-digest:end -->

## Identity

- AIProxer: an open-core, universal compatibility layer between AI clients/agents and inference backends. Go implementation: small core, explicit plugins, runnable `cmd/aipstd` distribution.
- Describe implemented behavior only; mark future behavior explicitly.

## Open core

- **AIProxer**: the MIT-licensed open-source core, published as `github.com/aiproxer/aiproxer` with fresh history (no object from the private history).
- **AIProxer Enterprise**: closed source, kept with the full history in the private `github.com/aiproxer/aiproxer-enterprise`.
- One private repository holds both trees. Enterprise code lives under `enterprise/`, and a deterministic export (`tools/ossexport`) publishes everything else.
- Billing and money logic (pricing, rating, settlement, exposure/credit admission, usage-limit rules) is Enterprise code under `enterprise/`. The open-source tree keeps only call sites, ports, and no-op defaults.
- The open-source tree never imports `enterprise/`.
- `billing-financial-safety-contracts` (Enterprise code under `enterprise/`) stays parked until the owner resumes it.

## Temporary: remote CI unavailable in the private repository

GitHub Actions is disabled in `aiproxer/aiproxer-enterprise` because the organization's Actions billing is blocked: remote checks there never run and carry no signal. The public `aiproxer/aiproxer` repository's CI is unaffected. Delete this section when private remote CI runs again.

Until then, local evidence stands in for remote CI:
- **Run the local CI equivalents** on the PR head: the checks of every workflow that covers the changed paths (`.github/workflows/`, `scripts/ci-scope.sh`), starting with `make dev-test-changed` ([development iteration](docs/development-iteration.md#when-remote-ci-cannot-run)). Race lanes are excluded: never run them locally.
- **Post the evidence** in the PR description or a PR comment: the tested head SHA, each command with its result, and every required job that has no local equivalent (race lanes need no mention).
- **Merge on that evidence.** The maintainer approved treating it as the CI gate for `aip-pr-delivery`: remote checks that never started do not block. A failing local equivalent, or a remote job that ran and failed, still blocks.

## Source Of Truth

- Product vision (overrides everything else): [`VISION.md`](VISION.md).
- Steering is durable project memory: `.kiro/steering/`. The architectural product contract is `.kiro/steering/product.md`.
- Owner-ranked product goals and WIP limit: `.kiro/steering/roadmap.md`.
- Delivery slices, budgets, deferral, red-main handling: `.kiro/steering/delivery.md`. Read it before writing an issue, spec, task plan, or PR.
- Fast package map: `.kiro/steering/structure.md`.
- API/translation rules: `.kiro/steering/api-standards.md`.
- Routing/failover/B2BUA rules: `.kiro/steering/routing-and-orchestration.md`.
- Tech/tooling rules: `.kiro/steering/tech.md`.
- Test policy: `.kiro/steering/testing.md`.
- Do not add changelog, `_Updated`, `_Reason`, timestamp, or history footers to steering or agent instructions; git tracks history.

## Work Rules

- Ship a **slice**: the smallest change that delivers observable value and degrades safely. Everything else goes to a Deferred list and follow-up issues.
- TDD by default: test/interface first, implementation second. Tests prove the slice; they are not the deliverable.
- Smallest correct diff wins; avoid speculative abstractions.
- A feature branch carries only its slice. Failures that reproduce on `origin/main` are recorded in the PR body; their fixes go in a separate PR from `main`.
- Ask only when intent materially changes the result and repo context cannot resolve it.
- Never claim success without direct verification evidence.
- Preserve user-authored changes; never use destructive git commands unless explicitly requested.

## Respect git workflow: No work on main

Keep `main` clean: it only receives PR merges and never donates them.

- Before changing files, check the current worktree and branch. If you are already in a non-`main` worktree dedicated to the task, work there.
- Otherwise create one with `make worktree-create WORKTREE_BRANCH=fix/short-description [WORKTREE_BASE=origin/main]`, on a `fix/`, `feat/`, `spec/`, or `docs/` branch. If you create it by hand, use an absolute path directly under `<container>/worktrees/`; a relative path lands inside `branches/main` and is rejected at commit time.
- `main` and `dev` author nothing; the pre-commit hook refuses commits there. `AIP_ALLOW_MERGE_RECEIVER_COMMIT=1` is maintainer-only.
- Run `codegraph init` in a new worktree before implementation, or whenever `.codegraph/` is absent. Spec-archiving tasks may skip index creation; if archival reveals a need for code changes, the implementation rule applies to that repair.

**Change-size gate.** A commit or PR may change at most **100 `*.go` files**; docs, skills, and catalogs are exempt. Hooks, CI, and `go test ./internal/qa` enforce it. When it fires, stop and propose a split into slices. Overrides (`AIP_ALLOW_LARGE_CHANGE`, `aip.allowLargeChange`, the `allow-large-change` label) are maintainer-only, and never use `--no-verify`: it also skips secret scanning.

## Pointers

- Skills: load matching skills before implementation or review ([skill routing](docs/agent-workflow.md#skill-routing)). PR delivery and cleanup use `aip-pr-delivery`; code navigation uses `codegraph`. Repository steering overrides generic skill defaults.
- Skill changes: `.agents/skills/` is the single tracked source; follow [catalog ownership and validation](docs/agent-workflow.md#canonical-skill-catalog).
- Dependency PRs and module drift: [dependency delivery](docs/agent-workflow.md#dependabot-prs).

## Architecture Guardrails

- Core owns orchestration, routing (including A-leg routing overrides), failover, and B2BUA continuity.
- Provider semantics stay in adapters/plugins.
- Core must not import provider SDKs or concrete plugins.
- No pairwise protocol translators; use protocol <-> canonical adapters only.
- Streaming is primary; non-streaming collects the canonical stream.
- No transparent retry/failover after first downstream content event.
- Capability mismatches are negotiated per candidate before upstream I/O. The default policy is interoperability: when a backend cannot represent a request exactly, apply an explicit, logged, operator-configurable degradation instead of refusing; operators may select a strict policy that refuses. Never silently drop semantics, fabricate history, or reorder model-authored content. Existing reject-by-default paths move to this policy in their own slices.
- Request/response mutation belongs behind hooks/extensions, not core branching.
- Terminal decisions flow through one core chokepoint with a single exclusive provider slot; core never imports or branches on concrete providers (e.g., Agent Loop Guard); removal restores generic no-provider behavior.
- Use explicit construction/registration; no DI containers, reflection registries, globals, or Go native `plugin` in v1.
- Hybrid backends ([ADR 0008](docs/adr/0008-hybrid-backend-connector-plugins.md)): essential builtins are static; optional backends are executable gRPC connectors under `connectors/` (manifest-driven discovery). Do not add optional connectors to essential/`standard_table` fixed tables. Core keeps orchestration/B2BUA.
- Compatible-provider growth is data-driven (`internal/providerprofiles`) plus contract TCKs, not a Cartesian frontend×backend product or a new in-process backend per vendor.
- Billing seams: `.kiro/steering/product.md` (principle 8). SQLite/PostgreSQL parity: `.kiro/steering/testing.md`.

## Package Zones

Before choosing a package or changing dependency direction, read `.kiro/steering/structure.md` (ownership zones and intent-to-zone table). Inspect the tree for current package inventory rather than maintaining a second map here.

## Kiro Specs

- Specs are opt-in: `/kiro:*`, an explicit `.kiro/specs/...` path, or an explicit spec-driven request. Direct code is the default.
- Use a spec for public contract changes (`pkg/aipapi`, `pkg/aipsdk`), routing/B2BUA semantics, billing, or genuinely unclear requirements.
- If an active spec covers the work, do not code before its `requirements.md` and `design.md` are approved. Spec flow, budgets, and archiving: `.kiro/AGENTS.md`.

## Verification

- Before running checks or commits, use [verification workflow](docs/agent-workflow.md#verification) and `docs/development-iteration.md`. Run commits in background with no timeout; wait for their notification. Keep hooks enabled and scratch outside the repository.
- **Never run race checks locally**: no `make test-race`, `scripts/race-check.sh`, or `-race` flag, even when remote CI cannot run. Run focused non-race checks. Preserve the development-host guard without bypasses.
- Race checks are not part of coding, fixing, review, or delivery workflows. Scheduled remote scans run them, and a dedicated monitoring agent triages their findings. Missing race evidence never blocks a PR merge, task acceptance, spec completion, or closeout; do not request it, wait for it, or list it as a gap.
- Task evidence/restart recovery: `docs/agent-handoffs.md`; full completion: `docs/agent-closeout.md`. Start/link the inventory at delivery start and retain its final checker verdict before claiming closeout.
- PR status: `make pr-status PR=<n>` / `make pr-watch PR=<n>`; merged delivery, CI success and verified main are distinct. Use `aip-pr-delivery` for remote changes.

## Go Conventions

- Keep public `pkg/aipapi` / `pkg/aipsdk` contracts minimal, documented, and versionable.
- Define small interfaces where consumed; constructors return concrete types unless exposing stable SDK/plugin contracts.
- Own goroutines, channels, and cancellation explicitly; avoid per-request handler goroutines.
- Frontends map internal errors to wire shapes.
- Keep config typed; pass plugin config as raw subtrees to factories.
- Preserve empty-vs-null JSON semantics; use `internal/core/jsonpresence` when presence matters.
- Use forward-slash git pathspecs on Windows.
