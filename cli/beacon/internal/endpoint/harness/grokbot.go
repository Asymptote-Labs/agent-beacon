package harness

import (
	"os"
	"path/filepath"
	"runtime"
)

const (
	// GrokBotName is the canonical harness name, shared with pkg/asymptoteobserve and with the
	// cursor.surface value Cursor's export writes for Bot traffic.
	GrokBotName        = "grok_bot"
	GrokBotDisplayName = "Grok Bot"
	// GrokBotCapability names the only collection path Grok Bot has: Cursor's Enterprise
	// server-side OpenTelemetry export, configured in Cursor Team Settings rather than on this
	// machine. Nothing here is installable, which is why `harnessAction` has no command for it.
	GrokBotCapability = "cloud_export"
	// GrokBotStateDir is the per-user directory the desktop app's local-execution helper writes
	// under (its log is ~/.grokbot/local-exec-daemon.log). It is the one documented on-disk
	// footprint of the app and the detection tell that works on every platform it ships for.
	GrokBotStateDir = ".grokbot"

	grokBotMessage = "Grok Bot runs on Cursor-hosted cloud computers; Beacon cannot hook it on this machine. " +
		"Bot actions reach a collector only through Cursor Enterprise OpenTelemetry Export (Team Settings, with Action Recording on), " +
		"which the Beacon collector maps to the grok_bot harness"
)

// DiscoverGrokBot reports whether the Grok Bot desktop app is present on this machine.
//
// Detection is all this function can do. Grok Bot is xAI's always-on agent product: every Bot
// runs on a Cursor-hosted cloud computer, and the desktop app is a client to it plus an optional
// local-execution helper that runs approved commands on the member's machine. It exposes no hook,
// plugin, extension, session store, or local OTLP export, so there is nothing to install and no
// telemetry status other than "missing" to report. Presence still matters to an inventory: an
// operator deciding whether to turn on Cursor's OpenTelemetry Export wants to know which
// endpoints have the app at all.
//
// The macOS application bundle paths are best-effort. Only the ~/.grokbot state directory is
// confirmed from vendor guidance; the bundle name follows the product name and the pattern every
// other Electron desktop agent here uses, and a miss on it costs nothing because the state
// directory is created on first launch regardless.
func DiscoverGrokBot() Harness {
	h := Harness{Name: GrokBotName, DisplayName: GrokBotDisplayName, Capability: GrokBotCapability}
	home, _ := os.UserHomeDir()
	stateDir := filepath.Join(home, GrokBotStateDir)
	h.ConfigPath = stateDir
	if dirExists(stateDir) {
		h.Detected = true
		h.ExecutablePath = stateDir
	}
	for _, path := range grokBotDesktopPathsForOS(runtime.GOOS, home) {
		if dirExists(path) {
			h.Detected = true
			h.ExecutablePath = path
			break
		}
	}
	h.TelemetryStatus = TelemetryMissing
	h.Message = grokBotMessage
	return h
}

// grokBotDesktopPathsForOS lists where the desktop app itself would be found, beyond the state
// directory. Linux is deliberately empty: xAI ships .deb, .rpm and AppImage builds with no single
// install location, and the state directory is the detection there.
func grokBotDesktopPathsForOS(goos, home string) []string {
	switch goos {
	case "darwin":
		return []string{
			"/Applications/Grok Bot.app",
			filepath.Join(home, "Applications", "Grok Bot.app"),
			filepath.Join(home, "Library", "Application Support", "Grok Bot"),
		}
	case "windows":
		local := os.Getenv("LOCALAPPDATA")
		if local == "" {
			local = filepath.Join(home, "AppData", "Local")
		}
		return []string{filepath.Join(local, "Programs", "Grok Bot")}
	default:
		return nil
	}
}
