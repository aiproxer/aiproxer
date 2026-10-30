# Implementation Gap Analysis: control-plane-principal-scope

Date: 2026-07-01

## Analysis Status

- Requirements are generated but not yet approved in `spec.json`; this analysis proceeds because gap validation can inform requirement or design revisions.
- No external dependency research was needed. The feature is mainly a brownfield contract and lifecycle propagation change within existing auth, execution context, secure-session, usage, and traffic observer surfaces.
- Core steering emphasizes stable plugin facades, explicit transport auth/principal attachment, secure-session authority before backend execution, and no provider/transport leakage into core contracts.

## Current State Investigation

### Existing assets

- Transport auth middleware already attaches a `PrincipalView` at the HTTP edge via `httpauth.WithPrincipal` and preserves frontend id in context (`internal/stdhttp/auth/middleware.go:100`, `internal/stdhttp/auth/middleware.go:115`).
- `pkg/lipsdk/execview.PrincipalView` is the current safe plugin-facing identity view with `ID`, `DisplayName`, `Roles`, and `Claims` (`pkg/lipsdk/execview/views.go:3`).
- `execctx.Views` already bundles per-request snapshots for principal, session, attempt, workspace, and annotations, with map/slice copies to avoid caller mutation (`internal/core/execctx/views.go:16`, `internal/core/execctx/views.go:25`).
- Secure execution preparation reads transport principal, creates a synthetic local principal when configured, runs session/workspace/route-hint stages, and attaches `execctx.Views` after secure-session BeginTurn (`internal/core/runtime/executor_prepare_secure.go:62`, `internal/core/runtime/executor_prepare_secure.go:70`, `internal/core/runtime/executor_prepare_secure.go:351`, `internal/core/runtime/executor_prepare_secure.go:391`).
- Secure-session Manager requires a principal id before authorizing a turn, binding session authority to identity rather than client hints (`internal/core/securesession/app/manager.go:37`, `internal/core/securesession/app/manager.go:43`).
- Secure-session domain already has `PrincipalRef{ID, Issuer, Tenant}` and `WorkspaceRef{ID}` for session binding (`internal/core/securesession/domain/types.go:22`).
- Auth decisions include `DeviceIdentity`, `Challenge`, `Principal`, `SatisfiedLevel`, and `ReasonCode` (`pkg/lipsdk/auth/decision.go:5`, `pkg/lipsdk/auth/decision.go:22`).
- Auth audit events intentionally exclude raw bearer/API/OAuth/resume secrets and emit safe principal/device fields (`pkg/lipsdk/auth/events.go:22`, `pkg/lipsdk/auth/events.go:32`).
- Local API key auth validates configured key id and principal id, returns principal id plus device key id/fingerprint, and does not expose raw key material (`internal/core/auth/local_apikey.go:75`).
- Local no-op auth emits an explicit OS-derived or fallback principal (`internal/core/auth/local_noop.go:11`, `internal/core/auth/local_noop.go:45`).
- Usage and traffic observer contracts already carry `PrincipalID` but not richer scope fields (`pkg/lipsdk/usage/observe.go:8`, `pkg/lipsdk/traffic/observe.go:24`).
- Session and workspace views already expose session labels, workspace id/project root/labels/markers, and are used by feature stages (`pkg/lipsdk/session/view.go:5`, `pkg/lipsdk/workspace/view.go:3`).

### Existing patterns and constraints

- Public feature-facing data belongs in `pkg/lipsdk/*`; core execution uses internal `execctx.Views` to carry immutable per-request snapshots.
- Transport/auth code can use HTTP types; core and SDK contracts must not expose HTTP headers, provider SDK types, SQL handles, raw bearer material, or frontend wire details.
- Secure-session must remain the authority boundary before backend execution; client-provided session/resume fields are hints until BeginTurn validates or creates proxy-owned state.
- Existing safe audit events sanitize claim values by emitting claim names with empty values (`internal/stdhttp/auth/adapter.go:263`).
- Synthetic local principal behavior is currently an executor flag derived from single-user local mode and memory secure-session store (`internal/infra/runtimebundle/build.go:270`).
- Current observer surfaces are intentionally minimal, so expanding attribution must be backward-compatible or additive.

## Requirement-to-Asset Map

