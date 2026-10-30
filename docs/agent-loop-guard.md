# Agent Loop Guard (ALG)

Agent Loop Guard is the optional terminal-decision feature that stops a premature
"the model says it is done" exit from ending an interactive agent loop. It owns
exactly one thing: whether an otherwise recoverable clean terminal proposed by a
backend leg is allowed to reach the client, or whether the proxy continues the
loop instead.

ALG has two mutually exclusive strategies. Exactly one is active per deployment
generation, and they never run concurrently:

| Strategy | Completion is decided by | Auxiliary model call on the normal path |
|---|---|---|
| `attempt_completion` (preferred) | the worker model calls a proxy-injected `attempt_completion` tool | none |
| `semantic_verifier` (legacy) | a detached auxiliary model judges the trajectory | one, per eligible candidate stop |

**Recommended for new deployments: spell the strategy out explicitly.**

```yaml
plugins:
  features:
    - id: agent-loop-guard
      enabled: true
      config:
        strategy: attempt_completion        # explicit, recommended for new deployments
        max_protocol_reprompts: 1           # default 1; V1 accepts 1..3
        no_progress_limit: 2                # shared breaker; default 2, max 64
```

Omitting `strategy` is **not** the recommended form. An ALG row that omits it
keeps the pre-spec `semantic_verifier` behavior, so existing configurations are
unaffected by this feature — but a new row should say what it means:

```yaml
plugins:
  features:
    - id: agent-loop-guard
      enabled: true
      config:
        # no `strategy` key: resolves to semantic_verifier for backward compatibility
```

Loadable local example: [`config/examples/agent-loop-guard-preferred.yaml`](../config/examples/agent-loop-guard-preferred.yaml).
It is credential-free and is decoded by the config-example test suite. Note that
`aipstd serve` currently refuses to start *any* `kind: local-stub` example
(`missing security profile`), including the pre-existing ones, so this file is a
loadable configuration reference rather than a turnkey server config.

## Strategy selection

`strategy` accepts exactly two values, compared case-insensitively after trimming
surrounding whitespace (`attempt_completion`, `semantic_verifier`). Anything else
is a configuration error. An explicitly present but empty, whitespace-only, or
null value is also an error; only an **absent** key selects the legacy default.

| Configuration | Selected strategy | Contributed planes |
|---|---|---|
| row absent, or `enabled: false` | none | none |
| `enabled: true`, no `strategy` | `semantic_verifier` | terminal-decision provider |
| `enabled: true`, `strategy: semantic_verifier` | `semantic_verifier` | terminal-decision provider |
| `enabled: true`, `strategy: attempt_completion` | `attempt_completion` | terminal-decision provider **+** control-tool provider |

The plane composition is the whole story of the two strategies:

- `attempt_completion` contributes the ALG terminal-decision provider plus exactly
  one control-tool provider. No verifier machinery is built or reachable, and no
  auxiliary verifier call can be issued on this path.
- `semantic_verifier` — whether the selector is explicit or omitted — contributes
  the terminal-decision provider and **no** control-tool provider. No proxy-owned
  `attempt_completion` tool is injected or consumed.
