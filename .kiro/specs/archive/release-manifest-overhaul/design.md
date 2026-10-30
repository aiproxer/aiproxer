# Design: Release Manifest Overhaul

## Architecture & Algorithm

### 1. Pattern Categorization
The manifest loader in `scripts/check-release-clean.sh` reads `.release-files` and categorizes raw rules into three structures:
1. `exact_rules`: Hashtable/associative array of exact file paths (e.g. `Makefile`, `go.mod`, `README.md`).
2. `prefix_patterns`: List of prefix path strings derived from `dir/**`, `dir/*`, or `dir/` (e.g. `.kiro/`, `internal/`, `pkg/`).
3. `glob_patterns`: List of wildcard patterns containing `*` or `?`.

### 2. File Evaluation Flow
For each tracked file in the repository (obtained via `git ls-files` or `git ls-tree`):
1. **O(1) Exact Check**: Check if file exists in `exact_rules`.
2. **Fast String Prefix Check**: Check if file starts with any prefix in `prefix_patterns` using bash `[[ "$file" == "$p"* ]]`.
3. **Glob Case Match**: Check if file matches any wildcard pattern in `glob_patterns` using `case "$file" in $g)`.
4. If unmatched, record as violation.

### 3. Stale Rule Check
- For exact file rules in `exact_rules`, verify at least one tracked file matched the rule. If matched count is zero, flag as a stale exact entry.
- Directory namespace rules (`prefix_patterns`) are pre-authorized namespaces and do not flag zero-match as an error.

### 4. Manifest File Schema (`.release-files`)
Replaces 5,000+ flat file entries with domain-oriented pattern rules:
- Tooling/Workflows: `.github/**`, `.githooks/**`, `.jules/**`, `.cursor/**`, `.vscode/**`, `.dev/**`
- Kiro SDD Specs & Steering: `.kiro/**`
- Agent Skills: `.agents/**`
- Documentation: `docs/**`, `archived/**`, `specs/**`
- Core Code & Packages: `api/**`, `cmd/**`, `config/**`, `connector-support/**`, `connectors/**`, `internal/**`, `pkg/**`, `scripts/**`, `testdata/**`, `tools/**`
- Root Configs: `.coderabbit.yaml`, `.gitattributes`, `.gitignore`, `.gitleaks.toml`, `.golangci.yml`, `.goreleaser.yaml`, `.release-files`, `AGENTS.md`, `Makefile`, `README.md`, `config.yaml`, `go.mod`, `go.sum`, `skills-lock.json`
