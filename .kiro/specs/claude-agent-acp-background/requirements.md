# Requirements Document

## Project Description (Input)
Add bounded, operator-opt-in lifecycle tracking for the Claude agent's background tasks and subagents (follow-up to #705). Native Agent/Task tools already run under the connector's default bypass policy, including automatic between-turn permission approval. Explicit permission deny disables those tools. Out-of-turn notifications still drop; enhanced tracking, diagnostics and unsettled-runtime handling remain this follow-up's scope.

- **Who has the problem:** single-user AIProxer operators running the `claude-acp` agent runtime who want Claude's native background/subagent work (and its visibility) without silently unsafe behavior.
- **Current situation:** a connector-side terminal barrier waits for SDK idle and a drained live-task registry before releasing the pool claim. The five-second post-result drain deliberately does not support long-lived background servers across client turns. Retirement uses a per-runtime Linux subreaper to terminate/reap descendants independently of SDK stop outcomes; shared pool/transport cleanup is joinable. There is no pool-side background tracker or out-of-turn diagnostics capture. This follow-up must preserve that baseline rather than reintroduce early release, and is where persistent background sessions belong.
- **What should change:** an explicit `background_policy` for enhanced lifecycle tracking (default off/`deny`), bounded quiescence, and explicit visibility instead of silent drops. It does not change default permission bypass or claim rollback of external effects.

The current bounded, nonpersistent background-draining restriction is accepted for
the bypass slice. OS retirement itself is fail-closed and can remain pending while
its supervisor retains descendants/state; it is not a bounded containment
guarantee. Completion relies on verified supervisor exit, not the advisory marker.
Persistent OS cleanup failure/owner death must not authorize reuse or state deletion.

## V1 Slice
- **Delivers:** operator-selectable background tracking on existing local-only, honor and runtime-identity gates. Tracking `allow` records active work, protects unsettled-runtime reuse, surfaces bounded redacted diagnostics, and quiesces or terminates work on cancel/reap/reconfiguration/shutdown. Tools run under the connector's permission policy (default bypass), without a separate permission-profile certification.
- **First consumer:** the `claude-acp` instance on a single-user loopback deployment with `background_policy: allow` and the certified runtime profile.
- **Safe degradation:** tracking off preserves the selected permission policy (default bypass). Missing background-tracking evidence keeps enhanced tracking rejected, not native bypass tools disabled. Unknown or oversized tracked payloads become bounded diagnostics; post-output failures stay terminal and external effects are never claimed as reversed.

## Deferred

