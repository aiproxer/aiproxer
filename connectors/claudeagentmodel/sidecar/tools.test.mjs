import assert from 'node:assert/strict'
import test from 'node:test'
import { Client } from '@modelcontextprotocol/sdk/client/index.js'
import { InMemoryTransport } from '@modelcontextprotocol/sdk/inMemory.js'
import {
  MCP_PREFIX,
  ToolBroker,
  ToolFailure,
  buildToolCatalog,
  canonicalJSON,
  createToolServer,
} from './tool-server.mjs'
import { SidecarFailure, createPreToolUseHook } from './session.mjs'

const TOOLS = [
  {
    name: 'Read',
    mcp_name: 'Read',
    native_name: 'mcp__aiproxer__Read',
    description: 'read a file',
    parameters: { type: 'object', properties: { file_path: { type: 'string' } }, required: ['file_path'] },
  },
  {
    name: 'functions.apply_patch',
    mcp_name: 'functions_apply_patch_0123456789',
    native_name: 'mcp__aiproxer__functions_apply_patch_0123456789',
    description: 'apply a patch',
    parameters: { type: 'object' },
  },
]

function harness() {
  const catalog = buildToolCatalog(TOOLS)
  const broker = new ToolBroker()
  const frames = []
  let requestId = 'req-1'
  broker.bind((kind, payload) => frames.push({ request_id: requestId, kind, ...(payload ?? {}) }))
  const server = createToolServer({
    catalog,
    broker,
    onCall: async (tool, args, entry) => {
      frames.push({ request_id: requestId, kind: 'tool_call', tool_call_id: entry.id, native_name: tool.nativeName, tool_name: tool.name, arguments: args })
      frames.push({ request_id: requestId, kind: 'response_finished', stop_reason: 'tool_use', awaiting_tool_result: true })
      const result = await entry.deferred
      return { content: [{ type: 'text', text: result.content }], isError: Boolean(result.isError) }
    },
  })
  return { catalog, broker, frames, server, setRequestId: (id) => (requestId = id) }
}

async function connect(server) {
  const [clientTransport, serverTransport] = InMemoryTransport.createLinkedPair()
  await server.connect(serverTransport)
  const client = new Client({ name: 'aiproxer-test', version: '1.0.0' })
  await client.connect(clientTransport)
  return client
}

test('catalogue rejects duplicate and inconsistent native names', () => {
  assert.throws(() => buildToolCatalog([TOOLS[0], { ...TOOLS[0] }]), (err) => err instanceof ToolFailure)
  assert.throws(
    () => buildToolCatalog([{ ...TOOLS[0], native_name: 'mcp__aiproxer__Other' }]),
    (err) => err instanceof ToolFailure,
  )
  assert.throws(() => buildToolCatalog([{ ...TOOLS[0], parameters: [] }]), (err) => err instanceof ToolFailure)
  assert.deepEqual(buildToolCatalog(undefined).tools, [])
})

test('canonical json ignores key order but preserves values', () => {
  assert.equal(canonicalJSON({ b: 1, a: [true, null] }), canonicalJSON({ a: [true, null], b: 1 }))
  assert.notEqual(canonicalJSON({ a: '1' }), canonicalJSON({ a: 1 }))
  assert.equal(canonicalJSON({}), '{}')
})

test('MCP server publishes the exact admitted schema', async () => {
  const { server } = harness()
  const client = await connect(server)
  const listed = await client.listTools()
  assert.deepEqual(
    listed.tools.map((tool) => tool.name),
    ['Read', 'functions_apply_patch_0123456789'],
  )
  assert.deepEqual(listed.tools[0].inputSchema, TOOLS[0].parameters)
  assert.equal(listed.tools[0].description, 'read a file')
  assert.equal(listed.tools[0].annotations.readOnlyHint, false)
  assert.equal(listed.tools[1].annotations.readOnlyHint, false)
  await client.close()
})

