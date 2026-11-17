package repocheck

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The push gate runs with SCRIOBH_TIMING=off, so a test that asserts a
// wall-clock budget must call testutil.Timing(t) (see internal/testutil) or it
// would fail on the shared runner and measure the machine instead of the code.
// This is a static check, and a simple one. It parses every _test.go file in
// the repository and reports:
//
//  1. an upper-bound comparison on a measured duration (`time.Since(x) > b`,
//     `b < time.Since(x)`, or the same through a local variable assigned from
//     time.Since or time.Now().Sub) in a function that has no Timing call;
//  2. a call, in a function with no Timing call, to a same-package helper that
//     takes a time.Duration parameter and compares it against another value
//     (the `bound(t, name, time.Since(s), limit)` shape), unless the helper
//     does the comparison in a closure that itself calls Timing.
//
// A function is "covered" when it, or a function that encloses it, calls
// Timing directly. A Timing call inside a nested t.Run closure covers that
// closure only, so a behaviour test that keeps its time assertion in a
// t.Run("time", ...) subtest has to call Timing in the subtest.
//
// What it does not see (a reviewer has to): a relative budget (`slow > 3*fast`,
// the linearity and same-time tests), because a comparison of two measurements
// is ignored; a duration that reaches a
// comparison through a helper that is not shaped like `bound` (a helper that
// returns a median or a p95 of a slice and a test that compares that); a
// comparison through a struct field or a channel; a product-reported duration
// (a `.Took` or `Ms` field); `testing.Benchmark`; and whether Timing is the
// first line (the check does not order it, so a fixture built before Timing
// still costs the push gate its time). Lower bounds ("it did wait at least
// 100 ms"), a comparison of two measurements (sorting samples, a best or worst
// tracker), `for` conditions (a polling loop with a deadline) and log-only
// measurements are not reported. A comparison that looks like a budget and is
// not one carries the marker below on its own line or the line above it, with
// the reason.
const notABudgetMarker = "timing: not a budget"

// timingViolation is one place the check complains about.
type timingViolation struct {
	pos  token.Position
	fn   string
	what string
}

func (v timingViolation) String() string {
	return v.pos.String() + ": " + v.fn + ": " + v.what
}

// durationHelpers returns the names of the package-level functions in files that
// take a time.Duration parameter and compare it with something.
func durationHelpers(files []*ast.File) map[string]bool {
	out := map[string]bool{}
	for _, f := range files {
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Body == nil || fd.Recv != nil || strings.HasPrefix(fd.Name.Name, "Test") {
				continue
			}
			params := map[string]bool{}
			for _, fld := range fd.Type.Params.List {
				if sel, ok := fld.Type.(*ast.SelectorExpr); ok && sel.Sel.Name == "Duration" {
					if x, ok := sel.X.(*ast.Ident); ok && x.Name == "time" {
						for _, n := range fld.Names {
							params[n.Name] = true
						}
					}
				}
			}
			if len(params) == 0 {
				continue
			}
			if directTiming(fd.Body) {
				continue // the helper skips the test itself
			}
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				// A comparison inside a closure that calls Timing (a t.Run("time", ...)
				// in the helper) is skipped with the closure: not a budget helper.
				if lit, ok := n.(*ast.FuncLit); ok && directTiming(lit.Body) {
					return false
				}
				be, ok := n.(*ast.BinaryExpr)
				if !ok || !isOrdering(be.Op) {
					return true
				}
				if mentions(be.X, params) || mentions(be.Y, params) {
					out[fd.Name.Name] = true
				}
				return true
			})
		}
	}
	return out
}

func isOrdering(op token.Token) bool {
	return op == token.GTR || op == token.GEQ || op == token.LSS || op == token.LEQ
}

// mentions reports whether e uses an identifier in names.
func mentions(e ast.Expr, names map[string]bool) bool {
	found := false
	ast.Inspect(e, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && names[id.Name] {
			found = true
		}
		return !found
	})
	return found
}

