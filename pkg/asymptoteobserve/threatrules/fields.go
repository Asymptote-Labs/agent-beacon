package threatrules

import (
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/shellcmd"
)

// Field describes one CEL-addressable event field: the dotted path a rule references
// (after the "e." prefix) and its CEL type.
type Field struct {
	Path string
	Type string
}

// EventFields returns the authoritative set of CEL-addressable event fields a rule can
// match on, using the JSON field names CEL exposes. Candidate leaves are discovered by
// reflecting the Beacon event, then filtered to those that actually compile against the
// CEL env — so fields cel-go does not expose (e.g. interface{} or map[string]interface{}
// payloads) are excluded by construction, and each field's Type is its real CEL type.
func EventFields() []Field {
	var candidates []Field
	walkFields(reflect.TypeOf(asymptoteobserve.Event{}), "", map[reflect.Type]bool{}, &candidates)

	env, err := Env()
	if err != nil {
		// Env construction is exercised by its own test; degrade to reflected types.
		sort.Slice(candidates, func(i, j int) bool { return candidates[i].Path < candidates[j].Path })
		return candidates
	}

	fields := make([]Field, 0, len(candidates))
	for _, c := range candidates {
		ast, iss := env.Compile("e." + c.Path)
		if iss != nil && iss.Err() != nil {
			continue // not addressable in CEL (e.g. dyn/interface payload)
		}
		fields = append(fields, Field{Path: c.Path, Type: ast.OutputType().String()})
	}
	sort.Slice(fields, func(i, j int) bool { return fields[i].Path < fields[j].Path })
	return fields
}

func walkFields(t reflect.Type, prefix string, seen map[reflect.Type]bool, out *[]Field) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return
	}
	if seen[t] {
		return // guard against any cyclic type graph
	}
	seen[t] = true
	defer delete(seen, t)

	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		name := celFieldName(f)
		if name == "" || name == "-" {
			continue
		}
		path := name
		if prefix != "" {
			path = prefix + "." + name
		}
		ft := f.Type
		for ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}
		if ft.Kind() == reflect.Struct && !isLeafStruct(ft) {
			walkFields(ft, path, seen, out)
			continue
		}
		*out = append(*out, Field{Path: path, Type: celTypeName(ft)})
	}
}

// isLeafStruct reports structs that CEL treats as scalars rather than objects to recurse
// into. None of the Beacon event sub-types qualify today, but this keeps the walker
// honest if a time.Time or similar is added.
func isLeafStruct(t reflect.Type) bool {
	return t.String() == "time.Time"
}

func celTypeName(t reflect.Type) string {
	switch t.Kind() {
	case reflect.String:
		return "string"
	case reflect.Bool:
		return "bool"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return "int"
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "uint"
	case reflect.Float32, reflect.Float64:
		return "double"
	case reflect.Slice, reflect.Array:
		return "list"
	case reflect.Map:
		return "map"
	case reflect.Interface:
		return "dyn"
	default:
		return "dyn"
	}
}

