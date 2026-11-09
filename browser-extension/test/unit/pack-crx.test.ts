// The CRX3 signer and update feed (tools/pack-crx.mjs). Chrome itself accepting the output is the
// policy e2e's job (e2e/policy-install.spec.ts); these pin the format and the refusals.
import { describe, expect, it } from 'vitest';
import crypto from 'node:crypto';
import { execFileSync } from 'node:child_process';
import { mkdtempSync, readFileSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import {
  compareVersions,
  crxAssetName,
  crxIdFromPublicKey,
  feedVersion,
  feedXml,
  newestSignedVersion,
  packCrx,
  readCrx,
} from '../../tools/pack-crx.mjs';

const tool = path.join(path.dirname(fileURLToPath(import.meta.url)), '..', '..', 'tools', 'pack-crx.mjs');
const rsaPem = () =>
  crypto.generateKeyPairSync('rsa', { modulusLength: 2048 }).privateKey.export({ type: 'pkcs8', format: 'pem' }).toString();
const spkiOf = (pem: string) => crypto.createPublicKey(pem).export({ type: 'spki', format: 'der' });
const zip = Buffer.from('PK\u0003\u0004 stand-in archive bytes');

describe('crxIdFromPublicKey', () => {
  it("matches Chromium's own id_util test vector", () => {
    // components/crx_file/id_util_unittest.cc: GenerateId("test").
    expect(crxIdFromPublicKey(Buffer.from('test'))).toBe('jpignaibiiemhngfjkcpokkamffknabf');
  });
});

describe('packCrx / readCrx', () => {
  const pem = rsaPem();

  it('round-trips: the ID comes from the key and the archive is carried byte for byte', () => {
    const { crx, id } = packCrx(zip, pem);
    expect(id).toBe(crxIdFromPublicKey(spkiOf(pem)));
    expect(id).toMatch(/^[a-p]{32}$/);
    expect(readCrx(crx)).toEqual({ id, zip });
  });

  it('writes the CRX3 framing and the signed_header_data field Chrome reads', () => {
    const { crx } = packCrx(zip, pem);
    expect(crx.subarray(0, 4).toString('latin1')).toBe('Cr24');
    expect(crx.readUInt32LE(4)).toBe(3);
    expect(crx.subarray(crx.length - zip.length)).toEqual(zip);
    // Field 10000, wire type 2, then an 18-byte SignedData holding the 16-byte crx_id.
    expect(crx.includes(Buffer.from([0x82, 0xf1, 0x04, 0x12, 0x0a, 0x10]))).toBe(true);
  });

  it('refuses an archive changed after signing', () => {
    const { crx } = packCrx(zip, pem);
    crx[crx.length - 1] ^= 0xff;
    expect(() => readCrx(crx)).toThrow('signature does not match');
  });

  it('refuses a header whose ID no proof belongs to', () => {
    const { crx } = packCrx(zip, pem);
    const otherId = crypto.createHash('sha256').update(spkiOf(rsaPem())).digest().subarray(0, 16);
    const at = crx.indexOf(Buffer.from([0x82, 0xf1, 0x04, 0x12, 0x0a, 0x10])) + 6;
    otherId.copy(crx, at);
    expect(() => readCrx(crx)).toThrow('no RSA proof');
  });

  it('refuses a file that is not a CRX3', () => {
    expect(() => readCrx(zip)).toThrow('not a CRX file');
  });

  it('refuses a key that is not RSA', () => {
    const ec = crypto.generateKeyPairSync('ec', { namedCurve: 'P-256' }).privateKey.export({ type: 'pkcs8', format: 'pem' });
    expect(() => packCrx(zip, ec.toString())).toThrow('must be RSA');
  });
});

describe('update feed', () => {
  const id = 'abcdefghijklmnopabcdefghijklmnop';
  const sha = 'a'.repeat(64);

  it('names the app, the version, where to download it and its hash', () => {
    const xml = feedXml({ id, version: '0.2.0', codebase: 'https://example.test/x.crx?a=1&b=2', sha256: sha });
    expect(xml).toContain("<gupdate xmlns='http://www.google.com/update2/response' protocol='2.0'>");
    expect(xml).toContain(`<app appid='${id}'>`);
    expect(xml).toContain(
      `<updatecheck codebase='https://example.test/x.crx?a=1&amp;b=2' version='0.2.0' hash_sha256='${sha}' />`,
    );
    expect(feedVersion(xml, id)).toBe('0.2.0');
    expect(feedVersion(xml, 'pppppppppppppppppppppppppppppppp')).toBeUndefined();
  });

  it('refuses values Chrome would reject', () => {
    const ok = { id, version: '0.2.0', codebase: 'https://example.test/x.crx', sha256: sha };
    expect(() => feedXml({ ...ok, id: 'not-an-id' })).toThrow('extension ID');
    expect(() => feedXml({ ...ok, version: '0.2.0-rc1' })).toThrow('version');
    expect(() => feedXml({ ...ok, codebase: 'file:///tmp/x.crx' })).toThrow('http(s)');
    expect(() => feedXml({ ...ok, sha256: 'A'.repeat(64) })).toThrow('SHA-256');
  });

  it('offers the newest published release that carries a signed build', () => {
    const signed = (v: string) => [crxAssetName(v), `${crxAssetName(v)}.sha256`, `agent-beacon-browser-extension-${v}-chrome.zip`];
    expect(
      newestSignedVersion([
        { tag: 'ext-v0.2.0', assets: signed('0.2.0') },
        { tag: 'ext-v0.10.0', assets: signed('0.10.0') },
        { tag: 'ext-v0.9.0', assets: signed('0.9.0') },
        { tag: 'ext-v0.11.0', draft: true, assets: signed('0.11.0') }, // not published
        { tag: 'ext-v0.12.0', assets: [crxAssetName('0.12.0')] }, // CRX without its checksum
        { tag: 'ext-v0.1.1', assets: ['agent-beacon-browser-extension-0.1.1-chrome.zip'] }, // before signing
        { tag: 'v1.3.60', assets: signed('1.3.60') }, // a CLI release
        { tag: 'ext-updates', assets: ['updates.xml'] },
      ]),
    ).toBe('0.10.0');
    expect(newestSignedVersion([{ tag: 'ext-v0.1.1', assets: [] }])).toBeUndefined();
  });

  it('compares dotted versions numerically', () => {
    expect(compareVersions('0.10.0', '0.9.9')).toBeGreaterThan(0);
    expect(compareVersions('1.0', '1.0.0')).toBe(0);
    expect(compareVersions('0.2.0', '0.2.1')).toBeLessThan(0);
  });
});

describe('CLI', () => {
  const dir = mkdtempSync(path.join(tmpdir(), 'beacon-crx-'));
  const pem = rsaPem();
  const id = crxIdFromPublicKey(spkiOf(pem));
  const run = (args: string[], env: Record<string, string> = {}) =>
    execFileSync(process.execPath, [tool, ...args], { env: { ...process.env, ...env }, encoding: 'utf8', stdio: 'pipe' });
  writeFileSync(path.join(dir, 'ext.zip'), zip);

  it('signs from a key in the environment, checks the ID, and verifies the result', () => {
    const out = run(
      ['pack', '--zip', path.join(dir, 'ext.zip'), '--key-env', 'TEST_CRX_KEY', '--out', path.join(dir, 'ext.crx'), '--expect-id', id],
      { TEST_CRX_KEY: pem },
    );
    expect(out.trim()).toBe(id);
    expect(run(['verify', '--crx', path.join(dir, 'ext.crx'), '--zip', path.join(dir, 'ext.zip'), '--expect-id', id])).toContain('crx ok');
  });

  it('refuses a key that gives a different ID than the pinned one', () => {
    expect(() =>
      run(['pack', '--zip', path.join(dir, 'ext.zip'), '--key-env', 'TEST_CRX_KEY', '--out', path.join(dir, 'x.crx'), '--expect-id', 'a'.repeat(32)], {
        TEST_CRX_KEY: pem,
      }),
    ).toThrow(/expected a{32}/);
  });

  it('refuses to roll the feed back to an older version', () => {
    const feed = path.join(dir, 'updates.xml');
    const args = (version: string) => ['feed', '--id', id, '--version', version, '--codebase', 'https://example.test/x.crx', '--sha256', 'b'.repeat(64), '--out', feed, '--current', feed];
    run(args('0.3.0'));
    expect(() => run(args('0.2.0'))).toThrow(/already offers 0\.3\.0/);
    run(args('0.3.0')); // re-running the same release rewrites the same feed
    expect(feedVersion(readFileSync(feed, 'utf8'), id)).toBe('0.3.0');
  });
});
