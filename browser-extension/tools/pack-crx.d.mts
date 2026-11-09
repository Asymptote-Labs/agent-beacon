// Types for tools/pack-crx.mjs, so the TypeScript unit tests can import it under
// `strict` without enabling allowJs for the whole project.

export declare function crxIdFromPublicKey(spkiDer: Buffer): string;
export declare function packCrx(zip: Buffer, privateKeyPem: string): { crx: Buffer; id: string };
export declare function readCrx(crx: Buffer): { id: string; zip: Buffer };
export declare function feedXml(feed: { id: string; version: string; codebase: string; sha256: string }): string;
export declare function feedVersion(xml: string, id: string): string | undefined;
export declare function compareVersions(a: string, b: string): number;
export declare function crxAssetName(version: string): string;
export declare function newestSignedVersion(
  releases: { tag?: string; draft?: boolean; assets?: string[] }[],
): string | undefined;