| Requirement | Existing assets | Gap classification | Notes |
| --- | --- | --- | --- |
| R1 Authoritative snapshot | `execctx.Views`, `PrincipalView`, synthetic local principal | Missing / Partial | Current view is principal-only and lacks subject category, explicit unknown-vs-empty semantics, and one named `principal/scope` snapshot. |
| R2 Trusted source boundaries | `stdhttp/auth`, `httpauth`, `secure-session BeginTurn`, access posture checks | Partial / Constraint | Trust boundary exists, but scope attribution from operator-controlled sources is not formalized beyond principal/workspace/session labels. |
| R3 Stable attribution coverage | `PrincipalView`, `DeviceIdentity`, `SessionView.Labels`, `WorkspaceView.Labels`, `PrincipalRef.Tenant` | Missing / Partial | No stable tenant/org/project/department/cost-center/policy-label bundle exposed as one model; credential id lives in auth decision/device path only. |
| R4 Lifecycle propagation | `execctx.WithViews`, secure prepare flow, auxiliary paths, attempt stream usage emission | Partial | Context propagation exists, but only current `PrincipalView` is propagated; derived/internal request marking and richer scope inheritance need definition. |
| R5 Safe read-only exposure | `execview`, `execctx.copyViews`, auth event sanitization | Partial | Copies prevent map/slice mutation, but there is no richer safe scope view nor explicit safety classification for attribution fields. |
| R6 Audit-safe evidence | `AuthDecisionEvent`, `SessionStartEvent`, usage/traffic observers, secure session attempt trace | Partial | Audit events and observers carry principal id only; richer scope correlation is absent. |
| R7 Compatibility | Additive SDK patterns and stable current principal id fields | Constraint | Must preserve `PrincipalView.ID` consumers and current protocol wire shapes. |
| R8 Explicit exclusions | N/A | Constraint | Design must avoid adding provisioning, billing, redaction engines, policy engines, GUI, or reporting behavior. |

## Missing Capabilities

1. **Canonical scope view**: No single protocol-neutral snapshot covers subject category, auth method, credential id, tenant/org, workspace, project, department, cost center, and policy labels.
2. **Presence semantics**: Existing string fields cannot reliably distinguish unknown from known-empty for future reporting and policy attribution.
3. **Credential attribution**: `DeviceIdentity.KeyID` is emitted in auth decisions but not propagated into execution views, usage, traffic, secure session, or diagnostics as stable attribution.
4. **Subject type**: Human/service/local/unknown identity categories are not modeled in the current SDK view.
5. **Scope safety policy**: Existing events sanitize claims ad hoc; there is no shared classification for which scope/claim/label values are feature-safe vs audit-safe.
6. **Auxiliary/internal provenance**: Requirements mention internally derived requests; current auxiliary context has depth/suppression concepts but no principal/scope marker visible as attribution.
7. **Observer enrichment path**: Usage and traffic observer contracts only carry `PrincipalID`; future usage/budget/admin features need richer scope without breaking existing observers.
8. **Operator-controlled attribution inputs**: Config/local auth records currently contain key id/principal id only; there is no stable place for optional tenant/project/department/cost-center metadata.

## Integration Challenges

- **Public contract stability**: `pkg/lipsdk/execview.PrincipalView`, `pkg/lipsdk/usage.Event`, and `pkg/lipsdk/traffic.Observation` are public-ish plugin surfaces; changes should be additive and preserve existing fields.
- **Context value discipline**: The repo already uses `execview.WithPrincipal` and `execctx.WithViews`; adding another context value risks split-brain identity unless design defines a single canonical snapshot and adapters.
- **Secure-session coupling**: Secure-session uses `domain.PrincipalRef{ID, Issuer, Tenant}`. Rich scope should not force secure-session to own billing/project/department semantics.
- **Audit safety**: Richer attribution increases leakage risk. Design needs clear safe-vs-sensitive field handling before any value reaches logs, diagnostics, usage, or traffic observers.
- **Local mode semantics**: There are two related concepts today: `LocalNoOpAuthenticator` and executor `SyntheticLocalPrincipal`. Design should avoid creating a third local identity behavior.
- **Unknown vs empty**: Adding presence semantics to every field may bloat public types. Design should choose a minimal, testable representation.

## Implementation Approach Options

### Option A: Extend Existing Views In Place

Extend `execview.PrincipalView`, `execctx.Views`, auth decision/events, usage/traffic events, and secure-session mapping with additive fields.

**Modules likely touched**
- `pkg/lipsdk/execview/views.go`
- `internal/core/execctx/views.go`
- `pkg/lipsdk/auth/decision.go`, `pkg/lipsdk/auth/events.go`
- `internal/stdhttp/auth/adapter.go`
- `internal/core/runtime/executor_prepare_secure.go`
- `pkg/lipsdk/usage/observe.go`, `pkg/lipsdk/traffic/observe.go`

**Trade-offs**
- Pros: Smallest diff, aligns with current extension-stage view patterns, easy backward compatibility if fields are additive.
- Pros: Avoids another context value and reuses `execctx.WithViews` lifecycle.
- Cons: `PrincipalView` may become a catch-all identity/scope struct; harder to express scope as separate from principal.
- Cons: Unknown-vs-empty semantics may be awkward if represented as strings only.

