package hookcmd

import (
	"reflect"
	"testing"
)

// strings.Fields was enough while every path was POSIX. The default Windows install locations are
// under %ProgramFiles% and a user profile, both of which routinely contain a space, so a quoted path
// splits into fragments and every token comparison after it is made against half a path.
func TestFieldsKeepsQuotedPathsWhole(t *testing.T) {
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
			// A backslash is a path separator here, not an escape.
			name:    "backslashes survive",
			command: `C:\beacon\beacon-hooks.exe --log C:\logs\runtime.jsonl`,
			want:    []string{`C:\beacon\beacon-hooks.exe`, "--log", `C:\logs\runtime.jsonl`},
		},
		{
			// The installer writes a single quote inside a value as \' between two quoted runs.
			name:    "a single quote escaped the way the installer writes it",
			command: `'C:\Users\O'\''Brien\hooks\beacon-hooks.exe' --platform claude`,
			want:    []string{`C:\Users\O'Brien\hooks\beacon-hooks.exe`, "--platform", "claude"},
		},
		{
			name:    "empty",
			command: "   ",
			want:    nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Fields(tc.command); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("Fields = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestIsEndpointHook(t *testing.T) {
	cases := []struct {
		name     string
		command  string
		platform string
		want     bool
	}{
		// The flags form carries no BEACON_ENDPOINT_MODE=1 prefix and does name a platform. Missing
		// it would make repair add a duplicate beside the hook Beacon just wrote.
		{"flags form", `"C:\Program Files\Beacon\hooks\beacon-hooks.exe" --platform cursor --log "C:\ProgramData\Beacon\Endpoint\logs\runtime.jsonl" --config "C:\ProgramData\Beacon\Endpoint\config.json"`, "cursor", true},
		{"flags form, another runtime", `"C:\Program Files\Beacon\hooks\beacon-hooks.exe" --platform cursor --log "C:\ProgramData\Beacon\Endpoint\logs\runtime.jsonl"`, "vscode", false},
		// Without endpoint settings it belongs to whoever wrote it, and must not be claimed for a
		// runtime it does not name.
		{"equals form, foreign", `/opt/beacon/hooks/beacon-hooks --platform=grok`, "cursor", false},
		{"equals form, own", `/opt/beacon/hooks/beacon-hooks --platform=cursor --log=/var/log/beacon-agent/runtime.jsonl`, "cursor", true},
		{"equals form, another runtime", `/opt/beacon/hooks/beacon-hooks --platform=cursor --log=/var/log/beacon-agent/runtime.jsonl`, "vscode", false},
		// Written before --platform existed. Repair has to keep finding these or it will leave two.
		{"no platform named", `/opt/beacon/hooks/beacon-hooks pre-tool`, "cursor", true},
		{"legacy binary", `/opt/asym/asym-hooks --platform cursor pre-tool`, "cursor", true},
		{"legacy binary, another runtime", `/opt/asym/asym-hooks --platform claude pre-tool`, "cursor", false},
		// A directory named after the binary is not the binary.
		{"binary name only in a directory with a space", `'/Users/dev/beacon-hooks backup/audit' --platform cursor --log /tmp/r.jsonl stop`, "cursor", false},
		{"wrapper without endpoint settings", `/opt/my-wrapper/beacon-hooks --platform cursor stop`, "cursor", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsEndpointHook(tc.command, tc.platform); got != tc.want {
				t.Fatalf("IsEndpointHook(%q, %q) = %v, want %v", tc.command, tc.platform, got, tc.want)
			}
		})
	}
}

