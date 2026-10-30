# Requirements Document

## Project Description (Input)
Who has the problem: developers who use several coding agents (Codex, Claude Code, OpenCode and others) through an AIProxer deployment, and operators who run AIProxer as a shared, multi-user proxy server.

Current situation: OpenAI Codex has a persistent memory system (`codex-rs/memories`). It learns from a user's past sessions in two background phases: per-session extraction, then consolidation into a `MEMORY.md` handbook and a short `memory_summary.md`. It injects that summary into new sessions, and the model consults the handbook on demand. Codex keeps all of this in files on the user's machine, and only Codex can use it. Other agents have no memory, or incompatible memories of their own. AIProxer sees every request and response of every agent that routes through it, but remembers nothing across sessions. Issue #820 (which absorbs #473) asks for this memory at the proxy.

What should change: AIProxer offers a minimal, faithful re-implementation of Codex memory (version 1 layout) at the proxy, driven only by traffic that passes through it. Each user gets a separate memory per project, and strict separation between users is the core contract. The proxy may run as an enterprise server for many users. It never runs on the user's machine and never reads the user's files. Improvements beyond Codex behaviour are deferred until the minimal version is in use.

## Introduction
Proxy Memory reproduces the Codex memory lifecycle at the proxy:

1. Record the conversation of each eligible session, as the proxy observes it.
2. When a session has been idle long enough, extract a raw memory and a session summary from it with an auxiliary model call.
3. Consolidate a memory bank's extraction results into a handbook and a short summary with a second auxiliary model call.
4. Inject that summary into each new session of the same user and project.
5. Let the model read and search the handbook, and save explicit notes, through proxy-hosted memory tools.

Codex's local equivalents of these steps are rollout files on disk, an agent that edits files in a memory folder, and shell reads of that folder. Here they become proxy-held records, single structured model calls, and an authenticated tool endpoint, so the feature works for many users on a shared server.

A memory bank belongs to exactly one tenant, one principal and one project. No request, tool call, control call, or background job can read or change another principal's bank.

## V1 Slice
- **Delivers**: For an allowlisted, authenticated user, the following are retained in one memory bank per project:
  - durable knowledge learned in one session through the proxy;
  - explicit notes the user saved.

  That knowledge is injected into the user's later sessions in the same project, whichever coding agent they use, and the model can read and search it on demand. The user can list, inspect, and forget their banks.
- **First consumer**: Coding agents that keep a session across turns (a proxy-issued session identifier or a client session hint) and whose requests carry a project path. They do this either through the existing workspace header or through their standard environment block (Codex `<environment_context>`, Claude Code and OpenCode `Working directory:`), with the feature row enabled.
- **Safe degradation**: Whenever ownership, project or storage cannot be established, the session runs exactly as without the feature: no injection, no capture, no tool results. The proxy logs and counts the degradation. Background failures leave the last published memory in effect. Capture never delays or alters a client response.

## Deferred
- Zero-configuration memory tools: the proxy injects and executes the memory tools itself, without the client adding the tool endpoint. Follow-up: TBD (needs a proxy-executed tool loop).
- A user-wide memory layer shared across one user's projects, with explicit promotion. Follow-up: TBD.
- Import of existing Codex, OpenCode or Claude Code memories through a client-side tool. Follow-up: TBD.
- Codex `skills/` procedures and scripts. Follow-up: TBD.
- Usage-based selection and pruning (Codex `usage_count`, `last_usage` and `<oai-mem-citation>` parsing). V1 uses Codex's documented fallback of recency by generation time. Follow-up: TBD.
- Retaining raw session transcripts after extraction, for exact-evidence search (Codex `rollout_path` lookups). Follow-up: TBD.
- Codex memory version 2 layout and prompts. Follow-up: TBD.
- Project aliases for worktrees, monorepos, and the same repository at different paths or on different machines. Follow-up: TBD.
- Withdrawing already-injected memory from sessions that are still running when the user forgets a bank. Follow-up: TBD.
- Classifying and skipping agent sub-sessions (sub-agents, title generation) beyond the request-shape rules in Requirement 3. Follow-up: TBD.
- Per-owner fairness and quotas for background extraction work. Follow-up: TBD.
- Cleanup of stored memory for principals removed from the allowlist, or after the feature is disabled. V1 keeps the data, and documents that operators delete it in the database. Follow-up: TBD.
- Encryption of stored memory at rest, operator plaintext inspection with audit, and team or tenant-shared memory with access lists. Follow-up: TBD.
- Dashboards, alerts and memory-specific admin read models beyond the counters in this spec. Follow-up: TBD.

