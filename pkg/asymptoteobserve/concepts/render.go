package concepts

import (
	"fmt"
	"strings"
)

// Markdown renders the catalog's reference: every store by kind, and the expressions generated
// for each tier. source is the catalog file's name, and regenerate the command that rewrites the
// reference; both are named in its header.
func (c *Catalog) Markdown(source, regenerate string) (string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", c.Name)
	fmt.Fprintf(&b, "<!-- Generated from %s (version %d). Do not edit: change the catalog, then run\n     %s -->\n\n", source, c.Version, regenerate)
	if c.Description != "" {
		fmt.Fprintf(&b, "%s\n\n", strings.TrimSpace(c.Description))
	}
	b.WriteString("A store is files or commands.\n\n" +
		"A path pattern names a file by the last components of its path, whatever directory they are\n" +
		"in: `.aws/credentials` is the file `credentials` in a directory `.aws`. Letters match without\n" +
		"regard to case, and `/` in a pattern is `/` or `\\` in a path. `*` is any run of characters\n" +
		"within a component, `{a,b,}` one of the alternatives (which may be empty), a component `**`\n" +
		"any number of directories, and a trailing `/**` everything under a directory.\n\n" +
		"A command is matched after the command line is read as bash: by its name behind `sudo` and\n" +
		"the like, by words that follow each other somewhere after the name, and by an option of which\n" +
		"one must be there. `-w|-g` is either short option, alone or in a cluster such as `-aw`, in its\n" +
		"own case; a longer option is a whole word that may carry `=value`.\n\n" +
		"A `definite` store exists to hold a credential, or prints one. A `likely` one often holds one\n" +
		"and often does not. What two stores match is the definite one's, then the one listed first.\n")
	for _, kind := range c.Kinds {
		header := false
		for i := range c.Stores {
			s := &c.Stores[i]
			if s.Kind != kind.ID {
				continue
			}
			if !header {
				fmt.Fprintf(&b, "\n## %s (`%s`)\n\n| Store | Tier | Paths or commands | Except | Example |\n|---|---|---|---|---|\n", kind.Title, kind.ID)
				header = true
			}
			matches := append([]string(nil), s.Match...)
			for _, m := range s.Commands {
				matches = append(matches, m.String())
			}
			fmt.Fprintf(&b, "| `%s`: %s | %s | %s | %s | `%s` |\n", s.ID, s.Title, s.Tier, code(matches), code(s.Exclude), cell(s.Examples[0]))
		}
	}
	b.WriteString("\n## Generated expressions\n\n" +
		"What a rule or a query uses is a selection of the catalog as expressions. These are the two\n" +
		"selections of every kind: the files over a path `p` in CEL and a column `FilePath` in KQL, and\n" +
		"the commands over a parsed command `c` in CEL, which reads `c.name` and `c.args`. The regular\n" +
		"expressions are RE2 syntax, which CEL and KQL read the same way. Commands have no KQL form: a\n" +
		"query over raw events has no parsed command line.\n")
	for _, tier := range []Tier{TierDefinite, TierLikely} {
		selection, err := c.Select(tier)
		if err != nil {
			return "", err
		}
		title := "`definite` stores"
		if tier == TierLikely {
			title = "`likely` and `definite` stores"
		}
		fmt.Fprintf(&b, "\n### %s\n\nFiles, CEL:\n\n```\n%s\n```\n\nFiles, KQL:\n\n```\n%s\n```\n\nCommands, CEL:\n\n```\n%s\n```\n",
			title, selection.PathCEL("p"), selection.PathKQL("FilePath"), selection.CommandCEL("c"))
	}
	return b.String(), nil
}

// code renders patterns as inline code, one per line of a table cell.
func code(patterns []string) string {
	quoted := make([]string, len(patterns))
	for i, p := range patterns {
		quoted[i] = "`" + cell(p) + "`"
	}
	return strings.Join(quoted, "<br>")
}

// cell makes text safe inside a table cell's inline code: a | would end the cell.
func cell(text string) string {
	return strings.ReplaceAll(text, "|", `\|`)
}