// isMeasure reports whether call is time.Since(...) or <expr>.Sub(...) on a
// time.Now() receiver.
func isMeasure(n ast.Node) bool {
	call, ok := n.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	if x, ok := sel.X.(*ast.Ident); ok && x.Name == "time" && sel.Sel.Name == "Since" {
		return true
	}
	if sel.Sel.Name == "Sub" {
		if inner, ok := sel.X.(*ast.CallExpr); ok {
			if s2, ok := inner.Fun.(*ast.SelectorExpr); ok {
				if x, ok := s2.X.(*ast.Ident); ok && x.Name == "time" && s2.Sel.Name == "Now" {
					return true
				}
			}
		}
	}
	return false
}

func containsMeasure(e ast.Node) bool {
	found := false
	ast.Inspect(e, func(n ast.Node) bool {
		if isMeasure(n) {
			found = true
		}
		return !found
	})
	return found
}

func isTimingCall(n ast.Node) bool {
	call, ok := n.(*ast.CallExpr)
	if !ok {
		return false
	}
	switch f := call.Fun.(type) {
	case *ast.SelectorExpr:
		return f.Sel.Name == "Timing"
	case *ast.Ident:
		return f.Name == "Timing"
	}
	return false
}

// directTiming reports whether body calls Timing outside any nested function
// literal.
func directTiming(body *ast.BlockStmt) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		if _, ok := n.(*ast.FuncLit); ok {
			return false
		}
		if isTimingCall(n) {
			found = true
		}
		return !found
	})
	return found
}

// checkTimingFile returns the violations in one parsed file. helpers is the set
// from durationHelpers for the file's package.
func checkTimingFile(fset *token.FileSet, f *ast.File, helpers map[string]bool) []timingViolation {
	marked := map[int]bool{}
	for _, cg := range f.Comments {
		for _, c := range cg.List {
			if strings.Contains(c.Text, notABudgetMarker) {
				line := fset.Position(c.Pos()).Line
				marked[line] = true
				marked[line+1] = true
			}
		}
	}
	var out []timingViolation
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Body == nil {
			continue
		}
		name := fd.Name.Name
		// Taint: identifiers assigned from an expression that measures time or
		// uses an already tainted identifier (per/n, append(ds, time.Since(...))).
		tainted := map[string]bool{}
		for pass := 0; pass < 3; pass++ {
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				as, ok := n.(*ast.AssignStmt)
				if !ok {
					return true
				}
				hit := false
				for _, r := range as.Rhs {
					if containsMeasure(r) || mentions(r, tainted) {
						hit = true
					}
				}
				if hit {
					for _, l := range as.Lhs {
						if ix, ok := l.(*ast.IndexExpr); ok { // times[i] = time.Since(start)
							l = ix.X
						}
						if id, ok := l.(*ast.Ident); ok && id.Name != "_" {
							tainted[id.Name] = true
						}
					}
				}
				return true
			})
		}
		measured := func(e ast.Expr) bool { return containsMeasure(e) || mentions(e, tainted) }

		var walk func(n ast.Node, covered bool)
		walk = func(n ast.Node, covered bool) {
			// forCond holds the nodes of a for statement's condition: a polling
			// loop with a deadline is not a budget.
			skip := map[ast.Node]bool{}
			ast.Inspect(n, func(m ast.Node) bool {
				if m == nil {
					return false
				}
				if lit, ok := m.(*ast.FuncLit); ok && m != n {
					walk(lit.Body, covered || directTiming(lit.Body))
					return false
				}
				if loop, ok := m.(*ast.ForStmt); ok && loop.Cond != nil {
					ast.Inspect(loop.Cond, func(c ast.Node) bool {
						if c != nil {
							skip[c] = true
						}
						return true
					})
				}
				if skip[m] || covered {
					return true
				}
				line := fset.Position(m.Pos()).Line
				switch x := m.(type) {
				case *ast.BinaryExpr:
					// A comparison of two measurements (sorting samples, a best or
					// worst tracker) is not a budget.
					upper := !(measured(x.X) && measured(x.Y)) &&
						(((x.Op == token.GTR || x.Op == token.GEQ) && measured(x.X)) ||
							((x.Op == token.LSS || x.Op == token.LEQ) && measured(x.Y)))
					if upper && !marked[line] {
						out = append(out, timingViolation{fset.Position(x.Pos()), name,
							"compares a measured duration with a budget and never calls testutil.Timing"})
					}
				case *ast.CallExpr:
					if id, ok := x.Fun.(*ast.Ident); ok && helpers[id.Name] && !marked[line] {
						for _, a := range x.Args {
							if measured(a) {
								out = append(out, timingViolation{fset.Position(x.Pos()), name,
									"passes a measured duration to the budget helper " + id.Name + " and never calls testutil.Timing"})
								break
							}
						}
					}
				}
				return true
			})
		}
		walk(fd.Body, directTiming(fd.Body))
	}
	return out
}

