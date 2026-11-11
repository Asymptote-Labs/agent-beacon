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
	b.WriteString("A pattern names a file by the last components of its path, whatever directory they are in:\n" +
		"`.aws/credentials` is the file `credentials` in a directory `.aws`. Letters match without regard\n" +
		"to case, and `/` in a pattern is `/` or `\\` in a path. `*` is any run of characters within a\n" +
		"component, `{a,b,}` one of the alternatives (which may be empty), a component `**` any number\n" +
		"of directories, and a trailing `/**` everything under a directory.\n\n" +
		"A `definite` store exists to hold a credential. A `likely` one often holds one and often does\n" +
		"not. A path that two stores match is the definite one's, then the one listed first.\n")
	for _, kind := range c.Kinds {
		header := false
		for i := range c.Stores {
			s := &c.Stores[i]
			if s.Kind != kind.ID {
				continue
			}
			if !header {
				fmt.Fprintf(&b, "\n## %s (`%s`)\n\n| Store | Tier | Paths | Except | Example |\n|---|---|---|---|---|\n", kind.Title, kind.ID)
				header = true
			}
			fmt.Fprintf(&b, "| `%s`: %s | %s | %s | %s | `%s` |\n", s.ID, s.Title, s.Tier, code(s.Match), code(s.Exclude), s.Examples[0])
		}
	}
	b.WriteString("\n## Generated expressions\n\n" +
		"What a rule or a query uses is a selection of the catalog as one expression. These are the two\n" +
		"selections of every kind, over a path `p` in CEL and a column `FilePath` in KQL. The regular\n" +
		"expressions are RE2 syntax, which both read the same way.\n")
	for _, tier := range []Tier{TierDefinite, TierLikely} {
		selection, err := c.Select(tier)
		if err != nil {
			return "", err
		}
		title := "`definite` stores"
		if tier == TierLikely {
			title = "`likely` and `definite` stores"
		}
		fmt.Fprintf(&b, "\n### %s\n\nCEL:\n\n```\n%s\n```\n\nKQL:\n\n```\n%s\n```\n", title, selection.CEL("p"), selection.KQL("FilePath"))
	}
	return b.String(), nil
}

// code renders patterns as inline code, one per line of a table cell.
func code(patterns []string) string {
	quoted := make([]string, len(patterns))
	for i, p := range patterns {
		quoted[i] = "`" + p + "`"
	}
	return strings.Join(quoted, "<br>")
}
