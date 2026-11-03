# Proxy memory (Codex-parity project memory)

`proxy-memory` gives coding agents the same durable project memory Codex keeps locally, but owned by the proxy: eligible turns are captured from the A-leg, a background auxiliary model extracts and consolidates them per project, and the published summary is reinjected as a backend-only system overlay on the first turn of each new eligible session. It is disabled by default. The outer registration's `enabled` controls activation; `config` accepts only the keys below, and an unknown key or a nested `enabled` is rejected.

Memory is scoped per user and per project: one bank per `(tenant, principal, project)`. The project is derived from the request itself — an explicit workspace hint (`X-AIP-Workspace`, when the client sends one), a `Working directory:` instruction line (Claude Code and similar harnesses), or a Codex `<environment_context><cwd>…</cwd>` block — and stored as an opaque scope identifier (a SHA-256 of the normalized path). Two users over the same path share nothing.

## Requirements

* **A durable continuity database.** Proxy memory needs a durable continuity store: the memory tables live in their **own** database, never in the continuity database, so a memory write can never hold the connection or the write lock the mandatory core path needs. On SQLite that is a separate file (`sqlite_path`, by default `proxy-memory.db` next to the continuity file); on PostgreSQL it is the same logical database on a dedicated pool, so an advisory write can never starve the mandatory pool. A `sqlite_path` that resolves to the continuity file itself is refused before the file is opened (the same file under another spelling, a symlink or a hardlink included), and the derived default is refused too when the continuity file is named `proxy-memory.db`; the candidate generation then fails at compile time (the active generation keeps serving). Enabling the feature without a durable store fails the [candidate generation](runtime-config-reload.md) at compile time (the active generation keeps serving), so a half-working memory surface is never published. Operators with the memory-only continuity store must add a durable one first.
* **The background auxiliary client.** Both routes below must resolve; extraction and consolidation run as background auxiliary jobs with `OriginInternal` scope, so they can never recurse into capture or injection.
* **A backend that advertises both phase capabilities.** Both phases request a JSON response (`application/json` response MIME type), which makes `structured_outputs` a **required** capability: it is not soft-strippable, so a backend that does not advertise it is excluded before any upstream work. No other built-in in-process backend advertises it or exposes a config key to add it; the generic `custom-openresponses-compatible` mode can declare `structured_outputs`, but it is not in that mode's default list, so declare it explicitly (an external connector can also declare it in its manifest). The phases send legacy message-authority calls, and a generic OpenResponses backend projects those to ordered items, so it must also advertise `ordered_items` (part of its default list). A route missing either prerequisite keeps failing phase 1 on the ordinary retry path and publishes no bank; memory never degrades to unstructured output.
* **Authentication with per-user API keys.** Eligibility is the ordered rule set: known principal, not internal origin, not the local fallback principal (`aip_local_unknown`), not a local principal (unless `allow_local_principal` is set), and present in `principals`. `principals: ["*"]` accepts every eligible principal — **never use a shared key**, because a shared key collapses every user into one principal and one memory bank. On a listener without an API-key policy, requests authenticate as the proxy process's OS identity — the OS username/UID, or the `AIP_OS_PRINCIPAL_ID`, `USER` and `USERNAME` hints — and only an identity that cannot be resolved at all falls back to `aip_local_unknown`. Local principals are excluded by default; such a request becomes eligible only with `allow_local_principal: true` **and** a matching `principals` entry (`["*"]` matches), while `aip_local_unknown` is rejected unconditionally. Every credential-free caller of that listener shares that single OS-derived principal and one memory bank, so give each user a distinct API key on shared deployments.
* **Established sessions only.** A turn is captured or injected only when it carries a proxy-issued session identifier or a client session hint. A request with neither allocates a fresh A-leg every time, so it counts as no session (`capture_no_session`, `inject_no_session`) and is skipped.

## Schema lifetime

The memory store keeps its schema history in `bun_proxy_memory_migrations`, inside the memory store's database. Migration `20261016000000` is the **frozen first-release baseline**; after the freeze, every schema change is a new migration with a later version prefix. Editing the baseline does not upgrade an existing database: an applied migration is recorded and never replayed, so a database that already recorded `20261016000000` keeps the tables and columns it had.

