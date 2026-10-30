package shellcmd

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// TestFileEffects pins the file rules gitlink.ShellWrittenPaths reads. An effect of a command
// after a cd is written "cd <op> <path>".
func TestFileEffects(t *testing.T) {
	for _, tc := range []struct {
		command string
		want    []string
	}{
		{"rm -f a.txt b.txt", []string{"delete a.txt", "delete b.txt"}},
		{"rm -rf -- -weird", []string{"delete -weird"}},
		{"touch new.go && go test ./...", []string{"touch new.go"}},
		{"mv old.go new.go", []string{"move_from old.go", "move_to new.go"}},
		{"git mv src/a.go src/b.go", []string{"move_from src/a.go", "move_to src/b.go"}},
		{"git rm --cached secret.env", []string{"delete secret.env"}},
		{"cp -r template out/dir", []string{"write out/dir"}},
		{"cp -t out a.txt src/b.txt", []string{"write out/a.txt", "write out/b.txt"}},
		{"cp --target-directory=out a.txt", []string{"write out/a.txt"}},
		{"install -Dt bin tool", []string{"write bin/tool"}},
		{"ln -s -t links ../real.txt", []string{"link links/real.txt"}},
		{"install -m 0755 -t /usr/local/bin build/tool", []string{"write /usr/local/bin/tool"}},
		{"install -o root -g wheel -t /etc/app conf.yaml", []string{"write /etc/app/conf.yaml"}},
		{"install -m 0644 conf.yaml /etc/app/conf.yaml", []string{"write /etc/app/conf.yaml"}},
		{`cp -t "$DEST" a.txt`, nil},
		{`cp -t out "$SRC" a.txt`, []string{"write out/a.txt"}},
		{"mv -t archive a.txt b.txt", []string{"move_from a.txt", "move_from b.txt", "move_to archive/a.txt", "move_to archive/b.txt"}},
		{"truncate -s 0 app.log", []string{"truncate app.log"}},
		{"touch -d '2 hours ago' stamp", []string{"touch stamp"}},
		{"echo hi > out.txt", []string{"write out.txt"}},
		{"echo hi >out.txt 2>&1", []string{"write out.txt"}},
		{"printf x >> log.txt; cat log.txt", []string{"append log.txt"}},
		{"cmd >| forced.txt", []string{"write forced.txt"}},
		{"go build &> build.log", []string{"write build.log"}},
		{"make 2>/dev/null", nil},
		{"cat a.txt | tee copy.txt", []string{"write copy.txt"}},
		{"sed -i 's/a/b/' x.go y.go", []string{"edit x.go", "edit y.go"}},
		{"sed -i.bak -e 's/a/b/' x.go", []string{"edit x.go"}},
		{"sed 's/a/b/' x.go", nil}, // not in place: prints, writes nothing
		{"perl -pi -e 's/a/b/' z.pl", []string{"edit z.pl"}},
		{"perl -Mwarnings -ne 'print if /x/' z.pl", nil}, // -M's module name is not -i
		{"perl -Ilib -ne 'print' z.pl", nil},
		{"perl -MList::Util=sum -lane 'print sum @F' data.txt", nil},
		{"perl -Mstrict -pi -e 's/a/b/' z.pl", []string{"edit z.pl"}},
		{"perl -0777 -pi -e 's/a/b/' z.pl", []string{"edit z.pl"}},
		{"perl -lpi -e 's/a/b/' z.pl", []string{"edit z.pl"}},
		{"sed -n -es/i/j/p x.go y.go", nil}, // -e's script is not -i
		{"perl -pi -e's/a/b/' z.pl", []string{"edit z.pl"}},
		{"sed -i -e's/a/b/' x.go", []string{"edit x.go"}},
		{"sed -i --expression=s/a/b/ x.go", []string{"edit x.go"}},
		{"sed -i -e 's/a/b/' -e 's/c/d/' x.go", []string{"edit x.go"}},
		{"sed -i -f fix.sed x.go", []string{"edit x.go"}},
		{"sed -ie 's/a/b/' x.go", []string{"edit x.go"}}, // GNU: the suffix is "e"
		{"sed -E -i 's/a/b/' x.go", []string{"edit x.go"}},
		{"sed -i 's/x/y/' f", []string{"edit f"}},
		{"sed -i '' 's/x/y/' f", []string{"edit f"}}, // BSD sed: '' is the backup suffix
		{"sed -i '' 's/x/y/' f g", []string{"edit f", "edit g"}},
		{"sed -i '' -e 's/a/b/' a.go", []string{"edit a.go"}},
		{`sed -i "s/$old/$new/" f`, []string{"edit f"}}, // an unknown script is not a suffix
		{`sed -i "s/$a/$b/" x y`, []string{"edit x", "edit y"}},
		{"sudo -u deploy rm /srv/app/old.txt", []string{"delete /srv/app/old.txt"}},
		{"/usr/bin/sudo -u deploy rm /srv/app/old.txt", []string{"delete /srv/app/old.txt"}},
		{"/usr/bin/env FOO=1 rm a.txt", []string{"delete a.txt"}},
		{"sudo -E tee /etc/app.conf < in.txt", []string{"write /etc/app.conf"}},
		{"sudo -i rm old.txt", nil}, // a login shell starts in the target user's home
		{"env FOO=1 rm a.txt", []string{"delete a.txt"}},
		{"env -C sub rm a.txt", nil},
		{"rm a.txt # cleanup b.txt", []string{"delete a.txt"}},
		{"rm {a,b}.txt", []string{"delete a.txt", "delete b.txt"}},
		{"if true; then rm a.txt; fi", []string{"delete a.txt"}},
		{"for f in *.tmp; do rm \"$f\"; done", nil},
		{"echo $((1<<3)) > n.txt\nrm old.txt", []string{"write n.txt", "delete old.txt"}},
		{"grep x <<< \"$v\"\nrm a.txt", []string{"delete a.txt"}},
		{"git commit -m \"$(cat <<'EOF'\nrm -rf dist\nEOF\n)\"", nil},
		{"FOO=1 sudo rm build/out", []string{"delete build/out"}},
		{`rm "file with spaces.txt"`, []string{"delete file with spaces.txt"}},
		{`rm file\ two.txt`, []string{"delete file two.txt"}},
		{"echo '> not a redirect' ; ls", nil},
		{"rm $TARGET", nil}, // unknown value
		{`"$TOOL" rm victim.txt`, nil},
		{`command "$TOOL" rm victim.txt`, nil},
		{`cp first.txt second.txt "$DEST"`, nil},
		{`cp "$SOURCE" output.txt`, []string{"write output.txt"}},
		{`rm known.txt "$UNKNOWN"`, []string{"delete known.txt"}},
		{`"$TOOL" rm victim.txt > known.log`, []string{"write known.log"}},
		{`ROOT="$DIR" cd sub && rm relative.txt`, []string{"cd delete relative.txt"}},
		{`ROOT="$DIR" rm known.txt`, []string{"delete known.txt"}},
		{`"ROOT=$DIR" rm victim.txt`, nil},
		{`"$TOOL=x" rm victim.txt`, nil},
		{`"ROOT"=$DIR rm victim.txt`, nil},
		{`ROOT\=$DIR rm victim.txt`, nil},
		{`ROOT=$DIR rm known.txt`, []string{"delete known.txt"}},
		{"rm *.log", nil},                             // glob
		{"rm \"$(pwd)/x\"", nil},                      // expansion inside quotes
		{"(cd sub && rm x)", []string{"cd delete x"}}, // x is sub/x, which this parser does not follow
		{"cd /tmp && rm /abs/path.txt && rm rel.txt", []string{"cd delete /abs/path.txt", "cd delete rel.txt"}},
		{"rm before.txt; cd sub; rm after.txt", []string{"delete before.txt", "cd delete after.txt"}},
		{"command cd sub && rm after.txt", []string{"cd delete after.txt"}},
		{"ROOT=sub cd sub && touch after.txt", []string{"cd touch after.txt"}},
		{"builtin cd sub && rm after.txt", []string{"cd delete after.txt"}},
		{"command cd sub && rm /abs/path.txt", []string{"cd delete /abs/path.txt"}},
		{"touch before.txt; command pushd sub; rm after.txt", []string{"touch before.txt", "cd delete after.txt"}},
		{"command popd; rm after.txt", []string{"cd delete after.txt"}},
		{"ls -la; go test ./...", nil},
		{"tee -a log.txt < output.txt", []string{"append log.txt"}},
		{"tee log.txt <output.txt", []string{"write log.txt"}},
		{"tee copy.txt <<< hello", []string{"write copy.txt"}},
		{"echo '<' > lt.txt", []string{"write lt.txt"}},
		{"tee '<' out.txt", []string{"write <", "write out.txt"}},
		{"cp a.txt b.txt < /dev/null", []string{"write b.txt"}},
		{"sudo tee /etc/app.conf > /dev/null <<'EOF'\nkey: value\nEOF", []string{"write /etc/app.conf"}},
		{"cat <<EOF > gen.txt\nhello\nEOF", []string{"write gen.txt"}},
		{"cat > deploy.sh <<'EOF'\nrm -rf dist\necho done > status.txt\nEOF\nchmod +x deploy.sh", []string{"write deploy.sh"}},
		{"cat <<-END > a.txt\n\trm b.txt\n\tEND\nrm c.txt", []string{"write a.txt", "delete c.txt"}},
		{"cat <<A <<\"B\" > out.txt\ntouch x.txt\nA\ntouch y.txt\nB\ntouch z.txt", []string{"write out.txt", "touch z.txt"}},
		{"cat > a.md <<'EOF'\nDon't touch\nEOF\nrm b.txt", []string{"write a.md", "delete b.txt"}},
		{"grep -c x <<< 'rm here.txt'", nil},
		{"tee x.txt <<<\"hello\"\nrm y.txt", []string{"write x.txt", "delete y.txt"}},
		{"mv a.txt \"$DEST\"", []string{"move_from a.txt"}},
		{"ln -s ../real.txt link.txt", []string{"link link.txt"}},
		{"tee --append log.txt", []string{"append log.txt"}},
		{"go test &>> test.log", []string{"append test.log"}},
		{"make >&build.log", []string{"write build.log"}},
	} {
		commands, err := Parse(tc.command)
		if err != nil {
			t.Errorf("Parse(%q): %v", tc.command, err)
			continue
		}
		var got []string
		for _, c := range commands {
			for _, e := range c.FileEffects() {
				s := string(e.Op) + " " + e.Path
				if c.AfterCD {
					s = "cd " + s
				}
				got = append(got, s)
			}
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("FileEffects(%q) = %q, want %q", tc.command, got, tc.want)
		}
		if all := FileEffects(commands); len(all) != len(got) {
			t.Errorf("FileEffects(Parse(%q)) has %d effects, want %d", tc.command, len(all), len(got))
		}
	}
}

