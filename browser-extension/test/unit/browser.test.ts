import { describe, it, expect } from 'vitest';
import { detectBrowser, type NavigatorLike, type UABrand } from '../../src/shared/browser.js';

// Brand lists as each browser reports them in navigator.userAgentData.brands.
// Edge and Chromium were read from a live MV3 service worker (Edge 153, Chromium
// 141); the others follow each vendor's documented UA Client Hints behavior.
const b = (brand: string, version: string): UABrand => ({ brand, version });

const CHROME_UA =
  'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36';

function nav(brands: UABrand[] | undefined, userAgent = CHROME_UA, extra: Partial<NavigatorLike> = {}) {
  return { userAgent, userAgentData: brands ? { brands } : undefined, ...extra };
}

describe('detectBrowser — UA Client Hints brands', () => {
  it('Microsoft Edge (live Edge 153 service worker)', () => {
    const id = detectBrowser(
      nav(
        [b('Microsoft Edge', '153'), b('Not_A Brand', '8'), b('Chromium', '153')],
        'Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) HeadlessChrome/153.0.0.0 Safari/537.36 Edg/153.0.0.0',
      ),
    );
    expect(id).toEqual({
      name: 'Microsoft Edge',
      version: '153',
      brands: ['Microsoft Edge 153', 'Not_A Brand 8', 'Chromium 153'],
      original:
        'Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) HeadlessChrome/153.0.0.0 Safari/537.36 Edg/153.0.0.0',
    });
  });

  it('plain Chromium (live Chromium 141 service worker)', () => {
    const id = detectBrowser(nav([b('Chromium', '141'), b('Not?A_Brand', '8')]));
    expect(id?.name).toBe('Chromium');
    expect(id?.version).toBe('141');
  });

  it('Google Chrome', () => {
    const id = detectBrowser(nav([b('Google Chrome', '124'), b('Chromium', '124'), b('Not-A.Brand', '99')]));
    expect(id?.name).toBe('Google Chrome');
    expect(id?.version).toBe('124');
  });

  it('Brave, which adds its own brand', () => {
    const id = detectBrowser(nav([b('Brave', '124'), b('Chromium', '124'), b('Not-A.Brand', '99')]));
    expect(id?.name).toBe('Brave');
  });

  it("Opera, reporting Opera's own version rather than Chromium's", () => {
    const id = detectBrowser(
      nav(
        [b('Chromium', '124'), b('Opera', '110'), b('Not-A.Brand', '99')],
        CHROME_UA + ' OPR/110.0.0.0',
      ),
    );
    expect(id?.name).toBe('Opera');
    expect(id?.version).toBe('110');
  });

  it('Opera GX: the more specific vendor brand wins', () => {
    const id = detectBrowser(nav([b('Opera', '110'), b('Chromium', '124'), b('Opera GX', '110')]));
    expect(id?.name).toBe('Opera GX');
  });

  it('an unknown fork that brands itself is reported verbatim', () => {
    const id = detectBrowser(nav([b('Chromium', '124'), b('YaBrowser', '24'), b('Not-A.Brand', '99')]));
    expect(id?.name).toBe('YaBrowser');
    expect(id?.version).toBe('24');
  });

  it('Vivaldi adds no brand, so it reports as Chromium (documented limit)', () => {
    const id = detectBrowser(nav([b('Chromium', '124'), b('Not-A.Brand', '99')]));
    expect(id?.name).toBe('Chromium');
  });

  it('Arc copies Chrome\'s brand list, so it reports as Google Chrome (documented limit)', () => {
    const id = detectBrowser(nav([b('Chromium', '124'), b('Google Chrome', '124'), b('Not-A.Brand', '99')]));
    expect(id?.name).toBe('Google Chrome');
  });

  it('is independent of brand order (Chromium shuffles it)', () => {
    const brands = [b('Microsoft Edge', '153'), b('Not_A Brand', '8'), b('Chromium', '153')];
    const names = new Set<string | undefined>();
    for (const order of [[0, 1, 2], [2, 1, 0], [1, 2, 0], [2, 0, 1]]) {
      names.add(detectBrowser(nav(order.map((i) => brands[i])))?.name);
    }
    expect([...names]).toEqual(['Microsoft Edge']);
  });

  it.each(['Not_A Brand', 'Not)A;Brand', ' Not A;Brand', 'Not-A.Brand', 'Not?A_Brand', 'Not/A)Brand', 'Not A(Brand'])(
    'never picks the GREASE brand %j',
    (grease) => {
      expect(detectBrowser(nav([b(grease, '99'), b('Chromium', '124')]))?.name).toBe('Chromium');
    },
  );

  it('keeps GREASE in browser.brands, exactly as reported (semconv takes brands verbatim)', () => {
    const id = detectBrowser(nav([b('Not)A;Brand', '99'), b('Chromium', '124')]));
    expect(id?.brands).toEqual(['Not)A;Brand 99', 'Chromium 124']);
  });
});

