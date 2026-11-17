package main

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// The Phase 3 exit criterion "an agent with a scoped key creates a compendium,
// adds twenty entities, and drafts a chapter through the CLI", with the real
// server and the real slim CLI. The one step that needs a person is minting the
// key (and making the project it is restricted to); the key is restricted to
// that one project, which is how it can reach the book's Compendium too: the
// agent makes it with `project compendium create`, then fills it.
func TestAgentCLIScopedKeyBuildsABook(t *testing.T) {
	server := buildBinary(t, "")
	cliBin := buildCLI(t)
	p := start(t, server, t.TempDir())
	c := client()
	signIn(t, c, p.base, "dev-writer")
	post := func(path, payload string) map[string]any {
		t.Helper()
		req, _ := http.NewRequest("POST", p.base+path, strings.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		resp, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		raw := body(t, resp)
		if resp.StatusCode >= 300 {
			t.Fatalf("POST %s: %d %s", path, resp.StatusCode, raw)
		}
		return jsonOf(t, raw)
	}
	pid := post("/api/v1/projects", `{"title":"The Salt Road","kind":"novel"}`)["item"].(map[string]any)["id"].(string)
	post("/api/v1/projects", `{"title":"Someone Else's Book"}`) // a second project the key must not reach
	secret := post("/api/v1/keys", `{"name":"book agent","scope":"write","project_id":"`+pid+`","expires_in_days":1}`)["secret"].(string)
	cli := cliRun{bin: cliBin, url: p.base, key: secret, cfg: t.TempDir(), cache: t.TempDir()}
	ok := func(stdin string, args ...string) map[string]any {
		t.Helper()
		code, out, errs := cli.run(t, stdin, args...)
		if code != 0 {
			t.Fatalf("%s: exit %d\n%s\n%s", strings.Join(args, " "), code, out, errs)
		}
		return jsonOf(t, out)["data"].(map[string]any)
	}
	exit := func(args ...string) (int, map[string]any) {
		t.Helper()
		code, _, errs := cli.run(t, "", args...)
		if errs == "" {
			return code, nil
		}
		return code, jsonOf(t, errs)
	}

	// The key knows its project and, until there is one, no Compendium.
	if who := ok("", "whoami"); who["project"] == nil || who["compendium"] != nil {
		t.Fatalf("whoami: %v", who)
	}
	// It cannot make a Compendium of its own, only one for its project.
	if code, f := exit("compendium", "create", "--name", "Free floating"); code != 5 || f["error"].(map[string]any)["code"] != "forbidden" {
		t.Errorf("compendium create with a project key: exit %d %v", code, f)
	}
	ok("", "project", "compendium", "create", "The Salt Road", "--name", "The Salt Road Bible", "--dry-run")
	if who := ok("", "whoami"); who["compendium"] != nil {
		t.Fatal("the dry run made a Compendium")
	}
	ok("", "project", "compendium", "create", "The Salt Road", "--name", "The Salt Road Bible")
	who := ok("", "whoami")
	if who["compendium"].(map[string]any)["name"] != "The Salt Road Bible" {
		t.Fatalf("whoami after: %v", who)
	}

	// Twenty entities, the first with aliases and a field, named in the commands.
	ok("", "entity", "create", "The Salt Road Bible", "--kind", "character", "--name", "Máire Ní Bhriain",
		"--aliases", "Máire", "--fields", `{"age": 34, "role": "keeper of the harbour light"}`)
	for i := 2; i <= 20; i++ {
		kind := []string{"character", "place", "item", "org", "concept"}[i%5]
		ok("", "entity", "create", "The Salt Road Bible", "--kind", kind, "--name", fmt.Sprintf("Entity %02d", i), "--summary", "Added by the agent.")
	}
	list := ok("", "entity", "list", "The Salt Road Bible")
	if list["total"].(float64) != 20 {
		t.Fatalf("entities: %v", list["total"])
	}
	// Read one back by name: with the Compendium said, and without (the key has only one).
	for _, args := range [][]string{
		{"entity", "get", "Máire Ní Bhriain", "--compendium", "The Salt Road Bible"},
		{"entity", "get", "Máire"},
	} {
		got := ok("", args...)["item"].(map[string]any)
		if got["name"] != "Máire Ní Bhriain" || got["fields"].(map[string]any)["age"] != "34" {
			t.Errorf("%v: %v", args, got)
		}
	}
	ok("", "search", "compendium", "The Salt Road Bible", "--q", "harbour")
	// A name already used is refused, in a way an agent can read.
	if code, f := exit("entity", "create", "The Salt Road Bible", "--kind", "place", "--name", "máire"); code != 4 || f["error"].(map[string]any)["code"] != "conflict" {
		t.Errorf("a clashing alias: exit %d %v", code, f)
	}

	// And a chapter of the book itself, from standard input.
	ok("Máire walked down to the harbour before the light came up.\n", "doc", "create", "The Salt Road", "--title", "The Harbour at Low Tide", "--kind", "chapter", "--content", "-")
	ch := ok("", "doc", "get", "The Salt Road/The Harbour at Low Tide")["item"].(map[string]any)
	if !strings.Contains(ch["content"].(string), "harbour") {
		t.Errorf("chapter: %v", ch)
	}

	// Nothing else is within reach: not the other book, not the list of books or Compendiums.
	for _, args := range [][]string{{"project", "list"}, {"compendium", "list"}, {"project", "get", "Someone Else's Book"}} {
		if code, _ := exit(args...); code == 0 {
			t.Errorf("%v succeeded with a project key", args)
		}
	}
	// The mirror holds both: the chapter in the book, the bible in its compendium/ folder.
	code, out, errs := cli.run(t, "", "mirror", "status", "The Salt Road")
	if code != 0 {
		t.Fatalf("mirror status: %d %s %s", code, out, errs)
	}
}
