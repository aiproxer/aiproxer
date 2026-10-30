// Project-authored sidecar. Upstream packages are operator-installed, not
// copied or patched. Task 2.1 binds this file and the loaded installation bytes.
import { readFileSync, realpathSync } from 'node:fs';
import { isAbsolute, join } from 'node:path';
import { createRequire } from 'node:module';
import { pathToFileURL } from 'node:url';
import { installIdentity } from './identity.mjs';
import { installModels } from './models.mjs';

async function main() {
  if (Number(process.versions.node.split('.')[0]) < 22) throw new Error('Node >=22 required');
  const args = process.argv.slice(2);
  const rootAt = args.indexOf('--adapter-root');
  const sourceAt = args.indexOf('--credential-source');
  const source = args[sourceAt + 1];
  const sources = ['api_key', 'subscription_token', 'host_login', 'gateway'];
  if (rootAt < 0 || sourceAt < 0 || !isAbsolute(args[rootAt + 1] ?? '') || !sources.includes(source)) throw new Error('invalid bootstrap configuration');
  const allowed = new Set(['--adapter-root', args[rootAt + 1], '--credential-source', source, '--hide-claude-auth']);
  if (args.some(a => !allowed.has(a)) || (args.length !== 4 && args.length !== 5) || process.env.CLAUDE_CODE_EXECUTABLE || process.env.CLAUDE_AGENT_ACP_EXPERIMENTAL_V2) throw new Error('unsupported runtime override');
  // The gateway posture is connector-owned config: its non-secret endpoint
  // travels by this child-environment name only, and its presence binds the
  // sidecar's evidence to exactly one endpoint. Header VALUES are secrets and
  // never reach this process environment or argv.
  const gatewayBaseUrl = process.env.CLAUDE_GATEWAY_BASE_URL;
  if (source === 'gateway') {
    try { if (!gatewayBaseUrl || new URL(gatewayBaseUrl).protocol !== 'http:' && new URL(gatewayBaseUrl).protocol !== 'https:') throw 0; } catch { throw new Error('invalid gateway configuration'); }
  } else if (gatewayBaseUrl !== undefined) throw new Error('invalid gateway configuration');
  const root = realpathSync(args[rootAt + 1]);
  const metadata = JSON.parse(readFileSync(join(root, 'package.json'), 'utf8'));
  if (metadata.name !== '@agentclientprotocol/claude-agent-acp' || metadata.version !== '0.89.1' || metadata.dependencies?.['@anthropic-ai/claude-agent-sdk'] !== '0.3.293') throw new Error('unsupported adapter identity');
  const require = createRequire(join(root, 'package.json'));
  const load = file => import(pathToFileURL(file).href);
  const [{ v1AgentApp, buildConfigOptions }, { isDeniedModel }, { applyManagedPolicyEnv }, { nodeToWebReadable, nodeToWebWritable }, { ndJsonStream }] = await Promise.all([
    load(join(root, 'dist/acp-agent.js')), load(join(root, 'dist/session-model.js')), load(join(root, 'dist/managed-policy.js')),
    load(join(root, 'dist/utils.js')), load(require.resolve('@agentclientprotocol/sdk')),
  ]);
  await applyManagedPolicyEnv(); // identical trusted upstream administrator tier
  if (process.env.CLAUDE_CODE_EXECUTABLE) throw new Error('unsupported runtime override');
  // Upstream diagnostics may contain account payloads. This endpoint emits only
  // fixed safe diagnostics; stdout remains exclusively ACP protocol traffic.
  const safeError = () => process.stderr.write('Claude ACP adapter diagnostic\n');
  console.log = console.info = console.warn = console.debug = console.error = safeError;
  const logger = { log() {}, error: safeError };
  let agent;
  let observe;
  let models;
  const gateway = source === 'gateway' ? { baseUrl: gatewayBaseUrl, snapshot: null } : undefined;
  const app = v1AgentApp(logger, created => { agent = created; models = installModels(agent, buildConfigOptions, isDeniedModel, { gateway }); observe = installIdentity(agent, source, { models, gateway }); });
  app.onRequest('_aip/effective_identity_v1', { parse: params => params }, ({ params }) => observe(params));
  app.onRequest('_aip/models_v1', { parse: params => params }, ({ params }) => models.discover(params));
  const connection = app.connect(ndJsonStream(nodeToWebWritable(process.stdout), nodeToWebReadable(process.stdin)));
  let closing;
  const shutdown = () => closing ??= (async () => { await agent?.dispose().catch(safeError); process.exit(0); })();
  connection.closed.then(shutdown, shutdown);
  process.stdin.once('end', shutdown);
  process.on('SIGTERM', shutdown);
  process.on('SIGINT', shutdown);
  process.on('unhandledRejection', safeError);
  process.stdin.resume();
}

main().catch(() => { process.stderr.write('Claude identity bootstrap refused: verify Node >=22, pinned runtime and connector sidecar\n'); process.exitCode = 1; });