- A disabled row contributes neither. A feature row with row-level
  `enabled: false` is not compiled at all — the factory is never called and its
  `config:` block is never decoded, so nothing in it is validated (see
  [Mutual exclusion](#mutual-exclusion)). When the factory *is* reached with an
  in-config `enabled: false`, the `config:` block is still fully decoded and
  validated, and the bundle is simply empty. Removing the feature from the
  registry likewise leaves the generic terminal path usable with no ALG provider
  present.

There is no runtime "auto" strategy and no hybrid verifier fallback. If a
selected strategy cannot act, it produces a conservative final outcome; it never
silently switches to the other strategy.

## Mutual exclusion

Strategy-specific keys are rejected when they are supplied under the other
strategy of an **enabled** row. The decision is made from **YAML key presence**,
before decoding, so a default value can never make an unused strategy look
configured, and a supplied key is never silently ignored:

| Under `strategy: attempt_completion` | Under `strategy: semantic_verifier` |
|---|---|
| `verifier_role` — rejected | `max_protocol_reprompts` — rejected |
| `verifier_timeout_seconds` — rejected | |
| `max_semantic_continuations` — rejected | |
| `explicit_completion_policy` — rejected | |

Rejection depends on the key being present, not on its value: `0`, `''`, `'   '`,
and `null` are all rejected, exactly like `7`.

Rejection applies to an **enabled** row. There are two separate `enabled` gates,
and only one of them is bypassed:

| Gate | Where | Effect on validation |
|---|---|---|
| Row-level `enabled:` on the plugin row | the plugin registration | `false` skips the row **before** the factory is called, so `config:` is never decoded or validated |
| `enabled:` *inside* the `config:` block of an enabled row | the ALG config decoder | `false` still fully decodes and validates `config:`, then yields an empty-plane bundle |

So for a row with `enabled: false`, **nothing** in this section applies: no
mutual exclusion, no unknown-key rejection, no unknown-`strategy` rejection, no
range checks. The configuration is not "accepted with warnings" — it is never
looked at. Any unknown key, any mixed key, and any out-of-range value are all
carried silently until the row is enabled.

For an enabled row, an unknown key, an unknown `strategy` value, or a mixed key
fails generation compilation; the last-good generation stays active and
unchanged.

These errors fail closed at generation compile, so `aipstd serve` refuses to start
and a bad reload is rejected while the last-good generation keeps serving. Note
that `aipstd check-config` currently reports a feature-bundle construction error
only as a `WARN inventory extensions bundle_error=…` log line and still prints
`configuration is valid`, so `check-config` alone is not proof that an ALG
strategy configuration is sound.

Programmatic construction (the Go `Config` value) receives equivalent checks
through the strategy-aware constructor, where a zero value means "omitted". That
path has no row-level gate — it is the YAML registration path above that skips
disabled rows, so do not read the programmatic behavior as applying to a
disabled configuration file.

## Configuration reference

### Shared

| Key | Default | Range | Notes |
|---|---|---|---|
| `enabled` | — | — | Row-level gate. A row-level `enabled: false` is not compiled, so its `config:` block is **not validated at all**. An in-config `enabled: false` under an enabled row is still decoded and validated. |
| `strategy` | `semantic_verifier` (omitted only) | `attempt_completion` \| `semantic_verifier` | Case-insensitive; whitespace trimmed. |
| `no_progress_limit` | `2` | `1..64` | Shared bounded no-progress breaker; used by both strategies. |

### `attempt_completion`

| Key | Default | Range |
|---|---|---|
| `max_protocol_reprompts` | `1` | `1..3` (V1) |

`max_protocol_reprompts` must be a YAML integer. Fractional values (`2.5`),
quoted numbers, booleans, sequences, and mappings are rejected rather than
truncated or coerced. Values above the V1 maximum of 3 fail generation
compilation before publication.

The effective number of protocol-repair turns is additionally narrowed by the
platform's own continuation policy, so a configured 3 can yield fewer than 3.
Progress never replenishes the total cap.

### `semantic_verifier`

| Key | Default | Range | Notes |
|---|---|---|---|
| `verifier_role` | `loop_guard` | ≤128 chars | Auxiliary role the verifier call uses. |
| `verifier_timeout_seconds` | `4` | `1..300` | Bounded verifier deadline. |
| `max_semantic_continuations` | `3` | `1..64` | Intersected with the platform continuation cap. |
| `explicit_completion_policy` | `trust` | `trust` \| `verify` | `trust` lets a trusted explicit-completion signal bypass verification; `verify` forces verification even when one is present. |

Explicit legacy configuration:

```yaml
plugins:
  features:
    - id: agent-loop-guard
      enabled: true
      config:
        strategy: semantic_verifier
        verifier_role: loop_guard
        verifier_timeout_seconds: 4
        max_semantic_continuations: 3
        no_progress_limit: 2
        explicit_completion_policy: trust
```

Disabled:

```yaml
plugins:
  features:
    - id: agent-loop-guard
      enabled: false
      config: {}
```

Loadable local example pinning current behavior:
[`config/examples/agent-loop-guard-legacy-verifier.yaml`](../config/examples/agent-loop-guard-legacy-verifier.yaml).

## When the completion protocol activates

`attempt_completion` is an explicit handshake with the worker model, so it can
only run on a B-leg where a hidden tool may safely be added. Activation is
decided per candidate, from that candidate's real backend capabilities and the
client's real tool constraints, and is recomputed if retry, race, or failover
changes the candidate. Ineligibility is **not** an error: the call is passed
through untouched, the client's tool choice is never rewritten, and no proxy tool
is injected.

| Condition | Result | Reason code |
|---|---|---|
| Backend does not advertise callable tools | inactive | `backend_tools_unsupported` |
| `tool_choice: none` | inactive | `tool_choice_none` |
| `tool_choice: any`, or an unrecognized mode | inactive | `tool_choice_constrained` |
| `tool_choice: required` | inactive | `tool_choice_required` |
| Tool choice names a specific tool | inactive | `tool_choice_required` |
| A non-empty allowed-tools subset is present | inactive | `allowed_tools_constrained` |
| The client declares a tool named `attempt_completion` | inactive | `tool_name_collision` |
| The client already declares the exact control instruction | inactive | `instruction_collision` |
| Tool choice unset or `auto`, no named tool, no allowed-tools subset, no collision | active | — |

The rules are deliberately conservative:

- A client that forbids tools, requires one specific tool, or constrains the
  allowed set has its constraint honored. An optional loop guard never widens a
  client's tool-selection semantics.
- A same-named client tool is always a collision, even if the client's definition
  happens to be byte-identical to the proxy's. The client keeps ownership: the
  call is neither relabeled nor intercepted. When that client-owned tool completes
  through its ordinary tool-result path, its normalized explicit-completion
  evidence remains usable by ALG, exactly as before.
- The protocol is never emulated through prose parsing on a tool-incapable
  backend.
- When activation is ineligible, `attempt_completion` does **not** fall back to
  the semantic verifier, and it does not invent a completion signal. The
  conservative final outcome stands.

## Model-facing contract (fixed)

The injected tool is a stable model-facing ABI in this version. It is not
operator-renamable and is not derived from configuration, defaults, or
environment.

- **Name**: exactly `attempt_completion`.
- **Schema**: one object with exactly one required property `result` of type
  `string`, and `additionalProperties: false`.
- **No `command` parameter**: `command` is intentionally absent. It would add
  side-effect semantics unrelated to completion and would widen the local-control
  boundary.
- **Instruction role**: system, with byte-stable base text that carries no
  timestamp, trace id, attempt id, or counter.

A candidate completion call is accepted only when it is exactly one JSON object
with exactly `result`, carrying a non-empty bounded string. Missing, empty,
wrong-typed, unknown, duplicate, trailing, non-UTF-8, or oversized values are
treated as bounded model mistakes; the call is never exposed to the client as a
fallback tool invocation, and no `result` text or argument ever reaches an error
string, metric label, or reason code.

Visibility stays one-sided. The injected tool definition and instruction are
visible to the selected B-leg model only — not to A-leg/client truth,
client-visible history, or frontend tool catalogs. A claimed proxy-owned call is
diverted from the client path before ordinary tool policy and reactors run, so it
never becomes client tool execution. Ordinary text, reasoning, media, usage, and
non-control tool events keep streaming normally; this strategy does not buffer a
whole response until completion.

When no meaningful client-visible assistant text has been committed yet, a valid
`result` may become the final client-visible assistant text. When text has already
been committed, the `result` remains completion evidence only and is not
duplicated into the transcript.

## Missing-signal recovery

When the protocol was active for the candidate, no trusted completion signal was
observed, and an otherwise recoverable clean terminal is proposed, ALG suppresses
that terminal and requests one bounded hidden continuation instead. The repair
text tells the worker that the previous turn ended without the required
completion signal, directs it to call `attempt_completion` if the requested work
is complete, and otherwise forbids invention and scope expansion.

With the default `max_protocol_reprompts: 1`:

1. First unmarked candidate stop → suppressed, one hidden repair continuation.
2. Second unmarked candidate stop → allowed through. The turn terminates instead
   of an unbounded repair sequence.

Raising the bound to 2 or 3 gives more self-correction chances at the cost of more
hidden turns. Both the total cap and the no-progress breaker apply; progress never
resets the total cap.

ALG stops conservatively and does not build a continuation when the candidate is
authoritative (client cancellation, refusal, content filtering), when committed
output leaves no safe continuation point, when the protocol was not active for
that candidate, when the carried protocol state is unusable, or when the platform
refuses continuation admission. Absence of `attempt_completion` alone is never
converted into continuation after client cancellation, and the strategy never
adds a replay budget on top of existing pre-output transport recovery.

## Choosing a strategy: the self-attestation trade-off

`attempt_completion` is **self-attestation**: the model that did the work also
declares that the work is done. That is the whole point of the strategy and also
its main limitation.

What you gain with `attempt_completion`:

- No auxiliary model call on the normal path, so no extra latency, no extra token
  spend, and no second usage line for the guard itself.
- Familiar surface: models already exposed to Cline/Roo/Kilo-style
  `attempt_completion` follow it more reliably than they satisfy an ad-hoc
  verifier prompt.
- A bounded, self-correcting handshake: an accidental clean stop gets one hidden
  repair turn instead of ending the loop.

What you give up:

- The worker is judging its own homework. A model that stops early — or is
  over-eager to wrap up — can call `attempt_completion` and be believed. There is
  no independent party to catch that.
- Activation is conditional. Tool-incapable backends, constrained tool choices,
  and `attempt_completion` name collisions leave the protocol inactive, and there
  is no automatic verifier fallback in that case.
- The guard's effectiveness depends on prompt-following quality of the deployed
  worker model.

Choose `semantic_verifier` instead when:

- you do not trust the worker's self-report on the tasks being routed — for
  example long unattended automation where an early "done" is expensive;
- your worker models are unreliable tool callers, or your clients pin tool choices
  that make the protocol ineligible;
- you can absorb one extra model call per eligible candidate stop, in latency and
  in usage.

Note that `semantic_verifier` is not purely independent either: with the default
`explicit_completion_policy: trust`, a trusted explicit-completion signal still
bypasses verification. Set `explicit_completion_policy: verify` when the verifier
must be the authority even when the model produced an explicit completion signal.

The legacy strategy also fails closed: with no auxiliary collector available, the
verifier returns `UNCERTAIN` and ALG takes the conservative final outcome rather
than inventing one.

## Migrating an existing deployment

Nothing about an existing configuration changes on its own. There is no implicit
strategy switch, no implicit reprompt budget, and no implicit rewrite of legacy
verifier keys.

1. **Do nothing.** A row that enables ALG without `strategy` keeps the
   `semantic_verifier` behavior it had before, with the same defaults
   (`verifier_role: loop_guard`, `verifier_timeout_seconds: 4`,
   `max_semantic_continuations: 3`, `no_progress_limit: 2`,
   `explicit_completion_policy: trust`).
2. **Pin the current behavior explicitly.** Add `strategy: semantic_verifier` to
   make the intent visible to the next operator. Behavior is unchanged.
3. **Opt in to the new strategy deliberately.** Change it to
   `strategy: attempt_completion` and drop the verifier-only keys, which are
   rejected under the preferred strategy. Keep `no_progress_limit`; it is shared.
4. **Roll back** by changing the selector back to `semantic_verifier` and
   restoring the verifier keys.

Reload follows immutable generation semantics: newly admitted requests use the
newly published generation, while in-flight requests stay pinned to the strategy
their generation admitted. A reload carrying invalid configuration is rejected and
the last-good generation remains active and unchanged.

For a staged rollout, `enabled: false` is the first step: it removes the ALG
terminal provider entirely without touching any other configuration.

**Caveat: a row-level `enabled: false` row is not validated, so it is not a safe
place to stage configuration.** Because the row is skipped before the factory
runs, a staged block is accepted no matter how wrong it is. A row left disabled
with `strategy: attempt_completion` and a leftover `verifier_role` starts
cleanly and fails only at the moment the row is switched on — during the rollout
you were trying to make safe.

Use this order instead:

1. Write the complete intended `config:` block with `enabled: true`.
2. Validate it on the path that will actually compile it: start `aipstd serve`,
   or apply the reload, and confirm it is accepted. `aipstd check-config` is not
   sufficient on its own — see the operator note in
   [Mutual exclusion](#mutual-exclusion).
3. Only then set `enabled: false` for the staged period, leaving the validated
   block untouched.
4. When the row is switched back to `enabled: true`, nothing else changes: the
   block that already validated will validate again.

## Diagnosing protocol inactivity

The bounded reason vocabulary distinguishes active from inactive projections and
is visible in existing decision diagnostics. Activation reasons are
`backend_tools_unsupported`, `tool_choice_none`, `tool_choice_constrained`,
`tool_choice_required`, `allowed_tools_constrained`, `tool_name_collision`, and
`instruction_collision` (empty reason means active). Terminal outcomes include
`explicit_completion`, `completion_protocol_inactive`, `protocol_terminal`,
`missing_completion_signal`, `budget_exhausted`, `no_progress`,
`authoritative_candidate`, `unsafe_action_state`, `pre_output_failure`,
`missing_trajectory`, `invalid_input`, and `invalid_protocol_state`.

Captured completion calls are recorded through the existing decision-diagnostic
seam as a bounded `control_tool_call` record carrying only the frozen provider id
plus a bounded outcome and reason code. Completion `result` text, tool arguments,
prompt text, and raw A-leg/B-leg identifiers never reach that record or any metric
label. Local control-tool handling manufactures no provider usage: upstream model
usage continues to be attributed to the real B-leg.

## Package map

| Package | Role |
|---|---|
| `pkg/aipsdk/controltool` | Generic provider-neutral proxy-owned control-tool contract, spec validation, projection and the V1 eligibility matrix |
| `pkg/aipapi` | Canonical `explicit_completion` alias set and completion-evidence helpers |
| `pkg/aipsdk/terminaldecision` | Generic terminal-decision input, decision, and continuation contract |
| `internal/plugins/features/agentloopguard` | Feature root: strategy config, terminal provider, legacy verifier path, preferred protocol provider, completion control provider |
| `internal/plugins/features/agentloopguard/causepolicy` | Shared candidate cause/safety classification |
| `internal/plugins/features/agentloopguard/progress` | Legacy progress / no-progress policy and state |
| `internal/plugins/features/agentloopguard/verifier` | Legacy detached bounded semantic verifier |
| `internal/plugins/features/agentloopguard/protocolstate` | Preferred bounded protocol state (repromts, evidence fingerprint, no-progress) |
| `internal/plugins/features/agentloopguard/protocolpolicy` | Preferred missing-signal policy and bounded continuation intent |
| `internal/standardplugins` (`features_install.go`) | Strategy-aware plane composition: terminal provider always when enabled, control-tool provider only for `attempt_completion` |
| `internal/core/extensions`, `internal/core/runtime` | Generic control-tool projection, interception, and capture seams — no ALG import or name |