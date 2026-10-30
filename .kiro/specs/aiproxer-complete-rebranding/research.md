# Research Log

## Measurement of `main` (2026-10-30, `ea9f84e5`)

Placeholders as in `design.md`: `{a}` is the former abbreviation, `{M}` the current module path.

| Surface | Size |
|---|---|
| Tracked files / Go files | 9,740 / 7,838 |
| Go files importing `{M}` | 5,830 |
| Go modules | 43: root, 3 connector-support, 35 connectors, 4 `testdata` fixtures |
| Nested modules that `replace` the root with a relative path | 35 |
| Occurrences of the public package names and their import aliases | ~63,000 |
| Tracked paths with a branded component | 904 files in ~50 directories |
| Files with `{A}_*` environment variables / `X-{A}-*` headers / `{a}_*` names | 266 / 84 / 132 |
| Durable SQL tables with the `{a}_` prefix | 6 secure-session tables (SQLite and PostgreSQL baseline DDL) |
| Per-user state directories with a branded name | 1: the `openaichatgptplan` SIWC host record and profiles under `UserConfigDir()` |
| Archived spec files (never modified) | 782 files, about 13 MB |
| Full verification gate | about 7 min root suite, 1.5 min all modules, 1 min product checks |

## Findings that reshaped the design

- **The module graph must flip atomically.** Each nested module requires the root through `{M}` with a relative `replace`. Once the root declares a different path, every nested module that still requires `{M}` fails to resolve.
- **Package waves gain nothing.** The public API package alone is imported by most Go files. Each staged wave would exceed the 100-file change-size gate and need the same owner override as a single PR.
- **Zero-legacy conflicts with migration code.** A data-preserving rename of persisted tables or directories would have to name the old identifiers. With no external deployment, renaming in place with manual notes resolves the conflict.
- **The public repository name is taken.** `github.com/aiproxer/aiproxer` is public. It holds the earlier implementation on two legacy branches, has 20 stars and no tags, and has a ruleset that blocks creating `main`. The owner chose to reuse it: the export lands as an orphan `main`, and the legacy branches stay.
- **The proto package is a wire name.** Renaming it changes the gRPC method paths. `make proto-check` runs `buf breaking` against the merge base, so the rename PR carries one declared exception.
- **The export is already correct.** `tools/ossexport` excludes `enterprise/**` and the billing specs through `export-ignore`. Its link check flags only links into excluded paths, so dangling archive links do not fail it.
- **The change-size override is renamed by the rename itself.** The pre-commit hook runs from the working tree, so the rename commits set both the former and the target override variables.
