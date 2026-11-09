import { spawn } from 'node:child_process'
import readline from 'node:readline'

const mode = process.env.FAKE_MODE ?? ''
const canary = process.env.ANTHROPIC_API_KEY ?? 'opaque-canary-missing'

function send(frame) {
  process.stdout.write(JSON.stringify(frame) + '\n')
}

let sequence = 0
function emit(requestId, kind, payload = {}) {
  sequence += 1
  send({ kind, request_id: requestId, sequence, ...payload })
}

if (mode === 'stderr') {
  process.stderr.write(`sidecar failed with credential ${canary}\n`)
  process.exit(1)
}
if (mode === 'no_read') {
  // Greet, then never read stdin. A permitted large start write blocks.
  send({ kind: 'hello', protocol: '1', sdk_version: '0.3.316', node_version: process.version })
  setInterval(() => {}, 1000)
} else if (mode === 'no_hello') {
  process.stdout.write(JSON.stringify({ kind: 'ready' }) + '\n')
  setInterval(() => {}, 1000)
} else {
  process.stdout.write(
    JSON.stringify({
      kind: 'hello',
      protocol: '1',
      sdk_version: '0.3.316',
      node_version: process.version,
    }) + '\n',
  )
}

if (mode === 'burst_exit') {
  // Final frames written immediately before a clean exit. Pipe writes are
  // synchronous on POSIX, so every frame is in the pipe when the leader exits.
  for (let i = 0; i < 64; i++) {
    send({ kind: 'burst', text: String(i) })
  }
  process.exit(0)
}

if (mode === 'descendant' || mode === 'descendant_crash') {
  // An SDK-shaped descendant in the sidecar's process group. It ignores stdio
  // and outlives a graceful or crashing parent unless the owner retires the
  // whole group.
  const child = spawn(process.execPath, ['-e', 'setInterval(() => {}, 1000)'], { stdio: 'ignore' })
  send({ kind: 'descendant', text: String(child.pid) })
  if (mode === 'descendant_crash') {
    setTimeout(() => process.exit(7), 50)
  }
}

if (mode === 'noisy_stderr') {
  const chunk = 'x'.repeat(4096)
  let written = 0
  process.stderr.on('error', () => {
    send({ kind: 'noise_error' })
  })
  const pump = () => {
    while (written < 512 * 1024) {
      written += chunk.length
      if (!process.stderr.write(chunk)) {
        process.stderr.once('drain', pump)
        return
      }
    }
    send({ kind: 'noise_done' })
  }
  pump()
}

