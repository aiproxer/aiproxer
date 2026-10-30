# Research Notes

## Purpose

This research supports issue #323 and the `agent-runtime-routing-composition-safety` specification. It asks one narrow architectural question:

> How should Go-LIP distinguish ordinary inference endpoints from backends that encapsulate an agent/orchestration runtime, and where should routing reject unsafe composition without introducing provider-name switches or a second routing engine?

The repository baseline inspected for this spec is `main` at commit `d0224a9de48c2e6e5a57b559f4f755ee4029c95d`.

## Current Routing Shape

`internal/core/routing` already separates syntax from execution:

- `Parse` builds a `Selector` AST.
- A `Selector` is an ordered failover chain of `FailoverAlt`.
- A failover alternative contains exactly one primary, weighted group, or parallel group.
- Weighted branches can carry `[first]` and `[thinker]`; the narrow thinker hybrid can embed a parallel executor group.
- `CompileSelector` performs alias expansion, parsing, model-only defaulting, and unresolved-model-only rejection.
- `buildRoutePlan` calls `CompileSelector`, then native-model binding, affinity resolution, interleaved-state loading, request-size work, planning, admission, and ultimately backend `Open`.

This separation is useful: whether a backend is a model-like inference target or an orchestration runtime is not selector grammar. It is generation metadata about a referenced backend.

## Existing Preflight Seams

Two current paths already need the same selector semantics:

1. **Normal request execution** — `internal/core/runtime/executor_route_plan.go` invokes `routing.CompileSelector`.
2. **Admin A-leg route overrides** — `internal/infra/runtimebundle/routeoverride_generation.go` invokes `routing.CompileSelector` and `routing.RejectUnknownBackends` before accepting an override.

This argues for one pure semantic validation stage after compilation, rather than parser changes or a route-override-specific check.

Configured/default selectors can be validated at generation build/reload using the same stage. Aliases must still be validated after expansion at use time because regexp aliases can make a raw client string resolve to a composition that was not statically enumerated.

## Backend Factory Kind vs Configured Instance ID

The brownfield repository already has the distinction needed for a clean solution:

- backend registration metadata is keyed by **factory kind**;
- `LifecycleBackendFactory` receives a configured **instance ID**;
- `runtimebundle.buildBackends` explicitly computes `fid := p.FactoryID()` and `iid := p.InstanceID()`;
- executor routing is keyed by `iid`;
- model inventory rows retain both `BackendID: iid` and `Kind: fid`.

Therefore execution semantics can be declared once at the factory/export boundary and projected during generation assembly into an immutable `instanceID -> execution class` view used by routing.

This is preferable to teaching core about connector kinds.

## Why Existing Metadata Is Not the Right Signal

### Backend security profile

`pkg/lipsdk.BackendSecurityProfile` describes credential mode and access scope. A local-only backend may be ordinary local inference (for example llama.cpp), while a cloud-backed or local agent runtime can own orchestration state. Security posture and execution semantics are orthogonal.

### Process sharing / discovered-plugin provenance

`per_instance` and `shared_artifact` describe host process lifecycle. Many ordinary inference connectors are executable plugins, and some agent runtimes may use either process strategy. “Discovered connector” is not synonymous with “agent runtime.”

### Canonical capability flags

Capabilities such as tools, reasoning, streaming, and structured outputs describe what canonical input/output semantics a backend can represent. A normal inference model can support tools, while an agent runtime can hide its own SDK-native tools/MCP behind a text stream. Treating `Tools=true` or similar as an orchestration signal would be wrong.

### Provider or backend names

Hard-coding `acp`, `cursor`, `codex`, or suffix/prefix patterns into core would recreate the central provider matrix that recent architecture work is intentionally removing. It would also make third-party connectors unsafe by default in unpredictable ways.

## Per-Export Granularity Is Required

The Codex external connector proves that classification cannot live only at plugin-artifact level. Its manifest exports both:

- `openai-codex` — direct access to the ChatGPT Codex Responses inference service;
- `openai-codex-app-server` — the Codex App Server execution mode.

They share one connector artifact but have different execution semantics. The execution class therefore belongs to each exported factory kind.

The same pattern is likely to recur for future products that expose both an inference API and an agent runtime.

## Why Failover Must Be Included

Go-LIP's current B2BUA safety boundary permits transparent recovery only before client-visible canonical output is committed. That is sufficient for ordinary inference retry/failover semantics but is not sufficient to prove that an agent runtime has had no external effect.

The Cursor SDK backend is a concrete counterexample. Its documented behavior allows SDK-native tools and configured MCP servers to execute inside the Cursor agent; those operations are not replayed as canonical frontend tool calls.

A failure can therefore occur in this order:

1. agent runtime accepts the prompt;
2. the runtime edits files, executes a command, or invokes MCP;
3. no client-visible token has yet been emitted;
4. the runtime fails;
5. core sees a pre-output recoverable failure;
6. ordinary failover dispatches another backend;
7. the second backend repeats or conflicts with the first side effect.

“Pre-output” and “side-effect-free” are different properties. Until a backend can explicitly prove a stronger retry property, agent-runtime failover should be denied in the default-safe policy.

## Chosen Semantic Model

Use a small explicit execution taxonomy:

- `inference` — model-like request/response execution suitable for existing Go-LIP routing composition semantics;
- `agent_runtime` — an orchestration/agent runtime that can own hidden session/workspace/tool execution state;
- `unknown` — compatibility state for legacy/third-party registrations that do not declare the new metadata.

