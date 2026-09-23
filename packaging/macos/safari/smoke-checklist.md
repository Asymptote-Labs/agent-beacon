# Safari manual smoke checklist

Safari has no automated in-browser test. Playwright cannot load extensions into
WebKit, and `safaridriver` cannot install them. The Playwright replay e2e in
`browser-extension/e2e/` therefore runs on Chromium only, and it remains the
correctness gate for the site adapters. Run this checklist by hand on a Mac for
every Safari build you intend to hand to users, and record the results in the
pull request or release notes.

The IDs (S1 to S10) are referenced from [`compatibility.md`](compatibility.md).

## Setup

- A Mac with Safari 18 or later (Safari > About Safari). Safari 18 is the first
  version documented to support `world: "MAIN"` in manifest content scripts.
- The Beacon endpoint agent installed and running, so the collector is listening
  on `127.0.0.1:4318`:

  ```bash
  beacon endpoint status          # add --system for the package install
  lsof -nP -iTCP:4318 -sTCP:LISTEN
  ```

- A fresh extension build:

  ```bash
  cd browser-extension && npm ci && npm run build:safari && cd ..
  ```

- Safari > Settings > Advanced: turn on "Show features for web developers".

Choose one of two ways to load the build:

- **Quick (no Xcode, unsigned):** Safari > Settings > Developer > turn on
  "Allow unsigned extensions", then "Add Temporary Extension…" and select
  `browser-extension/dist-safari`. Safari removes temporary extensions after 24 hours or
  when you quit Safari, and "Allow unsigned extensions" also resets on quit.
- **App wrapper (what users get):**

  ```bash
  sh packaging/macos/safari/create-safari-project.sh
  open "dist/safari/Beacon Browser Collector/Beacon Browser Collector.xcodeproj"
  ```

  In Xcode, set the signing team on both targets, select the macOS app scheme,
  and choose Product > Run. Then enable the extension in Safari > Settings >
  Extensions.

Keep the runtime log open in a terminal throughout:

```bash
tail -f ~/.beacon/endpoint/logs/runtime.jsonl      # user install
sudo tail -f /var/log/beacon-agent/runtime.jsonl   # system/package install
```

## Checks

| ID | Check | Pass when |
|---|---|---|
| S1 | **Loads.** The extension appears in Safari > Settings > Extensions with name "Agent Beacon — Browser Collector" and the manifest's version, and can be enabled. If you used the app wrapper, note any packager warnings about unsupported manifest keys. | Enabled, no load error. Any packager warnings are copied into the results. |
| S2 | **Service worker starts.** Develop > Web Extension Background Content (or the extension's service worker entry in the Develop menu) opens an inspector for `sw.js`. | The console shows no uncaught errors at startup. |
| S3 | **MAIN-world interceptor runs.** Grant the extension access to claude.ai (toolbar button > Always Allow on This Website), reload claude.ai, open the page's Web Inspector, and check that `window.fetch` has been wrapped. For example, `window.fetch.toString()` does not print `[native code]`. | The interceptor is installed before the page's first chat request. |
| S4 | **Site-access flow.** On a site you have not granted yet, the toolbar badge asks for access and capture does not happen. Open the popup: the host-permission banner should list the missing sites, and its grant button should trigger Safari's permission request. After you grant access, capture works. | The behaviour matches Safari's documented ask-first model. Write down the exact prompts shown; users will see the same ones. |
| S5 | **Reaches the collector.** Grant access to `127.0.0.1` too, if Safari lists it: Safari > Settings > Websites > the extension, or "Always Allow on Every Website" for a test profile only. Send a claude.ai message and watch the service worker inspector's Network and Console tabs. | The `POST http://127.0.0.1:4318/v1/logs` returns 200. There is no "access control checks" (CORS) error, no ATS error, and no Local Network prompt from macOS. If it fails, record the exact console error and see "Reaching the collector" in `compatibility.md`. |
| S6 | **claude.ai end to end.** Send one message on claude.ai. | `runtime.jsonl` gains a `prompt.submitted` and an `agent.response.completed` event with harness `claude_web`, the right model, and the prompt and response text (with retention `full`). |
| S7 | **chatgpt.com end to end.** Send one message on chatgpt.com. | The same pair of events, with harness `chatgpt_web`. |
| S8 | **Retention control.** In the popup, set retention to `metadata` and send a message, then `redacted` with an email address in the prompt, then back to `full`. The options page (Safari > Settings > Extensions > the extension > Settings) shows the OTLP endpoint and the per-site toggles. | Under `metadata` the new events carry no prompt or response text. Under `redacted` the email is scrubbed. Under `full` the text returns. Settings persist across a Safari restart. |
| S9 | **Collector down, then up.** Stop the collector, send a message, then start it again. Package install: `sudo launchctl bootout system/com.beacon.endpoint.collector`, then `sudo launchctl bootstrap system /Library/LaunchDaemons/com.beacon.endpoint.collector.plist`. User install: the same with `gui/$(id -u)/com.beacon.endpoint.collector.user` and `~/Library/LaunchAgents/com.beacon.endpoint.collector.user.plist`. | The popup's queue depth rises while the collector is down. Within about a minute of restarting it (alarms-driven retry), the queued events land in `runtime.jsonl` and the queue drains. Keep the outage to a minute or two: `delivery.ts` drops an item after 6 failed attempts. |
| S10 | **Scope.** Browse an unrelated site with the extension enabled. | No events, and the extension requests no access to that site. |

## MDM (only for managed-fleet validation)

Run this on a **supervised** Mac with macOS 15 or later, enrolled in an MDM that
supports Declarative Device Management. Use a signed build, not a temporary
extension.

| ID | Check | Pass when |
|---|---|---|
| M1 | Install the signed app. Read the extension's composed identifier with `codesign -dv "/Applications/Beacon Browser Collector.app/Contents/PlugIns/"*.appex`: the `Identifier=` value plus ` (<TeamIdentifier>)`. | The identifier matches the key in `mdm/safari-extension-settings.declaration.json` once `TEAMID1234` is replaced. |
| M2 | Deploy the declaration. Open Safari. | The extension is on and cannot be turned off (`State: AlwaysOn`), and claude.ai, chatgpt.com and `127.0.0.1` need no user grant (`AllowedDomains`). |
| M3 | Repeat S5 to S7 without granting anything by hand. | Capture works end to end. Also note whether the app had to be launched once before Safari listed the extension. |

## Signed build (only for release candidates)

| ID | Check | Pass when |
|---|---|---|
| R1 | `codesign --verify --deep --strict --verbose=2 "Beacon Browser Collector.app"` | Valid, with a Developer ID Application authority. |
| R2 | `xcrun stapler validate "Beacon Browser Collector.app"` (and the `.pkg`, if one is built) | "The validate action worked!" |
| R3 | `spctl --assess --type execute -vv "Beacon Browser Collector.app"` | `accepted`, `source=Notarized Developer ID`. |
| R4 | On a clean Mac or VM without "Allow unsigned extensions" turned on, install the release artifact and repeat S1, S5 and S6. | The extension works without any developer setting. |
