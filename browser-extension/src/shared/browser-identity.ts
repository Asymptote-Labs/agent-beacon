// PURE: which browser is this extension running in?
//
// Every Chromium fork runs the same MV3 build, so a captured turn says nothing
// about the browser it came from unless we record it. On a managed fleet that
// matters: a paste into ChatGPT from the managed Edge profile and one from a
// personal Brave install are different investigations.
//
// Resolution order, from most to least reliable:
//   1. navigator.userAgentData.brands (UA Client Hints). Edge, Opera and Brave
//      add their own brand here, which the UA string no longer carries reliably.
//   2. A Brave-only `navigator.brave` object, for Brave builds whose brand list
//      reads like plain Chrome.
//   3. The UA string's vendor tokens (Edg/, OPR/, Vivaldi/, Firefox/), for
//      engines without UA Client Hints.
//
// What this cannot do: Arc and Vivaldi do not add a brand of their own, and
// copy Chrome's UA string, so from a service worker they are indistinguishable
// from Google Chrome or plain Chromium. We report what the browser claims and
// document that limit rather than guessing.
//
// No I/O, no chrome.*: sw.ts hands in `navigator`, tests hand in fixtures.

/** One entry of NavigatorUAData.brands. */
export interface UABrand {
  brand: string;
  version: string;
}

/** The subset of (Worker)Navigator this reads. Every field is optional. */
export interface NavigatorLike {
  userAgent?: string;
  userAgentData?: { brands?: readonly UABrand[] } | null;
  brave?: unknown;
}

export interface BrowserIdentity {
  /** Browser name as the browser itself claims it, e.g. "Microsoft Edge". OTel `user_agent.name`. */
  name: string;
  /** Major version of that browser, when known. OTel `user_agent.version`. */
  version?: string;
  /** UA Client Hints brands as "<brand> <version>", in reported order. OTel `browser.brands`. */
  brands: string[];
  /** The raw UA string, when available. OTel `user_agent.original`. */
  original?: string;
}

const CHROME = 'Google Chrome';
const CHROMIUM = 'Chromium';

// Chromium adds a deliberately malformed "GREASE" brand ("Not_A Brand",
// "Not)A;Brand", " Not A;Brand", ...) with randomized punctuation so sites
// cannot match the brand list exactly. It never names a browser.
const GREASE = /^\s*not.?a.?brand\s*$/i;

/** Resolve the browser identity, or undefined when nothing identifies it. */
export function detectBrowser(nav: NavigatorLike | undefined): BrowserIdentity | undefined {
  if (!nav) return undefined;
  const rawBrands = Array.isArray(nav.userAgentData?.brands) ? nav.userAgentData!.brands! : [];
  const valid = rawBrands.filter(
    (b): b is UABrand => typeof b?.brand === 'string' && b.brand.trim() !== '',
  );
  const brands = valid.map((b) => `${b.brand} ${b.version ?? ''}`.trim());
  const original = typeof nav.userAgent === 'string' && nav.userAgent !== '' ? nav.userAgent : undefined;
  const isBrave = nav.brave != null && typeof nav.brave === 'object';

  let found = fromBrands(valid) ?? (original ? fromUserAgent(original) : undefined);
  if (!found) return undefined;
  // Brave's own object is a stronger signal than a brand list that reads like Chrome.
  if (isBrave && (found.name === CHROME || found.name === CHROMIUM)) found = { name: 'Brave', version: found.version };

  const identity: BrowserIdentity = { name: found.name, brands };
  if (found.version) identity.version = found.version;
  if (original) identity.original = original;
  return identity;
}

interface Found {
  name: string;
  version?: string;
}

function fromBrands(brands: readonly UABrand[]): Found | undefined {
  const named = brands.filter((b) => !GREASE.test(b.brand));
  if (named.length === 0) return undefined;
  // Any brand other than the engine and Chrome is the vendor naming itself
  // (Microsoft Edge, Opera, Brave, ...). Chromium shuffles brand order, so when
  // there is more than one, pick deterministically: the most specific (longest)
  // name, so "Opera GX" beats "Opera", then alphabetical.
  const vendor = named
    .filter((b) => b.brand !== CHROME && b.brand !== CHROMIUM)
    .sort((a, b) => b.brand.length - a.brand.length || a.brand.localeCompare(b.brand))[0];
  const pick =
    vendor ?? named.find((b) => b.brand === CHROME) ?? named.find((b) => b.brand === CHROMIUM);
  if (!pick) return undefined;
  return { name: pick.brand.trim(), version: pick.version || undefined };
}

// Vendor tokens first: every Chromium fork also carries "Chrome/", and Chrome
// and Edge also carry "Safari/".
const UA_RULES: Array<[RegExp, string]> = [
  [/\bEdg(?:e|A|iOS)?\/(\d+)/, 'Microsoft Edge'],
  [/\bOPR\/(\d+)/, 'Opera'],
  [/\bVivaldi\/(\d+)/, 'Vivaldi'],
  [/\bBrave\/(\d+)/, 'Brave'],
  [/\bFirefox\/(\d+)/, 'Firefox'],
  // The UA string alone cannot tell Chrome from the forks that copy it, so
  // claim only the engine.
  [/\b(?:Headless)?Chrome\/(\d+)/, CHROMIUM],
  [/\bVersion\/(\d+)[^ ]* (?:Mobile\/\S+ )?Safari\//, 'Safari'],
];

function fromUserAgent(ua: string): Found | undefined {
  for (const [re, name] of UA_RULES) {
    const m = re.exec(ua);
    if (m) return { name, version: m[1] };
  }
  return undefined;
}
