#!/usr/bin/env node
// Signs the Chrome build as a CRX3 file and writes the update feed Chrome polls for new versions.
//
// IT installs the extension through Chrome policy (ExtensionSettings `force_installed`) from a
// self-hosted update URL rather than the Chrome Web Store. That needs two files: a CRX signed with
// our key, whose public key fixes the extension ID, and an update manifest (`updates.xml`) naming
// the current version and where to download it.
//
// Dependency-free on purpose (node:crypto, node:fs): the release job that holds the signing key runs
// this file without `npm ci`, so no third-party package ever executes alongside the key.
//
// The format follows Chromium's components/crx_file (crx3.proto, crx_creator.cc, crx_verifier.cc):
//
//   "Cr24" | uint32le 3 | uint32le header length | CrxFileHeader | zip
//
//   CrxFileHeader { repeated AsymmetricKeyProof sha256_with_rsa = 2; bytes signed_header_data = 10000; }
//   AsymmetricKeyProof { bytes public_key = 1; bytes signature = 2; }   // SubjectPublicKeyInfo DER
//   SignedData { bytes crx_id = 1; }                                     // first 16 bytes of SHA-256(SPKI)
//
// The signature is RSA PKCS#1 v1.5 with SHA-256 over
//   "CRX3 SignedData\x00" | uint32le len(signed_header_data) | signed_header_data | zip
//
// Plain JavaScript, like targets.mjs, so Node runs it directly. Types for the unit tests are in
// pack-crx.d.mts.

import crypto from 'node:crypto';
import fs from 'node:fs';
import { pathToFileURL } from 'node:url';
import { CHROME_EXTENSION_ID } from './targets.mjs';

const MAGIC = Buffer.from('Cr24', 'latin1');
const CRX_VERSION = 3;
const SIGNED_DATA_PREFIX = Buffer.from('CRX3 SignedData\x00', 'latin1');
const FIELD_PUBLIC_KEY = 1;
const FIELD_SIGNATURE = 2;
const FIELD_CRX_ID = 1;
const FIELD_SHA256_WITH_RSA = 2;
const FIELD_SIGNED_HEADER_DATA = 10000;
const ID_PATTERN = /^[a-p]{32}$/;
const VERSION_PATTERN = /^\d{1,9}(\.\d{1,9}){0,3}$/;
const SHA256_PATTERN = /^[0-9a-f]{64}$/;

/** Chrome's extension ID for a public key: SHA-256 of its SPKI DER, first 16 bytes, each hex digit
 *  mapped onto a–p (components/crx_file/id_util.cc). */
export function crxIdFromPublicKey(spkiDer) {
  const head = sha256(spkiDer).subarray(0, 16).toString('hex');
  return [...head].map((c) => String.fromCharCode(97 + parseInt(c, 16))).join('');
}

/**
 * Sign `zip` (the extension archive, as published) with an RSA private key in PEM form.
 * @returns {{ crx: Buffer, id: string }}
 */
export function packCrx(zip, privateKeyPem) {
  const key = crypto.createPrivateKey(privateKeyPem);
  if (key.asymmetricKeyType !== 'rsa') {
    throw new Error(`signing key must be RSA, got ${key.asymmetricKeyType}`);
  }
  const spki = crypto.createPublicKey(key).export({ type: 'spki', format: 'der' });
  const signedHeaderData = lengthDelimited(FIELD_CRX_ID, sha256(spki).subarray(0, 16));
  const signature = crypto.sign('sha256', signedMessage(signedHeaderData, zip), {
    key,
    padding: crypto.constants.RSA_PKCS1_PADDING,
  });
  const proof = Buffer.concat([
    lengthDelimited(FIELD_PUBLIC_KEY, spki),
    lengthDelimited(FIELD_SIGNATURE, signature),
  ]);
  const header = Buffer.concat([
    lengthDelimited(FIELD_SHA256_WITH_RSA, proof),
    lengthDelimited(FIELD_SIGNED_HEADER_DATA, signedHeaderData),
  ]);
  const prefix = Buffer.alloc(12);
  MAGIC.copy(prefix, 0);
  prefix.writeUInt32LE(CRX_VERSION, 4);
  prefix.writeUInt32LE(header.length, 8);
  return { crx: Buffer.concat([prefix, header, zip]), id: crxIdFromPublicKey(spki) };
}

/**
 * Parse a CRX3 file and verify it the way Chrome does for an off-store extension: the header names
 * an ID, an RSA proof's public key hashes to that ID, and that proof's signature covers the archive.
 * Throws on anything else.
 * @returns {{ id: string, zip: Buffer }}
 */
