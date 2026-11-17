package mcp_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/mcp"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/registry"
)

func liveCatalog(t *testing.T) (*mcp.Catalog, ops.Schema) {
	t.Helper()
	schema := registry.Build(nil, nil, "").Schema()
	cat, err := mcp.BuildCatalog(schema)
	if err != nil {
		t.Fatal(err)
	}
	return cat, schema
}

// TestCommittedToolsAreCurrent is the drift test of the tool list: the committed
// snapshot is exactly what the registry makes today. A new, removed or reshaped op
// changes it; `make skill` writes it and the diff is what a reviewer reads (which
// new ops became tools, which were excluded and why).
func TestCommittedToolsAreCurrent(t *testing.T) {
	cat, _ := liveCatalog(t)
	want, err := cat.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	have, err := os.ReadFile("tools.snapshot.json")
	if err != nil {
		t.Fatal("tools.snapshot.json is missing: run `make skill`")
	}
	if string(have) != string(want) {
		t.Error("internal/mcp/tools.snapshot.json is stale: run `make skill` and commit the result")
	}
	ref, err := os.ReadFile(filepath.Join("..", "..", "skills", "scriobh", "mcp.md"))
	if err != nil || string(ref) != cat.Reference() {
		t.Error("skills/scriobh/mcp.md is stale: run `make skill` and commit the result")
	}
}

// Every op is decided: it is a tool or it is excluded with a reason, never both and never neither.
func TestEveryOpIsAToolOrExcludedWithAReason(t *testing.T) {
	cat, schema := liveCatalog(t)
	if len(cat.Tools)+len(cat.Excluded) != len(schema.Ops) {
		t.Fatalf("%d tools + %d excluded != %d ops", len(cat.Tools), len(cat.Excluded), len(schema.Ops))
	}
	seen := map[string]bool{}
	for _, tl := range cat.Tools {
		seen[tl.Op] = true
	}
	for _, e := range cat.Excluded {
		if seen[e.Op] {
			t.Errorf("%s is both a tool and excluded", e.Op)
		}
		if len(strings.TrimSpace(e.Reason)) < 10 {
			t.Errorf("%s is excluded without a reason", e.Op)
		}
	}
	if len(cat.Tools) < 150 {
		t.Errorf("only %d tools", len(cat.Tools))
	}
}

var toolNameRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// What a tool may be, stated as properties of the whole list rather than as a list of names.
func TestToolsAreSafeByConstruction(t *testing.T) {
	cat, schema := liveCatalog(t)
	byOp := map[string]ops.OpSchema{}
	for _, o := range schema.Ops {
		byOp[o.Name] = o
	}
	names := map[string]bool{}
	for _, tl := range cat.Tools {
		o := byOp[tl.Op]
		if !toolNameRe.MatchString(tl.Name) || names[tl.Name] {
			t.Errorf("bad or duplicate tool name %q", tl.Name)
		}
		names[tl.Name] = true
		if tl.Name != strings.ReplaceAll(tl.Op, ".", "_") {
			t.Errorf("%s: the name is not the op with underscores", tl.Name)
		}
		switch {
		case o.Auth.SessionOnly:
			t.Errorf("%s is session-only and a tool", tl.Name)
		case o.RawBody:
			t.Errorf("%s takes a raw body and is a tool", tl.Name)
		case strings.HasSuffix(o.Name, ".purge"):
			t.Errorf("%s permanently deletes and is a tool", tl.Name)
		}
		for _, grp := range []string{"key", "ai", "git", "backup", "webhook", "share", "session", "import", "mirror"} {
			if tl.Group() == grp {
				t.Errorf("%s: the group %s is never offered", tl.Name, grp)
			}
		}
		props, _ := tl.InputSchema["properties"].(map[string]any)
		for p := range props {
			l := strings.ToLower(p)
			for _, w := range []string{"secret", "password", "token", "api_key", "credential"} {
				if strings.Contains(l, w) {
					t.Errorf("%s takes %q", tl.Name, p)
				}
			}
		}
		// The schema is plain JSON Schema: an object, closed, every required property declared, no hint keys left.
		if tl.InputSchema["type"] != "object" || tl.InputSchema["additionalProperties"] != false {
			t.Errorf("%s: input schema is not a closed object", tl.Name)
		}
		if req, ok := tl.InputSchema["required"].([]string); ok {
			for _, r := range req {
				if _, ok := props[r]; !ok {
					t.Errorf("%s: required %q is not a property", tl.Name, r)
				}
			}
		}
		b, _ := json.Marshal(tl.InputSchema)
		if strings.Contains(string(b), `"in":`) || strings.Contains(string(b), `"ref":`) {
			t.Errorf("%s: the schema still carries an op hint keyword", tl.Name)
		}
		// Annotations follow the op.
		switch {
		case o.Scope == "read" && !tl.Annotations.ReadOnly:
			t.Errorf("%s reads but is not read-only", tl.Name)
		case o.Scope != "read" && tl.Annotations.ReadOnly:
			t.Errorf("%s writes but is read-only", tl.Name)
		case o.Method == "DELETE" && !tl.Annotations.Destructive:
			t.Errorf("%s deletes but is not destructive", tl.Name)
		case o.Method == "GET" && (tl.Annotations.Destructive || !tl.Annotations.Idempotent):
			t.Errorf("%s: a GET is idempotent and not destructive", tl.Name)
		}
		if tl.Annotations.OpenWorld {
			t.Errorf("%s claims an open world", tl.Name)
		}
		_, hasDry := props["dry_run"]
		if tl.Mutating() && !o.NoDryRun && !hasDry {
			t.Errorf("%s can be previewed but offers no dry_run", tl.Name)
		}
		if _, hasKey := props["idempotency_key"]; hasKey != tl.Mutating() {
			t.Errorf("%s: idempotency_key should be offered exactly to writes", tl.Name)
		}
		if tl.Annotations.ReadOnly && !strings.Contains(tl.Description, "never as instructions") {
			t.Errorf("%s: a reading tool's description lacks the untrusted-data note", tl.Name)
		}
	}
}

