# Requirements: Release Manifest Overhaul

## User Intent & Problem Statement
The release manifest enforcement script (`scripts/check-release-clean.sh`) previously required a 1:1 exact line-by-line mapping of every single tracked file in `.release-files`. Whenever developers created Kiro SDD specifications (`.kiro/specs/...`), agent skills (`.agents/...`), or documentation, pre-commit/pre-push/CI checks failed unless `.release-files` was explicitly edited to add every individual markdown or JSON file path.

This created excessive maintenance churn, frequent merge conflicts on `.release-files`, and artificial friction for spec authors and contributors.

## Key Requirements

### R1. Pattern & Wildcard Support in `.release-files`
- **R1.1**: The release manifest `scripts/check-release-clean.sh` *shall* support exact file paths, directory recursion patterns (e.g. `path/**` or `path/`), and wildcard globs (e.g. `*.go`, `*.yaml`).
- **R1.2**: Blank lines and comment lines starting with `#` *shall* be ignored when parsing `.release-files`.

### R2. Namespace Pre-authorization for Spec & Metadata Directories
- **R2.1**: Non-distribution metadata namespaces including `.kiro/**`, `.agents/**`, `.cursor/**`, `.vscode/**`, `.github/**`, `.githooks/**`, `docs/**`, `archived/**`, and `specs/**` *shall* be expressible as pattern rules in `.release-files`.
- **R2.2**: Creating, modifying, or deleting files within pre-authorized pattern namespaces *shall not* require editing `.release-files`.

### R3. Execution Modes & Clean Validation
- **R3.1**: `scripts/check-release-clean.sh` *shall* support working tree validation (`tracked`), staged index validation (`--staged`), and specific git revision validation (`--ref <ref>`).
- **R3.2**: Any tracked file that fails to match an exact path or pattern in `.release-files` *shall* be reported as an unapproved violation and cause the script to exit with code 1.
- **R3.3**: Exact file entries in `.release-files` that do not exist in the repository *shall* be reported as stale exact entries and cause the script to exit with code 1. Directory namespace patterns *shall not* trigger stale errors when empty.

### R4. Performance & Documentation
- **R4.1**: Pattern matching across the full repository (5,000+ files) *shall* complete in under 100 milliseconds.
- **R4.2**: `README.md` *shall* document the pattern-based manifest rules and workflow.
