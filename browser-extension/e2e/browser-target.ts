// Resolves which browser the replay e2e launches, from the environment.
//
//   BROWSER_CHANNEL     A Playwright channel: `chromium` (the default), `msedge`,
//                       `chrome`, or a pre-release variant such as `msedge-beta`.
//                       CI runs `chromium` and `msedge`.
//   BROWSER_EXECUTABLE  An absolute path to any other Chromium-family browser
//                       binary (Brave, Opera, Vivaldi, Arc). Those forks have no
//                       Playwright channel, so this is how the manual checklist in
//                       docs/runtimes/browser-extension-chromium.mdx runs the same
//                       specs against them. It wins over BROWSER_CHANNEL.
//
// Kept apart from fixtures.ts so it can be reasoned about without a browser.

export interface BrowserTarget {
  /** Human label for logs and assertions: the channel name, or the executable. */
  label: string;
  /** Spread into chromium.launchPersistentContext(). */
  launch: { channel?: string; executablePath?: string };
  /**
   * The `user_agent.name` values the extension may report in this browser, when
   * knowable in advance. Undefined for an arbitrary executable, whose brand is
   * whatever that binary claims.
   */
  expectedUserAgentNames?: readonly string[];
}

// Playwright's `chromium` channel is a Chrome for Testing build (older
// Playwright releases shipped a raw Chromium build). Neither is Chrome-branded,
// so both should report the plain engine brand; "Google Chrome for Testing" is
// accepted in case a CfT build ever brands itself.
const EXPECTED_NAMES: Record<string, readonly string[]> = {
  chromium: ['Chromium', 'Google Chrome for Testing'],
  chrome: ['Google Chrome'],
  msedge: ['Microsoft Edge'],
};

export function resolveBrowserTarget(env: Record<string, string | undefined>): BrowserTarget {
  const executablePath = env.BROWSER_EXECUTABLE?.trim();
  if (executablePath) {
    return { label: executablePath, launch: { executablePath } };
  }
  const channel = env.BROWSER_CHANNEL?.trim() || 'chromium';
  if (!/^(chromium|chrome|msedge)(-(beta|dev|canary))?$/.test(channel)) {
    throw new Error(
      `BROWSER_CHANNEL=${channel} is not a Chromium-family Playwright channel. ` +
        'Use chromium, chrome, or msedge (optionally -beta, -dev, -canary), ' +
        'or set BROWSER_EXECUTABLE to a browser binary.',
    );
  }
  return {
    label: channel,
    launch: { channel },
    expectedUserAgentNames: EXPECTED_NAMES[channel.replace(/-(beta|dev|canary)$/, '')],
  };
}