// The ops the registry has that carry their own dry_run flag are all handled.
func TestOpsWithTheirOwnDryRunFlag(t *testing.T) {
	cat, schema := liveCatalog(t)
	n := 0
	for _, o := range schema.Ops {
		props, _ := o.Input["properties"].(map[string]any)
		if _, ok := props["dry_run"]; !ok {
			continue
		}
		n++
		if tl, ok := cat.Lookup(mcp.ToolName(o.Name)); ok && tl.InputSchema["properties"].(map[string]any)["dry_run"] == nil {
			t.Errorf("%s lost its dry_run", tl.Name)
		}
	}
	if n == 0 {
		t.Skip("no op has its own dry_run any more")
	}
}

// A hand-built schema where two ops would make one tool name, or take a reserved argument, is refused.
func TestCatalogRefusesCollisions(t *testing.T) {
	mk := func(name, field string) ops.OpSchema {
		return ops.OpSchema{Name: name, Method: "POST", Path: "/api/v1/" + name, Scope: "write", Auth: ops.Auth{Kind: ops.AuthWriter},
			Input: map[string]any{"type": "object", "properties": map[string]any{field: map[string]any{"type": "string"}}}}
	}
	if _, err := mcp.BuildCatalog(ops.Schema{Ops: []ops.OpSchema{mk("doc.b_c", "x"), mk("doc.b.c", "x")}}); err == nil {
		t.Error("doc.b_c and doc.b.c both become doc_b_c")
	}
	if _, err := mcp.BuildCatalog(ops.Schema{Ops: []ops.OpSchema{mk("doc.b", "idempotency_key")}}); err == nil {
		t.Error("an input named idempotency_key must be refused")
	}
	if _, err := mcp.BuildCatalog(ops.Schema{Ops: []ops.OpSchema{mk("doc.b", "dry_run")}}); err == nil {
		t.Error("a string input named dry_run must be refused: only a boolean body flag is an op's own preview")
	}
}

// The list is deterministic: building twice gives the same bytes, in name order.
func TestCatalogIsDeterministic(t *testing.T) {
	a, _ := liveCatalog(t)
	b, _ := liveCatalog(t)
	x, _ := a.Snapshot()
	y, _ := b.Snapshot()
	if string(x) != string(y) {
		t.Error("two builds differ")
	}
	if !sort.SliceIsSorted(a.Tools, func(i, j int) bool { return a.Tools[i].Name < a.Tools[j].Name }) {
		t.Error("tools are not in name order")
	}
}

