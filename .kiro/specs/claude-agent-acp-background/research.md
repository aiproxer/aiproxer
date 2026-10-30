# Research: claude-agent-acp-background

## Archived V1 exclusion posture — main `e5662018`
- Session metadata (`connectors/claudeagentacp/internal/product/authenv.go`, `HandshakeProfile`): `agents: {}`, `disallowedTools: ["Agent","Task","TaskOutput","TaskStop"]`, `mcpServers: {}`, `settingSources: []`, tools-off; permission policy `deny` only.
- Process environment (`AuthEnv.Prepare`): `CLAUDE_CODE_DISABLE_BACKGROUND_TASKS=1` for every spawn.
- Transport (`connector-support/acp/transport_stdio.go`): with `RefuseOutOfTurnRequests`, out-of-turn server requests get `-32601`/cancel replies and out-of-turn notifications are dropped silently.
- Archived spec (`archive/claude-agent-acp-connector/requirements.md` Deferred): "Support for detached/background tasks and subagents. V1 excludes these tools and refuses requests outside an active turn; it does not promise rollback of operator-approved external effects."

## Upstream evidence (certified adapter 0.89.1 bytes, offline cache)
- The adapter ships dedicated modules for this capability: `dist/acp-subagents.js`, `dist/async-tasks.js`, with `finishLifecycle` finishing `subagents.finishAll`/`asyncTasks.finishAll`, and a `forwardSubagentText` capability.
- `session/update` notifications for subagent/task lifecycle and `_meta` task metadata are emitted through the same routed path as ordinary updates; out-of-turn emissions therefore reach the transport like any notification.
- Issue #705 comments cite upstream reliability reports (orphaned children, blocked async notifications, stuck background-subagent turns, background visibility) — the reason V1 refuses by default and why a bounded lifecycle owner, not raw enablement, is the requirement.

## Unverified until task 2.1
- Exact notification kinds/shapes for task and subagent lifecycle on the pinned profile; the enablement metadata shape `agents` expects under `allow`; stuck-detection signals vs. normal long-running work; whether the adapter emits a quiescence signal after `finishLifecycle`.
- These unknowns are why task 2.1 records fixtures before any wiring and why the tracker treats unknown kinds as diagnostics-only.

## Permission-policy baseline
- The owner replaced tools-off-by-default with full bypass and automatic one-time approval, including between turns. Native Agent/Task tools no longer depend on a permission certification. Explicit permission deny retains the archived restrictions above. A sidecar barrier now holds the prompt until SDK idle and no live tasks, and stops tasks before query retirement on failure. This follow-up adds enhanced tracking/visibility, not a missing reuse barrier or new tool authorization gate.

## Certification implications
- Enhanced background tracking must bind the existing runtime/sidecar/Node identities and its own lifecycle evidence. Native tools already follow the independent permission policy; this evidence is not a prerequisite for default bypass.
- Reuse of the existing history-replay reset path for replacement is the tested alternative to claiming rollback; external effects are never presented as reversed.
