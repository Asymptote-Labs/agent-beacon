# Release Signing Setup — macOS code signing & notarization

This is a **one-time** setup so the release CI workflow
(`.github/workflows/release.yml`) can sign, notarize, and staple Beacon's macOS
`.pkg` automatically on every release. The values you add here are stored as
encrypted secrets and variables in the repo's protected `release` GitHub
Environment; the workflow reads them automatically whenever a version tag is
pushed — there is nothing to run by hand afterward.

You will:
1. Read three non-secret identity strings (your signing identities + Team ID).
2. Export two signing certificates and create a notarization key.
3. Upload the secrets **and** set the three identity values directly from your
   Mac into the `release` environment (so the private keys never travel through
   chat, email, or anyone else's machine).

Everything runs on your Mac. Estimated time: ~20 minutes.

---

## Prerequisites

- Membership in the **Apple Developer Program** for the team that owns Beacon.
- The two **Developer ID** certificates installed in your login keychain:
  *Developer ID Application* and *Developer ID Installer*. If you've ever built
  the signed `.pkg` locally you already have them. If not, create both at
  https://developer.apple.com/account/resources/certificates → "+" → choose each
  type, download, and double-click to install.
- The GitHub CLI: `brew install gh` then `gh auth login`.
- **Admin** access on the `asymptote-labs/agent-beacon` repo (needed to add
  environment secrets and variables; a repo admin can grant it). Confirm with:
  `gh auth status`

> The `release` GitHub Environment is **already set up** — you just add your
> values to it. To see it in the browser, open:
> **https://github.com/asymptote-labs/agent-beacon/settings/environments**
> and click **`release`** (its Environment secrets and variables are what the
> commands below populate). You can do everything from the terminal, though — the
> `gh` commands in Steps 4–5 target it directly.

---

## Step 1 — Read your identity strings and Team ID

```bash
security find-identity -v | grep "Developer ID"
```

You'll see lines like:

```
1) ABC123…  "Developer ID Application: Asymptote Labs (TEAMID)"
2) DEF456…  "Developer ID Installer: Asymptote Labs (TEAMID)"
```

Note these three values **exactly** — you'll set them as GitHub Variables in
Step 5 (they're not secret; they appear in every signed app):

- **DEVELOPER_ID_APP_IDENTITY** = the full `Developer ID Application: … (TEAMID)` string
- **DEVELOPER_ID_INSTALLER_IDENTITY** = the full `Developer ID Installer: … (TEAMID)` string
- **APPLE_TEAM_ID** = the `TEAMID` inside the parentheses (also on your
  https://developer.apple.com/account → Membership page)

---

## Step 2 — Export the two certificates as `.p12`

The `.p12` file bundles a certificate **with its private key** — that's what CI
needs. Use Keychain Access (the GUI is the reliable way):

1. Open **Keychain Access** → **login** keychain → **My Certificates**.
2. Find **"Developer ID Application: …"**. Expand the triangle so you can see a
   private key nested under it (no key = you can't sign; re-create the cert).
3. Right-click the certificate → **Export "Developer ID Application…"** →
   File Format: **Personal Information Exchange (.p12)** → save as `app.p12`.
4. Set a strong **export password** when prompted. Write it down — you'll need it
   in Step 4 as `DEVELOPER_ID_APP_CERT_PASSWORD`.
5. Repeat for **"Developer ID Installer: …"** → save as `installer.p12` with its
   own export password (`DEVELOPER_ID_INSTALLER_CERT_PASSWORD`).

---

## Step 3 — Create an App Store Connect API key (for notarization)

1. Go to https://appstoreconnect.apple.com → **Users and Access** →
   **Integrations** tab → **App Store Connect API** → **Team Keys**.
2. Click **+ (Generate API Key)**. Name it `beacon-notary`. Access: **Developer**.
3. **Download the key** — a file like `AuthKey_XXXXXX.p8`. ⚠️ Apple lets you
   download it **only once**; keep it safe.
4. Note two values on that page:
   - **Key ID** — the `XXXXXX` in the filename / the key's row.
   - **Issuer ID** — the UUID shown at the top of the Keys section.

---

## Step 4 — Upload everything to GitHub as secrets (run from your Mac)

Work in the folder containing `app.p12`, `installer.p12`, and `AuthKey_XXXXXX.p8`.
Replace the placeholder passwords / IDs with your real values. These commands
pipe the files straight into GitHub — nothing is copied anywhere else.

```bash
R=asymptote-labs/agent-beacon

# Certificates (base64-encoded inline so they stay text):
base64 -i app.p12       | gh secret set DEVELOPER_ID_APP_CERT_P12        --env release --repo "$R"
base64 -i installer.p12 | gh secret set DEVELOPER_ID_INSTALLER_CERT_P12  --env release --repo "$R"
base64 -i AuthKey_*.p8  | gh secret set NOTARY_API_KEY_P8                 --env release --repo "$R"

# Passwords / IDs (use your real values):
printf '%s' 'YOUR_APP_P12_EXPORT_PASSWORD'       | gh secret set DEVELOPER_ID_APP_CERT_PASSWORD        --env release --repo "$R"
printf '%s' 'YOUR_INSTALLER_P12_EXPORT_PASSWORD' | gh secret set DEVELOPER_ID_INSTALLER_CERT_PASSWORD  --env release --repo "$R"
printf '%s' 'YOUR_NOTARY_KEY_ID'                 | gh secret set NOTARY_API_KEY_ID                      --env release --repo "$R"
printf '%s' 'YOUR_NOTARY_ISSUER_UUID'            | gh secret set NOTARY_API_ISSUER                      --env release --repo "$R"
```

Verify all seven landed (names only are shown; values are never readable again):

```bash
gh secret list --env release --repo "$R"
```

Expected names:
`DEVELOPER_ID_APP_CERT_P12`, `DEVELOPER_ID_APP_CERT_PASSWORD`,
`DEVELOPER_ID_INSTALLER_CERT_P12`, `DEVELOPER_ID_INSTALLER_CERT_PASSWORD`,
`NOTARY_API_KEY_P8`, `NOTARY_API_KEY_ID`, `NOTARY_API_ISSUER`.

---

## Step 5 — Set the three identity Variables (not secret)

These are the values you noted in Step 1. They go in as **Variables** (not
secrets) because they aren't sensitive. Paste your real strings:

```bash
R=asymptote-labs/agent-beacon
gh variable set DEVELOPER_ID_APP_IDENTITY       --env release --repo "$R" --body 'Developer ID Application: Asymptote Labs (TEAMID)'
gh variable set DEVELOPER_ID_INSTALLER_IDENTITY --env release --repo "$R" --body 'Developer ID Installer: Asymptote Labs (TEAMID)'
gh variable set APPLE_TEAM_ID                    --env release --repo "$R" --body 'TEAMID'
```

Verify the three landed:

```bash
gh variable list --env release --repo "$R"
```

---

## Step 6 — Clean up local key material

```bash
rm -f app.p12 installer.p12 AuthKey_*.p8
```

(Keep a backup of the `.p12`/`.p8` in your password manager if you want to avoid
re-exporting later — but do not leave them loose on disk.)

---

## Done

The seven secrets and three variables now live in the `release` environment.
From here on, pushing a version tag (`vX.Y.Z`) automatically runs
`.github/workflows/release.yml`, which reads these values to build, sign,
notarize, staple, and publish the packages — no manual signing steps. If a run
fails on a signing step, the most common causes are a mistyped identity string
(Steps 1/5), a wrong `.p12` export password (Step 2), or a notary key without
enough access (Step 3, must be Developer or higher).
