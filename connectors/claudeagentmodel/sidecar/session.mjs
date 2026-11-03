import { readFileSync } from 'node:fs'
import { query } from '@anthropic-ai/claude-agent-sdk'
import { MCP_PREFIX, ToolBroker, ToolFailure, buildToolCatalog, createToolServer } from './tool-server.mjs'

const packageJSON = JSON.parse(readFileSync(new URL('./package.json', import.meta.url), 'utf8'))
export const SDK_VERSION = packageJSON.dependencies['@anthropic-ai/claude-agent-sdk']
const MCP_SERVER_NAME = 'aiproxer'

// Client-visible failure summaries are connector-owned constants. Raw SDK
// exception/error prose is never forwarded: it can contain prompts, request
// payloads, credentials, or fragments cut by an earlier truncation, and a
// later exact-match redactor cannot repair a lossy boundary.
const SAFE_FAILURE_MESSAGES = Object.freeze({
  authentication_failed: 'Claude authentication failed; run claude auth login and retry',
  auth_required: 'Claude authentication is required; run claude auth login and retry',
  oauth_org_not_allowed: 'the Claude organisation does not allow this login',
  account_on_hold: 'the Claude account is on hold',
  verification_required: 'the Claude account requires verification',
  cloud_credential_error: 'the Claude cloud credential could not be used',
  rate_limited: 'the Claude subscription rate limit was reached',
  plan_exhausted: 'the Claude subscription usage limit was reached',
  third_party_extra_usage:
    'Claude requires third-party extra usage for this request; add extra usage at claude.ai/settings/usage',
  permission_denied: 'the Claude Agent SDK denied the request',
  model_not_found: 'the requested model is not available in the current runtime inventory',
  invalid_request: 'the Claude Agent SDK rejected the request',
  max_output_tokens: 'the model output reached its token limit',
  max_turns: 'the Claude Agent SDK reached its turn limit',
  structured_output_unsupported: 'structured output is not supported by this connector slice',
  sdk_error: 'the Claude Agent SDK reported a failure',
  discovery_empty: 'the Claude Agent SDK reported no models',
  identity_unavailable: 'the Claude Agent SDK did not report its runtime identity',
  not_configured: 'the sidecar was not configured',
  invalid_configuration: 'the sidecar configuration was invalid',
  busy: 'the sidecar already has an active request',
  protocol_error: 'the sidecar protocol failed',
  unavailable: 'the Claude Agent SDK is unavailable',
  sdk_unavailable: 'the Claude Agent SDK is unavailable',
  native_tool_use: 'the SDK attempted a tool call that the request did not admit',
  native_tools_exposed: 'the SDK exposed tools outside the admitted caller catalogue',
  native_mcp_exposed: 'the SDK exposed an MCP server that the connector did not configure',
  mcp_unavailable: 'the transient caller-tool server is unavailable',
  tool_correlation_failed: 'the SDK tool call did not match the committed caller tool call',
  unsupported_parallel_tool_calls: 'a second outstanding native tool call is not supported',
  tool_abandoned: 'an admitted tool call never reached the connector',
  canceled: 'the tool call was canceled',
  superseded_output_committed: 'the SDK replaced assistant output after it was committed to the client',
  unsupported_post_tool_text: 'the model returned text after a tool_use block in one step; this connector cannot preserve that order',
  empty_response: 'the SDK completed without producing a response',
})

function safeFailureMessage(code) {
  return SAFE_FAILURE_MESSAGES[code] ?? SAFE_FAILURE_MESSAGES.sdk_error
}

// The SDK reports some provider decisions as a synthetic assistant message with
// error "unknown". Recognize only the exact observed third-party extra-usage
// rejection so unknown billing prose keeps the generic typed failure.
const THIRD_PARTY_EXTRA_USAGE_REJECTION =
  'API Error: 400 Third-party apps now draw from your extra usage, not your plan limits. Add more at claude.ai/settings/usage and keep going.'

export class SidecarFailure extends Error {
  constructor(code, _detail) {
    super(safeFailureMessage(code))
    this.name = 'SidecarFailure'
    this.code = code
  }
}

