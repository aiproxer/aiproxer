---
name: golang-code-style
description: "Practical Go style for readable, idiomatic code: formatting, control flow, declarations, API boundaries, comments, zero values, and reviewable organization. Use when writing or reviewing Go code where clarity matters beyond gofmt."
---

# Go code style

Check formatting with `gofmt -l` or `gofmt -d` during review; apply the repository’s formatter when editing is requested. Style is a means of making behavior and ownership easy to see, not a collection of universal thresholds.

## Focused review

For a review, keep the work read-only unless fixes are requested. Establish the review base, supported versions, and relevant contract; inspect changed code plus the callers and tests needed to assess it. Stay within this skill’s lens.

- Use the diff and surrounding control flow to show how a proposed style change reduces reader effort or exposes a bug. Preserve project conventions and explicit user choices.
- Before flattening branches, tracing early returns, or introducing helpers, compare side effects, short-circuit order, deferred cleanup, variable scope, and error identity.
- Check declarations and literals for shadowed errors, implicit zero-value assumptions, positional external fields, and accidental widening of public API.
- Distinguish enforced formatter/linter policy from preference. Function length, parameter count, receiver size, and import layout alone are insufficient findings.

Report each actionable finding with severity, confidence, file/symbol, trigger, consequence, and smallest remedy. Separate introduced/worsened defects from pre-existing debt, and state executed checks versus inference. If none survives validation, say so and identify coverage gaps.

## Clarity defaults

- Prefer early returns for invalid input and errors; keep the successful path easy to scan.
- Use `switch` when one value is compared against several cases. Keep `if` when conditions have different meanings or short-circuiting is important.
- Use `:=` for ordinary local initialization and `var` when the zero value is intentional or a declaration must be separated from assignment.
- Use keyed composite literals for non-trivial structs, especially across package boundaries.
- Keep names and comments precise. Explain invariants, ownership, compatibility, or a surprising choice; do not narrate syntax.
- Keep a `context.Context` first in functions that accept one. Do not store contexts in long-lived structs.
- Prefer `range` when the index is not part of the operation. Use an index when mutating elements, pairing with another slice, or needing exact positions.

## Zero values and collections

Nil slices are valid: `len`, `range`, and `append` work. Nil maps support reads and iteration but panic on assignment. Initialize a map before assignment. Allocate slices when a non-nil value is part of a wire contract or the API requires allocated storage; appending to a nil slice is valid. Do not initialize merely to satisfy a style rule.

```go
var pending []Item                 // nil means “not supplied”
pending = append(pending, item)    // valid
labels := make(map[string]string)  // assignment follows
```

Capacity hints are useful when a measured or known upper bound avoids growth; speculative large capacities waste memory.

## Functions and expressions

Keep functions focused, but do not split code solely to meet a line or parameter count. Group related values in a domain type only when that improves the contract; an options struct is not automatically clearer. Avoid naked returns in functions long enough to require scrolling.

Name complex boolean terms when the names express domain meaning. Preserve short-circuit order for checks with side effects, cost, or panic behavior. Use `strconv` for simple conversions, `strings.Builder` for repeated concatenation, and `fmt` when formatting is the actual operation.

Choose pointer or value parameters/receivers based on mutability, identity, copy cost, method-set needs, and measured behavior. There is no portable byte-size cutoff: compiler escape analysis, architecture, and workload matter.

## Package organization

Keep imports grouped and let tooling format them. A blank import is appropriate when a package documents an intentional registration side effect; place it where that side effect is visible and explain it when non-obvious. Avoid dot imports except in tightly controlled tests. Keep exported surface small because exported names become compatibility commitments.

Use standard-library `slices`, `maps`, and `cmp` when they make intent clearer. Add a dependency only when its behavior, maintenance, and license fit the repository; do not introduce a helper library for a one-line operation.

## Review checklist

Check formatting, names, error paths, nil/empty semantics, side-effect order, context propagation, and public contracts. Treat linter output as input for review, not an instruction to apply every suggestion. See [style details](references/details.md) for decisions that need a closer look.
