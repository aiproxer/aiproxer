# Design Document

## Overview

The rename runs as one scripted transformation, verified by the compiler and the existing gates, followed by an owner-run GitHub cutover. It adds no production component and changes no behaviour.

The earlier revision of this design staged the rename over eleven waves, with temporary import aliases, per-connector batch manifests and an out-of-tree checksummed scanner. A measurement of `main` (October 2026) showed that this staging cannot work and is not needed:

- 35 nested modules `replace` the root module with a relative path. Renaming the root breaks every nested module that has not been renamed in the same change, so a module wave cannot be green on its own.
- Every package wave touches thousands of Go files, so each one needs the change-size override anyway. Splitting adds override PRs without adding safety.
- The tree is regular: branded names are package names, import aliases, path segments and a few identifier families. One rule table covers them, and a full verification gate takes about ten minutes.

### Owner decisions (2026-10-30)

- **Public repository:** reuse the existing `github.com/aiproxer/aiproxer`. The export becomes an orphan `main`. Its legacy branches stay, because they may hold functionality still worth porting.
- **Durable state:** rename in place, with no migration code. There is no external deployment.
- **Execution:** one scripted PR under the change-size override.
- **Archive:** `.kiro/specs/archive/**` is immutable. The rename never touches it, and the guard test skips it.

## Boundary Commitments

- **This spec owns:** the rewrite tool and its rule table; the rename PR; the guard test; the follow-up PR that rewords Predecessor References; the cutover runbook.
- **Out of boundary:** archived specs; behaviour changes; refactors; moving code between the open-source and Enterprise trees; release publishing.
- **Allowed dependencies:** `tools/ossexport`; `scripts/check-all-modules.sh`; `make verify-product`; `buf`; the GitHub repository transfer API.

## Target Matrix

The source side is written with placeholders so that this document contains no retired spelling:

| Placeholder | Stands for |
|---|---|
| `{a}` / `{A}` | the former three-letter abbreviation, lower and upper case |
| `{g}` | `go` plus `{a}` |
| `{F}` | the former full product name |
| `{M}` | the current module path |

| Surface | Source form | Target |
|---|---|---|
| Module root (all 43 modules) | `{M}` and `{M}/...` | `github.com/aiproxer/aiproxer` and `.../...` |
| Public packages | `pkg/{a}api`, `pkg/{a}sdk`, `pkg/{a}runtime` | `pkg/aipapi`, `pkg/aipsdk`, `pkg/aipruntime` |
| Package names, import aliases | `{a}api`, `{a}feature`, `{a}cont`, … | `aipapi`, `aipfeature`, `aipcont`, … |
| Other branded packages | e.g. `{a}apidenial` | `aipapidenial` |
| Standard and Enterprise commands | `cmd/{a}std`, `enterprise/cmd/{a}std-enterprise` | `cmd/aipstd`, `enterprise/cmd/aipstd-enterprise` |
| Connector and test executables | `{a}-backend-*`, `{a}-backendplugin-fake*`, `{a}-taskrunner`, `{a}-testcost` | `aip-backend-*`, … |
| Exported identifiers | `{A}TestPostgresDSN`, … | `AIPTestPostgresDSN`, … |
| Environment variables | `{A}_*` | `AIP_*` |
| HTTP headers | `X-{A}-*` | `X-AIP-*` |
| Metrics, SQL tables, indexes | `{a}_*`, `idx_{a}_*` | `aip_*`, `idx_aip_*` |
| Span names, extension keys, git-config keys | `{a}.*` | `aip.*` |
| Plugin IDs | `io.{g}.*` | `com.aiproxer.*` |
| Proto package | `{g}.backendplugin.v1` | `aiproxer.backendplugin.v1` |
| Dotfiles and staging dirs | `.{g}-*` | `.aip-*` |
| Connector state directory | `UserConfigDir()/{a}/...` | `UserConfigDir()/aiproxer/...` |
| Skills | `{a}-pr-delivery`, `go-{a}-cordis-v4` | `aip-pr-delivery`, `aip-cordis-v4` |
| Prose | `{F}`, `{A}`, `Go-{A}`, `Go {A}` | `AIProxer` |
| Repository URLs | `github.com/matdev83/{g-repo}` | `github.com/aiproxer/aiproxer` |

