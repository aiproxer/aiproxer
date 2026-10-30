# Runtime configuration reload

Authoritative operator contract for explicit, transactional config reload in `cmd/aipstd` and `pkg/aipruntime`. This is the **one reload contract** for the process: public/SDK DTOs live in **`pkg/aipsdk/configreload`**; the process **`Host`** (from `runtimebundle.BuildHost`, private fields) owns attempt coordination, generation publication, and shutdown via **`Host.Close`**. Process-owned services live under the single **`ProcessServices`** owner. **Manager-owned retirement** drains superseded generations after a successful publish. Editing the config file alone has **no** runtime effect. There is no file watcher, mtime poller, debounce loop, periodic rescan, or automatic retry of a failed attempt.

Code-owned constants and paths: `pkg/aipsdk/configreload` (public vocabulary), `internal/core/config.DefaultConfigMaxBytes` (2 MiB), `internal/infra/runtimebundle.DefaultMaxRetainedGenerations` (8), `internal/stdhttp/admin/configreload` (`/admin/config/reload`, `/admin/config/status`), env `AIP_RELOAD_MANAGEMENT_ADDRESS` / `AIP_RELOAD_MANAGEMENT_TOKEN`.

## Triggers (explicit only)

| Trigger | When |
| --- | --- |
| `SIGHUP` | Unix-like platforms that provide it. Does **not** terminate the process. |
| Management API | `POST /admin/config/reload` on the **separate** management listener (opt-in; see below). |
| `pkg/aipruntime` | `Runtime.Reload` with `TriggerSIGHUP` or `TriggerAPI` (same coordinator). |

Unsupported: watchers, implicit reload on save, body-supplied YAML/path/URL, plugin install instructions, automatic retry after failure.

Every reload re-reads the **absolute source path fixed at process startup** (`--config`). The management POST body must be empty (or empty JSON `{}`); any `path` / `config` / `yaml` / `url` / `source` / `command` / `plugin` / `install` key is rejected.

## Source integrity and size limits

- Bound: **2 MiB** (`DefaultConfigMaxBytes`). Larger files fail as `source_oversize` without publication.
- Decode: **strict** one-document YAML with known core fields (`StrictDecode`). Multi-doc, trailing content, unknown core fields, and malformed YAML fail without publication.
- Changed content requires **atomic replacement** of the path target (new file identity). In-place rewrite of the same inode/handle with a different digest is rejected as `source_non_atomic_update`.
- Same identity + same private digest → successful **no-op** (no new generation).
- On Linux, a valid startup load remains available when positive ext4 lease evidence is absent, but runtime comparison then fails closed with the existing source-integrity outcome. Windows retains its `win-fileid` reload behavior; macOS and other current adapters keep their existing behavior. See [Configuration source integrity](configuration-source-integrity.md) for the support boundary, lease lifetime, and certification setup.

### Atomic rename workflow

Use a **working copy** of the fixed startup path (do not mutate committed examples in place during experiments):

```bash
WORK=$(mktemp -d)
CONFIG="$WORK/config.yaml"
cp ./config/examples/dogfood-local-stub.yaml "$CONFIG"
cp "$CONFIG" "$CONFIG.next"
# edit $CONFIG.next …
# optional: sync "$CONFIG.next"
mv -f "$CONFIG.next" "$CONFIG"   # atomic replace on the same filesystem
```

Do **not** open-and-truncate the live path. After a successful atomic replace, send one explicit trigger (`SIGHUP` or `POST …/reload`).
## Outcomes

Each trigger produces one terminal category (management JSON `category`; HTTP mapping in parentheses):

| Category | HTTP | Meaning |
| --- | --- | --- |
| `published` | 200 | New generation published for **new** admissions. |
| `no-op` | 200 | Effective config unchanged; generation ID unchanged. |
| `restart-required` | 409 | Startup-only field(s) changed; active generation unchanged. |
| `retention-blocked` | 409 | Retained-generation budget exhausted; active unchanged; pinned streams not killed. |
| `busy` | 409 | Another attempt in flight / coalesced. |
| `invalid` | 422 | Decode/validation/policy failure. |
| `source-integrity-failed` | 422 | Missing, oversize, unstable, non-atomic, unsupported type, etc. |
| `canceled` / `preparation-failed` / `internal-failed` | 503 | Host cancel, compile/lifecycle failure, or internal error. |

