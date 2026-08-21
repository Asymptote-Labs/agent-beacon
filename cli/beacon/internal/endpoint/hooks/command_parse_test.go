package hooks

import (
	"reflect"
	"testing"
)

// TestCommandFieldsKeepsQuotedPathsWhole is the reason this parser exists.
//
// strings.Fields was enough while every path was POSIX. The default Windows install locations are
// under %ProgramFiles% and a user profile, both of which routinely contain a space, so a quoted path
// splits into fragments and every token comparison after it is made against half a path.
func TestCommandFieldsKeepsQuotedPathsWhole(t *testing.T) {
	cases := []struct {
		name    string
		command string
		want    []string
	}{
		{
			name:    "the Windows form",
			command: `"C:\Program Files\Beacon\hooks\beacon-hooks.exe" pre-tool --platform cursor`,
			want:    []string{`C:\Program Files\Beacon\hooks\beacon-hooks.exe`, "pre-tool", "--platform", "cursor"},
		},
		{
			name:    "the POSIX form, single quotes and an env prefix",
			command: `BEACON_ENDPOINT_MODE=1 BEACON_ENDPOINT_LOG='/var/log/beacon-agent/runtime.jsonl' '/opt/beacon/hooks/beacon-hooks' --platform cursor`,
			want: []string{
				"BEACON_ENDPOINT_MODE=1",
				"BEACON_ENDPOINT_LOG=/var/log/beacon-agent/runtime.jsonl",
				"/opt/beacon/hooks/beacon-hooks",
				"--platform", "cursor",
			},
		},
		{
			// A dropped empty argument would shift every token after it left by one, which is how a
			// value gets read as a flag name.
			name:    "an empty quoted value is still an argument",
			command: `beacon-hooks --platform "" --log x`,
			want:    []string{"beacon-hooks", "--platform", "", "--log", "x"},
		},
		{
			name:    "runs of whitespace and tabs collapse",
			command: "beacon-hooks\t--platform   cursor  ",
			want:    []string{"beacon-hooks", "--platform", "cursor"},
		},
		{
			// A backslash is a path separator here, not an escape. Treating it as one would corrupt
			// every Windows path this parses.
			name:    "backslashes survive",
			command: `C:\beacon\beacon-hooks.exe --log C:\logs\runtime.jsonl`,
			want:    []string{`C:\beacon\beacon-hooks.exe`, "--log", `C:\logs\runtime.jsonl`},
		},
		{
			name:    "empty",
			command: "   ",
			want:    nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := commandFields(tc.command); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("commandFields = %#v, want %#v", got, tc.want)
			}
		})
	}
}

// TestFlagsFormIsRecognizedAsAnEndpointHook covers the regression that motivated this change.
//
// The flags form carries no BEACON_ENDPOINT_MODE=1 prefix and does name a platform, so it fell past
// both branches of isEndpointHookCommand and reported false. Every runtime's repair would have added
// a duplicate hook beside the one Beacon had just written, and uninstall would have left it behind --
// with nothing failing anywhere to say so.
func TestFlagsFormIsRecognizedAsAnEndpointHook(t *testing.T) {
	command := `"C:\Program Files\Beacon\hooks\beacon-hooks.exe" --platform cursor` +
		` --log "C:\ProgramData\Beacon\Endpoint\logs\runtime.jsonl"` +
		` --config "C:\ProgramData\Beacon\Endpoint\config.json"`

	if !isEndpointHookCommand(command, "cursor") {
		t.Fatal("the flags form is not recognized as an endpoint hook; repair would duplicate it")
	}
	// Still scoped to its own runtime: matching another platform is the direction that rewrites
	// somebody else's hook.
	if isEndpointHookCommand(command, "vscode") {
		t.Fatal("a cursor hook matched when asked about vscode")
	}
}

func TestEnvPrefixFormIsStillRecognized(t *testing.T) {
	// Already installed on every POSIX machine. A repair rewrites it, but it has to be found first.
	command := `BEACON_ENDPOINT_MODE=1 BEACON_ENDPOINT_LOG='/var/log/beacon-agent/runtime.jsonl' ` +
		`BEACON_ENDPOINT_CONFIG='/etc/beacon/endpoint/config.json' '/opt/beacon/hooks/beacon-hooks' --platform cursor`

	if !isEndpointHookCommand(command, "cursor") {
		t.Fatal("the env-prefix form is no longer recognized; a repair would duplicate every " +
			"already-installed POSIX hook")
	}
	if isEndpointHookCommand(command, "vscode") {
		t.Fatal("a cursor hook matched when asked about vscode")
	}
}

// TestAPlatformNamedWithAnEqualsSignCountsAsNamingOne closes a false positive.
//
// The old check asked whether the command contained "--platform " with a trailing space, which missed
// `--platform=cursor`. A command naming no platform is treated as an any-platform install, so that
// spelling made a hook match when asked about *every* runtime -- and repair rewrites what it matches.
//
// Both halves are asserted, because the fix is only safe if it did not also stop recognizing Beacon's
// own hooks written that way.
func TestAPlatformNamedWithAnEqualsSignCountsAsNamingOne(t *testing.T) {
	// No endpoint settings, so this belongs to whoever wrote it -- and must not be claimed for a
	// runtime it does not name. Before the fix, it was claimed for all of them.
	foreign := `/opt/beacon/hooks/beacon-hooks --platform=grok`
	if isEndpointHookCommand(foreign, "cursor") {
		t.Fatal("--platform=grok was claimed when asked about cursor; repair would rewrite it")
	}

	// With endpoint settings it is Beacon's, and the equals spelling must still resolve to the
	// platform it names.
	own := `/opt/beacon/hooks/beacon-hooks --platform=cursor --log=/var/log/beacon-agent/runtime.jsonl`
	if !isEndpointHookCommand(own, "cursor") {
		t.Fatal("an endpoint hook written with --platform=cursor is not matched for cursor")
	}
	if isEndpointHookCommand(own, "vscode") {
		t.Fatal("--platform=cursor matched when asked about vscode")
	}
}

