// Package cursorcliacp implements the Cursor CLI backend via the Agent Control
// Protocol over stdio. The Cursor CLI agent is spawned with the "acp" positional
// argument and communicates using JSON-RPC over stdin/stdout. The connector
// handles Cursor-specific server requests (permissions, questions, plans) in
// headless mode. Install the Cursor CLI and ensure `agent` is on PATH or set
// CURSOR_AGENT_BIN.
package product
