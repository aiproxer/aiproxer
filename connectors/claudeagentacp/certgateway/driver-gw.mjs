// Project-owned test-only driver: speaks the connector's ACP handshake frames
// to the shipped sidecar spawn of the pinned adapter (gateway credential
// source). Exits non-zero unless every certification row passes, including
// exact gateway header observation on the stub, streamed content deltas and
// one-use admission refusal.
import { spawn } from 'node:child_process';
import { appendFileSync, mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { join, resolve } from 'node:path';
import process from 'node:process';
import { createNdjsonParser } from './ndjson.mjs';

const adapterRoot = process.env.GW_ADAPTER_ROOT;
const sidecarDir = resolve(process.env.GW_SIDECAR);
const port = Number(process.env.GW_PORT ?? 18931);
const gatewayUrl = `http://127.0.0.1:${port}`;
const base = process.cwd();
const stateDir = join(base, 'cert-state');
const workspace = join(base, 'cert-workspace');
rmSync(stateDir, { recursive: true, force: true });
rmSync(workspace, { recursive: true, force: true });
mkdirSync(stateDir, { recursive: true });
mkdirSync(workspace, { recursive: true });

let child;
const die = message => {
  console.error(`CERT-GATEWAY-FAIL: ${message}`);
  try { child?.kill('SIGKILL'); } catch {}
  process.exitCode = 1;
  process.exit(1);
};
if (!adapterRoot || !sidecarDir) die('GW_ADAPTER_ROOT and GW_SIDECAR are required');
const stubLogPath = process.env.GW_LOG_FILE;
if (!stubLogPath) die('GW_LOG_FILE is required (the stub gateway request log)');

child = spawn(process.execPath, [join(sidecarDir, 'bootstrap.mjs'), '--adapter-root', adapterRoot, '--credential-source', 'gateway'], {
  env: {
    PATH: process.env.PATH,
    HOME: stateDir,
    CLAUDE_CONFIG_DIR: stateDir,
    CLAUDE_GATEWAY_BASE_URL: gatewayUrl,
  },
  stdio: ['pipe', 'pipe', 'pipe'],
});
child.stderr.pipe(process.stderr); // sidecar chatter forwards to the harness scanner

// Chunk-safe NDJSON framing: pipe reads may split a frame at any byte.
const replies = new Map();
let nextId = 1;
const updateTexts = [];
let unparsed = false;
const parser = createNdjsonParser({
  onLine: line => {
    let message;
    try { message = JSON.parse(line); } catch { unparsed = true; writeFileSync(join(base, 'cert-unparsed.txt'), line); return; }
    if (message.method === 'session/update' && message.params?.update?.sessionUpdate === 'agent_message_chunk') {
      const text = message.params?.update?.content?.text;
      if (typeof text === 'string' && text.length > 0) updateTexts.push(text);
    }
    if (message.id !== undefined && replies.has(message.id)) {
      const { resolve } = replies.get(message.id);
      replies.delete(message.id);
      resolve(message);
    }
  },
  onOverflow: () => die('ndjson frame exceeded the maximum frame size'),
  maxBytes: 1 << 20,
});
child.stdout.setEncoding('utf8'); // retain partial multi-byte code points between reads
child.stdout.on('data', chunk => {
  appendFileSync(join(base, 'cert-sidecar-stdout.ndjson'), chunk);
  parser.push(chunk);
});
const call = (method, params) => new Promise(actualResolve => {
  const id = nextId++;
  const timer = setTimeout(() => die(`no reply within 60s for ${method}`), 60000);
  replies.set(id, { resolve: value => { clearTimeout(timer); actualResolve(value); } });
  child.stdin.write(JSON.stringify({ jsonrpc: '2.0', id, method, params }) + '\n');
});

const row = name => appendFileSync(join(stateDir, 'cert-rows.txt'), name + '\n');

const meta = {
  claudeCode: {
    options: {
      agents: {}, allowDangerouslySkipPermissions: false,
      disallowedTools: ['Agent', 'Task', 'TaskOutput', 'TaskStop'],
      managedSettings: { allowManagedPermissionRulesOnly: true, disableAllHooks: true, permissions: { disableBypassPermissionsMode: 'disable' } },
      mcpServers: {}, permissionMode: 'default', plugins: [], settingSources: [],
      settings: { apiKeyHelper: '', env: {}, hooks: {}, permissions: { allow: [], deny: [], ask: [] }, plugins: [] },
      skills: [], strictMcpConfig: true, tools: [],
    },
  },
};

const init = await call('initialize', {
  protocolVersion: 1,
  clientCapabilities: { fs: { readTextFile: false, writeTextFile: false }, terminal: false, auth: { _meta: { gateway: true } } },
  clientInfo: { name: 'AIProxer', version: '1' },
});
if (init.error) die(`initialize: ${JSON.stringify(init.error).slice(0, 400)}`);
const methods = Array.isArray(init.result?.authMethods) ? init.result.authMethods : [];
if (!methods.some(m => m?.id === 'gateway' && m?._meta?.gateway?.protocol === 'anthropic')) {
  die(`initialize did not offer the gateway auth method: ${JSON.stringify(methods).slice(0, 500)}`);
}
row('initialize_advertised_gateway');

const auth = await call('authenticate', { methodId: 'gateway', _meta: { gateway: { baseUrl: gatewayUrl, headers: { 'X-Route': 'cert-route-value' } } } });
if (auth.error) die(`authenticate: ${JSON.stringify(auth.error).slice(0, 500)}`);
row('authenticate_accepted_exact_payload');

const session = await call('session/new', { cwd: workspace, mcpServers: [], _meta: meta });
if (session.error) die(`session/new: ${JSON.stringify(session.error).slice(0, 900)}`);
const sessionId = session.result?.sessionId;
if (!sessionId) die('session/new returned no session id');
row('session_created_gateway_session');

const observe = await call('_aip/effective_identity_v1', { sessionId, checkpointId: 'a'.repeat(64) });
if (observe.error || observe.result?.credentialSource !== 'gateway') die(`observation: ${JSON.stringify(observe).slice(0, 500)}`);
row('admission_ticket_gateway_issued');

const prompt = await call('session/prompt', { sessionId, prompt: [{ type: 'text', text: 'cert ping' }], _meta: { aipIdentity: observe.result } });
if (prompt.error) die(`prompt: ${JSON.stringify(prompt.error).slice(0, 700)}`);
if (prompt.result?.stopReason !== 'end_turn') die(`prompt stopped early: ${JSON.stringify(prompt.result).slice(0, 400)}`);
if (!updateTexts.join('').includes('gateway-turn-ok')) {
  die(`streamed deltas did not carry the stub content: ${JSON.stringify(updateTexts.slice(0, 8))}`);
}
row('gateway_routed_streamed_turn');
row('streamed_text_delta_with_stub_content');

const replay = await call('session/prompt', { sessionId, prompt: [{ type: 'text', text: 'replay' }], _meta: { aipIdentity: observe.result } });
if (!replay.error) die('replayed tuple was admitted');
row('replayed_ticket_refused');

// The one-use admission state must have destroyed the session exactly as
// designed: an effective-identity replay with a new checkpoint stays refused.
const secondObserve = await call('_aip/effective_identity_v1', { sessionId, checkpointId: 'b'.repeat(64) });
if (!secondObserve.error) die('post-refusal observation was admitted');
row('post_refusal_observation_refused');

const closed = new Promise(resolve => child.once('close', resolve));
const shutdownTimer = setTimeout(() => die('sidecar did not close after SIGTERM'), 5000);
try { child.kill('SIGTERM'); } catch {}
await closed;
clearTimeout(shutdownTimer);
if (parser.remaining().trim() !== '') die('sidecar ended with an incomplete ndjson frame');
if (unparsed) die('sidecar emitted unparseable frames');
const rows = readFileSync(join(stateDir, 'cert-rows.txt'), 'utf8').trim().split('\n').length;
if (rows !== 8) die(`certification rows incomplete: ${rows}`);
process.exit(0);