func TestAPlatformlessBeaconHookStillMatchesAnyRuntime(t *testing.T) {
	// Written before --platform existed. Repair has to keep finding these or it will leave two.
	if !isEndpointHookCommand(`/opt/beacon/hooks/beacon-hooks pre-tool`, "cursor") {
		t.Fatal("a hook naming no platform should match any runtime")
	}
}

func TestCommandHasPlatformReadsQuotedAndSeparatedValues(t *testing.T) {
	cases := []struct {
		command  string
		platform string
		want     bool
	}{
		{`beacon-hooks --platform cursor`, "cursor", true},
		{`beacon-hooks --platform "cursor"`, "cursor", true},
		{`beacon-hooks --platform=cursor`, "cursor", true},
		{`beacon-hooks --platform cursor`, "vscode", false},
		// The path contains a space and the platform follows it. With whitespace splitting the path
		// becomes two tokens; the flag still has to be found after them.
		{`"C:\Program Files\Beacon\beacon-hooks.exe" --platform vscode`, "vscode", true},
		// Nothing to compare against.
		{`beacon-hooks --platform`, "cursor", false},
		{`beacon-hooks`, "cursor", false},
	}
	for _, tc := range cases {
		if got := commandHasPlatform(tc.command, tc.platform); got != tc.want {
			t.Fatalf("commandHasPlatform(%q, %q) = %v, want %v", tc.command, tc.platform, got, tc.want)
		}
	}
}

// TestABareBeaconHooksInvocationIsNotClaimedAsAnEndpointHook keeps the false-positive direction shut.
//
// Someone may run this binary for a reason Beacon did not configure. Such a command names a platform
// and carries no endpoint settings, so it belongs to whoever wrote it.
func TestABareBeaconHooksInvocationIsNotClaimedAsAnEndpointHook(t *testing.T) {
	if isEndpointHookCommand(`beacon-hooks pre-tool --platform grok`, "cursor") {
		t.Fatal("a hook for another platform with no endpoint settings was claimed")
	}
}

func TestCommandCarriesEndpointSettingsRecognizesBothSpellings(t *testing.T) {
	carries := []string{
		`BEACON_ENDPOINT_MODE=1 beacon-hooks --platform cursor`,
		`beacon-hooks --platform cursor --log /var/log/beacon-agent/runtime.jsonl`,
		`beacon-hooks --platform cursor --config /etc/beacon/endpoint/config.json`,
		`beacon-hooks --platform cursor --cli /opt/beacon/bin/beacon`,
		`beacon-hooks --platform cursor --log=/var/log/beacon-agent/runtime.jsonl`,
	}
	for _, command := range carries {
		if !commandCarriesEndpointSettings(command) {
			t.Fatalf("endpoint settings not detected in %q", command)
		}
	}

	// A path that merely contains the text of a flag is not that flag. This is what token comparison
	// buys over a substring search, and why the quoted path has to stay whole.
	notCarries := []string{
		`beacon-hooks --platform cursor`,
		`"C:\tools\--log\beacon-hooks.exe" --platform cursor`,
		`beacon-hooks --platform cursor --logfile /tmp/x`,
	}
	for _, command := range notCarries {
		if commandCarriesEndpointSettings(command) {
			t.Fatalf("endpoint settings falsely detected in %q", command)
		}
	}
}

// The two builders of a hook invocation must stay in step.
//
// endpointCommandPrefix writes a command line for runtimes that hand a string to a shell;
// endpointCommandArgs builds argv for runtimes that spawn the binary themselves. A flag added to
// one and not the other means one runtime's hooks quietly stop reading the endpoint config, or stop
// reporting inventory, with nothing failing anywhere. Comparing the prefix's own tokens against
// argv is what keeps that from happening.
func TestEndpointCommandArgsMatchPrefix(t *testing.T) {
	cases := []struct {
		name       string
		binaryPath string
		logPath    string
		configPath string
	}{
		{name: "all settings", binaryPath: "/opt/beacon/hooks/beacon-hooks", logPath: "/var/log/beacon/runtime.jsonl", configPath: "/etc/beacon/config.json"},
		{name: "no log", binaryPath: "/opt/beacon/hooks/beacon-hooks", configPath: "/etc/beacon/config.json"},
		{name: "no config", binaryPath: "/opt/beacon/hooks/beacon-hooks", logPath: "/var/log/beacon/runtime.jsonl"},
		{name: "windows paths with spaces", binaryPath: `C:\Program Files\Beacon\beacon-hooks.exe`, logPath: `C:\ProgramData\Beacon\runtime.jsonl`, configPath: `C:\ProgramData\Beacon\config.json`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			prefix := endpointCommandPrefix("cline", tc.binaryPath, tc.logPath, tc.configPath)
			args := endpointCommandArgs("cline", tc.binaryPath, tc.logPath, tc.configPath)
			tokens := commandFields(prefix)
			if len(tokens) != len(args) {
				t.Fatalf("prefix tokens %v do not match argv %v", tokens, args)
			}
			for i := range args {
				if tokens[i] != args[i] {
					t.Errorf("token %d = %q, argv = %q", i, tokens[i], args[i])
				}
			}
		})
	}
}
