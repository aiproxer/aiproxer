// Certificate-harness test-only helper: a chunk-safe NDJSON line parser.
// Pipe reads may split JSON-RPC frames at arbitrary byte boundaries; this
// parser retains the incomplete tail across chunks and enforces a maximum
// frame size so a hostile or broken stream cannot grow memory unbounded.
export function createNdjsonParser({ onLine, onOverflow, maxBytes = 1 << 20 }) {
  if (typeof onLine !== 'function') throw new Error('onLine handler required');
  if (!Number.isSafeInteger(maxBytes) || maxBytes <= 0) throw new Error('maxBytes required');
  let tail = '';
  return {
    push(chunk) {
      tail += chunk;
      let index = tail.indexOf('\n');
      while (index >= 0) {
        const line = tail.slice(0, index);
        tail = tail.slice(index + 1);
        if (Buffer.byteLength(line) > maxBytes) {
          if (typeof onOverflow === 'function') onOverflow(Buffer.byteLength(line));
          else throw new Error(`ndjson frame exceeds ${maxBytes} bytes`);
        } else if (line.trim() !== '') {
          onLine(line);
        }
        index = tail.indexOf('\n');
      }
      if (Buffer.byteLength(tail) > maxBytes) {
        if (typeof onOverflow === 'function') onOverflow(Buffer.byteLength(tail));
        else throw new Error(`ndjson frame exceeds ${maxBytes} bytes`);
      }
    },
    // A valid NDJSON stream never ends mid-line; a nonempty tail at close is
    // surfaced so the harness can fail loudly instead of silently dropping it.
    remaining() {
      const rest = tail;
      tail = '';
      return rest;
    },
  };
}