// RenderFieldsMarkdown renders the field reference as the committed FIELDS.md document.
// The CLI `threatrules fields` command and the drift test share this single renderer.
func RenderFieldsMarkdown() string {
	var b strings.Builder
	b.WriteString("# Threat Rules — Event field reference\n\n")
	b.WriteString("Generated from the Beacon `Event` schema. These are the fields a rule's CEL\n")
	b.WriteString("expression can reference, bound to the variable `e` (e.g. `e.event.action`).\n")
	b.WriteString("Field selection is null-safe: an absent sub-object yields the zero value.\n\n")
	b.WriteString("Regenerate with `beacon rules fields --markdown > spec/threat-rules/FIELDS.md`.\n\n")
	b.WriteString("| Field | Type |\n|---|---|\n")
	for _, f := range EventFields() {
		fmt.Fprintf(&b, "| `e.%s` | %s |\n", f.Path, f.Type)
	}
	b.WriteString("\n## Derived fields\n\n")
	b.WriteString("These are computed by the engine from the event just before evaluation. They are\n")
	b.WriteString("never written to or read from the log, so a fixture sets the source field, not these.\n")
	b.WriteString("A field marked with a spec version needs a rule that declares that version or later.\n\n")
	for _, path := range DerivedFields() {
		fmt.Fprintf(&b, "- `e.%s`%s: %s\n", path, specNote(path), derivedFieldDocs[path])
	}
	fmt.Fprintf(&b, "\n### Fields of a parsed command\n\nEach entry of `e.%s`.\n\n", ParsedCommandsPath)
	b.WriteString("| Field | Type | Spec | Meaning |\n|---|---|---|---|\n")
	since := specVersionOf(ParsedCommandsPath)
	entry := reflect.TypeOf(asymptoteobserve.ParsedCommand{})
	for i := range entry.NumField() {
		f := entry.Field(i)
		name := celFieldName(f)
		version := specVersionOf(ParsedCommandsPath + "[]." + name)
		if version == "" {
			version = since
		}
		fmt.Fprintf(&b, "| `%s` | %s | %s | %s |\n", name, celTypeOf(f.Type), version,
			strings.ReplaceAll(parsedCommandFieldDocs[name], "|", `\|`))
	}
	return b.String()
}

// specVersionOf is the version a spec tag gives the field at path (v1.3), or "" for a field that
// carries none.
func specVersionOf(path string) string {
	for _, f := range specFields() {
		if f.path == path {
			return f.version
		}
	}
	return ""
}

func specNote(path string) string {
	if version := specVersionOf(path); version != "" {
		return " (spec " + version + ")"
	}
	return ""
}

// celTypeOf names a parsed command field's CEL type with what it holds: list(string),
// list({path, op}), map(string, string).
func celTypeOf(t reflect.Type) string {
	switch t.Kind() {
	case reflect.Slice:
		return "list(" + celTypeOf(t.Elem()) + ")"
	case reflect.Map:
		return "map(" + celTypeOf(t.Key()) + ", " + celTypeOf(t.Elem()) + ")"
	case reflect.Struct:
		names := make([]string, 0, t.NumField())
		for i := range t.NumField() {
			names = append(names, celFieldName(t.Field(i)))
		}
		return "{" + strings.Join(names, ", ") + "}"
	}
	return celTypeName(t)
}

// derivedFieldDocs is the field reference's entry for each of DerivedFields, wrapped as it is
// rendered. TestEveryDerivedFieldIsDocumented requires one per field. The version a field needs
// comes from its spec tag (specNote), not from this text.
var derivedFieldDocs = map[string]string{
	ParsedCommandsPath: "`command.command` read as a bash command line:\n" +
		"  one entry per command it runs, with the fields in the table below. Other here-document\n" +
		"  bodies and strings are not commands. A declaration is one (spec v1.3):\n" +
		"  `export HISTFILE=/dev/null` has the name `export` and the word `HISTFILE=/dev/null`.\n" +
		fmt.Sprintf("  The first %d bytes are read; a cut capture yields the commands before the cut, and\n", ParsedCommandsLimit) +
		"  one that does not parse yields an empty list.",
	ParsedIncompletePath: "true when `command.command` was not read to its end:\n" +
		"  it does not parse, it ends inside a quote, a here-document, a substitution or a compound\n" +
		"  command (as a cut capture does), it is longer than the engine reads, or it names more\n" +
		fmt.Sprintf("  than the engine reads (more than %d commands, or a brace expansion of thousands of\n", shellcmd.MaxCommands) +
		"  words; SPEC gives the bounds).\n" +
		"  `e.command.parsed_commands` then lists the commands before that point only, so a rule\n" +
		"  that concludes something from a command's absence asks this first.",
	ToolResultTextPath: "`gen_ai.tool.call.result` as text, only on read-type tool\n" +
		"  results (`file.read`, `mcp.tool_invoked`, MCP-named tools, web fetch/search tools, a\n" +
		"  `tool.path` that is an http(s) URL) whose content was retained. String leaves of an\n" +
		"  object or list result are joined one per line;\n" +
		"  content blocks in a list contribute their text without metadata or encoded bytes.\n" +
		fmt.Sprintf("  Capped at %d bytes and secret-redacted like other retained text; empty otherwise.", ToolResultTextLimit),
	SystemInstructionsTextPath: "`gen_ai.system_instructions` as text: the `content` of each\n" +
		"  `text` part, one per line (a bare string as is), when content was retained. These are\n" +
		"  runtime-provided instructions such as a skill index, never operator prompt text.\n" +
		fmt.Sprintf("  Each part is capped at %d bytes and secret-redacted like other retained text; empty otherwise.", SystemInstructionsPartLimit),
}

