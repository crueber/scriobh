package review

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// Every read of the comments table must choose who may see what it reads: through fetch (which applies visible), through visible or audience in
// the statement itself, or by naming visibility in the SQL. A new reader that does not fails this test, so a private ("Only me") comment cannot
// leak through a query somebody adds later: the author has to say, here, why a read is exempt.
//
// The unit is a statement (an assignment, a call, a return, a declaration), seen without the bodies of function literals inside it, which are
// statements of their own. What counts as a read:
//   - FROM or JOIN comments, in any statement;
//   - an UPDATE, INSERT or DELETE on comments with RETURNING (it reads what it changes);
//   - a table name put together at run time (a literal that ends in FROM, or FROM and a quote) in a function that lists sqlite_master: a loop over
//     every table reads comments too.
//
// The walk covers internal/ and cmd/, tests and testdata left out.
var (
	readOfComments = regexp.MustCompile(`(?is)\b(?:FROM|JOIN)\s+"?comments\b`)
	returningRead  = regexp.MustCompile(`(?is)\b(?:UPDATE|INSERT\s+INTO|DELETE\s+FROM)\s+"?comments\b.*\bRETURNING\b`)
	dynamicTable   = regexp.MustCompile(`(?is)\b(?:FROM|JOIN)\s*["'` + "`" + `]?\s*$`)
)

// exempt lists the statements that read comments for no one, with the reason. The key is the file under internal/ (or cmd/) and the function.
var exempt = map[string]string{
	"domain/comanchor/comanchor.go:afterWrite": "maintenance: moves the anchors of every thread of a document, private ones included, and returns counts only",
	"domain/review/notify.go:Sweep":            "lifecycle: deletes old tombstones and the private rows of authors who are gone; nothing is returned",
	"domain/review/comments.go:commentFrom":    "the FROM of fetch, the only place that uses it (asserted below); fetch adds visible()",
	"testutil/dump.go:Dump":                    "a test helper: digests every table, so that a test can prove an operation changed nothing",
	"testutil/dump.go:TablesHolding":           "a test helper: looks in every table for a canary, so that a test can prove a secret is nowhere but where it belongs",
}

// exemptSQL lists single statements, by function and a piece of their SQL, that read comments through a condition built somewhere else.
var exemptSQL = []struct{ key, contains, why string }{
	{"domain/review/comments.go:threadCounts", "FROM comments c", "its WHERE is threadFilter's, which starts with visible() (asserted below)"},
	{"domain/review/queue.go:queue", "FROM comments c", "its WHERE starts with audience(), which is visible() narrowed (asserted below)"},
	{"domain/admin/diagnostics.go:build", "GROUP BY 1", "the tables of queueTables are job tables (import_jobs, git_pushes, ai_runs, ai_jobs), not comments"},
}

type unit struct {
	key      string
	literals string
	calls    map[string]bool
	idents   map[string]bool
	dynamic  bool // the function it is in lists sqlite_master
}

func gather(n ast.Node, u *unit) {
	var lits []string
	var walk func(ast.Node)
	walk = func(n ast.Node) {
		ast.Inspect(n, func(x ast.Node) bool {
			switch v := x.(type) {
			case *ast.FuncLit:
				return false // its statements are units of their own
			case *ast.BasicLit:
				if v.Kind == token.STRING {
					if s, err := strconv.Unquote(v.Value); err == nil {
						lits = append(lits, s)
					}
				}
			case *ast.CallExpr:
				if id, ok := v.Fun.(*ast.Ident); ok {
					u.calls[id.Name] = true
				}
			case *ast.Ident:
				u.idents[v.Name] = true
			}
			return true
		})
	}
	walk(n)
	u.literals = strings.Join(lits, "\n")
}

