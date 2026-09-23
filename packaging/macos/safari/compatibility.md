# Safari compatibility of the browser collector

Researched 2026-09-23 against Apple's developer documentation, Apple Developer
Forums answers from Apple engineers, and MDN's browser-compat-data (the table
Apple's own docs point to). **Nothing here has been run in Safari yet.** Every
"supported" below means *documented as supported*. Each item names the manual
check in [`smoke-checklist.md`](smoke-checklist.md) that confirms it.

## What the extension uses, and what Safari documents

| Extension feature (from `browser-extension/src/manifest.json` and `src/`) | Safari support | Source | Verify with |
|---|---|---|---|
| Manifest V3 | Safari 15.4+ supports MV2 and MV3 | [Assessing browser compatibility][assess] | S1 |
| `background.service_worker` | Safari 15.4+ | [BCD background][bcd-bg] | S2 |
| `background.scripts` + `preferred_environment` (a fallback, if one is needed) | `scripts` Safari 14+; `preferred_environment` Safari 18+ | [BCD background][bcd-bg] | only if S5 fails |
| `content_scripts[].world: "MAIN"` (the interceptor depends on it) | **Safari 18+** | [BCD content_scripts][bcd-cs] | S3 |
| `scripting.executeScript` with `world: "MAIN"` (fallback if the manifest key misbehaves) | Safari 15.4+ | [BCD scripting][bcd-scripting] | only if S3 fails |
| `scripting.registerContentScripts` with `world` | Safari 16.4+; an Apple engineer confirmed MAIN is supported there | [BCD scripting][bcd-scripting], [forum 728849][forum-main] | only if S3 fails |
| `content_scripts[].run_at: "document_start"` | Safari 14+, **but** scripts are not injected until the user grants site access; later loads honour `run_at` | [BCD content_scripts][bcd-cs] | S3, S4 |
| `host_permissions` | Safari 15.4+, but **not granted at install**: the user grants each site (once, for a day, or always) from the toolbar, or in Safari Settings > Websites | [Managing permissions][perms] | S4 |
| `web_accessible_resources` object form (`resources`, `matches`) | Safari 15.4+. `use_dynamic_url` is always effectively on | [BCD WAR][bcd-war] | S3 |
| `options_page` | Safari 14+ | [BCD options_page][bcd-options] | S8 |
| `action.default_popup` | Safari 15.4+ | [BCD action][bcd-action] | S8 |
| `storage.local` (durable delivery queue, settings) | Supported; 5 MB limit unless `unlimitedStorage` | [Assessing browser compatibility][assess] | S7 |
| `alarms.create` / `alarms.onAlarm` (delivery retry) | Safari 14+ | [BCD alarms][bcd-alarms] | S7 |
| `runtime.sendMessage` / `onMessage` / `onInstalled` / `getPlatformInfo` / `id` | Safari 14+ | [BCD runtime][bcd-runtime] | S2, S5 |
| `chrome.*` namespace (the source calls `chrome.*` directly) | **Supported.** Safari exposes both `chrome.*` and `browser.*`, with callbacks and Promises | [Assessing browser compatibility][assess] | S1 |
| Service-worker `fetch` POST to `http://127.0.0.1:4318/v1/logs` | See "Reaching the collector" below | — | S5 |

Minimum Safari for the current manifest: **18**, because of the MAIN-world
content script. Safari 18 runs on macOS 13 Ventura and later.

## Reaching the collector

The issue expected two macOS network gates to apply. Both look unlikely to apply,
and a third gate, not in the issue, is the real risk.

1. **Local Network privacy prompt: not expected.** [TN3179][tn3179] defines a
   local network as an IP network on a broadcast-capable interface (Wi-Fi,
   Ethernet); loopback is not one. It also says, in so many words, "Traffic
   originating from WKWebView, SFSafariViewController, and Safari doesn't
   require local network access." The extension's `fetch` runs inside Safari,
   not in the wrapper app's process.
2. **App Transport Security exception (`NSAllowsLocalNetworking`): probably not
   needed.** ATS governs the app's own `URLSession` traffic. The extension's
   JavaScript runs in Safari's web and networking processes. Apple's Safari
   extension docs say nothing about ATS for extension `fetch`. If S5 shows an ATS
   error in the service worker console, add `NSAppTransportSecurity` >
   `NSAllowsLocalNetworking = YES` to the **extension target's** `Info.plist`
   and retest. Do not add it to the app's.
3. **CORS in the service worker: the real risk.** Safari bypasses CORS for
   background fetches only to hosts the extension *has been granted*
   ([forum 654839][forum-cors], Apple engineer). Host permissions start in "ask"
   mode in Safari ([Managing permissions][perms]). And there is a reported
   Safari bug where the bypass works for background *pages* but not background
   *service workers* ([repro repo][sw-cors-bug]; the same forum thread suggests
   `background.scripts` as the workaround). The collector's OTLP/HTTP receiver
   has no CORS configuration. The POST sends `content-type: application/json`,
   which forces a preflight. So an ungranted or unbypassed request fails with
   "Fetch API cannot load http://127.0.0.1:4318/v1/logs due to access control
   checks", and nothing is captured. Mitigations, in order of preference:
   - Grant `127.0.0.1` site access, either by the user or through MDM
     `AllowedDomains` (see [`mdm/`](mdm/)). This is the first thing to try in S5.
   - If the service worker still fails once access is granted, ship a Safari
     manifest variant with `background.scripts` (plus
     `preferred_environment: ["document", "service_worker"]`). The per-target
     build merged in #628 makes that a derived-manifest function on the `safari`
     entry in `browser-extension/tools/targets.mjs`, which today ships the
     manifest unchanged.
   - Last resort: add a `cors.allowed_origins` entry for
     `safari-web-extension://*` to the collector's OTLP HTTP receiver.
     Safari changes the extension's origin UUID on every launch
     ([forum 654839][forum-cors]), so an exact origin cannot be pinned. A wildcard
     would let any Safari extension post to the collector, so treat this as a
     security change, not a packaging tweak.

