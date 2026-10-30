# Functions and methods

Use verbs for operations (`Parse`, `Load`, `Write`) and predicates for queries (`IsReady`, `HasValue`). Keep names specific enough to distinguish side effects from pure inspection. A method receiver is usually one or two lowercase letters, consistent across the type.

Keep `context.Context` first when accepted. Put identifiers and options before output destinations when that matches the package’s existing convention. Do not use a generic `Do` or `Handle` when the operation has a more precise public name.

Method names are not reserved words. A type implements an interface only when its method set has the exact required signatures. Compile-time assertions make important intent visible.

Avoid constructors that merely return a struct literal. Use `NewX` when validation, dependencies, ownership, or setup establishes an invariant. Keep methods cohesive and avoid adding convenience methods that duplicate trivial field access.
