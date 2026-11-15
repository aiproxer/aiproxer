# Go comment inventory

Run from the repository root:

```sh
go run ./tools/commentaudit -format=json -limit=100 -offset=0 internal/core
```

Positional arguments are repository-root-relative literal file or directory paths;
omit them for all tracked Go files. The tool reads working-tree contents of paths
selected by `git ls-files`, including files excluded by build tags. Missing or
invalid files, unmatched scopes, unmerged paths and symlinked paths fail the run.
Tracked generated files (Go's standard generated marker) and files under a
`vendor` directory are listed separately, not audited. Untracked files are not
included.

Markdown is the default; use `-format=json` for machine consumption. Groups are
ordered by path and source position. `-offset` and `-limit` paginate groups;
`next_offset` is null at the end. Limits are 1–1000. Exclusions are repeated on
each page. Pagination and fingerprints are stable only while inputs stay fixed;
the SHA-256 fingerprint includes path, byte offset and exact group text.

Categories and evidence are crude, non-authoritative review hints, not decisions
or deletion recommendations. Symbols identify attached declaration docs or an
enclosing function when detectable; empty means unknown. The tool never edits
files. Inspect source and relevant constraints before taking any action.
