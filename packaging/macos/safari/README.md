# Safari packaging for the browser collector

Experimental, and not shipped. These files cover the Safari-specific pieces of
[#392](https://github.com/Asymptote-Labs/agent-beacon/issues/392): the Xcode app
wrapper, distribution and signing, MDM, and manual verification. None of it has
been run in Safari yet. [`compatibility.md`](compatibility.md) lists what is
documented and what still needs a Mac to confirm.

| File | Purpose |
|---|---|
| `create-safari-project.sh` | Generates the Xcode app wrapper from a built extension with `xcrun safari-web-extension-packager`. |
| `compatibility.md` | Safari support for each API and manifest key the extension uses, with sources and a gap list. |
| `smoke-checklist.md` | The manual checks a Safari build must pass. There is no automated Safari test. |
| `mdm/safari-extension-settings.declaration.json` | Example Declarative Device Management configuration that force-enables the extension and pre-grants its sites. |
| `check-mdm-declaration.py` | Validates that declaration against Apple's schema and the extension manifest. |
| `test-safari-packaging.sh` | Tests the wrapper's argument handling and guards (with a fake `xcrun`) and the declaration. Runs on Linux, and from `packaging/macos/test-endpoint-scripts.sh`. |

## How Safari differs

A Safari web extension cannot be loaded unpacked for real use. It ships inside a
macOS app. Safari discovers the extension from the app's `PlugIns/*.appex`, and
the app has to be signed. Safari's "Allow unsigned extensions" setting and its
"Add Temporary Extension…" button are development tools only: both reset when
Safari quits, and temporary extensions also expire after 24 hours.

The extension code itself needs little change. Safari exposes the `chrome.*`
namespace, and every API the collector calls is documented as supported in
Safari 18 or later. The open risks are the site-access grants and whether the
service worker's POST reaches `127.0.0.1:4318`. See the gap list in
`compatibility.md`.

## Generate the Xcode project

On a Mac with full Xcode (the Command Line Tools alone do not include the
packager):

```bash
cd browser-extension && npm ci && npm run build && cd ..
sh packaging/macos/safari/create-safari-project.sh
```

This writes `dist/safari/Beacon Browser Collector/`, which holds a macOS app
target (`ai.asymptote.beacon.browser-collector`) and the extension target
(`ai.asymptote.beacon.browser-collector.Extension`). The extension files are
**copied** into the project, so it is a snapshot of that build. Regenerate it
(`--force`) after rebuilding the extension. For a local edit-and-build loop, use
`--reference-resources`; the project then points at `browser-extension/dist`,
and Product > Build picks up each rebuild.

`--dry-run` validates the inputs and prints the exact packager command on any
OS. Run `--help` for all options: `--platform ios|all`, `--app-name`,
`--bundle-id`, and the rest.

The project is generated, not committed. The packager's output changes between
Xcode releases. Regenerating it from a fresh build keeps the wrapper and the
extension in step, and avoids a hand-edited Xcode project in the repository. If
the wrapper later needs native code (it does not today), commit the project
then.

### Dependency on the Firefox work (#393)

This directory wraps whatever build it is pointed at. It does not add a
`browser.*` shim or per-target manifests: #393 owns both.

- Safari does not need the shim. It supports `chrome.*`.
- Safari may need a manifest variant. If S5 in the checklist shows the service
  worker's POST failing CORS even after `127.0.0.1` access is granted, the Safari
  target needs `background.scripts` plus `preferred_environment`. That comes from
  #393's per-target manifest build. Point the script at that target's output
  with `--extension-dir` (or `SAFARI_EXTENSION_DIR`).

## Distribution and signing plan

Nothing here is wired into CI or a release workflow yet. This is the proposed
path.

**Recommendation: Developer ID, notarized, delivered as a signed package. Not the
App Store, at least at first.**

- Beacon already signs and notarizes a macOS package with Developer ID
  (`build-signed-notarized-pkg.sh`, and the `package` job in `release.yml`).
  Fleets deploy it through Jamf or Fleet. A Safari app delivered the same way
  fits that model: the same identities, the same notary profile, and the same
  MDM upload.
- The App Store adds review latency to every extension release. It also forces
  a privacy-label discussion about `retention: 'full'`. And it doesn't help
  managed fleets, which install through MDM anyway.
- The trade-off: Safari has no `update_url`, so a Developer ID build updates only
  when a newer package is installed. That's how the endpoint package already
  updates. Revisit the App Store if unmanaged individual users become a target.

Proposed release steps, for a macOS runner in the existing `release` environment,
which already holds `DEVELOPER_ID_APP_CERT_P12`, `DEVELOPER_ID_INSTALLER_CERT_P12`,
their passwords, and the `NOTARY_API_KEY_*` secrets:

1. Build the extension and strip sourcemaps, as the Chrome zip job does.
2. `sh packaging/macos/safari/create-safari-project.sh --force`
3. Archive the app with the Developer ID Application identity, the team from the
   `APPLE_TEAM_ID` variable, and the hardened runtime. macOS requires app
   extensions to be sandboxed, so keep the App Sandbox entitlement in the
   generated `.entitlements` files. The scheme name below assumes the default
   app name; `xcodebuild -list` shows the real one:

   ```bash
   xcodebuild -project "dist/safari/Beacon Browser Collector/Beacon Browser Collector.xcodeproj" \
     -scheme "Beacon Browser Collector" -configuration Release \
     -archivePath "dist/safari/BeaconBrowserCollector.xcarchive" \
     DEVELOPMENT_TEAM="$APPLE_TEAM_ID" CODE_SIGN_STYLE=Manual \
     CODE_SIGN_IDENTITY="$DEVELOPER_ID_APP_IDENTITY" \
     OTHER_CODE_SIGN_FLAGS=--timestamp ENABLE_HARDENED_RUNTIME=YES archive
   xcodebuild -exportArchive -archivePath "dist/safari/BeaconBrowserCollector.xcarchive" \
     -exportOptionsPlist ExportOptions.plist -exportPath dist/safari/export   # method: developer-id
   ```

4. Notarize and staple the app (`xcrun notarytool submit --wait`, then
   `xcrun stapler staple`), using the same notary credentials as
   `build-signed-notarized-pkg.sh`.
5. Wrap the app in a package that installs to `/Applications`. Sign it with the
   Developer ID Installer identity (`productbuild --component ... /Applications
   --sign "$DEVELOPER_ID_INSTALLER_IDENTITY"`), then notarize and staple the
   package.
6. Verify with checks R1 to R4 in [`smoke-checklist.md`](smoke-checklist.md),
   then publish the package and its `.sha256` on the `ext-v*` release.

This makes the reasoning in `release-extension.yml`'s header out of date. That
job skips `environment: release` because it has no signing secrets, but a
Safari job would need them. Add the Safari job to that workflow in the
`release` environment. The Chrome job can stay outside it.

Open question, carried over from the issue: should the Safari app eventually
ride inside the endpoint `.pkg`? That would give one artifact and one MDM upload.
But it ties the extension's `ext-v*` cadence to the CLI's `v*` releases, so start
with a separate package.

## MDM

Safari extension management is **Declarative Device Management only**. It needs
Safari 18, macOS 15 or later, and a **supervised** Mac. The issue assumed a
configuration profile (`.mobileconfig`) payload. None exists for this: an Apple
Device Management engineer confirmed on the forums that Safari extensions are
managed "via declarative device management (not a profile)", and a
`.mobileconfig` carrying these keys does nothing
([forum thread 760255](https://developer.apple.com/forums/thread/760255)).

[`mdm/safari-extension-settings.declaration.json`](mdm/safari-extension-settings.declaration.json)
is a `com.apple.configuration.safari.extensions.settings` declaration
([Apple schema](https://github.com/apple/device-management/blob/release/declarative/declarations/configurations/safari.extensions.settings.yaml)).
It:

- turns the collector on and prevents users from turning it off (`State: AlwaysOn`);
- leaves Private Browsing to the user (`PrivateBrowsing: Allowed`). Set it to
  `AlwaysOn` to capture private windows too, or to `AlwaysOff` to exclude them;
- pre-grants the sites in the manifest's `host_permissions`, including
  `127.0.0.1` and `localhost` for the collector, so users are never asked for
  site access (`AllowedDomains`). Whether Safari accepts IP literals such as
  `127.0.0.1` in `AllowedDomains` is not documented; check M2 confirms it.

Before deploying it:

1. Replace `TEAMID1234` in the `ManagedExtensions` key with the signing team ID.
   The key is the extension's composed identifier,
   `<extension bundle id> (<team id>)`. Read it from a signed build with
   `codesign -dv "/Applications/Beacon Browser Collector.app/Contents/PlugIns/"*.appex`.
2. Change `ServerToken` whenever you change the declaration, so devices apply the
   update.
3. Validate the edited file:

   ```bash
   python3 packaging/macos/safari/check-mdm-declaration.py \
     packaging/macos/safari/mdm/safari-extension-settings.declaration.json \
     --manifest browser-extension/src/manifest.json \
     --extension-bundle-id ai.asymptote.beacon.browser-collector.Extension
   ```

How to deliver the declaration depends on the MDM. It must support Declarative
Device Management and the Safari extensions configuration, so check your
vendor's documentation. Deploy the signed app package first (for example, as a
Jamf policy alongside the endpoint `.pkg`, as described in
[`../README.md`](../README.md#jamf-pro)), then the declaration.

Unsupervised Macs and macOS 14 or earlier cannot be managed this way. There,
users enable the extension and grant site access themselves; the prompts they
see are recorded by check S4.