test('MCP handler only awaits the deferred caller result', async () => {
  const { broker, frames, server, setRequestId } = harness()
  const client = await connect(server)
  const preToolUse = createPreToolUseHook({
    catalog: buildToolCatalog(TOOLS),
    broker,
    emit: (kind, payload) => frames.push({ kind, ...(payload ?? {}) }),
  })
  await preToolUse({ hook_event_name: 'PreToolUse', tool_name: MCP_PREFIX + 'Read', tool_input: { file_path: 'x' }, tool_use_id: 'toolu_01' }, undefined)

  const call = client.callTool({ name: 'Read', arguments: { file_path: 'x' } }, undefined, { timeout: 5000 })
  await new Promise((resolve) => setImmediate(resolve))
  assert.deepEqual(
    frames.filter((frame) => frame.kind === 'tool_call' || frame.kind === 'response_finished').map((frame) => frame.kind),
    ['tool_call', 'response_finished'],
  )
  assert.equal(frames.find((frame) => frame.kind === 'tool_call').tool_call_id, 'toolu_01')
  assert.equal(frames.find((frame) => frame.kind === 'tool_call').tool_name, 'Read')
  assert.deepEqual(frames.find((frame) => frame.kind === 'tool_call').arguments, { file_path: 'x' })

  setRequestId('req-2')
  broker.release('toolu_01', '{"content":"exact"}', false)
  const result = await call
  assert.equal(result.isError, false)
  assert.deepEqual(result.content, [{ type: 'text', text: '{"content":"exact"}' }])
  assert.equal(frames.filter((frame) => frame.kind === 'tool_released').length, 1)
  assert.throws(() => broker.release('toolu_01', 'again', false), (err) => err instanceof ToolFailure)
  await client.close()
})

test('MCP call without a PreToolUse entry fails closed', async () => {
  const { server } = harness()
  const client = await connect(server)
  await assert.rejects(() => client.callTool({ name: 'Read', arguments: {} }, undefined, { timeout: 2000 }))
  await client.close()
})

test('an unknown tool is not published and is refused', async () => {
  const { server } = harness()
  const client = await connect(server)
  await assert.rejects(() => client.callTool({ name: 'Bash', arguments: {} }, undefined, { timeout: 2000 }))
  await client.close()
})

test('PreToolUse denies native builtins without side effects', async () => {
  const { broker, frames } = harness()
  const hook = createPreToolUseHook({ catalog: buildToolCatalog(TOOLS), broker, emit: (kind, payload) => frames.push({ kind, ...(payload ?? {}) }) })
  const output = await hook({ hook_event_name: 'PreToolUse', tool_name: 'Read', tool_input: { file_path: '/etc/passwd' }, tool_use_id: 'toolu_native' }, 'toolu_native')
  assert.equal(output.hookSpecificOutput.permissionDecision, 'deny')
  assert.equal(broker.pending, null)
  assert.equal(frames.find((frame) => frame.kind === 'error').code, 'native_tool_use')
})

test('PreToolUse rejects a second outstanding call', async () => {
  const { broker } = harness()
  const state = { toolBlocks: new Map([['toolu_1', MCP_PREFIX + 'Read'], ['toolu_2', MCP_PREFIX + 'Read']]) }
  const hook = createPreToolUseHook({ catalog: buildToolCatalog(TOOLS), broker, emit: () => {}, state })
  await assert.rejects(
    () => hook({ tool_name: MCP_PREFIX + 'Read', tool_input: {}, tool_use_id: 'toolu_1' }, undefined),
    (err) => err instanceof SidecarFailure && err.code === 'unsupported_parallel_tool_calls',
  )
  // Once a call is claimed the broker refuses a second one even for a single
  // tool block.
  const solo = { toolBlocks: new Map([['toolu_1', MCP_PREFIX + 'Read']]) }
  const broker2 = new ToolBroker()
  broker2.bind(() => {})
  const soloHook = createPreToolUseHook({ catalog: buildToolCatalog(TOOLS), broker: broker2, emit: () => {}, state: solo })
  await soloHook({ tool_name: MCP_PREFIX + 'Read', tool_input: {}, tool_use_id: 'toolu_1' }, undefined)
  broker2.claim(buildToolCatalog(TOOLS).byMCP.get('Read'))
  await assert.rejects(
    () => soloHook({ tool_name: MCP_PREFIX + 'Read', tool_input: {}, tool_use_id: 'toolu_2' }, undefined),
    (err) => err instanceof SidecarFailure && err.code === 'unsupported_parallel_tool_calls',
  )
})