`{g-repo}` is the current repository name (family four). Enterprise-only material may name `github.com/aiproxer/aiproxer-enterprise`.

## Rewrite Tool

The tool lives in the implementer's scratch directory, not in the repository. It is one Go program (or script) with:

1. **Input set:** `git ls-files` minus `.kiro/specs/archive/**`, minus binary files.
2. **Inventory mode (`-report`):** prints every Legacy Token Set match, grouped by rule. Matches that no rule or false-positive entry covers go in an `UNCOVERED` section. The rename starts only when `UNCOVERED` is empty (requirement 3.2).
3. **Path phase:** computes path renames from the rules and applies them with `git mv`, deepest paths first. Directory renames happen before content rewriting, so content rules see final paths.
4. **Content phase:** applies the content rules in a fixed order, most specific first:
   1. module path;
   2. repository URLs;
   3. full-name forms;
   4. proto package and plugin IDs;
   5. `go`-prefixed forms;
   6. command and executable names;
   7. package identifiers and aliases;
   8. exported identifiers;
   9. `X-{A}-`, `{A}_`, `{a}_` and `{a}.`;
   10. standalone prose `{A}`.

   Rules match on token boundaries; a raw three-letter substitution is forbidden. The false-positive list (for example `clip`, `flip`, `slip`, `eclipse` and third-party names) is a reviewed constant in the tool.
5. **Regeneration phase:**
   - `gofmt -w` on changed Go files;
   - `buf generate` for `api/backendplugin/v1`;
   - the feature-plane generator (`scripts/generate-feature-planes.go`);
   - any other generator whose freshness test fails;
   - `GOWORK=off go mod tidy` in every module.
6. **Idempotence:** a second run reports zero changes. A run on the same input commit produces byte-identical output.

The tool deliberately contains retired spellings. It is attached to the rename PR as a gist or comment for review, and it stays out of the tracked tree.

### Hand edits after the tool

The rename PR keeps these in a separate commit so the mechanical commit can be reproduced:

- delete the `Transition` section of `AGENTS.md`, and point the Identity line at `cmd/aipstd`;
- fix prose a rule made awkward, such as a doubled product name;
- fix compile errors the rules could not foresee (expected: none or a handful);
- the guard test (below).

## Guard Test

Location: `internal/qa/legacy_names_test.go`.

- It builds its patterns at run time from fragments, so the file contains no retired spelling. It covers all four Legacy Token Set families, as path components and as content tokens.
- It walks `git ls-files` (it skips when Git is unavailable, like the other `internal/qa` hygiene tests), skipping `.kiro/specs/archive/**` and binary files.
- It holds the same reviewed false-positive list as the tool, and reports `path:line` for every match.
- Because it runs in `go test ./internal/qa`, the pre-commit hook and CI enforce it on every later change.

## Durable State and Wire Contracts

