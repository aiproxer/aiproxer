# Design Document

## Overview

This design extends Agent Loop Guard (ALG) with a preferred explicit-completion strategy while preserving the existing semantic-verifier strategy as a mutually exclusive legacy mode.

In `attempt_completion` mode, each eligible backend candidate receives two B-leg-only additions:

1. a byte-stable system/control instruction explaining the completion protocol; and
2. a familiar function tool named exactly `attempt_completion` with one required `result: string` argument.

The remote worker model uses that tool as an explicit completion handshake. The proxy privately consumes only the proxy-owned control call, records a generic trusted explicit-completion fact, optionally materializes `result` as final assistant text when no answer text has already been released, and lets the existing terminal-decision owner accept the terminal. If an eligible terminal arrives while the protocol was active but no completion signal was observed, ALG returns one bounded hidden protocol-repair continuation by default. A second unmarked stop is allowed rather than causing an unbounded hidden loop or invoking the semantic verifier.

The legacy strategy remains the current independent-verifier policy. The strategies do not call each other and cannot be composed concurrently.

### Goals

- Prefer an explicit completion handshake over a normal-path auxiliary semantic-verifier inference for new ALG deployments.
- Reuse the widely established `attempt_completion` model vocabulary from Cline/Roo/Kilo-style agents.
- Preserve the current semantic-verifier ALG strategy as a separately selectable compatibility path.
- Keep client/A-leg code unaware of proxy-owned completion-tool injection and calls.
- Preserve client `ToolChoice`, ordinary tool execution, streaming-first response behavior, no-post-commit replay, terminal ownership, accounting, and generation pinning.
- Reuse the existing normalized `ExplicitCompletion` terminal fact instead of creating an ALG-specific terminal authority.
- Add only the smallest generic SDK/runtime seam required for a model-visible/client-hidden proxy control tool.

### Non-Goals

- Prove semantically that every model self-certified completion is correct. Operators requiring independent semantic checking can select `semantic_verifier` instead.
- Run the explicit protocol and semantic verifier together or use one as fallback for the other.
- Inject hidden tools when doing so would broaden explicit client tool-choice restrictions.
- Implement a general arbitrary proxy-local tool runtime, MCP server, workflow engine, planner, DI/service locator, or tool sandbox.
- Replace transport recovery, terminal CAS/ownership, continuation transactions, routing, B2BUA, billing, secure-session authority, or frontend protocol adapters.
- Persist a new durable session record merely to remember the fixed completion instruction/tool.
- Parse completion from prose, XML, markdown, `[DONE]`, or provider-specific finish strings.

## Boundary Commitments

### This Spec Owns

- ALG strategy configuration and mutual-exclusion validation.
- `attempt_completion` tool/instruction policy and strict argument handling.
- Preferred-mode missing-signal decision policy, protocol state, and recovery wording.
- A narrow provider-neutral `controltool` SDK/feature plane and runtime stage for B-leg-only proxy control tools.
- Request-local activation/provenance, bounded control-call capture, normalized completion evidence, and pending result publication through existing canonical response ownership.
- Regression, architecture, observability, and compatibility certification for the new mode.

### Out of Boundary

- Generic terminal decision publication and terminal CAS ownership.
- Generic continuation admission/open/settlement and conversation-view continuation overlay lifecycle.
- Ordinary client tool execution semantics and client tool policy.
- Provider wire encoders except that they continue to translate the already-final canonical B-leg tool catalog normally.
- Durable conversation-view schema changes.
- New client/operator policy endpoints.
- Semantic-verifier redesign beyond preserving behavior under its explicit strategy.

### Allowed Dependencies

- `pkg/lipapi` canonical calls/events/items/tool definitions and explicit-completion helpers.
- `pkg/lipsdk/terminaldecision` and existing continuation/steering platform.
- New narrow `pkg/lipsdk/controltool` generic contract.
- `pkg/lipsdk/feature` typed plane composition.
- Existing request/session/scope/workspace views for bounded control-tool metadata.
- Existing response pipeline, attempt state, traffic/usage/recording, and terminal chokepoints only through generic integration.

### Revalidation Triggers

Re-run design validation if any of these change before implementation:

- attempt-transform/request-hook/post-hook rederivation ordering;
- conversation-view final reassertion or PTB/`Backend.Open` ordering;
- tool-call assembler/finalizer order;
- ordinary tool policy/reactor order;
- terminal-decision evidence or continuation intent contract;
- canonical `ToolChoice`/allowed-tools rules;
- backend capability resolution;
- active `b-leg-path-virtualization`, `large-payload-streaming-fast-path`, or another spec lands changes on the same seams.

## Existing Architecture and Brownfield Placement

The relevant current flow is approximately:

```text
accepted A-leg call
  -> candidate clone
  -> attempt transforms
  -> request hooks / shaping
  -> post-hook validation + candidate admission + context/accounting preflight
  -> final conversation-view reassertion
  -> final clamps/authorization/adaptation
  -> PTB capture
  -> Backend.Open
  -> backend events
  -> ordinary tool-call assembler/finalizers
  -> tool policies
  -> tool reactors
  -> response hooks
  -> client release
  -> provisional response_finished
  -> terminal-decision provider
  -> allow stop OR core-owned continuation transaction
```

The existing ALG contributes only `PlaneTerminalDecisionProvider`. The new preferred mode additionally needs one request/response-spanning generic capability. It must not be implemented by putting `agent-loop-guard` branches into the flow above.

### Why Existing Planes Are Insufficient

- `toolcatalog.Filter` is a remove/annotate contract and does not carry trusted request-local provenance for a privileged local action.
- A plain request transform cannot safely persist its private activation identity into the response path without abusing client/canonical metadata.
- Ordinary tool reactors execute after ordinary tool policy, too late to guarantee the hidden tool is never interpreted as a client tool.
- Completion gates buffer the whole completion until terminal, violating streaming-first behavior.
- Session openers only own labels, not model-visible instruction/tool projection.

Therefore V1 adds one narrow generic control-tool provider plane rather than overloading unrelated hooks.

## Selected Architecture