describe('detectBrowser — Brave without a Brave brand', () => {
  it('navigator.brave overrides a Chrome-looking brand list', () => {
    const id = detectBrowser(
      nav([b('Google Chrome', '124'), b('Chromium', '124')], CHROME_UA, { brave: { isBrave: () => true } }),
    );
    expect(id?.name).toBe('Brave');
    expect(id?.version).toBe('124');
  });

  it('navigator.brave does not override another vendor brand', () => {
    const id = detectBrowser(nav([b('Microsoft Edge', '153'), b('Chromium', '153')], CHROME_UA, { brave: {} }));
    expect(id?.name).toBe('Microsoft Edge');
  });

  it('a non-object navigator.brave is ignored', () => {
    const id = detectBrowser(nav([b('Google Chrome', '124')], CHROME_UA, { brave: true }));
    expect(id?.name).toBe('Google Chrome');
  });
});

describe('detectBrowser — UA string fallback (no UA Client Hints)', () => {
  it.each([
    ['Edge', CHROME_UA + ' Edg/120.0.0.0', 'Microsoft Edge', '120'],
    ['Opera', CHROME_UA + ' OPR/106.0.0.0', 'Opera', '106'],
    ['Vivaldi (older builds that still say so)', CHROME_UA + ' Vivaldi/6.5.3206.48', 'Vivaldi', '6'],
    ['Chrome-shaped UA: only the engine can be claimed', CHROME_UA, 'Chromium', '124'],
    [
      'Firefox',
      'Mozilla/5.0 (Macintosh; Intel Mac OS X 14.4; rv:125.0) Gecko/20100101 Firefox/125.0',
      'Firefox',
      '125',
    ],
    [
      'Safari',
      'Mozilla/5.0 (Macintosh; Intel Mac OS X 14_4) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.4 Safari/605.1.15',
      'Safari',
      '17',
    ],
  ])('%s', (_label, ua, name, version) => {
    const id = detectBrowser(nav(undefined, ua));
    expect(id?.name).toBe(name);
    expect(id?.version).toBe(version);
    expect(id?.brands).toEqual([]);
    expect(id?.original).toBe(ua);
  });

  it('falls back to the UA string when the brand list is empty', () => {
    expect(detectBrowser(nav([], CHROME_UA + ' Edg/120.0.0.0'))?.name).toBe('Microsoft Edge');
  });

  it('falls back to the UA string when the brand list is only GREASE', () => {
    const id = detectBrowser(nav([b('Not_A Brand', '8')], CHROME_UA + ' OPR/106.0.0.0'));
    expect(id?.name).toBe('Opera');
    expect(id?.brands).toEqual(['Not_A Brand 8']);
  });
});

describe('detectBrowser — nothing to go on', () => {
  it('returns undefined without a navigator', () => {
    expect(detectBrowser(undefined)).toBeUndefined();
  });

  it('returns undefined for an empty navigator', () => {
    expect(detectBrowser({})).toBeUndefined();
  });

  it('returns undefined for an unrecognizable UA and no brands', () => {
    expect(detectBrowser({ userAgent: 'curl/8.4.0', userAgentData: null })).toBeUndefined();
  });

  it('tolerates malformed brand entries', () => {
    const brands = [null, { brand: 42 }, { brand: '' }, b('Chromium', '124')] as unknown as UABrand[];
    const id = detectBrowser(nav(brands));
    expect(id?.name).toBe('Chromium');
    expect(id?.brands).toEqual(['Chromium 124']);
  });
});
