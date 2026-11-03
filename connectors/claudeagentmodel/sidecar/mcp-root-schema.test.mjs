import assert from 'node:assert/strict'
import test from 'node:test'
import { Client } from '@modelcontextprotocol/sdk/client/index.js'
import { InMemoryTransport } from '@modelcontextprotocol/sdk/inMemory.js'
import { ToolBroker, buildToolCatalog, createToolServer } from './tool-server.mjs'

// Characterizes the real MCP root-schema contract that Go admission must match:
// the installed client rejects any tool whose inputSchema root is not
// {"type":"object"}, so admitting such a schema would make the whole catalogue
// unlistable at the model boundary.

function tool(parameters) {
  return {
    name: 'Read',
    mcp_name: 'Read',
    native_name: 'mcp__aiproxer__Read',
    description: 'read a file',
    parameters,
  }
}

async function listThroughRealClient(parameters) {
  const broker = new ToolBroker()
  broker.bind(() => {})
  const server = createToolServer({
    catalog: buildToolCatalog([tool(parameters)]),
    broker,
    onCall: async () => ({ content: [{ type: 'text', text: 'ok' }] }),
  })
  const [clientTransport, serverTransport] = InMemoryTransport.createLinkedPair()
  await server.connect(serverTransport)
  const client = new Client({ name: 'root-schema-probe', version: '1.0.0' })
  await client.connect(clientTransport)
  try {
    return await client.listTools()
  } finally {
    await client.close()
  }
}

test('an object root schema lists through the installed MCP client', async () => {
  const listed = await listThroughRealClient({ type: 'object', properties: { file_path: { type: 'string' } } })
  assert.deepEqual(listed.tools.map((entry) => entry.name), ['Read'])
  assert.equal(listed.tools[0].inputSchema.type, 'object')
})

test('non-object roots are rejected by the installed MCP client', async () => {
  await assert.rejects(() => listThroughRealClient({}))
  await assert.rejects(() => listThroughRealClient({ type: 'string' }))
})
