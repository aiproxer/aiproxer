import { createHash } from 'node:crypto'
import readline from 'node:readline'

// SDK-shaped fake sidecar for the deferred tool bridge. It speaks the private
// NDJSON protocol and parks exactly like the real pinned SDK sidecar after a
// committed tool call; it never executes a tool.

const mode = process.env.FAKE_MODE ?? ''

process.stdout.write(
  JSON.stringify({
    kind: 'hello',
    protocol: '1',
    sdk_version: '0.3.316',
    node_version: process.version,
  }) + '\n',
)

function send(frame) {
  process.stdout.write(JSON.stringify(frame) + '\n')
}

let sequence = 0
function emit(requestId, kind, payload = {}) {
  sequence += 1
  send({ kind, request_id: requestId, sequence, ...payload })
}

let parked = null
let emittedSecond = false

const rl = readline.createInterface({ input: process.stdin, crlfDelay: Infinity })
rl.on('line', (line) => {
  let frame
  try {
    frame = JSON.parse(line)
  } catch {
    process.exit(1)
  }
  switch (frame.kind) {
    case 'configure':
      return
    case 'discover':
      send({
        kind: 'models',
        request_id: frame.request_id,
        sequence: 1,
        models: [
          { value: 'sonnet', resolved_model: 'claude-sonnet-5', display_name: 'Sonnet', description: 'test' },
          { value: 'opus', display_name: 'Opus' },
        ],
        cli_version: '2.1.311',
        sdk_version: '0.3.316',
        node_version: process.version,
      })
      return
    case 'start': {
      sequence = 0
      const tools = Array.isArray(frame.tools) ? frame.tools : []
      if (mode === 'echo_prompt') {
        emit(frame.request_id, 'response_started')
        emit(frame.request_id, 'message_started')
        emit(frame.request_id, 'text_delta', { text: frame.prompt })
        emit(frame.request_id, 'response_finished', { stop_reason: 'end_turn' })
        return
      }
      if (mode === 'native_tool') {
        emit(frame.request_id, 'response_started')
        emit(frame.request_id, 'error', { code: 'native_tool_use', message: 'the SDK attempted a native tool call' })
        return
      }
      if (mode === 'post_tool_text') {
        emit(frame.request_id, 'response_started')
        emit(frame.request_id, 'error', {
          code: 'unsupported_post_tool_text',
          message: 'raw sidecar detail that must not reach the client',
        })
        return
      }
      if (tools.length === 0) {
        emit(frame.request_id, 'response_started')
        emit(frame.request_id, 'message_started')
        emit(frame.request_id, 'text_delta', { text: 'hello ' })
        emit(frame.request_id, 'text_delta', { text: 'world' })
        emit(frame.request_id, 'usage', { input_tokens: 10, output_tokens: 4, cache_read_tokens: 2 })
        emit(frame.request_id, 'response_finished', { stop_reason: 'end_turn' })
        return
      }
      emit(frame.request_id, 'response_started')
      emit(frame.request_id, 'message_started')
      if (mode === 'slow') return
      emit(frame.request_id, 'text_delta', { text: 'let me look' })
      const tool = tools[0]
      const args = mode === 'empty_args' ? {} : { file_path: 'x' }
      const unique =
        mode === 'unique_ids'
          ? 'toolu_' + createHash('sha256').update(frame.request_id + ':' + process.pid + ':' + Date.now()).digest('hex').slice(0, 24)
          : ''
      const id = unique || (mode === 'empty_args' ? 'toolu_01EMPTY' : 'toolu_01ABC')
      emit(frame.request_id, 'tool_call', {
        tool_call_id: id,
        native_name: tool.native_name,
        tool_name: tool.name,
        arguments: args,
      })
      if (mode === 'parallel') {
        emit(frame.request_id, 'tool_call', {
          tool_call_id: 'toolu_02SECOND',
          native_name: tool.native_name,
          tool_name: tool.name,
          arguments: { file_path: 'y' },
        })
      }
      if (mode === 'parallel') {
        emit(frame.request_id, 'response_finished', { stop_reason: 'tool_use', awaiting_tool_result: true })
        return
      }
      if (mode === 'flood_park') {
        // Six events precede the terminal; filling the rest of the 16-slot
        // response queue makes the terminal emit block.
        for (let i = 0; i < 10; i += 1) emit(frame.request_id, 'text_delta', { text: 'x' })
      }
      parked = { requestId: frame.request_id, callID: id, tools }
      emit(frame.request_id, 'response_finished', { stop_reason: 'tool_use', awaiting_tool_result: true })
      if (mode === 'die_after_park') setTimeout(() => process.exit(0), 20)
      return
    }
    case 'tool_result': {
      if (!parked) {
        emit(frame.request_id, 'error', { code: 'tool_correlation_failed', message: 'no parked native turn is waiting' })
        return
      }
      if (frame.tool_call_id !== parked.callID) {
        emit(frame.request_id, 'error', { code: 'tool_correlation_failed', message: 'the released tool call does not match the pending native id' })
        return
      }
      sequence = 0
      emit(frame.request_id, 'tool_released', { tool_call_id: parked.callID })
      if (mode === 'stall_after_result') return
      emit(frame.request_id, 'response_started')
      emit(frame.request_id, 'message_started')
      emit(frame.request_id, 'text_delta', { text: 'got:' + frame.content })
      if (mode === 'second_tool' && !emittedSecond) {
        emittedSecond = true
        const tool = parked.tools[1] ?? parked.tools[0]
        emit(frame.request_id, 'tool_call', {
          tool_call_id: 'toolu_02SECOND',
          native_name: tool.native_name,
          tool_name: tool.name,
          arguments: { file_path: 'z' },
        })
        parked = { requestId: frame.request_id, callID: 'toolu_02SECOND', tools: parked.tools }
        emit(frame.request_id, 'response_finished', { stop_reason: 'tool_use', awaiting_tool_result: true })
        return
      }
      emit(frame.request_id, 'usage', { input_tokens: 20, output_tokens: 3 })
      emit(frame.request_id, 'response_finished', { stop_reason: 'end_turn' })
      parked = null
      return
    }
    case 'cancel':
    case 'shutdown':
      process.exit(0)
      return
    default:
      send({ kind: 'error', code: 'protocol_error', message: 'unknown request kind' })
      process.exit(1)
  }
})
