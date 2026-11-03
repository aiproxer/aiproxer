import { Client } from '@modelcontextprotocol/sdk/client/index.js'
import { InMemoryTransport } from '@modelcontextprotocol/sdk/inMemory.js'

// Deterministic SDK-shaped query used only by the production-wiring test. It
// replaces the external SDK query boundary; the real session.mjs, bridge.mjs,
// tool-server.mjs and the installed MCP client/server all execute.
export const SDK_VERSION = '0.3.316'

export function query({ prompt, options }) {
  const scenario = process.env.AIP_FIXTURE_SCENARIO ?? 'normal'
  const iterator = (async function* () {
    yield { type: 'system', subtype: 'init', tools: [], mcp_servers: [{ name: 'aiproxer', status: 'connected' }] }
    for await (const _message of prompt) break

    if (scenario === 'post_tool_stream') {
      yield { type: 'stream_event', event: { type: 'message_start', message: { model: 'claude-sonnet-5' } } }
      yield { type: 'stream_event', event: { type: 'content_block_start', content_block: { type: 'text' } } }
      yield { type: 'stream_event', event: { type: 'content_block_delta', delta: { type: 'text_delta', text: 'look' } } }
      yield {
        type: 'stream_event',
        event: {
          type: 'content_block_start',
          content_block: { type: 'tool_use', id: 'toolu_fixture', name: 'mcp__aiproxer__Read' },
        },
      }
      yield { type: 'stream_event', event: { type: 'content_block_delta', delta: { type: 'text_delta', text: 'AFTER-TOOL' } } }
      return
    }
    if (scenario === 'post_tool_message') {
      yield {
        type: 'assistant',
        message: {
          model: 'claude-sonnet-5',
          content: [
            { type: 'text', text: 'look' },
            { type: 'tool_use', id: 'toolu_fixture', name: 'mcp__aiproxer__Read', input: { file_path: 'x' } },
            { type: 'text', text: 'AFTER-TOOL' },
          ],
        },
      }
      return
    }

    yield { type: 'stream_event', event: { type: 'message_start', message: { model: 'claude-sonnet-5' } } }
    yield { type: 'stream_event', event: { type: 'content_block_delta', delta: { type: 'text_delta', text: 'look' } } }
    yield {
      type: 'stream_event',
      event: {
        type: 'content_block_start',
        content_block: { type: 'tool_use', id: 'toolu_fixture', name: 'mcp__aiproxer__Read' },
      },
    }

    const hook = options.hooks.PreToolUse[0].hooks[0]
    const decision = await hook(
      { hook_event_name: 'PreToolUse', tool_name: 'mcp__aiproxer__Read', tool_input: { file_path: 'x' }, tool_use_id: 'toolu_fixture' },
      'toolu_fixture',
    )
    if (decision?.hookSpecificOutput?.permissionDecision === 'deny') {
      throw new Error('the connector denied the tool call')
    }

    const [clientTransport, serverTransport] = InMemoryTransport.createLinkedPair()
    await options.mcpServers.aiproxer.instance.connect(serverTransport)
    const client = new Client({ name: 'sdk-fixture', version: '1.0.0' })
    await client.connect(clientTransport)
    const call = await client.callTool({ name: 'Read', arguments: { file_path: 'x' } }, undefined, { timeout: 30_000 })
    await client.close()

    yield { type: 'stream_event', event: { type: 'message_start', message: { model: 'claude-sonnet-5' } } }
    yield {
      type: 'stream_event',
      event: { type: 'content_block_delta', delta: { type: 'text_delta', text: 'got:' + call.content[0].text } },
    }
    yield { type: 'result', subtype: 'success', stop_reason: 'end_turn', usage: { input_tokens: 3, output_tokens: 2 } }
  })()
  return { [Symbol.asyncIterator]: () => iterator, close() {} }
}
