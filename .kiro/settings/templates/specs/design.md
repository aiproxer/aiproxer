# Design Document Template

---
**Purpose**: Give implementers enough detail to build the V1 slice consistently, and no more.

**Budget**: At most 300 lines (`.kiro/steering/delivery.md`). A longer draft means the slice is too large: split the spec rather than trimming prose. Delete every section and placeholder that does not apply.
---

## Overview
One or two paragraphs: the value this slice delivers, who consumes it first, and how it degrades when it cannot decide or fails.

### Goals
- V1 outcome 1
- Done criterion

### Non-Goals
- Deferred items from `requirements.md`, each with its follow-up issue

## Boundary Commitments

### This Spec Owns
- Behaviours, data, and contracts this slice is responsible for

### Out of Boundary
- Related concerns this slice does not own, including deferred scope

### Allowed Dependencies
- Existing packages, seams, and stores this design uses

### Revalidation Triggers
- Changes that force consumers of this slice to re-check integration

## Architecture

### Seams Reused
Name the existing seams the slice plugs into (extension stage, plane, facade, request facts, store, config subtree). For each new seam, add one line tying it to a V1 requirement and explaining why no existing seam fits.

### Project Boundary Questions (AIProxer)
- Core-owned or plugin-owned? [answer]
- New canonical concept, or adapter-specific behaviour? [answer]
- Streaming-first path preserved? [answer]
- Provider SDK leakage avoided? [which package owns SDK/wire types]
- No retry/failover after first client-visible output? [answer]
- Secure-session, diagnostics, or startup-security posture affected? [yes/no]

### Flow (optional)
A Mermaid sequence or state diagram only when the flow is non-obvious. Plain Mermaid, no styling.

## File Structure Plan
Concrete paths; this drives task `_Boundary:_` annotations.

### New Files
- `internal/plugins/features/example/classifier.go` — decision logic

### Modified Files
- `path/to/existing.go` — what changes and why

## Components and Interfaces

| Component | Package | Intent | Requirements |
|-----------|---------|--------|--------------|
| ExampleComponent | `internal/plugins/features/example` | One-line responsibility | 1.1, 1.2 |

Add a detail block only for a component that introduces a new contract:

#### [Component Name]
```go
type Decider interface {
	Decide(ctx context.Context, in Input) (Decision, error)
}
```
- Invariants and failure behaviour
- State (process-local unless a V1 requirement needs persistence)

## Data Model (optional)
Only when the slice adds or changes persisted state; list tables, keys, and the `dbparity` registration.

## Error Handling
How the slice fails: fail-open or fail-closed, and what the caller sees.

## Testing Strategy
Follow Test Proportionality in `.kiro/steering/testing.md`.
- Table tests: [decision cases from the acceptance criteria]
- Integration: [one test per seam the slice touches]
- Fail-open/failure: [the degradation path]
- Existing generic architecture rules extended: [rule and the entry added, or none]

## Configuration (optional)
The config subtree and its defaults; the feature is off or inert by default unless a requirement says otherwise.