const EFFORT_LEVELS = new Set(['low', 'medium', 'high', 'xhigh', 'max'])

export function buildOptions(config, abortController, extras = {}) {
  const options = {
    abortController,
    pathToClaudeCodeExecutable: config.cli,
    cwd: config.workspace,
    settingSources: [],
    settings: { disableAllHooks: true },
    strictMcpConfig: true,
    skills: [],
    tools: [],
    allowedTools: extras.servedTools ? [`${MCP_PREFIX}*`] : [],
    disallowedTools: [],
    permissionMode: 'dontAsk',
    includePartialMessages: true,
    verbatimPrompts: true,
    env: config.env,
    mcpServers: extras.mcpServers ?? {},
  }
  if (extras.hooks) options.hooks = extras.hooks
  if (config.model) options.model = config.model
  // Keep Claude Code's first-party system prompt and append the client's
  // instructions. Replacing it with a custom string makes subscription
  // inference ineligible as third-party application usage.
  options.systemPrompt = { type: 'preset', preset: 'claude_code', snapshot: false }
  if (typeof config.system === 'string' && config.system !== '') options.systemPrompt.append = config.system
  if (typeof config.effort === 'string' && EFFORT_LEVELS.has(config.effort)) options.effort = config.effort
  return options
}

async function* oneMessage(text) {
  yield {
    type: 'user',
    message: { role: 'user', content: text },
    parent_tool_use_id: null,
  }
}

// A pending prompt generator keeps discovery from starting a user turn while
// still allowing the SDK to initialize and report its model catalog.
async function* pendingPrompt() {
  await new Promise(() => {})
}

function safeAccount(account) {
  if (!account || typeof account !== 'object') return undefined
  const safe = {}
  if (typeof account.subscriptionType === 'string') safe.subscription_type = account.subscriptionType
  if (typeof account.apiProvider === 'string') safe.api_provider = account.apiProvider
  return Object.keys(safe).length > 0 ? safe : undefined
}

export async function discoverModels(config, abortController) {
  const q = query({ prompt: pendingPrompt(), options: buildOptions(config, abortController) })
  try {
    const init = await q.initializationResult()
    const models = (Array.isArray(init.models) ? init.models : [])
      .filter((model) => model && typeof model.value === 'string' && model.value.trim() !== '')
      .map((model) => {
        const row = {
          value: model.value,
          display_name: typeof model.displayName === 'string' ? model.displayName : '',
          description: typeof model.description === 'string' ? model.description : '',
        }
        if (typeof model.resolvedModel === 'string' && model.resolvedModel !== '') row.resolved_model = model.resolvedModel
        if (Array.isArray(model.supportedEffortLevels)) row.supported_effort_levels = model.supportedEffortLevels
        return row
      })
    if (models.length === 0) throw new SidecarFailure('discovery_empty', 'the SDK reported no models')
    if (typeof init.claude_code_version !== 'string' || init.claude_code_version === '') {
      throw new SidecarFailure('identity_unavailable', 'the SDK did not report its Claude Code version')
    }
    return {
      models,
      cli_version: init.claude_code_version,
      account: safeAccount(init.account),
    }
  } catch (err) {
    if (err instanceof SidecarFailure) throw err
    if (abortController.signal.aborted) return undefined
    throw new SidecarFailure(classifyError(err), err?.message ?? String(err))
  } finally {
    q.close()
  }
}

export function createTurnState(requestedModel) {
  return {
    requestedModel: typeof requestedModel === 'string' ? requestedModel : '',
    started: false,
    messageStarted: false,
    textStreamed: false,
    deltaText: false,
    effectiveModel: '',
    cliVersion: '',
    builtinTools: undefined,
    pendingWarnings: [],
    stopReason: '',
    toolBlocks: new Map(),
    // True once the current assistant message has started its tool_use block.
    // Text observed after that point would have to be emitted before the
    // canonical tool call, which reorders model-authored content.
    messageHasToolUse: false,
  }
}