**Fresh install.** An empty memory database is initialized on first use: the baseline and every unapplied forward migration run in order, and each is recorded only after it succeeds, so a failed migration is retried on the next open.

* **SQLite:** the proxy creates the file at `sqlite_path` (by default `proxy-memory.db` next to the continuity file) and applies the schema to it.
* **PostgreSQL:** the proxy applies the schema to the continuity logical database on the dedicated memory pool; the connection must be allowed to create and alter tables and indexes there.

**Supported upgrades.** A memory database created from the frozen baseline is upgraded in place at open. Forward migrations add tables, indexes and columns with defaults, and preserve the rows already in the store.

* **SQLite:** copy the configured memory database file (`sqlite_path`) while the proxy is stopped, then start the new build against the same path.
* **PostgreSQL:** take the usual PostgreSQL backup or point-in-time recovery point, then start the new build against the same DSN.

**Pre-freeze databases: fresh install only.** Before the freeze the baseline was still being edited in place, so a database created by a pre-release build can have `20261016000000` recorded while missing tables and columns a later build queries (for example `proxy_memory_session_bindings`, or `proxy_memory_sessions.source_seq`). The history table records the version name, not the shape it created, so nothing distinguishes such a database from a current baseline. The proxy **does not inspect an existing schema**: it skips the migrations the database already recorded and does not replay the edited baseline, so the objects the current baseline would create stay absent and the failure surfaces later — at the first capture, admission read or worker step that uses a missing object — not at startup. Such a database is supported by **fresh install only**:

* **SQLite:** stop the proxy, move the old file aside (keep it; the proxy never removes or resets the database for you), and let a fresh file be created at the same path.
* **PostgreSQL:** stop every proxy instance that uses this store, take a copy, then drop only the `proxy_memory_*` tables and delete their rows from `bun_proxy_memory_migrations`. The logical database is shared with the continuity store, so never drop the continuity tables.

Data in a pre-freeze memory database is not migrated; preserving it is a manual, operator-owned step. A disposable old test store may be recreated only when it is explicitly isolated from continuity and user data.

## Configuration

Merge this row into your existing `plugins.features` list (the tracked [`config.yaml`](../config.yaml) and the distribution [`config/config.yaml`](../config/config.yaml) include the disabled example):

```yaml
plugins:
  features:
    - id: proxy-memory
      enabled: true
      config:
        principals: ["alice", "bob"]   # per-user API-key principal IDs; never list a shared key
        extract_route: "openai:gpt-5-mini"
        consolidate_route: "openai:gpt-5"
```

Every key and its default:

| Key | Default | Meaning |
|---|---|---|
| `principals` | required | Allowlisted principal IDs; `["*"]` alone means every eligible principal |
| `allow_local_principal` | `false` | Allow credential-free `local_noop` requests under their OS-resolved proxy identity (username/UID, or `AIP_OS_PRINCIPAL_ID`/`USER`/`USERNAME`), subject to `principals`; the unresolved-identity fallback (`aip_local_unknown`) stays ineligible |
| `extract_route` | required | Route selector for phase 1 extraction |
| `consolidate_route` | required | Route selector for phase 2 consolidation |
| `models` | derived | Per-phase model policy: the default target, the lock set and the allowed inventory of each phase |
| `funding_mode` | none | How the background phases' provider calls are funded: `customer_funded` (AIProxer Enterprise) or `operator_sponsored`. Without it the phases run usage-only on a build without billing, and are refused where a monetary binding is bound — see [Background funding](#background-funding) |
| `sponsor_keys` | none | The sponsor keys the operator declared for the background work. A `sponsor_key` outside this set is refused and counted |
| `sponsor_key` | none | The selected sponsor key; it must be one of the declared `sponsor_keys` |
| `sqlite_path` | derived | The proxy-memory database file (SQLite continuity store only). Empty selects `proxy-memory.db` next to the continuity database. The memory database is always separate from the continuity database: a path that resolves to the continuity file is refused, and a non-durable target (`:memory:`) is refused too, because the memory database must be durable. |
| `min_idle` | `6h` | Idle time before a captured session becomes claimable |
| `max_session_age` | `240h` | Sessions older than this are never extracted |
| `recall_timeout` | `50ms` | Total budget of the read-only first-turn recall (the admission snapshot read) |
| `max_unused_age` | `720h` | Extractions older than this are not consolidated |
| `max_raw_memories` | `256` | Extractions sent to one consolidation attempt; remaining ones stay queued |
| `poll_interval` | `5m` | Worker tick interval |
| `phase1_batch` | `8` | Extraction attempts per tick |
| `phase2_batch` | `4` | Consolidation attempts per tick |
| `phase1_input_tokens` | `150000` | Phase 1 transcript budget |
| `phase2_input_tokens` | `150000` | Phase 2 input budget |
| `phase2_note_bytes` | `65536` | Note bytes sent to one consolidation |
| `summary_tokens` | `2500` | Injection summary budget (max 4000) |
| `max_transcript_bytes` | `8388608` | Per-transcript cap (max 64 MiB) |
| `lease` | `1h` | Claim lease; also recovers work from crashed instances |
| `retry_backoff` | `1h` | Retry delay after a failed attempt |
| `aux_timeout` | `10m` | Background auxiliary collection timeout |
| `max_output_bytes` | `1048576` | Auxiliary output cap |
| `max_note_bytes` | `8192` | Explicit note size cap |
| `max_read_bytes` | `65536` | Memory read cap (minimum 16) |
| `max_search_results` | `50` | Search hit cap |

Invalid or incomplete configuration fails the candidate generation; a reload that fails leaves the previous generation usable.

### Per-phase model policy

The optional `models` subtree gives each background phase its own target, its own lock set and its own allowed inventory; the two phases are governed independently. Declaring the subtree is an explicit choice: it must declare **both** phases, and every phase must declare its `defaults`, its `locks` and its `allowed` inventory. A configuration without the subtree keeps the previous behaviour unchanged, with no field locked, and an individual `defaults` field that is absent keeps the operator's default route.

```yaml
models:
  extraction:
    defaults:
      backend: openai          # optional; the default route's backend when absent
      model: gpt-5-mini        # optional; the default route's model when absent
      reasoning_effort: low    # optional; "low" for extraction, "medium" for consolidation when absent
    locks:
      backend: false           # optional; a field the operator does not name is not locked
      model: true
      reasoning_effort: false
    allowed:
      backends: [openai, anthropic] # the backends a user may choose
      models: [gpt-5-mini, gpt-5]   # the models a user may choose
      targets:                      # the reasoning efforts a user may choose, per target
        - backend: openai
          model: gpt-5-mini
          reasoning_efforts: [low, medium]
        - backend: anthropic
          model: gpt-5
          reasoning_efforts: [high]
  consolidation:
    defaults:
      backend: openai
      model: gpt-5
      reasoning_effort: medium
    locks: {}
    allowed:
      backends: [openai]
      models: [gpt-5]
      targets:
        - backend: openai
          model: gpt-5
          reasoning_efforts: [medium, high]
```

The route selector is **derived**, never a user field: it is built from the phase's default route (`extract_route`, `consolidate_route`) and the phase's declared backend and model, so a client can never point a phase at a target the operator did not name. An absent backend or model keeps the operator's default; an absent effort keeps today's value. The derivation replaces the target only: the operator's global parameters, leaf annotations and leaf query are preserved.

A default route that names more than one target (a failover, weighted or parallel selector) is refused **when a phase declares a backend or a model**, because such a route cannot carry one declared target. A phase that declares no target keeps the operator's route as it is, with its own failover policy.

The `allowed` inventory is a **policy** statement about what a user may choose, not a claim that a provider supports the listed values: an empty list means "no user-selectable override", not "everything allowed", and the provider's own reasoning-effort values are not enumerated anywhere. The allowed efforts are declared per target, because the effort values are provider-specific: the same effort can be allowed for one target and refused for another.

### Per-field locks

