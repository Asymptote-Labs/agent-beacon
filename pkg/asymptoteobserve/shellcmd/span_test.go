package shellcmd

import (
	"reflect"
	"strings"
	"testing"
)

// Carried is the command line a carrier hands on: the words that say what it runs.
func TestCarried(t *testing.T) {
	for _, tc := range []struct {
		command string
		want    string // "" when the command carries none
	}{
		{`xargs -0 -n 1 rm -f`, `rm -f`},
		{`xargs -I{} cp "{}" "$DEST/"`, `cp "{}" "$DEST/"`},
		{`sudo xargs -r kill -9`, `kill -9`},
		{`xargs`, ``},
		{`find . -name '*.log' -exec rm -f {} +`, `rm -f {}`},
		{`find . -type f -exec chmod 644 {} \; -execdir sh -c 'echo "$1"' _ {} ';'`, "chmod 644 {}\nsh -c 'echo \"$1\"' _ {}"},
		{`find . -name '*.go' -print`, ``},
		{`find . -exec rm {}`, ``},
		{`ssh -p 2222 -i ~/.ssh/key deploy@host.example 'cd /srv && rm -rf releases'`, `cd /srv && rm -rf releases`},
		{`ssh host.example rm -rf /srv/releases`, `rm -rf /srv/releases`},
		{`ssh host.example "ls $DIR"`, `ls $DIR`},
		{`ssh host.example`, ``},
		{`docker exec -it -u root web rm -rf /var/www`, `rm -rf /var/www`},
		{`podman exec web sh -c 'id; whoami'`, `sh -c 'id; whoami'`},
		{`docker exec web`, ``},
		{`docker run --rm alpine rm -rf /data`, ``},
		{`kubectl exec -n prod web-0 -c app -- rm -rf /data`, `rm -rf /data`},
		{`kubectl exec web-0 ls`, ``},
		{`su - deploy -c 'git push --force'`, `git push --force`},
		{`su --command='id' deploy`, `id`},
		{`su deploy`, ``},
		{`eval "git push" --force`, `git push --force`},
		{`eval "$CMD"`, `$CMD`},
		{`watch -n 5 'kubectl get pods | grep web'`, `kubectl get pods | grep web`},
		{`trap 'rm -rf "$TMP"' EXIT`, `rm -rf "$TMP"`},
		{`trap - EXIT`, ``},
		{`trap -p`, ``},
		{`rm -rf x`, ``},
	} {
		commands, err := Parse(tc.command)
		if err != nil || len(commands) == 0 {
			t.Fatalf("Parse(%q) = %+v, %v", tc.command, commands, err)
		}
		got, ok := commands[0].Carried()
		if got != tc.want || ok != (tc.want != "") {
			t.Errorf("Carried(%q) = %q, %v, want %q", tc.command, got, ok, tc.want)
		}
	}
}

// WithScripts puts what a carrier runs after it, like a shell's script, and says whether it
// read all of it.
func TestWithScriptsReadsCarriers(t *testing.T) {
	for _, tc := range []struct {
		command string
		want    []string // name of each command, * after one whose command line was read
	}{
		{`git ls-files | xargs rm -f`, []string{"git", "xargs*", "rm"}},
		{`find . -name '*.sh' -exec sh -c 'chmod +x "$1"; git add "$1"' _ {} \;`, []string{"find*", "sh*", "chmod", "git"}},
		{`ssh host.example 'cd /srv && sudo systemctl restart web'`, []string{"ssh*", "cd", "systemctl"}},
		{`docker exec web bash -lc 'id && rm -rf /tmp/x'`, []string{"docker*", "bash*", "id", "rm"}},
		{`eval "$(ssh-agent -s)"`, []string{"eval*", "", "ssh-agent", "ssh-agent"}},
		// A command line that ends early is listed as far as it goes, and is not read.
		{`ssh host.example 'echo "unterminated'`, []string{"ssh", "echo"}},
		{`bash -c 'ls; sh -c "id"'`, []string{"bash*", "ls", "sh*", "id"}},
		{`echo 'rm -rf /' | sh`, []string{"echo", "sh"}},
		{`bash deploy.sh`, []string{"bash"}},
		{`xargs`, []string{"xargs"}},
	} {
		commands, err := Parse(tc.command)
		if err != nil {
			t.Fatalf("Parse(%q): %v", tc.command, err)
		}
		var got []string
		for _, c := range WithScripts(commands, 3) {
			name := c.Name()
			if c.RunsRead() {
				name += "*"
			}
			got = append(got, name)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("WithScripts(%q) = %q, want %q", tc.command, got, tc.want)
		}
	}
}

