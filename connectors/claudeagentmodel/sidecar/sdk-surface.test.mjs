import assert from 'node:assert/strict'
import { execFileSync } from 'node:child_process'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import { buildOptions } from './session.mjs'
import { MCP_PREFIX, buildToolCatalog, createToolServer, ToolBroker } from './tool-server.mjs'

// Source-bound characterization of the *pinned* SDK/CLI surface this connector
// depends on. These checks run offline and without a paid account; the fake
// sidecar tests prove the Go bridge, this file proves the pinned boundary.

const packageJSON = JSON.parse(readFileSync(new URL('./package.json', import.meta.url), 'utf8'))
const declaration = readFileSync(new URL('./node_modules/@anthropic-ai/claude-agent-sdk/sdk.d.ts', import.meta.url), 'utf8')

function typeBlock(name) {
  const start = declaration.indexOf(`export declare type ${name} =`)
  assert.ok(start >= 0, `pinned SDK declaration is missing type ${name}`)
  const next = declaration.indexOf('\nexport declare ', start + 1)
  return declaration.slice(start, next < 0 ? undefined : next)
}

const TOOL = {
  name: 'Read',
  mcp_name: 'Read',
  native_name: 'mcp__aiproxer__Read',
  description: 'read a file',
  parameters: { type: 'object', properties: { file_path: { type: 'string' } }, required: ['file_path'] },
}

test('the SDK dependency is pinned to the characterized version', () => {
  assert.equal(packageJSON.dependencies['@anthropic-ai/claude-agent-sdk'], '0.3.316')
  assert.equal(packageJSON.dependencies['@modelcontextprotocol/sdk'], '1.32.2')
})

test('the pinned Options declaration carries every isolation setting we rely on', () => {
  const options = typeBlock('Options')
  for (const field of [
    'tools?:',
    'allowedTools?:',
    'disallowedTools?:',
    'settingSources?:',
    'strictMcpConfig?:',
    'permissionMode?:',
    'hooks?:',
    'verbatimPrompts?:',
    'skills?:',
    'mcpServers?: Record<string, McpServerConfig>',
  ]) {
    assert.ok(options.includes(field), `pinned SDK Options is missing ${field}`)
  }
})

test('PreToolUse exposes the native tool_use_id, name and input', () => {
  const input = typeBlock('PreToolUseHookInput')
  assert.match(input, /tool_name: string/)
  assert.match(input, /tool_input: unknown/)
  assert.match(input, /tool_use_id: string/)
  const output = typeBlock('PreToolUseHookSpecificOutput')
  assert.match(output, /permissionDecision\?: HookPermissionDecision/)
})

test('an in-process SDK MCP server is a connectable instance', () => {
  assert.match(typeBlock('McpSdkServerConfigWithInstance'), /instance: McpServer/)
  const status = typeBlock('McpServerStatus')
  for (const value of ["'connected'", "'failed'", "'needs-auth'", "'pending'", "'disabled'"]) {
    assert.ok(status.includes(value), `pinned McpServerStatus is missing ${value}`)
  }
})

test('buildOptions disables every native surface and admits only the transient server', () => {
  const catalog = buildToolCatalog([TOOL])
  const broker = new ToolBroker()
  const server = createToolServer({ catalog, broker, onCall: async () => ({ content: [] }) })
  const options = buildOptions(
    { cli: '/usr/local/bin/claude', workspace: '/tmp', env: { PATH: '/usr/bin' } },
    new AbortController(),
    {
      servedTools: true,
      mcpServers: { aiproxer: { type: 'sdk', name: 'aiproxer', instance: server, timeout: 1_800_000 } },
      hooks: { PreToolUse: [{ hooks: [async () => ({ continue: true })] }] },
    },
  )
  assert.deepEqual(options.tools, [])
  assert.deepEqual(options.settingSources, [])
  assert.deepEqual(options.settings, { disableAllHooks: true })
  assert.equal(options.strictMcpConfig, true)
  assert.equal(options.permissionMode, 'dontAsk')
  assert.equal(options.verbatimPrompts, true)
  assert.deepEqual(options.skills, [])
  assert.deepEqual(options.disallowedTools, [])
  assert.deepEqual(options.allowedTools, [`${MCP_PREFIX}*`])
  assert.deepEqual(options.systemPrompt, { type: 'preset', preset: 'claude_code', snapshot: false })
  assert.deepEqual(Object.keys(options.mcpServers), ['aiproxer'])
  assert.equal(options.mcpServers.aiproxer.type, 'sdk')
  assert.equal(typeof options.mcpServers.aiproxer.instance.connect, 'function')
  assert.equal(options.hooks.PreToolUse.length, 1)
})

test('a text-only turn advertises no tool surface at all', () => {
  const options = buildOptions(
    { cli: '/usr/local/bin/claude', workspace: '/tmp', env: { PATH: '/usr/bin' } },
    new AbortController(),
  )
  assert.deepEqual(options.allowedTools, [])
  assert.deepEqual(options.mcpServers, {})
  assert.equal(options.hooks, undefined)
  assert.deepEqual(options.systemPrompt, { type: 'preset', preset: 'claude_code', snapshot: false })
})

test('client instructions are appended to the Claude Code preset', () => {
  const options = buildOptions(
    { cli: '/usr/local/bin/claude', workspace: '/tmp', env: {}, system: 'client instruction' },
    new AbortController(),
  )
  assert.deepEqual(options.systemPrompt, {
    type: 'preset',
    preset: 'claude_code',
    snapshot: false,
    append: 'client instruction',
  })
})

test('the installed Claude Code CLI satisfies the verbatimPrompts minimum', (t) => {
  let raw
  try {
    raw = execFileSync('claude', ['--version'], { encoding: 'utf8', timeout: 10_000 }).trim()
  } catch {
    t.skip('the Claude Code CLI is not installed on this host')
    return
  }
  const match = /(\d+)\.(\d+)\.(\d+)/.exec(raw)
  assert.ok(match, `unexpected CLI version output: ${raw}`)
  const version = [Number(match[1]), Number(match[2]), Number(match[3])]
  const minimum = [2, 1, 248]
  for (let i = 0; i < minimum.length; i += 1) {
    if (version[i] > minimum[i]) break
    if (version[i] < minimum[i]) assert.fail(`Claude Code ${version.join('.')} is older than the required 2.1.248`)
  }
})