A lock is **per phase and per field**: `backend`, `model` and `reasoning_effort` are locked independently, and the two phases are locked independently. A locked field resolves to the operator's value, so the effective value of a field is the **locked operator value, else the permitted owner value, else the operator default**; a partial override changes only the fields it names.

A lock is enforced on both sides of the boundary:

* a **write** that changes a locked field is refused explicitly, naming the phase and the field and never the value; a write that names a permitted field and a locked one is refused whole, and a write that repeats the locked value changes nothing and is idempotent;
* the **runtime** resolves the phase through the same lock set, so an owner value for a locked field never reaches the effective target.

A lock that is introduced **after** an owner override already existed wins: while the lock applies, the effective target is the locked operator value and the owner's value for that field is not effective. The returned settings state omits the locked field from the owner's overrides.

That state is a **pure projection**: it does not change the owner's stored settings. Reusing it is **not** a durable invalidation — an owner override that is still stored and is read again after the lock is removed becomes effective again. The durable handling of a stale override, and its reconciliation with the stored unprojected overrides, is **not implemented yet** and remains required for the persistence, read and reload acceptance; the guarantee that a stale override does not survive is not delivered by this subtree alone. A lock is a policy statement about the field, not about the provider: the checks against the final compiled target (an alias or a query parameter that points elsewhere) belong to the enforcement against the compiled actual target, not to this subtree.

### Enforcement against the compiled actual target

A phase that declares the `models` subtree is checked against the **final compiled actual target** before it is dispatched: the target the runtime addresses after the model-alias resolution and the route-parameter merge, not the target the phase asked for. The checks are the inventory check, the lock check and the capability check, and a phase that fails one of them is **refused explicitly** — it is never dispatched against an unchecked target, and nothing is silently substituted or downgraded.

* the **inventory** is the closed set of values the phase may use, so a compiled actual target outside a declared list is refused. A list the operator did not declare constrains nothing at dispatch: the phase keeps the operator's own route, and no owner may select a value for that field either.
* the **lock** is checked against the compiled actual target, so neither an alias nor a route parameter can move a locked field. The operator's own reference is the operator's **effective** value — the operator's declared value completed by the operator's default route — and it is normalized through the **same** compilation that produced the candidate, so locking the operator's unchanged alias does not refuse the operator's own target. A lock therefore pins the value the operator's effective target carries: a field the operator named no value for but whose **default route** supplies one is pinned to that inherited value, and it pins nothing only when neither the declaration nor the route names one. The reference keeps **every** leaf it compiles to, so an operator alias that resolves to several targets locks each of them, and the comparison is made leaf by leaf on the **locked fields only** — an unlocked field that a route parameter moves is not a lock violation.
* the **capability** check refuses a reasoning effort the actual target cannot carry. The runtime would otherwise strip that effort at the pre-open candidate admission or at the post-request transform, which would silently violate the phase's declared effort and the operator's lock.

Every candidate the compiled route can address is checked, not only the first one: the runtime may fail over to a later candidate or run a parallel leg, so a candidate the policy does not cover would otherwise serve the call unchecked.

A settings **write** is checked the same way, before it stores anything: the proposed target is compiled and checked against the final compiled actual target, and a write that the deployment does not carry, that the provider does not support, or that the strict policy cannot prove is **refused explicitly** and leaves the stored settings unchanged. An unproven fact is refused only under `models.strict`; the default policy serves it, counts it and logs it.

### The pre-open invariant

The compiled-target check runs **before the dispatch**. The runtime may still change the attempt after that check — a request-part hook may rewrite it, and the runtime's own negotiated downgrade may strip the reasoning effort — so the same target and the same reasoning effort are **re-asserted at the provider-open chokepoint**, immediately before the provider is opened and after every mutating transform:

* the attempt must still carry the **frozen** reasoning effort, the one the dispatched call resolved to for that candidate;
* the **deployment facts** about the target are applied there: a target the model registry proves **absent** for its backend is refused, and a target whose installation the registry does not prove is served, counted and logged;
* the **provider's own answer** for the effort value is applied there: a value the provider states it does not carry is refused, and a value whose per-value support no provider publishes is served, counted and logged.

