#!/usr/bin/env node
import path from 'node:path'
import { SDK_VERSION, SidecarFailure, discoverModels, runTurn } from './session.mjs'
import { ToolBroker, ToolFailure } from './tool-server.mjs'

const PROTOCOL = '1'
const MAX_FRAME_BYTES = 8 << 20
const MAX_CONCURRENT = 1
const MAX_TOOLS = 128

let config = null
let active = null
let sequence = 0
let pending = Promise.resolve()
let shuttingDown = false

function write(frame) {
  if (shuttingDown) return
  const line = JSON.stringify(frame)
  if (Buffer.byteLength(line, 'utf8') > MAX_FRAME_BYTES) {
    process.stderr.write('sidecar frame exceeded the transport bound\n')
    process.exit(1)
  }
  process.stdout.write(line + '\n')
}

function emit(requestId, kind, payload = {}) {
  sequence += 1
  write({ kind, request_id: requestId, sequence, ...payload })
}

function failure(requestId, err) {
  if (err instanceof SidecarFailure) emit(requestId, 'error', { code: err.code, message: err.message })
  else if (err instanceof ToolFailure) emit(requestId, 'error', { code: err.code, message: err.message })
  else emit(requestId, 'error', { code: 'sdk_error', message: 'the Claude Agent SDK failed' })
}

function validateConfig(frame) {
  if (typeof frame.cli !== 'string' || !path.isAbsolute(frame.cli)) {
    throw new SidecarFailure('invalid_configuration', 'the connector CLI path is not absolute')
  }
  if (typeof frame.workspace !== 'string' || !path.isAbsolute(frame.workspace)) {
    throw new SidecarFailure('invalid_configuration', 'the workspace path is not absolute')
  }
  return { cli: frame.cli, workspace: frame.workspace, env: { ...process.env } }
}

function validateStart(frame) {
  if (typeof frame.request_id !== 'string' || frame.request_id === '') {
    throw new SidecarFailure('invalid_request', 'start requires a request id')
  }
  if (typeof frame.prompt !== 'string' || frame.prompt.trim() === '') {
    throw new SidecarFailure('invalid_request', 'a non-empty prompt is required')
  }
  if (frame.tools !== undefined && (!Array.isArray(frame.tools) || frame.tools.length > MAX_TOOLS)) {
    throw new SidecarFailure('invalid_request', 'the tool catalogue is invalid or oversized')
  }
  const pendingTTLMillis = Number.isFinite(frame.pending_ttl_ms) && frame.pending_ttl_ms > 0 ? frame.pending_ttl_ms : 1800000
  return {
    model: typeof frame.model === 'string' ? frame.model : '',
    system: typeof frame.system === 'string' ? frame.system : '',
    effort: typeof frame.effort === 'string' ? frame.effort : '',
    prompt: frame.prompt,
    request_id: frame.request_id,
    tools: frame.tools,
    pendingTTLMillis,
  }
}

async function handleDiscover(frame) {
  if (!config) throw new SidecarFailure('not_configured', 'the sidecar was not configured')
  const controller = new AbortController()
  active = { requestId: frame.request_id, controller, broker: null }
  try {
    const found = await discoverModels(config, controller)
    if (!found || controller.signal.aborted) return
    emit(frame.request_id, 'models', {
      models: found.models,
      cli_version: found.cli_version,
      account: found.account,
      sdk_version: SDK_VERSION,
      node_version: process.version,
    })
  } catch (err) {
    if (!controller.signal.aborted) failure(frame.request_id, err)
  } finally {
    active = null
  }
}

async function handleStart(frame) {
  if (!config) throw new SidecarFailure('not_configured', 'the sidecar was not configured')
  if (active) throw new SidecarFailure('busy', 'the sidecar already has an active request')
  const request = validateStart(frame)
  const controller = new AbortController()
  const broker = new ToolBroker()
  active = { requestId: request.request_id, controller, broker }
  const emitForActive = (kind, payload) => {
    if (!active || active.controller !== controller) return
    emit(active.requestId, kind, payload)
  }
  try {
    await runTurn({
      config,
      request,
      emit: emitForActive,
      abortController: controller,
      broker,
      pendingTTLMillis: request.pendingTTLMillis,
    })
  } catch (err) {
    if (!controller.signal.aborted) failure(active.requestId, err)
  } finally {
    if (active && active.controller === controller) active = null
  }
}

