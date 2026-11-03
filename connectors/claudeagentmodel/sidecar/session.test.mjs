import assert from 'node:assert/strict'
import test from 'node:test'
import {
  SidecarFailure,
  buildOptions,
  classifyError,
  createTurnState,
  mapSdkMessage,
  resetResponse,
} from './session.mjs'
import { buildToolCatalog } from './tool-server.mjs'

function collect(message, state = createTurnState(), catalog = undefined) {
  const events = []
  mapSdkMessage(message, state, (kind, payload) => events.push({ kind, ...(payload ?? {}) }), catalog)
  return { events, state }
}

test('text stream starts once and emits ordered deltas', () => {
  const { events } = collect({
    type: 'stream_event',
    event: { type: 'message_start', message: { model: 'claude-sonnet-5' } },
  })
  assert.deepEqual(events.map((event) => event.kind), ['response_started', 'message_started'])
})

test('text delta requires a started message and preserves exact text', () => {
  const state = createTurnState()
  const events = []
  const emit = (kind, payload) => events.push({ kind, ...(payload ?? {}) })
  mapSdkMessage({ type: 'stream_event', event: { type: 'message_start', message: {} } }, state, emit)
  mapSdkMessage(
    { type: 'stream_event', event: { type: 'content_block_delta', delta: { type: 'text_delta', text: ' hello\n' } } },
    state,
    emit,
  )
  assert.deepEqual(events.map((event) => event.kind), ['response_started', 'message_started', 'text_delta'])
  assert.equal(events[2].text, ' hello\n')
})

test('result emits explicit usage presence and a terminal', () => {
  const { events } = collect({
    type: 'result',
    subtype: 'success',
    stop_reason: 'end_turn',
    usage: {
      input_tokens: 11,
      output_tokens: 7,
      cache_read_input_tokens: 3,
      cache_creation_input_tokens: 0,
    },
  })
  assert.deepEqual(events.map((event) => event.kind), [
    'response_started',
    'message_started',
    'usage',
    'response_finished',
  ])
  assert.equal(events[2].input_tokens, 11)
  assert.equal(events[2].output_tokens, 7)
  assert.equal(events[2].cache_read_tokens, 3)
  assert.equal(events[2].cache_write_tokens, 0)
  assert.equal(events[3].stop_reason, 'end_turn')
})

test('native tools exposed by init fail closed', () => {
  assert.throws(
    () => collect({ type: 'system', subtype: 'init', tools: ['Bash'], mcp_servers: [] }),
    (err) => err instanceof SidecarFailure && err.code === 'native_tools_exposed',
  )
})

test('the connector-owned MCP tools listed by init are admitted', () => {
  const catalog = buildToolCatalog([
    {
      name: 'lookup_chat',
      mcp_name: 'lookup_chat',
      native_name: 'mcp__aiproxer__lookup_chat',
      description: 'lookup',
      parameters: { type: 'object', properties: {} },
    },
  ])
  const { events } = collect(
    { type: 'system', subtype: 'init', tools: ['mcp__aiproxer__lookup_chat'], mcp_servers: [{ name: 'aiproxer', status: 'connected' }] },
    createTurnState(),
    catalog,
  )
  assert.deepEqual(events, [])
})

test('a tool-use-only assistant message starts the response before the tool call', () => {
  const catalog = buildToolCatalog([
    {
      name: 'shell',
      mcp_name: 'shell',
      native_name: 'mcp__aiproxer__shell',
      description: 'run a command',
      parameters: { type: 'object', properties: { command: { type: 'string' } } },
    },
  ])
  const state = createTurnState()
  const events = []
  mapSdkMessage(
    {
      type: 'assistant',
      message: {
        model: 'claude-haiku-5-5',
        content: [{ type: 'tool_use', id: 'toolu_1', name: 'mcp__aiproxer__shell', input: { command: 'python3 analyze.py' } }],
      },
    },
    state,
    (kind, payload) => events.push({ kind, ...(payload ?? {}) }),
    catalog,
  )
  assert.deepEqual(events.map((event) => event.kind), ['response_started', 'message_started'])
  assert.equal(state.messageHasToolUse, true)
  assert.equal(state.toolBlocks.get('toolu_1'), 'mcp__aiproxer__shell')
})

