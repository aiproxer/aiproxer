---
name: golang-continuous-integration
description: "Design and review Go CI/CD workflows for meaningful test coverage, supply-chain trust, dependency updates, containers, and releases."
---

# Go continuous integration

Design CI around the repository's supported Go versions, platforms, and release policy. Read existing workflows before adding jobs. A useful pipeline gives fast deterministic feedback on pull requests and reserves privileged publishing for trusted refs and protected environments.

## Focused review

For a review, keep the work read-only unless fixes are requested. Establish the review base, supported versions, and relevant contract; inspect changed code plus the callers and tests needed to assess it. Stay within this skill’s lens.

- Trace each event, checked-out revision, expression input, credential, permission, and artifact from untrusted PR to privileged release. Include reusable workflows and workflow_run consumers.
- Check all modules/build tags and required-check names are exercised; skipped jobs, path filters, allow-failure, shell pipelines, and stale baselines can make checks vacuous.
- Inspect cache and artifact provenance before restore or promotion, and isolate fork-controlled code from secrets, write tokens, and persistent privileged runners.
- Verify publishing/auto-merge eligibility against explicit release and upgrade policy, immutable action/tool versions, required checks, and the actual revision being delivered.

Report each actionable finding with severity, confidence, file/symbol, trigger, consequence, and smallest remedy. Separate introduced/worsened defects from pre-existing debt, and state executed checks versus inference. If none survives validation, say so and identify coverage gaps.

## Baseline checks

Start with the commands the repository already uses. A typical baseline is:

```sh
go test ./...
go vet ./...
go test -race ./...       # on supported race-detector platforms
go test -coverprofile=coverage.out ./...
go mod tidy
git diff --exit-code -- go.mod go.sum
```

The race detector is a dynamic test, not proof that all races are absent. Run it on a supported, adequately sized runner and choose package filters or a platform matrix when it is too expensive. Use `-shuffle=on` when tests are order-sensitive, and `-count=1` for tests whose result depends on external services or time. Do not impose a universal coverage percentage or latency target; establish thresholds from the project and workload.

Run integration tests in a separate job with pinned service versions, health checks, bounded startup/readiness polling, and explicit cleanup. Keep credentials and production endpoints out of pull-request jobs.

## Lint and static analysis

Run the repository's configured linter, formatter, and `go vet` analyzers. `golangci-lint` configuration is optional and may be `.golangci.yml`, `.golangci.yaml`, `.golangci.toml`, or `.golangci.json` depending on the tool version. Never run an autofix concurrently with implementation or another mutating job; make fixes in a reviewed working tree and rerun the checks. See the local `golang-lint` skill.

## Security and dependency checks

Use the smallest useful set for the project:

- `govulncheck ./...` for reachable vulnerabilities in the module's build graph.
- `gosec ./...` or CodeQL for complementary source-analysis coverage.
- Container scanning for images, if the project publishes images.
- License and secret scanning when required by project policy.

Keep scanners and actions at reviewed versions. A vulnerability scan is not a substitute for patch review or a threat model. Dependabot should open reviewable updates; auto-merge requires protected branches, required checks, least-privilege permissions, and an explicit policy for indirect or major upgrades. See [repo-security](references/repo-security.md).

## GitHub Actions supply chain

Pin third-party actions to immutable full commit SHAs and retain a comment with the human-readable release. Major tags are mutable and are not pins. Update SHAs through a reviewed dependency-update process. Use `permissions: contents: read` as the default and grant write, package, attestations, or security-event permissions only to the job that needs them. Do not run untrusted pull-request code with write credentials or use `pull_request_target` to execute it.

Use a cache only when its key includes the relevant Go/module/tool inputs. Make jobs fail clearly, upload logs and coverage on failure where safe, and avoid masking a failed command with a later successful command.

## Containers and multi-platform builds

Build multi-platform images with native builders, cross-compilation, or emulation as appropriate; QEMU is one option, not a requirement. Build pull requests without pushing. Publish only from a protected tag or release environment, with registry credentials supplied as secrets. Scope image-push, provenance, SBOM, and vulnerability-scan permissions to separate jobs where practical.

## Releases

Use GoReleaser or a small explicit release job according to the artifact shape. Validate the exact archive names and checksums generated by the configuration; do not invent download URLs. Trigger publishing from version tags, make the release environment require approval when appropriate, and record the Go/toolchain and source revision used. Libraries may need no binary build at all.

## Workflow sequence

1. Inspect `go.mod`, existing workflows, supported platforms, and branch protection.
2. Add a fast format/build/unit-test job.
3. Add lint, vet, race, integration, vulnerability, and coverage jobs where justified.
4. Add dependency-update configuration with review and permission boundaries.
5. Add container/release jobs only after artifact and credential paths are tested.
6. Validate YAML, action SHAs, permissions, cache keys, and failure behavior; run each important command locally.

Templates in [assets](assets/) are starting points, not drop-in policy. Replace placeholder image names, module paths, secrets, versions, and action SHAs before use.

Related local skills: `golang-lint`, `golang-security`, `golang-dependency-management`, `golang-testing`, `golang-benchmark`, and `golang-documentation`.
