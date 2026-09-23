// Types for tools/targets.mjs, so the TypeScript unit tests can import it under
// `strict` without enabling allowJs for the whole project.

export declare const GECKO_ID: string;
export declare const FIREFOX_MIN_VERSION: string;
export declare const FIREFOX_ANDROID_MIN_VERSION: string;
export declare const MIN_FIREFOX_FOR_MAIN_WORLD: number;
export declare const SAFARI_MIN_VERSION: number;
export declare const FIREFOX_DATA_COLLECTION: Readonly<{ required: string[] }>;

export declare function firefoxManifest(base: Record<string, any>): Record<string, any>;

export interface BuildTarget {
  name: string;
  outdir: string;
  esbuildTarget: string[];
  manifest: ((base: Record<string, any>) => Record<string, any>) | null;
}

export declare const TARGETS: Readonly<Record<string, BuildTarget>>;
export declare const DEFAULT_TARGET: string;
export declare function getTarget(name: string): BuildTarget;
export declare function assertSafeOutdir(projectDir: string, srcDir: string, outdir: string): void;
