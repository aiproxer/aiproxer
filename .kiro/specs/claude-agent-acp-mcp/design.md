# Design Document: claude-agent-acp-mcp

## Overview
MCP pass-through becomes an explicit, validated, fail-closed option of the `claudeagentacp` connector. The MCP `deny` default keeps the selected permission policy's metadata unchanged. MCP `allow` retains its own recorded MCP validation prerequisite and runs under the connector's permission policy (default `bypass`, explicit `deny`). No separate permission certification prerequisite remains.

## Configuration
New instance configuration alongside `credential_source` / `admin_policy`:

```yaml
mcp_passthrough: deny            # deny (default) | allow
mcp_servers:                     # required non-empty for allow
  - name: docs                 # ^[a-z0-9][a-z0-9-_]{0,63}$, unique
    transport: stdio           # stdio | sse | http
    command: /usr/bin/mcp-docs # absolute path for stdio
    args: ["--root", "/srv/docs"]
    env:                       # bounded non-secret literals only
      LOG_LEVEL: warn
  - name: metrics
    transport: https? -> http  # sse|http
    url: https://mcp.example.com/sse   # https, or http for loopback hosts only
```

Validation (all fail-closed, reject before exec, entry-named diagnostics):

- strict schema: unknown fields reject; `name` regex + uniqueness; ≤ 32 servers;
- `stdio`: absolute `command`, ≤ 32 args each ≤ 4096 bytes, env ≤ 32 entries with names matching `^[A-Z_][A-Z0-9_]*$` and values ≤ 4096 bytes; secret-shaped values (reusing the connector's existing secret heuristics: token/api-key patterns) reject;
- `sse`/`http`: URL parse must succeed, scheme `https`, or `http` with a loopback host (`127.0.0.1`/`::1`/`localhost`); userinfo in URLs rejects; header-style credentials reject as secret-shaped;
- the list must be non-empty for `allow`; empty/deny config produces `mcpServers: []` exactly as today.

Operator additions never flow through the environment allowlist; server env is constructed only from this block. Disabled→enabled and list changes follow the existing restart-required binding rule (startup snapshot; no live re-read).

## Gates
`mcp_passthrough: allow` requires, in order:

1. existing local-only posture and `admin_policy: honor` (unchanged pre-exec gate);
2. `mcpCertifiedProfile` constant matching the observed runtime/extension digest (new, compiled-in, same form as the existing profile pin);
3. the connector's selected permission policy (default `bypass`, explicit `deny`), without an additional permission-profile digest.

Composition lives in `AuthEnv`/`Config` so every consumer (configure, doctor, spawn) sees identical outcomes; no partial enablement.

## Session metadata wiring
`AuthEnv.HandshakeProfile()` under `allow` replaces the top-level `SessionNewMCPServers` (`[]` today) with the ACP-shaped array the adapter consumes (verified 0.89.1: top-level `params.mcpServers` stdio/http/sse mapping). `claudeCode.options.mcpServers: {}`, `strictMcpConfig: true`, settings/plugins/hooks isolation, tool posture and permission policy are unchanged. Session fingerprints (adapter `computeSessionFingerprint` includes sorted mcpServers) change only when the configured list changes — which the restart-required rule already forces.

Client-supplied extensions cannot contribute: the connector-owned profile is built after `mergeHandshakeProfile` consumes client extension keys; MCP keys from clients are not among the accepted extension keys and remain ignored (test-proven, no new key).

## Permission interplay
MCP does not introduce approval semantics. Under `permission_policy: deny`, requests are declined. Under default `bypass`, prompts select an offered `allow_once`, including between turns; persistent-only offers cancel rather than granting durable rules. This is full OS-user access, not credential or filesystem confinement. MCP tests exercise both existing policies, not a synthetic permission-profile gate.

## Lifecycle and errors
Server processes are adapter children under the existing owned runtime; kill/reap/close already terminate the process tree, and the tests extend the process-tree suite to cover a configured-server fixture. Server failures ride the existing classification: bounded redacted diagnostics; pre-output eligibility and post-output terminality unchanged. Identity drift keeps the existing recertify-reject behavior.

## Certification
A dedicated MCP validation run records: advertisement of configured servers in a real session; a denied MCP tool call under permission deny; execution under permission bypass; process-tree cleanup with an active server on close. Evidence is attached to the spec and the MCP enabling constant is set only for the observed digest.

## Testing strategy
Red-first per task: schema/validation tables; byte-identity tests with MCP disabled under each permission policy; gate composition tests (missing honor, missing MCP digest, drift); metadata wiring tests over the adapter-shaped array; permission tests under deny and bypass; process-tree tests with a configured fake server. Regression: cursor/agy/gemini suites and explicit permission-deny tests unchanged.

## Risks and mitigations
- Secret leakage into server env → validation rejects secret-shaped values; connector secrets never participate in server env construction.
- Remote endpoint abuse → https-only except loopback; userinfo/credential headers reject.
- Silent posture coupling → MCP configuration never overrides the selected permission policy; missing MCP validation is named explicitly.
- Scope creep into OAuth/elicitation → explicitly deferred; elicitation capabilities stay unadvertised.
