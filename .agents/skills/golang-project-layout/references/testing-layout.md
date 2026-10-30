# Tests, Benchmarks, and Examples

## File Naming Conventions

Go uses suffix-based naming for test-related files:

| Suffix | Purpose | Build behavior |
| --- | --- | --- |
| `_test.go` | Tests, benchmarks, and examples | Not included in normal builds |
| No test suffix | Regular code | Included when platform suffixes and build constraints match |

## Where to Place Tests

**Co-locate tests with the code they test:**

```
internal/
├── handler/
│   ├── handler.go          # Production code
│   ├── handler_test.go     # Tests for handler
│   └── handler_bench_test.go  # Benchmarks (optional)
├── service/
│   ├── service.go
│   └── service_test.go
└── model/
    ├── user.go
    └── user_test.go

pkg/
└── logger/
    ├── logger.go
    └── logger_test.go
```

**Key principles:**

- Tests use the production package or its external `_test` package according to the behavior under test
- Test files are in the **same directory** as the code they test
- Use `_test.go` suffix for all test files

## Test Package Options

When writing tests, you have two options for the package declaration:

**Option 1: Same package (white-box testing)**

```go
package handler  // Same package, can access unexported

import "testing"

func TestHandler(t *testing.T) {
    // Can access unexported functions and types
    internalFunction()
}
```

**Option 2: Package with `_test` suffix (black-box testing)**

```go
package handler_test  // Different package, only exported API

import "testing"

func TestHandler(t *testing.T) {
    // Can only access exported functions and types
    handler.PublicMethod()
}
```

**When to use each:**

- Use **same package** for unit tests that need to test internals
- Use **`_test` suffix** when verifying the public contract independently of implementation, including unit tests

## Benchmarks

Benchmarks can use any `_test.go` filename and contain functions with the `Benchmark` prefix. A `_bench_test.go` suffix is only an optional organizational convention.

## Examples

Examples serve two purposes: documentation and verification.

Examples can live in any `_test.go` file; a `*_example_test.go` suffix is only an optional organizational convention:

```
pkg/
└── logger/
    ├── logger.go
    ├── logger_test.go
    └── logger_example_test.go     # Examples
```

**Example function format:**

```go
package logger

import "fmt"

func ExampleLogger_Info() {
    log := New()
    log.Info("processing started")
    log.Info("processing complete")
    // Output:
    // INFO: processing started
    // INFO: processing complete
}
```

**Key points:**

- Example functions must start with `Example`
- The `// Output:` comment verifies the output
- Examples are runnable tests: `go test` will fail if output doesn't match
- `godoc` displays examples as documentation
- The file name does not control discovery; `Example...` function names do.

**For executable examples** (standalone demo programs):

```
examples/
└── basic-usage/
    └── main.go                    # Executable example
```

## Test Utilities

When you have shared test helpers, use a dedicated package:

```
test/
└── testutils/
    ├── mock.go
    └── fixtures.go
```

Or use the `internal/testutil` pattern:

```
internal/
└── testutil/
    ├── mock.go
    └── fixtures.go
```

## Test Fixtures

Fixtures are test data files used across multiple tests. Use one of these patterns:

**Option 1: Local testdata directory** (package-specific fixtures)

```
internal/
└── handler/
    ├── handler.go
    ├── handler_test.go
    └── testdata/
        ├── users.json
        ├── request_valid.json
        └── request_invalid.json
```

**Option 2: Global test directory** (shared across packages)

```
test/
└── fixtures/
    ├── users.json
    ├── products.json
    └── responses/
        ├── success.json
        └── error.json
```

**Option 3: Embedded fixtures** (Go 1.16+, use `//go:embed`)

```
internal/
└── handler/
    ├── handler.go
    ├── handler_test.go
    └── testdata/
        └── users.json
```

**Important notes:**

- Go ignores the `testdata` directory when building regular packages
- Use `testdata/` for package-specific test data
- Use `test/fixtures/` for cross-package shared fixtures
- Go source fixtures may live in `testdata/` when testing parsers, generators, or build tooling; ordinary package discovery ignores that directory

## Running Tests

```bash
go test ./...                    # Run all tests
go test ./internal/handler       # Test specific package
go test -v ./...                 # Verbose output
go test -race ./...              # Race detection
go test -cover ./...             # Coverage report
go test -short ./...             # Skip long-running tests
```

## Test File Summary

| File Type | Suffix | Package | Purpose |
| --- | --- | --- | --- |
| Test | `*_test.go` | `package X` or `package X_test` | Unit/integration tests |
| Benchmark | `*_test.go` | `package X` or `package X_test` | Performance tests; specialized suffix optional |
| Example (godoc) | `*_test.go` | `package X` or `package X_test` | Documentation + verification; specialized suffix optional |
| Executable example | No suffix | `package main` | Standalone demo programs |
| Shared test utility package | Regular `.go` files | `package testutil` | Importable helpers for tests; `_test.go` helpers are local to their test package |
