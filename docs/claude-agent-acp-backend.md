# Claude Agent ACP backend (local single-user)

`claudeagentacp` is an optional executable connector, not the native `anthropic`
API backend. `claudeagentacp` is the factory kind; routes select a configured
instance ID, such as `claude-acp:anthropic/<canonical-model>`. Model identity is
runtime-discovered in the `anthropic/` namespace, independently of the factory.
Exact advertised model/effort selection is required; moving aliases do not satisfy
an explicit versioned request. No static model catalog or manual `models` config
is supported (manual configuration is strictly rejected).
The host denies multi-user access from its manifest. Examples in
[`config/examples/claude-agent-acp.yaml`](../config/examples/claude-agent-acp.yaml)
are disabled and retain the rejecting default `admin_policy: refuse`.
Enable only one source row: the gateway example uses instance `claude-acp`; the
alternate API/token rows are `claude-agent-api` and `claude-agent-token`, and
host login reuses the `claude-acp` shape. After installation,consent and enabling that instance, inspect `/v1/models` for its canonical IDs.
The connector has no canonical default model, so the default route comes from the
model the instance publishes in its runtime inventory. `routing.default_route` is
optional for that reason: omit it and the default route is the model the enabled
instance designated as its default — the connector reports its current model as
that default, so the proxy never guesses from the enumeration order. A backend
that publishes several models must designate one; the proxy does not pick a
model for the operator, and it does not verify that a model can serve a given
operation, so a model the backend cannot serve fails at the backend and is
reported there. `aipstd routes` publishes the result as
`effective_default_route` together with `effective_default_route_model` and
`effective_default_route_source` (`discovered_inventory`). Name the route to pin
one model and its effort, as the example does: a configured route is
authoritative and always wins over the discovered model. When the inventory
cannot be discovered, or the instance designates no default model, there is no
default route to fall back to, `aipstd check-config` warns about it, and
`aipstd routes` reports that no default route exists. A discovered model
identifier is external data: the proxy uses it only as a single model of a single
route, so a catalog can never change the shape of the default route or point it
at another backend.
The connector inventory also retains the exact native mapping (not exposed by
the OpenAI-compatible list). For example, **only if advertised**, a selector could be
`claude-acp:anthropic/claude-sonnet-5.5?reasoning_effort=high`, with discovered native
ID `claude-sonnet-5-5`. This is illustrative, not an availability or static catalog
claim. Effort must also be advertised for the selected model. Failed discovery
provides no fabricated fallback; runtime changes can withdraw a previously listed ID.

## Install explicitly

Only Linux/amd64 glibc has a pinned supported runtime profile. Windows,
macOS, WSL and other architectures are not certified. Shared Go native CI checks
are regressions of portable support code, not Claude runtime support claims.
Node **>=22** is required, matching the adapter's minimum supported version.
Newer patch, minor and major releases are not blocked by a release allowlist.
The connector reports the observed version and executable digest in identity
evidence; neither is pinned to the particular build used for certification.

Build the connector artifact from a checkout; keep outputs outside the repository:

```sh
go run ./tools/backendplugin/package_plugins -root . -profile full \
  -select claudeagentacp -dest /absolute/scratch/claude-plugins
```

Install the complete `claudeagentacp/` tree into a trusted discovery directory.
`bin/aip-backend-claudeagentacp` must have adjacent
`bin/claudeagentacp-adapter-extension/{bootstrap.mjs,identity.mjs,models.mjs}`. Configure
`plugins.backend_discovery.paths` for that directory; do not move just the binary.
See [plugin operator examples](../config/examples/plugin-operator-full-discovery.yaml).

The operator, **never AIProxer**, installs upstream dependencies. Use the checked-in
certification recipe, not a fresh caret-range resolution, on Linux/amd64 glibc:

```sh
mkdir -p /opt/claude-agent-acp/runtime
cp connectors/claudeagentacp/install/package.json \
   connectors/claudeagentacp/install/package-lock.json \
   /opt/claude-agent-acp/runtime/
npm ci --prefix /opt/claude-agent-acp/runtime
```

Both files belong to the certified identity: `package.json` pins the adapter and
carries the security overrides, `package-lock.json` closes every transitive
version and registry integrity value. The identity hashes installed package
bytes, so it does not depend on the npm major that runs the install.

The recipe keeps the certified adapter, SDK and bundled-CLI bytes and overrides
three transitive libraries to patched releases: `zod` 4.6.6, `body-parser` 2.4.0
and `fast-uri` 3.1.9. Those override the adapter's own exact pins for libraries
whose shipped versions carry published advisories; the executed adapter
entrypoint, Agent SDK module and bundled CLI are byte-identical to the certified
identity below.