`unknown` is not an authored positive class. It is the conservative effective value for missing metadata.

### Safe policy

Under the default `safe` policy:

- one direct primary targeting `inference` is allowed;
- one direct primary targeting `agent_runtime` is allowed;
- one direct primary targeting `unknown` remains allowed for backward compatibility;
- any weighted, parallel, thinker, or ordered-failover composition that can reach `agent_runtime` is rejected;
- any such composition that can reach `unknown` is also rejected;
- inference-only compositions are unchanged.

Global TTFT/affinity parameters and leaf query parameters do not make a single-primary selector “composed.”

An explicit operator policy `unrestricted` restores the current behavior exactly. There is no client-controlled/per-selector bypass.

## Official Classification Direction

The implementation must derive the final list from owned factory/export metadata rather than a core switch. Based on current semantics:

### Agent-runtime class

Expected official agent-runtime exports include ACP/whole-agent harness paths such as:

- generic ACP;
- Cursor CLI ACP;
- Gemini CLI ACP;
- agent/harness wrappers exported through ACP;
- `openai-codex-app-server`;
- `cursorsdk`.

The precise official inventory shall be asserted from manifests/registration sources during implementation, not duplicated in routing.

### Inference class

Examples include:

- essential hosted inference backends;
- compatible protocol-family/provider profiles;
- `openai-codex`;
- `opencode-go` and `opencode-zen` (their connector kinds point to OpenCode Zen inference endpoints);
- local inference runtimes such as llama.cpp/Ollama/vLLM/LM Studio;
- ordinary hosted inference/aggregator connectors.

This is specifically intended to prove that local-only, process-spawning, and discovered connectors are not automatically classified as agent runtimes.

## Metadata Placement Options Considered

### Option A: hard-coded routing allow/deny list

Rejected. It violates core/provider isolation, scales poorly, and cannot classify third-party exports.

### Option B: infer from security/process/capability metadata

Rejected. Each candidate signal has counterexamples and mixes unrelated concerns.

### Option C: add an `agent_runtime` capability to `pkg/lipapi`

Rejected. This is backend execution topology/policy metadata, not a canonical request/event capability that frontends should negotiate.

### Option D: add a runtime backend-plugin gRPC ABI field

Rejected for the first implementation. The host must know the class before execution, and the closed installation manifest already describes each exported factory. A runtime ABI field would add negotiation/versioning surface without solving an earlier problem.

### Option E: explicit factory/export registration metadata

Chosen. Add a focused execution profile to SDK/registration metadata and an `execution_class` field to executable connector export metadata. Runtimebundle projects it to configured backend instances.

## Manifest Compatibility

The current closed `golip.backendplugin.manifest/v1` parser uses `DisallowUnknownFields`, so the host parser and manifest type must be updated together for the additive field.

Compatibility rules:

- existing manifests that omit `execution_class` continue to parse and resolve to `unknown`;
- official manifests are migrated to explicit `inference` or `agent_runtime`;
- invalid non-empty values fail manifest validation;
- a new manifest containing the field is naturally intended for a host release that knows the field; this spec does not introduce a backend-plugin protocol-major/minor change because no runtime wire behavior changes.

A manifest-schema v2 is intentionally not introduced for one additive installation-metadata field.

## Generation and Reload Behavior

Execution-class metadata and composition policy belong to the immutable runtime generation:

- factory/export metadata is process-start/discovery input;
- configured backend rows project classes to the current generation's instance IDs;
- `routing.execution_composition_policy` is generation config;
- in-flight requests keep their already-built route plan and are not reinterpreted;
- later turns use the new generation after reload;
- stored A-leg route overrides remain raw selectors and are not rewritten or cleared. If a later generation makes one unsafe, that later turn fails preflight.

This matches existing route-override and generation semantics.

## Error and Diagnostics Boundary

The rejection should be a typed routing/core error carrying bounded semantic facts such as:

- composition kind;
- configured backend instance ID;
- execution class;
- active policy.

It must not need the raw selector, prompt, workspace, or provider-private state.

Frontends should map it through existing execution-error plumbing to an invalid-request / HTTP 400-class response. No new `pkg/lipapi.Call` field or provider-specific wire contract is required.

## Deferred Refinements

Not in the first implementation:

- per-mode flags such as `allow_weighted` / `allow_failover`;
- a claimed `pre_output_side_effect_free` or idempotency capability;
- transactional workspace rollback across agent runtimes;
- client-controlled safety bypass;
- provider-specific exceptions;
- automatic conversion of agent runtimes into safe thinker-only modes.

If real use cases justify selective composition later, extend declarative semantics from evidence, not exceptions.

## Validation Targets

The implementation should prove at least:

- direct inference/agent-runtime/unknown routes remain usable in safe mode;
- every composite class is rejected for agent-runtime/unknown and unchanged for inference-only routes;
- `unrestricted` is legacy-compatible;
- aliases are checked after expansion;
- configured instance IDs are classified correctly even when different from factory kinds;
- direct `openai-codex` and `openai-codex-app-server` receive different classes from one plugin;
- an ordinary discovered/local inference connector remains composable;
- invalid admin overrides do not mutate the override store;
- runtime rejection happens before backend open, billing authorization, weighted/session mutation, affinity mutation, or interleaved-state load;
- no routing/core code contains provider-name classification;
- no backend-plugin gRPC ABI or canonical `lipapi` model change is required.