// resetResponse starts a new canonical HTTP response on the same native turn.
// The native query stays parked between the two responses, so only the
// per-response bookkeeping is cleared.
export function resetResponse(state) {
  state.started = false
  state.messageStarted = false
  state.deltaText = false
  state.toolBlocks = new Map()
  state.messageHasToolUse = false
  state.pendingWarnings = []
  state.stopReason = ''
}

function ensureStarted(state, emit) {
  if (!state.started) {
    emit('response_started', {})
    emit('message_started', {})
    state.started = true
    state.messageStarted = true
    for (const warning of state.pendingWarnings) emit('warning', warning)
    state.pendingWarnings = []
  }
}

function warn(state, emit, code, message) {
  const warning = { code, message: safeMessage(message) }
  if (state.started) emit('warning', warning)
  else state.pendingWarnings.push(warning)
}

function emitText(state, emit, text) {
  if (!text) return
  ensureStarted(state, emit)
  emit('text_delta', { text })
  state.textStreamed = true
}

function emitUsage(state, emit, usage) {
  if (!usage || typeof usage !== 'object') return
  const frame = {}
  for (const [source, target] of [
    ['input_tokens', 'input_tokens'],
    ['output_tokens', 'output_tokens'],
    ['cache_read_input_tokens', 'cache_read_tokens'],
    ['cache_creation_input_tokens', 'cache_write_tokens'],
  ]) {
    if (typeof usage[source] === 'number') frame[target] = usage[source]
  }
  if (Object.keys(frame).length > 0) emit('usage', frame)
}

function assertAdmittedTool(state, catalog, name) {
  if (!catalog.byNative.has(name)) {
    throw new SidecarFailure('native_tool_use', 'the SDK attempted a tool call that the connector did not admit')
  }
}

function mapAssistantContent(state, catalog, emit, content) {
  if (!Array.isArray(content)) return
  let seenToolUse = false
  for (const block of content) {
    if (!block || typeof block !== 'object') continue
    if (block.type === 'text') {
      if (seenToolUse) {
        // The canonical tool call is only emitted from the later MCP handler, so
        // emitting this text now would move it before the call and corrupt the
        // client-visible order and continuation prefix.
        throw new SidecarFailure('unsupported_post_tool_text', 'the assistant message carried text after a tool_use block')
      }
      if (!state.deltaText) emitText(state, emit, block.text)
    } else if (block.type === 'tool_use') {
      seenToolUse = true
      // A tool-use-only assistant message has no text/message_start to open the
      // response. The canonical tool call still needs response_started before
      // the MCP handler emits its terminal response_finished frame.
      ensureStarted(state, emit)
      state.messageHasToolUse = true
      assertAdmittedTool(state, catalog, block.name)
      state.toolBlocks.set(block.id, block.name)
    } else if (block.type === 'thinking' || block.type === 'redacted_thinking') {
      warn(state, emit, 'reasoning_not_supported', 'the model returned reasoning content that this connector slice does not expose')
    }
  }
}

export function mapSdkMessage(message, state, emit, catalog = buildToolCatalog(undefined)) {
  if (!message || typeof message !== 'object') return
  switch (message.type) {
    case 'system':
      mapSystemMessage(message, catalog, state, emit)
      return
    case 'stream_event':
      mapStreamEvent(message, catalog, state, emit)
      return
    case 'assistant': {
      const superseded = Array.isArray(message.supersedes) && message.supersedes.length > 0
      if (superseded && state.textStreamed) {
        // The SDK frame replaces previously delivered assistant messages. This
        // stream cannot retract text already sent to the client, so a successful
        // terminal must never certify the stale prefix.
        throw new SidecarFailure('superseded_output_committed', 'the SDK replaced committed assistant output')
      }
      if (message.message?.model) state.effectiveModel = message.message.model
      if (message.error) {
        const code = isThirdPartyExtraUsageRejection(message)
          ? 'third_party_extra_usage'
          : mapAssistantError(message.error)
        throw new SidecarFailure(code, message.error)
      }
      if (superseded || !state.deltaText || hasToolUse(message.message?.content)) {
        mapAssistantContent(state, catalog, emit, message.message?.content)
      }
      return
    }
    case 'rate_limit_event':
      mapRateLimit(message, state, emit)
      return
    case 'auth_status':
      if (message.error) throw new SidecarFailure(mapAssistantError(message.error), message.error)
      return
    case 'result':
      mapResult(message, state, emit)
      return
    default:
      return
  }
}

