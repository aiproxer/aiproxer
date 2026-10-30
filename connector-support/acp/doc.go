// Package acp implements the Agent Client Protocol (ACP) prompt-turn subset as a backend
// connector. It speaks to an ACP agent using JSON-RPC 2.0 over HTTP POST to {BaseURL}/v1/acp
// with NDJSON streaming for session/prompt (test transport compatible with
// internal/refbackend/acp). A future stdio [Transport] can drive the same handshake and
// mapping layers for CLI subprocess agents.
//
// # Call.Extensions (JSON values)
//
// Session and wire hints (merged with [Config.Handshake]):
//
//   - "acp.sessionId" (string): reuse an existing ACP session; skip session/new.
//   - "acp.cwd" / "acp.workspace" (string): session/new cwd (default "/").
//   - "acp.mcpServers" (JSON array): forwarded to session/new mcpServers (default []).
//   - "acp.initialize.clientCapabilities" (object): merged into initialize clientCapabilities.
//   - "acp.initialize.clientInfo" (object): merged into initialize clientInfo.
//   - "acp.authenticate.skip" (bool): when true, omit the authenticate RPC (Gemini-style).
//   - "acp.authenticate.methodId" (string): e.g. cursor_login; builds authenticate params.
//   - "acp.authenticate.params" (object): full authenticate params override.
//   - "acp.messageId" (string): session/prompt messageId; generated if absent.
//
// # Transport matrix
//
// HTTP (this package’s [httpTransport]): one POST per unary RPC; session/prompt returns
// NDJSON in the response body. Inbound server→client requests during a prompt are rare;
// when present, responses are sent as additional POSTs via [Transport.SendJSONRPC].
//
// Stdio (future): newline-delimited JSON-RPC on stdin/stdout; [ServerRequestHandler]
// responses are written on the same stream.
package acp

// ID is the reserved plugin identifier for the ACP backend.
const ID = "acp"

const (
	extSessionJSONKey = "acp.sessionId"

	extCwdJSONKey        = "acp.cwd"
	extWorkspaceJSONKey  = "acp.workspace"
	extMCPServersJSONKey = "acp.mcpServers"

	extInitClientCapabilitiesJSONKey = "acp.initialize.clientCapabilities"
	extInitClientInfoJSONKey         = "acp.initialize.clientInfo"

	extAuthenticateSkipJSONKey     = "acp.authenticate.skip"
	extAuthenticateMethodIDJSONKey = "acp.authenticate.methodId"
	extAuthenticateParamsJSONKey   = "acp.authenticate.params"

	extMessageIDJSONKey = "acp.messageId"
)