// checkTimingSource parses src as a package of one test file and checks it.
func checkTimingSource(t *testing.T, src string) []timingViolation {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "x_test.go", src, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	return checkTimingFile(fset, f, durationHelpers([]*ast.File{f}))
}

func TestTimingCheckBites(t *testing.T) {
	const head = "package p\nimport (\"testing\"; \"time\"; \"x/testutil\")\nvar _ = testutil.Timing\n"
	bad := map[string]string{
		"direct": head + `func TestA(t *testing.T) {
	start := time.Now()
	if time.Since(start) > time.Second { t.Fatal("slow") }
}`,
		"variable": head + `func TestA(t *testing.T) {
	start := time.Now()
	took := time.Since(start)
	if took >= 2*time.Second { t.Fatal("slow") }
}`,
		"reversed": head + `func TestA(t *testing.T) {
	start := time.Now()
	if time.Second < time.Since(start) { t.Fatal("slow") }
}`,
		"derived": head + `func TestA(t *testing.T) {
	start := time.Now()
	for i := 0; i < 3; i++ {}
	per := time.Since(start) / 3
	if per > time.Millisecond { t.Fatal("slow") }
}`,
		"indexed": head + `func TestA(t *testing.T) {
	times := make([]time.Duration, 3)
	for i := range times { start := time.Now(); times[i] = time.Since(start) }
	median := times[1]
	if median > time.Second { t.Fatal("slow") }
}`,
		"sub": head + `func TestA(t *testing.T) {
	start := time.Now()
	if time.Now().Sub(start) > time.Second { t.Fatal("slow") }
}`,
		"helper": head + `func bound(t *testing.T, got, limit time.Duration) {
	if got > limit { t.Fatal("slow") }
}
func TestA(t *testing.T) {
	start := time.Now()
	bound(t, time.Since(start), time.Second)
}`,
		"timing in a subtest does not cover the parent": head + `func TestA(t *testing.T) {
	start := time.Now()
	if time.Since(start) > time.Second { t.Fatal("slow") }
	t.Run("time", func(t *testing.T) { testutil.Timing(t) })
}`,
	}
	for name, src := range bad {
		if v := checkTimingSource(t, src); len(v) == 0 {
			t.Errorf("%s: the check did not report a budget with no Timing call", name)
		}
	}
	good := map[string]string{
		"timing first": head + `func TestA(t *testing.T) {
	testutil.Timing(t)
	start := time.Now()
	if time.Since(start) > time.Second { t.Fatal("slow") }
}`,
		"timing in the subtest that asserts": head + `func TestA(t *testing.T) {
	start := time.Now()
	took := time.Since(start)
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		if took > time.Second { t.Fatal("slow") }
	})
}`,
		"timing covers a closure": head + `func TestA(t *testing.T) {
	testutil.Timing(t)
	start := time.Now()
	func() { if time.Since(start) > time.Second { t.Fatal("slow") } }()
}`,
		"two measurements compared": head + `func TestA(t *testing.T) {
	times := make([]time.Duration, 3)
	for i := range times { start := time.Now(); times[i] = time.Since(start) }
	sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })
}`,
		"lower bound": head + `func TestA(t *testing.T) {
	start := time.Now()
	if time.Since(start) < 100*time.Millisecond { t.Fatal("did not wait") }
}`,
		"polling loop with a deadline": head + `func TestA(t *testing.T) {
	start := time.Now()
	for time.Since(start) < 10*time.Second { }
}`,
		"log only": head + `func TestA(t *testing.T) {
	start := time.Now()
	t.Logf("took %v", time.Since(start))
}`,
		"marker": head + `func TestA(t *testing.T) {
	start := time.Now()
	// timing: not a budget: waits for the deadline to fire
	if time.Since(start) > time.Minute { t.Fatal("hung") }
}`,
		"helper with timing": head + `func bound(t *testing.T, got, limit time.Duration) {
	if got > limit { t.Fatal("slow") }
}
func TestA(t *testing.T) {
	testutil.Timing(t)
	bound(t, time.Since(time.Now()), time.Second)
}`,
		"helper that compares inside a Timing subtest": head + `func bound(t *testing.T, got, limit time.Duration) {
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		if got > limit { t.Fatal("slow") }
	})
}
func TestA(t *testing.T) {
	start := time.Now()
	bound(t, time.Since(start), time.Second)
}`,
		"helper called with a literal": head + `func bound(t *testing.T, got, limit time.Duration) {
	if got > limit { t.Fatal("slow") }
}
func TestA(t *testing.T) { bound(t, time.Second, 2*time.Second) }`,
	}
	for name, src := range good {
		if v := checkTimingSource(t, src); len(v) != 0 {
			t.Errorf("%s: the check reported %v", name, v)
		}
	}
}