```mermaid
flowchart TD
    CFG[ALG config] --> STRAT{strategy}
    STRAT -->|semantic_verifier| LEG[Existing ALG verifier provider]
    STRAT -->|attempt_completion| ATP[Preferred ALG terminal provider]
    STRAT -->|attempt_completion| CTP[Control-tool provider]

    CTP --> PROJ[Generic candidate control-tool projection]
    PROJ -->|eligible| B[B-leg sees stable instruction + attempt_completion]
    PROJ -->|ineligible| BIN[B-leg unchanged; protocol inactive]

    B --> EV[Backend events]
    EV --> CLAIM{proxy-owned control call?}
    CLAIM -->|no| NORMAL[Existing streaming/tool pipeline]
    CLAIM -->|yes| CAP[Bounded private control-call capture]
    CAP --> HANDLE[ALG control handler]
    HANDLE --> FACT[Trusted explicit-completion fact + optional pending result]

    NORMAL --> TERM[Provisional terminal]
    FACT --> TERM
    BIN --> TERM
    TERM --> ATP
    ATP -->|explicit completion| STOP[Core terminal owner publishes final stop]
    ATP -->|active + missing signal + budget| CONT[Core continuation transaction]
    ATP -->|inactive / exhausted / unsafe| STOP
```

## Configuration and Strategy Model

Extend the existing ALG feature configuration without introducing a second feature ID:

```yaml
plugins:
  features:
    agent-loop-guard:
      enabled: true
      strategy: attempt_completion
      max_protocol_reprompts: 1
      no_progress_limit: 2
```

Legacy explicit configuration remains:

```yaml
plugins:
  features:
    agent-loop-guard:
      enabled: true
      strategy: semantic_verifier
      verifier_role: loop_guard
      verifier_timeout_seconds: 4
      max_semantic_continuations: 3
      no_progress_limit: 2
      explicit_completion_policy: trust
```

Existing configuration remains valid:

```yaml
plugins:
  features:
    agent-loop-guard:
      enabled: true
      # no strategy: resolves to semantic_verifier for backward compatibility
```

Disabled configuration:

```yaml
plugins:
  features:
    agent-loop-guard:
      enabled: false
```

### Strategy Type

```go
type Strategy string

const (
    StrategyAttemptCompletion Strategy = "attempt_completion"
    StrategySemanticVerifier Strategy = "semantic_verifier"
)
```

Extend `Config` with:

```go
Strategy               Strategy
MaxProtocolReprompts   int // default 1, supported 1..3 in V1
```

Retain current verifier fields unchanged.

### Validation Rules

- `enabled: false` contributes no terminal provider and no control-tool provider.
- Omitted strategy under an enabled legacy config resolves to `semantic_verifier`.
- New docs/examples always spell `strategy: attempt_completion` when recommending ALG.
- `attempt_completion` rejects explicit verifier-only keys (`verifier_role`, `verifier_timeout_seconds`, `max_semantic_continuations`, `explicit_completion_policy`).
- `semantic_verifier` rejects explicit `max_protocol_reprompts`.
- `no_progress_limit` remains a shared bounded breaker.
- Unknown strategies/keys fail generation construction.
- Presence-sensitive validation is performed during YAML decode so default values cannot make an unused strategy look configured.
- Programmatic config construction receives equivalent `Validate` checks through an explicit strategy-aware constructor path.

No runtime “auto” strategy exists. Mutual exclusion is structural.

## Generic `controltool` SDK Contract

Add `pkg/lipsdk/controltool` as a small provider-neutral contract. It represents one optional proxy-owned model control tool, not a generic application tool runtime.

### Core Types

Equivalent public shape:

```go
package controltool

type Provider interface {
    ID() string
    Spec() Spec
    Handle(context.Context, CompletedCall, Meta) (Outcome, error)
}

type Spec struct {
    Tool         lipapi.ToolDef
    Instruction Instruction
    MaxArgsBytes int
}

type Instruction struct {
    Role lipapi.Role // V1: system or developer only
    Text string
}

type CompletedCall struct {
    ToolCallID string
    ToolName   string
    ArgsJSON   []byte
}

type Meta struct {
    TraceID      string
    ALegID       string
    BLegID       string
    CandidateKey string
    AttemptSeq   int
    Scope        scope.PrincipalScopeView
    Session      session.SessionView
    Workspace    workspace.WorkspaceView
}

type OutcomeKind uint8
const (
    OutcomeInvalid OutcomeKind = iota
    OutcomeComplete
)

type Outcome struct {
    Kind       OutcomeKind
    ResultText string
    ReasonCode string
}
```

Validation/bounds:

- one non-empty provider ID;
- one bounded tool definition with valid JSON Schema;
- one bounded stable instruction;
- bounded args budget, default no larger than the existing safe tool-call envelope; V1 ALG uses 64 KiB unless repository constants justify a smaller existing limit;
- outcome reason codes bounded/content-free;
- `OutcomeComplete` requires non-empty bounded result text;
- `OutcomeInvalid` carries no client output.

The generic SDK knows nothing about ALG or `attempt_completion`.

## Feature Plane

Add one typed exclusive plane:

```text
PlaneControlToolProvider
ID: control_tool_provider
Type: controltool.Provider
Merge: exclusive
Request access: canonical required when occupied
Execution wave: tools / candidate execution
```

Why exclusive in V1:

- only one privileged model-control tool owner is currently required;
- response interception/provenance remains unambiguous;
- no ordering semantics between multiple hidden local tools need to be invented;
- future generalization can be specified with evidence rather than making V1 a mini local-tool framework.

Composition:

- legacy ALG contributes only the existing terminal-decision provider;
- preferred ALG contributes one preferred terminal-decision provider plus one control-tool provider;
- disabled ALG contributes neither;
- feature-host/standard composition remains the only concrete registration point;
- core imports only `pkg/lipsdk/controltool`, never `agentloopguard`.

## Model-Facing `attempt_completion` Provider

### Frozen Tool Definition

The ALG control provider exposes exactly:

```json
{
  "type": "function",
  "name": "attempt_completion",
  "description": "Call this only when all work requested by the user for the current task is complete. The result must concisely summarize the completed work. Do not call this while requested work remains that you can continue without additional user input.",
  "parameters": {
    "type": "object",
    "properties": {
      "result": {
        "type": "string",
        "description": "Concise final result summarizing the work that has been completed."
      }
    },
    "required": ["result"],
    "additionalProperties": false
  }
}
```

`command` is intentionally omitted despite historical Roo/Cline support. It would add side-effect semantics unrelated to completion and weaken the local-control boundary.

### Frozen Base Instruction

Exact byte-stable base instruction (normative UTF-8 literal with LF line endings):