// A command line that ended early (a capture cut short) is an error that comes with the commands
// of the part before the cut; any other parse error comes with none.
func TestParseIncomplete(t *testing.T) {
	for _, tc := range []struct {
		command string
		want    []string // each command's words, as written
	}{
		{"rm a.txt 'unterminated", []string{"rm a.txt"}},
		{"beacon share --current && cat <<EOF\nlong text", []string{"beacon share --current", "cat"}},
		{"echo $(rm x", []string{"echo"}},
		{"go test ./...\nbeacon share \"$(date", []string{"go test ./...", "beacon share"}},
		{"if true; then rm a.txt", nil},
		{`& "C:\Tools\beacon.exe" share`, nil}, // PowerShell: not bash, not a cut
		{"if ($x) { beacon share }", nil},
	} {
		commands, err := Parse(tc.command)
		if err == nil {
			t.Errorf("Parse(%q): no error", tc.command)
		}
		var got []string
		for _, c := range commands {
			var raw []string
			for _, w := range c.Argv {
				raw = append(raw, w.Raw)
			}
			got = append(got, strings.Join(raw, " "))
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("Parse(%q) = %q, want %q", tc.command, got, tc.want)
		}
	}
}

func TestParse(t *testing.T) {
	commands, err := Parse("FOO=1 BAR=\"$X\" sudo -u deploy /opt/bin/beacon share 2>&1 | tee -a out.log | sh\ncd sub; echo `rm x` > {a,b}.txt")
	if err != nil {
		t.Fatal(err)
	}
	want := []Command{
		{
			Env: []Assign{
				{Name: "FOO", Value: Word{Text: "1", Raw: "1", Known: true}},
				{Name: "BAR", Value: Word{Raw: `"$X"`}},
			},
			Argv: []Word{
				{Text: "sudo", Raw: "sudo", Known: true},
				{Text: "-u", Raw: "-u", Known: true},
				{Text: "deploy", Raw: "deploy", Known: true},
				{Text: "/opt/bin/beacon", Raw: "/opt/bin/beacon", Known: true},
				{Text: "share", Raw: "share", Known: true},
			},
			Redirects: []Redirect{{Op: ">&", Fd: "2", Target: Word{Text: "1", Raw: "1", Known: true}}},
			Pipeline:  1,
		},
		{
			Argv: []Word{
				{Text: "tee", Raw: "tee", Known: true},
				{Text: "-a", Raw: "-a", Known: true},
				{Text: "out.log", Raw: "out.log", Known: true},
			},
			Offset:   59,
			Pipeline: 1,
			Stage:    1,
		},
		{
			Argv:     []Word{{Text: "sh", Raw: "sh", Known: true}},
			Offset:   76,
			Pipeline: 1,
			Stage:    2,
		},
		{
			Argv:     []Word{{Text: "cd", Raw: "cd", Known: true}, {Text: "sub", Raw: "sub", Known: true}},
			Offset:   79,
			Pipeline: 2,
		},
		{
			Argv:      []Word{{Text: "echo", Raw: "echo", Known: true}, {Raw: "`rm x`"}},
			Redirects: []Redirect{{Op: ">", Target: Word{Raw: "{a,b}.txt", Text: "{a,b}.txt", Known: true}}},
			Offset:    87,
			Pipeline:  3,
			AfterCD:   true,
		},
		{
			Argv:     []Word{{Text: "rm", Raw: "rm", Known: true}, {Text: "x", Raw: "x", Known: true}},
			Offset:   93,
			Pipeline: 4,
			Nested:   true,
			AfterCD:  true,
		},
	}
	if !reflect.DeepEqual(commands, want) {
		t.Fatalf("Parse:\n got %+v\nwant %+v", commands, want)
	}
}