## Boundary Context
- **In scope**:
  - per-principal enablement and the isolation contract;
  - project identification from request content;
  - transcript capture from passing traffic;
  - background extraction and consolidation;
  - first-turn injection;
  - an authenticated memory-tool endpoint;
  - authenticated status, list and forget controls;
  - bounded counters and logs for degradations.
- **Out of scope**:
  - any access to the user's machine or files;
  - any change to client-visible responses;
  - routing, failover or billing rules;
  - native memory systems inside agents;
  - all Deferred items.
- **Adjacent expectations**:
  - Authentication supplies the principal.
  - The existing workspace header supplies an optional project path.
  - The durable continuity store (SQLite or PostgreSQL) supplies persistence and the once-per-session injection decision.
  - Auxiliary request execution performs and accounts for the background model calls.
  - Operators should tell users to disable an agent's native memory, to avoid duplicate memories.
- **Boundary ownership**:
  - The feature plugin owns memory policy, prompts, rendering and orchestration.
  - Infrastructure owns persistence.
  - The feature host owns composition, workers and the HTTP projection.
  - The standard HTTP layer owns the endpoint mount.
  - Core changes only to pass the accepted request to the existing session-bootstrap hook.
- **Revalidation triggers**:
  - conversation-view bootstrap semantics;
  - request-transform or final-stream-observer stage contracts;
  - authentication scope fields;
  - the canonical message, item and event representations;
  - the workspace-hint extension keys.

## Requirements

### Requirement 1: Enablement and owner isolation
**Objective:** As an operator of a shared proxy, I want memory enabled only for principals I approve and strictly partitioned per principal, so that no user's memory can reach another user.

#### Acceptance Criteria
1. The Proxy Memory feature shall stay disabled unless its feature registration is explicitly enabled.
2. When the feature is enabled, the Proxy Memory feature shall treat a request as memory-eligible only if:
   - the authenticated principal identifier is known and non-empty;
   - the request originates from a client rather than from the proxy itself;
   - the principal appears in the operator's memory principal allowlist, or the allowlist is the wildcard.
3. If the authenticated principal is the local unknown-OS fallback principal, then the Proxy Memory feature shall treat the request as not memory-eligible regardless of configuration.
4. If the authenticated principal has the local subject kind and the operator has not enabled local-principal memory, then the Proxy Memory feature shall treat the request as not memory-eligible.
5. The Proxy Memory feature shall read, inject, search, return, modify or delete a memory artifact only for the authenticated caller or background job acting for that artifact's owner. The owner is the tenant, principal and project recorded when the artifact was first stored.
6. If the feature cannot determine eligibility or reach memory storage for a request, then the Proxy Memory feature shall serve the request without memory, record a degradation counter, and log the degradation without user content.
7. If the operator enables the feature without a durable continuity store, then the proxy shall reject the configuration at load or reload time with an error naming the missing prerequisite.
8. If the operator enables the feature without both an extraction route and a consolidation route, then the proxy shall reject the configuration at load or reload time with an error naming the missing setting.
9. If a request carries neither a proxy-issued session identifier nor a client session hint, then the Proxy Memory feature shall treat it as not memory-eligible and count it.

### Requirement 2: Project identification from traffic
**Objective:** As a user, I want my memory kept separately for each project, derived only from what my agent already sends, so that facts from one project do not appear in another.

#### Acceptance Criteria
1. When an eligible request carries a workspace hint (the workspace header or an existing workspace-path request extension), the Proxy Memory feature shall derive the project from that hint.
2. When an eligible request carries no workspace hint, the Proxy Memory feature shall derive the project from the working directory in one of these places:
   - the request's instructions or system/developer messages (a `Working directory:` or `Primary working directory:` line);
   - a user content block that begins with a Codex `<environment_context>` element containing `<cwd>`.
