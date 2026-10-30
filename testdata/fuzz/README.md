# Fuzz seed corpus layout

This file is the **repo-level index**. Actual seed files live next to each package, e.g. `pkg/aipapi/testdata/fuzz/FuzzCallValidateJSON/…`.

Each corpus file must use the **`go test fuzz v1`** encoding (see the [fuzzing tutorial](https://go.dev/doc/tutorial/fuzz)): first line is exactly `go test fuzz v1`, then one line per fuzz argument using Go literal syntax (`[]byte("…")`, `string("…")`, …). Hand-authoring is possible for small seeds; otherwise copy minimized outputs from a local fuzz cache into the matching `testdata/fuzz/FuzzName/` directory.

Go native fuzzing loads the seed corpus from:

1. `f.Add(...)` inside each `func FuzzXxx(f *testing.F)`
2. **Committed files** under `{package_dir}/testdata/fuzz/FuzzXxx/`

`{package_dir}` is the directory of the Go package being tested (for example `pkg/aipapi` or `internal/core/routing`). The subdirectory name **`FuzzXxx` must match the fuzz function name exactly**.

## File → input mapping

| Fuzz argument type | File content |
|--------------------|--------------|
| `[]byte` | Raw bytes of the file |
| `string` | File decoded as UTF-8 text (whole file is one string) |

## Adding seeds

1. Pick the package and fuzz target (see `docs/release-gates.md` table and `Makefile` `test-fuzz`).
2. Create `testdata/fuzz/FuzzTargetName/your_seed` under that package (any filename; avoid secrets).
3. Run a short fuzz locally to ensure it loads:  
   `go test -fuzz=FuzzTargetName$ -fuzztime=2s -run=^$ ./path/to/pkg`

Interesting inputs from local fuzzing can be copied from the fuzz cache directory; prefer minimal reproducers.
