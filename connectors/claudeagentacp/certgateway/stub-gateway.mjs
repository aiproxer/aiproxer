// Project-owned test-only stub: an Anthropic-protocol gateway on loopback
// recording every request. No external network is ever contacted.
import http from 'node:http';
import { appendFileSync } from 'node:fs';

const logFile = process.env.GW_LOG_FILE ?? 'gateway-requests.log';
const port = Number(process.env.GW_PORT ?? 18931);
const server = http.createServer((req, res) => {
  let body = '';
  req.on('data', c => { body += c; });
  req.on('end', () => {
    let model = '';
    try { model = JSON.parse(body)?.model ?? ''; } catch {}
    appendFileSync(logFile, JSON.stringify({ method: req.method, url: req.url, headers: req.headers, model }) + '\n');
    if (req.method === 'POST' && req.url.includes('/v1/messages')) {
      res.writeHead(200, { 'content-type': 'text/event-stream' });
      const send = (event, data) => res.write(`event: ${event}\ndata: ${JSON.stringify(data)}\n\n`);
      send('message_start', { type: 'message_start', message: { id: 'msg_gw', type: 'message', role: 'assistant', content: [], stop_reason: null, model: 'stub', usage: { input_tokens: 5, output_tokens: 0 } } });
      send('content_block_start', { type: 'content_block_start', index: 0, content_block: { type: 'text', text: '' } });
      send('content_block_delta', { type: 'content_block_delta', index: 0, delta: { type: 'text_delta', text: 'gateway-turn-ok' } });
      send('content_block_stop', { type: 'content_block_stop', index: 0 });
      send('message_delta', { type: 'message_delta', delta: { stop_reason: 'end_turn' }, usage: { output_tokens: 1 } });
      send('message_stop', { type: 'message_stop' });
      res.end();
      return;
    }
    res.writeHead(404, { 'content-type': 'application/json' });
    res.end(JSON.stringify({ type: 'error', error: { type: 'not_found' } }));
  });
});
server.listen(port, '127.0.0.1', () => console.log(`stub gateway on ${port}`));
