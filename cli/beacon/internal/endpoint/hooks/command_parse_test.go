package hooks

import (
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/hookcmd"
)

// A profile directory with an apostrophe (C:\Users\O'Brien) puts \' between two quoted runs in every
// path of the command. Read as an unterminated quote, it swallowed --platform, and the hook matched
// every runtime.
func TestAHookUnderAnApostrophePathKeepsItsPlatform(t *testing.T) {
	for _, home := range []string{`C:\Users\O'Brien`, `/home/o'brien`} {
		command := endpointCommandPrefix("claude", home+`/.beacon/endpoint/hooks/beacon-hooks`,
			home+`/.beacon/endpoint/logs/runtime.jsonl`, home+`/.beacon/endpoint/config.json`)
		if !hookcmd.IsEndpointHook(command, "claude") {
			t.Errorf("%s: not matched for claude", command)
		}
		if hookcmd.IsEndpointHook(command, "cursor") {
			t.Errorf("%s: a claude hook matched when asked about cursor", command)
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
		{name: "apostrophes", binaryPath: `C:\Users\O'Brien\.beacon\beacon-hooks.exe`, logPath: `/home/o'brien/it's/runtime.jsonl`, configPath: `/home/o'brien/'quoted'/config.json'`},
		{name: "shell metacharacters", binaryPath: "/opt/$HOME/`id`/beacon-hooks", logPath: `/var/log/a b;c|d&e/runtime.jsonl`, configPath: `/etc/"dq"/config.json`},
		{name: "unc and non-ascii", binaryPath: `\\server\share\Beacon\beacon-hooks.exe`, logPath: `/Users/zoë/日誌/runtime.jsonl`, configPath: `C:\ProgramData\Beacon\config.json`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			prefix := endpointCommandPrefix("cline", tc.binaryPath, tc.logPath, tc.configPath)
			args := endpointCommandArgs("cline", tc.binaryPath, tc.logPath, tc.configPath)
			tokens := hookcmd.Fields(prefix)
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

// Inventory is written by the scheduled job, so the hook command no longer carries a path to the
// CLI. Emitting it again would be harmless but would re-open the door to hook-driven inventory.
func TestEndpointCommandBuildersNoLongerEmitCLI(t *testing.T) {
	prefix := endpointCommandPrefix("claude", "/opt/beacon/hooks/beacon-hooks", "/var/log/beacon/runtime.jsonl", "/etc/beacon/config.json")
	if strings.Contains(prefix, "--cli") {
		t.Fatalf("prefix must not carry --cli: %s", prefix)
	}
	for _, arg := range endpointCommandArgs("claude", "/opt/beacon/hooks/beacon-hooks", "/var/log/beacon/runtime.jsonl", "/etc/beacon/config.json") {
		if arg == "--cli" {
			t.Fatal("argv must not carry --cli")
		}
	}
	if !hookcmd.CarriesEndpointSettings("beacon-hooks --platform claude --cli /opt/beacon/bin/beacon session-start") {
		t.Fatal("a hook written by an older version, carrying only --cli, must still be recognized")
	}
}
