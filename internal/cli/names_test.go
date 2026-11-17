package cli_test

import (
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/cli"
	"github.com/crueber/scriobh/internal/ops"
)

// A manuscript with a nested tree, two projects of the same name, and a title
// that exists twice.
func seedBook(t *testing.T, e *env) (pid, partOne, ch3 string) {
	t.Helper()
	pid = itemID(t, e.ok("project", "create", "--title", "The Salt Road", "--kind", "novel"))
	p1 := itemID(t, e.ok("doc", "create", "The Salt Road", "--title", "Part One", "--kind", "part"))
	c3 := itemID(t, e.ok("doc", "create", "The Salt Road", "--title", "Chapter 3", "--kind", "chapter", "--parent-id", p1, "--content", "Three."))
	p2 := itemID(t, e.ok("doc", "create", "The Salt Road", "--title", "Part Two", "--kind", "part"))
	e.ok("doc", "create", "The Salt Road", "--title", "Chapter 3", "--kind", "chapter", "--parent-id", p2, "--content", "Also three.")
	e.ok("doc", "create", "The Salt Road", "--title", "Epilogue", "--kind", "chapter", "--content", "End.")
	return pid, p1, c3
}

func TestNameResolution(t *testing.T) {
	e := newEnv(t, nil)
	pid, partOne, ch3 := seedBook(t, e)

	t.Run("exact then case-folded", func(t *testing.T) {
		if got := itemID(t, e.ok("project", "get", "The Salt Road")); got != pid {
			t.Errorf("exact: %s", got)
		}
		if got := itemID(t, e.ok("project", "get", "the  salt ROAD")); got != pid {
			t.Errorf("folded: %s", got)
		}
	})
	t.Run("a path through the tree", func(t *testing.T) {
		if got := itemID(t, e.ok("doc", "get", "The Salt Road/Part One/Chapter 3")); got != ch3 {
			t.Errorf("path: %s", got)
		}
		if got := itemID(t, e.ok("doc", "get", "The Salt Road/Part One")); got != partOne {
			t.Errorf("part: %s", got)
		}
	})
	t.Run("a project flag and a bare title", func(t *testing.T) {
		r := e.ok("doc", "get", "Epilogue", "--project", "The Salt Road")
		if r.data(t)["item"].(map[string]any)["title"] != "Epilogue" {
			t.Errorf("%s", r.stdout)
		}
		r = e.ok("doc", "get", "part one/chapter 3", "--project", "the salt road")
		if itemID(t, r) != ch3 {
			t.Errorf("folded path under --project: %s", r.stdout)
		}
	})
	t.Run("an ambiguous title lists its candidates with ids and never guesses", func(t *testing.T) {
		r := e.run("doc", "get", "Chapter 3", "--project", "The Salt Road")
		if r.code != cli.ExitUsage {
			t.Fatalf("exit %d, want 2: %s", r.code, r.stderr)
		}
		f := r.fail(t)
		cands, _ := f["candidates"].([]any)
		if f["code"] != "ambiguous" || len(cands) != 2 {
			t.Fatalf("%v", f)
		}
		paths := ""
		for _, c := range cands {
			m := c.(map[string]any)
			if !strings.HasPrefix(m["id"].(string), "doc_") {
				t.Errorf("candidate without an id: %v", m)
			}
			paths += m["path"].(string) + ";"
		}
		if !strings.Contains(paths, "Part One/Chapter 3") || !strings.Contains(paths, "Part Two/Chapter 3") {
			t.Errorf("candidates should show their paths: %s", paths)
		}
		// Disambiguated by path, it works.
		if itemID(t, e.ok("doc", "get", "Part Two/Chapter 3", "--project", "The Salt Road")) == ch3 {
			t.Error("resolved to the wrong chapter")
		}
	})
	t.Run("two projects with one name are ambiguous", func(t *testing.T) {
		e.ok("project", "create", "--title", "Twin")
		e.ok("project", "create", "--title", "Twin")
		r := e.run("project", "get", "Twin")
		if r.code != cli.ExitUsage || r.fail(t)["code"] != "ambiguous" || len(r.fail(t)["candidates"].([]any)) != 2 {
			t.Errorf("exit %d: %s", r.code, r.stderr)
		}
	})
	t.Run("exact beats case-folded", func(t *testing.T) {
		e.ok("project", "create", "--title", "Dune")
		e.ok("project", "create", "--title", "DUNE")
		r := e.ok("project", "get", "Dune")
		if r.data(t)["item"].(map[string]any)["title"] != "Dune" {
			t.Errorf("%s", r.stdout)
		}
		// Folded alone is a fine match; folded with two candidates is ambiguous.
		if r := e.run("project", "get", "dune"); r.code != cli.ExitUsage {
			t.Errorf("two case variants: exit %d, want 2", r.code)
		}
	})
	t.Run("not found suggests the nearest", func(t *testing.T) {
		r := e.run("doc", "get", "The Salt Road/Part One/Chapter 4")
		f := r.fail(t)
		if r.code != cli.ExitNotFound || f["code"] != "not_found" || !strings.Contains(f["message"].(string), "Chapter 4") {
			t.Errorf("exit %d: %s", r.code, r.stderr)
		}
		r = e.run("project", "get", "Salt")
		if r.code != cli.ExitNotFound || !strings.Contains(r.stderr, "The Salt Road") {
			t.Errorf("a near miss should suggest: %s", r.stderr)
		}
	})
	t.Run("a document name needs a project", func(t *testing.T) {
		r := e.run("doc", "get", "Epilogue")
		if r.code != cli.ExitUsage || !strings.Contains(r.fail(t)["message"].(string), "project") {
			t.Errorf("exit %d: %s", r.code, r.stderr)
		}
	})
	t.Run("ids pass through without a single lookup", func(t *testing.T) {
		e.seen()
		e.ok("doc", "get", ch3)
		got := strings.Join(e.seen(), ",")
		if strings.Contains(got, "GET /api/v1/projects") && !strings.Contains(got, "/api/v1/docs/") {
			t.Errorf("an id should not trigger lookups: %s", got)
		}
		for _, p := range strings.Split(got, ",") {
			if p == "GET /api/v1/projects" || strings.HasSuffix(p, "/docs") {
				t.Errorf("unexpected list call %s in %s", p, got)
			}
		}
		// id: and name: force the reading.
		if itemID(t, e.ok("doc", "get", "id:"+ch3)) != ch3 {
			t.Error("id: prefix")
		}
		e.ok("project", "create", "--title", "prj_looks_like_an_id_but_is_a_name_x")
		if r := e.ok("project", "get", "name:prj_looks_like_an_id_but_is_a_name_x"); r.data(t)["item"].(map[string]any)["title"] != "prj_looks_like_an_id_but_is_a_name_x" {
			t.Errorf("name: prefix: %s", r.stdout)
		}
	})
	t.Run("a trashed project is found by name for a restore", func(t *testing.T) {
		e.ok("project", "create", "--title", "Soon Gone")
		e.ok("project", "trash", "Soon Gone", "--yes")
		r := e.ok("project", "restore", "Soon Gone")
		if r.data(t)["item"].(map[string]any)["trashed"] != false {
			t.Errorf("%s", r.stdout)
		}
	})
	t.Run("a sibling named in the same command is found in the same project", func(t *testing.T) {
		r := e.ok("doc", "move", "Part Two/Chapter 3", "--project", "The Salt Road", "--parent-id", "Part One")
		if r.data(t)["item"].(map[string]any)["parent_id"] != partOne {
			t.Errorf("%s", r.stdout)
		}
	})
}

// A key pinned to one project cannot list projects, but can still name it.
func TestPinnedKeyResolvesItsOwnProjectByName(t *testing.T) {
	e := newEnv(t, nil)
	pid, _, _ := seedBook(t, e)
	pinned := e.mint(e.userID, ops.RoleWriter, `{"name":"pinned","project_id":"`+pid+`"}`)
	r := e.runWith(pinned, "", "doc", "get", "The Salt Road/Part One/Chapter 3")
	if r.code != 0 {
		t.Fatalf("exit %d: %s", r.code, r.stderr)
	}
	r = e.runWith(pinned, "", "whoami")
	if r.code != 0 || r.data(t)["project"].(map[string]any)["title"] != "The Salt Road" {
		t.Errorf("whoami for a pinned key: %s", r.stdout)
	}
	if r := e.runWith(pinned, "", "project", "list"); r.code != cli.ExitAuth {
		t.Errorf("a pinned key listing projects: exit %d, want 5", r.code)
	}
}