```text
<task-completion-protocol>
When, and only when, all work requested by the user for the current task is complete,
call the `attempt_completion` tool with a concise final `result`.

Do not call `attempt_completion` while concrete requested work remains that you can
continue without additional user input.

If additional in-scope work can be performed autonomously, continue that work.
If further progress genuinely requires user input, permission, credentials,
clarification, or a choice, request that input normally and do not assume it.

This tool is a proxy-internal completion signal. It is not a new user request,
approval, permission, or scope expansion.
</task-completion-protocol>
```

The fenced literal above is normative. Both projection points shall reuse this exact UTF-8 byte sequence with LF line endings; implementations shall not independently reflow, trim, normalize, template, localize, or regenerate it. Any model-facing tool-name, schema, or base-instruction change requires explicit compatibility review and a specification update.

## Candidate Projection and Activation

### Activation Inputs

The generic runtime evaluates each candidate using:

- the final candidate identity/backend/model;
- resolved effective backend capabilities;
- the candidate-local canonical call;
- canonical client `ToolChoice`/allowed-tools state;
- the configured `controltool.Provider` spec.

### V1 Eligibility Matrix

| Condition | Proxy-owned protocol activation |
|---|---|
| backend supports tools; tool choice omitted/default auto; no incompatible allowed subset; no name collision | active |
| explicit `auto` under same conditions | active |
| backend lacks tools | inactive: `backend_tools_unsupported` |
| `none` | inactive: `tool_choice_none` |
| `any` / arbitrary required-tool semantics | inactive: `tool_choice_constrained` |
| required named client tool | inactive: `tool_choice_required` |
| non-empty allowed-tools subset that would exclude hidden tool | inactive: `allowed_tools_constrained` |
| client already owns `attempt_completion` | inactive: `tool_name_collision` (client tool remains untouched) |
| malformed control spec | candidate generation/build failure, not per-request best effort |

V1 deliberately chooses a smaller safe activation set instead of inventing equivalence rules for every provider's tool-choice dialect.

### Two-Point Projection

The control projection must satisfy both accurate preflight/accounting and final B-leg stability:

1. **Post-hook projection**: after ordinary candidate/request feature mutation is complete, but before the authoritative post-hook candidate admission/context/accounting preflight. This projection determines activation and inserts the stable control instruction/tool into the candidate clone so downstream sizing, requirements, and accounting see the real model-facing request.
2. **Final reassertion**: after conversation-view final reassertion and other known late canonical reconstruction, but before candidate adaptation/PTB capture/`Backend.Open`. This reasserts only the already-approved byte-identical control projection. If it cannot reproduce the approved projection without changing eligibility/semantics, the candidate fails closed before upstream open.

This is analogous to the repository's existing need to freeze/reassert model-visible state without putting feature-specific policy into core.

### Authority-Neutral Instruction Projection

A generic pure helper inserts one complete control instruction into the backend-effective trajectory without conflicting authorities:

- legacy/message authority: place the instruction in the stable system/developer instruction prefix;
- item authority: materialize the same normative instruction text as a leading system/developer message item before mutable user/assistant history;
- never mutate A-leg baseline/history;
- never accumulate multiple copies within one candidate;
- final reassertion reproduces the exact approved instruction bytes, tool-definition bytes, and relative order from the post-hook projection.

Tool definition is appended to the backend-effective tool catalog only after collision and ToolChoice checks pass. The client's original ToolChoice value is not rewritten.

### Activation State

Runtime stores request/attempt-local trusted state outside client data:

```go
type controlToolActivation struct {
    ProviderID  string
    ToolName    string
    MaxArgs     int
    Active      bool
    ReasonCode  string
}
```

The activation is pinned to the attempt/generation and is never reconstructed from a tool name emitted by the model. It is not serialized to frontend or backend metadata.

## Response Interception

### Placement

Control-tool interception occurs on backend canonical events **before** the ordinary tool-call assembler/finalizers and before ordinary tool policy/reactors.

This ordering is binding:

```text
backend event / BTP observation / provider usage
  -> proxy control-tool capture (only if trusted activation matches)
      -> claimed: private bounded capture/handle; no client event
      -> not claimed: existing ordinary tool assembler/finalizers
          -> ordinary tool policy
          -> tool reactors
          -> response hooks
          -> client release
```

Raw B-leg traffic capture may observe the upstream control call under existing operator traffic-capture policy; A-leg/client/PTC output must not.

### Capture State

Attempt-local capture tracks only the active control call:

```go
type capture struct {
    callID      string
    started     bool
    finished    bool
    args        bounded buffer
    invalid     bool
}
```

Rules:

- a `tool_call_started` matching the prepared tool name establishes the captured call ID;
- later name-less args/finish fragments correlate only by that call ID;
- only one control call may be active per response in V1;
- args overflow, duplicate start/finish, malformed lifecycle, or multiple control calls mark protocol invalid and are swallowed rather than leaked to client execution;
- non-control tool calls/events proceed normally;
- an unresolved ordinary client tool boundary prevents the control completion from becoming unconditional terminal evidence.

### Handler Semantics

ALG's concrete handler decodes strict JSON using duplicate/unknown-field rejection:

```go
type args struct {
    Result string `json:"result"`
}
```

Validation:

- exactly one JSON object;
- exactly key `result`;
- string value;
- trimmed non-empty result;
- valid UTF-8;
- bounded to the existing assistant-text/result maximum;
- no trailing JSON value;
- no `command`, status, approval, or additional metadata.

Expected model mistakes return `OutcomeInvalid` with a bounded reason; internal handler failure returns an error. Neither path falls through to client tool execution.

## Completion Evidence and Pending Result

Extend generic terminal evidence additively:

```go
type Evidence struct {
    // existing fields...
    ExplicitCompletion         bool
    ExplicitCompletionExpected bool
}
```

Runtime projection sets:

- `ExplicitCompletionExpected = true` only when this attempt had a successfully active proxy control protocol;
- `ExplicitCompletion = lipapi.HasExplicitCompletion(nativeItems) || validProxyControlCompletion`.

The legacy semantic-verifier provider ignores `ExplicitCompletionExpected`; the preferred provider consumes it.

A valid proxy control action is recorded as an internal completed control fact, not a synthetic client-visible ToolCall/ToolResult. This is semantically equivalent to the completed local execution that `HasExplicitCompletion` demands from an ordinary harness-owned completion tool.