**Last-good rollback:** any pre-publication failure leaves the active generation and in-flight work unchanged. Correct the candidate on disk (atomic replace), then send **another** explicit trigger. Failures are never retried automatically.

**Mixed changes:** reloadable + restart-required in one candidate → whole transaction rejected as `restart-required` (no partial apply).

## Restart-required vs reloadable (summary)

Authoritative classifiers live in `internal/core/configreload.Classify`. High-level:

**Startup-only (restart-required examples):** `access`, `server.address` / server timeouts / decode admission caps, `logging.*`, `diagnostics`, `observability`, `database`, `continuity` store topology, `auth.remote` / auth-handler class, affinity **store**, secure-session store/DSN/enable topology, metering **journal** / accounting **ledger|authority|concurrency** store topology, model-catalog/inventory **external refresh** toggles, management listener/auth (process-owned; not YAML-swapped mid-flight).

**Reloadable examples:** `plugins.frontends|backends|features` rows, `routing.default_route` / `max_attempts` / health policy (store unchanged), `model_aliases`, `identity`, generation-owned `http_client.*`, many request-plane limits (`server.max_request_body_bytes`, `max_pending_wire_events`), local API keys within a fixed auth mode, request-plane metering/control-plane **policy** knobs that do not change store topology.

`server.large_payload_fast_path.max_inflight_spool_bytes` sizes the process-owned spool ledger and requires restart when its effective value changes. Spelling out the existing default does not require restart. A candidate that also changes reloadable fields is rejected as a whole; the running generation and spool budget remain unchanged.

`restart-required` responses expose a sorted, bounded list of safe field paths (`restart_required_fields`, max 32) plus `restart_required_field_count` — never old/new secret values.

## Generations, models, retention

- Startup publishes **generation 1**. Only a successful material publish allocates the next monotonic process-local configuration generation ID.
- In-flight requests and streams keep the generation they acquired until completion; reload does not migrate or force-close them.
- `model_generation` on status is a subordinate model/policy snapshot label, distinct from the config generation ID.
- Default retained-generation budget: **8** (`DefaultMaxRetainedGenerations`). Exhaustion → `retention-blocked` / `retention_pressure`; old streams continue until they drain.

## Management API (opt-in)

Disabled unless `AIP_RELOAD_MANAGEMENT_ADDRESS` is set (recommended loopback: `127.0.0.1:9090`). Empty address preserves ordinary data-plane serve with no management port contention.

| Env | Role |
| --- | --- |
| `AIP_RELOAD_MANAGEMENT_ADDRESS` | Startup-fixed bind. Required to enable management. |
| `AIP_RELOAD_MANAGEMENT_TOKEN` | Dedicated bearer (≥16 Unicode code points). Required for `multi_user` or any non-loopback bind. |

Auth:

- Explicit single-user **loopback** may use documented **local trust** when the token env is unset.
- Multi-user or non-loopback requires bearer auth. Weak/short tokens fail startup of management (or reject enablement).
- Missing required bearer → management stays **disabled** with a warning; data-plane serve continues.
- Cookie / data-plane local API keys do **not** authorize reload.
- Default browser-origin posture: reject non-empty `Origin` unless allowlisted at startup; reject cross-site Fetch Metadata; no permissive CORS; `OPTIONS` preflight is never an authorized reload.

Paths (management listener only, not the data-plane mux):

- `POST /admin/config/reload`
- `GET /admin/config/status`

Status includes `active_generation`, last success/failure, `retained_generations`, `retention_pressure`, `model_generation`, `busy`, and (on the management surface) `fixed_source_path`. The public `pkg/aipruntime` status DTO **omits** filesystem paths and secrets.

