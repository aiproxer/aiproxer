# Dependency Conflicts & Resolution

## Diagnosing Conflicts

```bash
# See why a module is in your build
go mod why -m github.com/some/module

# See which version is selected
go list -m github.com/some/module

# See the full requirement graph
go mod graph

# List all modules in the build
go list -m all
```

## Resolution Strategies

**Replace selected content** (temporary, main-module-only policy):

```bash
go mod edit -replace=example.com/pkg@v1.2.0=example.com/pkg@v1.3.1
```

```go
// go.mod
replace example.com/pkg v1.2.0 => example.com/pkg v1.3.1
```

**Use a local fork** (for debugging or patching):

```go
replace example.com/pkg => ../my-local-fork
```

**Block a problematic version**:

```bash
go mod edit -exclude=example.com/pkg@v1.3.0
```

When a version is excluded, any requirement on that version is redirected to the next higher available version.

**Force upgrade a transitive dependency**:

```bash
go get github.com/transitive/dep@v1.5.0
```

This updates the minimum requirement in `go.mod`; MVS can still select a higher version required elsewhere. Inspect `go list -m <module>` afterward. An intentional downgrade through `go get` may also downgrade modules that require the newer version; review the whole graph diff.

## Resolution Workflow

1. Run `go mod graph` and `go mod why -m <module>` to understand the dependency chain
2. Identify which of your direct dependencies pulls in the conflicting version
3. Try upgrading the direct dependency first with a reviewed version: `go get github.com/direct/dep@vX.Y.Z`
4. If that doesn't resolve it, use `replace` or `exclude` as a temporary fix
5. Run `go mod tidy` to clean up
6. Verify with `go build ./...` and `go test ./...`

**Important**: `replace` and `exclude` directives only take effect in the **main module's** `go.mod`. They are ignored when your module is used as a dependency. Verify a published library as a consumer without relying on its local replacements. Prefer a properly versioned upstream fix or fork when consumers must use patched content.

Source: [Go module graph and replacements](https://go.dev/ref/mod).

## Retract (For Module Authors)

Mark versions as broken or accidentally published:

```go
// go.mod
retract v1.0.0         // Contains critical bug in auth
retract [v1.1.0, v1.2.0] // Range of broken versions
```

Retracted versions are still downloadable but `go get` will not select them by default, and `go list -m -u` warns about them.