### Pending Result Text

The response pipeline retains a bounded `pendingCompletionResult` only for a valid proxy completion.

At accepted terminal publication:

- if no meaningful client-visible assistant text has been committed, core emits the result through the existing canonical response release/recording/traffic path before `response_finished`, synthesizing only any lifecycle events needed to maintain canonical sequence legality;
- if assistant text was already committed, do not emit the result automatically; it remains completion evidence/summary and avoids duplicate answers;
- result text is never emitted if the completion outcome is invalid or the terminal is not accepted.

This flush is owned by the generic terminal/response path. ALG does not write directly to a frontend.

## Preferred Terminal Policy

Implement strategy dispatch inside the feature provider; do not add strategy branches to core.

### Decision Order

For `attempt_completion`:

1. Validate context/input.
2. Authoritative cancellation/refusal/filter/non-recoverable causes -> allow stop.
3. Trusted `ExplicitCompletion` -> allow stop immediately (subject to existing terminal safety).
4. Pre-output transport/empty failure -> existing stream-recovery policy remains authoritative; no semantic protocol reprompt competes with it.
5. Unsafe partial ordinary tool state / missing resumable trajectory -> allow stop.
6. `ExplicitCompletionExpected == false` -> allow stop with bounded `completion_protocol_inactive` reason; never call verifier.
7. If protocol reprompt budget/no-progress breaker is exhausted -> allow stop.
8. Otherwise build one bounded missing-signal continuation intent from the existing user objective and retained trajectory.

No auxiliary request is issued anywhere in this strategy.

### Protocol State

Do not overload the legacy `alg-state-v1` semantic-verifier state token. Add a feature-local V1 protocol token with a distinct prefix, for example:

```text
alg-proto-v1.<bounded opaque state>
```

State contains only bounded counters and a stable progress fingerprint:

```go
type ProtocolState struct {
    Reprompts             int
    LastFingerprint       string
    ConsecutiveNoProgress int
    Terminal              bool
}
```

Fingerprint inputs reuse canonical stable evidence concepts from the existing progress package: normalized objective/candidate/recent text, canonical action kinds/status/names, cause, and explicit-completion expectation/observation. Volatile IDs/timestamps/attempt IDs are excluded.

`max_protocol_reprompts` counts continuation intents, not candidate evaluations:

- default = 1;
- V1 accepted range = 1..3;
- effective bound is additionally capped by platform continuation policy;
- new progress may reset only consecutive no-progress count, never total reprompt count.

### Missing-Signal Recovery Intent

Semantically fixed content:

```text
<automated-completion-protocol-repair>
The previous model turn ended without the required `attempt_completion` signal.
This is proxy-internal recovery control, not a new user request, approval,
permission, or scope expansion.

If all work requested by the user is complete, call `attempt_completion` now
with a concise final `result`.

If concrete requested work remains and can proceed without new user input,
continue exactly that work from the retained safe point.

If further progress requires user input, permission, credentials, clarification,
or a choice, request that input normally and end. Do not assume it.

Do not invent, repeat, broaden, optimize, or discover work merely because this
recovery message was sent.
</automated-completion-protocol-repair>
```

The continuation intent still carries existing internal-control provenance, trajectory reference, bounded objective, control-state token, and platform-owned placement semantics.

### Legitimate User-Input Case

With the default cap of one:

```text
B1 asks user for required information and stops unmarked
  -> hidden protocol repair B2
B2 recognizes user input is required, repeats/clarifies the request and stops unmarked
  -> cap exhausted -> A-side terminal is allowed
```

This intentionally spends one hidden B-leg to distinguish accidental stop from a stable unmarked stop without a second model/verifier. Operators who find that cost undesirable can disable ALG or select the legacy verifier strategy; V1 does not add prose heuristics.

## Legacy Semantic-Verifier Path

The current provider implementation remains behaviorally intact under `strategy: semantic_verifier`:

```text
candidate
  -> current cause policy
  -> trusted explicit-completion handling per existing config
  -> detached verifier when eligible
  -> current progress/no-progress policy
  -> current continuation intent
```

Requirements:

- existing config omission maps here;
- existing `verifier_role`, timeout, max semantic continuation, explicit completion trust/verify, and no-progress semantics remain;
- the legacy path does not construct a control-tool provider;
- the preferred path does not construct/use `verifier.New`, auxiliary role `loop_guard`, or verifier parser/prompt;
- shared pure helpers may be reused only where that does not create runtime cross-strategy fallback.

## ToolChoice and Native Completion Tools

### Proxy Injection

The proxy does not override or rewrite client ToolChoice. V1 activates only when adding one invisible control capability is compatible with the original request contract.

### Client-Owned `attempt_completion`

If the client already declares `attempt_completion`:

- proxy injection is inactive due name collision;
- the client tool remains client-owned and follows ordinary tool policy/reactor/frontend handling;
- no proxy-owned call is intercepted by name;
- if a completed native call/result later produces existing `ExplicitCompletion`, either ALG strategy may consume that generic fact according to its own policy;
- absence of that native completion does not set `ExplicitCompletionExpected` for the proxy protocol, so the preferred strategy does not invent a hidden reprompt solely from the collision.

This preserves Cline/Roo/Kilo clients that already own their completion tool while adding transparent behavior for clients that do not.

## Transport and Continuation Interaction

- **Pre-output transport failure**: unchanged existing replay/failover recovery; control protocol adds no retry.
- **Post-output interruption after active protocol**: no completion signal plus retained safe trajectory can trigger the preferred continuation policy; original attempt is never replayed.
- **Completed ordinary tool/result before interruption**: retain and continue; do not re-execute.
- **Incomplete ordinary tool args/opaque unsafe state**: conservative stop/failure as current policy requires.
- **Client cancellation**: no protocol recovery.
- **Refusal/filter**: no protocol recovery.
- **Continuation leg**: the same control-tool provider is projected again if the continuation candidate remains eligible, so the worker can complete the handshake on the repair turn.

## Accounting, Context, Traffic, and Cache Semantics

### Accounting and Context

Control tool + instruction are real provider input and therefore must participate in:

- canonical protocol requirements;
- candidate capability admission;
- context-window sizing;
- request token estimation/preflight;
- provider input usage/cost when upstream reports it;
- PTB traffic capture.

They are not part of A-leg/client input truth and must not be counted as client-authored content by any customer-side semantic accounting that distinguishes proxy additions.