if (mode === 'no_read') {
  // Keep the event loop alive; stdin is intentionally never resumed.
} else {
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
        if (mode === 'discover_error') {
          send({ kind: 'error', request_id: frame.request_id, code: 'auth_required', message: 'run claude auth login' })
          return
        }
        if (mode === 'discover_secret') {
          send({ kind: 'error', request_id: frame.request_id, code: 'sdk_error', message: `SDK rejected credential ${canary}` })
          return
        }
        send({
          kind: 'models',
          request_id: frame.request_id,
          sequence: 1,
          models: [
            { value: 'sonnet', resolved_model: 'claude-sonnet-5', display_name: 'Sonnet', description: 'test', supported_effort_levels: ['low', 'high'] },
            { value: 'opus', display_name: 'Opus' },
          ],
          cli_version: '2.1.311',
          sdk_version: '0.3.316',
          node_version: process.version,
          account: { subscription_type: 'max', api_provider: 'firstParty' },
        })
        return
      case 'start':
        sequence = 0
        if (mode === 'auth') {
          emit(frame.request_id, 'error', { code: 'authentication_failed', message: 'run claude auth login' })
          return
        }
        if (mode === 'rate_limit') {
          emit(frame.request_id, 'error', { code: 'rate_limited', message: 'subscription rate limit reached' })
          return
        }
        if (mode === 'plan_exhausted') {
          emit(frame.request_id, 'error', { code: 'plan_exhausted', message: 'subscription usage limit reached' })
          return
        }
        if (mode === 'third_party_extra_usage') {
          // The Go bridge must replace this raw detail with its own
          // connector-owned taxonomy summary; the raw text is a privacy canary.
          emit(frame.request_id, 'error', {
            code: 'third_party_extra_usage',
            message: 'raw provider billing detail: Third-party apps now draw from your extra usage',
          })
          return
        }
        if (mode === 'org_policy') {
          emit(frame.request_id, 'error', { code: 'oauth_org_not_allowed', message: 'organisation policy blocks this login' })
          return
        }
        if (mode === 'native_tools_exposed') {
          // The Go bridge must replace this raw detail with its own client-visible
          // taxonomy summary; the raw text is a privacy canary, not client output.
          emit(frame.request_id, 'error', { code: 'native_tools_exposed', message: 'raw sidecar native-tools detail' })
          return
        }
        if (mode === 'mcp_disconnect') {
          emit(frame.request_id, 'error', { code: 'mcp_unavailable', message: 'the transient caller-tool server disconnected' })
          return
        }
        if (mode === 'malformed_frame') {
          emit(frame.request_id, 'response_started')
          process.stdout.write('this is not an ndjson frame\n')
          return
        }
        if (mode === 'fallback_warning') {
          emit(frame.request_id, 'response_started')
          emit(frame.request_id, 'warning', { code: 'model_refusal_fallback', message: 'requested a, ran b' })
          emit(frame.request_id, 'message_started')
          emit(frame.request_id, 'text_delta', { text: 'fallback text' })
          emit(frame.request_id, 'response_finished', { stop_reason: 'end_turn' })
          return
        }
        if (mode === 'superseded') {
          emit(frame.request_id, 'response_started')
          emit(frame.request_id, 'message_started')
          emit(frame.request_id, 'text_delta', { text: 'committed prefix' })
          emit(frame.request_id, 'error', { code: 'superseded_output_committed', message: 'the SDK replaced committed output' })
          return
        }
        if (mode === 'error_secret') {
          emit(frame.request_id, 'error', { code: 'sdk_error', message: `SDK rejected credential ${canary}` })
          return
        }
        if (mode === 'error_prose_credential') {
          emit(frame.request_id, 'error', { code: 'sdk_error', message: 'x'.repeat(450) + canary })
          return
        }
        if (mode === 'error_prompt') {
          emit(frame.request_id, 'error', { code: 'sdk_error', message: `SDK rejected request payload: ${frame.prompt}` })
          return
        }
        if (mode === 'bad_sequence') {
          emit(frame.request_id, 'response_started')
          send({ kind: 'message_started', request_id: frame.request_id, sequence: 1 })
          return
        }
        if (mode === 'exit_early') {
          process.exit(0)
          return
        }
        if (mode === 'exit_partial') {
          emit(frame.request_id, 'response_started')
          emit(frame.request_id, 'message_started')
          emit(frame.request_id, 'text_delta', { text: 'partial' })
          setTimeout(() => process.exit(0), 20)
          return
        }
        if (mode === 'echo_start') {
          emit(frame.request_id, 'response_started')
          emit(frame.request_id, 'message_started')
          emit(frame.request_id, 'text_delta', { text: `effort=${frame.effort ?? ''};model=${frame.model ?? ''}` })
          emit(frame.request_id, 'response_finished', { stop_reason: 'end_turn' })
          return
        }
        if (mode === 'slow') {
          setTimeout(() => {
            emit(frame.request_id, 'response_started')
            emit(frame.request_id, 'message_started')
            emit(frame.request_id, 'text_delta', { text: 'late' })
            emit(frame.request_id, 'response_finished', { stop_reason: 'end_turn' })
          }, 10000)
          return
        }
        emit(frame.request_id, 'response_started')
        emit(frame.request_id, 'message_started')
        if (mode === 'warning_secret') {
          emit(frame.request_id, 'warning', { code: 'sdk_warning', message: `SDK diagnostic included credential ${canary}` })
          emit(frame.request_id, 'text_delta', { text: 'safe' })
          emit(frame.request_id, 'response_finished', { stop_reason: 'end_turn' })
          return
        }
        emit(frame.request_id, 'text_delta', { text: 'hello ' })
        emit(frame.request_id, 'text_delta', { text: 'world' })
        emit(frame.request_id, 'usage', {
          input_tokens: 10,
          output_tokens: 4,
          cache_read_tokens: 2,
        })
        emit(frame.request_id, 'response_finished', { stop_reason: 'end_turn' })
        return
      case 'cancel':
        if (mode === 'stubborn') return
        process.exit(0)
        return
      case 'shutdown':
        if (mode === 'stubborn') return
        process.exit(0)
        return
      default:
        send({ kind: 'error', code: 'protocol_error', message: 'unknown request kind' })
        process.exit(1)
    }
  })
}

if (mode === 'stubborn') {
  setInterval(() => {}, 1000)
}
