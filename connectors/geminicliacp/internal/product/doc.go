// Package geminicliacp implements the Gemini CLI backend via the Agent Control
// Protocol over stdio. The Gemini CLI is spawned with --experimental-acp and
// communicates using JSON-RPC over stdin/stdout. This connector uses a minimal
// handshake (initialize → session/new, no authenticate) matching the Gemini CLI
// ACP protocol. Install the CLI with: npm install -g @google/gemini-cli.
package product