Local handling of the control call creates no synthetic provider usage.

### Traffic

- CTP/A-leg capture: no proxy control definition/instruction/call.
- PTB/B-leg capture: final backend-effective request includes control instruction/tool when active.
- BTP raw/canonical capture: upstream control events may be visible under existing internal/operator traffic policy.
- PTC/client capture: control tool lifecycle is absent; optional result text appears as ordinary assistant text if surfaced.

### Prompt Cache

The base instruction and tool definition are byte-stable. No timestamp/counter/request ID enters them. Activation can legitimately change the provider-visible request when backend/tool-choice/model eligibility changes; such a change is an explicit request-shape discontinuity, not something core should hide with provider-specific cache-key tricks.

## Failure Behavior

| Condition | Behavior |
|---|---|
| malformed feature config | candidate generation fails; last-good generation preserved |
| invalid control-tool spec at construction | generation fails |
| backend has no tools | protocol inactive; no verifier fallback |
| incompatible ToolChoice | protocol inactive; no verifier fallback |
| client name collision | protocol inactive; client tool untouched |
| post-hook/final projection drift | exclude/fail candidate before Backend.Open |
| malformed/oversized proxy control call | swallow local call, mark invalid, no client fallback execution |
| control handler expected model error | invalid completion; terminal policy may reprompt if active/budgeted |
| internal control handler error | conservative terminal path; never leak local tool to client |
| valid completion | trusted explicit completion; accept otherwise-safe terminal |
| first active unmarked eligible stop | hidden protocol-repair continuation |
| unmarked stop after default one repair | allow stop |
| cancellation/refusal/filter | allow/propagate authoritative outcome |
| post-commit transport failure | continuation only when safe; no replay/failover |

## Concurrency and Lifecycle

- Provider/config live in the immutable runtime generation.
- Request pins one generation, terminal provider, and control provider for its lifetime.
- Activation/capture/result state is attempt/logical-response local; no process-global map or background goroutine.
- Parallel/racing candidates receive independent activations/captures and only the winning attempt's evidence may affect the logical response.
- A losing attempt's control result must be discarded with that attempt.
- Reload can switch strategies for new requests without changing in-flight strategy.
- Withdrawal leaves no durable protocol row or stale overlay to clean.

## Observability

Use existing bounded feature/extension telemetry conventions. Suggested content-free dimensions/reasons:

```text
strategy=attempt_completion|semantic_verifier
activation=active|backend_tools_unsupported|tool_choice_none|tool_choice_constrained|allowed_tools_constrained|tool_name_collision|projection_error
control=observed|valid|invalid|args_too_large|multiple_calls|handler_error
terminal=explicit_completion|missing_signal_reprompt|reprompt_exhausted|protocol_inactive|no_progress|authoritative_candidate
```

Do not label with:

- `result` text;
- prompt/instruction text;
- tool args;
- raw IDs;
- user objective;
- candidate output;
- secrets.

Existing trace/A-leg/B-leg correlation may remain in trace/log fields under current policy but not metric labels.

## File Structure Plan

The tree below is the original design-time expectation, annotated in place against what was actually
built. Annotations mark supersession (`planned X; built as Y`) and unplanned additions. Nothing in this
section changes a Boundary Commitment, an Architecture Ratchet, or a requirement; where the built shape
diverges from the design's *intent* rather than only from its file list, the divergence is recorded under
Open Design Notes instead of being folded into the design silently.

Reconciliation basis: `git diff --name-status 1fc49fe2..HEAD` — 71 added, 59 modified, 0 deleted, 130
paths. A planned path that appears as neither `A` nor `D` below was never created; a path with no plan
line was built unplanned.

```text
pkg/lipsdk/controltool/
├── doc.go                    # ADDED unplanned: package doc pinning "contract, not a runtime" (doc.go:1-7)
├── types.go                  # generic Provider/Spec/Outcome/Meta contracts
├── errors.go                 # ADDED unplanned: bounded validation sentinels (errors.go:5-6)
├── validate.go               # bounds and static spec validation
├── projection.go             # pure authority-neutral projection helpers
└── *_test.go                 # contract_test.go, projection_test.go

pkg/lipsdk/feature/
├── plane_manifest.go         # PlaneControlToolProvider + generated/ratchet updates
├── plane_generated.go        # MODIFIED: generated frozen plane set + exclusive slot
└── errors.go                 # MODIFIED unplanned: ErrControlToolProviderConflict (errors.go:24-25)

pkg/lipsdk/terminaldecision/
└── types.go                  # MODIFIED unplanned in this plan: additive ExplicitCompletionExpected
                              # beside ExplicitCompletion (types.go:136-155); matches Completion
                              # Evidence and Pending Result, this tree simply did not name the file

internal/core/extensions/
├── control_tool.go           # generic bounded stage runner / evidence
├── snapshot.go               # MODIFIED unplanned in this plan: ControlToolProvider() /
│                             # ControlToolProviderIdentity() request-snapshot accessors (snapshot.go:396-412)
└── completion_run.go         # MODIFIED unplanned: new EffectiveReplacement field on
                              # CompletionGateChainResult (completion_run.go:22-33)

internal/core/runtime/
├── executor_*                # post-hook projection + final reassertion
│                             #   (executor_attempt_transform.go +132/-0, executor_retry_stream.go +77/-0,
│                             #    executor_final_stream_obs.go +21/-0, executor_open_attempt.go +23/-1)
├── attempt_*                 # PLANNED NEW OWNER FILE NOT CREATED; ownership landed in the pre-existing
│                             #   attempt_session.go (+83/-29), which is the request-local attempt owner
├── response_pipeline_*       # PLANNED NEW FILES NOT CREATED under this glob; pre-existing
│                             #   response_pipeline.go (+82/-6) and response_pipeline_observations.go (+50/-4)
│                             #   were modified in place, so the new seams took sibling names:
│                             #   control_call_capture.go, response_control_interception.go,
│                             #   response_pending_completion.go
├── terminal_decision_*       # generic Expected/Observed evidence projection + result drain
│                             #   (terminal_decision_evidence.go +111/-4; see Open Design Note 1 for the
│                             #    Actions/ActionCount projection change inside it)
└── candidate_open_tool_response_ordering_characterization_test.go
                              # ADDED unplanned, but in internal/core/runtime/ and not
                              #   internal/archtest/: Adjacent-Spec Revalidation characterization

internal/plugins/features/agentloopguard/
├── config.go                 # strategy/mutual exclusion/backcompat (+274/-19)
├── provider.go               # strategy dispatch (+49)
├── completiontool.go         # SPLIT: fixed Spec/projection only (completionToolSpec at :63)
├── completiontool_handle.go  # ADDED unplanned: the strict handler and NewCompletionToolProvider (:31, :42)
├── preferred_provider.go     # BUILT INSTEAD OF protocol.go: preferred terminal receiver (Decide at :47)
├── protocolpolicy/           # BUILT INSTEAD OF protocol.go: pure missing-signal policy + repair intent
│                             #   (package doc protocolpolicy.go:1-4)
├── protocolstate/            # bounded fingerprint/counters/token (protocolstate.go, token.go)
├── verifier/                 # existing legacy path retained
├── progress/                 # existing legacy progress retained
└── *_test.go

internal/standardplugins/
└── feature composition       # preferred contributes terminal + control provider
                              #   (features_install.go:48-60, strategy-conditional control contribution)

internal/archtest/               # 16 paths total in the diff for this directory: 10 added and
                              #   6 modified, counted only from `git diff --name-only
                              #   1fc49fe2..HEAD -- internal/archtest/`
