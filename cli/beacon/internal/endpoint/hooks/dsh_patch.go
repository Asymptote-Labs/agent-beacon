package hooks

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Editing `$DSH_HOME/cordis.patch.yml`, which is the user's file and not Beacon's.
//
// Every other runtime Beacon installs into either gives it a file of its own or gives it a JSON
// document, where "read, change one key, write" loses nothing. This is neither. The patch layer is
// YAML a person writes by hand: it carries their comments, their quoting, and `!!js` expressions
// the loader evaluates at boot. Decoding it into Go structs and re-encoding would silently discard
// all three -- the comments explaining why a row exists, and the tag that makes an expression an
// expression rather than the string "Number(process.env.DSH_PORT ?? 3081)".
//
// So the document is edited as a document: parsed into a yaml.Node tree, one element appended to or
// removed from the top-level sequence, re-encoded from the same tree. yaml.v3 carries comments,
// scalar style and custom tags through that round trip, which is what makes touching a live user
// file defensible rather than merely convenient. A test pins it, because it is a property of the
// library rather than of this code and a library change would take a user's boot configuration with
// it.
//
// What Beacon appends is one self-contained element:
//
//	- insert:
//	    - id: beacon-endpoint-hooks
//	      name: '@deepseek-ai/dsh-hooks-claude-code'
//	      config:
//	        configPath: /home/you/.dsh/beacon-endpoint-hooks.json
//
// Self-contained rather than a row pushed into somebody else's existing `insert:` list, for two
// reasons. An uninstall can then remove exactly what an install added, by dropping one element,
// rather than reaching into a list whose other rows it must leave untouched and whose emptiness it
// would then have to reason about. And the loader applies patch elements in order, so an element of
// Beacon's own cannot change when another row's position changes.
//
// The path in `configPath` is absolute, and that is required rather than tidy: the bridge resolves
// a relative config path against the directory the process was launched from, which for an agent
// runtime is wherever the user happened to be standing.

// dshPatchDocument is a parsed patch file plus what the caller needs to write it back.
type dshPatchDocument struct {
	// node is the document node, or nil when the file does not exist.
	node *yaml.Node
	// existed records whether there was a file, which decides whether removing Beacon's last row
	// should delete the file or leave an empty one. It must delete: see writeDshPatch.
	existed bool
}

// readDshPatch parses the patch file, or reports that there is none.
//
// A file that exists but is empty or comments-only parses to a document with no content node.
// That is not an error here even though it IS an error to dsh -- the runtime refuses to boot on
// one -- because refusing to install over a file that already breaks the runtime would leave the
// operator with the broken file and no telemetry. Appending Beacon's element fixes it as a side
// effect, which is the better outcome and the same call the hooks-file reader makes.
func readDshPatch(path string) (dshPatchDocument, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return dshPatchDocument{}, nil
		}
		return dshPatchDocument{}, fmt.Errorf("read %s: %w", path, err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		// Unlike the hooks file, a patch file Beacon cannot parse is NOT overwritten. The two
		// differ because the consequences differ: an unparsable hooks file registers nothing and
		// costs nothing to replace, while this file is where the user's whole plugin tree is
		// tweaked, and dsh's parser is not Go's -- a document this fails on may be one the runtime
		// loads perfectly well. Refusing with the parse error names the file and leaves it intact.
		return dshPatchDocument{}, fmt.Errorf(
			"%s is not valid YAML (%w); Beacon will not rewrite a patch file it cannot read. "+
				"Fix or move the file and run the install again", path, err)
	}
	if len(doc.Content) == 0 {
		return dshPatchDocument{existed: true}, nil
	}
	return dshPatchDocument{node: &doc, existed: true}, nil
}

// dshPatchSequence returns the top-level sequence of patch elements, or an error when the document
// is some other shape.
//
// A patch file is a list. A mapping or a scalar at the top level is a file whose author meant
// something else, or a different file entirely that happens to be at this path, and appending a
// list element to it would produce a document that is neither.
func dshPatchSequence(doc dshPatchDocument, path string) (*yaml.Node, error) {
	if doc.node == nil {
		return nil, nil
	}
	root := doc.node.Content[0]
	if root.Kind != yaml.SequenceNode {
		return nil, fmt.Errorf(
			"%s is not a list of patch entries; Beacon will not rewrite it. A dsh patch file is a "+
				"YAML sequence -- see the runtime's own overlay examples", path)
	}
	return root, nil
}

// dshPatchEntryNode builds Beacon's element.
func dshPatchEntryNode(hooksPath string) *yaml.Node {
	row := &yaml.Node{Kind: yaml.MappingNode}
	appendMapEntry(row, "id", scalar(dshPatchEntryID, 0))
	// Single-quoted, because the package name begins with `@`. Unquoted it is still a valid YAML
	// scalar, but the leading sigil is the kind of thing a reader edits "back" to something wrong,
	// and every example in the runtime's own documentation quotes it.
	appendMapEntry(row, "name", scalar(dshBridgePackage, yaml.SingleQuotedStyle))

	config := &yaml.Node{Kind: yaml.MappingNode}
	appendMapEntry(config, "configPath", scalar(hooksPath, yaml.SingleQuotedStyle))
	appendMapEntry(row, "config", config)

	insertList := &yaml.Node{Kind: yaml.SequenceNode, Content: []*yaml.Node{row}}
	element := &yaml.Node{Kind: yaml.MappingNode}
	appendMapEntry(element, "insert", insertList)
	element.HeadComment = "Beacon endpoint telemetry. Mounts DeepSeek's Claude Code hook bridge at\n" +
		"a hooks file Beacon owns. Local only: the hooks make no network calls.\n" +
		"Managed by `beacon endpoint hooks`; remove with `beacon endpoint hooks uninstall`."
	return element
}

