package concepts

import (
	"fmt"
	"strings"
	"sync"

	"github.com/google/cel-go/cel"
)

// A rule does not run the catalog's matcher (Lookup, LookupCommand): it runs the expressions
// generated from the catalog (Select, rendered by PathCEL, MentionCEL, CommandCEL and
// RedactedCEL). Check holds those to the catalog's examples as it holds the matcher, for every
// selection a rule can ask for, so that the two cannot drift apart for any catalog: the
// corpus's, or one a pack brings.

// selectionEnv is the CEL environment a selection's expressions are evaluated in: s, a text (a
// path, a command line), and c, a parsed command as the rule engine presents one, with its name
// and its args (one word per line).
var selectionEnv = sync.OnceValues(func() (*cel.Env, error) {
	return cel.NewEnv(cel.Variable("s", cel.StringType), cel.Variable("c", cel.MapType(cel.StringType, cel.StringType)))
})

// generated is one selection a rule can ask for, its expressions compiled.
type generated struct {
	tier                             Tier
	kinds                            []string
	selection                        Selection
	path, mention, command, redacted cel.Program
}

func (g *generated) String() string {
	if len(g.kinds) == 0 {
		return fmt.Sprintf("the %s stores", g.tier)
	}
	return fmt.Sprintf("the %s stores of kind %s", g.tier, strings.Join(g.kinds, ", "))
}

// expressions are the catalog's generated expressions for every selection a rule can ask for:
// each tier, of every kind and of each kind.
type expressions []*generated

func (c *Catalog) expressions() (expressions, error) {
	env, err := selectionEnv()
	if err != nil {
		return nil, err
	}
	compile := func(expr string) (cel.Program, error) {
		ast, iss := env.Compile(expr)
		if iss != nil && iss.Err() != nil {
			return nil, iss.Err()
		}
		return env.Program(ast, cel.EvalOptions(cel.OptOptimize))
	}
	var all expressions
	for _, tier := range []Tier{TierDefinite, TierLikely} {
		kinds := [][]string{nil}
		for _, k := range c.Kinds {
			kinds = append(kinds, []string{k.ID})
		}
		for _, kind := range kinds {
			selection, err := c.Select(tier, kind...)
			if err != nil {
				return nil, err
			}
			g := &generated{tier: tier, kinds: kind, selection: selection}
			for _, form := range []struct {
				prog *cel.Program
				expr string
			}{
				{&g.path, selection.PathCEL("s")},
				{&g.mention, selection.MentionCEL("s")},
				{&g.command, selection.CommandCEL("c")},
				{&g.redacted, selection.RedactedCEL("s")},
			} {
				if *form.prog, err = compile(form.expr); err != nil {
					return nil, fmt.Errorf("%s: the generated expression does not compile: %w", g, err)
				}
			}
			all = append(all, g)
		}
	}
	return all, nil
}

func evalBool(prog cel.Program, vars map[string]any) bool {
	out, _, err := prog.Eval(vars)
	if err != nil {
		panic("concepts: a generated expression failed to evaluate: " + err.Error())
	}
	b, _ := out.Value().(bool)
	return b
}

// selected reports whether one of the stores a selection takes is what matches says.
func (c *Catalog) selected(g *generated, matches func(*Store) bool) bool {
	for i := range c.Stores {
		if s := &c.Stores[i]; s.selectedBy(g.tier, g.kinds) && matches(s) {
			return true
		}
	}
	return false
}

// checkPath requires every selection's expressions to say of path what the catalog's matcher
// says: the expression over a path, and the one over a text, for a path a text can name (one in
// a directory, with no blank in it).
func (x expressions) checkPath(c *Catalog, path string) error {
	comps := splitPath(path)
	named := strings.ContainsAny(path, `/\`) && !strings.ContainsAny(path, " \t\n")
	for _, g := range x {
		want := c.selected(g, func(s *Store) bool { return s.matchesComponents(comps) })
		vars := map[string]any{"s": path}
		if got := evalBool(g.path, vars); got != want {
			return fmt.Errorf("the expression generated for %s says %v of the path, the catalog %v", g, got, want)
		}
		if got := evalBool(g.mention, vars); named && got != want {
			return fmt.Errorf("the expression generated for a text that names %s says %v of the path, the catalog %v", g, got, want)
		}
	}
	return nil
}

// checkCommandLine requires every selection's expressions to say of each command a command line
// runs what the catalog's matcher says (checkCommand). For a line that is one command and
// nothing else, the expression for a log whose redaction took the command's name must find what
// such a log holds (TOKEN=[REDACTED] auth token) for TOKEN=$(gh auth token)) where a store has
// a redacted form for the command, and must not find it for a command that is no store at all.
func (x expressions) checkCommandLine(c *Catalog, line string) error {
	commands, err := invocations(line)
	if err != nil {
		return err
	}
	for _, inv := range commands {
		if err := x.checkCommand(c, inv); err != nil {
			return err
		}
	}
	if len(commands) != 1 || !strings.HasPrefix(line, commands[0].name+" ") {
		return nil
	}
	inv := commands[0]
	_, anyStore := c.lookupCommand(inv.name, inv.args)
	logged := map[string]any{"s": "TOKEN=[REDACTED]" + strings.TrimPrefix(line, inv.name) + ")"}
	for _, g := range x {
		redacted := c.selected(g, func(s *Store) bool { return s.redactedForm(inv.name, inv.args) })
		if got := evalBool(g.redacted, logged); redacted && !got || !anyStore && got {
			return fmt.Errorf("the expression generated for %s with the command's name redacted says %v of %q, the catalog %v", g, got, logged["s"], redacted)
		}
	}
	return nil
}

// checkCommand requires every selection's expression over a parsed command to say of one what
// the catalog's matcher says.
func (x expressions) checkCommand(c *Catalog, inv invocation) error {
	vars := map[string]any{"c": map[string]string{"name": inv.name, "args": strings.Join(inv.args, "\n")}}
	for _, g := range x {
		want := c.selected(g, func(s *Store) bool { return s.matchesCommand(inv.name, inv.args) })
		if got := evalBool(g.command, vars); got != want {
			return fmt.Errorf("the expression generated for %s says %v of %s %q, the catalog %v", g, got, inv.name, inv.args, want)
		}
	}
	return nil
}

// redactedForm reports whether the command with this name and these words after it is one of the
// store's by a match that its redacted form finds by its words (redactedRegex). One told only by
// options is found only where the line writes an option in full, which a match does not ask.
func (s *Store) redactedForm(name string, args []string) bool {
	for k := range s.commands {
		if p := &s.commands[k]; len(p.words) > 0 && p.redactedRegex() != "" && p.matches(name, args) {
			return true
		}
	}
	return false
}