test('the MCP arguments must match the captured tool input', async () => {
  const catalog = buildToolCatalog(TOOLS)
  const broker = new ToolBroker()
  broker.bind(() => {})
  const server = createToolServer({
    catalog,
    broker,
    onCall: async () => ({ content: [{ type: 'text', text: 'never' }] }),
  })
  const client = await connect(server)
  const hook = createPreToolUseHook({ catalog, broker, emit: () => {} })
  await hook({ tool_name: MCP_PREFIX + 'Read', tool_input: { file_path: 'x' }, tool_use_id: 'toolu_args' }, undefined)
  await assert.rejects(() => client.callTool({ name: 'Read', arguments: { file_path: 'y' } }, undefined, { timeout: 2000 }))
  await client.close()
})

test('the hook accepts the id from the callback argument when the input omits it', async () => {
  const catalog = buildToolCatalog(TOOLS)
  const broker = new ToolBroker()
  broker.bind(() => {})
  const hook = createPreToolUseHook({ catalog, broker, emit: () => {} })
  await hook({ tool_name: MCP_PREFIX + 'Read', tool_input: {} }, 'toolu_callback')
  assert.equal(broker.pending.id, 'toolu_callback')
})


test('a stale unclaimed PreToolUse entry does not block the next call', async () => {
  const catalog = buildToolCatalog(TOOLS)
  const broker = new ToolBroker()
  broker.bind(() => {})
  const hook = createPreToolUseHook({ catalog, broker, emit: () => {} })
  await hook({ tool_name: MCP_PREFIX + 'Read', tool_input: { file_path: 'x' }, tool_use_id: 'toolu_stale' }, undefined)
  await hook({ tool_name: MCP_PREFIX + 'Read', tool_input: { file_path: 'y' }, tool_use_id: 'toolu_fresh' }, undefined)
  assert.equal(broker.pending.id, 'toolu_fresh')
  broker.claim(catalog.byMCP.get('Read'))
  await assert.rejects(
    () => hook({ tool_name: MCP_PREFIX + 'Read', tool_input: {}, tool_use_id: 'toolu_third' }, undefined),
    (err) => err instanceof SidecarFailure && err.code === 'unsupported_parallel_tool_calls',
  )
})


test('an MCP call for another admitted tool never attaches to the wrong pending', async () => {
  const { broker, frames, server } = harness()
  const client = await connect(server)
  const hook = createPreToolUseHook({ catalog: buildToolCatalog(TOOLS), broker, emit: () => {} })
  await hook({ tool_name: MCP_PREFIX + 'Read', tool_input: { file_path: 'x' }, tool_use_id: 'toolu_name' }, undefined)
  await assert.rejects(
    () => client.callTool({ name: 'functions_apply_patch_0123456789', arguments: { patch: 'x' } }, undefined, { timeout: 2000 }),
  )
  assert.equal(frames.filter((frame) => frame.kind === 'tool_call').length, 0)
  assert.equal(broker.pending.id, 'toolu_name')
  assert.equal(broker.pending.claimed, undefined)
  await client.close()
})

test('an error-looking caller result stays content, not protocol status', async () => {
  const { broker, frames, server } = harness()
  const client = await connect(server)
  const hook = createPreToolUseHook({ catalog: buildToolCatalog(TOOLS), broker, emit: () => {} })
  await hook({ tool_name: MCP_PREFIX + 'Read', tool_input: { file_path: 'x' }, tool_use_id: 'toolu_error' }, undefined)
  const call = client.callTool({ name: 'Read', arguments: { file_path: 'x' } }, undefined, { timeout: 5000 })
  await new Promise((resolve) => setImmediate(resolve))
  const content = '{"error":"permission denied","is_error":true}'
  broker.release('toolu_error', content, false)
  const result = await call
  // The canonical boundary carries content only: the connector neither infers a
  // failure from the text nor asserts success on the caller's behalf.
  assert.equal(result.isError, false)
  assert.deepEqual(result.content, [{ type: 'text', text: content }])
  assert.equal(frames.filter((frame) => frame.kind === 'tool_released').length, 1)
  await client.close()
})
