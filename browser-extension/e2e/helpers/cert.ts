// Generates a throwaway self-signed cert (via openssl) covering the chat-site
// hostnames, so the replay server can answer https://claude.ai / https://chatgpt.com
// after --host-resolver-rules maps those hosts to it. Test-only; never trusted
// beyond the automation Chromium launched with --ignore-certificate-errors.

import { execFileSync } from 'node:child_process';
import { mkdtempSync, readFileSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';

let cached: { key: string; cert: string } | undefined;

export function getTestCert(): { key: string; cert: string } {
  if (cached) return cached;
  const dir = mkdtempSync(path.join(os.tmpdir(), 'beacon-cert-'));
  const keyPath = path.join(dir, 'key.pem');
  const certPath = path.join(dir, 'cert.pem');
  execFileSync('openssl', [
    'req', '-x509', '-newkey', 'rsa:2048', '-nodes',
    '-keyout', keyPath,
    '-out', certPath,
    '-days', '3',
    '-subj', '/CN=claude.ai',
    '-addext',
    'subjectAltName=DNS:claude.ai,DNS:chatgpt.com,DNS:chat.openai.com,DNS:localhost,IP:127.0.0.1',
  ]);
  cached = { key: readFileSync(keyPath, 'utf8'), cert: readFileSync(certPath, 'utf8') };
  return cached;
}
