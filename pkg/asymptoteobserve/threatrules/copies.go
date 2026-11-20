package threatrules

import (
	"fmt"
	"strings"

	celast "github.com/google/cel-go/common/ast"
)

// CatalogCopy says that a rule's own regular expressions say again what a catalog of the
// rule's pack says: between them they spell out two or more of the catalog's stores, or one of
// them defines a store on its own (it names the store and accepts every example the store
// gives). The copy is what a catalog exists to prevent. It starts out equal to the catalog and
// stops being so the next time either is changed, and nothing says which of the two is right.
type CatalogCopy struct {
	// Catalog is the catalog's name, and Stores the stores of it the rule spells out, in the
	// catalog's order.
	Catalog string
	Stores  []string
	// Patterns are the regular expressions that name a store, as the rule writes them.
	Patterns []string
}

func (c CatalogCopy) String() string {
	pattern := c.Patterns[0]
	if len(pattern) > 96 {
		pattern = pattern[:96] + "..."
	}
	return fmt.Sprintf("its regular expressions spell out %s of catalog %s (the first is %q); call the catalog's predicates instead, so that the rule and the catalog cannot drift apart",
		strings.Join(c.Stores, ", "), c.Catalog, pattern)
}

// CatalogCopies returns, for each catalog of rule's pack that the rule copies, what it copies:
// the stores that the constant patterns of its matches() calls name (concepts.Catalog.Named),
// when there are two or more. One store may be named for a reason of the rule's own; two are
// a list. It looks at what a catalog's predicates are asked about, where calling one is the
// same question: a path (e.file.path, the path of a file a command reads), a command line
// (e.command.command) and a parsed command's args. A pattern matched against something else
// (a tool description, one word of a command) is the rule's own business. It reads the rule as
// written (asWritten): what a predicate expands to is the catalog's own and is not looked at. A
// rule loaded without definitions has nothing to copy.
func CatalogCopies(rule *Rule) []CatalogCopy {
	if rule.lib == nil || len(rule.lib.catalogs) == 0 {
		return nil
	}
	var patterns []string
	for _, e := range rule.written() {
		for _, literal := range matchesLiterals(e) {
			if catalogSubject(literal.subject) {
				patterns = append(patterns, literal.pattern)
			}
		}
	}
	var copies []CatalogCopy
	for _, catalog := range rule.lib.catalogs {
		copied := CatalogCopy{Catalog: catalog.Name}
		named, defined := map[string]bool{}, map[string]bool{}
		for _, pattern := range patterns {
			stores := catalog.Named(pattern)
			if len(stores) == 0 {
				continue
			}
			copied.Patterns = append(copied.Patterns, pattern)
			for _, store := range stores {
				named[store] = true
			}
			for _, store := range catalog.Defined(pattern) {
				defined[store] = true
			}
		}
		// One store named is the rule's own business unless the rule also says what it is.
		if len(named) < 2 {
			named = defined
		}
		if len(named) == 0 {
			continue
		}
		for _, store := range catalog.Stores {
			if named[store.ID] {
				copied.Stores = append(copied.Stores, store.ID)
			}
		}
		copies = append(copies, copied)
	}
	return copies
}

// catalogSubject reports whether an expression is something a catalog's predicates are asked
// about: a path, the command line, or a parsed command's args.
func catalogSubject(e celast.Expr) bool {
	if e.Kind() != celast.SelectKind {
		return false
	}
	sel := e.AsSelect()
	switch sel.FieldName() {
	case "path", "args":
		return true
	case "command":
		op := sel.Operand()
		return op.Kind() == celast.SelectKind && op.AsSelect().FieldName() == "command"
	}
	return false
}