3. If no project can be derived for a session, then the Proxy Memory feature shall neither inject, capture nor expose memory for that session.
4. The Proxy Memory feature shall decide a session's project at the session's first turn. It shall not move captured content of a session to a different project on later turns.
5. The Proxy Memory feature shall treat a derived path as an opaque label. It shall never read, resolve or access that path on any filesystem.
6. The Proxy Memory feature shall not write project paths or memory text to logs or metric labels.

### Requirement 3: Session capture from passing traffic
**Objective:** As a user, I want the proxy to record what happened in my coding sessions, the way Codex records rollouts, so that memory can be learned later without slowing my agent.

#### Acceptance Criteria
1. When a turn of an eligible session completes and its response has been released to the client, the Proxy Memory feature shall record the session's transcript. The transcript holds the user and assistant messages, tool calls and tool outputs from the request history, followed by the released assistant output.
2. The Proxy Memory feature shall exclude the following from transcripts:
   - system, developer and instruction content;
   - reasoning content;
   - images and files, replaced by omission placeholders;
   - user content blocks that are AGENTS.md instruction blocks or skill blocks.
3. The Proxy Memory feature shall redact secret-like values before storing a transcript. It shall use at least the Codex redaction patterns: OpenAI-style keys, AWS access key IDs, bearer tokens, and key, token, secret or password assignments.
4. If a backend attempt fails, is cancelled or is replaced before its response is released to the client, then the Proxy Memory feature shall record nothing from that attempt. When no attempt of a turn is released, it shall record nothing for that turn.
5. The Proxy Memory feature shall record transcripts without delaying, buffering or altering the client response stream. When its bounded capture queue is full, it shall drop the capture, and count and log the drop.
6. The Proxy Memory feature shall bound each stored transcript to the configured byte limit, keeping the newest content, and shall count and log each truncation.
7. The Proxy Memory feature shall never record requests that the proxy itself issues.

### Requirement 4: Background extraction (Codex phase 1)
**Objective:** As a user, I want each finished session distilled into a raw memory and a summary, so that durable lessons survive without keeping whole transcripts.

#### Acceptance Criteria
1. While a captured session has been idle for at least the configured idle period and its last activity is within the configured maximum session age, the Proxy Memory feature shall extract memory from it in the background through the operator-configured extraction route.
2. The Proxy Memory feature shall build each extraction request from the Codex version 1 stage-one prompts, applied to the session transcript truncated to the configured input token limit. It shall accept only a result with a raw memory, a session summary and an optional slug.
3. When an extraction succeeds, the Proxy Memory feature shall redact and store the result, replace any earlier result for the same session, and delete the session's stored transcript.
4. When a session receives new turns after its extraction, the Proxy Memory feature shall extract it again after it becomes idle again.
5. If an extraction fails or returns an invalid result, then the Proxy Memory feature shall keep the transcript, store no partial result, and retry after the configured backoff.
6. While several proxy instances share one database, the Proxy Memory feature shall let at most one instance at a time extract a given session.
7. The Proxy Memory feature shall issue extraction calls as proxy-internal auxiliary requests attributed to the memory owner's principal.
8. When a stored transcript's last activity is older than the configured maximum session age, the Proxy Memory feature shall delete it without extraction.

### Requirement 5: Background consolidation (Codex phase 2)
**Objective:** As a user, I want each memory bank consolidated into a handbook and a short summary, so that future sessions start with the most useful knowledge.