test('a foreign tool name listed by init still fails closed when tools are served', () => {
  const catalog = buildToolCatalog([
    {
      name: 'lookup_chat',
      mcp_name: 'lookup_chat',
      native_name: 'mcp__aiproxer__lookup_chat',
      description: 'lookup',
      parameters: { type: 'object', properties: {} },
    },
  ])
  for (const tool of ['Bash', 'mcp__other__lookup_chat']) {
    assert.throws(
      () => collect({ type: 'system', subtype: 'init', tools: [tool], mcp_servers: [{ name: 'aiproxer', status: 'connected' }] }, createTurnState(), catalog),
      (err) => err instanceof SidecarFailure && err.code === 'native_tools_exposed',
    )
  }
})

test('native mcp servers exposed by init fail closed', () => {
  assert.throws(
    () => collect({ type: 'system', subtype: 'init', tools: [], mcp_servers: [{ name: 'x' }] }),
    (err) => err instanceof SidecarFailure && err.code === 'native_mcp_exposed',
  )
})

test('fallback after committed text is a terminal protocol failure', () => {
  const state = createTurnState()
  const events = []
  const emit = (kind, payload) => events.push({ kind, ...(payload ?? {}) })
  mapSdkMessage({ type: 'stream_event', event: { type: 'message_start', message: {} } }, state, emit)
  mapSdkMessage(
    { type: 'stream_event', event: { type: 'content_block_delta', delta: { type: 'text_delta', text: 'partial' } } },
    state,
    emit,
  )
  assert.throws(
    () =>
      mapSdkMessage(
        {
          type: 'system',
          subtype: 'model_refusal_fallback',
          original_model: 'a',
          fallback_model: 'b',
          retracted_message_uuids: ['m1'],
        },
        state,
        emit,
      ),
    (err) => err instanceof SidecarFailure && err.code === 'superseded_output_committed',
  )
})

test('fallback before text is an explicit warning', () => {
  const { events } = collect({
    type: 'system',
    subtype: 'model_refusal_fallback',
    original_model: 'a',
    fallback_model: 'b',
  })
  assert.deepEqual(events.map((event) => event.kind), ['response_started', 'message_started', 'warning'])
  assert.equal(events[2].code, 'model_refusal_fallback')
})

test('fallback warning carries bounded requested and effective model identities', () => {
  const state = createTurnState('requested-model')
  const events = []
  mapSdkMessage(
    { type: 'system', subtype: 'model_refusal_fallback', fallback_model: 'effective-model' },
    state,
    (kind, payload) => events.push({ kind, ...(payload ?? {}) }),
  )
  assert.equal(events[2].code, 'model_refusal_fallback')
  assert.match(events[2].message, /requested-model/)
  assert.match(events[2].message, /effective-model/)
  assert.equal(state.effectiveModel, 'effective-model')
})

test('sdk supersedes after committed text fails before a success terminal', () => {
  const state = createTurnState('requested-model')
  const events = []
  const emit = (kind, payload) => events.push({ kind, ...(payload ?? {}) })
  mapSdkMessage({ type: 'stream_event', event: { type: 'message_start', message: {} } }, state, emit)
  mapSdkMessage(
    { type: 'stream_event', event: { type: 'content_block_delta', delta: { type: 'text_delta', text: 'partial' } } },
    state,
    emit,
  )
  assert.throws(
    () =>
      mapSdkMessage(
        {
          type: 'assistant',
          supersedes: ['m1'],
          message: { model: 'claude-sonnet-5', content: [{ type: 'text', text: 'replacement' }] },
        },
        state,
        emit,
      ),
    (err) => err instanceof SidecarFailure && err.code === 'superseded_output_committed',
  )
  assert.deepEqual(events.map((event) => event.kind), ['response_started', 'message_started', 'text_delta'])
  assert.equal(events[2].text, 'partial')
})

