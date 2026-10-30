# Auditing Dependencies

## Test-Only vs Binary Dependencies

Go's `go.mod` does **not** distinguish between test-only and production dependencies. All modules appear together, with `// indirect` marking transitive dependencies.

### What Gets Included in Your Binary

- `*_test.go` files are **never** compiled by `go build` — only by `go test`
- Packages imported only by test files are not linked into the final binary
- However, their modules still appear in `go.mod`

### Module Graph Pruning (Go 1.17+)

For Go 1.17+ modules, graph pruning retains the dependencies needed for imported packages while avoiding unnecessary transitive go.mod loading. The main module records more explicit indirect requirements to support pruning and lazy loading, so do not promise a smaller go.mod or infer production linkage from the module graph. See [module graph pruning](https://go.dev/ref/mod#graph-pruning).

### Upgrading With or Without Test Dependencies

```bash
go get -u ./...       # Broad upgrade; review all graph changes and test carefully
go get -u -t ./...    # Broad upgrade including test dependencies; review all graph changes
```

### Impact on Binary Size

Inspect the actual release binary and target package/build graph when evaluating size. A binary analyzer can help attribute retained code/data, but absence in its report does not prove a dependency is test-only: build tags, unused packages, dead-code elimination and inlining also affect attribution.

## Vulnerability Scanning with govulncheck

`govulncheck` reports known vulnerabilities that affect your code. It uses static analysis to narrow reports to vulnerabilities in code paths your project actually calls — unlike generic CVE scanners that flag every dependency regardless of usage.

```bash
# Scan source code (most common)
govulncheck ./...
# Or, when govulncheck is pinned with a Go 1.24+ tool directive:
go tool govulncheck ./...

# Scan a compiled binary
govulncheck -mode=binary ./bin/myapp

# JSON output (for CI integration)
govulncheck -format json ./...

# Include test code in analysis
govulncheck -test ./...
```

Output shows the vulnerability ID, affected module, fixed version, and the call trace from your code to the vulnerable function. Source-mode output may include module/package findings without a reachable vulnerable symbol. Distinguish those from called findings and inspect the selected build tags/platform. Binary mode has different precision; neither mode establishes that other builds or dynamic paths are safe.

Source: [govulncheck documentation](https://pkg.go.dev/golang.org/x/vuln/cmd/govulncheck).

For CI pipeline integration, see the local `golang-continuous-integration` skill.

## Tracking Outdated Dependencies with go-mod-outdated

`psampaz/go-mod-outdated` lists outdated direct dependencies with available updates.

```bash
# Show outdated direct dependencies with available updates
go list -u -m -json all | go-mod-outdated -update -direct

# Fail in CI if dependencies are outdated
go list -u -m -json all | go-mod-outdated -update -direct -ci

# Markdown output
go list -u -m -json all | go-mod-outdated -update -direct -style markdown
```

Output columns: MODULE, CURRENT version, WANTED (latest minor/patch), LATEST (latest overall), and VALID TIMESTAMPS (warns if an "update" is chronologically older than current).

## Analyzing Dependency Size with goweight

`jondot/goweight` lists every package linked into the binary sorted by size contribution. It helps identify bloated dependencies and evaluate whether a lighter alternative exists.

```bash
goweight          # Sort by size
goweight --json   # JSON output for CI tracking
```

**Modern alternative**: [go-size-analyzer](https://github.com/Zxilly/go-size-analyzer) (`gsa`) supports ELF, Mach-O, PE, and WebAssembly formats with interactive HTML/SVG visualization:

```bash
go get -tool github.com/Zxilly/go-size-analyzer/cmd/gsa@vX.Y.Z # replace with a reviewed release
go build -o ./myapp ./cmd/myapp
go tool gsa -f html -o size-report.html ./myapp
```