func units(t *testing.T) []unit {
	t.Helper()
	var out []unit
	for _, root := range []string{filepath.Join("..", ".."), filepath.Join("..", "..", "..", "cmd")} {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() && (d.Name() == "testdata" || d.Name() == "node_modules") {
				return filepath.SkipDir
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			fset := token.NewFileSet()
			f, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				t.Fatalf("%s: %v", path, err)
			}
			rel, _ := filepath.Rel(root, path)
			if strings.Contains(root, "cmd") {
				rel = "cmd/" + rel
			}
			rel = filepath.ToSlash(rel)
			add := func(name string, n ast.Node, dyn bool) {
				u := unit{key: rel + ":" + name, calls: map[string]bool{}, idents: map[string]bool{}, dynamic: dyn}
				gather(n, &u)
				out = append(out, u)
			}
			for _, decl := range f.Decls {
				switch d := decl.(type) {
				case *ast.FuncDecl:
					if d.Body == nil {
						continue
					}
					whole := unit{calls: map[string]bool{}, idents: map[string]bool{}}
					ast.Inspect(d.Body, func(x ast.Node) bool {
						if bl, ok := x.(*ast.BasicLit); ok && bl.Kind == token.STRING && strings.Contains(bl.Value, "sqlite_master") {
							whole.dynamic = true
						}
						return true
					})
					ast.Inspect(d.Body, func(x ast.Node) bool {
						switch x.(type) {
						case *ast.AssignStmt, *ast.ExprStmt, *ast.ReturnStmt, *ast.DeclStmt, *ast.SendStmt, *ast.DeferStmt, *ast.GoStmt:
							add(d.Name.Name, x, whole.dynamic)
						}
						return true
					})
				case *ast.GenDecl:
					for _, sp := range d.Specs {
						if vs, ok := sp.(*ast.ValueSpec); ok {
							for _, n := range vs.Names {
								add(n.Name, vs, false)
							}
						}
					}
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return out
}

func (u unit) reads() bool {
	if readOfComments.MatchString(u.literals) || returningRead.MatchString(u.literals) {
		return true
	}
	if u.dynamic {
		for _, l := range strings.Split(u.literals, "\n") {
			if dynamicTable.MatchString(l) {
				return true
			}
		}
	}
	return false
}

// chooses says the statement itself decides who may see what it reads.
func (u unit) chooses() bool {
	return u.calls["visible"] || u.calls["audience"] || u.calls["onlyShared"] || u.idents["commentFrom"] || strings.Contains(u.literals, "visibility")
}

func TestEveryCommentReadFiltersVisibility(t *testing.T) {
	seen := map[string]bool{}
	reads := 0
	for _, u := range units(t) {
		if !u.reads() {
			continue
		}
		reads++
		seen[u.key] = true
		if _, ok := exempt[u.key]; ok || u.chooses() {
			continue
		}
		skip := false
		for _, e := range exemptSQL {
			if e.key == u.key && strings.Contains(strings.Join(strings.Fields(u.literals), " "), e.contains) {
				skip = true
			}
		}
		if skip {
			continue
		}
		t.Errorf("%s has a statement that reads the comments table (or every table) and does not choose who may see what it reads: use fetch, visible() or audience(), or name `visibility` in the SQL (a private comment is for its author alone). If the read is for no one (maintenance), name the function in exempt with the reason.\n    SQL: %.200s", u.key, strings.Join(strings.Fields(u.literals), " "))
	}
	if reads < 25 {
		t.Fatalf("only %d statements that read comments found: is the scan stale?", reads)
	}
	var stale []string
	for _, e := range exemptSQL {
		found := false
		for _, u := range units(t) {
			if u.key == e.key && strings.Contains(strings.Join(strings.Fields(u.literals), " "), e.contains) && u.reads() {
				found = true
			}
		}
		if !found {
			stale = append(stale, e.key+" | "+e.contains)
		}
	}
	for k := range exempt {
		if !seen[k] {
			stale = append(stale, k)
		}
	}
	sort.Strings(stale)
	if len(stale) > 0 {
		t.Errorf("exempt lists functions that no longer read comments: %v", stale)
	}
}

// The helpers the rule rests on do apply it, and the one FROM of fetch is used by fetch alone.
func TestTheHelpersThatCarryTheRuleApplyIt(t *testing.T) {
	need := map[string]string{"domain/review/comments.go:fetch": "visible", "domain/review/comments.go:threadFilter": "visible", "domain/review/comments.go:audience": "visible"}
	calls := map[string]map[string]bool{}
	users := map[string]bool{}
	for _, u := range units(t) {
		if calls[u.key] == nil {
			calls[u.key] = map[string]bool{}
		}
		for c := range u.calls {
			calls[u.key][c] = true
		}
		if u.idents["commentFrom"] && u.key != "domain/review/comments.go:commentFrom" {
			users[u.key] = true
		}
	}
	for k, want := range need {
		if calls[k] == nil {
			t.Errorf("%s not found", k)
		} else if !calls[k][want] {
			t.Errorf("%s does not call %s", k, want)
		}
	}
	if len(users) != 1 || !users["domain/review/comments.go:fetch"] {
		t.Errorf("commentFrom is used by %v, and may be used by fetch alone", users)
	}
}