- Interactive steering or in-band input for running background tasks and subagents (the foreground turn model is unchanged).
- Durable background state across proxy restarts; V1 owns background work only within a live runtime's lifetime.
- Per-task budgets, schedules, or quotas beyond the single bounded shutdown/quiescence deadline.
- Background visibility in billing or usage accounting beyond authoritative adapter reports.
- MCP-provided tools inside background work, and ACP v2 asynchronous surfaces (separate follow-ups #1193/#1194).
- macOS/Windows certification of the background-enabled profile (Linux/amd64 deny/allow only until separately certified).

## Boundary Context
- **In scope**: `claudeagentacp` configuration (`background_policy`), enablement metadata and lifecycle ownership, bounded out-of-turn diagnostics in `connector-support/acp`, certification evidence recording, operator docs and configuration example.
- **Out of scope**: changing the selected permission policy or its bypass default, claiming enhanced tracking without validated runtime evidence, rollback of external effects, steering/streaming background work into the HTTP turn, changing other ACP connectors' behavior, any Anthropic OAuth or credential handling.
- **Adjacent expectations:** the operator provisions the same single credential source as V1 and accepts that background work runs with the same local trust as the foreground agent.
- **Trust model**: unchanged from the archived connector spec; background work is agent-owned local work with operator-approved tooling, never a security boundary against the same user.
- **Boundary ownership**: backend plugin (`connectors/claudeagentacp`) plus connector support (`connector-support/acp`, additive opt-in seams).
- **Revalidation triggers**: adapter/runtime profile changes; adapter background/subagent notification or capability changes; transport out-of-turn policy changes; process-tree lifecycle changes.

## Requirements

### Requirement 1: Explicit background policy and fail-closed enablement
**Objective:** As a single-user operator, I want an explicit opt-in for enhanced background tracking, preserving the independently selected permission policy.

#### Acceptance Criteria
1. While background tracking is unset or `deny`, the connector shall preserve the selected permission policy's session metadata, child environment and approval behavior; explicit permission deny remains byte-identical to the archived tools-off posture.
2. When `background_policy: allow` is selected, the connector shall require the existing gates (local-only posture, `admin_policy: honor`) and a recorded certification of the exact adapter/runtime profile for background enablement; otherwise it shall reject the instance before any process exec, naming the unmet prerequisite.
3. While tracking `allow` is active, the connector shall track only covered notification forms, changing no credential source, root, environment allowlist or permission policy.
4. The connector shall reject `allow` in multi-user deployments and shall reject enablement after any runtime or sidecar identity drift, exactly like the existing certification gates.
5. Background tools shall run under the connector's permission policy (default bypass, explicit deny), without a separate permission-profile prerequisite; enhanced lifecycle tracking shall not widen permission deny.

### Requirement 2: Bounded lifecycle owner for background activity
**Objective:** As an operator, I want background work owned and bounded, so that no orphaned process or unsettled runtime silently corrupts later turns.

#### Acceptance Criteria
1. When an HTTP turn's stream ends while the runtime still reports active background work, the connector shall record the runtime as unsettled rather than idle; idle reaping shall not treat it as reapable without quiescence or explicit termination.
2. When a new foreground turn targets an unsettled runtime, the connector shall either wait boundedly for quiescence or replace the process with explicit, logged disclosure; it shall never silently interleave a foreground turn into unsettled background state.
3. When the client cancels a turn, the connector shall end the foreground stream with exactly one terminal outcome and shall terminate owned background work for that runtime within the bounded shutdown deadline.
4. When the proxy stops, the runtime is idle-reaped, or a reconfigure removes the instance after its admitted work drains, the connector shall quiesce or terminate all owned background work and the process tree within the bounded deadline; no owned descendant shall outlive the runtime.
5. The connector shall never claim rollback of external effects produced by approved background or foreground work.

### Requirement 3: Explicit visibility instead of silent drops
**Objective:** As an operator, I want background activity and out-of-turn events visible, so that nothing the agent does is silently discarded.

#### Acceptance Criteria
1. While `allow` is active, out-of-turn notifications describing background or subagent activity shall become bounded, redacted diagnostics attributable to the runtime instead of transport-level silent drops.
2. While background tracking is off, notification drops shall remain unchanged, and approval behavior shall follow the selected permission policy, including bypass between-turn approval.
3. Background visibility shall never fabricate assistant content, reorder model-authored content, or report tokens beyond authoritative adapter reports.
4. Unknown, malformed or oversized background payloads shall be bounded and redacted like existing diagnostics, never retained raw.

### Requirement 4: Failure, cancellation and termination semantics
**Objective:** As a client, I want background-capable turns to fail and end like every other turn, so that clients need no background-specific handling.

#### Acceptance Criteria
1. If background work fails after foreground output, the connector shall end the stream with an error and never retry or fail over, exactly like existing post-output failures.
2. If background work fails before any foreground output, the existing pre-output failover eligibility and classification (authentication, plan exhaustion, capability) shall apply unchanged.
3. If background work is stuck beyond the bounded deadline, the connector shall terminate and reset the runtime; a subsequent compatible turn shall replay history into a replacement process without user-visible corruption.
4. Cancellation during active background work shall end with exactly one terminal outcome and shall not produce duplicate or late content events.

### Requirement 5: Certification evidence and disclosure
**Objective:** As the maintainer, I want enablement tied to recorded evidence, so that `allow` ships only with demonstrated lifecycle behavior.

#### Acceptance Criteria
1. Enabling `background_policy: allow` shall require a recorded certification run against the exact adapter/runtime profile demonstrating: background start and completion between turns, out-of-turn visibility, cancel with active background work, shutdown with active background work, and stuck-work replacement.
2. The connector shall disclose the active background posture (deny default, allow enabled) in startup logging and resolved-profile evidence without secrets.
3. Operator documentation shall cover the two policies, the certification requirement, single-user limits, and the no-rollback disclosure; a disabled configuration example shall demonstrate `allow`.
4. The deny posture and all other ACP connectors' behavior shall remain covered by the existing tests without behavioral change.