Put Node >=22 on the connector process PATH; set `executable` to
`/opt/claude-agent-acp/runtime/node_modules/.bin/claude-agent-acp`. The lock includes
all transitive versions and registry integrity values (including optional native
packages); it is metadata, not redistribution. Keep installation directories and
files readable, non-world-writable and writable only by trusted owners/groups.
Unknown versions, bytes, changed dependencies, links inside the tree, missing
bundled CLI or sidecar, external CLI overrides and unsafe Node loaders are refused.
Preserve the lock; never run an update against an admitted installation. Reinstall
and reconfigure after drift; upgrades require a newly reviewed certification.

## Exact pinned identity

Linux/amd64 runtime and sidecar identities, independent of permission policy:

| Component | Version | SHA-256 |
| --- | --- | --- |
| Adapter entrypoint | 0.89.1 | `4a1f0fff5cdd26b8c9adb5f9b3d314640e06f876cfff97cac437159b4551ec27` |
| Agent SDK sdk.mjs | 0.3.293 | `a980b792353c301eea60d7896704cff21440de7c4aeefc3fe481be079504e203` |
| Bundled Linux CLI | 2.1.293 | `8968405e26db478af44eabc4635ab5ca557057b702a54460a59c13e1b253e978` |
| Node executable | >=22 (tested: 22.23.4) | reported in identity evidence, not pinned |
| Complete installed node_modules tree | locked closure | `d39ea52fb6f52fdc2617b65ce4ebb8831312df708118d60d6f6ad0ccb202547f` |
| Project bootstrap.mjs | shipped source | `3c3ad6569f91d561ca2c933bbc32ad1fb0e861fca83a61b979a24225c5e9f90d` |
| Project identity.mjs | shipped source | `9d4dc445cb445ab1d3aea5f60ecf7a737ea164727f2413d05f6f3f322116709d` |
| Project models.mjs | shipped source | `53b9a67fbd5e0222db49a692d8284e2ac19f8de07f79ff609488c67ba377d064` |
| Three-file project sidecar | shipped closure | `7ed0bff33783ae1f1eccaa624d115a7e0dc9acc50b449ddc2579a33edcf30c3d` |
| Tree + sidecar (resolved profile digest) | combined | `dde72401ce3b32662a9fe620cc43bb55be06ac10d6a8d9cfe441665712256629` |

The tree uses lexical depth-first traversal, excluding `.bin` links and npm's
`node_modules/.package-lock.json` install bookkeeping, hashing each
relative forward-slash path + NUL + file SHA-256 hex + newline. Sidecar identity
uses that record format for `bootstrap.mjs`, `identity.mjs`, then `models.mjs`; combined identity
is SHA-256 of tree hex + NUL + sidecar hex. A package version or lock alone does
not authorize unknown installation bytes. Re-verify an installed tree against
this identity with `AIP_CLAUDE_ACP_INSTALL_DIR=<install>` and
`go test ./connectors/claudeagentacp/internal/product -run TestInstalledBundleIdentity`. No real-account/billing smoke is claimed.
Discovery creates a bounded tools-off session and sends no user prompt. The SDK
can issue model-validation inference when selecting a model; discovery/selection
is not a guarantee of zero upstream requests or zero cost. Model validation
failures reject rather than silently select another model.

## Conversation-scoped agent runtimes

The Claude agent process carries the conversation, so the runtime pool keeps
**one live agent process per conversation**, never one shared process for every
request. The conversation identity is the **proxy-owned authoritative session
id** the host injects (`proxy_owned_session_id`, negotiated at ABI minor 4).
That id is stable exactly when the client resumes a session: a resumed session
keeps one agent process, and a stateless client — which the host gives a new
session per request — is isolated per request and replays its transcript into a
fresh process. No client-carried identifier and no transcript content is used
as an identity: neither is proof of conversation identity, and one agent
process must never carry two conversations, because it would also carry their
tool state.

Without host session authority, each execution receives a fresh opaque runtime
identity at `Open`, even when its tracing ID and content match another request.
An identical retry therefore gets a separate process unless it genuinely resumes
the authoritative session.

