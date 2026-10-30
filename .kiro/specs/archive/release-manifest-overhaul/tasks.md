# Tasks: Release Manifest Overhaul

- [x] **Task 1: Overhaul Enforcement Script (`scripts/check-release-clean.sh`)**
  - Implement exact path, prefix directory (`dir/**`), and glob wildcard pattern matching.
  - Implement mode support (`tracked`, `--staged`, `--ref`).
  - Implement stale rule checking for exact file entries.

- [x] **Task 2: Refactor Release Manifest (`.release-files`)**
  - Replace 5,000+ line flat file list with clean pattern rules for `.kiro/**`, `.agents/**`, `docs/**`, `internal/**`, `pkg/**`, etc.
  - Preserve exact top-level configuration and documentation rules.

- [x] **Task 3: Update Documentation (`README.md`)**
  - Document pattern-based manifest rules and workflow.

- [x] **Task 4: Verification and Hygiene Checks**
  - Verify working tree check: `bash scripts/check-release-clean.sh`.
  - Verify staged check: `bash scripts/check-release-clean.sh --staged`.
  - Verify ref check: `bash scripts/check-release-clean.sh --ref HEAD`.
  - Verify pre-commit and quality gates.