function hasToolUse(content) {
  return Array.isArray(content) && content.some((block) => block?.type === 'tool_use')
}

function mapSystemMessage(message, catalog, state, emit) {
  if (message.subtype === 'init') {
    if (typeof message.claude_code_version === 'string') state.cliVersion = message.claude_code_version
    state.builtinTools = message.tools
    if (Array.isArray(message.tools)) {
      // The pinned SDK lists the connector's own MCP tools in init.tools once
      // the transient server is connected. Those are the admitted caller tools,
      // not native built-ins. Any tool outside the exact admitted catalogue
      // (built-in, foreign MCP, or an unknown shape) still fails closed.
      const exposed = message.tools.map((tool) => (typeof tool === 'string' ? tool : tool?.name))
      if (exposed.some((name) => typeof name !== 'string' || !catalog.byNative.has(name))) {
        throw new SidecarFailure('native_tools_exposed', 'the SDK exposed tools outside the admitted caller catalogue')
      }
    }
    if (Array.isArray(message.mcp_servers)) {
      let own
      for (const server of message.mcp_servers) {
        if (!server || server.name !== MCP_SERVER_NAME) {
          throw new SidecarFailure('native_mcp_exposed', 'the SDK exposed an MCP server that the connector did not configure')
        }
        own = server
        if (server.status === 'failed' || server.status === 'disabled' || server.status === 'needs-auth') {
          throw new SidecarFailure('mcp_unavailable', 'the transient tool server is unavailable')
        }
      }
      if (catalog.tools.length > 0 && !own) {
        throw new SidecarFailure('mcp_unavailable', 'the SDK did not register the transient tool server')
      }
    }
    return
  }
  if (message.subtype === 'model_refusal_fallback') {
    if (Array.isArray(message.retracted_message_uuids) && message.retracted_message_uuids.length > 0 && state.textStreamed) {
      throw new SidecarFailure('superseded_output_committed', 'a model fallback retracted output after it was committed')
    }
    const requested = state.requestedModel || message.original_model || ''
    const effective = message.fallback_model || state.effectiveModel || ''
    state.effectiveModel = effective || state.effectiveModel
    ensureStarted(state, emit)
    warn(
      state,
      emit,
      'model_refusal_fallback',
      `the SDK fell back from model ${modelLabel(requested, 'the requested model')} to model ${modelLabel(effective, 'an unknown model')} after a refusal`,
    )
    return
  }
  if (message.subtype === 'model_refusal_no_fallback') {
    ensureStarted(state, emit)
    warn(state, emit, 'model_refused', 'the model refused the request and no fallback ran')
  }
}

function mapStreamEvent(message, catalog, state, emit) {
  const event = message.event
  if (!event || typeof event !== 'object') return
  switch (event.type) {
    case 'message_start':
      if (event.message?.model) state.effectiveModel = event.message.model
      state.messageHasToolUse = false
      ensureStarted(state, emit)
      return
    case 'content_block_start':
      if (event.content_block?.type === 'tool_use') {
        assertAdmittedTool(state, catalog, event.content_block.name)
        state.messageHasToolUse = true
        state.toolBlocks.set(event.content_block.id, event.content_block.name)
      } else if (event.content_block?.type === 'text' && state.messageHasToolUse) {
        throw new SidecarFailure('unsupported_post_tool_text', 'the assistant streamed a text block after a tool_use block')
      }
      return
    case 'content_block_delta':
      if (event.delta?.type === 'text_delta') {
        if (state.messageHasToolUse) {
          throw new SidecarFailure('unsupported_post_tool_text', 'the assistant streamed text after a tool_use block')
        }
        emitText(state, emit, event.delta.text)
        state.deltaText = true
      }
      else if (event.delta?.type === 'thinking_delta') {
        warn(state, emit, 'reasoning_not_supported', 'the model returned reasoning content that this connector slice does not expose')
      }
      return
    default:
      return
  }
}

