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
		{"FOO=1 sudo rm build/out", []string{"build/out"}},
		{`rm "file with spaces.txt"`, []string{"file with spaces.txt"}},
		{`rm file\ two.txt`, []string{"file two.txt"}},
		{"echo '> not a redirect' ; ls", nil},
		{"rm $TARGET", nil},       // unknown value
		{"rm *.log", nil},         // glob
		{"rm \"$(pwd)/x\"", nil},  // expansion inside quotes
		{"(cd sub && rm x)", nil}, // x is sub/x, which this parser does not follow
		{"cd /tmp && rm /abs/path.txt && rm rel.txt", []string{"/abs/path.txt"}},
		{"rm before.txt; cd sub; rm after.txt", []string{"before.txt"}},
		{"ls -la; go test ./...", nil},
		{"cat <<EOF > gen.txt\nhello\nEOF", []string{"gen.txt"}},
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
