# Library Documentation

See the local `golang-testing` skill for executable Example functions.

## Public vs Private Libraries

Not all documentation applies equally. Adapt to your audience:

| Documentation | Public Library | Private Library |
| --- | --- | --- |
| Doc comments on exported symbols | Required | Required |
| Package comments | Required | Required |
| README.md | Useful entry point when consumers need setup or overview | Useful when local docs lack an entry point |
| Code examples in comments | Generous | Generous |
| `ExampleXxx()` test functions | Recommended | Recommended |
| Go Playground demos | Optional, only after disclosure review | Do not publish private code |
| pkg.go.dev / godoc | Primary docs surface | Use `go doc` locally or internal tooling |
| Documentation website | Large projects | Only if many teams consume the library |
| Publish public documentation | Only with authorization and disclosure review | Keep private API docs private |
| llms.txt | Only when a consumer needs this summary | Only when a consumer needs this summary |
| CHANGELOG.md | Recommended | Recommended |
| CONTRIBUTING.md | Recommended | Recommended (internal wiki may suffice) |

**Private libraries** should still have excellent doc comments and examples. Keep public-facing artifacts (Playground, pkg.go.dev, registries) out of private publication paths.

---

## Go Playground Demos

An executable `Example` test is the portable default. A Playground link is optional for deliberately public, self-contained code; review licenses, dependencies, embedded data, and disclosure before publishing it.

Add a `Play:` line in the doc comment:

```go
// Map applies fn to each element of the slice and returns a new slice.
//
// Play: https://go.dev/play/p/abc123xyz
//
// Example:
//
//	doubled := Map([]int{1, 2, 3}, func(x int) int { return x * 2 })
//	// doubled: [2, 4, 6]
func Map[T any, U any](s []T, fn func(T) U) []U {
```

For deliberately public, self-contained examples, create a Playground link at <https://go.dev/play/> only after disclosure and dependency review. An executable Example test remains the default.

Guidelines for playground demos:

- Keep demos self-contained — include all imports and a `main()` function
- Show the most common use case first
- Show real-world examples
- Print results so the output is visible when someone clicks "Run"
- Add comments explaining what each section does

---

## Example Test Functions

Examples are useful executable documentation for important exported APIs. They appear in generated docs and are verified by `go test`:

```go
// In map_example_test.go

package mypackage_test

import (
    "fmt"
    "strings"
    "github.com/{owner}/{repo}"
)

// ExampleMap demonstrates mapping over a slice.
func ExampleMap() {
    result := mypackage.Map([]int{1, 2, 3}, func(x int) int {
        return x * 2
    })
    fmt.Println(result)
    // Output: [2 4 6]
}

// ExampleMap_strings demonstrates mapping with string transformation.
func ExampleMap_strings() {
    result := mypackage.Map([]string{"hello", "world"}, strings.ToUpper)
    fmt.Println(result)
    // Output: [HELLO WORLD]
}
```

Naming conventions:

- `ExampleFuncName()` — example for a package-level function
- `ExampleTypeName()` — example for a type
- `ExampleTypeName_MethodName()` — example for a method
- `ExampleFuncName_suffix()` — multiple examples for the same function (suffix is lowercase)
- `Example()` — example for the whole package

Use a `// Output:` comment when output is stable and should be verified; examples without it still compile and can demonstrate setup or APIs whose output is nondeterministic.

---

## Code Examples in Doc Comments

Be generous with examples in doc comments. Show common use cases, edge cases, and error handling:

```go
// NewClient creates a new HTTP client with the given options.
//
// Example — basic client:
//
//	client := NewClient()
//
// Example — with custom timeout and retries:
//
//	client := NewClient(
//	    WithTimeout(10 * time.Second),
//	    WithRetries(3),
//	    WithRetryBackoff(time.Second),
//	)
//
// Example — with authentication:
//
//	client := NewClient(
//	    WithBearerToken(os.Getenv("API_TOKEN")),
//	)
func NewClient(opts ...Option) *Client {
```

---

## godoc and pkg.go.dev

Your doc comments automatically render on [pkg.go.dev](https://pkg.go.dev) when you tag a release and someone imports your package. This is the primary documentation surface for public Go libraries.

**How godoc renders comments:**

- First sentence of each doc comment appears in the package index
- `// Package foo provides...` appears as the package description
- Code blocks (indented by one tab) render as formatted code
- `# Heading` syntax (Go 1.19+) creates sections
- `[Link text]` creates a text link when paired with a `[Link text]: URL` definition; bare recognized URLs can also link
- `[Identifier]` links to other symbols in the package
- `Deprecated:` marker gets special styling

**For private libraries:** pkg.go.dev won't index private modules. Use `go doc` locally or run `pkgsite` on your internal network. Some teams set up a shared pkgsite instance for internal Go modules.

```bash
# View docs for a specific symbol
go doc github.com/{owner}/{repo}.FuncName

# View full package docs
go doc -all github.com/{owner}/{repo}

# If the repository deliberately tracks pkgsite as a Go 1.24+ tool:
go get -tool golang.org/x/pkgsite/cmd/pkgsite@vX.Y.Z
go tool pkgsite -http=:6060
# Then open http://localhost:6060
```

Replace the placeholder with the approved version and use the repository's existing tool setup when available. `go get -tool` changes module files; inspecting or reviewing docs alone does not require adding a dependency.

---

## Documentation Website

For larger libraries or frameworks, consider a dedicated documentation website.

### Recommended Frameworks

- **Docusaurus** (React-based) — best for large projects, supports versioning natively
- **MkDocs Material** (Python-based) — simpler setup, great search, clean design

Both can be deployed on Vercel.

### Recommended Sections

Follow the [Diataxis framework](https://diataxis.fr/) for organizing documentation:

| Section | Purpose | Example |
| --- | --- | --- |
| Getting Started | First steps, installation, hello world | "Install and run your first query in 5 minutes" |
| Tutorial | Step-by-step learning | "Build a REST API with authentication" |
| How-to Guides | Task-oriented recipes | "How to configure connection pooling" |
| Reference | Complete API documentation | Auto-generated from godoc |
| Deep dive / internals | Conceptual understanding | "How the scheduler algorithm works" |

### llms.txt

Add an `llms.txt` file only when the project has a use for a concise machine-readable summary.

This is an emerging convention for making projects AI-friendly. Place it alongside your README.

### Publication review

Before submitting a public library to an external documentation index or aggregator, verify that the repository, dependencies, examples, license, and embedded data are intended for disclosure. Private libraries should use local `go doc` or an internal documentation service.