function mapRateLimit(message, state, emit) {
  const info = message.rate_limit_info
  if (!info || typeof info !== 'object') return
  // The primary subscription window decides whether this turn may run. The
  // overage status only reports whether overflow billing is available:
  // `overageStatus: rejected` with `overageDisabledReason: org_level_disabled`
  // is the normal no-overage configuration, and the same live turn streams and
  // completes with `status: allowed`. Treating it as exhaustion refuses a
  // healthy account.
  if (info.status === 'rejected') {
    throw new SidecarFailure('plan_exhausted', 'the Claude subscription reported an exhausted usage limit')
  }
  if (info.status === 'allowed_warning') {
    warn(state, emit, 'rate_limit_warning', 'the Claude subscription reported a usage warning')
  }
}

function mapResult(message, state, emit) {
  if (message.subtype !== 'success') {
    const detail = Array.isArray(message.errors) && message.errors.length > 0 ? message.errors[0] : 'the SDK turn failed'
    throw new SidecarFailure(mapResultError(message.subtype), detail)
  }
  ensureStarted(state, emit)
  emitUsage(state, emit, message.usage)
  const stopReason = typeof message.stop_reason === 'string' ? message.stop_reason : 'end_turn'
  if (stopReason === 'tool_use' || stopReason === 'tool_calls') {
    throw new SidecarFailure('tool_correlation_failed', 'the SDK query ended while a tool call was still outstanding')
  }
  state.stopReason = stopReason
  emit('response_finished', { stop_reason: stopReason })
}

function mapResultError(subtype) {
  switch (subtype) {
    case 'error_max_turns':
      return 'max_turns'
    case 'error_max_budget_usd':
      return 'plan_exhausted'
    case 'error_max_structured_output_retries':
      return 'structured_output_unsupported'
    default:
      return 'sdk_error'
  }
}

function isThirdPartyExtraUsageRejection(message) {
  if (message?.error !== 'unknown') return false
  if (message?.message?.model !== '<synthetic>') return false
  const content = message?.message?.content
  if (!Array.isArray(content) || content.length !== 1) return false
  return content[0]?.type === 'text' && content[0]?.text === THIRD_PARTY_EXTRA_USAGE_REJECTION
}

function mapAssistantError(error) {
  switch (error) {
    case 'authentication_failed':
    case 'oauth_org_not_allowed':
    case 'account_on_hold':
    case 'verification_required':
    case 'cloud_credential_error':
      return error
    case 'rate_limit':
      return 'rate_limited'
    case 'overloaded':
      return 'unavailable'
    case 'billing_error':
      // No dedicated connector code exists yet; a billing problem is never a
      // subscription usage limit, so degrade to the generic typed failure.
      return 'sdk_error'
    case 'model_not_found':
      return 'model_not_found'
    case 'invalid_request':
      return 'invalid_request'
    case 'max_output_tokens':
      return 'max_output_tokens'
    default:
      return 'sdk_error'
  }
}

export function classifyError(err) {
  const text = String(err?.message ?? err ?? '').toLowerCase()
  if (text.includes('not logged in') || text.includes('authentication') || text.includes('oauth') || text.includes('login')) {
    return 'authentication_failed'
  }
  // A rate limit or provider quota is transient capacity, not exhausted plan
  // usage. Only explicit subscription usage-limit prose means plan exhaustion.
  if (text.includes('rate limit') || text.includes('quota')) return 'rate_limited'
  if (text.includes('usage limit')) return 'plan_exhausted'
  if (text.includes('model not found') || text.includes('unknown model')) return 'model_not_found'
  if (text.includes('permission') || text.includes('denied')) return 'permission_denied'
  return 'sdk_error'
}

function modelLabel(model, fallback) {
  const text = safeMessage(model)
  if (!text) return fallback
  return JSON.stringify(text.length > 80 ? text.slice(0, 80) : text)
}