| Item | Change | Manual step for an existing local install |
|---|---|---|
| Secure-session tables (6, SQLite and PostgreSQL baseline DDL) | renamed in the baseline migration | drop the local secure-session database, or `ALTER TABLE ... RENAME` the six tables and indexes |
| Connector state directory (`openaichatgptplan` SIWC host record and profiles) | directory name changes | move the directory under the user config dir to `aiproxer/` |
| Plugin IDs in configuration | `io.{g}.*` → `com.aiproxer.*` | edit operator config files that name plugin IDs |
| Environment variables and headers | `AIP_*` / `X-AIP-*` | update shell profiles, clients and scripts |
| Proto package | gRPC method names change | rebuild out-of-tree connectors (Deferred: Cursor plugin, #1404) |

The rename PR body and the release notes carry this table. For the proto package, `make proto-check` runs `buf breaking` against the merge base. On the rename PR that check fails by design, and the PR records this as the declared exception. After the merge, the base is the renamed package, and the check is meaningful again.

Metric renames change only the prefix; type, labels and help meaning stay the same.

## Verification Gate

The same gate runs on the baseline and on the rename PR head. Command names use the environment variables of the tree being tested.

| Step | Command |
|---|---|
| build | `go build ./...` |
| vet | `go vet -tags=$t ./...` for `t` in `""`, `integration`, `precommit`, `precommit,integration` |
| root suite | `go test -count=1 -tags=precommit,integration -timeout=60m ./...` |
| all modules | `bash scripts/check-all-modules.sh` |
| product | `make verify-product` |
| proto | `make proto-check` (expected `buf breaking` exception on the rename PR only) |
| lint | `bash scripts/lint-all-modules.sh --base <baseline>` |
| export | `go run ./tools/ossexport -rev HEAD` twice, digests equal, export tree tests green |
| identity | `go version -m` and `--help`/`--version` of the built `aipstd` show only target names |

Known `main` flakes (#1544) are recorded and do not block. Any new failure blocks.

## Delivery Sequence

1. **Freeze:**
   - land or close every open PR;
   - announce a merge freeze to all agents;
   - record the baseline SHA and a green gate.
2. **Tool:**
   - write it;
   - run `-report` until `UNCOVERED` is empty;
   - check idempotence and determinism.
3. **Rename PR:**
   - from a worktree at the baseline: tool commit, then hand-edit commit;
   - run the gate;
   - post the evidence and the tool;
   - merge.
4. **Commit override:** the pre-commit hook reads the change-size override from the working tree, and the tool renames that variable. Set both the former and the target override variables for the rename commits only, and apply the `allow-large-change` label. The owner authorized this for the rename PR.
5. **Predecessor PR:** reword the remaining Predecessor References in the maintained tree. This is a small docs-and-comments PR, and it may be dispatched.
6. **Cutover** (runbook below).
7. **Lift the freeze.** Announce the tool to owners of kept branches (Deferred: per-branch migration).

## Cutover Runbook

Preflight, as checked on 2026-10-30:

- The owner is an admin of the `aiproxer` organization.
- `aiproxer/aiproxer-enterprise` is a private, empty placeholder.
- `aiproxer/aiproxer` is public, holds only the two legacy branches and no tags, and has the active ruleset `main-protector` on `refs/heads/main`. That ruleset forbids creation and update and has no bypass actors.

Steps:

1. Delete the placeholder `aiproxer/aiproxer-enterprise`.
2. Transfer the current private repository to the `aiproxer` organization under the name `aiproxer-enterprise`, using `POST /repos/{owner}/{repo}/transfer` with `new_owner` and `new_name`. Verify it is private, and check its issues, PRs, rulesets and secrets.
3. Export the rename merge commit: `go run ./tools/ossexport -rev <sha> -out aiproxer.tar -extract-dir <dir>`.
4. In `<dir>`, run `git init -b main` and create one commit with no parent.
5. Set `main-protector` to `disabled`, push `main` to `aiproxer/aiproxer`, set `main` as the default branch, and set the ruleset back to `active`.
6. Verify the public repository:
   - `main` has one parentless commit whose tree equals the export;
   - its branches are `main` plus the two untouched legacy branches;
   - no private commit SHA resolves in it.
7. Point maintainer remotes at `aiproxer-enterprise`. Run the full gate in a clean clone of the private repository, and the export tree tests in a clean clone of the public one.
8. Note which public workflows fail for lack of secrets or Enterprise paths. Fixing them is a follow-up issue.

If a step fails, stop. The code stays at the target names and nothing in the source changes to compensate. If the public repository receives a private object, delete the `main` branch there and push it again from the export.

## Error Handling

- **Compile errors after the tool:** fix the rule and rerun from the baseline, rather than hand-patching many files. A hand fix is acceptable only for a one-off.
- **An `UNCOVERED` match:** add a rule or a reviewed false-positive entry; never skip it.
- **Main moves during the freeze:** rebase is not needed. Rerun the tool on the new baseline.

## Requirements Traceability

| Requirement | Realized by |
|---|---|
| 1 | Target Matrix; Rewrite Tool |
| 2 | Rewrite Tool input set; Guard Test; Predecessor PR; identity gate step |
| 3 | Rewrite Tool (report, idempotence); Delivery Sequence |
| 4 | Durable State and Wire Contracts |
| 5 | Verification Gate; Cutover Runbook |
