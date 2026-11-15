# Go comment cleanup queue

Run from the repository root:

```sh
go run ./tools/commentaudit
go run ./tools/commentaudit -files=0 internal/core
go run ./tools/commentaudit -detail internal/core
go run ./tools/commentaudit -format=json -limit=100 -offset=0 internal/core
```

Positional arguments are repository-root-relative literal file or directory paths;
omit them for all tracked Go files. The tool reads working-tree contents of paths
selected by `git ls-files`, including files excluded by build tags. Missing or
invalid files, unmatched scopes, unmerged paths and symlinked paths fail the run.
Tracked generated files (Go's standard generated marker) and files under a
`vendor` directory are not audited; detail and JSON list them as excluded, while
compact reports their count. Untracked files are not included.

Markdown is the default. Without `-detail` it is a compact, file-level queue
that never quotes comment text: each selected file appears at most once per
bucket on one line, with the path as inline code, its group count, the line range
of every group, and the unique hint names in that bucket. Buckets classify comment groups, not files, so
a file can appear under more than one bucket. `Likely cleanup` contains groups
whose hints are only `banner` or `test_label`; `Protected or sensitive` contains
groups with any `directive`, `license` or `lint` hint, including a mixed group
that also contains prose; all other groups are `Needs judgment`. The header
reports selected and remaining file counts, the total number of groups in the
selected files, the number of excluded files, and reminds the reader to inspect
the source. Compact output has no group truncation and no pagination; passing
`-offset` or `-limit` explicitly fails.

Files are selected in deterministic path order: the first N comment-bearing
files, with no severity ranking. `-files=N` caps the selection; compact defaults
to 5 and `0` means all. `-detail` and JSON default to all files, and an explicit
`-files=N` caps them with the same path-order selection. File selection happens
before `-offset` and `-limit` paginate groups.

`-detail` prints the full Markdown inventory with each group's comment text,
findings, symbol and fingerprint. The same full inventory is available as JSON
with `-format=json`. Both paginate comment groups with `-offset` and `-limit`
(default 100); `next_offset` is null at the end, and limits are 1–1000. Explicit
file caps bound the totals and pagination to the selected files.

Category findings are crude, non-authoritative review hints, not decisions or
deletion recommendations. `test_label` is only a cleanup hint for a short,
single-line, label-like comment (uppercase start, no sentence-ending punctuation,
at most five words) inside a top-level `Test...` function in a `_test.go` file;
it is not proof. `banner` is also a cleanup hint. `exported_doc` marks docs
attached to exported declarations. Symbols identify attached declaration docs or
an enclosing function when detectable; empty means unknown.

Pagination and fingerprints are stable only while inputs stay fixed; the SHA-256
fingerprint includes path, byte offset and exact group text. Exclusions are
repeated on each page. The tool never edits files. Inspect source and relevant
constraints before taking any action.
