// Package hookcmd recognises the hook commands Beacon's endpoint install writes into a runtime's
// config. The installer (cli/beacon) uses it to find its own hooks for status, repair and
// uninstall; the hooks binary (cli/beacon-hooks) uses it to tell whether Beacon's Cursor hooks
// already cover an event. Both must agree, which is why it is one package.
package hookcmd

import "strings"

// Binary stems a Beacon hook command may run: the current binary and the one older versions
// installed.
const (
	BinaryStem       = "beacon-hooks"
	LegacyBinaryStem = "asym-hooks"
)

// CursorEvents names the Cursor hooks.json event(s) the installer wires each beacon-hooks
// subcommand to. The installer writes Cursor's hooks from it, and the hooks binary only trusts a
// Beacon entry for a subcommand under one of these events.
var CursorEvents = map[string][]string{
	"session-start":  {"sessionStart"},
	"prompt-submit":  {"beforeSubmitPrompt"},
	"pre-tool":       {"preToolUse"},
	"agent-thought":  {"afterAgentThought"},
	"agent-response": {"afterAgentResponse"},
	"post-tool":      {"afterFileEdit", "postToolUse", "postToolUseFailure"},
	"stop":           {"stop"},
	"session-end":    {"sessionEnd"},
}

// IsEndpointHook decides whether command is one an endpoint install wrote for platform ("" for
// any platform).
//
// Both directions are costly. A false negative makes repair add a second hook beside the first
// and leaves uninstall's behind; a false positive rewrites or deletes a hook somebody else
// installed.
func IsEndpointHook(command, platform string) bool {
	fields := Fields(command)
	hasPlatform := platform == "" || hasPlatform(fields, platform)
	namesPlatform := namesPlatform(fields)
	beacon, legacy := binaries(fields)

	if carriesEndpointSettings(fields) && beacon {
		return hasPlatform || !namesPlatform
	}
	if beacon && !namesPlatform {
		return true
	}
	return legacy && hasPlatform
}

// Runs reports whether command is an endpoint hook that explicitly names platform and runs
// subcommand, which every Beacon hook command ends with.
func Runs(command, platform, subcommand string) bool {
	fields := Fields(command)
	if len(fields) == 0 || fields[len(fields)-1] != subcommand || !hasPlatform(fields, platform) {
		return false
	}
	return IsEndpointHook(command, platform)
}

// HasPlatform reports whether command names platform with --platform.
func HasPlatform(command, platform string) bool {
	return hasPlatform(Fields(command), platform)
}

// CarriesEndpointSettings reports whether a command was written by an endpoint install, as
// opposed to being some other invocation of the same binary.
//
// Two spellings, because the values moved from the environment to flags. An inline
// `BEACON_ENDPOINT_MODE=1 ...` prefix is a POSIX shell construct that neither Windows shell
// accepts, so a Windows hook carries `--log`/`--config`/`--cli` instead. Both must be recognized:
// already installed POSIX hooks use the prefix, and until a repair rewrites them one machine has
// both.
func CarriesEndpointSettings(command string) bool {
	return carriesEndpointSettings(Fields(command))
}

func carriesEndpointSettings(fields []string) bool {
	for _, field := range fields {
		if field == "BEACON_ENDPOINT_MODE=1" {
			return true
		}
		// --cli is no longer written (inventory comes from the scheduled job, not the hook), but
		// it is still recognized so hooks installed by older versions are repaired and removed.
		for _, name := range []string{"--log", "--config", "--cli"} {
			if field == name || strings.HasPrefix(field, name+"=") {
				return true
			}
		}
	}
	return false
}

// binaries reports whether any token names the Beacon or legacy hooks binary: its basename, with
// either path separator, contains the stem (so `beacon-hooks.exe` and a renamed
// `old-beacon-hooks` both count). Looking at each token's basename rather than searching the
// whole command keeps a directory named after the binary from passing as the binary itself.
func binaries(fields []string) (beacon, legacy bool) {
	for _, field := range fields {
		name := field[strings.LastIndexAny(field, `/\`)+1:]
		beacon = beacon || strings.Contains(name, BinaryStem)
		legacy = legacy || strings.Contains(name, LegacyBinaryStem)
	}
	return beacon, legacy
}

// namesPlatform reports whether a command specifies a platform at all. A command that names none
// is treated as an any-platform install, which is how hooks written before the flag existed are
// still recognized. Both `--platform x` and `--platform=x` count.
func namesPlatform(fields []string) bool {
	for _, field := range fields {
		if field == "--platform" || strings.HasPrefix(field, "--platform=") {
			return true
		}
	}
	return false
}

func hasPlatform(fields []string, platform string) bool {
	for i, field := range fields {
		if field == "--platform" {
			return i+1 < len(fields) && fields[i+1] == platform
		}
		if value, found := strings.CutPrefix(field, "--platform="); found {
			return value == platform
		}
	}
	return false
}

// Fields splits a hook command into argv-like tokens, keeping quoted runs together.
//
// strings.Fields is not enough once paths contain spaces, as the default Windows install
// locations under %ProgramFiles% or a user profile routinely do. Quoting is stripped because
// callers compare against bare values like "cursor".
//
// Both quote characters, because the command may have been written for either shell -- POSIX
// hooks are single-quoted and Windows ones double-quoted. Backslash is not an escape: on Windows
// it is a path separator. The one exception is \' outside quotes, which is how a single quote in
// a value is written by closing the quoted run, writing \' and reopening it.
func Fields(command string) []string {
	var (
		fields  []string
		current strings.Builder
		quote   rune
		started bool
	)
	flush := func() {
		if started {
			fields = append(fields, current.String())
			current.Reset()
			started = false
		}
	}
	runes := []rune(command)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
				break
			}
			current.WriteRune(r)
		case r == '\\' && i+1 < len(runes) && runes[i+1] == '\'':
			current.WriteRune('\'')
			started = true
			i++
		case r == '"' || r == '\'':
			// Marks the token as started even if the quotes turn out to be empty, so `--platform ""`
			// yields an empty value rather than dropping the argument and shifting every token after
			// it left by one.
			quote = r
			started = true
		case r == ' ' || r == '\t':
			flush()
		default:
			current.WriteRune(r)
			started = true
		}
	}
	flush()
	return fields
}