// parsedCommandFieldDocs is the field reference's row for each field of a parsed command, by its
// CEL name. TestEveryParsedCommandFieldIsDocumented requires one per field.
var parsedCommandFieldDocs = map[string]string{
	"name": "The base name of the command after its wrappers (`sudo`, `env`, `exec`, `timeout`, `uv run` " +
		"and the like); \"\" when it is unknown.",
	"argv": "Every word, wrappers included, as the shell passes it on; \"\" for a word whose value depends " +
		"on an expansion, a glob or `~`.",
	"argv_raw": "Every word, wrappers included, as written.",
	"args": "The words after the name, one per line, so `c.args.matches(\"(?m)^auth\\ntoken$\")` asks for two " +
		"whole words that follow each other.",
	"env":          "The `NAME=value` assignments before the command; \"\" for an unknown value.",
	"file_effects": "The files it evidently writes, and how (`op`), where the path's value is known.",
	"file_writes": "The files it writes, and how, each path as written, so a write to `~/.bashrc` or " +
		"`$HOME/.netrc`, which `file_effects` leaves out, is listed. A download's output file and the " +
		"destination of `scp` and `rsync` are writes, and so is the directory an archive or a clone lands " +
		"in, written with a trailing slash.",
	"file_reads": "The files whose content it takes in, each path as written without its quotes " +
		"(`~/.aws/credentials`, `$HOME/.netrc`). `via` is `direct` for a file it opens, `stdin` for one " +
		"that reaches its standard input by a redirection or a pipe, `argument` for one a substitution or " +
		"a variable writes into its words.",
	"script_sends": "The paths an inline python, node, bun, deno, ruby or php script sends over the " +
		"network: opened inside a call that sends, or held by a variable used there. Headers, credentials " +
		"and URL parameters are not data. Each is also one of `file_reads`.",
	"socket_redirects": "Its redirections to or from bash's `/dev/tcp/host/port` and " +
		"`/dev/udp/host/port`, also through a descriptor an earlier `exec` opened; `op` is `write`, " +
		"`read` or `readwrite`.",
	"pipeline": "Identifies the command's pipeline: the stages of one pipeline share it.",
	"stage":    "The command's 0-based position in its pipeline.",
	"earlier_stages": "The indexes, in this list, of the commands at earlier stages of its pipeline. A " +
		"rule about two stages of a pipeline asks these and `later_stages`, not every command of the line.",
	"later_stages": "The indexes, in this list, of the commands at later stages of its pipeline: " +
		"`c.later_stages.exists(i, e.command.parsed_commands[i].name == \"sh\")`.",
	"nested": "Inside `$(...)`, `<(...)`, a shell's script, or what a carrier or an inline script runs.",
	"runs_read": "True when the command hands a command line to another program and that command line " +
		"was read to its end, so its commands follow it in the list: a shell's `-c` text, the " +
		"here-document it reads or the literal script a pipe sends it, the command of `xargs`, " +
		"`find -exec`, `ssh host`, `docker exec`, `kubectl exec --`, `su -c`, `eval`, `watch` and `trap`, " +
		"and the command behind `sudo -i` or `env -C`. Never for an interpreter, though the commands its " +
		"inline script runs through `os.system(\"..\")` and the like are listed.",
	"span": "The part of the line that can decide what the command runs, for a rule that must match text " +
		"because the command is a program it cannot see into: its own words, redirections and " +
		"here-documents, with the earlier stages of its pipeline and the commands before it that wrote a " +
		"file, the line from its start when a variable the line may have set reaches it, and the loop or " +
		"the line when it is in a loop or a function.",
}
