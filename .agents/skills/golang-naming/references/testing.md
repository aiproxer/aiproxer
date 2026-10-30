# Test and example names

Name tests after behavior: `TestParserRejectsEmptyInput`, `TestStoreReturnsNotFound`. Use subtests to separate input or policy cases and keep names stable enough for `go test -run` selection. Benchmark names state the operation and meaningful dimension (`BenchmarkEncode/size=4K`). Fuzz targets name the invariant or parser.

Examples use the exported identifier they document and should contain an `Output:` comment only when output is stable and intentional. Avoid test names that reveal private helper names or an implementation detail the package may change.