## Gap list

Blocking. Must be confirmed on a Mac before Safari counts as supported:

- **G1: collector reachability (S5).** The CORS and host-grant behaviour above.
  If this fails, nothing is captured.
- **G2: MAIN-world interceptor (S3).** Documented for Safari 18, but not yet
  observed teeing `window.fetch` on claude.ai and chatgpt.com in Safari.
- **G3: site-access grants.** Content scripts do not run on claude.ai or
  chatgpt.com until the user grants access. Unmanaged users need an onboarding
  step. The host-permission banner merged in #628 (`src/shared/permissions.ts`,
  shown in the popup and options page) may cover that, since it asks through
  `permissions.request`. How Safari answers that call from an extension page has
  not been tested, so S4 must check it. Managed fleets can pre-grant with MDM
  `AllowedDomains`, but only on supervised macOS 15+ (see
  [`README.md`](README.md#mdm)).

Non-blocking differences:

- **G4: service-worker lifetime.** Safari's MV3 background is non-persistent,
  like Chrome's. The in-memory assembler has the same mid-stream-suspension
  limitation the README already lists. Safari's idle timeout is not documented.
- **G5: no `update_url`.** Apple says to deliver Safari extension updates
  through the App Store ([Assessing browser compatibility][assess]). A
  Developer ID build updates only when a newer app is installed, by a package or
  by MDM.
- **G6: sites' streaming in WebKit.** claude.ai and chatgpt.com may serve WebKit a
  different transport, such as XHR or a different SSE framing. The adapters are
  fixture-tested on Chromium captures only. Record a Safari fixture if S4 or S6
  shows different traffic.
- **G7: no automated in-browser test.** Playwright cannot load extensions into
  WebKit, and `safaridriver` cannot install them. The replay e2e stays
  Chromium-only. Safari is covered by the unit tests, which are
  browser-agnostic, plus the manual checklist.
- **G8: browser identity.** Safari events carry the same `claude_web` and
  `chatgpt_web` harness names. The browser-identity attribute is #394's work.

Not needed for Safari:

- **A `browser.*` shim.** Safari supports `chrome.*` natively. The shim merged
  in #628 (`src/shared/browser.ts`) prefers `browser.*` where it exists, which is
  harmless in Safari but not required.
- **The Local Network usage string (`NSLocalNetworkUsageDescription`).** See
  "Reaching the collector" above.

## Sources

- [Assessing your Safari web extension's browser compatibility][assess]
- [Managing Safari web extension permissions][perms]
- [Packaging a web extension for Safari][packaging]
- [Running your Safari web extension][running]
- [TN3179: Understanding local network privacy][tn3179]
- [Safari 18 release notes][safari18]: Device Management of extension state, private browsing, and website access
- MDN browser-compat-data: [manifest `background`][bcd-bg], [manifest `content_scripts`][bcd-cs], [manifest `web_accessible_resources`][bcd-war], [manifest `options_page`][bcd-options], [manifest `action`][bcd-action], [`scripting`][bcd-scripting], [`alarms`][bcd-alarms], [`runtime`][bcd-runtime]
- Apple Developer Forums: [world MAIN support (728849)][forum-main], [background CORS (654839)][forum-cors]
- [Safari service-worker background CORS repro][sw-cors-bug]

[assess]: https://developer.apple.com/documentation/safariservices/assessing-your-safari-web-extension-s-browser-compatibility
[perms]: https://developer.apple.com/documentation/safariservices/managing-safari-web-extension-permissions
[packaging]: https://developer.apple.com/documentation/safariservices/packaging-a-web-extension-for-safari
[running]: https://developer.apple.com/documentation/safariservices/running-your-safari-web-extension
[tn3179]: https://developer.apple.com/documentation/technotes/tn3179-understanding-local-network-privacy
[safari18]: https://developer.apple.com/documentation/safari-release-notes/safari-18-release-notes
[bcd-bg]: https://github.com/mdn/browser-compat-data/blob/main/webextensions/manifest/background.json
[bcd-cs]: https://github.com/mdn/browser-compat-data/blob/main/webextensions/manifest/content_scripts.json
[bcd-war]: https://github.com/mdn/browser-compat-data/blob/main/webextensions/manifest/web_accessible_resources.json
[bcd-options]: https://github.com/mdn/browser-compat-data/blob/main/webextensions/manifest/options_page.json
[bcd-action]: https://github.com/mdn/browser-compat-data/blob/main/webextensions/manifest/action.json
[bcd-scripting]: https://github.com/mdn/browser-compat-data/blob/main/webextensions/api/scripting.json
[bcd-alarms]: https://github.com/mdn/browser-compat-data/blob/main/webextensions/api/alarms.json
[bcd-runtime]: https://github.com/mdn/browser-compat-data/blob/main/webextensions/api/runtime.json
[forum-main]: https://developer.apple.com/forums/thread/728849
[forum-cors]: https://developer.apple.com/forums/thread/654839
[sw-cors-bug]: https://github.com/JamiesWhiteShirt/safari-service-worker-background-bug