func scalar(value string, style yaml.Style) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value, Style: style}
}

func appendMapEntry(mapping *yaml.Node, key string, value *yaml.Node) {
	mapping.Content = append(mapping.Content, scalar(key, 0), value)
}

// isDshBeaconElement reports whether one top-level patch element is Beacon's.
//
// By the row id inside its `insert` list, which is what the loader itself keys entries on. An
// element carrying Beacon's row alongside rows somebody else added is deliberately NOT claimed:
// removing it would take their rows with it, and Beacon only ever writes an element of its own.
func isDshBeaconElement(element *yaml.Node) bool {
	if element.Kind != yaml.MappingNode {
		return false
	}
	insertList := mappingValue(element, "insert")
	if insertList == nil || insertList.Kind != yaml.SequenceNode || len(insertList.Content) != 1 {
		return false
	}
	id := mappingValue(insertList.Content[0], "id")
	return id != nil && id.Kind == yaml.ScalarNode && id.Value == dshPatchEntryID
}

// mappingValue reads one key out of a mapping node, or nil.
func mappingValue(mapping *yaml.Node, key string) *yaml.Node {
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i+1]
		}
	}
	return nil
}

// dshPatchHasEntry reports whether the bridge is mounted at a Beacon hooks file.
func dshPatchHasEntry(path string) bool {
	doc, err := readDshPatch(path)
	if err != nil {
		return false
	}
	root, err := dshPatchSequence(doc, path)
	if err != nil || root == nil {
		return false
	}
	for _, element := range root.Content {
		if isDshBeaconElement(element) {
			return true
		}
	}
	return false
}

// addDshPatchEntry mounts the bridge, replacing Beacon's earlier row if there is one.
//
// Replacing rather than skipping, because the row carries the hooks file's absolute path: a
// reinstall after DSH_HOME moved, or after a scope changed, must repoint the mount. Skipping would
// leave it aimed at a file that is no longer there, and the bridge's answer to that is a warning in
// a log nobody is reading.
//
// Appended at the end rather than inserted at the front. Later elements win in this layer, and the
// end is also where a person adding a row by hand would put one -- so the file stays the shape its
// author expects.
func addDshPatchEntry(path, hooksPath string) error {
	doc, err := readDshPatch(path)
	if err != nil {
		return err
	}
	root, err := dshPatchSequence(doc, path)
	if err != nil {
		return err
	}
	if root == nil {
		root = &yaml.Node{Kind: yaml.SequenceNode}
		doc.node = &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{root}}
	}
	entry := dshPatchEntryNode(hooksPath)
	replaced := false
	for i, element := range root.Content {
		if isDshBeaconElement(element) {
			// The author's own comment above Beacon's row, if they wrote one, is theirs to keep.
			entry.HeadComment = element.HeadComment
			root.Content[i] = entry
			replaced = true
			break
		}
	}
	if !replaced {
		root.Content = append(root.Content, entry)
	}
	return writeDshPatch(path, doc)
}

// removeDshPatchEntry unmounts the bridge, and reports whether anything was there.
func removeDshPatchEntry(path string) (bool, error) {
	doc, err := readDshPatch(path)
	if err != nil {
		return false, err
	}
	root, err := dshPatchSequence(doc, path)
	if err != nil || root == nil {
		return false, err
	}
	kept := make([]*yaml.Node, 0, len(root.Content))
	removed := false
	for _, element := range root.Content {
		if isDshBeaconElement(element) {
			removed = true
			continue
		}
		kept = append(kept, element)
	}
	if !removed {
		return false, nil
	}
	root.Content = kept
	return true, writeDshPatch(path, doc)
}

// writeDshPatch writes the document back, or deletes the file when nothing is left in it.
//
// The deletion is not tidiness. An empty or comments-only patch file does not boot dsh: the loader
// treats it as a present-but-unreadable layer and fails startup, and the runtime's own guidance is
// to write `[]` rather than leave one. An absent file, by contrast, is skipped cleanly. So an
// uninstall that removed Beacon's last element must remove the file -- truncating it to an empty
// list would work, but it would leave behind a file Beacon created and the user never asked for.
//
// A file that still has other elements is written back, and a file Beacon is emptying but did not
// create is removed too: Beacon wrote every element that was in it, so there is nothing of the
// user's to preserve, and the alternative is the non-booting file above.
func writeDshPatch(path string, doc dshPatchDocument) error {
	root := doc.node.Content[0]
	if len(root.Content) == 0 {
		if !doc.existed {
			return nil
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	var buffer strings.Builder
	encoder := yaml.NewEncoder(&buffer)
	// Two spaces, matching the runtime's own overlay examples. yaml.v3 defaults to four, which
	// would reindent every line of a file it did not write the first time Beacon touched it -- a
	// diff an operator would have to read to find the one line that actually changed.
	encoder.SetIndent(2)
	if err := encoder.Encode(doc.node); err != nil {
		return err
	}
	if err := encoder.Close(); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(buffer.String()), 0644)
}