export function readCrx(crx) {
  if (crx.length < 12 || !crx.subarray(0, 4).equals(MAGIC)) throw new Error('not a CRX file');
  const version = crx.readUInt32LE(4);
  if (version !== CRX_VERSION) throw new Error(`unsupported CRX version ${version}`);
  const headerLength = crx.readUInt32LE(8);
  if (12 + headerLength > crx.length) throw new Error('CRX header runs past the end of the file');
  const header = crx.subarray(12, 12 + headerLength);
  const zip = crx.subarray(12 + headerLength);

  const proofs = [];
  let signedHeaderData;
  for (const { field, value } of readFields(header)) {
    if (field === FIELD_SHA256_WITH_RSA) proofs.push(value);
    if (field === FIELD_SIGNED_HEADER_DATA) signedHeaderData = value;
  }
  if (!signedHeaderData) throw new Error('CRX header has no signed_header_data');
  const crxId = readFields(signedHeaderData).find((f) => f.field === FIELD_CRX_ID)?.value;
  if (!crxId || crxId.length !== 16) throw new Error('CRX header has no 16-byte crx_id');

  const message = signedMessage(signedHeaderData, zip);
  for (const proof of proofs) {
    const fields = readFields(proof);
    const spki = fields.find((f) => f.field === FIELD_PUBLIC_KEY)?.value;
    const signature = fields.find((f) => f.field === FIELD_SIGNATURE)?.value;
    if (!spki || !signature || !sha256(spki).subarray(0, 16).equals(crxId)) continue;
    const key = crypto.createPublicKey({ key: spki, format: 'der', type: 'spki' });
    const ok = crypto.verify('sha256', message, { key, padding: crypto.constants.RSA_PKCS1_PADDING }, signature);
    if (!ok) throw new Error('CRX signature does not match its contents');
    return { id: crxIdFromPublicKey(spki), zip };
  }
  throw new Error('CRX has no RSA proof for its own extension ID');
}

/**
 * The update manifest Chrome fetches from the policy's (or the manifest's) update_url
 * (extensions/browser/updater/safe_manifest_parser.cc). `hash_sha256` makes Chrome refuse a
 * download that is not exactly the published CRX.
 */
export function feedXml({ id, version, codebase, sha256: hash }) {
  if (!ID_PATTERN.test(id)) throw new Error(`not an extension ID: ${id}`);
  if (!VERSION_PATTERN.test(version)) throw new Error(`not a Chrome extension version: ${version}`);
  const url = new URL(codebase);
  if (url.protocol !== 'https:' && url.protocol !== 'http:') throw new Error(`codebase must be http(s): ${codebase}`);
  if (!SHA256_PATTERN.test(hash)) throw new Error(`not a lowercase SHA-256: ${hash}`);
  return [
    "<?xml version='1.0' encoding='UTF-8'?>",
    "<gupdate xmlns='http://www.google.com/update2/response' protocol='2.0'>",
    `  <app appid='${id}'>`,
    `    <updatecheck codebase='${xmlAttr(url.href)}' version='${version}' hash_sha256='${hash}' />`,
    '  </app>',
    '</gupdate>',
    '',
  ].join('\n');
}

/** The version a feed offers for `id`, or undefined when it names none. Reads only what feedXml writes. */
export function feedVersion(xml, id) {
  const app = new RegExp(`<app\\s+appid=['"]${id}['"][^>]*>([\\s\\S]*?)</app>`).exec(xml);
  return app ? /\bversion=['"]([^'"]+)['"]/.exec(app[1])?.[1] : undefined;
}

/** The signed build's asset name on an `ext-v<version>` release. */
export function crxAssetName(version) {
  return `agent-beacon-browser-extension-${version}-chrome.crx`;
}

/**
 * The highest version among published `ext-v*` releases that carry a signed CRX and its checksum,
 * or undefined. The feed offers this rather than whichever release ran last, so a feed job that
 * never ran, or a re-run of an older tag, cannot leave fleets on an older version.
 * @param {{ tag?: string, draft?: boolean, assets?: string[] }[]} releases
 */
export function newestSignedVersion(releases) {
  let newest;
  for (const release of releases) {
    if (release.draft || !release.tag?.startsWith('ext-v')) continue;
    const version = release.tag.slice('ext-v'.length);
    if (!VERSION_PATTERN.test(version)) continue;
    const crx = crxAssetName(version);
    if (!release.assets?.includes(crx) || !release.assets.includes(`${crx}.sha256`)) continue;
    if (newest === undefined || compareVersions(version, newest) > 0) newest = version;
  }
  return newest;
}

/** Compare dotted Chrome versions numerically: negative, zero or positive. */
export function compareVersions(a, b) {
  const pa = a.split('.').map(Number);
  const pb = b.split('.').map(Number);
  for (let i = 0; i < Math.max(pa.length, pb.length); i++) {
    const d = (pa[i] ?? 0) - (pb[i] ?? 0);
    if (d !== 0) return d;
  }
  return 0;
}

function sha256(data) {
  return crypto.createHash('sha256').update(data).digest();
}

function signedMessage(signedHeaderData, zip) {
  const length = Buffer.alloc(4);
  length.writeUInt32LE(signedHeaderData.length, 0);
  return Buffer.concat([SIGNED_DATA_PREFIX, length, signedHeaderData, zip]);
}

function varint(n) {
  const out = [];
  while (n > 0x7f) {
    out.push((n & 0x7f) | 0x80);
    n = Math.floor(n / 128);
  }
  out.push(n);
  return Buffer.from(out);
}