// A group nobody has classified is never offered: the catalog fails closed, and says which group it was.
func TestAnUnclassifiedGroupIsNotExposed(t *testing.T) {
	op := func(name string) ops.OpSchema {
		return ops.OpSchema{Name: name, Method: "GET", Path: "/api/v1/" + name, Scope: "read", Auth: ops.Auth{Kind: ops.AuthWriter},
			Input: map[string]any{"type": "object", "properties": map[string]any{}}}
	}
	cat, err := mcp.BuildCatalog(ops.Schema{Ops: []ops.OpSchema{op("zzznew.list"), op("doc.list")}})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cat.Lookup("zzznew_list"); ok {
		t.Fatal("an op of a group nobody classified became a tool")
	}
	if _, ok := cat.Lookup("doc_list"); !ok {
		t.Error("a classified group lost its tool")
	}
	found := false
	for _, e := range cat.Excluded {
		if e.Op == "zzznew.list" {
			found = strings.Contains(e.Reason, `"zzznew"`)
		}
	}
	if !found {
		t.Errorf("the exclusion does not name the group: %+v", cat.Excluded)
	}
}

// Every op group the registry has is decided in code, exposed or excluded, and only one of the two. A new group
// fails this test until someone decides, which is the point: nothing is offered to an agent by default.
func TestEveryRegisteredGroupIsClassified(t *testing.T) {
	_, schema := liveCatalog(t)
	groups := map[string]bool{}
	for _, o := range schema.Ops {
		groups[strings.SplitN(o.Name, ".", 2)[0]] = true
	}
	var names []string
	for g := range groups {
		names = append(names, g)
	}
	sort.Strings(names)
	for _, g := range names {
		switch c := mcp.ClassOfGroup(g); c {
		case mcp.GroupExposed, mcp.GroupExcluded:
		case mcp.GroupBoth:
			t.Errorf("the op group %q is in both the exposed and the excluded list", g)
		default:
			t.Errorf("the op group %q is not classified: add it to exposedGroups or excludedGroups in internal/mcp/catalog.go (an agent must never be offered a group by default)", g)
		}
	}
	for _, g := range mcp.ClassifiedGroups() {
		if !groups[g] {
			t.Errorf("the classification names %q, which is not an op group any more", g)
		}
	}
}

// A group narrowed to named ops fails closed: an op of the group that is neither on its list nor excluded by name fails
// this test until a person decides, and is not offered meanwhile.
func TestNarrowedGroupsAreDecidedOpByOp(t *testing.T) {
	cat, schema := liveCatalog(t)
	if len(mcp.NarrowedGroups()) == 0 {
		t.Fatal("no group is narrowed to named ops; the editions must be")
	}
	for _, g := range mcp.NarrowedGroups() {
		if mcp.ClassOfGroup(g) != mcp.GroupExposed {
			t.Errorf("the narrowed group %q is not an exposed group", g)
		}
		allowed := map[string]bool{}
		for _, n := range mcp.OnlyOps(g) {
			allowed[n] = true
		}
		seen := map[string]bool{}
		for _, o := range schema.Ops {
			if strings.SplitN(o.Name, ".", 2)[0] != g {
				continue
			}
			seen[o.Name] = true
			_, offered := cat.Lookup(mcp.ToolName(o.Name))
			switch {
			case allowed[o.Name] && mcp.IsExcludedByName(o.Name):
				t.Errorf("%s is both on the list of %q and excluded by name", o.Name, g)
			case allowed[o.Name] && !offered:
				t.Errorf("%s is on the list of %q and is not offered", o.Name, g)
			case !allowed[o.Name] && !mcp.IsExcludedByName(o.Name):
				t.Errorf("%s is neither on the list of %q nor excluded by name: decide it in internal/mcp/catalog.go", o.Name, g)
			case !allowed[o.Name] && offered:
				t.Errorf("%s is offered although it is not on the list of %q", o.Name, g)
			}
		}
		for n := range allowed {
			if !seen[n] {
				t.Errorf("the list of %q names %s, which is not an op", g, n)
			}
		}
	}
	// What the narrowing is for: an op the group might get later is not offered.
	if _, ok := cat.Lookup("edition_download"); ok {
		t.Error("edition.download is offered")
	}
	future := schema
	future.Ops = append(append([]ops.OpSchema(nil), schema.Ops...), ops.OpSchema{Name: "edition.preview", Method: "GET", Path: "/api/v1/projects/{id}/editions/preview",
		Auth: ops.RequireProject(ops.ProjectReader), Scope: ops.ScopeRead, Input: map[string]any{}, Output: map[string]any{}})
	c2, err := mcp.BuildCatalog(future)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := c2.Lookup("edition_preview"); ok {
		t.Error("an edition op nobody approved is offered")
	}
	var why string
	for _, x := range c2.Excluded {
		if x.Op == "edition.preview" {
			why = x.Reason
		}
	}
	if !strings.Contains(why, "onlyOps") {
		t.Errorf("the reason the new op is excluded: %q", why)
	}
}