**Effort/Risk**: M / Medium. Additive and aligned, but public surface changes need careful tests.

### Option B: Create A New Dedicated Scope Snapshot Contract

Add a new SDK package or type such as a `ScopeView` / `PrincipalScopeView`, then place it inside `execctx.Views` while keeping `PrincipalView` stable for compatibility.

**Modules likely touched**
- New `pkg/lipsdk/...` view type for principal/scope attribution.
- `internal/core/execctx/views.go` to carry the snapshot.
- Auth adapters to build the snapshot from trusted auth decisions and operator metadata.
- Runtime prepare path to pass the snapshot to secure-session, observers, and feature stages.

**Trade-offs**
- Pros: Clean separation between principal identity and broader attribution.
- Pros: Best fit for future usage/budget/policy/admin features without bloating `PrincipalView`.
- Pros: Can model presence/safety explicitly.
- Cons: More files and more design surface.
- Cons: Existing plugins that only inspect `PrincipalView` need bridging/migration to see richer scope.

**Effort/Risk**: M-L / Medium. Cleaner long-term, slightly more upfront contract design.

### Option C: Hybrid Compatibility Layer

Keep `PrincipalView` as-is, introduce a dedicated richer scope snapshot, and provide adapters that derive legacy principal fields from the richer snapshot plus helper methods for observers and events.

**Combination strategy**
- New richer scope snapshot is canonical for attribution.
- `PrincipalView` remains the compatibility subset and is populated from the canonical snapshot.
- Existing events retain current fields and optionally gain additive scope fields or a nested safe scope summary.
- Usage/traffic observers can gain additive scope fields or helper access via context before a broader observer contract revision.

**Trade-offs**
- Pros: Avoids breaking plugins and preserves current auth/session behavior.
- Pros: Prevents `PrincipalView` from becoming too broad while giving future features a stable model.
- Pros: Supports incremental rollout across auth, secure-session, usage, traffic, and diagnostics.
- Cons: Requires discipline to prevent canonical snapshot and legacy principal subset from diverging.
- Cons: More tests are needed to prove bridging consistency.

**Effort/Risk**: L / Medium. Most robust foundation for later enterprise features; broader integration but low external dependency risk.

## Feasibility and Complexity

- Overall feasibility: High. The codebase already has transport auth, safe principal views, secure-session authority, execution context snapshots, and observer contracts.
- Estimated effort: L (1-2 weeks). The feature touches public SDK contracts, auth events, runtime lifecycle propagation, secure-session mapping, and observer metadata.
- Risk: Medium. Main risks are public surface compatibility, secret leakage through richer fields, and split-brain identity if multiple context values are introduced.
- Complexity signal: Cross-cutting workflow and contract evolution, not algorithmically hard and no new external service dependency.

## Recommendations For Design Phase

- Prefer **Option C** unless design finds a simpler additive-only path that preserves separation between principal identity and broader attribution.
- Define one canonical attribution snapshot and one legacy compatibility projection; do not let auth, execctx, usage, and diagnostics each build their own scope maps.
- Keep `PrincipalView.ID` stable and additive to avoid breaking existing feature plugins.
- Treat credential id, tenant, project, department, cost center, roles/scopes, and policy labels as safe only after explicit classification; avoid passing raw claim values by default.
- Keep secure-session focused on identity/session authority; pass only the principal fields it needs instead of making it own all control-plane attribution.
- Revalidate secure-session denial paths, session-start audit, auth decision events, usage observers, traffic observers, and local no-auth posture.

## Research Needed During Design

1. Decide the exact presence representation for unknown vs known-empty fields without overcomplicating SDK types.
2. Decide whether richer attribution reaches usage/traffic observer structs directly as additive fields or remains accessible from context for this spec.
3. Decide how operator-controlled scope metadata is configured for local API keys and external auth providers without designing full directory management.
4. Decide how auxiliary/internal request provenance is represented in the scope snapshot and how it composes with plugin suppression/depth metadata.
5. Confirm whether secure-session durable records should store tenant/workspace/project attribution now or only expose it through request lifecycle evidence.

## Files Most Likely Relevant In Design

- `pkg/lipsdk/execview/views.go`
- `pkg/lipsdk/execview/context.go`
- `internal/core/execctx/views.go`
- `internal/core/execctx/submit_views.go`
- `internal/core/runtime/executor_prepare_secure.go`
- `internal/core/runtime/attempt_stream.go`
- `pkg/lipsdk/auth/decision.go`
- `pkg/lipsdk/auth/events.go`
- `internal/core/auth/local_apikey.go`
- `internal/core/auth/local_noop.go`
- `internal/stdhttp/auth/adapter.go`
- `internal/stdhttp/auth/middleware.go`
- `internal/core/securesession/app/manager.go`
- `internal/core/securesession/domain/types.go`
- `pkg/lipsdk/usage/observe.go`
- `pkg/lipsdk/traffic/observe.go`