A divergence at that point is an **explicit refusal**: the attempt is closed as never started and the phase fails on its ordinary failure path. Ordinary traffic is untouched: the check runs only for a phase the feature owns.

A refusal fails that attempt only: the core's pre-open guard is per attempt and does not coordinate the legs of a parallel round. That is why a declared `models` policy requires each phase route to name a **single target**: a refusal then ends the phase, with no other candidate to serve it. A cancelled attempt is **not** a refusal: a cancellation stays a cancellation.

The capability facts are coarse. The backend capability set carries a **boolean** reasoning capability and the model catalog a **tri-state** per-model fact; neither states which effort *values* a provider supports. The enforcement therefore refuses an effort the target cannot carry at all and never claims that a provider supports an effort value: the operator's inventory remains the declared policy about the values, and the provider-level support of a specific value stays **unknown** until a provider publishes it.

### The strict policy switch

`models.strict` selects the strict policy for the two facts the deployment does not prove: the **installation** of the actual target and the provider's **per-value support** of a reasoning effort.

* the default policy is the interoperable one: an unproven fact is served, counted (`model_policy_unverified_installation`, `model_policy_unverified_effort`) and logged, so a legacy route keeps working;
* the strict policy **refuses** instead, which is the operator's explicit choice.

The switch is an operator setting, not a fourth owner-selectable field: it adds no target, no model and no effort to the owner-facing surface, and it never widens what an owner may choose.

The registry publication is a snapshot, so the installation fact is only as fresh as the last refresh: a model installed after the last refresh is refused until the next refresh. That window is the operator's registry refresh cadence, and it is logged and counted, never hidden.

## Background funding

Both background phases call a provider, so their funding is an **explicit operator choice**. The choice is bound by the operator configuration, never by a request: the auxiliary role is a known label and carries no funding authority, and the mode, the sponsor key and the account never travel on a client request, a canonical call or a client extension.

* **`customer_funded`** — the leg is charged to a customer. The host must present a **complete, validated monetary binding**: the cheap credit screen, the quote and admission, the terminal handoff and the lifecycle behind them. On a host that does not, the background work is refused before the provider is opened. The account is the one the host resolves from the authenticated, captured owner (tenant and principal) and freezes when the operation is admitted; the feature never names an account.
* **A durable terminal handoff is part of that completeness.** The host's terminal usage sink must hold the obligation durably **before** it acknowledges the terminal append, so a completed obligation survives a process restart and a central delivery outage. A bare central store cannot: the customer charge would die with the process while the provider cost stayed booked. The host therefore states the durable handoff only where its terminal transport really provides one, and the live readiness is consulted at dispatch: a handoff that is full or closed refuses new payable work instead of accepting an obligation it cannot hold. A central delivery outage on its own does not make the local durable queue unusable, so it does not stop new work.
* **`operator_sponsored`** — the operator sponsors the leg. The provider is opened, the usage is metered, and **no** customer settlement, customer account row or customer balance change follows. The provider cost is not dropped: it rides the same usage record.
* **No mode** — depends on the build. Without any monetary binding (the open-source build) no customer money can move: the phases run usage-only, exactly as before, paid by the operator's own provider credentials. Where a monetary binding is bound (AIProxer Enterprise), an undeclared mode is **refused** before any provider is opened, because the ordinary billing path would otherwise charge a customer silently; the refusal is counted (`aip_background_sponsorship_refused_total{reason}`) and logged with a bounded reason.

`customer_funded` needs a monetary binding, which only AIProxer Enterprise provides: on the open-source build it is refused at dispatch with the reason `monetary_host_incomplete`.

A `sponsor_key` names a **sponsor**, never an account: it is never financial authority on its own, and the account always comes from the host's identity seam. A selected key outside the declared `sponsor_keys` is refused and counted. The reason label is a closed set (`mode_undeclared`, `sponsor_key_undeclared`, `monetary_host_incomplete`, `handoff_not_ready`); it never names a principal, an account, a sponsor or any request content.

## Client setup

