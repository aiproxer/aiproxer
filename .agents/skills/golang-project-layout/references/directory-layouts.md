# Directory layout choices

Go does not require a universal repository tree. Start with the module boundary,
the number of binaries, and which packages are public. Preserve an existing layout
when it communicates those boundaries clearly.

## Common shapes

For multiple binaries, separate entry points under `cmd/<name>/` and put reusable
orchestration below them:

```text
project/
  cmd/server/main.go
  cmd/worker/main.go
  internal/                 # importable only within the parent module tree
  pkg/                      # public only when external consumers are intended
  testdata/                 # repository-level fixtures, when useful
  go.mod
```

A small binary can keep `main.go` at the module root if that is the established
layout; moving it to `cmd/` is an organization choice, not a correctness fix.
Libraries commonly keep public packages at the root or under a deliberate package
directory and use `internal/` for implementation details. `pkg/` is optional and
should not be used as a dumping ground.

## Boundary rules

- `cmd` packages should wire dependencies, parse process-level options, and start
  the application. Move reusable business behavior into a package that can be
  tested without a process.
- `internal` visibility is enforced by the Go toolchain: code may import an
  `internal` package only from within the parent directory tree.
- Use domain-specific package names; avoid catch-all `util`, `common`, and
  `helpers` packages unless their API has a precise boundary.
- Add `api/`, `configs/`, `scripts/`, `examples/`, or generated directories only
  when the project has a consumer and a documented owner for them.

## Check before reorganizing

Inspect `go.mod`, import paths, build tags, generated code, deployment manifests,
and scripts before moving a package. A directory move can change import paths and
`internal` visibility even when code behavior is unchanged. Prefer an incremental
move with compatibility shims only when consumers require one.

Build the actual commands after layout changes:

```sh
go list ./...
go test ./...
go build ./cmd/...
```

Use `go build ./...` when binaries may live outside `cmd/`.
