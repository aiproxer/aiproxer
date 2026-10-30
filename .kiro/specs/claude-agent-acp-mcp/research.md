# Research: claude-agent-acp-mcp

## Archived baseline — main `d85d44de` (spec) / connector tree `e5662018`
- `AuthEnv.HandshakeProfile()` (`connectors/claudeagentacp/internal/product/authenv.go`): top-level `SessionNewMCPServers: []`, `claudeCode.options.mcpServers: {}`, `strictMcpConfig: true`; tools off; permission policy deny-only.
- Client extension keys (`extMCPServersJSONKey`) feed `mergeHandshakeProfile` for shared ACP HTTP usage; the connector-owned profile is applied after and is not client-overridable.
- `connector-support/acp/handshake.go` already supports top-level `mcpServers` in `session/new`.

## Adapter evidence (certified 0.89.1 bytes, offline cache)
- `session/new` top-level `params.mcpServers` are mapped: stdio `{name, command, args, env[]}` → SDK `{type: "stdio", ...}`; `{type: "http"|"sse", url, headers[]}` → corresponding SDK types (`dist/acp-agent.js` around line 7159).
- Session fingerprint includes sorted `mcpServers` (`computeSessionFingerprint`), so list changes are naturally session-invalidating; the connector's restart-required rule matches.
- The adapter starts background MCP OAuth flows only for elicitation-capable clients (`startMcpServerAuthentication`, `clientCapabilities.elicitation.url`); AIProxer advertises no elicitation, so remote OAuth cannot start — consistent with the Deferred list.
- `strictMcpConfig: true` is honored by the adapter options surface (`OPTION_REBUILDS_SESSION` includes `strictMcpConfig`), so V1 metadata can keep it.

## Open items for task 1.1/2.1
- Exact adapter expectations for `sse` vs `http` server field names and header encoding (headers array entries `{name, value}`) — pinned by fixtures before wiring.
- Whether configured stdio servers are spawned eagerly at session start or lazily on first use (affects process-tree test fixtures); verify against the pinned profile during the spike that already produces the V1 metadata fixtures.

## Dependencies and sequencing
- Permissions now use the connector's policy (default full bypass, explicit deny). MCP retains its own validation prerequisite; there is no separate permission-profile gate. The archived tools-off observation above applies only to explicit deny now. Full OS-user access is not credential confinement.
- Related scheduling: #1185 children — #1192 macOS (blocked), this issue, #1194 ACP v2, #1195 gateway auth.
