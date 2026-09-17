package openclawplugin

import _ "embed"

// OpenClaw discovers a plugin as a directory rather than a single file, so three artifacts are
// embedded here where the other managed runtimes embed one.
//
// The root source of truth for all three lives at plugins/openclaw-beacon/src/. Tests fail if
// these copies drift, and `bun run sync` in that directory is what updates them.

// Template embeds the plugin entry Beacon renders and writes.
//
//go:embed beacon.js
var Template string

// PackageManifest embeds the plugin's own package.json.
//
// It is named plugin.package.json in the source tree so it cannot be mistaken for -- or picked up
// by tooling as -- the workspace package.json that sits beside it. It is written to disk as
// `package.json`, which is the name OpenClaw reads.
//
// It declares the entry under both `openclaw.extensions` and `openclaw.runtimeExtensions`.
// OpenClaw treats the first as a source entrypoint and the second as a built-JavaScript one for
// installed packages, and a plugin Beacon writes into the global extensions directory is reached
// through whichever path the host takes. The entry is plain JavaScript, so both descriptions are
// true of the same file and no transpilation step stands between OpenClaw and the plugin.
//
//go:embed plugin.package.json
var PackageManifest string

// PluginManifest embeds openclaw.plugin.json, the pre-runtime manifest OpenClaw reads before it
// loads any plugin code. `activation.onStartup` is what makes the gateway load the plugin without
// an agent run to trigger it, which is required for the session and gateway-level hooks.
//
//go:embed openclaw.plugin.json
var PluginManifest string