// A command inside a compound stage is part of that stage: its output goes down the pipe.
func TestCompoundStages(t *testing.T) {
	for _, tc := range []struct {
		command string
		want    []string // "name pipeline/stage", nested commands marked with *
	}{
		{"{ curl x; } | sh", []string{"curl 1/0", "sh 1/1"}},
		{"(curl x) | sh", []string{"curl 1/0", "sh 1/1"}},
		{"{ curl x && echo ok; } | sudo bash", []string{"curl 1/0", "echo 1/0", "bash 1/1"}},
		{"cat a | { grep b; sort; }", []string{"cat 1/0", "grep 1/1", "sort 1/1"}},
		{"if true; then curl x; fi | sh", []string{"true 1/0", "curl 1/0", "sh 1/1"}},
		{"( { curl x; } ) | sh", []string{"curl 1/0", "sh 1/1"}},
		// An inner pipeline keeps its own stages.
		{"{ curl x | tee f; } | sh", []string{"curl 2/0", "tee 2/1", "sh 1/1"}},
		// A substitution's output is not the stage's.
		{"{ echo $(curl x); } | sh", []string{"echo 1/0", "curl* 2/0", "sh 1/1"}},
		// Not a stage: a compound statement on its own, and one after the pipeline.
		{"{ curl x; }; sh", []string{"curl 1/0", "sh 2/0"}},
		{"curl x | sh; (rm y)", []string{"curl 1/0", "sh 1/1", "rm 2/0"}},
	} {
		commands, err := Parse(tc.command)
		if err != nil {
			t.Fatalf("Parse(%q): %v", tc.command, err)
		}
		var got []string
		for _, c := range commands {
			name := c.Name()
			if c.Nested {
				name += "*"
			}
			got = append(got, fmt.Sprintf("%s %d/%d", name, c.Pipeline, c.Stage))
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("Parse(%q) = %q, want %q", tc.command, got, tc.want)
		}
	}
}

