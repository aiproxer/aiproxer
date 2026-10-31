# Requirements Document

## Introduction

This specification renames the product to **AIProxer** (prose and display) with `aiproxer` as the machine, repository and module slug, and `aip` as the three-letter abbreviation. Issue #429 records the original request; #641 tracks implementation. The prerequisite `open-core-billing-split` is complete: Enterprise code lives under `enterprise/`, and `tools/ossexport` publishes the rest.

The rename is mechanical. An out-of-tree rewrite tool applies one ordered rule table to the repository, and the Go compiler, the test suites and a tracked guard test prove the result. The tool is deterministic and idempotent, so the same tool also migrates branches started before the rename.

The **Legacy Token Set** means every former project-specific name of this product and repository, in any case, separator, prefix, suffix, path, identifier, header, environment, metric or key form. This specification names its members by description so that it contains no retired spelling. The set has four families:

- the former three-letter abbreviation, standalone or as a segment of a compound name (packages, commands, executables, environment variables, headers, metrics, config and git-config keys, plugin IDs, dotfiles, artifacts);
- that abbreviation with the `go` language prefix;
- the former full product name (the three words the abbreviation stands for);
- the former full name with the `go` prefix, which is also the current repository and module name.

Unrelated third-party identifiers and ordinary English words that contain the abbreviation's letters are not members.

A **Predecessor Reference** is a mention of this product's earlier implementation in another language: its repository, behaviour attributed to its era, parity with it, or fixture provenance traced to it.

The **maintained tree** is every Git-tracked path except `.kiro/specs/archive/**`. The archive is an immutable record and this specification never modifies it.

## Boundary Context

- **In scope**: Go module paths of all modules; branded package, command, executable, directory and file names; source identifiers; project-owned headers, environment variables, metrics, span names, extension keys, plugin IDs, the backend-plugin proto package, persisted table names and state directories; build, CI, release and developer tooling; docs, README, AGENTS files, skills, active Kiro specs and steering; the GitHub cutover of both repositories.
- **Out of scope**: `.kiro/specs/archive/**`; moving code between the open-source and Enterprise trees; behaviour changes; refactors; provider and protocol names; Git history and GitHub discussion history.
- **Adjacent expectations**: `tools/ossexport` defines the open-source tree. The existing public repository `github.com/aiproxer/aiproxer` holds the earlier implementation on its legacy branches; those branches stay.
- **Revalidation triggers**: a change to the target names, to the export boundary, or to the decision that durable state is renamed without migration code.

## Requirements

### Requirement 1: Target identity

**Objective:** As a maintainer, I want one target identity on every project-owned surface, so that no surface keeps the former brand.

#### Acceptance Criteria

1. When the rename completes, human-readable product prose shall say `AIProxer`, and lowercase machine identities (repository, release and module slug) shall say `aiproxer`.
2. When the rename completes, every Go module in the repository (root, connector-support, connectors, test fixtures) shall live under `github.com/aiproxer/aiproxer`.
3. When the rename completes, project-owned names shall follow the target matrix in `design.md`: `pkg/aipapi`, `pkg/aipsdk`, `pkg/aipruntime`, `cmd/aipstd`, `aip-backend-*` executables, `X-AIP-*` headers, `AIP_*` environment variables, `aip_*` metrics, `aip.*` span and extension keys, `com.aiproxer.*` plugin IDs, and the `aiproxer.backendplugin.v1` proto package.
4. The rename shall rename the open-source and Enterprise trees consistently and shall not move code between them.
5. If a request, environment or configuration uses a retired project-owned name after the rename, the system shall treat it as unknown input; no retired name shall be read, emitted or aliased for compatibility.

### Requirement 2: Zero legacy names in the maintained tree

**Objective:** As a maintainer, I want the maintained tree free of former names, with a permanent check, so that the rename cannot silently regress.

#### Acceptance Criteria

1. When the rename completes, every path component and the textual content of every file in the maintained tree shall contain no Legacy Token Set match, except entries in a reviewed false-positive list.
2. The rename shall not modify any file under `.kiro/specs/archive/**`; links from archived specs to renamed paths may dangle.
3. When the rename completes, a tracked guard test in `internal/qa` shall fail on any Legacy Token Set match in the maintained tree, and that test shall not itself contain a retired spelling.
4. When the rename completes, the maintained tree shall contain no Predecessor Reference; each one shall be removed or reworded to describe behaviour on its own terms.
5. When the standard distribution is built, its executable name, help and version output, and Go build metadata shall carry only target names.

### Requirement 3: Mechanical, atomic and reproducible migration

**Objective:** As an implementation agent, I want the rename to be one scripted transformation, so that it finishes in a day and every step is reproducible.

#### Acceptance Criteria

1. The rewrite tool shall live outside the tracked tree, apply an ordered rule table, produce the same output for the same input, and change nothing on a second run.
2. Before mutation, the tool shall report every Legacy Token Set match in the maintained tree that no rule or false-positive entry covers, and the rename shall not start while that report is non-empty.
3. The mechanical rename shall land as one pull request covering all modules, under the owner-authorized change-size override for that pull request only.
4. The rename pull request shall contain no behaviour change and no refactor beyond what the compiler requires to follow a renamed symbol.
5. When a branch was started before the rename, running the same tool on it and rebasing it shall be sufficient to migrate it.

### Requirement 4: Durable state and wire contracts

**Objective:** As an operator, I want renamed durable identifiers and wire names to be explicit, so that no state is lost without warning.

#### Acceptance Criteria

1. The rename shall rename durable branded identifiers (the secure-session tables, the per-user connector state directory, plugin IDs) in place, with no migration code, because no external deployment exists.
2. When the rename lands, its pull request and the release notes shall give the manual steps for existing local installations: move the connector state directory and recreate or rename the secure-session tables.
3. When the backend-plugin proto package is renamed, the generated code shall be regenerated, the breaking-change check shall record an explicit exception, and the host and every in-repository connector shall be rebuilt together.
4. When a metric is renamed, its type, labels and help meaning shall stay unchanged.

### Requirement 5: Verification and repository cutover

**Objective:** As the owner, I want both repositories at their target locations with verified builds, so that day-to-day work and the public release use only target names.

#### Acceptance Criteria

1. Before mutation, the implementation shall record the baseline commit and a green verification gate: build, vet under all four tag sets, the root suite, the all-module check, `make verify-product`, and the deterministic open-source export with its tests.
2. When the rename pull request is ready to merge, the same verification gate shall be green on its head.
3. When the cutover runs, the existing private repository shall be transferred with its full history to `github.com/aiproxer/aiproxer-enterprise`, after the owner's empty placeholder of that name is deleted.
4. When the cutover runs, the open-source export shall be pushed to the existing public repository `github.com/aiproxer/aiproxer` as a single-commit orphan `main` branch and made the default branch; the legacy branches shall remain untouched, and the public repository shall contain no object from the private repository's history.
5. When the cutover completes, a clean clone of each repository shall pass its verification gate (the full gate on the private repository; the export tree tests on the public one), and open-source documentation shall reference only `github.com/aiproxer/aiproxer`.

## Deferred / Out-of-Plan

- Rewording or renaming anything under `.kiro/specs/archive/**` (owner decision: the archive is immutable).
- Migrating each kept worktree and in-flight branch; the tool makes it possible, and owners run it per branch.
- Publishing a tagged release and container images under the new names.
- Rebuilding and republishing the externally published Cursor plugin against the renamed proto package (#1404).
- Triage of the public repository's earlier issues, description and topics.
- Porting functionality that still lives only on the public repository's legacy branches.
