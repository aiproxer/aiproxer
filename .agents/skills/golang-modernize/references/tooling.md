# Modernization tooling

Use tools in a staged loop and review generated diffs:

```text
go version
go mod tidy        # only when dependency graph changes are intended
go fix ./...       # inspect every rewrite
gofmt -w <changed files>
go test ./...
go vet ./...
```

Run `go fix` with a clean or separately reviewable diff. Its suggestions follow the active toolchain and module language version; they are not automatically safe for every public contract.

For PGO, build the exact service with a profile captured from a representative workload, then compare baseline and profile-guided binaries under identical load. Keep profile provenance with the build and discard a profile that no longer represents the binary or workload. A benchmark profile from a different program is not a production PGO profile, and no fixed speedup is guaranteed.

For analyzer or linter upgrades, pin versions according to repository policy, read release notes, and run the old and new checks before changing configuration. Do not run competing formatters or autofix processes concurrently on the same files.
