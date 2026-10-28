package gitlink

import (
	"reflect"
	"testing"
)

func TestShellWrittenPaths(t *testing.T) {
	for _, tc := range []struct {
		command string
		want    []string
	}{
		{"rm -f a.txt b.txt", []string{"a.txt", "b.txt"}},
		{"rm -rf -- -weird", []string{"-weird"}},
		{"touch new.go && go test ./...", []string{"new.go"}},
		{"mv old.go new.go", []string{"old.go", "new.go"}},
		{"git mv src/a.go src/b.go", []string{"src/a.go", "src/b.go"}},
		{"git rm --cached secret.env", []string{"secret.env"}},
		{"cp -r template out/dir", []string{"out/dir"}},
		{"cp -t out a.txt src/b.txt", []string{"out/a.txt", "out/b.txt"}},
		{"cp --target-directory=out a.txt", []string{"out/a.txt"}},
		{"install -Dt bin tool", []string{"bin/tool"}},
		{"ln -s -t links ../real.txt", []string{"links/real.txt"}},
		{"install -m 0755 -t /usr/local/bin build/tool", []string{"/usr/local/bin/tool"}},
		{"install -o root -g wheel -t /etc/app conf.yaml", []string{"/etc/app/conf.yaml"}},
		{"install -m 0644 conf.yaml /etc/app/conf.yaml", []string{"/etc/app/conf.yaml"}},
		{`cp -t "$DEST" a.txt`, nil},
		{`cp -t out "$SRC" a.txt`, []string{"out/a.txt"}},
		{"mv -t archive a.txt b.txt", []string{"a.txt", "b.txt", "archive/a.txt", "archive/b.txt"}},
		{"truncate -s 0 app.log", []string{"app.log"}},
		{"touch -d '2 hours ago' stamp", []string{"stamp"}},
		{"echo hi > out.txt", []string{"out.txt"}},
		{"echo hi >out.txt 2>&1", []string{"out.txt"}},
		{"printf x >> log.txt; cat log.txt", []string{"log.txt"}},
		{"cmd >| forced.txt", []string{"forced.txt"}},
		{"go build &> build.log", []string{"build.log"}},
		{"make 2>/dev/null", nil},
		{"cat a.txt | tee copy.txt", []string{"copy.txt"}},
		{"sed -i 's/a/b/' x.go y.go", []string{"x.go", "y.go"}},
		{"sed -i.bak -e 's/a/b/' x.go", []string{"x.go"}},
		{"sed 's/a/b/' x.go", nil}, // not in place: prints, writes nothing
		{"perl -pi -e 's/a/b/' z.pl", []string{"z.pl"}},
		{"perl -Mwarnings -ne 'print if /x/' z.pl", nil}, // -M's module name is not -i
		{"perl -Ilib -ne 'print' z.pl", nil},
		{"perl -MList::Util=sum -lane 'print sum @F' data.txt", nil},
		{"perl -Mstrict -pi -e 's/a/b/' z.pl", []string{"z.pl"}},
		{"perl -0777 -pi -e 's/a/b/' z.pl", []string{"z.pl"}},
		{"perl -lpi -e 's/a/b/' z.pl", []string{"z.pl"}},
		{"sed -n -es/i/j/p x.go y.go", nil}, // -e's script is not -i
		{"perl -pi -e's/a/b/' z.pl", []string{"z.pl"}},
		{"sed -i -e's/a/b/' x.go", []string{"x.go"}},
		{"sed -i --expression=s/a/b/ x.go", []string{"x.go"}},
		{"sed -i -e 's/a/b/' -e 's/c/d/' x.go", []string{"x.go"}},
		{"sed -i -f fix.sed x.go", []string{"x.go"}},
		{"sed -ie 's/a/b/' x.go", []string{"x.go"}}, // GNU: the suffix is "e"
		{"sed -E -i 's/a/b/' x.go", []string{"x.go"}},
		{"sed -i 's/x/y/' f", []string{"f"}},
		{"sed -i '' 's/x/y/' f", []string{"f"}}, // BSD sed: '' is the backup suffix
		{"sed -i '' 's/x/y/' f g", []string{"f", "g"}},
		{"sed -i '' -e 's/a/b/' a.go", []string{"a.go"}},
		{`sed -i "s/$old/$new/" f`, []string{"f"}}, // an unknown script is not a suffix
		{`sed -i "s/$a/$b/" x y`, []string{"x", "y"}},
		{"sudo -u deploy rm /srv/app/old.txt", []string{"/srv/app/old.txt"}},
		{"sudo -E tee /etc/app.conf < in.txt", []string{"/etc/app.conf"}},
		{"sudo -i rm old.txt", nil}, // a login shell starts in the target user's home
		{"env FOO=1 rm a.txt", []string{"a.txt"}},
		{"env -C sub rm a.txt", nil},
		{"rm a.txt # cleanup b.txt", []string{"a.txt"}},
		{"rm {a,b}.txt", []string{"a.txt", "b.txt"}},
		{"if true; then rm a.txt; fi", []string{"a.txt"}},
		{"for f in *.tmp; do rm \"$f\"; done", nil},
		{"echo $((1<<3)) > n.txt\nrm old.txt", []string{"n.txt", "old.txt"}},
		{"grep x <<< \"$v\"\nrm a.txt", []string{"a.txt"}},
		{"git commit -m \"$(cat <<'EOF'\nrm -rf dist\nEOF\n)\"", nil},
		{"rm a.txt 'unterminated", nil},           // a command that does not parse was captured incomplete
		{"rm a.txt && cat <<EOF\ncut short", nil}, // nor does one that ends inside a here-document
		{"FOO=1 sudo rm build/out", []string{"build/out"}},
		{`rm "file with spaces.txt"`, []string{"file with spaces.txt"}},
		{`rm file\ two.txt`, []string{"file two.txt"}},
		{"echo '> not a redirect' ; ls", nil},
		{"rm $TARGET", nil}, // unknown value
		{`"$TOOL" rm victim.txt`, nil},
		{`command "$TOOL" rm victim.txt`, nil},
		{`cp first.txt second.txt "$DEST"`, nil},
		{`cp "$SOURCE" output.txt`, []string{"output.txt"}},
		{`rm known.txt "$UNKNOWN"`, []string{"known.txt"}},
		{`"$TOOL" rm victim.txt > known.log`, []string{"known.log"}},
		{`ROOT="$DIR" cd sub && rm relative.txt`, nil},
		{`ROOT="$DIR" rm known.txt`, []string{"known.txt"}},
		{`"ROOT=$DIR" rm victim.txt`, nil},
		{`"$TOOL=x" rm victim.txt`, nil},
		{`"ROOT"=$DIR rm victim.txt`, nil},
		{`ROOT\=$DIR rm victim.txt`, nil},
		{`ROOT=$DIR rm known.txt`, []string{"known.txt"}},
		{"rm *.log", nil},         // glob
		{"rm \"$(pwd)/x\"", nil},  // expansion inside quotes
		{"(cd sub && rm x)", nil}, // x is sub/x, which this parser does not follow
		{"cd /tmp && rm /abs/path.txt && rm rel.txt", []string{"/abs/path.txt"}},
		{"rm before.txt; cd sub; rm after.txt", []string{"before.txt"}},
		{"command cd sub && rm after.txt", nil},
		{"ROOT=sub cd sub && touch after.txt", nil},
		{"builtin cd sub && rm after.txt", nil},
		{"command cd sub && rm /abs/path.txt", []string{"/abs/path.txt"}},
		{"touch before.txt; command pushd sub; rm after.txt", []string{"before.txt"}},
		{"command popd; rm after.txt", nil},
		{"ls -la; go test ./...", nil},
		{"tee -a log.txt < output.txt", []string{"log.txt"}},
		{"tee log.txt <output.txt", []string{"log.txt"}},
		{"tee copy.txt <<< hello", []string{"copy.txt"}},
		{"echo '<' > lt.txt", []string{"lt.txt"}},
		{"tee '<' out.txt", []string{"<", "out.txt"}},
		{"cp a.txt b.txt < /dev/null", []string{"b.txt"}},
		{"sudo tee /etc/app.conf > /dev/null <<'EOF'\nkey: value\nEOF", []string{"/etc/app.conf"}},
		{"cat <<EOF > gen.txt\nhello\nEOF", []string{"gen.txt"}},
		{"cat > deploy.sh <<'EOF'\nrm -rf dist\necho done > status.txt\nEOF\nchmod +x deploy.sh", []string{"deploy.sh"}},
		{"cat <<-END > a.txt\n\trm b.txt\n\tEND\nrm c.txt", []string{"a.txt", "c.txt"}},
		{"cat <<A <<\"B\" > out.txt\ntouch x.txt\nA\ntouch y.txt\nB\ntouch z.txt", []string{"out.txt", "z.txt"}},
		{"cat > a.md <<'EOF'\nDon't touch\nEOF\nrm b.txt", []string{"a.md", "b.txt"}},
		{"grep -c x <<< 'rm here.txt'", nil},
		{"tee x.txt <<<\"hello\"\nrm y.txt", []string{"x.txt", "y.txt"}},
	} {
		got := ShellWrittenPaths(tc.command)
		if len(got) == 0 && len(tc.want) == 0 {
			continue
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("ShellWrittenPaths(%q) = %q, want %q", tc.command, got, tc.want)
		}
	}
}

func TestPatchPaths(t *testing.T) {
	patch := `*** Begin Patch
*** Update File: cli/main.go
@@
-old
+new
*** Add File: docs/new.md
+hello
*** Delete File: stale.txt
*** Update File: a.go
*** Move to: b.go
*** End Patch`
	want := []string{"cli/main.go", "docs/new.md", "stale.txt", "a.go", "b.go"}
	if got := PatchPaths(patch); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestPatchPathsInArguments(t *testing.T) {
	patch := "*** Begin Patch\n*** Update File: x.go\n*** End Patch"
	for name, args := range map[string]interface{}{
		"object":      map[string]interface{}{"input": patch},
		"json text":   `{"input":"*** Begin Patch\n*** Update File: x.go\n*** End Patch"}`,
		"nested list": []interface{}{map[string]interface{}{"patch": patch}},
		"bare string": patch,
	} {
		if got := patchPathsIn(args); !reflect.DeepEqual(got, []string{"x.go"}) {
			t.Errorf("%s: got %q", name, got)
		}
	}
	if got := patchPathsIn(map[string]interface{}{"content": "no patch here"}); len(got) != 0 {
		t.Fatalf("got %q", got)
	}
}
