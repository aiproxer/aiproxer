# Requirements Document

## Project Description (Input)
Add operator-configured MCP server pass-through to the `claudeagentacp` connector (follow-up to issue #705; split from #1185). The connector ships strict empty MCP: `session/new` carries `mcpServers: []` plus `strictMcpConfig: true`. Permissions independently default to full `bypass`, with explicit tools-off `deny`. This follow-up owns server configuration, credential provisioning hygiene and permission-policy interplay, not a new tool approval policy.

- **Who has the problem:** single-user operators who run the `claude-acp` agent runtime and want their own MCP servers available to the agent, without weakening the connector's isolation story.
- **Current situation:** MCP is deliberately closed by configuration and strict mode; the adapter (verified 0.89.1 bytes) consumes `session/new` top-level `mcpServers` in ACP shape and maps them to SDK servers, including background OAuth flows for elicitation-capable clients (AIProxer advertises none).
- **What should change:** an explicit, fail-closed `mcp_passthrough` policy with operator-configured servers, strict validation and secret hygiene. MCP enablement retains its own validation prerequisite and runs under the connector's permission policy (default bypass).

## V1 Slice
- **Delivers:** `mcp_passthrough: deny|allow` plus a validated `mcp_servers` list on the `claude-acp` instance. Under `allow`, configured servers pass through `session/new` with `strictMcpConfig` retained; transport shape, URL policy, non-secret environment and bounds are validated before sessions. Tool execution uses the selected permission policy: bypass auto-approves, deny refuses. MCP retains only its own recorded validation prerequisite.
- **First consumer:** the `claude-acp` instance on a single-user loopback deployment once MCP validation is recorded.
- **Safe degradation:** `deny` stays byte-identical; malformed, secret-shaped, or unsupported entries reject the instance pre-exec; client-supplied MCP extensions never populate or override the connector-owned configuration; server connection failures surface as diagnostics and follow existing failover/terminal rules.

## Deferred
- Per-server secret provisioning and rotation (env values are bounded non-secret literals in V1; secret refs are a follow-up).
- Remote MCP OAuth flows and elicitation-based authentication (AIProxer advertises no elicitation capabilities; operator-provisioned credentials remain deferred).
- An additional MCP-specific tool approval policy.
- Per-server tool filtering, MCP prompts/resources surfaces, and the centralized MCP gateway (#470) relationship.
- Windows/macOS certification of the MCP-enabled profile (Linux/amd64 only per the existing profile pins).
- MCP behavior under ACP v2 (#1194).

## Boundary Context
- **In scope**: `mcp_passthrough` configuration and validation, session metadata wiring, environment/URL hygiene, gate composition with existing certifications, permission-interplay tests, certification evidence, operator docs and a disabled example.
- **Out of scope**: approving MCP tool calls outside the existing permission policy, spawn/supervision of server processes by the connector, secrets for servers, remote OAuth, changes to other ACP connectors or to non-MCP sessions.
- **Adjacent expectations:** server processes run as adapter/CLI children under the same owned roots and process tree; the operator accepts the same local trust as the foreground agent.
- **Trust model**: unchanged from the archived connector spec; configured servers are operator-owned local processes or operator-chosen remote endpoints, never a sandbox boundary.
- **Boundary ownership**: backend plugin (`connectors/claudeagentacp`) plus additive `connector-support/acp` seams only if needed for session metadata variants.
- **Revalidation triggers**: adapter MCP mapping or authentication changes; permission-policy changes; process-tree or root-lifecycle changes.

## Requirements

### Requirement 1: Explicit policy and validated operator configuration
**Objective:** As a single-user operator, I want explicit, strictly validated MCP configuration, so that the default stays closed and nothing malformed or secret-bearing reaches the agent.

#### Acceptance Criteria
1. While `mcp_passthrough` is unset or `deny`, the connector shall preserve the selected permission policy's session metadata (`mcpServers: []`, no configured servers anywhere), without changing the bypass default.
2. When `mcp_passthrough: allow` is selected, the connector shall accept only operator-configured servers from a strict schema (unique bounded names; `stdio` with command/args or `sse`/`http` with URL; bounded env of non-secret literals) and reject unknown fields, malformed entries, duplicate names, and secret-shaped values before any process exec.
3. Client-supplied MCP extensions shall never populate, override, or extend the connector-owned server list.
4. Server list or policy changes shall follow the existing restart-required binding rule; disabled or removed entries shall not resolve or launch anything.

### Requirement 2: Isolation and credential hygiene
**Objective:** As an operator, I want explicit server credential provisioning hygiene, without mistaking full OS-user bypass for credential confinement.

#### Acceptance Criteria
1. MCP server processes shall be launched only by the agent runtime as its children under the connector-owned process tree; the connector shall never spawn or supervise them directly, and kill/reap/close shall reach the whole tree.
2. The connector shall not provision credential secrets into configured server environments, arguments, logs or diagnostics. This does not prevent OS-user processes or bypass tools from accessing readable credentials.
3. Remote servers shall be limited to operator-configured `https` URLs; plain `http` shall be admitted only for loopback hosts, matching the local single-user posture.
4. Any malformed, oversized, or unsupported server entry shall reject the instance pre-exec with a diagnostic naming the entry, never a partial configuration.

### Requirement 3: Permission-policy interplay
**Objective:** As an operator, I want MCP to change nothing about approvals, so that tool execution stays governed by the existing policy.

#### Acceptance Criteria
1. MCP tool execution shall use the existing permission policy: deny declines; default bypass auto-accepts offered `allow_once`, including between turns. Persistent-only offers cancel rather than granting durable rules. MCP shall not add another approval policy.
2. Enabling `mcp_passthrough: allow` shall require the recorded MCP validation; it shall run under the connector's permission policy (default bypass), without a separate permission-profile prerequisite.
3. While `deny` is active, refusal behavior outside turns and unknown requests shall remain exactly as the archived V1 (no new events, no retained payloads).
4. Session metadata shall retain `strictMcpConfig: true` and the existing settings/hook/plugin isolation unchanged.

### Requirement 4: Failure, lifecycle and diagnostics
**Objective:** As a client, I want MCP-enabled sessions to behave like every other session, so that clients need no MCP-specific handling.

#### Acceptance Criteria
1. Server connection or startup failures shall surface as bounded redacted diagnostics attributable to the session, mapped through existing error classification; pre-output failover eligibility and post-output terminal rules shall be unchanged.
2. Cancel, idle reap, reconfiguration drain and shutdown shall terminate owned server processes with the runtime's process tree; no configured server process shall outlive its runtime.
3. Runtime identity or configuration drift after enablement shall reject (or reset) exactly like existing certification gates; a mismatched profile shall never receive a session referencing configured servers.

### Requirement 5: Certification, disclosure and regression safety
**Objective:** As the maintainer, I want evidence-gated enablement and honest disclosure, so that MCP ships only with demonstrated behavior.

#### Acceptance Criteria
1. Enabling `mcp_passthrough: allow` shall require recorded MCP validation against the exact adapter/runtime profile demonstrating: server advertisement, a denied call under permission deny, execution under permission bypass, and process-tree cleanup on close.
2. Startup and resolved-profile disclosure shall name the MCP posture (deny default, allow enabled) and the configured server names, without secrets or URLs' credentials.
3. Operator documentation shall cover the two policies, validation rules, credential isolation and the certification requirement; a disabled example shall demonstrate `allow`.
4. The deny posture, other credential sources, and other ACP connectors shall remain covered by existing tests without behavioral change.

## Out-of-Scope Assumptions Worth Naming
- The connector's default bypass policy grants full OS-user access, including credential-file access; MCP validation is not a sandbox guarantee.
- No client capability negotiation changes (elicitation stays unadvertised), so remote OAuth flows cannot start.