test('sdk supersedes before any committed text maps the replacement once', () => {
  const { events } = collect({
    type: 'assistant',
    supersedes: ['m1'],
    message: { model: 'claude-sonnet-5', content: [{ type: 'text', text: 'replacement' }] },
  })
  assert.deepEqual(events.map((event) => event.kind), ['response_started', 'message_started', 'text_delta'])
  assert.equal(events[2].text, 'replacement')
})

test('validated effort is forwarded as a supported sdk option', () => {
  const options = buildOptions(
    { cli: '/usr/local/bin/claude', workspace: '/tmp', env: {}, effort: 'high' },
    new AbortController(),
  )
  assert.equal(options.effort, 'high')
  const without = buildOptions(
    { cli: '/usr/local/bin/claude', workspace: '/tmp', env: {}, effort: 'unsupported' },
    new AbortController(),
  )
  assert.equal(without.effort, undefined)
})

test('rejected subscription limit is plan_exhausted', () => {
  assert.throws(
    () => collect({ type: 'rate_limit_event', rate_limit_info: { status: 'rejected' } }),
    (err) => err instanceof SidecarFailure && err.code === 'plan_exhausted',
  )
})

test('an allowed primary window with disabled overage does not fail the turn', () => {
  // Exact live SDK 0.3.316 frame on the owner's login: the five-hour window is
  // allowed and only overage is rejected because the organisation disabled it.
  const { events, state } = collect({
    type: 'rate_limit_event',
    rate_limit_info: {
      status: 'allowed',
      rateLimitType: 'five_hour',
      overageStatus: 'rejected',
      overageDisabledReason: 'org_level_disabled',
      isUsingOverage: false,
    },
  })
  assert.deepEqual(events, [])
  assert.deepEqual(state.pendingWarnings, [])
})

test('disabled overage never overrides an allowed warning', () => {
  const { state } = collect({
    type: 'rate_limit_event',
    rate_limit_info: { status: 'allowed_warning', overageStatus: 'rejected', isUsingOverage: false },
  })
  assert.deepEqual(
    state.pendingWarnings.map((warning) => warning.code),
    ['rate_limit_warning'],
  )
})

test('a rate-limit assistant error is rate_limited, not plan exhaustion', () => {
  assert.throws(
    () => collect({ type: 'assistant', error: 'rate_limit', message: { model: 'x', content: [] } }),
    (err) => err instanceof SidecarFailure && err.code === 'rate_limited',
  )
})

test('an overloaded provider is unavailable, not plan exhaustion', () => {
  assert.throws(
    () => collect({ type: 'assistant', error: 'overloaded', message: { model: 'x', content: [] } }),
    (err) => err instanceof SidecarFailure && err.code === 'unavailable',
  )
})

test('a billing error is never reported as plan exhaustion', () => {
  assert.throws(
    () => collect({ type: 'assistant', error: 'billing_error', message: { model: 'x', content: [] } }),
    (err) => err instanceof SidecarFailure && err.code === 'sdk_error',
  )
})

const thirdPartyExtraUsageText =
  'API Error: 400 Third-party apps now draw from your extra usage, not your plan limits. Add more at claude.ai/settings/usage and keep going.'

function syntheticAssistantFrame(text, model = '<synthetic>') {
  return {
    type: 'assistant',
    error: 'unknown',
    message: {
      model,
      stop_reason: 'stop_sequence',
      content: [{ type: 'text', text }],
    },
  }
}

test('the exact synthetic third-party extra-usage rejection is a typed permission failure', () => {
  assert.throws(
    () => collect(syntheticAssistantFrame(thirdPartyExtraUsageText)),
    (err) =>
      err instanceof SidecarFailure &&
      err.code === 'third_party_extra_usage' &&
      err.message ===
        'Claude requires third-party extra usage for this request; add extra usage at claude.ai/settings/usage',
  )
})