The turns of one conversation are serialized: a single stdio agent process
cannot carry two concurrent turns. A turn that arrives while the previous turn
of the same conversation still holds its runtime waits for that release
instead of failing, because the host publishes the client-visible response
before it closes the backend stream. The wait is bounded by
`turn_wait_seconds` (default 30) and observes the request's cancellation; a
turn that is still refused after that bound reports the runtime conflict
explicitly and never reuses the held process. `max_runtimes` (default 8) is a
hard bound on the live agent processes of one instance: a request that would
exceed the bound retires an idle process and waits for a running turn to
finish when no idle process is left. A process whose extinction was not
confirmed stays owned by the pool, keeps counting against the bound, and is
never reused. Set `max_runtimes: 0` to leave the pool unbounded.

A pool-wide construction reservation covers admission through process
registration. Registered children count during handshake as well as while
serving turns or retiring. Refused publication transfers the child to a
separate pool-owned retirement; request cancellation alone never quarantines
the reusable slot.

A turn that ends without a clean completion withdraws its runtime: the
incarnation it owned is detached before the claim is released, so the next turn
retires that exact process and never the process its successor published. A
refused `session/set_config_option` exchange is a pre-output failure the host
may fail over, not evidence of an unsupported model; the connector never
retries the failed turn itself.

## Credentials, consent and limits

Only `api_key`, `subscription_token`, `host_login` and `gateway` are accepted
credential sources, and the connector never falls back from one to another.
Stock `aipstd` provisions opaque `ConfigureRequest.Secrets` using
`plugins.backend_discovery.instances.<instance-id>.secret_refs`:
`api_key` for default `api_key`, or `claude_oauth_token` for `subscription_token`.
For subscription tokens, the operator runs official **`claude setup-token`** and
provisions its output privately; never paste it into YAML, argv or logs. For
`host_login`, run official **`claude auth login`** yourself; no connector secret is
required. AIProxer never initiates login or reads/copies/refreshes credential stores.
Host login passes through the CLI-owned home/configuration directory and accepts
the same host trust and administrator policy as the operator's own CLI. Key/token
sources use fresh process-owned roots removed on process termination.

Each binding must name the exact backend `kind`; credentials accept only
`{env: ENV_NAME}`, never literal/default/interpolated values or files. For example,
`api_key: {env: AIP_CLAUDE_API_KEY}` selects that variable in the **root host's**
environment. Only requested names are captured, after verified catalog registration
and access approval; secrets reach Configure only after peer authentication and
protocol negotiation, never through the connector launch environment.
`execution_env: [HOME, PATH, TMPDIR, CLAUDE_CONFIG_DIR]` explicitly captures
non-secret execution context; set PATH to a supported Node >=22 installation
and HOME/config locator to the intended CLI-owned root for host login. The fixed
allowlist also accepts platform/temp/locale paths, XDG_CONFIG_HOME and
ANTHROPIC_CONFIG_DIR; loader, provider and credential selectors are rejected.
Unrelated plugins retain an empty environment. Disabled rows never resolve values.
Binding changes and disabled-to-enabled transitions require restart. Ordinary
reload preserves startup snapshots; rotating an external environment value also
requires restarting the host. Inventory/structural inspection never captures.
An explicitly selected bound-instance `aipstd doctor --instance <id> --config <path>` uses the same authorization,
capture and peer/negotiation/Configure path, then closes its owned resources; it
does not provision other configured instances.

Explicitly select **`admin_policy: honor`** before enabling any source: the pinned
CLI may fetch uncached organisation/server policy, so even API-key default/refuse
rejects before runtime execution. Honor consents to that authority, including
provider, credential, hook and tool policy; AIProxer never bypasses it. Local/cached
policy changes replace the process. Startup and resolved profile disclose source,
runtime identity and `honored_potential_server_policy`, without account e-mail or
secrets. Each turn observes **current-applied** query identity before SDK input;
remote revision remains unknown, not a latest-policy refresh certificate.

`permission_policy: bypass` is the default: full built-in tools, including
Agent/Task, run in `bypassPermissions` mode; only `AskUserQuestion` is disabled.
Remaining permission prompts select offered `allow_once`, including while a
turn drains. Persistent-only (`allow_always`) offers cancel: automated approval
does not grant durable permission rules in the operator's Claude settings.
The agent can read, write and execute
anything the proxy's OS user can, **including that user's Claude login
credentials**; PR #1235's synthetic runtime probe demonstrated credential-file
access. This is intentional local single-user operation, not a sandbox. Set
`permission_policy: deny` for locked-down use (tools off, declines only); the
manifest still rejects multi-user deployments under either policy. `allow` is
no longer a valid value. Host settings/hooks isolation remains unchanged.

