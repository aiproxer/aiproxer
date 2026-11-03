import assert from 'node:assert/strict'
import { spawn } from 'node:child_process'
import { once } from 'node:events'
import { fileURLToPath } from 'node:url'
import readline from 'node:readline'
import test from 'node:test'

// Drives the production sidecar entrypoint (bridge.mjs) and its real session /
// broker / MCP glue. Only the external SDK query is replaced by a fixture, so a
// result-release wiring defect cannot hide behind a hand-written protocol fake.

const KNOWN_KINDS = new Set([
  'hello',
  'models',
  'response_started',
  'message_started',
  'text_delta',
  'usage',
  'warning',
  'tool_call',
  'tool_released',
  'response_finished',
  'error',
])

function supportModuleHooks() {
  // registerHooks landed in Node 22.15; newer majors support it too.
  const [major, minor] = process.versions.node.split('.').map(Number)
  return major > 22 || (major === 22 && minor >= 15)
}

function startSidecar(t, extraEnv = {}) {
  const sidecarDir = fileURLToPath(new URL('.', import.meta.url))
  const child = spawn(process.execPath, ['--import', './testdata/sdk-register.mjs', 'bridge.mjs'], {
    cwd: sidecarDir,
    stdio: ['pipe', 'pipe', 'pipe'],
    env: { ...process.env, HOME: sidecarDir, PATH: process.env.PATH ?? '', ...extraEnv },
  })
  t.after(() => {
    child.kill('SIGKILL')
  })

  const frames = []
  const waiters = []
  const lines = readline.createInterface({ input: child.stdout })
  let stderr = ''
  child.stderr.on('data', (chunk) => {
    stderr += chunk.toString()
  })
  lines.on('line', (line) => {
    let frame
    try {
      frame = JSON.parse(line)
    } catch {
      frame = { kind: 'malformed', raw: line }
    }
    frames.push(frame)
    for (const waiter of [...waiters]) {
      if (waiter.match(frame)) {
        waiters.splice(waiters.indexOf(waiter), 1)
        waiter.resolve(frame)
      }
    }
  })
  const send = (frame) => child.stdin.write(JSON.stringify(frame) + '\n')
  const next = (match, timeoutMs = 15_000) =>
    new Promise((resolve, reject) => {
      const existing = frames.find(match)
      if (existing) {
        resolve(existing)
        return
      }
      const waiter = { match, resolve }
      waiters.push(waiter)
      setTimeout(() => {
        const index = waiters.indexOf(waiter)
        if (index >= 0) waiters.splice(index, 1)
        reject(new Error(`timed out waiting for a frame; stderr=${stderr} frames=${JSON.stringify(frames)}`))
      }, timeoutMs).unref()
    })
  return { child, frames, send, next, stderr: () => stderr }
}

function startToolTurn(context) {
  context.send({ kind: 'configure', cli: '/usr/local/bin/claude', workspace: fileURLToPath(new URL('.', import.meta.url)) })
  context.send({
    kind: 'start',
    request_id: 'req-1',
    model: 'sonnet',
    prompt: 'read the file',
    tools: [
      {
        name: 'Read',
        mcp_name: 'Read',
        native_name: 'mcp__aiproxer__Read',
        description: 'read a file',
        parameters: { type: 'object', properties: { file_path: { type: 'string' } } },
      },
    ],
    pending_ttl_ms: 60_000,
  })
}

test('production runTurn releases the deferred tool call exactly once', async (t) => {
  if (!supportModuleHooks()) {
    t.skip('node:module registerHooks requires Node 22.15 or newer')
    return
  }
  const context = startSidecar(t)
  const { child, frames, send, next } = context

  await next((frame) => frame.kind === 'hello')
  startToolTurn(context)

  const parked = await next((frame) => frame.kind === 'response_finished' && frame.awaiting_tool_result === true)
  assert.equal(parked.stop_reason, 'tool_use')
  const toolCall = frames.find((frame) => frame.kind === 'tool_call')
  assert.ok(toolCall, `missing tool_call: ${JSON.stringify(frames)}`)
  assert.equal(toolCall.tool_call_id, 'toolu_fixture')
  assert.equal(toolCall.tool_name, 'Read')
  assert.equal(toolCall.native_name, 'mcp__aiproxer__Read')
  assert.deepEqual(toolCall.arguments, { file_path: 'x' })
  assert.equal(frames.filter((frame) => frame.kind === 'tool_released').length, 0)

  // The released result must arrive on a valid frame kind, and the continuation
  // must complete through the same production query.
  send({ kind: 'tool_result', request_id: 'req-2', tool_call_id: 'toolu_fixture', content: 'contents' })
  const completed = await next((frame) => frame.kind === 'response_finished' && !frame.awaiting_tool_result && frame.request_id === 'req-2')
  assert.equal(completed.stop_reason, 'end_turn')
  const released = frames.filter((frame) => frame.kind === 'tool_released')
  assert.equal(released.length, 1, `expected one release frame: ${JSON.stringify(frames)}`)
  assert.equal(released[0].tool_call_id, 'toolu_fixture')
  const continued = frames.filter((frame) => frame.kind === 'text_delta' && frame.request_id === 'req-2')
  assert.equal(continued.map((frame) => frame.text).join(''), 'got:contents')
  for (const frame of frames) {
    assert.ok(KNOWN_KINDS.has(frame.kind), `invalid frame kind: ${JSON.stringify(frame)}`)
  }
  const unknownKeysInRelease = Object.keys(released[0]).filter((key) => /^\d+$/.test(key))
  assert.deepEqual(unknownKeysInRelease, [])

  send({ kind: 'shutdown' })
  await once(child, 'exit')
})

for (const scenario of ['post_tool_stream', 'post_tool_message']) {
  test(`post-tool text is refused before emission (${scenario})`, async (t) => {
    if (!supportModuleHooks()) {
      t.skip('node:module registerHooks requires Node 22.15 or newer')
      return
    }
    const context = startSidecar(t, { AIP_FIXTURE_SCENARIO: scenario })
    const { child, frames, send, next } = context
    await next((frame) => frame.kind === 'hello')
    startToolTurn(context)

    const failure = await next((frame) => frame.kind === 'error')
    assert.equal(failure.code, 'unsupported_post_tool_text')
    // The post-tool suffix must never reach the canonical stream, and the tool
    // call must not be emitted after it.
    assert.equal(
      frames.filter((frame) => frame.kind === 'text_delta' && String(frame.text).includes('AFTER-TOOL')).length,
      0,
      `post-tool text escaped: ${JSON.stringify(frames)}`,
    )
    assert.equal(frames.filter((frame) => frame.kind === 'tool_call').length, 0)
    assert.equal(frames.filter((frame) => frame.kind === 'response_finished' && frame.awaiting_tool_result).length, 0)
    send({ kind: 'shutdown' })
    await once(child, 'exit')
  })
}