// The span of a command is its own text, and the part of the line before it that can reach
// it: an earlier stage of its pipeline, a command that wrote a file, a variable the line may
// have set, the loop or function it is in. A mention elsewhere on the line is not in it.
func TestSpans(t *testing.T) {
	for _, tc := range []struct {
		command string
		name    string // the command whose span is asked for
		want    string
	}{
		// Its own words, whatever else the line holds.
		{`git commit -m 'never git push --force' && ./run-tests.sh --fast; echo done`, "run-tests.sh", `./run-tests.sh --fast`},
		{`./deploy.sh 2>&1 | tail -3`, "deploy.sh", `./deploy.sh 2>&1`},
		// A here-document is the command's, though the line holds it later.
		{"python3 - <<'EOF' | tail -1\nimport os\nos.system('id')\nEOF\necho done", "python3", "python3 - <<'EOF'\nimport os\nos.system('id')\n"},
		// What a pipe sends it.
		{`echo hi; echo 'rm -rf /' | tr a-z a-z | sh`, "sh", `echo 'rm -rf /' | tr a-z a-z | sh`},
		{"cat <<'EOF' | sh\nrm -rf /\nEOF", "sh", "cat <<'EOF' | sh\nrm -rf /\n"},
		{`{ echo 'rm -rf /'; } | sh`, "sh", `echo 'rm -rf /'; } | sh`},
		// A file an earlier command wrote, which it may read or run without naming it.
		{`echo ok; printf 'all:\n\trm -rf /\n' > Makefile; make`, "make", `printf 'all:\n\trm -rf /\n' > Makefile; make`},
		{"echo ok && cat > /tmp/x.sh <<'EOF'\nrm -rf /\nEOF\nbash /tmp/x.sh", "bash", "cat > /tmp/x.sh <<'EOF'\nrm -rf /\nEOF\nbash /tmp/x.sh"},
		{`echo ok; go test ./... > /dev/null; ./run.sh`, "run.sh", `./run.sh`},
		{`echo ok; rm -f x.sh; ./run.sh`, "run.sh", `./run.sh`},
		// A variable the line may have set, and not one the environment sets.
		{`echo ok; CMD='rm -rf /'; eval "$CMD"`, "eval", `echo ok; CMD='rm -rf /'; eval "$CMD"`},
		{`echo ok; "$HOME"/bin/tool --all`, "tool", `"$HOME"/bin/tool --all`},
		{`echo ok; HOME=/tmp/h; "$HOME"/bin/tool`, "tool", `echo ok; HOME=/tmp/h; "$HOME"/bin/tool`},
		{`echo ok; export TOOL=/tmp/t; ./run.sh "$TOOL"`, "run.sh", `echo ok; export TOOL=/tmp/t; ./run.sh "$TOOL"`},
		// A loop's body runs again, and a function's wherever it is called.
		{`echo ok; for f in a b; do ./run.sh; echo 'rm -rf /' > run.sh; done; echo end`, "run.sh", `for f in a b; do ./run.sh; echo 'rm -rf /' > run.sh; done`},
		{`f() { ./run.sh; }; echo 'rm -rf /' > run.sh; f`, "run.sh", `f() { ./run.sh; }; echo 'rm -rf /' > run.sh; f`},
	} {
		commands, err := Parse(tc.command)
		if err != nil {
			t.Fatalf("Parse(%q): %v", tc.command, err)
		}
		commands = WithScripts(commands, 3)
		writes := make([]bool, len(commands))
		for i, c := range commands {
			for _, e := range c.FileWrites() {
				if (e.Op == OpWrite || e.Op == OpAppend) && !strings.HasPrefix(e.Path, "/dev/") {
					writes[i] = true
				}
			}
		}
		spans := Spans(commands, writes)
		found := false
		for i, c := range commands {
			if c.Name() != tc.name {
				continue
			}
			found = true
			if spans[i] != tc.want {
				t.Errorf("span of %s in %q:\n got %q\nwant %q", tc.name, tc.command, spans[i], tc.want)
			}
			break
		}
		if !found {
			t.Errorf("%q: no command named %s", tc.command, tc.name)
		}
	}
}

// A command WithScripts added has the span of the command of the line it came from.
func TestSpansOfScriptCommands(t *testing.T) {
	commands, err := Parse(`git commit -m 'mention ./x.sh'; echo go | bash -c 'cat; ./x.sh'; echo end`)
	if err != nil {
		t.Fatal(err)
	}
	commands = WithScripts(commands, 3)
	spans := Spans(commands, make([]bool, len(commands)))
	for i, c := range commands {
		if c.Name() == "x.sh" {
			if want := `echo go | bash -c 'cat; ./x.sh'`; spans[i] != want {
				t.Errorf("span of the script's command = %q, want %q", spans[i], want)
			}
			return
		}
	}
	t.Fatal("no command named x.sh")
}
