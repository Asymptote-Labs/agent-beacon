// A stand-in for the beacon OTLP collector. Listens on an ephemeral loopback
// port, records every POST to /v1/logs (decoded OTLP JSON), and satisfies CORS
// preflight so the extension service worker can post to it cross-origin.

import http from 'node:http';
import type { AddressInfo } from 'node:net';
import type { LogsEnvelope } from '../../src/shared/otlp.js';

export interface MockCollector {
  url: string;
  received: LogsEnvelope[];
  reset(): void;
  close(): Promise<void>;
}

export async function startMockCollector(): Promise<MockCollector> {
  const received: LogsEnvelope[] = [];

  const server = http.createServer((req, res) => {
    // CORS preflight from the extension SW (content-type: application/json).
    if (req.method === 'OPTIONS') {
      res.writeHead(204, corsHeaders());
      res.end();
      return;
    }
    if (req.method === 'POST' && req.url === '/v1/logs') {
      let body = '';
      req.on('data', (c) => (body += c));
      req.on('end', () => {
        try {
          received.push(JSON.parse(body) as LogsEnvelope);
        } catch {
          /* ignore malformed */
        }
        res.writeHead(200, { 'content-type': 'application/json', ...corsHeaders() });
        res.end('{}');
      });
      return;
    }
    res.writeHead(404, corsHeaders());
    res.end();
  });

  await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', resolve));
  const { port } = server.address() as AddressInfo;

  return {
    url: `http://127.0.0.1:${port}/v1/logs`,
    received,
    reset: () => (received.length = 0),
    close: () => new Promise<void>((resolve) => server.close(() => resolve())),
  };
}

function corsHeaders(): http.OutgoingHttpHeaders {
  return {
    'access-control-allow-origin': '*',
    'access-control-allow-methods': 'POST, OPTIONS',
    'access-control-allow-headers': 'content-type',
  };
}