Administrator policy is honored, never overridden: if it disables bypass,
Claude uses normal mode while the handler still auto-accepts offered approvals.
The agent executes its own tools, so their activity appears as the shared ACP
tool-summary text in the assistant stream, never as client tool calls
(`tool_calls` / `tool_use`) that a client would try to execute. Before publishing a successful turn terminal, the sidecar waits
up to five seconds for SDK idle and an empty live-background-task registry. On
timeout or cancellation it asks the SDK to stop registered detached tasks (with
a one-second control deadline), then withdraws the query; stream close kills/reaps the
runtime and a later request must replay history into a fresh process after
cleanup completes. On connection close, bypass shutdown signals its per-runtime
supervisor and closes stdin. Only a verified successful supervisor process exit
authorizes root removal; the writable shutdown file is advisory and never
authorizes skipping cleanup or killing the owner. Detached
work cannot silently overlap a later turn on the same runtime. MCP pass-through
and richer background diagnostics remain separate follow-ups. No external effect
or billed request can be rolled back.

**Background support is bounded draining, not full native long-lived background
compatibility.** A shell task such as a development server cannot remain alive
across successful client turns: after the adapter returns its foreground result,
the five-second drain deadline can turn that interaction into a terminal error
and retire the runtime. Persistent background sessions remain separate Kiro scope.
Full built-in tool authority does not imply compatibility with every native
background lifetime.

