import test from 'node:test';
import assert from 'node:assert/strict';
import { createNdjsonParser } from './ndjson.mjs';

// A value whose JSON encoding embeds an escaped newline, built via
// JSON.stringify so no source-level backslash confusion can slip in.
const embedded = JSON.stringify({ b: 'se!c0nd'.replace('!', '\n') });

test('ndjson parser survives byte-by-byte fragmentation', () => {
  const parsed = [];
  const parser = createNdjsonParser({ onLine: line => parsed.push(JSON.parse(line)) });
  const payload = [
    JSON.stringify({ jsonrpc: '2.0', id: 1, result: { sessionId: 's1' } }),
    JSON.stringify({ jsonrpc: '2.0', id: 2, error: { code: -32602, message: 'nope' } }),
    JSON.stringify({ jsonrpc: '2.0', method: 'session/update', params: { sessionId: 's1', update: { sessionUpdate: 'agent_message_chunk', content: { type: 'text', text: 'gateway-turn-ok' } } } }),
    '',
  ].join('\n');
  for (const byte of payload) parser.push(byte); // one byte at a time
  assert.equal(parsed.length, 3);
  assert.deepEqual(parsed[0], { jsonrpc: '2.0', id: 1, result: { sessionId: 's1' } });
  assert.deepEqual(parsed[2], { jsonrpc: '2.0', method: 'session/update', params: { sessionId: 's1', update: { sessionUpdate: 'agent_message_chunk', content: { type: 'text', text: 'gateway-turn-ok' } } } });
  assert.equal(parser.remaining(), '');
});

test('ndjson parser keeps incomplete tails and buffer boundaries mixed with CRLF and escaped newlines', () => {
  const parsed = [];
  const parser = createNdjsonParser({ onLine: line => parsed.push(JSON.parse(line)) });
  parser.push('{"a":1}\r');
  parser.push('\n' + embedded);
  parser.push('\n');
  assert.equal(parsed.length, 2);
  assert.deepEqual(parsed[0], { a: 1 });
  assert.deepEqual(parsed[1], { b: 'se\nc0nd' });
  assert.equal(parser.remaining(), '');
});

test('ndjson parser enforces maximum frame size', () => {
  let overflowed = 0;
  const parsed = [];
  const parser = createNdjsonParser({
    onLine: line => parsed.push(JSON.parse(line)),
    onOverflow: size => { overflowed = size; },
    maxBytes: 16,
  });
  parser.push(JSON.stringify({ big: 'x'.repeat(64) }) + '\n' + '{"ok":1}\n');
  assert.ok(overflowed > 16);
  assert.deepEqual(parsed, [{ ok: 1 }]);
});

test('ndjson parser reports a dangling final line instead of dropping it', () => {
  const parsed = [];
  const parser = createNdjsonParser({ onLine: line => parsed.push(JSON.parse(line)) });
  parser.push('{"id":3}\n{"id":4}\n');
  assert.equal(parsed.length, 2);
  assert.equal(parser.remaining(), '');
  const clip = createNdjsonParser({ onLine: () => {} });
  clip.push('{"truncated');
  assert.equal(clip.remaining(), '{"truncated');
});