function safeMessage(value) {
  let text = typeof value === 'string' ? value : String(value?.message ?? value ?? '')
  text = text.replace(/sk-ant-[A-Za-z0-9_-]+/g, '[redacted]')
  text = text.replace(/\s+/g, ' ').trim()
  return text.length > 512 ? text.slice(0, 512) : text
}

// createPreToolUseHook captures the native tool_use_id and complete arguments
// before the MCP handler may wait. It never executes anything and denies any
// name outside the admitted catalogue.
export function createPreToolUseHook({ catalog, broker, emit, state }) {
  return async function preToolUse(input, toolUseID) {
    const id = typeof input?.tool_use_id === 'string' && input.tool_use_id !== '' ? input.tool_use_id : typeof toolUseID === 'string' ? toolUseID : ''
    const nativeName = typeof input?.tool_name === 'string' ? input.tool_name : ''
    const tool = catalog.byNative.get(nativeName)
    if (!tool || !nativeName.startsWith(MCP_PREFIX)) {
      emit('response_started', {})
      emit('error', { code: 'native_tool_use', message: 'the SDK attempted a native tool call while client tools were the only admitted surface' })
      return {
        hookSpecificOutput: {
          hookEventName: 'PreToolUse',
          permissionDecision: 'deny',
          permissionDecisionReason: 'only caller-admitted tools are available',
        },
      }
    }
    if (state && state.toolBlocks instanceof Map && state.toolBlocks.size > 1) {
      throw new SidecarFailure('unsupported_parallel_tool_calls', 'the assistant proposed more than one tool call in one step')
    }
    try {
      broker.create({ id, nativeName, tool, args: input?.tool_input ?? {} })
    } catch (err) {
      if (err instanceof ToolFailure) throw new SidecarFailure(err.code, err.message)
      throw err
    }
    return { continue: true }
  }
}

export async function runTurn({ config, request, emit, abortController, broker, pendingTTLMillis = 1800000 }) {
  const state = createTurnState(request.model)
  const catalog = buildToolCatalog(request.tools)
  const owner = broker ?? new ToolBroker()
  owner.bind(emit)
  const served = catalog.tools.length > 0
  const extras = {}
  if (served) {
    const server = createToolServer({
      catalog,
      broker: owner,
      onCall: async (tool, args, entry) => {
        // Defensive invariant: the MCP handler terminal must never precede the
        // response start, even if an assistant frame was not observed first.
        ensureStarted(state, emit)
        emit('tool_call', {
          tool_call_id: entry.id,
          native_name: tool.nativeName,
          tool_name: tool.name,
          arguments: args,
        })
        emit('response_finished', { stop_reason: 'tool_use', awaiting_tool_result: true })
        const result = await entry.deferred
        resetResponse(state)
        return { content: [{ type: 'text', text: result.content }], isError: Boolean(result.isError) }
      },
    })
    extras.servedTools = true
    extras.mcpServers = {
      [MCP_SERVER_NAME]: {
        type: 'sdk',
        name: MCP_SERVER_NAME,
        instance: server,
        timeout: Math.max(1000, Number(pendingTTLMillis) || 1800000),
      },
    }
    extras.hooks = {
      PreToolUse: [{ hooks: [createPreToolUseHook({ catalog, broker: owner, emit, state })] }],
    }
  }
  try {
    const q = query({
      prompt: oneMessage(request.prompt),
      options: buildOptions({ ...config, model: request.model, system: request.system, effort: request.effort }, abortController, extras),
    })
    try {
      for await (const message of q) {
        mapSdkMessage(message, state, emit, catalog)
        if (message.type === 'result') break
      }
    } finally {
      q.close()
    }
  } catch (err) {
    owner.fail(err instanceof Error ? err : new Error('the tool call was aborted'))
    if (abortController.signal.aborted) return
    if (err instanceof SidecarFailure) throw err
    if (err instanceof ToolFailure) throw new SidecarFailure(err.code, err.message)
    throw new SidecarFailure(classifyError(err), err?.message ?? String(err))
  }
  if (!state.started) throw new SidecarFailure('empty_response', 'the SDK completed without producing a response')
}
