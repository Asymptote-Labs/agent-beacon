package sessionfs

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// backfillCollectors is every package endpointBackfillRunners (cmd/endpoint_backfill.go) sweeps. A
// system backfill confines reads only for code that reads through this package, so a new
// session-store collector joins this list.
var backfillCollectors = []string{
	"claudesession", "codexsession", "cursorsession", "opencodesession", "copilotsession",
	"clinesession", "factorysession", "pisession", "primesession", "hermessession",
	"dshsession", "fxsession", "groksession", "openclawsession", "devinsession",
}

// directReads are the calls that would read a session store around the confinement.
var directReads = map[string]bool{
	"os.Open": true, "os.OpenFile": true, "os.ReadFile": true, "os.ReadDir": true,
	"os.Stat": true, "os.Lstat": true, "os.DirFS": true, "os.OpenRoot": true,
	"filepath.WalkDir": true, "filepath.Walk": true, "filepath.Glob": true,
	"ioutil.ReadFile": true, "ioutil.ReadDir": true, "sql.Open": true,
}

// allowedDirectReads are the exceptions, by package, function and call.
var allowedDirectReads = map[string]bool{
	// The collector's own cursor file, under Beacon's base directory rather than the user's store.
	"*.LoadState.os.ReadFile": true,
	// The driver open itself, after SQLitePath has turned the path into a confined snapshot.
	"cursorsession.openSQLiteReadOnly.sql.Open":   true,
	"hermessession.openSQLiteReadOnly.sql.Open":   true,
	"opencodesession.openSQLiteReadOnly.sql.Open": true,
	"devinsession.openSQLiteReadOnly.sql.Open":    true,
	// Existence checks on project paths named inside a transcript, to infer the workspace. They
	// read no content and the paths are usually outside the home.
	"cursorsession.normalizeDirectoryCandidate.os.Stat":        true,
	"cursorsession.resolveDashEncodedProjectDirectory.os.Stat": true,
}

func TestBackfillCollectorsReadTheirStoresThroughSessionfs(t *testing.T) {
	for _, pkg := range backfillCollectors {
		files, err := filepath.Glob(filepath.Join("..", pkg, "*.go"))
		if err != nil || len(files) == 0 {
			t.Fatalf("%s: no sources (%v)", pkg, err)
		}
		for _, file := range files {
			if strings.HasSuffix(file, "_test.go") {
				continue
			}
			checkCollectorFile(t, pkg, file)
		}
	}
}

func checkCollectorFile(t *testing.T, pkg, file string) {
	t.Helper()
	src, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	parsed, err := parser.ParseFile(fset, file, src, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, decl := range parsed.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		var calls []string
		ast.Inspect(fn, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if ident, ok := sel.X.(*ast.Ident); ok {
				name := ident.Name + "." + sel.Sel.Name
				if directReads[name] {
					calls = append(calls, name)
				}
			}
			return true
		})
		usesSQLitePath := strings.Contains(string(src[fset.Position(fn.Pos()).Offset:fset.Position(fn.End()).Offset]), "sessionfs.SQLitePath(")
		for _, call := range calls {
			key := pkg + "." + fn.Name.Name + "." + call
			if allowedDirectReads[key] || allowedDirectReads["*."+fn.Name.Name+"."+call] {
				if call == "sql.Open" && !usesSQLitePath {
					t.Errorf("%s: %s opens SQLite without sessionfs.SQLitePath", file, fn.Name.Name)
				}
				continue
			}
			t.Errorf("%s: %s calls %s directly; read the session store through sessionfs so a system backfill stays confined to the console user's home", file, fn.Name.Name, call)
		}
	}
}