// Every test file in the repository: a budget needs the Timing call.
func TestTimingTestsCallTimingSkip(t *testing.T) {
	root := repoRoot(t)
	byDir := map[string][]string{}
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			switch d.Name() {
			case "node_modules", ".git", ".claude", "archive", "vendor", "tmp":
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, "_test.go") {
			byDir[filepath.Dir(path)] = append(byDir[filepath.Dir(path)], path)
		}
		return nil
	})
	var bad []string
	checked := 0
	for _, paths := range byDir {
		fset := token.NewFileSet()
		var files []*ast.File
		for _, p := range paths {
			f, err := parser.ParseFile(fset, p, nil, parser.ParseComments)
			if err != nil {
				t.Errorf("%s: %v", p, err)
				continue
			}
			files = append(files, f)
			checked++
		}
		helpers := durationHelpers(files)
		for _, f := range files {
			seen := map[string]bool{} // one line per function: the first hit
			for _, v := range checkTimingFile(fset, f, helpers) {
				if seen[v.fn] {
					continue
				}
				seen[v.fn] = true
				rel, err := filepath.Rel(root, v.pos.Filename)
				if err != nil {
					rel = v.pos.Filename
				}
				bad = append(bad, rel+":"+strconv.Itoa(v.pos.Line)+": "+v.fn+": "+v.what)
			}
		}
	}
	if checked < 50 {
		t.Fatalf("only %d test files found: the walk is broken", checked)
	}
	sort.Strings(bad)
	if len(bad) > 0 {
		t.Errorf("tests that compare a measured duration with a budget and never call testutil.Timing(t) "+
			"(the push gate sets SCRIOBH_TIMING=off; call it first in a test that only measures, or move the "+
			"bound into a t.Run(\"time\") subtest that calls it; a comparison that is not a budget takes a "+
			"`// %s: <reason>` comment):\n  %s", notABudgetMarker, strings.Join(bad, "\n  "))
	}
}