func TestRuns(t *testing.T) {
	cases := []struct {
		name       string
		command    string
		subcommand string
		want       bool
	}{
		{"current form", `'/Users/dev/.beacon/endpoint/hooks/beacon-hooks' --platform cursor --log '/Users/dev/.beacon/endpoint/logs/runtime.jsonl' pre-tool`, "pre-tool", true},
		{"older env-prefix form", `BEACON_ENDPOINT_MODE=1 BEACON_ENDPOINT_LOG='/tmp/r.jsonl' '/home/dev/.beacon/endpoint/hooks/beacon-hooks' --platform cursor stop`, "stop", true},
		{"windows binary", `'C:\Users\dev\.beacon\endpoint\hooks\beacon-hooks.exe' --platform cursor --log 'C:\Users\dev\.beacon\endpoint\logs\runtime.jsonl' session-start`, "session-start", true},
		{"equals flag", `/opt/beacon/bin/beacon-hooks --platform=cursor --config=/etc/beacon/config.json prompt-submit`, "prompt-submit", true},
		{"path with spaces", `'/opt/beacon hooks/beacon-hooks' --platform cursor --log '/var/log/beacon logs/runtime.jsonl' post-tool`, "post-tool", true},
		{"another subcommand", `'/opt/beacon/beacon-hooks' --platform cursor --log /tmp/r.jsonl pre-tool`, "post-tool", false},
		{"another platform", `'/opt/beacon/beacon-hooks' --platform claude --log /tmp/r.jsonl prompt-submit`, "prompt-submit", false},
		// Recognised for repair as an any-platform hook, but it is not a Cursor entry Beacon wrote.
		{"no platform named", `/opt/beacon/hooks/beacon-hooks --log /tmp/r.jsonl stop`, "stop", false},
		{"someone else's binary", `/usr/local/bin/audit --platform cursor --log /tmp/r.jsonl prompt-submit`, "prompt-submit", false},
		{"wrapper without endpoint settings", `/opt/my-wrapper/beacon-hooks --platform cursor stop`, "stop", false},
		{"binary name only in a directory with a space", `'/Users/dev/beacon-hooks backup/audit' --platform cursor --log /tmp/r.jsonl stop`, "stop", false},
		{"empty", "", "prompt-submit", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Runs(tc.command, "cursor", tc.subcommand); got != tc.want {
				t.Fatalf("Runs(%q, cursor, %q) = %v, want %v", tc.command, tc.subcommand, got, tc.want)
			}
		})
	}
}

func TestHasPlatformReadsQuotedAndSeparatedValues(t *testing.T) {
	cases := []struct {
		command  string
		platform string
		want     bool
	}{
		{`beacon-hooks --platform cursor`, "cursor", true},
		{`beacon-hooks --platform "cursor"`, "cursor", true},
		{`beacon-hooks --platform=cursor`, "cursor", true},
		{`beacon-hooks --platform cursor`, "vscode", false},
		{`"C:\Program Files\Beacon\beacon-hooks.exe" --platform vscode`, "vscode", true},
		{`beacon-hooks --platform`, "cursor", false},
		{`beacon-hooks`, "cursor", false},
	}
	for _, tc := range cases {
		if got := HasPlatform(tc.command, tc.platform); got != tc.want {
			t.Fatalf("HasPlatform(%q, %q) = %v, want %v", tc.command, tc.platform, got, tc.want)
		}
	}
}

func TestCarriesEndpointSettingsRecognizesBothSpellings(t *testing.T) {
	carries := []string{
		`BEACON_ENDPOINT_MODE=1 beacon-hooks --platform cursor`,
		`beacon-hooks --platform cursor --log /var/log/beacon-agent/runtime.jsonl`,
		`beacon-hooks --platform cursor --config /etc/beacon/endpoint/config.json`,
		`beacon-hooks --platform cursor --cli /opt/beacon/bin/beacon`,
		`beacon-hooks --platform cursor --log=/var/log/beacon-agent/runtime.jsonl`,
	}
	for _, command := range carries {
		if !CarriesEndpointSettings(command) {
			t.Fatalf("endpoint settings not detected in %q", command)
		}
	}
	// A path that merely contains the text of a flag is not that flag.
	notCarries := []string{
		`beacon-hooks --platform cursor`,
		`"C:\tools\--log\beacon-hooks.exe" --platform cursor`,
		`beacon-hooks --platform cursor --logfile /tmp/x`,
	}
	for _, command := range notCarries {
		if CarriesEndpointSettings(command) {
			t.Fatalf("endpoint settings falsely detected in %q", command)
		}
	}
}