Client disconnect after an accepted reload does **not** cancel the host-owned attempt (default timeout 1 minute).

## `check-config` parity

`aipstd check-config` calls `runtimebundle.ValidateDistribution`: the same generation compiler in dry-run mode with **true unpublished validation** — candidate resources are **always rolled back**; it never publishes or retains a generation (no fake check-config publication). Use it to validate a candidate **before** atomic replace + trigger:

```bash
go run ./cmd/aipstd check-config --config ./config/examples/dogfood-local-stub.yaml
```

## Local dogfood examples

Validate the committed no-key stub any time; for live reload drills, copy it to a working path and pass that path as `--config` so the process fixed source is the working file.

### 1. Validate committed dogfood (check-config)

```bash
go run ./cmd/aipstd check-config --config ./config/examples/dogfood-local-stub.yaml
```

### 2. Working copy + serve (management opt-in, single-user loopback local trust)

```bash
WORK=$(mktemp -d)
CONFIG="$WORK/config.yaml"
cp ./config/examples/dogfood-local-stub.yaml "$CONFIG"
go run ./cmd/aipstd check-config --config "$CONFIG"

export AIP_RELOAD_MANAGEMENT_ADDRESS=127.0.0.1:9090
# optional strong token even on loopback:
# export AIP_RELOAD_MANAGEMENT_TOKEN='sixteen-chars-min'
go run ./cmd/aipstd serve --config "$CONFIG"
```

### 3. Atomic replace + SIGHUP (Unix)

With `serve` using `$CONFIG` from above:

```bash
cp "$CONFIG" "$CONFIG.next"
# make a reloadable edit in $CONFIG.next (e.g. routing.max_attempts)
go run ./cmd/aipstd check-config --config "$CONFIG.next"
mv -f "$CONFIG.next" "$CONFIG"
kill -HUP "$AIPSTD_PID"   # PID of the serve process
```

### 4. Management API reload + status

```bash
curl -sS -X POST http://127.0.0.1:9090/admin/config/reload
curl -sS http://127.0.0.1:9090/admin/config/status
# with bearer:
curl -sS -H "Authorization: Bearer ${AIP_RELOAD_MANAGEMENT_TOKEN}" \
  -X POST http://127.0.0.1:9090/admin/config/reload
```

### 5. Invalid candidate → correct → trigger again

1. Atomic-rename a deliberately invalid or restart-required candidate onto `$CONFIG`.
2. Trigger once (`SIGHUP` or `POST`); expect `invalid`, `restart-required`, or `source-integrity-failed`. Active generation stays last-good.
3. Atomic-rename a corrected candidate, then trigger **again**. There is no automatic retry.

## Shutdown and secret safety

**`Host.Close` is the sole process shutdown coordinator.** Shutdown rejects new reload triggers, cancels in-flight candidate work, drains the data-plane HTTP server, awaits coordinator idle (candidate rollback), drains retained generations, closes management, then closes process services. Tracing shutdown stays on the outer deadline.

`Runtime.Close` (public facade) delegates to `Host.Close`: it serializes close attempts, honors the caller deadline for idle/drain/tracing, is idempotent after success, and remains **retryable** after deadline or teardown failure (not `sync.Once`). Facade pointers stay usable so concurrent Reload/Status/Execute fail through manager/coordinator shutdown state. CLI teardown follows the same host-owned order.

Responses, logs, and metrics must not include raw YAML, credentials, DSNs, private digests, or opaque secret values. Management bodies cannot supply configuration content.

## Related

- Spec: `.kiro/specs/archive/versioned-runtime-reloadable-proxy-configuration/`
- ADR: [adr/0008-versioned-runtime-config-reload.md](adr/0008-versioned-runtime-config-reload.md)
- Dogfood workflow: [dogfood-local.md](dogfood-local.md)
- Architecture: [architecture.md](architecture.md), [runtime-flow.md](runtime-flow.md)
- Release gates: [release-gates.md](release-gates.md)
