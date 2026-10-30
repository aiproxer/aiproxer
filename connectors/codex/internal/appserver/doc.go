// package appserver implements the OpenAI Codex CLI app-server backend.
// It launches the Codex CLI in app-server mode over stdio and speaks the Codex
// JSON-RPC 2.0 protocol (initialize → initialized → thread/start → turn/start →
// item/* notifications → turn/completed). This is a local-agent backend that
// uses the user's personal Codex login (OAuth-style), so it is treated as a
// credential-none backend. Install the Codex CLI and ensure `codex` is on PATH
// or set CODEX_BIN.
package appserver
