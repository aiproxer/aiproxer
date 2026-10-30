# Pointers and `unsafe`

Ordinary pointers keep an object reachable and preserve type/liveness information. A `uintptr` is an integer and does not keep an object alive. Converting a pointer to `uintptr`, storing it, and converting it back later is unsafe unless it fits one of the narrow patterns documented by the `unsafe.Pointer` package.

Do not claim that the garbage collector moves objects between ordinary statements. The important hazards are losing pointer liveness, violating pointer arithmetic rules, retaining invalid addresses, and using memory after its owner has changed. Keep conversions in one expression where the documentation permits them and avoid retaining a `uintptr`.

Prefer safe slices, `copy`, `reflect`, or a package API. `unsafe.SliceData` and `unsafe.StringData` are Go 1.20 additions; use them only with valid lifetimes and lengths. Never cast arbitrary memory to a type with stronger alignment, pointer, or layout assumptions without proving those assumptions for every supported platform.
