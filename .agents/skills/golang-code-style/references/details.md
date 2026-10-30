# Style decisions that need context

## Conditions

Name a boolean when it captures domain meaning or prevents a condition from becoming a wall of operators. Preserve short-circuit order when a check is expensive, has side effects, or protects a later dereference.

```go
isOwner := resource.OwnerID == user.ID
canOverride := user.IsAdmin && permissions.Contains("override")
if isOwner || canOverride { ... }
```

Do not create names that merely restate syntax. A small two-clause condition is often clearer inline.

## Parameters and receivers

There is no universal parameter-count or struct-size cutoff. Group values into a domain type when they form a coherent concept or are passed together repeatedly; do not hide an awkward API behind an options struct. Choose pointer/value based on mutation, identity, method sets, copying, escape behavior, and measurements on the supported architecture.

## Imports and literals

Key fields in exported or cross-package struct literals. A blank import is valid when package initialization intentionally registers a driver, codec, or implementation; make the side effect visible and document it. Avoid dot imports outside narrowly controlled tests.