// A tool result is the only message that may arrive while a turn is parked, so
// it is dispatched immediately instead of waiting behind the running turn.
function handleToolResult(frame) {
  if (typeof frame.request_id !== 'string' || frame.request_id === '') {
    throw new SidecarFailure('protocol_error', 'tool_result requires a request id')
  }
  if (typeof frame.tool_call_id !== 'string' || frame.tool_call_id === '') {
    throw new SidecarFailure('protocol_error', 'tool_result requires a tool call id')
  }
  if (typeof frame.content !== 'string') {
    throw new SidecarFailure('protocol_error', 'tool_result requires string content')
  }
  if (!active || !active.broker) {
    throw new SidecarFailure('tool_correlation_failed', 'no parked native turn is waiting for a tool result')
  }
  active.requestId = frame.request_id
  active.broker.release(frame.tool_call_id, frame.content, frame.is_error === true)
}

function handleFrame(frame) {
  if (!frame || typeof frame !== 'object' || typeof frame.kind !== 'string') {
    write({ kind: 'error', code: 'protocol_error', message: 'missing sidecar request kind' })
    process.exit(1)
  }
  if (frame.kind === 'tool_result') {
    try {
      handleToolResult(frame)
    } catch (err) {
      if (active) failure(active.requestId, err)
      else write({ kind: 'error', code: err.code ?? 'protocol_error', message: err.message ?? 'tool result refused' })
    }
    return Promise.resolve()
  }
  switch (frame.kind) {
    case 'configure': {
      config = validateConfig(frame)
      return Promise.resolve()
    }
    case 'discover': {
      if (typeof frame.request_id !== 'string' || frame.request_id === '') {
        write({ kind: 'error', code: 'protocol_error', message: 'discover requires a request id' })
        process.exit(1)
      }
      return handleDiscover(frame)
    }
    case 'start': {
      return handleStart(frame)
    }
    case 'cancel': {
      if (active) {
        if (active.broker) active.broker.fail(new SidecarFailure('canceled', 'the caller canceled the turn'))
        active.controller.abort()
      }
      return Promise.resolve()
    }
    case 'shutdown': {
      shuttingDown = true
      if (active) {
        if (active.broker) active.broker.fail(new SidecarFailure('canceled', 'the connector shut the sidecar down'))
        active.controller.abort()
      }
      process.exit(0)
    }
    default:
      write({ kind: 'error', code: 'protocol_error', message: 'unknown sidecar request kind' })
      process.exit(1)
  }
}

function onLine(line) {
  if (line.trim() === '') return
  let frame
  try {
    frame = JSON.parse(line)
  } catch {
    write({ kind: 'error', code: 'protocol_error', message: 'malformed sidecar request' })
    process.exit(1)
  }
  // A tool result is the only message that may arrive while a turn is parked, and
  // control frames must abort a parked turn, so they are dispatched immediately
  // instead of waiting behind the running turn.
  if (frame?.kind === 'tool_result' || frame?.kind === 'cancel' || frame?.kind === 'shutdown') {
    handleFrame(frame)
    return
  }
  pending = pending.then(() => handleFrame(frame)).catch(() => {
    write({ kind: 'error', code: 'protocol_error', message: 'sidecar request failed' })
    process.exit(1)
  })
}

let buffer = ''
process.stdin.setEncoding('utf8')
process.stdin.on('data', (chunk) => {
  buffer += chunk
  if (Buffer.byteLength(buffer, 'utf8') > MAX_FRAME_BYTES) {
    write({ kind: 'error', code: 'protocol_error', message: 'sidecar request exceeded the frame bound' })
    process.exit(1)
  }
  let index
  while ((index = buffer.indexOf('\n')) >= 0) {
    const line = buffer.slice(0, index)
    buffer = buffer.slice(index + 1)
    onLine(line)
  }
})
process.stdin.on('end', () => {
  if (active) {
    if (active.broker) active.broker.fail(new SidecarFailure('canceled', 'the connector closed the sidecar input'))
    active.controller.abort()
  }
  process.exit(0)
})

write({
  kind: 'hello',
  protocol: PROTOCOL,
  sdk_version: SDK_VERSION,
  node_version: process.version,
  max_concurrent: MAX_CONCURRENT,
})
