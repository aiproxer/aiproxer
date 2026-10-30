#!/usr/bin/env node
// run.mjs - execute the vendored official OpenResponses compliance runner.
//
// Node lacks Bun's WebSocket constructor-with-headers extension, which the
// official suite relies on for server-side WebSocket tests (see
// src/lib/compliance-tests.ts `makeWebSocketSession`). We shim the global
// WebSocket with the `ws` package (supporting (url, { headers }) and the
// WHATWG EventTarget API the suite uses) before running the upstream runner.
//
// The upstream runner is TypeScript with extensionless relative imports, so we
// bundle it (plus the vendored schemas and sse-parser) with esbuild into
// memory and execute it in-process. Dependencies are pinned exactly via
// package-lock.json; this script performs no network access.
//
// Usage: node scripts/run.mjs --base-url <url> [--api-key <key>] [--model <model>]
//                             [--auth-header <name>] [--no-bearer] [--filter <ids>]
//                             [--verbose] [--json]

import path from "node:path";
import { fileURLToPath } from "node:url";
import { build } from "esbuild";

const toolRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");

// 1. Shim the WebSocket global so server-side WebSocket tests can send the
//    authorization header the OpenResponses frontend requires on upgrade.
try {
  const { default: WsWebSocket } = await import("ws");
  globalThis.WebSocket = WsWebSocket;
} catch (error) {
  console.error("run.mjs: failed to load ws WebSocket shim:", error);
  process.exit(2);
}

// 2. Bundle the vendored upstream runner (TS + extensionless imports) in memory.
const bundle = await build({
  entryPoints: [path.join(toolRoot, "bin", "compliance-test.ts")],
  bundle: true,
  write: false,
  platform: "node",
  format: "esm",
  target: "node20",
  sourcemap: "inline",
  logLevel: "silent",
});

const runnerCode = bundle.outputFiles[0].text;
const runnerPath = path.join(toolRoot, "dist", "compliance-test.mjs");

// 3. Execute the runner with the user-provided arguments. The upstream runner
//    calls process.exit(failed > 0 ? 1 : 0), which terminates this process with
//    the suite's exit code.
process.argv = [process.execPath, runnerPath, ...process.argv.slice(2)];
const runnerModule = await import("data:text/javascript;base64," +
  Buffer.from(runnerCode).toString("base64"));
await runnerModule;
