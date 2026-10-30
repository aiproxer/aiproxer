# Brownfield Gap Analysis

## Status and Scope
Performed against `55e5d4412834a1948c418ac1923cca3c25576019`, using current owning source and the approved requirements. This is a behavior-preserving existing-system extension. Source details, authority/effect maps, policy matrix and sibling tips are recorded in `research.md`.

## Existing Implementation Inventory
- SDK manifest: 28 ordered plane declarations, each with request-access annotation.
- Existing generator: validates complete closed inventory and annotation classes, emits frozen SDK policies, installs one file atomically, supports read-only currency check.
- Core eligibility: separately handwritten count, ordered IDs and default access classes; complete-census compiler freezes fixed blocker masks.
- Standard composition: constructs the standard census, adds frozen feature/hook/port occupancy, builds the summary and concrete production authority gate.
- Defensive gate: seal/pin/summary checks plus independent typed-plane, hook, narrow-port and custom-port decisions.
- Existing generic currency/architecture and scoped quality checks: SDK artifact only.

## Gaps by Requirement

| Requirement IDs | Reuse | Actual gap |
| --- | --- | --- |
| 1.1, 1.5 | Validated manifest order already matches all current blocker positions | Count/IDs/access are still authored again in core; no derived core artifact |
| 1.2, 1.6 | Existing annotation/inventory validation and atomic writer | Selected outputs must validate before writes; new core capacity guard |
| 1.3, 1.4 | Complete-census rejection, stable index lookup and bounded labels | Preserve after replacing source of metadata; do not weaken validation |
| 2.1, 2.2, 2.3, 2.4, 2.5 | Conservative response/canonical planes, four hooks, protected authorities and narrow exemptions | Characterize/preserve, not a support or permission expansion |
| 3.1, 3.2 | Pure hook/port compile helpers and separately written equivalent gate conditions | Reuse predicates on current defensive facts; summary alone must not authorize |
| 3.3, 3.4 | Custom port classification/name checks and seal/generation reasons | Preserve separate defenses and ordering; no typed-manifest replacement |
| 4.1, 4.2, 4.3, 4.4, 4.5 | Static disposition, production builder, frozen SDK policy, generic currency tests | Extend currency to core output and prove existing first-consumer/immutability/resource contracts |

## Approaches Evaluated
1. **Extend existing generator, emit core-local metadata**: Removes handwriting without public API or runtime imports. Second output adds bounded check/install integration. Preferred for synthesis.
2. **Export frozen SDK projection**: Viable immutable source, but adds public surface or bridge for core. Bigger contract and projection change than required.
3. **Compose from mutable public descriptors**: Smaller apparent diff, but mutability becomes admission policy authority; not safe without additional capture machinery.
4. **Keep handwritten tables plus parity tests**: Detects drift without eliminating repeated edits; does not satisfy declaration-driven acceptance.

## Integration Boundaries and Risks
- Current 28 positions fit 32-bit plane mask. Guard overflow, do not enlarge the representation.
- Manifest order is policy-visible through diagnostics; preserve current order with an independent compatibility fixture. Future append support does not authorize adding a plane in this slice.
- All four hook chains and four response-only planes block. The response-hook getter comment is stale; implementation is authoritative.
- Compile-time full-census validation and defensive typed-plane inspection are not equivalent. Consolidate only equivalent hook/narrow-port predicates.
- Five protected typed planes and three custom-port name families are different sets; do not merge by inference or add control-tool substring policy.
- Existing `-out` custom callers must not start overwriting repository-default core output.
- Existing writer guarantees per-file replacement only; validate-before-install is not cross-file transactionality.
- Shared `authority_gate.go` edits with assessment simplification require rebaseline/revalidation, not a dependency. Lifecycle/proof siblings own their separate corrections; generation-token allocation stays out.

## Feasibility and Research Conclusion
No new external dependency, platform service, API verification, lifecycle owner or unresolved scope question is needed. A bounded emitter, serving census integration, command/currency extension and predicate reuse fit one delivery slice. Estimated implementation: four sequential 1-3 hour tasks, ~350-650 added non-test lines including core output, ~15-25 paths; estimate is not measured delivery evidence.

## Baseline Evidence
Current generator/SDK/largebody complete package suites, selected serving runtime/runtimebundle/frontend tests and existing SDK currency check pass. Existing architecture manifest-currency selections also pass. Exact commands and evidence limitations are in `research.md` and final `validation.md`. These results do not prove the proposed consolidation is implemented.
