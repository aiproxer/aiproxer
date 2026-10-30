# Go version checkpoints

Check the installed toolchain and the module’s `go` line before adopting an API. These are useful checkpoints, not a substitute for reading the active release notes:

| Feature | Availability to verify |
| --- | --- |
| `any`, `comparable`, generics | Go 1.18 |
| `errors.Join` | Go 1.20 |
| `httputil.ReverseProxy.Rewrite` | Go 1.20 (not a Go 1.26 addition) |
| `context.WithoutCancel`, `OnceFunc`/`OnceValue` | Go 1.21 |
| `clear`, `min`/`max` | Go 1.21 |
| range over integers, loop-variable semantics | Go 1.22 (loop semantics follow the package language version) |
| `slices`, `maps`, `cmp` growth and later helpers | Check each API’s release |
| `unsafe.SliceData`, `unsafe.StringData` | Go 1.20 |
| `testing/synctest` and later testing helpers | Check the module/toolchain; APIs evolved after experimental releases |

## Migration discipline

Raise the `go` line only after checking language semantics, standard-library behavior, supported builders, generated code, and dependency compatibility. A newer toolchain may compile code that an older supported toolchain cannot; CI and release images must be updated together.

Replacing `io/ioutil` with `io`/`os`, old random APIs with `math/rand/v2`, or manual collection loops with `slices`/`maps` can improve clarity, but verify error behavior, deterministic output, seeding, allocation behavior, and API compatibility. Do not rewrite public identifiers or serialized forms as part of a mechanical modernization.

Use build constraints and compatibility shims when a library supports multiple Go versions. Keep version-specific files small and test every supported target. Treat draft release notes and experimental APIs as provisional until the target toolchain documents them.
