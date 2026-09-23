# Agentic AI browsers: what Beacon can see, and how

Research findings for [#395](https://github.com/Asymptote-Labs/agent-beacon/issues/395):
ChatGPT Atlas (and what replaced it), Perplexity Comet, Dia, plus short notes on the
assistants built into Edge and Chrome.

This is an internal design note, not user documentation, so it lives next to the extension
rather than in the published `docs/` site. Nothing in it changes what the extension does.

## Status of this document

- **Desk research only.** No agentic browser was installed or run for this. Every statement
  about a browser's behaviour comes from vendor pages, news reports, or security write-ups, and
  is marked **[unverified]** until someone runs the checklist at the end of that browser's
  section.
- **The sources were found by web search, and the pages were not opened.** The research
  environment's egress proxy blocked direct fetches of every vendor and news domain it tried
  (openai.com, help.openai.com, perplexity.ai, diabrowser.com, techcrunch.com, wikipedia.org,
  developer.chrome.com). The citations below are the URLs the search engine returned, and each
  claim is from the search engine's summary of that page. Re-read the page before relying on a
  claim, especially quoted dates and feature names. All sources were accessed on 2026-09-23.
- **The statements about how the extension works come from this repository** (`src/manifest.json`,
  `src/interceptor/interceptor.ts`, `src/content/content.ts`, `src/background/`). They are
  accurate for `main` at the time of writing.

## Summary

| Browser | Status on 2026-09-23 | Does today's extension load on claude.ai / chatgpt.com tabs? | Can an extension see the built-in agent's model traffic? | Best route to the agent's activity |
|---|---|---|---|---|
| ChatGPT Atlas | **Shut down on 2026-08-09.** Replaced by the ChatGPT desktop app's built-in browser and the official ChatGPT extension for Chrome and other Chromium browsers | Moot for Atlas. For the desktop app's browser: probably, in ordinary tabs, if an unpacked or self-hosted extension can be installed there **[unverified]** | **No.** The chat surface is native, and autonomous tasks reportedly run in a cloud browser on OpenAI's servers | Vendor-side: OpenAI workspace controls and Compliance API. On the endpoint, only the agent's effects in local tabs |
| Perplexity Comet | Shipping on macOS, Windows, Android, iOS | Very likely: Comet accepts Chrome Web Store extensions and Chrome policies **[unverified]** | **No for agent actions**: they run through a hidden built-in extension over a WebSocket. **Unclear for the sidebar's chat stream**, which is SSE and may be a perplexity.ai page | Comet Enterprise audit logs (vendor-side). An extension could see effects, and possibly the sidebar chat |
| Dia | Shipping on macOS. Windows planned for autumn 2026 | Very likely: Dia accepts Chrome Web Store extensions **[unverified]** | **No.** The assistant is reported to be a separate native process that talks to The Browser Company's servers | Nothing vendor-side is documented. On the endpoint: effects, and possibly Dia's local chat history |
| Edge Copilot Mode | Shipping (Actions in limited preview) | Yes, and that is #394's scope | **No.** Built into the browser | Microsoft Purview and Edge policies |
| Chrome with Gemini | Shipping (auto browse) | Yes, Chrome is the supported target | **No.** Built into the browser | Chrome Enterprise policies and reporting |

The pattern holds across all five. **None of these assistants makes its model calls from a web
page that an extension can inject into.** Their prompts and responses cannot be captured the way
claude.ai and chatgpt.com are captured today. What an extension can see is the agent's *effects*
on ordinary tabs, and it cannot reliably tell those from what the user did.

**Recommendation:**

1. Document that the existing extension works in Comet and Dia (and, once checked, in the ChatGPT
   desktop app's browser) for ordinary claude.ai and chatgpt.com tabs, after someone runs the checklists.
2. Do **not** build agent capture into `browser-extension/`. The only part an extension can reach
   is effects capture, and that turns a three-origin extension into a browsing-history collector.
   CLAUDE.md lists that as a current non-goal ("general browser or SaaS activity monitoring beyond
   the supported chat surfaces"). If the product decides to do it anyway, it should be a separate
   build with its own consent flow (see [Effects-based capture](#effects-based-capture-and-its-privacy-cost)).
3. The richest agent records live with the vendors (Comet Enterprise audit logs, OpenAI's
   Compliance API). Ingesting them is "cloud audit ingestion", also a current non-goal, so that
   needs its own product decision rather than an adapter.

## How the extension works, and why that decides the answer

Three facts about the current extension settle most of the per-browser questions:

1. **It only runs in pages whose URL matches its content-script patterns.** `src/manifest.json`
   declares two content scripts: `interceptor.js` in the `MAIN` world and `content.js` in the
   `ISOLATED` world. Both match only `*://claude.ai/*`, `*://chatgpt.com/*` and
   `*://chat.openai.com/*`, and both run at `document_start` in the top frame only (no
   `all_frames`). The host permissions are the same three origins plus the loopback collector.
2. **It captures by wrapping `window.fetch` in the page.** `interceptor.js` wraps
   `window.fetch`, uses `looksLikeChatRequest()` to decide which requests to tee, and relays the
   cloned response stream over `window.postMessage`. A request the page never makes through
   `window.fetch` is invisible to it. That includes XHR, WebSocket, native code, another
   extension's pages, and a browser-internal WebUI.
3. **Adapters are chosen by page host.** `content.js` sends `window.location.host`, and
   `adapterForHost()` in the service worker picks the Claude or ChatGPT adapter. A host with no
   adapter is dropped in `Assembler.handle`.

Chromium never lets an extension inject content scripts into `chrome://` pages (or a fork's
equivalent, such as `comet://`), `chrome-untrusted://` pages, other extensions' pages, or the Web
Store, and enterprise policy can block more hosts through `runtime_blocked_hosts`
([MV3 restricted-URL notes](https://mv3-extension.com/core-apis-cross-browser-data-management/tabs-api-window-management/handling-restricted-urls-and-tab-permissions/);
[Chrome content scripts guide](https://developer.chrome.com/docs/extensions/develop/concepts/content-scripts)).
A browser-native assistant therefore falls into one of four cases:

- **Native UI** (Swift, C++, a Views panel). No page exists to inject into.
- **WebUI or built-in extension page** (`comet://…`, `chrome-extension://<vendor id>/…`). A page
  exists, but Chromium forbids injecting into it.
- **Server-side browser** (the agent drives a browser in the vendor's cloud). Nothing on the
  endpoint to observe.
- **An ordinary https page in a side panel.** This is the only case where our approach could
  work, and it would still need a new host permission, a new match pattern and a new adapter.

The `MAIN`-world `world` key needs Chromium 111 or later
([w3c/webextensions#485](https://github.com/w3c/webextensions/issues/485)). Every browser here
is far past that: Dia reportedly moved to Chromium 149 in June 2026
([Dia changelog](https://www.diabrowser.com/changelog); [Releasebot, Dia](https://releasebot.io/updates/dia)).
The engine version is not a blocker for any of them.

**Installing it is the practical blocker.** The extension is not on the Chrome Web Store. The
README's install path is Developer mode and "Load unpacked", and the release workflow publishes
a zip for the same flow. Whether each browser exposes Developer mode, or honours
`ExtensionInstallForcelist` with a self-hosted update URL, is the first thing every checklist
below verifies.

---

## ChatGPT Atlas, and what replaced it

### Status

Atlas was a Chromium-based browser for macOS, launched on 2025-10-21. OpenAI announced on
2026-07-09 that it would be discontinued, and **it stopped working on 2026-08-09**. OpenAI moved
its capabilities into two products:

- the **ChatGPT desktop app**, which has a built-in browser. It reportedly added Chrome extension
  support and optional full Chrome DevTools Protocol (CDP) access on 2026-09-18, and runs
  autonomous tasks in a cloud browser on OpenAI's servers;
- the official **ChatGPT extension** for Chrome, Edge, Brave, Opera and Vivaldi, which runs a
  side panel and can drive the browser in tab groups.

Sources: [OpenAI Help, "Evolving Atlas into ChatGPT for browser-based agentic work"](https://help.openai.com/en/articles/20001371-evolving-atlas-into-chatgpt-for-browser-based-agentic-work);
[TechCrunch, 2026-07-09](https://techcrunch.com/2026/07/09/openai-is-shutting-down-atlas-but-its-ai-browser-ambitions-are-still-growing/);
[ecorpit, shutdown and migration paths](https://ecorpit.com/chatgpt-atlas-shutdown-browser-agent-migration-2026/);
[Crypto Briefing, extension support in the desktop browser](https://cryptobriefing.com/openai-chatgpt-chrome-extension-support/);
[Runtime Wire, same](https://runtimewire.com/article/openai-chatgpt-desktop-browser-chrome-extensions);
[ChatGPT Learn, Browser](https://learn.chatgpt.com/docs/browser);
[ChatGPT Learn, Browser extension](https://learn.chatgpt.com/docs/chrome-extension).

The Atlas findings are kept because they explain the shape of the successor, and because the
question the issue raised about Atlas is the same one the desktop app raises.

### Extension support

- **Atlas (historical):** accepted Chrome Web Store extensions, managed from the profile menu,
  with an explicit import of extensions from Chrome added in 2026
  ([Tactiq](https://tactiq.io/learn/chrome-extensions-chatgpt-atlas); [OpenAI Help, Atlas release notes](https://help.openai.com/en/articles/12591856-chatgpt-atlas-release-notes)).
  For enterprises it read managed preferences under `com.openai.atlas.web`, and "many other
  Chromium-style keys are expected to work"
  ([OpenAI Help, Atlas for Enterprise](https://help.openai.com/en/articles/12603091-chatgpt-atlas-for-enterprise)). **[unverified]**
- **ChatGPT desktop app's browser:** Chrome extensions reportedly installable and pinnable since
  2026-09-18. OpenAI reportedly says the agent "cannot interact with extensions or their data"
  (Runtime Wire, Crypto Briefing above). Zscaler says its own browser extension, deployed by MDM,
  enforces policy inside this embedded browser, which suggests MDM force-install works there
  ([Zscaler, "How to Secure ChatGPT's Embedded Browser"](https://www.zscaler.com/blogs/product-insights/chatgpt-desktop-app-includes-app-browser-how-do-you-secure-embedded-browsers)).
  Whether Developer mode and Load unpacked exist there is unknown. **[unverified]**

### Would our content scripts load?

- **Ordinary claude.ai or chatgpt.com tabs:** yes, if the extension installs. They are normal
  https pages, and the match patterns fire on them just as in Chrome. **[unverified]**
- **The ChatGPT chat surface itself:** no. This is the issue's wrinkle, and the evidence supports
  it. In Atlas the UI was rebuilt natively in SwiftUI, AppKit and Metal. Chromium ran out of
  process ("OWL"), with the Swift app as a Mojo client of the Chromium browser process
  ([OpenAI engineering, "How we built OWL"](https://openai.com/index/building-chatgpt-atlas/);
  [ByteByteGo summary](https://blog.bytebytego.com/p/the-architecture-behind-atlas-openais)).
  The Ask ChatGPT sidebar was therefore not a `chatgpt.com` tab, and `*://chatgpt.com/*` would
  never have matched it. The desktop app inherits the same design: the conversation happens in
  the native app, and the browser is a pane. **[unverified for the desktop app]**

### Is the agent's model traffic visible to an extension?

**No.**

- The sidebar and agent-mode model calls come from the native app or OpenAI's backend, not from a
  page's `window.fetch`. There is nothing for `interceptor.js` to wrap.
- Autonomous work in the desktop app reportedly runs in a cloud browser on OpenAI's servers
  ([ecorpit](https://ecorpit.com/chatgpt-atlas-shutdown-browser-agent-migration-2026/)). Nothing on
  the endpoint sees those page loads, let alone the prompts. **[unverified]**
- **The official ChatGPT extension for Chrome** is the most relevant successor for Beacon, because
  it acts inside the user's own Chrome, next to our extension. Its listing reportedly requests the
  debugger, all-sites access, history, downloads, bookmarks, native messaging and tab groups, and
  its tasks run in tab groups
  ([ChatGPT Learn, Chrome extension](https://learn.chatgpt.com/docs/chrome-extension.md);
  [UtilEngine](https://utilengine.com/blog/chatgpt-chrome-extension/)). Its model traffic starts in
  its own `chrome-extension://` side panel and service worker, which we cannot inject into. The
  pages it drives are ordinary tabs, though. If it opens claude.ai or chatgpt.com, our content
  scripts load there as usual. On any other site we see nothing, because the extension holds no
  host permission there. **[unverified]**

### Privacy trade-offs of capturing agent actions

The general analysis is in [Effects-based capture](#effects-based-capture-and-its-privacy-cost).
Two points are specific to this browser:

- Tasks run in the vendor's cloud browser, so effects capture on the endpoint misses them entirely.
  An effects collector here pays the full browsing-history cost for partial coverage.
- A Chrome agent that drives tabs through the debugger produces input that looks like the user's
  own at the DOM level. Attribution would rest on heuristics, such as membership of a tab group the
  agent created.

### Alternative collection paths

- **OpenAI workspace controls.** Admins can disable the in-app browser, restrict site access,
  uploads, downloads, history access and CDP, and set site rules that users cannot override. The
  Compliance Logs Platform and Compliance API cover audit, SIEM, eDiscovery and DLP
  ([ChatGPT Learn, Work admin FAQ](https://learn.chatgpt.com/docs/enterprise/work-admin-faq);
  [OpenAI Help, Enterprise and Edu release notes](https://help.openai.com/en/articles/10128477-chatgpt-enterprise-and-edu-release-notes)).
  This is where agent activity is actually recorded. For Beacon it would be cloud audit ingestion
  (a non-goal). **[unverified]**
- **CDP.** The desktop app exposes CDP to *ChatGPT* as an opt-in developer setting, not as a port
  another process can attach to (ChatGPT Learn, Browser). Separately, Chrome 136 and later ignore
  `--remote-debugging-port` on the default profile
  ([Chrome for Developers blog](https://developer.chrome.com/blog/remote-debugging-port)), so
  attaching to a user's real browser over CDP is a dead end by design, and should be.
- **Network or OS level.** TLS interception at a proxy sees the endpoints being called, but it
  sits outside Beacon's local-only posture and is fragile against pinned apps.

### Recommendation

Drop Atlas from scope. Treat the **ChatGPT desktop app's browser** as another Chromium host for
the *existing* capture: run the checklist below, and if the extension installs, document it.
Record the official ChatGPT Chrome extension as the agent that most often acts next to ours, and
note that we only see it on the three chat origins.

### Manual verification checklist (ChatGPT desktop app)

1. Open the built-in browser's extensions page. Record whether Developer mode and Load unpacked
   exist. If they do, load `browser-extension/dist/`.
2. If not, on an MDM-managed Mac, try force-installing through the managed-preferences domain the
   app documents (record which domain it is), using `ExtensionInstallForcelist` and a self-hosted
   update URL.
3. With the Beacon endpoint agent running, open `https://claude.ai` in the built-in browser and
   send a message. Check `runtime.jsonl` for a `claude_web` `prompt.submitted` and an
   `agent.response.completed`. Repeat on `https://chatgpt.com` for `chatgpt_web`.
4. Send a message in the app's own ChatGPT conversation pane. Confirm that **nothing** is captured,
   which is the expected result. Record the pane's URL or type if the inspector exposes one.
5. Ask the agent to open claude.ai and send a message there. Record whether a `claude_web` event
   appears, and whether it can be told apart from a user-sent one (it should not be).
6. In Chrome with the official ChatGPT extension installed next to ours, repeat step 5 with a
   Chrome task. Record the tab group name.
7. Note the app version and the Chromium version from the user agent string.

---

## Perplexity Comet

### Status and extension support

Comet is Perplexity's Chromium-based browser. It launched for Windows and macOS on 2025-07-09 and
later shipped on Android (2025-11-20) and iOS (2026-03-18)
([Wikipedia, Comet](https://en.wikipedia.org/wiki/Comet_(browser))). It accepts Chrome Web Store
extensions, managed at `comet://extensions/`
([Cybernews review](https://cybernews.com/ai-tools/perplexity-comet-review/);
[Supercharge, Comet vs Chrome](https://www.superchargebrowser.com/library/perplexity-comet-vs-chrome-extensions/)).
Comet Enterprise supports more than 500 Chromium policies, including extension blocklist,
allowlist and force-install. Admins reuse existing Chrome MDM configuration by changing the
domain from `com.google.Chrome` to `ai.perplexity.comet`
([Perplexity Help, Comet Policies and Controls](https://www.perplexity.ai/help-center/en/articles/13529668-comet-policies-and-controls);
[Perplexity Help, Installing Comet for Enterprise (macOS)](https://www.perplexity.ai/help-center/en/articles/13528679-installing-comet-for-enterprise-macos)).
**[unverified]**

### Would our content scripts load?

**Very likely yes** on ordinary claude.ai and chatgpt.com tabs. The desktop browser behaves like
Chrome for extensions, and `ExtensionInstallForcelist` under `ai.perplexity.comet` is the natural
enterprise install path for an off-store extension. Mobile Comet is out of scope, since Chromium on
Android and iOS does not run desktop extensions. **[unverified]**

### Is the agent's model traffic visible to an extension?

**Agent actions: no. The sidebar's chat stream: unclear, and worth one hands-on check.**

Zenity Labs reverse-engineered Comet
([Zenity Labs, "Perplexity Comet: A Reversing Story"](https://labs.zenity.io/post/perplexity-comet-a-reversing-story)).
In its account, the assistant ("Sidecar") receives the conversation as an **SSE stream** (model
reasoning, citations, answers). When the backend wants the browser to act, the stream carries an
`entropy_request`. The Sidecar passes it to a built-in **`comet-agent` extension** through
Chrome's extension messaging API, and that extension opens its own **WebSocket** to the backend
for RPC, screenshots and action results. SquareX reports that Comet's embedded "Agentic" and
"Analytics" extensions are hidden from the extensions page and cannot be disabled, and that the
agentic one can reach a privileged `chrome.perplexity.mcp.addStdioServer` API
([SquareX Labs](https://labs.sqrx.com/comet-mcp-api-allows-ai-browsers-to-execute-local-commands-dec185fb524b);
[SquareX](https://sqrx.com/comet-mcp-api-vulnerability)). **[unverified]**

What that means for us:

- **The action channel is invisible.** It is a WebSocket owned by a component extension. We cannot
  inject into another extension's pages or worker, and `interceptor.js` does not wrap WebSockets
  anyway.
- **The conversation channel may be visible.** The Sidecar consumes SSE, as perplexity.ai's web app
  does, and SquareX describes the privileged API as reachable from "the Perplexity webpage". If
  the Sidecar is an ordinary `https://www.perplexity.ai/...` document, a content script matching
  that origin could tee it in the same way we tee chatgpt.com. That needs, in order: confirming
  the Sidecar's document URL and scheme; confirming Comet does not block third-party injection into
  it (it may treat perplexity.ai as a protected host); a new host permission and match pattern; and
  a new Perplexity adapter. It would also widen the extension's scope to a fourth chat origin,
  which is a product decision under CLAUDE.md. That decision is simple for perplexity.ai in a
  normal tab, and harder for a sidebar that sees every page the user visits. **[unverified]**

### Privacy trade-offs of capturing agent actions

- The Sidecar reads the current page and can act across tabs. That is the reason for Brave's and
  Zenity's indirect prompt-injection findings: Comet fed page content to the model without
  separating it from user instructions, and a summarised page could make it open Gmail and send
  data out ([Brave, "Agentic Browser Security: Indirect Prompt Injection in Perplexity Comet"](https://brave.com/blog/comet-prompt-injection/);
  [Brave, "Unseeable prompt injections in screenshots"](https://brave.com/blog/unseeable-prompt-injections/);
  [Zenity, PleaseFix](https://zenity.io/research/pleasefix-vulnerabilities)). Capturing the
  Sidecar's stream would therefore record page content from arbitrary sites that the user
  attached, as well as chat. With the `full` retention default, that is far broader than
  claude.ai and chatgpt.com chat.
- Effects capture has the general cost described below.

### Alternative collection paths

- **Comet Enterprise audit logs.** Admins can restrict agent actions, require approvals, review
  per-session action logs, and export audit logs (user queries, agentic actions, file access,
  connector usage) to Splunk, Sentinel or Datadog. This is gated on 50 or more Enterprise Pro seats
  or one Enterprise Max seat
  ([Perplexity Help, Comet for Enterprise](https://www.perplexity.ai/help-center/en/articles/12781449-comet-for-enterprise);
  [Perplexity Help, Comet Telemetry](https://www.perplexity.ai/help-center/en/articles/13617928-comet-telemetry);
  [TestingCatalog](https://www.testingcatalog.com/perplexity-launches-comet-enterprise-with-advanced-security/)).
  This is the authoritative record of what the agent did, but it is cloud audit ingestion.
  **[unverified]**
- **Policy.** The 500+ Chromium policies can block domains for the agent, and `ExtensionSettings`
  can deploy our extension. Whether policy can remove the hidden component extensions is unknown.
- **CDP or native messaging.** CDP has the same default-profile restriction as Chrome (assuming
  Comet tracks upstream), and native messaging only connects our own extension to a local host, so
  neither adds visibility.

### Recommendation

Document baseline capture in Comet after the checklist. Do not propose agent capture. As a
separate, small follow-up, decide whether to spend the one hands-on check needed to learn whether
the Sidecar is an injectable perplexity.ai document. If it is, a Perplexity adapter is a scoping
decision, not an architecture change.

### Manual verification checklist (Comet)

1. Open `comet://extensions`, turn on Developer mode, and load `browser-extension/dist/`. Record
   whether it loads without warnings.
2. With the Beacon endpoint agent running, send a message on `https://claude.ai` and on
   `https://chatgpt.com` in normal tabs. Confirm `claude_web` and `chatgpt_web` events in
   `runtime.jsonl`.
3. On a managed Mac, push `ExtensionInstallForcelist` under `ai.perplexity.comet` with a
   self-hosted update URL, and record whether it installs.
4. Open the Assistant (Sidecar). Right-click it and choose Inspect, or use `comet://inspect`.
   Record the Sidecar document's URL and scheme (`https://www.perplexity.ai/...`, `comet://...` or
   `chrome-extension://...`).
5. If step 4 shows an https perplexity.ai document: temporarily add that origin to a *local* copy
   of the manifest's `matches` and `host_permissions`, reload, and check in the Sidecar's DevTools
   console whether `window.__beaconInterceptorInstalled` is `true`. Do not commit that change.
6. Check whether the hidden component extensions appear anywhere (`comet://extensions`,
   `comet://system`), and record their IDs if shown.
7. Run a small agent task ("open example.com and click the first link") and record whether the
   navigation shows up as an ordinary tab navigation in the Sidecar's inspector.
8. Record the Comet version and the Chromium version from the user agent string.

---

## Dia

### Status and extension support

Dia is The Browser Company's Chromium-based browser, written in Swift. The company is now an
Atlassian subsidiary (acquisition announced on 2025-10-21, reportedly $610M). Dia is macOS only,
requiring Apple silicon and macOS 14 or later, and a Windows release is planned for autumn 2026
([Wikipedia, Dia](https://en.wikipedia.org/wiki/Dia_(web_browser));
[PiunikaWeb, 2026-07-30](https://piunikaweb.com/2026/07/30/dia-windows-slated-officially-launch-fall-2026/);
[Sigma, Dia alternatives](https://www.sigmabrowser.com/blog/5-best-dia-browser-alternatives-in-2026-which-is-best)).
It accepts Chrome Web Store extensions, gained Chromium Side Panel API support in v1.10.1
(2025-12-17), and announced a Manifest V2 phase-out for early 2026. Extensions are treated as a
secondary feature, with manual reinstall and no automatic migration
([SupaSidebar, Dia status tracker](https://supasidebar.com/blog/dia-browser-status-tracker);
[Supercharge, Dia vs Chrome extensions](https://www.superchargebrowser.com/library/dia-browser-vs-chrome-extensions/)).
For enterprises, Dia supports standard Chromium policies, including Chrome Cloud Management, plus
Dia-specific MDM policies such as per-site AI disablement
([Dia for Work](https://www.diabrowser.com/forwork); [Dia Security](https://www.diabrowser.com/security);
[Own the Browser, Dia configuration](https://ownthebrowser.com/browsers/dia/configuration/)).
**[unverified]**

### Would our content scripts load?

**Very likely yes** on ordinary claude.ai and chatgpt.com tabs. Our extension is MV3, so the MV2
phase-out does not affect it. Dia's managed-preferences domain is not documented in the sources
found, so the checklist has to discover it. **[unverified]**

### Is the agent's model traffic visible to an extension?

**No.** The assistant sidebar is reported to be a separate process that talks to a hosted backend.
When the user adds a tab as context, Dia sends the page content (or a summary) to model providers
through The Browser Company's servers, under zero-data-retention agreements
([Wikipedia, Dia](https://en.wikipedia.org/wiki/Dia_(web_browser)); [Dia Security](https://www.diabrowser.com/security)).
The calls do not go to claude.ai or chatgpt.com, and do not come from a web page. Even if they did,
our adapters only parse those two sites' private stream formats. **[unverified]**

Dia is also the least agentic of the three. Coverage describes chat with tab context, Memory,
Skills (prompt templates) and work integrations (Slack, Notion, Google Calendar, Gmail), not
autonomous multi-step browsing of the kind Atlas and Comet offer
([PiunikaWeb](https://piunikaweb.com/2026/06/16/dia-for-windows-browser-company-slack-veteran/);
[Avaratak](https://www.avaratak.com/blog/out-of-the-tab-forest-atlassian-dia-browser)). Effects
capture has little to observe here. **[unverified]**

### Privacy trade-offs of capturing agent actions

The main risk is not actions but **context**: Dia defaults to having the user's open tabs as
context, and Memory summarises browsing history. Anything that captured Dia's model inputs would
capture that browsing history as a side effect. Effects capture has the general cost described
below, with little agent activity to justify it.

### Alternative collection paths

- **Local chat history.** Dia's security page says chat history "lives on your machine" by
  default. That is the same shape as the runtimes Beacon already backfills by reading local records
  (`beacon endpoint fx sync`, `cline sync`, `copilot sync`). If the store is readable and not
  encrypted with a key Beacon should not touch, a read-only poll collector could record Dia
  conversations with `harness.collection_method=poll`, without going through the browser at all.
  This is the most promising non-extension path found for any of the three browsers, but the
  store's location, format and encryption are all unknown. **[unverified]**
- **Vendor-side:** no audit-log or compliance export is documented in the sources found.
- **Policy:** Dia's AI-disablement MDM policy is a control, not a telemetry source.

### Recommendation

Document baseline capture in Dia after the checklist. Do not propose agent capture through the
extension. File a follow-up to investigate Dia's local chat-history store as a candidate for a
`sync`-style poll collector, which fits Beacon's existing patterns better than anything in the
browser.

### Manual verification checklist (Dia)

1. Open Dia's extensions page and record its internal URL. Turn on Developer mode if it exists, and
   load `browser-extension/dist/`.
2. With the Beacon endpoint agent running, send a message on `https://claude.ai` and on
   `https://chatgpt.com`. Confirm `claude_web` and `chatgpt_web` events in `runtime.jsonl`.
3. Discover Dia's managed-preferences domain (from the app bundle ID, for example
   `defaults read /Applications/Dia.app/Contents/Info.plist CFBundleIdentifier`), push
   `ExtensionInstallForcelist` to it on a managed Mac, and record the result.
4. Ask the Dia assistant a question with a claude.ai tab attached as context. Confirm that
   **nothing** is captured, which is the expected result.
5. Find where Dia keeps chat history under `~/Library/Application Support/` (search for a
   directory named after the bundle ID). Record the file names, formats (SQLite, LevelDB, JSON),
   and whether contents look encrypted. Do not copy real conversations anywhere.
6. Record the Dia version and the Chromium version from the user agent string.

---

## Edge Copilot Mode and Chrome's Gemini (brief)

Both are built into the browser, so the answer to "can an extension see the model traffic" is
**no** for the same structural reasons. Neither is a web page on an origin we match.

- **Edge.** Copilot Mode, Actions and Journeys are in limited preview. Admins control them with the
  `CopilotCoworkToolActionsEnabled` and `EdgeEntraCopilotPageContext` policies, plus Purview DLP
  for labelled content ([Microsoft Learn, Configuring Copilot Mode](https://learn.microsoft.com/en-us/DeployEdge/microsoft-edge-management-service-copilot-mode);
  [Microsoft Learn, CopilotCoworkToolActionsEnabled](https://learn.microsoft.com/en-us/deployedge/microsoft-edge-policies/copilotcoworktoolactionsenabled)).
  Baseline capture in Edge belongs to #394.
- **Chrome.** Gemini in Chrome and auto browse are controlled with the `GeminiSettings` and
  `GenAiDefaultSettings` policies, with DLP for agentic workflows under Chrome Enterprise
  ([Google Cloud blog, Gemini in Chrome Enterprise](https://cloud.google.com/blog/products/chrome-enterprise/supercharging-employee-productivity-with-ai-securely-with-gemini-in-chrome-enterprise);
  [Google Cloud blog, Future Mode part 2](https://cloud.google.com/blog/products/chrome-enterprise/future-mode-part-2-the-foundation-for-securing-agentic-browsing)).

**[unverified]** Both vendors' visibility tooling is part of their enterprise consoles, and that
is where an incident responder will find agent activity for these two.

---

## Effects-based capture and its privacy cost

Where the model traffic is out of reach, the issue's fallback is to capture what the agent *does*.
This is what that would take in Chromium MV3, and what it would cost.

### What each piece needs

| Signal | API and permission | Install warning Chrome shows | Can it tell the agent from the user? |
|---|---|---|---|
| Top-level and frame navigations (URL, transition type) | `webNavigation` | "Read your browsing history" | No. An agent's navigation and a user's click look the same. The transition type is at best a weak hint |
| Tab URL and title changes | `tabs` | "Read your browsing history" | No, apart from heuristics (a new tab group created by the agent, a background tab) |
| Form submissions and field values | Content script on every site, meaning `<all_urls>` host permission | "Read and change all your data on all websites" | No. Debugger-driven input is dispatched as trusted input. Synthetic `el.click()` gives `isTrusted === false`, but agents that use CDP input will not produce that |
| Downloads and uploads | `downloads` and content script | "Manage your downloads" | No |

The agent's identity is the main gap. Of the agents covered here, only the official ChatGPT
extension drives the user's own Chrome, through the debugger. Chromium does not label that input as
agent-originated for other extensions. An effects collector would therefore record *everything* the
browser does, and leave attribution to downstream heuristics.

### What it costs

- **It becomes a different product.** The README's current claim is that the extension "has no
  access to other tabs, browsing history, or page content elsewhere." That claim would be false.
  With `retention: 'full'` as the default, form values on every site (addresses, messages, internal
  tool inputs) would reach `runtime.jsonl` and every configured shipper.
- **It conflicts with a stated non-goal.** CLAUDE.md lists "general browser or SaaS activity
  monitoring beyond the supported chat surfaces" as a non-goal.
- **The consent conversation changes.** Full-history collection on a managed device is normally
  justified by an employer's policy and disclosed through an HR process. The current extension's
  "turn it on and you will see your own chats" framing does not cover it.

### If it is built anyway

Build it as a **separate extension** with its own ID, name and listing, never as a permission
added to this one, and:

- ship it only through enterprise force-install (`ExtensionInstallForcelist`), with no end-user
  download path;
- default to **metadata** retention: URL origin and path without query string, the transition
  type, and a flag for "happened in an agent-owned tab group" where that can be detected. No form
  values, and never page content;
- use optional host permissions (`chrome.permissions.request`) scoped by policy to the domains an
  investigation needs, instead of `<all_urls>`;
- rewrite the README privacy section and the docs data inventory (`docs/security/data-inventory.mdx`)
  before the first release.

## A normalized agentic-browsing event (draft, for the follow-up issue)

This is a sketch so that a future adapter starts from the schema rather than from a browser API.
Nothing here is implemented.

- **Agent effects map to tool events.** In the OTel GenAI model, an agent acting on the world is a
  tool execution (`execute_tool`). An agent navigation becomes `tool.invoked` / `tool.completed`
  with `gen_ai.tool.name` set to one of `browser.navigate`, `browser.click`, `browser.type`,
  `browser.submit` or `browser.download`, and `gen_ai.tool.call.arguments` carrying the target
  (the URL origin and path under metadata retention, the element descriptor under full retention).
- **There is no `prompt.submitted` or `agent.response.completed`**, because the prompts are not
  observable. The events must not imply a model turn Beacon never saw. This follows the rule the
  poll collectors already apply: do not synthesize what the runtime does not expose.
- **An actor attribute is needed**, for example `beacon.browser.actor` = `agent` | `user` |
  `unknown`, with `unknown` as the honest default. It should never be inferred as `agent` without a
  concrete signal.
- **Harness names need care.** `NormalizeHarnessName` in `pkg/asymptoteobserve/harness.go` maps
  any name containing `chatgpt` to `chatgpt_web`. A harness such as `chatgpt_desktop` would be
  silently folded into the web chat harness unless a case is added above that rule. Candidates:
  `comet_browser`, `dia_browser`, `chatgpt_desktop_browser`, each with its own case.
- **Keep `harness.collection_method=otlp`** for anything the extension sends, and use `poll` for a
  Dia history reader.

## Does this belong in `browser-extension/`?

**Baseline capture (the existing extension running in these browsers): yes.** It is the same code
and the same origins, and it only needs documentation once the checklists are run. If #394 adds a
browser-identity attribute from `navigator.userAgentData.brands`, events from Comet and Dia can be
told apart from Chrome's with no extra work.

**Capturing the agents: no.**

- The model traffic is not reachable from an extension in any browser studied.
- Effects capture is a different product with a different consent model. If built, it is a
  separate extension.
- The authoritative records (Comet audit logs, OpenAI compliance logs) are vendor-side, and
  ingesting them is a product decision about cloud audit ingestion.
- The one local, endpoint-shaped candidate is Dia's on-disk chat history, and that belongs in the
  CLI as a `sync` collector, not in the extension.

## Suggested follow-up issues

1. Run the three manual checklists and add a "Tested browsers" table to `browser-extension/README.md`.
2. Comet: determine whether the Sidecar is an injectable perplexity.ai document (checklist steps 4
   and 5), then decide whether a Perplexity adapter is in scope.
3. Dia: investigate the local chat-history store as a `beacon endpoint dia sync` candidate.
4. Product decision: effects-based capture as a separate, force-install-only extension. Yes or no.
5. Product decision: vendor audit-log ingestion (Comet Enterprise, OpenAI Compliance API) against
   the "cloud audit ingestion" non-goal.