test('near-miss third-party billing prose stays the generic sdk_error', () => {
  for (const frame of [
    syntheticAssistantFrame(`${thirdPartyExtraUsageText} `),
    syntheticAssistantFrame(thirdPartyExtraUsageText, 'claude-sonnet-5'),
    {
      ...syntheticAssistantFrame(thirdPartyExtraUsageText),
      error: 'billing_error',
    },
  ]) {
    assert.throws(
      () => collect(frame),
      (err) => err instanceof SidecarFailure && err.code === 'sdk_error',
    )
  }
})

test('untyped rate limits and quotas are rate_limited, usage limits are plan_exhausted', () => {
  assert.equal(classifyError(new Error('429 rate limit exceeded')), 'rate_limited')
  assert.equal(classifyError(new Error('provider quota exceeded')), 'rate_limited')
  assert.equal(classifyError(new Error('subscription usage limit reached')), 'plan_exhausted')
})

test('authentication failure is typed', () => {
  assert.throws(
    () => collect({ type: 'assistant', error: 'authentication_failed', message: { model: 'x', content: [] } }),
    (err) => err instanceof SidecarFailure && err.code === 'authentication_failed',
  )
})

test('a completed assistant message supplies text when partial deltas are absent', () => {
  const { events } = collect({
    type: 'assistant',
    message: {
      model: 'claude-sonnet-5',
      content: [
        { type: 'text', text: 'complete ' },
        { type: 'text', text: 'text' },
      ],
    },
  })
  assert.deepEqual(events.map((event) => event.kind), ['response_started', 'message_started', 'text_delta', 'text_delta'])
  assert.equal(events[2].text + events[3].text, 'complete text')
})

test('options are isolated and connector-owned', () => {
  const options = buildOptions(
    { cli: '/usr/local/bin/claude', workspace: '/tmp', env: { PATH: '/usr/bin' } },
    new AbortController(),
  )
  assert.deepEqual(options.settingSources, [])
  assert.deepEqual(options.settings, { disableAllHooks: true })
  assert.equal(options.strictMcpConfig, true)
  assert.deepEqual(options.tools, [])
  assert.deepEqual(options.allowedTools, [])
  assert.deepEqual(options.mcpServers, {})
  assert.equal(options.permissionMode, 'dontAsk')
  assert.equal(options.verbatimPrompts, true)
  assert.deepEqual(options.env, { PATH: '/usr/bin' })
  assert.equal(options.pathToClaudeCodeExecutable, '/usr/local/bin/claude')
  assert.deepEqual(options.systemPrompt, { type: 'preset', preset: 'claude_code', snapshot: false })
})

test('sidecar failures never forward raw sdk prose or truncated credential fragments', () => {
  const secret = 'OpaquePrivateCredentialThatMustNotEscape'.repeat(3)
  const failure = new SidecarFailure('sdk_error', 'x'.repeat(450) + secret)
  assert.equal(failure.code, 'sdk_error')
  assert.equal(failure.message, 'the Claude Agent SDK reported a failure')
  assert.ok(!failure.message.includes(secret.slice(0, 20)))
})

test('known sidecar failure codes map to distinct client-safe summaries', () => {
  const secret = 'OpaquePrivateCredentialThatMustNotEscape'
  const auth = new SidecarFailure('authentication_failed', `prompt payload ${secret}`)
  assert.match(auth.message, /authentication/i)
  assert.ok(!auth.message.includes(secret))
  const unknown = new SidecarFailure('not_a_known_code', secret)
  assert.equal(unknown.code, 'not_a_known_code')
  assert.equal(unknown.message, 'the Claude Agent SDK reported a failure')
})

test('unknown informational messages are ignored', () => {
  const { events } = collect({ type: 'status', subtype: 'compacting' })
  assert.deepEqual(events, [])
})

