import { Server } from '@modelcontextprotocol/sdk/server/index.js'
import { CallToolRequestSchema, ListToolsRequestSchema } from '@modelcontextprotocol/sdk/types.js'

export const MCP_SERVER_NAME = 'aiproxer'
export const MCP_PREFIX = 'mcp__aiproxer__'

export class ToolFailure extends Error {
  constructor(code, message) {
    super(String(message ?? '').slice(0, 512))
    this.name = 'ToolFailure'
    this.code = code
  }
}

// canonicalJSON re-encodes one JSON value with sorted object keys so a hook
// observation and an MCP call compare equal without depending on key order.
export function canonicalJSON(value) {
  if (value === null || typeof value !== 'object') return JSON.stringify(value ?? null)
  if (Array.isArray(value)) return `[${value.map((entry) => canonicalJSON(entry)).join(',')}]`
  const keys = Object.keys(value).sort()
  return `{${keys.map((key) => `${JSON.stringify(key)}:${canonicalJSON(value[key])}`).join(',')}}`
}

// buildToolCatalog validates the connector-owned tool snapshot the Go bridge
// admitted for this step. The sidecar never invents tools and never executes one.
export function buildToolCatalog(requested) {
  if (requested === undefined || requested === null) {
    return { tools: [], byNative: new Map(), byMCP: new Map() }
  }
  if (!Array.isArray(requested)) throw new ToolFailure('invalid_request', 'tools must be an array')
  if (requested.length > 128) throw new ToolFailure('invalid_request', 'tools exceed the catalogue bound')
  const tools = []
  const byNative = new Map()
  const byMCP = new Map()
  for (const entry of requested) {
    if (!entry || typeof entry !== 'object') throw new ToolFailure('invalid_request', 'tool definition must be an object')
    const name = typeof entry.name === 'string' ? entry.name : ''
    const mcpName = typeof entry.mcp_name === 'string' ? entry.mcp_name : ''
    const nativeName = typeof entry.native_name === 'string' ? entry.native_name : ''
    const description = typeof entry.description === 'string' ? entry.description : ''
    const parameters = entry.parameters
    if (name === '' || mcpName === '' || nativeName === '') {
      throw new ToolFailure('invalid_request', 'tool definition omits its name mapping')
    }
    if (nativeName !== MCP_PREFIX + mcpName) {
      throw new ToolFailure('invalid_request', 'tool definition has an inconsistent native name')
    }
    if (!parameters || typeof parameters !== 'object' || Array.isArray(parameters)) {
      throw new ToolFailure('invalid_request', 'tool parameters must be a JSON object schema')
    }
    if (byMCP.has(mcpName) || byNative.has(nativeName)) {
      throw new ToolFailure('invalid_request', 'tool definitions collide on a native name')
    }
    const tool = { name, mcpName, nativeName, description, parameters }
    tools.push(tool)
    byMCP.set(mcpName, tool)
    byNative.set(nativeName, tool)
  }
  return { tools, byNative, byMCP }
}

// ToolBroker owns the single outstanding deferred invocation of one step. The
// PreToolUse hook creates the entry before the MCP handler may wait; the handler
// only awaits it and never executes a command, file or network operation.
export class ToolBroker {
  constructor() {
    this.pending = null
    this.emit = () => {}
  }

  // bind uses the same two-argument emission contract as the rest of the
  // sidecar: (kind, payload). The bridge decides which request id the frame
  // belongs to, so a release can never emit a malformed frame.
  bind(emit) {
    this.emit = emit
  }

  create({ id, nativeName, tool, args }) {
    if (this.pending) {
      if (!this.pending.claimed) {
        // The earlier tool_use never reached the MCP handler (for example the
        // permission layer denied it), so it cannot be waiting for a result.
        const stale = this.pending
        this.pending = null
        stale.reject(new ToolFailure('tool_abandoned', 'the earlier tool call never reached the MCP handler'))
      } else {
        throw new ToolFailure('unsupported_parallel_tool_calls', 'a second outstanding native tool call is not supported')
      }
    }
    if (typeof id !== 'string' || id === '') throw new ToolFailure('tool_correlation_failed', 'PreToolUse omitted the native tool id')
    if (tool.nativeName !== nativeName) throw new ToolFailure('tool_correlation_failed', 'PreToolUse named a tool outside the admitted catalogue')
    if (!args || typeof args !== 'object' || Array.isArray(args)) {
      throw new ToolFailure('tool_correlation_failed', 'PreToolUse supplied non-object tool arguments')
    }
    let resolve
    let reject
    const deferred = new Promise((res, rej) => {
      resolve = res
      reject = rej
    })
    deferred.catch(() => {})
    const entry = { id, nativeName, tool, argsCanonical: canonicalJSON(args), deferred, resolve, reject }
    this.pending = entry
    return entry
  }

  claim(tool) {
    const entry = this.pending
    if (!entry || entry.tool.nativeName !== tool.nativeName) {
      throw new ToolFailure('tool_correlation_failed', 'no PreToolUse entry matches this MCP tool call')
    }
    entry.claimed = true
    return entry
  }

  release(callID, content, isError) {
    const entry = this.pending
    if (!entry) throw new ToolFailure('tool_correlation_failed', 'no deferred tool call is waiting')
    if (entry.id !== callID) throw new ToolFailure('tool_correlation_failed', 'the released tool call does not match the pending native id')
    this.pending = null
    entry.resolve({ content, isError: Boolean(isError) })
    this.emit('tool_released', { tool_call_id: entry.id })
    return entry
  }

  fail(error) {
    if (!this.pending) return
    const entry = this.pending
    this.pending = null
    entry.reject(error)
  }
}

// createToolServer publishes exactly the admitted catalogue as raw JSON Schema
// and routes each call into the deferred broker. The handler never executes a
// tool: it awaits the caller's next HTTP result.
export function createToolServer({ catalog, broker, onCall }) {
  const server = new Server({ name: MCP_SERVER_NAME, version: '1.0.0' }, { capabilities: { tools: {} } })
  server.setRequestHandler(ListToolsRequestSchema, () => ({
    tools: catalog.tools.map((tool) => ({
      name: tool.mcpName,
      description: tool.description,
      inputSchema: tool.parameters,
      // One outstanding call per step: never advertise a read-only hint that
      // would invite concurrent invocation.
      annotations: { readOnlyHint: false },
    })),
  }))
  server.setRequestHandler(CallToolRequestSchema, async (request) => {
    const name = request?.params?.name
    const tool = catalog.byMCP.get(name)
    if (!tool) throw new ToolFailure('tool_correlation_failed', 'the MCP call named a tool outside the admitted catalogue')
    const args = request?.params?.arguments
    const entry = broker.claim(tool)
    if (canonicalJSON(args ?? {}) !== entry.argsCanonical) {
      throw new ToolFailure('tool_correlation_failed', 'the MCP call arguments do not match the captured tool input')
    }
    return await onCall(tool, args ?? {}, entry)
  })
  return server
}