Give the agent the memory tools over MCP. The endpoint is the authenticated MCP entry `https://<proxy>/v1/aip/memory/mcp`; pass the user's own API key (the same key their inference turns use, so both share one principal).

Codex (`~/.codex/config.toml`):

```toml
[mcp_servers.aiproxer_memory]
url = "https://<proxy>/v1/aip/memory/mcp"
http_headers = { "Authorization" = "Bearer <your-api-key>" }
```

Claude Code:

```sh
claude mcp add --transport http aiproxer-memory https://<proxy>/v1/aip/memory/mcp \
  --header "Authorization: Bearer <your-api-key>"
```

OpenCode (`opencode.json`):

```json
{
  "$schema": "https://opencode.ai/config.json",
  "mcp": {
    "servers": {
      "aiproxer_memory": {
        "type": "remote",
        "url": "https://<proxy>/v1/aip/memory/mcp",
        "oauth": false,
        "headers": { "Authorization": "Bearer {env:PROXY_MEMORY_API_KEY}" }
      }
    }
  }
}
```

The exact client file syntax belongs to each client; check its MCP documentation for your version. The closed tool catalog is `memory_list`, `memory_read`, `memory_search` and `memory_add_note`; every tool takes the opaque `scope` identifier the proxy shows in the list response.

**Disable native memory in the agents.** Run exactly one memory authority. Turn off the agent's own or built-in memory feature (for example, Codex's native memory setting) and remove other memory MCP servers or plugins, so the proxy memory tools are the only store the agent writes to and reads from.

**`X-AIP-Workspace` is optional.** When a client sends it (an absolute path), it becomes the primary project hint and dominates the derivation; without it the proxy derives the project from the harness instructions or the Codex environment context. Codex and Claude Code sessions need no extra header.

## REST controls

All routes require the same authenticated, eligible principal; responses only ever cover the caller's own banks, and another owner's scope is indistinguishable from one that does not exist.

| Route | Result |
|---|---|
| `GET /v1/aip/memory` | `{"banks":[…]}` for the caller only: `scope`, `project_label`, `version`, `published_at`, `pending_sessions`, `extractions`, `pending_notes` |
| `GET /v1/aip/memory/{scope}` | One bank's status plus its published `memory_summary` |
| `DELETE /v1/aip/memory/{scope}` | `204`; erases every transcript, extraction, note, published document and pending job of the bank and fences the scope so late writes cannot resurrect it |
| `GET /v1/aip/memory/settings` | The caller's own per-phase settings: the stored `overrides` and, per phase, the administrator's `defaults`, the `effective` values and the `locks` |
| `PUT /v1/aip/memory/settings` | `200` with the same document; replaces the caller's own overrides. A locked field, a value outside the allowed inventory, or a target the compiled actual target does not cover is refused with `409` and stores nothing |
| `DELETE /v1/aip/memory/settings` | `204`; revokes the caller's own overrides and leaves every bank untouched |
| `POST /v1/aip/memory/mcp` | MCP JSON-RPC 2.0 (`initialize`, `ping`, `tools/list`, `tools/call`); notifications answer `202` with an empty body |

The `scope` value is the opaque project key (64 hex characters) from the list response; it cannot be inverted into a path, and it is checked together with the caller's principal.

The settings routes exist where the operator declared a per-phase model policy. They carry no owner identity: the owner is the authenticated principal, so neither the body nor the query string can change whose settings are read or written. The settings are global for that owner — they hold no project, and forgetting a bank does not revoke them. A write is checked against the final compiled actual target, so a target the deployment cannot serve is refused before it is stored.

## Limits