test('an admitted native tool_use block is not a leak', () => {
  const catalog = {
    tools: [],
    byNative: new Map([['mcp__aiproxer__Read', { name: 'Read', mcpName: 'Read', nativeName: 'mcp__aiproxer__Read' }]]),
    byMCP: new Map(),
  }
  const state = createTurnState()
  const events = []
  const emit = (kind, payload) => events.push({ kind, ...(payload ?? {}) })
  mapSdkMessage(
    {
      type: 'assistant',
      message: {
        model: 'claude-sonnet-5',
        content: [
          { type: 'text', text: 'thinking...' },
          { type: 'tool_use', id: 'toolu_01', name: 'mcp__aiproxer__Read', input: { file_path: 'x' } },
        ],
      },
    },
    state,
    emit,
    catalog,
  )
  assert.deepEqual(events.map((event) => event.kind), ['response_started', 'message_started', 'text_delta'])
  assert.equal(state.toolBlocks.get('toolu_01'), 'mcp__aiproxer__Read')
})

test('a tool_use outside the admitted catalogue fails closed', () => {
  const state = createTurnState()
  assert.throws(
    () =>
      mapSdkMessage(
        { type: 'assistant', message: { model: 'x', content: [{ type: 'tool_use', id: 'toolu_02', name: 'Bash', input: {} }] } },
        state,
        () => {},
      ),
    (err) => err instanceof SidecarFailure && err.code === 'native_tool_use',
  )
})

test('a started tool_use content block outside the catalogue fails closed', () => {
  const state = createTurnState()
  assert.throws(
    () =>
      mapSdkMessage(
        { type: 'stream_event', event: { type: 'content_block_start', content_block: { type: 'tool_use', id: 'toolu_03', name: 'Read' } } },
        state,
        () => {},
      ),
    (err) => err instanceof SidecarFailure && err.code === 'native_tool_use',
  )
})

test('the connector-owned MCP server in init is accepted', () => {
  const { events } = collect({ type: 'system', subtype: 'init', tools: [], mcp_servers: [{ name: 'aiproxer', status: 'connected' }] })
  assert.deepEqual(events, [])
})

test('a query that ends with an outstanding tool call fails closed', () => {
  assert.throws(
    () => collect({ type: 'result', subtype: 'success', stop_reason: 'tool_use', usage: {} }),
    (err) => err instanceof SidecarFailure && err.code === 'tool_correlation_failed',
  )
})


test('a released tool result starts a new canonical response', () => {
  const state = createTurnState()
  const events = []
  const emit = (kind, payload) => events.push({ kind, ...(payload ?? {}) })
  mapSdkMessage({ type: 'stream_event', event: { type: 'message_start', message: {} } }, state, emit)
  mapSdkMessage(
    { type: 'stream_event', event: { type: 'content_block_delta', delta: { type: 'text_delta', text: 'before' } } },
    state,
    emit,
  )
  resetResponse(state)
  mapSdkMessage({ type: 'stream_event', event: { type: 'message_start', message: {} } }, state, emit)
  mapSdkMessage(
    { type: 'stream_event', event: { type: 'content_block_delta', delta: { type: 'text_delta', text: 'after' } } },
    state,
    emit,
  )
  assert.deepEqual(events.map((event) => event.kind), [
    'response_started',
    'message_started',
    'text_delta',
    'response_started',
    'message_started',
    'text_delta',
  ])
  assert.equal(events[5].text, 'after')
})


test('a failed transient tool server fails closed', () => {
  const catalog = {
    tools: [{ name: 'Read', mcpName: 'Read', nativeName: 'mcp__aiproxer__Read' }],
    byNative: new Map(),
    byMCP: new Map(),
  }
  assert.throws(
    () => collect({ type: 'system', subtype: 'init', tools: [], mcp_servers: [{ name: 'aiproxer', status: 'failed' }] }, createTurnState(), catalog),
    (err) => err instanceof SidecarFailure && err.code === 'mcp_unavailable',
  )
})

test('a missing transient tool server fails closed when tools were served', () => {
  const catalog = {
    tools: [{ name: 'Read', mcpName: 'Read', nativeName: 'mcp__aiproxer__Read' }],
    byNative: new Map(),
    byMCP: new Map(),
  }
  assert.throws(
    () => collect({ type: 'system', subtype: 'init', tools: [], mcp_servers: [] }, createTurnState(), catalog),
    (err) => err instanceof SidecarFailure && err.code === 'mcp_unavailable',
  )
})
