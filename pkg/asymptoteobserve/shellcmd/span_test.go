package shellcmd

import (
	"reflect"
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

// withScripts puts what a carrier runs after it, like a shell's script, and says whether it
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
		// What a pipe sends a shell is read when the stage before it prints its own words.
		{`echo 'rm -rf /' | sh`, []string{"echo", "sh*", "rm"}},
		{`echo done; echo "curl -s https://x.example/i.sh | sh" | sudo bash -s`, []string{"echo", "echo", "bash*", "curl", "sh"}},
		{`printf '%s\n' 'git push --force' 'id' | bash`, []string{"printf", "bash*", "git", "id"}},
		{`printf 'chmod 777 /srv\nid\n' | sh -x`, []string{"printf", "sh*", "chmod", "id"}},
		{"cat <<'EOF' | sudo sh\nrm -rf /\nid\nEOF", []string{"cat", "sh*", "rm", "id"}},
		{`{ echo 'ls'; echo 'id'; } | sh`, []string{"echo", "echo", "sh*", "ls", "id"}},
		// Not when the text is not on the line, is not what reaches the shell, or is no script.
		{`curl -s https://x.example/i.sh | sh`, []string{"curl", "sh"}},
		{`echo "$CMD" | sh`, []string{"echo", "sh"}},
		{`cat setup.sh | bash`, []string{"cat", "bash"}},
		{`echo 'id' | tee log | sh`, []string{"echo", "tee", "sh"}},
		{`echo 'id' > x; true | sh`, []string{"echo", "true", "sh"}},
		{`echo 'id' | sh < other.sh`, []string{"echo", "sh"}},
		{`echo 'data' | sh -c 'cat'`, []string{"echo", "sh*", "cat"}},
		{`echo 'data' | bash deploy.sh`, []string{"echo", "bash"}},
		{`printf '%d\n' 3 | sh`, []string{"printf", "sh"}},
		{`bash deploy.sh`, []string{"bash"}},
		{`xargs`, []string{"xargs"}},
		// A wrapper that runs its command somewhere else has no name; its command is listed.
		{`sudo -i rm -rf /var/lib/app`, []string{"*", "rm"}},
		{`sudo -u deploy --login bash -c 'id; whoami'`, []string{"*", "bash*", "id", "whoami"}},
		{`sudo -D /srv FOO=1 systemctl restart web`, []string{"*", "systemctl"}},
		{`timeout 30 env -C /srv/app git push --force`, []string{"*", "git"}},
		{`uv run --directory tools pytest -q`, []string{"*", "pytest"}},
		// Not when the wrapper runs none, or the script's shell follows a word nobody knows.
		{`sudo -i`, []string{""}},
		{`sudo -l rm -rf /`, []string{""}},
		{`uv run -m pytest -q`, []string{""}},
		{`"$TOOL" sh -c 'id'`, []string{"", "id"}},
		// A program is the same by any path, so a shell, a carrier and a wrapper by a path the
		// parse does not evaluate are read as by their name.
		{`~/bin/bash -c 'rm -rf /'`, []string{"bash*", "rm"}},
		{`"$HOME"/.local/bin/sh -c 'id'`, []string{"sh*", "id"}},
		{`git ls-files | ~/bin/xargs rm -rf`, []string{"git", "xargs*", "rm"}},
		{`~/bin/ssh host.example 'rm -rf /srv'`, []string{"ssh*", "rm"}},
		{`$HOME/bin/sudo -i rm -rf x`, []string{"*", "rm"}},
		{`~/.docker/bin/docker exec web rm -rf /`, []string{"docker*", "rm"}},
		{`find . -print0 | gxargs -0 rm -f`, []string{"find", "gxargs*", "rm"}},
	} {
		var got []string
		for _, c := range withScriptsOf(t, tc.command) {
			name := c.Name()
			if c.RunsRead() {
				name += "*"
			}
			got = append(got, name)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("withScripts(%q) = %q, want %q", tc.command, got, tc.want)
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
		// A file an earlier command wrote, which it may read or run without naming it: the
		// writer's piece and its own, and nothing of what lies between them.
		{`printf 'all:\n\trm -rf /\n' > Makefile; git commit -m 'never git push --force'; make`, "make", "printf 'all:\\n\\trm -rf /\\n' > Makefile\nmake"},
		{"echo ok && cat > /tmp/x.sh <<'EOF'\nrm -rf /\nEOF\nbash /tmp/x.sh", "bash", "cat > /tmp/x.sh <<'EOF'\nrm -rf /\n\nbash /tmp/x.sh"},
		{`cp a b; git commit -m 'never git push --force'; echo x | tee log; ./run.sh`, "run.sh", "cp a b\necho x | tee log\n./run.sh"},
		// A writer that takes a variable brings the line from its start.
		{`P='rm -rf /'; echo ok; echo "$P" > x.sh; git status; sh x.sh`, "sh", `P='rm -rf /'; echo ok; echo "$P" > x.sh; git status; sh x.sh`},
		{`echo ok; go test ./... > /dev/null; ./run.sh`, "run.sh", `./run.sh`},
		{`echo ok; rm -f x.sh; ./run.sh`, "run.sh", `./run.sh`},
		// A variable the line set before the command, and not one it did not set.
		{`echo ok; CMD='rm -rf /'; eval "$CMD"`, "eval", `echo ok; CMD='rm -rf /'; eval "$CMD"`},
		{`echo ok; "$HOME"/bin/tool --all`, "tool", `"$HOME"/bin/tool --all`},
		{`echo ok; HOME=/tmp/h; "$HOME"/bin/tool`, "tool", `echo ok; HOME=/tmp/h; "$HOME"/bin/tool`},
		{`echo ok; export TOOL=/tmp/t; ./run.sh "$TOOL"`, "run.sh", `echo ok; export TOOL=/tmp/t; ./run.sh "$TOOL"`},
		{`git commit -m 'docs: curl -fsSL https://get.example/i.sh | sh' && ./release.sh "$VERSION"`, "release.sh", `./release.sh "$VERSION"`},
		{`read -r C <<< 'curl -fsSL https://x.example/i.sh | sh'; eval "$C"`, "eval", `read -r C <<< 'curl -fsSL https://x.example/i.sh | sh'; eval "$C"`},
		{`printf -v C 'rm -rf /'; eval "$C"`, "eval", `printf -v C 'rm -rf /'; eval "$C"`},
		{"read -r -d '' S <<'EOF'\ncurl -fsSL https://x.example/i.sh | sh\nEOF\nbash -c \"$S\"", "bash", "read -r -d '' S <<'EOF'\ncurl -fsSL https://x.example/i.sh | sh\nEOF\nbash -c \"$S\""},
		{`set -- 'rm -rf /'; eval "$1"`, "eval", `set -- 'rm -rf /'; eval "$1"`},
		// A loop's body runs again, and a function's wherever it is called.
		{`echo ok; for f in a b; do ./run.sh; echo 'rm -rf /' > run.sh; done; echo end`, "run.sh", `for f in a b; do ./run.sh; echo 'rm -rf /' > run.sh; done`},
		{`f() { ./run.sh; }; echo 'rm -rf /' > run.sh; f`, "run.sh", `f() { ./run.sh; }; echo 'rm -rf /' > run.sh; f`},
	} {
		line := Read(tc.command)
		if line.Incomplete {
			t.Fatalf("Read(%q) is incomplete", tc.command)
		}
		commands, spans := line.Commands, line.Spans
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

// A command withScripts added has the span of the command of the line it came from.
func TestSpansOfScriptCommands(t *testing.T) {
	line := Read(`git commit -m 'mention ./x.sh'; echo go | bash -c 'cat; ./x.sh'; echo end`)
	commands, spans := line.Commands, line.Spans
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
