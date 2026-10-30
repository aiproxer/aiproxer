# Compilation failures

Start with the exact package and command. Capture `go version`, `go env GOMOD GOOS GOARCH CGO_ENABLED`, and the module/toolchain lines.

For undefined names, inspect imports, build tags, generated files, and the selected module version. For interface errors, compare exact method signatures and pointer/value method sets. For module errors, inspect `go list -m all`, `go mod graph`, and the `go`/toolchain lines before editing dependencies.

CGO and cross-platform failures require the matching compiler, SDK, environment, and build tags. Reproduce with the same `GOOS`, `GOARCH`, `CGO_ENABLED`, and toolchain as CI. Do not hide a compile error with a build-tag change unless the supported-platform policy explicitly calls for it.
