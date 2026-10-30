# Backend plugin authoring guide

This guide covers closed executable backend plugins for AIProxer. The reference module is [`connectors/localstub`](../../connectors/localstub).

## Closed manifest

Ship a `*.backendplugin.json` next to the install root (discovery does not recurse). Required fields: `schema`, `plugin_id`, `version`, `build_id`, `executable` (relative, no `..`), `sha256`, protocol range, `platforms`, and `exports[]` with `kind`, `credential_mode`, `access_scope`, `process_sharing`, and `execution_class` (`"inference"` or `"agent_runtime"`).

Template: `connectors/localstub/manifest/template.backendplugin.json`. Packaging fills digest/`build_id` from [`release.yaml`](../../connectors/localstub/release.yaml).

## Execution class and routing safety

Each export must declare its `execution_class` honestly based on runtime execution semantics:
- `"inference"`: Direct stateless or turn-scoped inference backends that are safe to compose in failover, weighted, parallel/race, and thinker hybrid routing chains.
- `"agent_runtime"`: Autonomous agent runtimes, orchestrated sub-processes, or interactive agent protocols (e.g. ACP connectors, Cursor SDK agents, OpenAI Codex App-Server). Under the default `safe` execution composition policy, agent runtimes cannot be composed in failover, parallel, or weighted chains with other backends, preventing uncontrolled duplicate side-effects and race conditions. Direct routing (e.g. `acp:claude-3-7-sonnet`) is always supported.

Note that process isolation, local execution, or tool capabilities do not classify an export as `agent_runtime` — only whole-agent autonomous loop orchestration semantics do. Legacy manifests omitting `execution_class` normalize to effective `unknown`, which allows direct routing but disallows multi-backend composition under `safe` policy.

## Access scope, credential posture, and multi-user eligibility

Declare `access_scope` and `credential_mode` honestly; **your metadata is necessary but not sufficient** to authorize shared use.

```text
execution_class: agent_runtime  => access_scope: local_only   # enforced by first-party architecture guards
credential_mode: oauth_user     => access_scope: local_only   # enforced by manifest validation and the host gate
```

