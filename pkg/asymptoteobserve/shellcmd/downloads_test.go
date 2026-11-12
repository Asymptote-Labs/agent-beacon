package shellcmd

import (
	"reflect"
	"testing"
)

// A download's output file, the destination of a copy from another host and dd's output are
// files a command writes. The directory an archive is extracted or a repository is cloned into
// is a write and not an effect: which files land there is not on the command line.
func TestDownloadAndExtractionTargets(t *testing.T) {
	for _, tc := range []struct {
		command string
		effects []string
		writes  []string // nil when the same as effects
	}{
		// curl: -o, --output, attached and in a cluster; -O takes the name from the URL.
		{"curl -fsSL https://x.example/k.pub -o ~/.ssh/authorized_keys", nil, []string{"write ~/.ssh/authorized_keys"}},
		{"curl -fsSLo /tmp/i.sh https://x.example/install.sh", []string{"write /tmp/i.sh"}, nil},
		{"curl --output=hooks.json https://x.example/h", []string{"write hooks.json"}, nil},
		{"curl -sS -o a.json https://x.example/a -o b.json https://x.example/b", []string{"write a.json", "write b.json"}, nil},
		{"curl -sSO https://x.example/team/AGENTS.md", []string{"write AGENTS.md"}, nil},
		{"curl --remote-name --output-dir ~/.claude https://x.example/settings.json?v=2", nil, []string{"write ~/.claude/settings.json"}},
		// The file's name is known though another word of the command is not.
		{`curl -H "Authorization: Bearer $T" -x http://proxy.example:8080 -O https://x.example/dl/tool.tgz`, []string{"write tool.tgz"}, nil},
		// Not a file: standard output, a device, an upload, a request without an output option.
		{"curl -o - https://x.example/a", nil, nil},
		{"curl -so /dev/null -w '%{http_code}' https://x.example/a", nil, nil},
		{"curl -T notes.txt https://x.example/upload", nil, nil},
		{"curl -fsSL https://x.example/install.sh", nil, nil},
		{"curl -d @body.json -H 'Referer: https://x.example/page.html' https://x.example/api", nil, nil},
		// wget: -O, or the name the URL ends in, under -P.
		{"wget -qO ~/.cursor/hooks.json https://x.example/hooks.json", nil, []string{"write ~/.cursor/hooks.json"}},
		{"wget --output-document=.mcp.json https://x.example/m.json", []string{"write .mcp.json"}, nil},
		{"wget -q https://x.example/dist/install.sh", []string{"write install.sh"}, nil},
		{"wget -nv -P /opt/tools https://x.example/dist/tool.tgz", []string{"write /opt/tools/tool.tgz"}, nil},
		{"wget -qO- https://x.example/install.sh", nil, nil},
		{"wget -r -np https://x.example/docs/", nil, nil},
		{"wget --spider https://x.example/dist/tool.tgz", nil, nil},
		// dd.
		{"dd if=/dev/zero of=/srv/fill bs=1M count=10", []string{"write /srv/fill"}, nil},
		{"dd if=img.iso of=/dev/sda", nil, nil},
		// scp and rsync write here only when the last operand is on this host.
		{"scp build@host.example:/etc/app.conf ./app.conf", []string{"write ./app.conf"}, nil},
		{"scp -i ~/.ssh/k build@host.example:keys/a.pub build@host.example:keys/b.pub ~/.ssh/", nil, []string{"write ~/.ssh/a.pub", "write ~/.ssh/b.pub"}},
		{"scp notes.txt build@host.example:/srv/", nil, nil},
		{"rsync -a build@host.example:/srv/hooks/pre-commit .git/hooks/", []string{"write .git/hooks/pre-commit"}, nil},
		{`rsync -a /tmp/skills/ "$HOME/.claude/skills/"`, nil, []string{"write $HOME/.claude/skills/"}},
		{"rsync -a src/ build@host.example:/srv/app/", nil, nil},
		// An archive or a clone puts files into a directory.
		{"tar -xzf payload.tgz -C ~/.claude/skills", nil, []string{"write ~/.claude/skills/"}},
		{"tar xzf payload.tgz", nil, []string{"write ./"}},
		{"tar --extract --file=payload.tar --directory=/opt/app", nil, []string{"write /opt/app/"}},
		{"tar czf out.tgz src", nil, nil},
		{"tar tzf payload.tgz", nil, nil},
		{"unzip -o plugin.zip -d ~/.gemini/extensions/helper", nil, []string{"write ~/.gemini/extensions/helper/"}},
		{"unzip -l plugin.zip", nil, nil},
		{"git clone --depth 1 https://x.example/someone/ext ~/.gemini/extensions/ext", nil, []string{"write ~/.gemini/extensions/ext/"}},
		{"git clone -b main git@x.example:team/skills.git", nil, []string{"write skills/"}},
		{"gh repo clone someone/helper .claude/plugins/helper -- --depth 1", nil, []string{"write .claude/plugins/helper/"}},
		{"git fetch origin main", nil, nil},
	} {
		commands, err := Parse(tc.command)
		if err != nil || len(commands) == 0 {
			t.Errorf("Parse(%q): %v", tc.command, err)
			continue
		}
		var effects, writes []string
		for _, e := range commands[0].FileEffects() {
			effects = append(effects, string(e.Op)+" "+e.Path)
		}
		for _, e := range commands[0].FileWrites() {
			writes = append(writes, string(e.Op)+" "+e.Path)
		}
		want := tc.writes
		if want == nil {
			want = tc.effects
		}
		if !reflect.DeepEqual(effects, tc.effects) {
			t.Errorf("FileEffects(%q) = %q, want %q", tc.command, effects, tc.effects)
		}
		if !reflect.DeepEqual(writes, want) {
			t.Errorf("FileWrites(%q) = %q, want %q", tc.command, writes, want)
		}
	}
}