---

# Design Discovery and Synthesis Update

Date: 2026-07-01

## Summary

- **Feature**: `control-plane-principal-scope`
- **Discovery Scope**: Extension
- **Key Findings**:
  - Existing `PrincipalView`, auth middleware, secure-session, and `execctx.Views` are sufficient integration points.
  - A richer scope contract is needed, but existing `PrincipalView` must remain the compatibility projection.
  - No external dependency is needed; this is an additive SDK/core contract and propagation change.

## Research Log

### Extension Point Analysis
- **Context**: Design must fit existing auth and execution lifecycle without changing client protocols.
- **Sources Consulted**: `pkg/lipsdk/execview`, `pkg/lipsdk/auth`, `internal/stdhttp/auth`, `internal/core/execctx`, `internal/core/runtime`, `pkg/lipsdk/usage`, `pkg/lipsdk/traffic`.
- **Findings**:
  - `execctx.Views` is the right lifecycle carrier for immutable request views.
  - `httpauth.AuthenticationResult` is the right edge carrier for auth-provider scope data.
  - Usage and traffic observer structs can be extended additively.
- **Implications**: Design uses a new public scope view plus compatibility projection rather than replacing existing principal fields.

### Design Synthesis
- **Generalization**: Subject identity, credential attribution, tenant/project labels, and observer correlation are all one attribution problem, not separate feature-specific maps.
- **Build vs Adopt**: Build a small typed SDK contract; no external identity library fits the proxy's safe, protocol-neutral attribution needs.
- **Simplification**: Avoid new storage, admin APIs, billing hooks, or policy engines. Keep this spec to request-lifecycle attribution only.

## Architecture Pattern Evaluation

| Option | Description | Strengths | Risks / Limitations | Notes |
|--------|-------------|-----------|---------------------|-------|
| Extend `PrincipalView` only | Add every scope field to the existing principal view | Smallest diff | Bloats identity-only type and weakens separation | Rejected for long-term enterprise scope clarity |
| New scope only | Replace principal usage with a new scope view | Clean model | Breaks existing feature consumers | Rejected for compatibility risk |
| Hybrid compatibility | Add scope view and derive existing principal view from it | Stable migration, clear authority | Requires consistency tests | Selected |

## Design Decisions

### Decision: Add `pkg/lipsdk/scope` As The Safe Attribution Contract
- **Context**: Requirements need broader attribution than existing principal identity.
- **Alternatives Considered**:
  1. Expand `PrincipalView` with all scope fields.
  2. Add a dedicated scope view and bridge principal compatibility.
- **Selected Approach**: Add a dedicated `PrincipalScopeView` and derive `PrincipalView` from it.
- **Rationale**: Keeps old consumers stable while preventing principal identity from becoming a catch-all enterprise metadata type.
- **Trade-offs**: More files and tests, but clearer boundaries.
- **Follow-up**: Verify no split-brain identity between scope and principal projection.

### Decision: Use Presence-Aware String Values
- **Context**: Requirements distinguish unknown from known-empty.
- **Alternatives Considered**:
  1. Plain strings.
  2. Pointers.
  3. Small `Value{Known, Value}` type.
- **Selected Approach**: Use a small presence-aware `Value` type.
- **Rationale**: Explicit, serializable, testable, and does not require nil checks everywhere.
- **Trade-offs**: Slightly more verbose field access.
- **Follow-up**: Keep helper constructors small and avoid over-generalizing into nullable primitives beyond this contract.

### Decision: Add Scope To Observer Events Additively
- **Context**: Later usage and admin features need richer attribution.
- **Alternatives Considered**:
  1. Require observers to read scope from context.
  2. Add scope directly to existing observer event structs.
- **Selected Approach**: Add a safe scope field to usage and traffic observations while preserving `PrincipalID`.
- **Rationale**: Event records remain self-contained and backward-compatible.
- **Trade-offs**: Observer payloads become larger.
- **Follow-up**: Ensure no metric labels are created from high-cardinality scope values.

## Risks & Mitigations

- Split-brain identity between scope and principal projection — generate projection from scope and test consistency.
- Secret leakage through richer scope fields — define safe contract, sanitize auth events, and reject raw transport/credential values.
- Breaking plugin consumers — keep existing principal fields and observer fields additive.
- Scope creep into billing or policy engines — boundary section explicitly excludes enforcement and reporting features.