* **Forget does not withdraw memory already injected into running sessions.** `DELETE` erases the bank for future turns — a later first turn observes the erased bank and applies its own no-match decision — but an agent session that already received the overlay keeps it in its own context until that session ends. Start a new session to drop it.
* **The first-turn decision is persisted in the background.** The request path reads the admission snapshot once, applies the frozen decision immediately, and queues the durable write. The write happens after the response, on the background writer. The decision is therefore **applied** before it is **durable**: the counters report the applied decision, not a committed one.
* **The durable decision can be lost.** The writer queue is bounded (`256` jobs) and the write has its own deadline (`5s`). A full queue, a stopped writer, a failed write, a process crash, or another node can leave the decision undurable. The counters show it (`inject_dropped_queue`, `inject_decision_dropped`, `inject_error`), and the durable binding of the A-leg is then absent. A decision that was refused before it was queued, or whose durable write failed, authorizes no overlay: the turn that froze it continues without recall, and a later turn of that session finds no decision.
* **A missing decision means no recall, not a rebuilt one.** When the frozen decision is not available — a lost durable decision, a cache eviction, or another node — the turn continues **without** the overlay. The current bank text is never re-read to reconstruct a missing decision, and no current bank summary is injected in its place. The counters show it (`inject_decision_unavailable`).
* **A capture needs its decision.** A captured turn is only learnable input when the A-leg's durable first-turn binding exists. A capture whose decision was never persisted is dropped, not repaired (`capture_unbound`).
* **Memory lags the session.** Capture happens when a turn is released, extraction only after `min_idle`, and consolidation on the worker cadence: the injected summary reflects the last consolidation, never the turn in flight.
* **Redaction is best-effort.** Codex secret patterns are applied to everything stored and injected, but no pattern set is complete. Never route credentials through an agent that has memory enabled.
* **Memory is stored in plaintext in the proxy database.** The feature does not encrypt at rest; the redaction pass covers secret-shaped values, not storage. Protect the database with the same controls as your continuity and metering data, and use `DELETE` to purge a user's bank.

## Observability

Bounded counter `aip_proxy_memory_events_total{event=…}` (no path, principal, A-leg or memory text ever becomes a label):

| Event | Meaning |
|---|---|
| `inject_matched` | A frozen matched decision was applied to the request (the durable write may still be pending) |
| `inject_no_summary` | A frozen no-summary decision was applied to the request |
| `inject_ineligible` | Request failed eligibility |
| `inject_no_session` | Request has no established session |
| `inject_no_project` | No project could be derived |
| `inject_decision_unavailable` | No validated first-turn decision was available; the turn continued without recall |
| `inject_dropped_queue` | The frozen decision was dropped: the bounded writer queue refused it, and the turn continued without recall |
| `inject_decision_dropped` | The queued decision write was rejected as stale or refused by the store |
| `inject_error` | An admission read or a decision persistence failure served the turn without memory |
| `capture_recorded` | One captured session upsert |
| `capture_no_session` | Capture dropped: no established session |
| `capture_dropped_queue` | Capture dropped: writer queue full |
| `capture_dropped_pending` | Pending turn evicted from the bounded map |
| `capture_truncated` | Transcript cut to `max_transcript_bytes` |
| `capture_owner_mismatch` | Upsert refused for another owner's A-leg |
| `capture_forgotten` | Upsert refused by a forget fence |
| `capture_unbound` | Upsert refused: the A-leg has no durable first-turn binding for the owner |
| `capture_stale` | Upsert refused: stored state already covers the capture |
| `capture_error` | Failed capture write |
| `phase1_succeeded` / `phase1_failed` | Extraction outcome |
| `phase1_lease_lost` | Completion lost its lease (another instance owns the claim) |
| `phase1_purged` | Rows removed by the worker purge step |
| `phase2_published` / `phase2_published_empty` | Consolidation outcome |
| `phase2_truncated` | Consolidation input budget dropped items |
| `phase2_failed` | Failed consolidation attempt |
| `phase2_lease_lost` | Publish lost its lease |
| `tool_call` | One successful MCP tool call |
| `control_forget` | One successful forget control call |
| `settings_refused` | One refused settings write: a locked field or a value outside the phase's allowed inventory |
| `worker_panic` | One recovered worker panic |
| `store_unavailable` | Memory-store failure on a read path (with `inject_error` or an HTTP 500). A replayed frozen decision never counts here: the current read succeeded. |

Logs are content-free: bounded `event`/`reason`/`error_kind` fields only, never paths, principal IDs, A-leg IDs or memory text.