Bypass runtimes run under a dedicated Linux subreaper supervisor (the packaged
connector's internal executable mode). SDK stop outcomes are recorded as
acknowledged, rejected, or timed out, but none is treated as an OS termination
proof. On retirement the supervisor kills and reaps its adopted descendants,
including detached process groups, using identity-bound pidfds. It acknowledges
cleanup only after the kernel reports no remaining children. Child-list inspection
is preflighted before Node launches. This requires Linux procfs, subreaper and
pidfd support; it does not add filesystem/network sandboxing.

**OS cleanup is fail-closed, not bounded.** Reaper enumeration/signalling failure
or its attempt deadline retains the supervisor and retries; it does not exit and
abandon detached children. If retirement remains pending for two seconds, an
operator error reports incomplete cleanup. The supervisor and configuration state
remain owned and concurrent shutdown callers may remain blocked until the OS
failure is resolved. Unexpected supervisor death returns a sticky incomplete-
cleanup error, refuses runtime reuse, and retains process/instance state for
operator recovery. Do not treat a killed supervisor or a missing marker as proof
of extinction. Abrupt process destruction by the OS/operator is not a containment
guarantee. Concurrent pool/transport cleanup callers join the same retirement
before the instance directory can be removed.

The delivery evidence includes a synthetic pinned-runtime lifecycle regression
for foreground/default bypass, detached drain/reuse, timeout, cancellation and
persistent-only host-login offers. It masks host credential/configuration
directories in user/mount/PID/network namespaces and uses a loopback TLS provider;
it is not a live-account certification. Maintained sidecar regressions cover the
idle/task barrier, failure-closed retirement and two sequential turns; deployment tests
exercise omitted permission policy through stock-host Configure and doctor.
Canonical usage is mapped from the adapter's authoritative reports: the
terminal prompt result's exact input/output/cache-read/cache-write/total
counters, and mid-stream `usage_update` context totals with the reported cost.
Mid-stream updates carry no input/output split, so none is synthesized; input
presence is marked per counter and absent reports stay absent.
**Agent boundary.** Claude Code runs as the agent with its own system prompt,
tools and (empty, strict) MCP configuration; a client harness's context never
reaches it. Client instructions and system/developer messages are dropped
before the runtime, with a content-free warning log naming how many were
dropped; the user/assistant conversation is still served. Requests carrying
client tool definitions or tool-call history are rejected
(`missing required capabilities: tools`), and client-supplied MCP servers are
never forwarded.
Adapter identity pushes (`_auth/status_update`) update server-side
diagnostics and the instance evidence (kind and plan label only, never
account e-mail); pushes arriving with no active turn are dropped by the
transport. Plan usage exhaustion surfaces as the explicit `plan_exhausted`
error kind with per-source remediation; before output it is eligible for
operator-configured failover, after output it ends the stream terminally.

**Subscription use.** You may run this connector with your own Claude
subscription: Anthropic's Claude Code terms allow an end user to sign in to the
unmodified Claude Code with their own plan, and sign-in here always completes
through Anthropic's own flow. The allowance is yours alone: never resell it or
make it available to anyone else. The host enforces this by rejecting the
connector in multi-user deployments, and the proxy never handles Claude.ai tokens
outside the official runtime. Shared or commercial deployments must use API keys
or cloud-provider credentials instead. See the
[subscription policy](backend-plugins/unsupported.md) for details.

### Gateway credential source (`credential_source: gateway`)

The gateway source routes the agent through **your** Anthropic-protocol gateway
with the adapter's ACP `gateway` authentication method. It is an explicit,
isolated fourth source: a connector instance never replaces its gateway
credentials with API keys, tokens or host login. Failures name gateway remediation.

**Routing boundary:** this is an instance-level credential guarantee, not a
route-wide failover prohibition. Before visible output, an authentication-required
error (`-32000`) remains recoverable by the host and can select another explicitly
configured route candidate, including a separate API-key instance. For gateway-only
traffic, use a route containing only gateway instances; do not add first-party
instances as sequential or parallel alternatives. After visible output, no
transparent replay is allowed. The connector does not change another instance's
source or propagate its gateway header secrets to it.

- `gateway.base_url` (non-secret config): absolute URL, **https**, or
  **http only for a loopback host** (`127.0.0.1`, `localhost`, `::1`); no user
  information in the URL. Requests carry the endpoint exactly as configured.
- `gateway.headers` (names only, ≤16 unique): each header name needs one opaque
  secret provisioned like other connector secrets. Header VALUES never appear
  in argv, logs, diagnostics, disclosure or invalid-configuration errors.
- Admission verifies, per prompt, a fresh session-query observation that the
  adapter's stored provider override is still exactly this endpoint and that
  no first-party credential is active. Wrong endpoints, cleared overrides,
  stale or first-party tickets refuse before any input reaches the SDK.
- Honest posture: usage and billing flow through **your** gateway; AIProxer
  makes **no Claude plan and no Anthropic API billing claim** for this source.
  Gateway quota/billing failures surface through generic upstream
  classification, never as Anthropic `plan_exhausted`.
- The runtime replaces a configured `Authorization` header with its own
  bypass-token authorization, so `Authorization` (any case) **rejects at
  configuration**; provision gateway credentials under a supported custom
   header name instead. Supported names match
   `^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`, the host's secret-reference namespace;
   HTTP token names outside that namespace (for example `X+Route`) reject.
- During certification the CLI also issues endpoint preflight/lookup requests
  (for example `HEAD /api/hello`) to the configured host itself; the gateway
   answers them from its normal HTTP routing. The pinned CLI also makes an
   auxiliary model request to generate a session title. Every recorded model
   request must carry the provisioned custom header and bypass authorization;
   the reachability probe carries neither credential header.

The gateway instance follows the same `admin_policy: honor` consent, deny
posture, owned roots, policy re-checks and process-tree discipline as the other
sources. The shipped sidecar digest changed for this feature; both tables above
list the recertified three-file identity and its combined digest.

The artifact includes only the Go executable and project-authored ESM sidecar
(repository license applies). Upstream adapter/ACP SDK are Apache-2.0; Agent SDK
and bundled CLI have their own Anthropic notices/license terms, Node has its own
license, and transitive licenses appear in the lock and installed packages.
Retain and review installed upstream LICENSE/NOTICE/README files. AIProxer does not
redistribute the adapter, SDK, CLI, Node, or their npm payloads. Stock `aipstd`
builds and runs without Node when this optional connector is unused; its runtime
dependencies do not include the Claude or Cursor SDK.

## Shared ACP source compatibility

The independent `connector-support/acp` Go module adds `SessionNewMeta` to
`HandshakeProfile` and `RetainRPCError` to `SessionUpdateMapperOptions`.
Existing keyed literals and zero-value/default behavior remain compatible;
external **unkeyed** literals of these structs must be converted to keyed literals.
New `SessionOptionsSpec`, `TransportOptionsSpec`, `PromptAdmissionSpec` and
`RuntimeIdentitySpec` interfaces are optional opt-ins; existing connector specs
need not implement them. `RuntimePoolConfig` gains the zero-value-compatible
`ClaimWait` and `MaxRuntimes` fields. `RuntimePool.ClaimForTurn` now takes a
`context.Context` so the bounded wait observes request cancellation;
`SetProcessContext` and `KillRuntimeContext` are the context-bounded forms of
the publication and retirement calls, and the context-free `SetProcess` and
`KillRuntime` remain for callers that own the slot. The canonical `pkg/aipapi`,
public runtime Options and backend-plugin wire ABI are unchanged. These are
distinct from the shared ACP Go source API.
