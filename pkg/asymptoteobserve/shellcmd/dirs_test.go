package shellcmd

import (
	"reflect"
	"testing"
)

func TestDirIsWhereTheShellOfTheCommandIs(t *testing.T) {
	for _, tc := range []struct {
		line string
		want []string // name=Dir of each command of Read
	}{
		{"echo x >> requirements.txt; cd /tmp/x", []string{"echo=", "cd="}},
		{"cd /tmp/x && echo x > package.json", []string{"cd=", "echo=/tmp/x/"}},
		{"cd /tmp", []string{"cd="}},
		{"cd /tmp && echo x > f", []string{"cd=", "echo=/tmp/"}},
		{"cd / && echo x > f", []string{"cd=", "echo=/"}},
		{"(cd /tmp/x && make) ; echo x > package.json", []string{"cd=", "make=/tmp/x/", "echo="}},
		{"cd /tmp/x; (cd /srv && make); touch f", []string{"cd=", "cd=/tmp/x/", "make=/srv/", "touch=/tmp/x/"}},
		{"echo $(cd /tmp/x; pwd) > f; touch g", []string{"echo=", "cd=", "pwd=/tmp/x/", "touch="}},
		{"cd /tmp/x | cat; touch f", []string{"cd=", "cat=", "touch="}},
		{"cd /tmp/x & touch f", []string{"cd=", "touch="}},
		{"cd /tmp/x && cd sub/../y && touch f", []string{"cd=", "cd=/tmp/x/", "touch=/tmp/x/y/"}},
		{"cd sub && touch f", []string{"cd=", "touch="}},
		{"cd /tmp/x && cd && touch f", []string{"cd=", "cd=/tmp/x/", "touch="}},
		{"cd /tmp/x && cd - && touch f", []string{"cd=", "cd=/tmp/x/", "touch="}},
		{"cd \"$D\" && touch f", []string{"cd=", "touch="}},
		{"pushd /tmp/x && touch f && popd && touch g", []string{"pushd=", "touch=/tmp/x/", "popd=/tmp/x/", "touch="}},
		{"cd -P -- /tmp/x && touch f", []string{"cd=", "touch=/tmp/x/"}},
		{"cd /tmp/x; for d in a b; do cd /srv; done; touch f", []string{"cd=", "cd=", "touch="}},
		{"touch f; for d in a b; do cd /tmp/x; touch g; done", []string{"touch=", "cd=", "touch="}},
		{"cd /tmp/x && bash -c 'touch f; cd /srv; touch g'", []string{"cd=", "bash=/tmp/x/", "touch=/tmp/x/", "cd=/tmp/x/", "touch=/srv/"}},
		{"cd /tmp/x && ssh host 'touch f'", []string{"cd=", "ssh=/tmp/x/", "touch="}},
	} {
		line := Read(tc.line)
		var got []string
		for _, c := range line.Commands {
			got = append(got, c.Name()+"="+c.Dir)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("Read(%q) dirs = %q, want %q", tc.line, got, tc.want)
		}
	}
}