#### Acceptance Criteria
1. When a memory bank has extraction results or explicit notes newer than its last published memory, the Proxy Memory feature shall consolidate the bank in the background through the operator-configured consolidation route. It shall use the Codex version 1 consolidation prompt and produce a new handbook and a new memory summary.
2. The Proxy Memory feature shall consolidate at most the configured number of most recent extraction results, considering only results generated within the configured maximum unused age.
3. The Proxy Memory feature shall publish a consolidation result as a new version only if no other version of that bank was published since the consolidation began.
4. While several proxy instances share one database, the Proxy Memory feature shall let at most one instance at a time consolidate a given bank.
5. If a consolidation fails or returns an invalid result, then the Proxy Memory feature shall keep the previously published memory in effect and retry after the configured backoff.
6. When a bank has no extraction results and no notes left within the selection window, the Proxy Memory feature shall publish an empty memory for it.
7. When a consolidation is published, the Proxy Memory feature shall delete only the explicit notes that consolidation included in full.
8. The Proxy Memory feature shall issue consolidation calls as proxy-internal auxiliary requests attributed to the bank owner's principal.
9. If a consolidation's input exceeds the configured input limit, then the Proxy Memory feature shall leave out the oldest extraction results and any notes that do not fit, keep omitted notes for a later consolidation, and count and log the truncation.

### Requirement 6: Memory injection into new sessions
**Objective:** As a user, I want my project memory placed in front of the model when a new session starts, so that my agent benefits without any client change.

#### Acceptance Criteria
1. When the first turn of an eligible session starts and the session's bank has a non-empty published memory summary, the Proxy Memory feature shall add the memory instructions as proxy-owned instructions that the model sees and the client does not. These are the Codex read-path guidance adapted to memory tools, the summary truncated to the configured token limit, and the bank's opaque memory scope identifier.
2. The Proxy Memory feature shall keep the injected memory unchanged for every later turn, retry and failover of that session, even after a newer version is published.
3. If the session is not eligible, has no project, or its bank has no published summary, then the Proxy Memory feature shall leave the model-bound request unchanged.
4. If loading memory for injection fails, then the Proxy Memory feature shall continue the turn without memory and record the degradation.
5. The Proxy Memory feature shall not inject memory into a session whose first turn happened before memory became available to that session.

### Requirement 7: Memory tools endpoint
**Objective:** As a user, I want my agent to look up details and save notes on demand, the way Codex reads its memory folder, using only my proxy credentials.

#### Acceptance Criteria
1. Where the feature is enabled, the proxy shall expose an authenticated Model Context Protocol endpoint offering the tools `memory_list`, `memory_read`, `memory_search` and `memory_add_note`.
2. When a memory tool is called with a memory scope identifier, the Proxy Memory feature shall act only on the bank with that identifier that belongs to the authenticated caller. For any other identifier it shall return the same not-found result as for an unknown one.
3. When `memory_read` is called with a document path, the Proxy Memory feature shall return that document's content, limited to the requested line range and the configured size limit. Valid paths are `MEMORY.md`, `memory_summary.md` and `rollout_summaries/<name>.md`.
4. When `memory_search` is called with a query, the Proxy Memory feature shall return the matching lines of the bank's documents with their document paths and line numbers, up to the configured result limit.
5. When `memory_add_note` is called with note text, the Proxy Memory feature shall redact and store the note, within the configured size limit, for the bank's next consolidation.
6. If a request to the endpoint is not authenticated, or comes from a non-eligible principal, then the proxy shall reject it without revealing whether any memory exists.

### Requirement 8: User memory controls
**Objective:** As a user, I want to see and erase what the proxy remembers about me, so that I stay in control of retained data.

#### Acceptance Criteria
1. When an authenticated eligible user lists memory banks, the Proxy Memory feature shall return only that user's banks, each with its scope identifier, project label, published version and time, and pending-work counts.
2. When an authenticated eligible user requests one of their banks, the Proxy Memory feature shall return its status and published memory summary.
3. When an authenticated eligible user forgets one of their banks, the Proxy Memory feature shall delete all of that bank's transcripts, extraction results, notes, published memory and pending jobs. After that, no new session, tool call or control call shall return the deleted content.
4. When a bank is forgotten while sessions are running, the Proxy Memory feature shall leave the memory already injected into those sessions until they end, and shall document this limit to users and operators.
5. If a user requests or forgets a bank that is not theirs or does not exist, then the Proxy Memory feature shall respond not found.
6. When a bank is forgotten, the Proxy Memory feature shall permanently stop capturing the sessions that had already contributed to it, and shall discard any extraction or consolidation of that bank that was already running.
