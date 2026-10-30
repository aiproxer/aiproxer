# Packages and files

Package names are lowercase, short, and stable. Avoid `util`, `common`, or `misc` packages that become dumping grounds. A `cmd/` directory is a convention for command entrypoints, not required by the language. An `internal/` package is importable only from within the parent tree of that `internal` directory; the rule follows filesystem ancestry, not just the module declaration.

Use suffixes and build constraints to make platform and role clear (`_test.go`, `_linux.go`, `_windows.go`). Keep generated markers and generated-file ownership intact. File names do not affect export visibility or interface satisfaction.

Keep package APIs coherent: related types and constructors should be discoverable together, while transport-specific representations can live in separate packages or files. Avoid import cycles by placing shared contracts in a small lower-level package.
