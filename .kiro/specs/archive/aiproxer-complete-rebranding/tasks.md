# Implementation Plan

## Execution Rules

- The rename is one scripted transformation. Fix the tool and rerun it from the baseline; do not hand-patch what a rule should cover.
- Never modify `.kiro/specs/archive/**`.
- Do not mix behaviour changes or refactors into the rename.
- Do not add aliases, dual reads or dual emits for retired names.
- The rewrite tool and its rule table stay outside the tracked tree.
- Merges to `main` are frozen from task 1.1 until task 3.1 has merged.

## Tasks

- [x] 1. Freeze and baseline

- [x] 1.1 Freeze `main` and record a green baseline
  - Land or close every open PR, and announce the merge freeze to all running agents.
  - Record the baseline SHA, then run the verification gate from `design.md` on it.
  - Record known `main` flakes (#1544) separately from new failures.
  - Observable completion: the baseline SHA and the result of each gate step are posted on #641.
  - _Requirements: 5.1_
  - _Boundary: repository_
  - _Depends: none_
  - _Validation: verification gate from `design.md`_

- [x] 2. Build the rewrite tool

- [x] 2.1 Write the rule table and the inventory report
  - Implement the path, content and regeneration phases from `design.md`, with the Target Matrix as the rule table and a reviewed false-positive list.
  - Run `-report` against the baseline. Add rules or false-positive entries until `UNCOVERED` is empty.
  - Observable completion: the report has no `UNCOVERED` entry, and each false-positive entry has a one-line reason.
  - _Requirements: 1.1, 1.2, 1.3, 2.2, 3.1, 3.2_
  - _Boundary: out-of-tree tool_
  - _Depends: 1.1_
  - _Validation: `-report` output against the baseline_

- [x] 2.2 Prove determinism and idempotence
  - Run the tool twice on two clean checkouts of the baseline, and compare the resulting trees.
  - Run the tool again on its own output.
  - Observable completion: the two trees are identical, and the second run changes nothing.
  - _Requirements: 3.1, 3.5_
  - _Boundary: out-of-tree tool_
  - _Depends: 2.1_
  - _Validation: `git diff --stat` between the two runs; second-run change count is zero_

- [x] 3. Rename the maintained tree

- [x] 3.1 Apply the tool and land the rename PR
  - In a worktree at the baseline:
    - commit the tool output as one mechanical commit;
    - then commit the hand edits from `design.md`, which include removing the `AGENTS.md` Transition section;
    - then add the guard test (`internal/qa/legacy_names_test.go`), which must not contain a retired spelling.
  - Run the full verification gate, including the identity step. `buf breaking` fails by design on this PR only.
  - In the PR body, give:
    - the tool and its checksum;
    - the gate evidence;
    - the durable-state table with its manual steps;
    - the proto exception.
  - Apply the `allow-large-change` label, set both override variables for the commits, then squash-merge.
  - Observable completion:
    - `main` builds and passes the gate under target names only;
    - the guard test is green;
    - no file under `.kiro/specs/archive/**` changed.
  - _Requirements: 1.1, 1.2, 1.3, 1.4, 1.5, 2.1, 2.2, 2.3, 2.5, 3.3, 3.4, 4.1, 4.2, 4.3, 4.4, 5.2_
  - _Boundary: repository-wide rename_
  - _Depends: 2.2_
  - _Validation: verification gate; `go test ./internal/qa -run LegacyNames`; `git diff --stat <baseline> -- .kiro/specs/archive` is empty_

- [x] 3.2 Reword Predecessor References
  - Find mentions of the earlier implementation in the maintained tree: comments, docs, fixture names and test descriptions.
  - Reword each to describe the behaviour on its own terms, keeping the technical substance. Rename fixture files whose names carry that provenance.
  - Observable completion: a search for Predecessor References in the maintained tree finds none, and the affected tests pass.
  - _Requirements: 2.4_
  - _Boundary: comments, docs, test fixtures_
  - _Depends: 3.1_
  - _Validation: `make dev-test-changed`; guard test; the Predecessor Reference search over `git ls-files` minus the archive_

- [x] 4. Cut over the repositories

- [x] 4.1 Transfer the private repository
  - Re-run the preflight from the Cutover Runbook.
  - Delete the empty `aiproxer/aiproxer-enterprise` placeholder, then transfer the current repository to `aiproxer/aiproxer-enterprise`.
  - Verify it is private, and check issues, PRs, rulesets and secrets.
  - Point maintainer remotes at the new location.
  - Observable completion: `github.com/aiproxer/aiproxer-enterprise` holds the full history, and `git fetch` works from the container checkout.
  - _Requirements: 5.3_
  - _Boundary: GitHub administration (owner)_
  - _Depends: 3.2_
  - _Validation: `gh repo view aiproxer/aiproxer-enterprise`; `git fetch origin` in `branches/main`_

- [x] 4.2 Publish the export to the public repository
  - Export the merge commit of task 3.2 with `tools/ossexport`, and create one parentless commit from it.
  - Disable `main-protector`, push the commit as `main`, make `main` the default branch, then re-enable the ruleset.
  - Leave both legacy branches untouched.
  - Observable completion:
    - public `main` has one parentless commit whose tree equals the export;
    - the legacy branches are unchanged;
    - no private commit SHA resolves in the public repository.
  - _Requirements: 5.4_
  - _Boundary: GitHub administration (owner)_
  - _Depends: 4.1_
  - _Validation: `gh api repos/aiproxer/aiproxer/branches`; `git rev-list --count main` is 1; private SHA lookup returns 404_

- [x] 4.3 Verify clean clones
  - Clone `aiproxer/aiproxer-enterprise` fresh and run the full gate.
  - Clone `aiproxer/aiproxer` fresh and run the export tree tests.
  - Check that open-source docs reference only `github.com/aiproxer/aiproxer`.
  - Record which public workflows fail for lack of secrets or Enterprise paths, and open one follow-up issue for them.
  - Observable completion: both clean clones are green, and the evidence is posted on #641.
  - Status: private clean clone: full gate + export OK; public CI fully green on 096233fd after #1685 (fixes #1681, closed) and #1686.
  - _Requirements: 5.5_
  - _Boundary: verification_
  - _Depends: 4.2_
  - _Validation: verification gate (private clone); export tree tests (public clone)_

- [x] 5. Close out

- [x] 5.1 Lift the freeze and archive the spec
  - Announce the end of the freeze and the tool location to owners of kept branches.
  - Archive this spec, and remove its entry from `internal/qa/kirospec/budget.go` if one is present.
  - Close #641.
  - Observable completion: the spec is under `.kiro/specs/archive/`, and #641 is closed with links to the evidence.
  - _Requirements: 3.5_
  - _Boundary: Kiro / repository_
  - _Depends: 4.3_
  - _Validation: `go run ./tools/kiro/kirocheck`_

## Completion Status

- [x] 3.1 #1669 merged 0ec20041; 3.2 #1670 merged d1351b43; progress #1682.
- [x] 4.1 repository transferred to private aiproxer/aiproxer-enterprise (full history, 145 open issues/PRs).
- [x] 4.2 public aiproxer/aiproxer main = orphan export (initial e0146d3e, LICENSE fix 8124dc0); legacy-python-* branches untouched; no private SHA present.
- [x] 4.3 private clean clone: full gate + export OK; public CI fully green on 096233fd after #1685 (fixes #1681, closed) and #1686.
- [x] Freeze: lifted by the owner separately (kept branches migrate with the rewrite tool; owner informed).
