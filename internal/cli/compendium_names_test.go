package cli_test

import (
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/cli"
	"github.com/crueber/scriobh/internal/ops"
)

// Compendiums and entities are addressed by name with no CLI code of their own:
// compendium.list and entity.list are the list ops the resolver finds in the
// schema, an entity is looked up inside the Compendium named by --compendium,
// and an alias is as good as a name.
func TestCompendiumAndEntityNamesResolve(t *testing.T) {
	e := newEnv(t, nil)
	cid := itemID(t, e.ok("compendium", "create", "--name", "The Salt Road Bible"))
	itemID(t, e.ok("compendium", "create", "--name", "Another World"))
	maire := itemID(t, e.ok("entity", "create", "The Salt Road Bible", "--kind", "character", "--name", "Máire Ní Bhriain",
		"--aliases", "Máire", "--aliases", "Mrs Nolan", "--fields", `{"age": 34}`))
	e.ok("entity", "create", "The Salt Road Bible", "--kind", "place", "--name", "The Harbour")
	// The same name in the other Compendium is a different entity.
	other := itemID(t, e.ok("entity", "create", "Another World", "--kind", "character", "--name", "Máire Ní Bhriain"))

	t.Run("the example from the brief", func(t *testing.T) {
		r := e.ok("entity", "get", "Máire Ní Bhriain", "--compendium", "The Salt Road Bible")
		item := r.data(t)["item"].(map[string]any)
		if item["id"] != maire || item["compendium_id"] != cid {
			t.Errorf("%s", r.stdout)
		}
		if f := item["fields"].(map[string]any); f["age"] != "34" {
			t.Errorf("fields: %v", f)
		}
	})
	t.Run("by alias, and ignoring case and spacing", func(t *testing.T) {
		for _, name := range []string{"Máire", "mrs  NOLAN", "máire ní bhriain"} {
			if got := itemID(t, e.ok("entity", "get", name, "--compendium", "the salt road bible")); got != maire {
				t.Errorf("%q resolved to %s", name, got)
			}
		}
	})
	t.Run("the same name in two Compendiums needs the Compendium", func(t *testing.T) {
		if got := itemID(t, e.ok("entity", "get", "Máire Ní Bhriain", "--compendium", "Another World")); got != other {
			t.Errorf("resolved to %s", got)
		}
		r := e.run("entity", "get", "Máire Ní Bhriain")
		if r.code != cli.ExitUsage {
			t.Fatalf("exit %d, want 2: %s", r.code, r.stderr)
		}
		if f := r.fail(t); !strings.Contains(f["message"].(string), "which compendium") {
			t.Errorf("%v", f)
		}
	})
	t.Run("a Compendium by name, and an unknown one with candidates", func(t *testing.T) {
		if got := itemID(t, e.ok("compendium", "get", "The Salt Road Bible")); got != cid {
			t.Errorf("compendium: %s", got)
		}
		r := e.run("compendium", "get", "Salt Road")
		if r.code != cli.ExitNotFound {
			t.Fatalf("exit %d: %s", r.code, r.stderr)
		}
		f := r.fail(t)
		if cands, _ := f["candidates"].([]any); len(cands) != 1 {
			t.Errorf("candidates: %v", f)
		}
	})
	t.Run("names in a body field", func(t *testing.T) {
		harbour := itemID(t, e.ok("entity", "get", "The Harbour", "--compendium", "The Salt Road Bible"))
		e.ok("entity", "relation", "add", "Máire", "--to-entity-id", "The Harbour", "--compendium", "The Salt Road Bible", "--kind", "member", "--label", "lives at")
		rels := e.ok("entity", "relation", "list", "Máire", "--compendium", "The Salt Road Bible").data(t)["items"].([]any)
		if len(rels) != 1 || rels[0].(map[string]any)["other_id"] != harbour {
			t.Errorf("relation: %v", rels)
		}
	})
	t.Run("a merge previews with names on both sides", func(t *testing.T) {
		e.ok("entity", "create", "The Salt Road Bible", "--kind", "place", "--name", "Quay")
		r := e.ok("entity", "merge", "Quay", "--into-id", "The Harbour", "--compendium", "The Salt Road Bible", "--dry-run")
		if r.envelope(t)["meta"].(map[string]any)["dry_run"] != true {
			t.Errorf("%s", r.stdout)
		}
		e.ok("entity", "get", "Quay", "--compendium", "The Salt Road Bible") // still there
	})
}

// A key restricted to one Compendium works in it by name, with no --compendium
// and with a list op it is not allowed to call.
func TestCompendiumPinnedKeyResolvesInsideItsCompendium(t *testing.T) {
	e := newEnv(t, nil)
	cid := itemID(t, e.ok("compendium", "create", "--name", "The Salt Road Bible"))
	maire := itemID(t, e.ok("entity", "create", "The Salt Road Bible", "--kind", "character", "--name", "Máire Ní Bhriain", "--aliases", "Máire"))
	pinned := e.mint(e.userID, ops.RoleWriter, `{"name":"pinned","scope":"write","compendium_id":"`+cid+`"}`)
	r := e.runWith(pinned, "", "entity", "get", "Máire")
	if r.code != 0 || itemID(t, r) != maire {
		t.Fatalf("exit %d: %s %s", r.code, r.stdout, r.stderr)
	}
	r = e.runWith(pinned, "", "entity", "create", "The Salt Road Bible", "--kind", "place", "--name", "The Harbour")
	if r.code != 0 {
		t.Fatalf("exit %d: %s %s", r.code, r.stdout, r.stderr)
	}
	for _, args := range [][]string{{"project", "list"}, {"compendium", "list"}, {"compendium", "create", "--name", "Mine"}} {
		if r := e.runWith(pinned, "", args...); r.code != cli.ExitAuth {
			t.Errorf("%v: exit %d, want %d", args, r.code, cli.ExitAuth)
		}
	}
}