├── agent_loop_guard_*        # ADDED unplanned by name: 8 of those 10 additions, being 4 non-test
│                             #   (ownership AST helper, ownership ratchet, strategy-isolation
│                             #    ratchet, terminal-owner census) plus 4 fixture/ratchet tests
├── git_fixture_test.go       # ADDED unplanned: isolated committed-Git fixture (git_fixture_test.go:31-43)
├── final_stream_observation_order_fixture_test.go
│                             # ADDED unplanned: coordinator-scoped observation-order validator fixtures
├── plane_report.go, plane_rules_tables.go
│                             # MODIFIED unplanned: W4_Tools 4->5 planes, control_tool_provider tables
└── 4 pre-existing tests MODIFIED (final_stream_observation_order_test.go,
                              #   plane_report_test.go, plane_rules_whitelist_test.go,
                              #   request_attempt_state_contract_test.go): the pre-existing-test
                              #   churn, completing the 6 modified paths

internal/testkit/
├── conformance/deployment.go # MODIFIED unplanned by name: real strategy composition (+87/-3)
├── conformance/agentloopguard_preferred_{e2e,telemetry,transport_e2e}_test.go
│                             # ADDED unplanned by name: the preferred acceptance matrix
└── planeparity/planeparity.go
                              # MODIFIED unplanned: control-tool sentinel + plane parity assertions

internal/featurebundle/
└── control_tool_provider_merge_test.go
                              # ADDED unplanned: exclusive-slot and provider-removal proofs (requirement 12.3)

internal/qa/
├── race_check_partition_contract_test.go
│                             # ADDED unplanned: staged archtest partition contract (:15-26, :59)
└── (none)                    # no production change; scripts/race-check.sh is the only non-test edit

scripts/race-check.sh         # MODIFIED unplanned: staged scopes partitioned like the full scan
internal/core/largebody/      # MODIFIED unplanned: plane census 27 -> 28
├── eligibility.go            #   WireEligibilityPlaneCount = 28 (:28), "control_tool_provider" order
│                             #   entry (:110), non-negotiable canonical plane (:418)
└── authority_gate.go         #   PlaneAccessCanonicalRequired for control_tool_provider (:147)
internal/infra/metering/journalstore/observation_store.go
                              # MODIFIED unplanned: identity resolution pre-insert -> post-insert
                              #   (:336), observationInsertEffect projection ownership (:406)
internal/infra/configsource/atomic_recycle_test.go
                              # MODIFIED unplanned: dedicated recycled-inode identity fixture repair
internal/core/runtime/conversation_view.go
                              # MODIFIED unplanned: dead hardcoded "alg-rec" overlay branch deleted (0/-35)
internal/plugins/backends/    # MODIFIED unplanned: canonical RoleDeveloper wire encoding
├── openailegacy/invoke.go, openairesponses/invoke.go
│                             #   literal "developer" wire role
└── protocols/anthropicmessages/invoke.go, protocols/geminigenerate/invoke.go
                              #   deliberate lossy coercion to the user role, loss named per site
internal/core/runtime/{interleaved_stream.go,executor_settlement.go,executor_recv_loop.go}
                              # MODIFIED unplanned at this size: shared concurrency rework
                              #   +534/-66, +342/-19, +294/-29 (see Open Design Note 3)
testdata/architecture/{extension_planes,hexagonal_migration}_baseline.json
                              # MODIFIED unplanned: plane-census baseline 27 -> 28
README.md, docs/agent-loop-guard.md, docs/plugin-authoring.md,
config/config.yaml, config/examples/agent-loop-guard-{preferred,legacy-verifier}.yaml
                              # docs / example config as applicable; examples are credential-free
```

No provider-specific backend adapter should gain ALG logic. This still holds: the four backend adapter
edits add a canonical role mapping only. They carry no feature name, no `attempt_completion` identifier,
and no protocol behaviour; provider role mapping is adapter-layer work that the design's own
`terminal_decision_continuation.go:83` developer-role steering already required before this branch.

### Plan-vs-Actual Reconciliation

| Planned | Actual | Evidence |
|---|---|---|
| `agentloopguard/protocol.go` (preferred terminal policy/recovery intent) | Never created; superseded by `protocolpolicy/` + `preferred_provider.go` | No `A`/`D` entry for `protocol.go` in the 130-path diff; `protocolpolicy.go:1-4` package doc; `preferred_provider.go:47`. Reason recorded: tasks.md task 8.1 splits a pure codec/state package from the policy and task 8.3 requires `NewConfiguredProvider` to select separate preferred and legacy receivers. |
| new `runtime/response_pipeline_*` files (control-call diversion / pending result) | `control_call_capture.go`, `response_control_interception.go`, `response_pending_completion.go` | Both `response_pipeline*.go` files already existed at `1fc49fe2` and were modified in place, so the glob was not free. Both planned responsibilities are covered: diversion by capture + interception, pending result by `response_pending_completion.go`. Reason not recorded beyond name occupancy. |
| new `runtime/attempt_*` activation/capture owner | Pre-existing `attempt_session.go` extended | `ls internal/core/runtime/attempt_*` shows no added non-test file; only `attempt_session.go` is in the diff (+83/-29). Reason not recorded. |
| `completiontool.go` = "fixed Spec + strict handler" | Split into `completiontool.go` (projection only, `:63`) and `completiontool_handle.go` (`:31`, `:42`) | Reason recorded: tasks.md task 7.1/7.2 — "Task 7.2 must reuse `completionToolSpec` without changing its ABI." |
| `internal/archtest/`, `internal/testkit/` (bare directory lines) | 16 archtest paths (10 added, 6 modified), 3 conformance tests, planeparity edits | Under-delivery of naming, not of scope; requirement 12.3/12.4 evidence landed as planned. `git diff --name-only 1fc49fe2..HEAD -- internal/archtest/` yields exactly 16: 8 `agent_loop_guard_*` (4 non-test + 4 fixture tests), `git_fixture_test.go`, `final_stream_observation_order_fixture_test.go`, `plane_report.go`, `plane_rules_tables.go`, plus 4 modified pre-existing tests (`final_stream_observation_order_test.go`, `plane_report_test.go`, `plane_rules_whitelist_test.go`, `request_attempt_state_contract_test.go`). |
| — (undeclared) | `pkg/lipsdk/controltool/{doc,errors}.go` | `doc.go:1-7` states the package "is not a tool runtime, service locator, or DI container", i.e. requirement 12.2 in prose; `errors.go:5-6` bounded sentinels. Approved-boundary. |
| — (undeclared) | `internal/core/largebody/{eligibility,authority_gate}.go` | Census 27 -> 28 (`eligibility.go:28`, `:110`, `:418`; `authority_gate.go:147` `PlaneAccessCanonicalRequired`) plus the two `testdata/architecture` baselines. Reason recorded: tasks.md task 2.2 requires the control-tool plane to stay canonical-required in both largebody eligibility compilation and authority assessment. Generic and feature-name-free, so 12.1 is unaffected. Approved-boundary. |
| — (undeclared) | `internal/core/extensions/completion_run.go` | New `EffectiveReplacement` field (`:22-33`) recording final rather than historical provenance. Generic completion-gate contract, ALG-free. See Open Design Note 2. |
| — (undeclared) | `internal/infra/metering/journalstore/observation_store.go` | `:336` inserts first and resolves the durable identity after a conflict-suppressed insert; `observationInsertEffect` (`:406`) then decides projection ownership. Changes replay/collision classification for **all** observation writes. Reason recorded: tasks.md "Dedicated billing/metering unblock". Outside this feature's surface; landed only as a separately reviewed repair. |
| — (undeclared) | 4 backend adapters | `openailegacy`/`openairesponses` map `RoleDeveloper` literally; `anthropicmessages`/`geminigenerate` coerce it to the user role with the loss named at each site. Reason recorded: tasks.md task 10.1 (slice B, adapters, adapters-user-decided). Approved-boundary, with Bedrock and the ACP/Cohere/Watsonx/OCI/Vertex connectors still fail-closed. |
| — (undeclared) | `scripts/race-check.sh` + `internal/qa/race_check_partition_contract_test.go` | Staged scopes now partition `internal/archtest` exactly as the pre-existing full scan does; the contract test (`:15-26`, `:59`) pins it against fake `go`/`git`/`cc` binaries. Reason recorded: tasks.md "Dedicated staged-race unblock". Scheduling only; identical `GO_ARGS`, budgets, and tags. |
| — (undeclared) | `internal/featurebundle/control_tool_provider_merge_test.go` | `:67` exclusive-slot proof and `:146` provider-removal proof through the real generated merge path; requirement 12.3 evidence. The plan named `internal/testkit/` but not `internal/featurebundle/`. Approved-boundary. |
| — (undeclared) | `internal/archtest/{git_fixture,final_stream_observation_order_fixture}_test.go` | `git_fixture_test.go:31-43` isolates Git fixtures from the caller's index; `final_stream_observation_order_fixture_test.go:11-23` pins the coordinator-scoped rule after the historical false positive. Reason recorded: tasks.md "Dedicated loader-fixture unblock" and task 12.1. |
| — (undeclared) | `internal/core/runtime/conversation_view.go` (0/-35) | Deletes a generic-core branch that hardcoded the feature identity `"agent_loop_guard"` and the `"alg-rec"` overlay spelling. Reason recorded: tasks.md task 12.1 — the branch was unreachable, so it was deleted rather than exempted. This narrows generic core and strengthens 12.1; see Open Design Note 4. |
| — (undeclared) | `internal/core/runtime/{interleaved_stream,executor_settlement,executor_recv_loop}.go` (+534/-66, +342/-19, +294/-29) | Concurrency rework of shared streaming and settlement seams: per-frame fence drain in the real receive loop, plus thinker-processor and memo-finalize single-consumer ownership. Reason recorded: tasks.md task 5.2. Deliberately bigger than the plan implied; see Open Design Note 3. |
| — (undeclared) | `internal/archtest/agent_loop_guard_*` (8 added files: 4 non-test ownership/isolation ratchet, AST helper and terminal-owner census + 4 fixture/ratchet tests) and `internal/core/runtime/candidate_open_tool_response_ordering_characterization_test.go` | Requirement 12.3/12.4 ratchets and the Adjacent-Spec Revalidation characterization; reason recorded: tasks.md task 12.1 and task 1.1. The characterization test was placed in `internal/core/runtime/`, not `internal/archtest/`, so it is not part of the 16 `internal/archtest/` paths. |

### Open Design Notes

These are substantive design gaps found while reconciling the plan. They are recorded, not repaired: no
requirement, boundary, or ratchet text is changed by this section.

1. **`Actions`/`ActionCount` projection changed for every terminal provider with no design amendment.**
   `terminalDecisionActions` now takes the attempt and prepends the held ordinary boundaries of a private
   publication candidate (`terminal_decision_evidence.go:158`, `:174`, merged by
   `terminalDecisionMergeHeldActions`) inside the same fixed `MaxEvidenceActions` capacity. The design's
   "Completion Evidence and Pending Result" adds two boolean fields and says nothing about action
   projection, and no requirement constrains it. This changes what legacy ALG and every other terminal
   provider can observe, and it is bounded and capability-safe but **unowned by this design**. It should
   be amended here or explicitly declined.
2. **New `EffectiveReplacement` field on the generic completion-gate result.**
   `internal/core/extensions/completion_run.go:33` changes a shared core contract that the design does not
   mention. It is ALG-free and additive, so no boundary moved, but the generic completion-gate contract is
   now wider than the design describes.
3. **Shared-streaming concurrency rework is larger than "generic integration".**
   `interleaved_stream.go` (+534/-66), `executor_settlement.go` (+342/-19), and `executor_recv_loop.go`
   (+294/-29) rework seams the design itself lists as revalidation triggers. Reason is recorded in
   tasks.md task 5.2 (per-frame fence drain so a candidate can be withdrawn before the actual B2
   transaction, plus interrupted-memo finalization), but the design's own concurrency section does not
   describe the resulting ownership model.
4. **A dead generic-core ALG name was removed rather than exempted.**
   `internal/core/runtime/conversation_view.go` (0/-35) deletes a hardcoded `"alg-rec"` overlay suppression
   branch that no production path published. Reason recorded: tasks.md task 12.1. This strengthens 12.1
   and is noted here only so the plan is not read as claiming that file was untouched.

## Testing Strategy

### TDD Order

Each implementation slice starts RED, then minimal GREEN, then refactor. Do not implement the runtime seam and backfill tests afterward.

### SDK / Generic Platform Tests

- control spec validation and bounds;
- exclusive plane merge/provider removal;
- authority-neutral instruction projection for message and item authority;
- exact byte identity of the normative base instruction across post-hook projection and final reassertion;
- stable/idempotent projection;
- ToolChoice eligibility matrix;
- candidate capability eligibility;
- collision behavior;
- post-hook projection/final reassertion equivalence;
- capture correlation, args bound, multiple/malformed lifecycle;
- proof that claimed calls bypass ordinary tool policy/reactors;
- proof non-control events take the unchanged pipeline;
- typed-nil/provider panic/failure normalization consistent with extension conventions.

### Preferred ALG Tests

- exact pinned tool name/schema/no-command field;
- exact pinned base instruction bytes and LF normalization contract;
- strict args parser: missing/extra/duplicate/wrong-type/trailing/oversize/invalid UTF-8;
- valid completion decision;
- completion-only result publication;
- prior visible text suppresses duplicate result publication;
- first missing-signal clean stop continues;
- second default unmarked stop allows;
- configured 2/3 reprompt bounds;
- no-progress stop;
- protocol inactive stops without verifier;
- assert zero auxiliary verifier calls in every preferred-mode fixture.

### Legacy Compatibility Tests

- old `enabled: true` config resolves to legacy;
- explicit `semantic_verifier` matches old decisions;
- verifier timeout/error/malformed/uncertain behavior unchanged;
- trusted explicit completion behavior unchanged;
- legacy mode contributes no control provider;
- preferred mode rejects verifier-specific mixed config.

### Runtime / Acceptance Matrix

Must include:

1. proxy completion as only model output;
2. streamed text then proxy completion;
3. ordinary client tool call before final completion;
4. clean normal stop with missing signal;
5. user-input-needed stop -> one repair -> second unmarked stop;
6. client cancel;
7. refusal/content filter;
8. pre-output EOF/idle;
9. post-output interruption with active protocol;
10. completed client tool/result then interruption;
11. incomplete client tool args;
12. malformed/oversized/multiple control call;
13. backend tools unsupported;
14. tool choice none/required/any/allowed subset;
15. client-owned `attempt_completion` collision/native evidence;
16. parallel/race loser emits completion but winner does not;
17. reload `semantic_verifier -> attempt_completion` and reverse with in-flight pinning;
18. disabled/no-provider behavior;
19. message-authority and item-authority frontends/backends;
20. streaming assertion that ordinary text is observed before backend terminal.

### Architecture Ratchets

- no `agentloopguard` import/name/switch in `internal/core` generic production code;
- new `controltool` package contains no concrete feature IDs/names;
- control tool cannot be surfaced to frontend execution path;
- no direct ALG append to client/A-leg `Call.Messages`/`Items`;
- no use of deprecated `turnTerminal.guardHidden` or second terminal owner;
- no second policy endpoint/store;
- no verifier import/call reachable from preferred strategy construction;
- no control-tool provider reachable from legacy strategy construction.

### Adjacent-Spec Revalidation

Before implementation proceeds past generic runtime wiring:

- inspect current main for landed `b-leg-path-virtualization` changes to attempt transforms, request hooks, finalizer assembly, or tool-policy order;
- inspect current main for large-payload streaming changes that affect event capture/bounds;
- adapt the generic control stage to current owners rather than reintroducing superseded seams;
- record focused characterization tests before changing shared ordering.

## Requirements Traceability

| Requirement | Design realization |
|---|---|
| 1 | Configuration and Strategy Model; Feature Plane; Legacy Path |
| 2 | Model-Facing Provider; frozen tool/instruction |
| 3 | Candidate Activation; trusted activation; response interception |
| 4 | V1 Eligibility Matrix; ToolChoice/native tool sections |
| 5 | Response Interception; capture; no completion gate |
| 6 | Completion Evidence and Pending Result |
| 7 | Preferred Terminal Policy; Protocol State; Recovery Intent |
| 8 | Transport and Continuation Interaction |
| 9 | Legacy Semantic-Verifier Path |
| 10 | Configuration; Concurrency and Lifecycle |
| 11 | Accounting/Traffic/Observability |
| 12 | Boundary Commitments; Testing; Architecture Ratchets; Adjacent-Spec Revalidation |

## Brownfield Design Validation Verdict

**GO after repairs.**

Repairs applied during validation:

1. Rejected whole-response completion gates because they buffer normal streaming.
2. Rejected late tool-reactor swallowing because tool policy runs first.
3. Added strict ToolChoice/capability eligibility rather than broadening client tool authority.
4. Added proxy-owned provenance rather than name-only interception.
5. Replaced literal one-shot session-prompt mutation with non-accumulating stable per-candidate materialization suitable for stateless upstream APIs.
6. Added one-reprompt default to resolve the legitimate user-input-stop ambiguity without a verifier fallback.
7. Preserved implicit legacy strategy for existing configs rather than silently changing enabled deployments.
8. Added an explicit current-main revalidation gate for active adjacent candidate/tool-finalization specs.

No unresolved architectural blocker remains at specification time. Implementation readiness still depends on performing the required revalidation against the then-current main branch before shared runtime edits.
