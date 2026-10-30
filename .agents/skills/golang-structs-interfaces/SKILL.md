---
name: golang-structs-interfaces
description: "Design Go structs, interfaces, methods, constructors, embedding, generics, JSON contracts, copying, and zero values. Use when shaping package boundaries or reviewing method sets, ownership, serialization, or substitutability."
---

# Go structs and interfaces

Design the smallest contract that callers need. Let concrete types carry state and let interfaces describe behavior at the point of consumption.

## Focused review

For a review, keep the work read-only unless fixes are requested. Establish the review base, supported versions, and relevant contract; inspect changed code plus the callers and tests needed to assess it. Stay within this skill’s lens.

- Compare T and *T method sets, addressability, interface satisfaction, typed-nil values, optional interfaces, and wrapper/embedding promotion at actual call sites.
- Inspect constructors and zero values for bypassed invariants; trace ownership of retained slices/maps/callbacks and structs containing non-copyable state.
- Check embedding promotes JSON marshalers or other hooks unexpectedly, suppresses outer fields, or leaks additional behavior into a public contract.
- Test presence/omission/null/empty semantics, generic constraints and comparable behavior, and substitute implementations against the consumer contract.

Report each actionable finding with severity, confidence, file/symbol, trigger, consequence, and smallest remedy. Separate introduced/worsened defects from pre-existing debt, and state executed checks versus inference. If none survives validation, say so and identify coverage gaps.

## Structs and constructors

Make the zero value useful when practical. Provide a constructor when invariants, dependencies, synchronization state, or required configuration cannot be represented safely by a zero value. A constructor may return a concrete type or an interface: return an interface intentionally when hiding representation or stabilizing a behavioral contract is valuable; return concrete when callers need configuration, inspection, or additional methods.

Use keyed literals across package boundaries. Document ownership for slices, maps, pointers, and callbacks stored in a struct. Do not copy a struct containing a mutex, once, atomic state, a live connection, or another type whose documentation restricts copying after first use. `go vet`’s `copylocks` check is useful but pattern-based; it is not a complete ownership verifier.

## Methods and interfaces

Choose pointer receivers for mutation, identity, large state, or a method set that must be shared; choose value receivers for small immutable values when copying is intentional. Keep receiver choices consistent so the intended method set is obvious. Verify interface satisfaction with a compile-time assertion when the relationship is important:

```go
var _ io.Reader = (*Decoder)(nil)
```

Define interfaces where they are consumed, keep them small, and do not add methods “for future implementations.” Accept interfaces when multiple implementations are meaningful; return a concrete type when it improves discoverability and avoids needless indirection.

Embedding promotes methods and fields but also exposes them as part of the outer type’s API. Use it for a genuine “is-a” or implementation reuse relationship, not as a shortcut for inheritance. Name explicit fields when ambiguity or ownership matters.

## JSON and other wire formats

Exported fields are serializable by `encoding/json` without tags; tags are needed when the wire name, omission, or compatibility behavior differs from the default. `omitempty` omits false, zero numbers, nil pointers/interfaces, nil/empty slices/maps, and empty strings according to the encoder’s type rules; it does not mean “omit every semantically empty value,” and it cannot distinguish all absent versus explicit-zero cases. Use pointers, custom marshalers, or presence types when that distinction is part of the contract.

Do not use tags as decoration. Test null, empty, absent, unknown, and round-trip cases for public formats. Keep wire structs separate from domain structs when transport evolution should not leak into core behavior.

## Generics and ownership

Use type parameters for algorithms that are genuinely type-independent and constraints that express required operations. Use interfaces for runtime behavior and decoupling. Clone mutable data at ownership boundaries; do not call all channels “non-copyable”—channel values are copyable handles, while copying a channel does not copy its queued data or ownership semantics. The safety question is who may send, receive, close, and cancel.