- `credential_mode: oauth_user` combined with any `access_scope` other than `local_only` is **invalid at manifest-validation time**. Declare both fields explicitly; the host never infers one from the other.
- `access_scope: local_only` is required for anything that spawns a local process, reads personal OAuth material from the local user context, or otherwise depends on one user's local trust boundary.
- In `access.mode: multi_user`, an enabled factory must additionally be present in the **host-owned** multi-user approval registry (`internal/standardplugins/multi_user_backend_policy.go`). That registry is outside `connectors/*`; absence is denial. A new connector therefore runs in single-user deployments and **fails closed** in multi-user until an explicit, separately reviewed registry change is made — no configuration flag can grant it. See [`operator.md`](operator.md#multi-user-eligibility-connector-posture-and-a-host-owned-approval).

Keep `Describe()`'s `AccessScope`, `CredentialMode`, and `ProcessSharing` identical to the packaged manifest export; a mismatch is a diagnostic failure, not a way to change eligibility. Do **not** read the deployment access mode or implement a local single-user check: architecture guards reject host-internal imports and locally declared multi-user authority inside the connector subtree.

## SDK server helper

Implement `pkg/aipsdk/backendplugin.Service` and serve via `backendplugin.NewGRPCServer` over the host-provided secure channel (`AIP_PLUGIN_CHANNEL_PIPE` on Windows). Do not import root `internal/…`.

## Capabilities and process sharing

Advertise honest static capabilities in `Describe`. Use `process_sharing: per_instance` unless isolation and concurrency are declared for shared artifacts. Route prefixes should identify the factory kind.

## Opaque config ownership

Configuration YAML is opaque to the host and delivered only after peer authentication. Parse and validate inside the connector. Secrets arrive only in `ConfigureRequest.Secrets` after peer auth—never via process environment bootstrap.

## Exact trust and secure IPC

Hosts verify digest, bind a private staged executable, launch that exact image, and authenticate the peer before negotiate/configure. Darwin remains fail-closed in current production profiles.

## Host-bound economic evidence (opt-in)

A connector may supply host-only provider evidence as a V2 accounting-evidence frame. Whether the host *binds* that claim into its own canonical identity is an explicit negotiation, not a default:

- Advertise `ProtocolMinor: 10` and the feature `host_bound_economic_evidence_v2` to opt in. The negotiated outcome is `backendplugin.HostBoundEconomicEvidenceV2Negotiated(neg)`.
- **Without the opt-in** the host performs **no identity rewrite** and does **not retain** the claim. It records an explicit capture loss (`peer_not_host_bound`) and the client request continues. Never assume the host will keep or relabel your observation when you did not opt in.

When you opt in, your frame is a *provider claim*. The host rewrites the host-owned dimensions and preserves yours:

- **The host owns and overwrites**: the store, the tenant, the credited account, the customer scope, the attempt lineage (A-leg, B-leg, request id, call id, billing call id, attempt id, attempt sequence), the subject kind, the observation id, the receipt time, and the `Supersedes` references.
- **You own and keep**: `SourceEventKey`, `StreamID`, `Revision`, a non-zero `Sequence`, the provider account key, the provider request id, the provider charge id, the origin, acquisition, authority, perspective, boundary, lifecycle and semantics members, the measures, the charges, the safe evidence fields, and `ObservedAt`.

Two rules decide whether your claim is admitted:

- **Attempt identity.** Leave the host-owned lineage fields empty, or repeat exactly the values the host gave you. The B-leg you were given is the *transport pseudonym*; the host accepts it and replaces it with the sealed B-leg. Any other value is a conflicting attempt identity: the whole claim is rejected with an explicit capture loss and the request continues.
- **References.** A `Supersedes` reference must name your own local observation id and revision, and its payload hash must be the hash of the claim *as you sent it*. The host verifies that hash against the source fingerprint it computed before it rewrote anything, then substitutes its own reference. A hash that does not match, a pair the SDK's scope rule rejects, or a predecessor that never arrives costs the **whole** claim: nothing is emitted, no synthetic reference is created, and no invalid correction is ever serialized. A predecessor that arrives later releases the held claim immediately, within the same attempt.

The host's durable identity is its own; yours is not. Reuse of one local observation id for two different source events is a replay/identity conflict: the first binding is preserved and the later claim is rejected with an explicit capture loss. The host keeps at most 1024 logical items per attempt and never evicts: beyond that, a genuinely new claim is rejected with an explicit capture loss and the request continues.

The operator can select a strict policy (`plugins.backend_discovery.economic_evidence_policy: strict`). Under the strict policy a claim that the host cannot admit **before** the attempt's output was committed refuses the attempt with a typed, non-retryable error; after the output was committed the committed stream is preserved, the capture loss is recorded, and nothing is rewritten or re-routed. The default `degrade` policy never fails a client request for an accounting reason.

This contract is capture-only: a bound observation is **not** proof of money eligibility, provider-cost posting or production billing safety.

## Graded reasoning-effort values (minor 11)

Advertise `ProtocolMinor: 11` and the optional feature `reasoning_effort_values_v1` to publish per-model graded effort facts on the model-specific `ResolveProfile` response. Set `ResolvedProfile.ReasoningEffortValues = &backendplugin.ReasoningEffortValuesV1{ModelID: <exact request model>, Supported: [...], Exhaustive: <bool>}`. Nil means no statement (Unknown).

Rules: echo the exact requested model ID; validate shapes (model <=256 bytes, <=32 values, each <=64 bytes, no blanks/controls/duplicates, valid UTF-8); never invent a global vocabulary; match exactly and case-sensitively; set `exhaustive=true` only when the list is the complete accepted graded input set (otherwise false); publish nothing for agnostic queries, undiscovered or stale models, failed discovery, binary-only (ungraded) support, or a contradictory non-positive binary effort flag. Unnegotiated peers have their facts suppressed/stripped to Unknown.

## Conformance

Run `pkg/aipsdk/backendplugin/conformance.Run` in the connector module (`GOWORK=off go test ./...`).

## Module versioning and release

- Connector `go.mod` may `replace` the monorepo root for local development only (`release.yaml` `replace_policy`).
- Published tags drop `replace` and depend on a tagged `github.com/aiproxer/aiproxer` version.
- Root never requires or replaces connector modules. Root CI uses `GOWORK=off`.

## Installation, compatibility, rollback

Operator install/trust/diagnostics/upgrade/rollback: [`operator.md`](operator.md) (platform dirs, closed manifests, inspect/doctor, troubleshooting).

- Minimal package: root binary only (`make package-minimal`).
- Full package: structurally discovers connectors/*/release.yaml profiles containing full (no maintained connector name list); installs into OS plugin roots (`/opt/AIProxer/plugins`, `/Library/Application Support/AIProxer/plugins`, `%ProgramFiles%\AIProxer\plugins`) with installer ownership and proxy read/execute metadata (`ACCESS.txt`). Runtime ACL application is layout-tested; live ProgramFiles mutation is not performed in CI.
- Removing one plugin directory leaves unrelated backends. Installed but unconfigured plugins stay inactive (no process launch).

## Private companions

Node/Python bridges stay under the connector package `private/` directory. Hosts must not import or launch them. See `docs/backend-plugins/examples/private-bridge/`.