/** A protobuf length-delimited field (wire type 2), the only kind the CRX header uses. */
function lengthDelimited(field, bytes) {
  return Buffer.concat([varint(field * 8 + 2), varint(bytes.length), bytes]);
}

function readVarint(buf, pos) {
  let value = 0;
  let scale = 1;
  for (let i = 0; i < 5; i++) {
    if (pos >= buf.length) throw new Error('truncated protobuf varint');
    const byte = buf[pos++];
    value += (byte & 0x7f) * scale;
    if ((byte & 0x80) === 0) return [value, pos];
    scale *= 128;
  }
  throw new Error('protobuf varint too long');
}

function readFields(buf) {
  const fields = [];
  let pos = 0;
  while (pos < buf.length) {
    let tag, length;
    [tag, pos] = readVarint(buf, pos);
    if (tag % 8 !== 2) throw new Error(`unexpected protobuf wire type ${tag % 8} in CRX header`);
    [length, pos] = readVarint(buf, pos);
    if (pos + length > buf.length) throw new Error('truncated protobuf field');
    fields.push({ field: Math.floor(tag / 8), value: buf.subarray(pos, pos + length) });
    pos += length;
  }
  return fields;
}

function xmlAttr(s) {
  return s.replace(/&/g, '&amp;').replace(/'/g, '&apos;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
}

// ---- CLI ----

const USAGE = `usage:
  pack-crx.mjs pack   --zip <archive.zip> --key-env <VAR> | --key <key.pem>  --out <file.crx> [--expect-id <id>]
  pack-crx.mjs verify --crx <file.crx> [--zip <archive.zip>] [--expect-id <id>]
  pack-crx.mjs feed   --id <id> --version <v> --codebase <url> --sha256 <hex> --out <updates.xml> [--current <updates.xml>]
  pack-crx.mjs newest --releases <releases.ndjson>

--expect-id defaults to CHROME_EXTENSION_ID in targets.mjs. pack prints the extension ID.
feed refuses to write a version lower than the one in --current, so re-running an old release
cannot roll installed extensions back. newest prints the highest ext-v* version whose release has a
signed CRX and its checksum, from one {"tag", "draft", "assets"} object per line.`;

function parseArgs(argv) {
  const opts = {};
  for (let i = 0; i < argv.length; i++) {
    const arg = argv[i];
    if (!arg.startsWith('--') || i + 1 >= argv.length) throw new Error(`unexpected argument ${arg}\n\n${USAGE}`);
    opts[arg.slice(2)] = argv[++i];
  }
  return opts;
}

function need(opts, name) {
  if (!opts[name]) throw new Error(`missing --${name}\n\n${USAGE}`);
  return opts[name];
}

function main(argv) {
  const [command, ...rest] = argv;
  const opts = parseArgs(rest);
  const expectId = opts['expect-id'] ?? CHROME_EXTENSION_ID;
  switch (command) {
    case 'pack': {
      const pem = opts['key-env'] ? process.env[opts['key-env']] : opts.key && fs.readFileSync(opts.key, 'utf8');
      if (!pem) throw new Error(`no signing key: set --key-env to a non-empty variable or pass --key\n\n${USAGE}`);
      const { crx, id } = packCrx(fs.readFileSync(need(opts, 'zip')), pem);
      if (id !== expectId) throw new Error(`signing key gives extension ID ${id}, expected ${expectId}`);
      fs.writeFileSync(need(opts, 'out'), crx);
      console.log(id);
      return;
    }
    case 'verify': {
      const { id, zip } = readCrx(fs.readFileSync(need(opts, 'crx')));
      if (id !== expectId) throw new Error(`CRX is extension ${id}, expected ${expectId}`);
      if (opts.zip && !zip.equals(fs.readFileSync(opts.zip))) {
        throw new Error(`CRX does not contain exactly ${opts.zip}`);
      }
      console.log(`crx ok: ${id}${opts.zip ? `, contains ${opts.zip}` : ''}`);
      return;
    }
    case 'feed': {
      const id = need(opts, 'id');
      const version = need(opts, 'version');
      if (opts.current && fs.existsSync(opts.current)) {
        const current = feedVersion(fs.readFileSync(opts.current, 'utf8'), id);
        if (current && compareVersions(version, current) < 0) {
          throw new Error(`the feed already offers ${current}; refusing to roll it back to ${version}`);
        }
      }
      const xml = feedXml({ id, version, codebase: need(opts, 'codebase'), sha256: need(opts, 'sha256') });
      fs.writeFileSync(need(opts, 'out'), xml);
      process.stdout.write(xml);
      return;
    }
    case 'newest': {
      const lines = fs.readFileSync(need(opts, 'releases'), 'utf8').split('\n').filter((line) => line.trim());
      const version = newestSignedVersion(lines.map((line) => JSON.parse(line)));
      if (!version) throw new Error('no published ext-v* release has a signed CRX and its checksum');
      console.log(version);
      return;
    }
    default:
      throw new Error(USAGE);
  }
}

if (import.meta.url === pathToFileURL(process.argv[1] ?? '').href) {
  try {
    main(process.argv.slice(2));
  } catch (err) {
    console.error(err instanceof Error ? err.message : err);
    process.exit(1);
  }
}