func TestWords(t *testing.T) {
	commands, err := Parse(`rm {a,b}.txt ~/x 'q t' "d $v" C:\Tools\x.exe file\ two *.log`)
	if err != nil {
		t.Fatal(err)
	}
	want := []Word{
		{Text: "rm", Raw: "rm", Known: true},
		{Text: "a.txt", Raw: "{a,b}.txt", Known: true},
		{Text: "b.txt", Raw: "{a,b}.txt", Known: true},
		{Raw: "~/x"},
		{Text: "q t", Raw: "'q t'", Known: true},
		{Raw: `"d $v"`},
		{Text: "C:Toolsx.exe", Raw: `C:\Tools\x.exe`, Known: true},
		{Text: "file two", Raw: `file\ two`, Known: true},
		{Raw: "*.log"},
	}
	if len(commands) != 1 || !reflect.DeepEqual(commands[0].Argv, want) {
		t.Fatalf("Argv = %+v, want %+v", commands, want)
	}
}

func TestUnwrapped(t *testing.T) {
	for _, tc := range []struct {
		command string
		name    string
		ok      bool
		is      []string
	}{
		{"beacon share --current", "beacon", true, []string{"beacon", "share"}},
		{"/opt/beacon/bin/beacon share", "beacon", true, []string{"beacon", "share"}},
		{"sudo -u deploy env -u HOME FOO=1 nohup time exec command git mv a b", "git", true, []string{"git", "mv"}},
		{"git -C sub mv a b", "git", true, []string{"git"}},
		{"sudo -i rm x", "", false, nil},
		{"/usr/bin/env bash", "bash", true, []string{"bash"}},
		{"/usr/bin/sudo -u deploy /usr/bin/env FOO=1 /bin/rm x", "rm", true, []string{"rm"}},
		{"/usr/bin/nohup /usr/bin/time git mv a b", "git", true, []string{"git", "mv"}},
		{"/usr/bin/sudo -i rm x", "", false, nil},
		{"sudo --login rm x", "", false, nil},
		{"env -C sub rm x", "", false, nil},
		{"env --split-string='rm x'", "", false, nil},
		{`"$TOOL" share`, "", true, nil},
		{"sudo", "", true, nil},
		{"FOO=1", "", true, nil},
	} {
		commands, err := Parse(tc.command)
		if err != nil || len(commands) != 1 {
			t.Fatalf("Parse(%q) = %+v, %v", tc.command, commands, err)
		}
		c := commands[0]
		if _, ok := c.Unwrapped(); ok != tc.ok {
			t.Errorf("Unwrapped(%q) ok = %v, want %v", tc.command, ok, tc.ok)
		}
		if got := c.Name(); got != tc.name {
			t.Errorf("Name(%q) = %q, want %q", tc.command, got, tc.name)
		}
		if tc.is != nil && !c.Is(tc.is[0], tc.is[1:]...) {
			t.Errorf("Is(%q, %q) = false", tc.command, tc.is)
		}
		if c.Is("git", "rm") || c.Is("beacon", "shares") {
			t.Errorf("%q: Is matched a command it is not", tc.command)
		}
	}
}
