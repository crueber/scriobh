package server

import (
	"encoding/json"
	"testing"

	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/testutil"
)

// compendiumFixtures drives every Compendium, entity, member, mirror and export
// op through the dry-run sweep: each is a preview that changes no row and no
// file, or declares that it cannot be one (mirror.compendium.rebuild writes
// files). They need a Compendium with entities in several states, a few
// people, and a project to wire it to.
func (f *dryFixture) compendiumFixtures(t *testing.T) map[string]drive {
	t.Helper()
	post := func(path, body string) map[string]any {
		t.Helper()
		rec := f.srv.do(f.owner, "POST", path, body)
		if rec.Code >= 300 {
			t.Fatalf("fixture POST %s: %d %s", path, rec.Code, rec.Body)
		}
		return decodeObj(t, rec)
	}
	idOf := func(m map[string]any) string { return m["item"].(map[string]any)["id"].(string) }
	cmp := idOf(post("/api/v1/compendiums", `{"name":"Dry Bible"}`))
	other := idOf(post("/api/v1/compendiums", `{"name":"Dry Cosmos"}`))
	archived := idOf(post("/api/v1/compendiums", `{"name":"Dry Archived Bible"}`))
	post("/api/v1/compendiums/"+archived+"/archive", "")
	trashed := idOf(post("/api/v1/compendiums", `{"name":"Dry Trashed Bible"}`))
	post("/api/v1/compendiums/"+trashed+"/trash", "")
	ada := idOf(post("/api/v1/compendiums/"+cmp+"/entities", `{"kind":"character","name":"Ada","aliases":["Addie"]}`))
	bea := idOf(post("/api/v1/compendiums/"+cmp+"/entities", `{"kind":"character","name":"Bea"}`))
	cal := idOf(post("/api/v1/compendiums/"+cmp+"/entities", `{"kind":"character","name":"Cal"}`))
	moon := idOf(post("/api/v1/compendiums/"+other+"/entities", `{"kind":"place","name":"The Moon"}`))
	gone := idOf(post("/api/v1/compendiums/"+cmp+"/entities", `{"kind":"item","name":"Lost key"}`))
	post("/api/v1/entities/"+gone+"/trash", "")
	// A person to add, and one already on the Compendium to change and remove.
	testutil.User(t, f.srv.DB, ops.RoleWriter, "dry-new@test.local")
	member := testutil.User(t, f.srv.DB, ops.RoleWriter, "dry-member@test.local")
	post("/api/v1/compendiums/"+cmp+"/members", `{"email":"dry-member@test.local","role":"reader"}`)
	post("/api/v1/projects/"+f.project+"/members", `{"email":"dry-member@test.local","role":"reader"}`)
	// An alias, a relationship, a field, a reference, a link and an override to remove.
	var alias struct{ Alias struct{ ID string } }
	_ = json.Unmarshal([]byte(mustBody(t, post("/api/v1/entities/"+ada+"/aliases", `{"alias":"Dry alias"}`))), &alias)
	var rel struct{ Item struct{ ID string } }
	_ = json.Unmarshal([]byte(mustBody(t, post("/api/v1/entities/"+ada+"/relations", `{"to_entity_id":"`+bea+`","kind":"ally"}`))), &rel)
	if rec := f.srv.do(f.owner, "PUT", "/api/v1/compendiums/"+cmp+"/entity-fields", `{"kind":"item","key":"weight","type":"number"}`); rec.Code >= 300 {
		t.Fatalf("fixture field: %d %s", rec.Code, rec.Body)
	}
	post("/api/v1/compendiums/"+cmp+"/refs", `{"target_id":"`+other+`"}`)
	// The project's own wiring: a Compendium it can reference and a cameo to unlink.
	post("/api/v1/projects/"+f.archivedProject+"/compendium/refs", `{"compendium_id":"`+other+`"}`)
	cameoBible := idOf(post("/api/v1/compendiums", `{"name":"Dry Cameos"}`))
	cameo := idOf(post("/api/v1/compendiums/"+cameoBible+"/entities", `{"kind":"character","name":"Dry Stranger"}`))
	post("/api/v1/projects/"+f.archivedProject+"/entity-links", `{"entity_id":"`+cameo+`"}`)
	rec := f.srv.do(f.owner, "PUT", "/api/v1/projects/"+f.archivedProject+"/entity-overrides", `{"entity_id":"`+moon+`","name":"Luna"}`)
	if rec.Code >= 300 {
		t.Fatalf("fixture override: %d %s", rec.Code, rec.Body)
	}
	set := func(path, body string) {
		if rec := f.srv.do(f.owner, "PUT", path, body); rec.Code >= 300 {
			t.Fatalf("fixture PUT %s: %d %s", path, rec.Code, rec.Body)
		}
	}
	set("/api/v1/projects/"+f.project+"/compendium", `{"compendium_id":"`+cmp+`"}`)

	u := "/api/v1"
	m := map[string]drive{
		"compendium.create":             {u + "/compendiums", `{"name":"Previewed Bible"}`, true},
		"project.compendium.create":     {u + "/projects/" + f.archivedProject + "/compendium", `{"name":"Previewed Project Bible"}`, true},
		"compendium.update":             {u + "/compendiums/" + cmp, `{"name":"Dry Bible Renamed"}`, true},
		"compendium.archive":            {u + "/compendiums/" + cmp + "/archive", "", true},
		"compendium.unarchive":          {u + "/compendiums/" + archived + "/unarchive", "", true},
		"compendium.trash":              {u + "/compendiums/" + other + "/trash", "", true},
		"compendium.restore":            {u + "/compendiums/" + trashed + "/restore", "", true},
		"compendium.purge":              {u + "/compendiums/" + trashed, `{"confirm_name":"Dry Trashed Bible"}`, true},
		"compendium.member.add":         {u + "/compendiums/" + cmp + "/members", `{"email":"dry-new@test.local","role":"reader"}`, true},
		"compendium.member.update":      {u + "/compendiums/" + cmp + "/members", `{"user_id":"` + member + `","role":"editor"}`, false},
		"compendium.member.remove":      {u + "/compendiums/" + cmp + "/members?user_id=" + member, "", true},
		"compendium.ref.add":            {u + "/compendiums/" + archived + "/refs", `{"target_id":"` + other + `"}`, true},
		"compendium.ref.remove":         {u + "/compendiums/" + cmp + "/refs?target_id=" + other, "", true},
		"compendium.export":             {u + "/compendiums/" + cmp + "/export", "", true},
		"mirror.compendium.rebuild":     {u + "/compendiums/" + cmp + "/mirror/rebuild", "", false},
		"entity.create":                 {u + "/compendiums/" + cmp + "/entities", `{"kind":"place","name":"Previewed place","aliases":["Prev"]}`, true},
		"entity.update":                 {u + "/entities/" + ada, `{"summary":"Previewed summary"}`, true},
		"entity.trash":                  {u + "/entities/" + bea + "/trash", "", true},
		"entity.restore":                {u + "/entities/" + gone + "/restore", "", true},
		"entity.alias.add":              {u + "/entities/" + ada + "/aliases", `{"alias":"Previewed alias"}`, true},
		"entity.alias.remove":           {u + "/entities/" + ada + "/aliases?alias_id=" + alias.Alias.ID, "", true},
		"entity.relation.add":           {u + "/entities/" + bea + "/relations", `{"to_entity_id":"` + cal + `","kind":"rival","symmetric":true}`, true},
		"entity.relation.remove":        {u + "/entities/" + ada + "/relations?relation_id=" + rel.Item.ID, "", true},
		"entity.field.set":              {u + "/compendiums/" + cmp + "/entity-fields", `{"kind":"place","key":"climate","type":"text"}`, true},
		"entity.field.remove":           {u + "/compendiums/" + cmp + "/entity-fields?kind=item&key=weight", "", true},
		"entity.merge":                  {u + "/entities/" + cal + "/merge", `{"into_id":"` + bea + `"}`, true},
		"project.compendium.set":        {u + "/projects/" + f.archivedProject + "/compendium", `{"compendium_id":"` + cmp + `"}`, true},
		"project.compendium.ref.add":    {u + "/projects/" + f.project + "/compendium/refs", `{"compendium_id":"` + other + `"}`, true},
		"project.compendium.ref.remove": {u + "/projects/" + f.archivedProject + "/compendium/refs?compendium_id=" + other, "", true},
		"project.entity.override.set":   {u + "/projects/" + f.project + "/entity-overrides", `{"entity_id":"` + ada + `","name":"Ada Q"}`, true},
		"project.entity.override.clear": {u + "/projects/" + f.archivedProject + "/entity-overrides?entity_id=" + moon, "", true},
		"project.entity.link.add":       {u + "/projects/" + f.project + "/entity-links", `{"entity_id":"` + moon + `"}`, true},
		"project.entity.link.remove":    {u + "/projects/" + f.archivedProject + "/entity-links?entity_id=" + cameo, "", true},
		"project.member.add":            {u + "/projects/" + f.project + "/members", `{"email":"dry-new@test.local","role":"reviewer"}`, true},
		"project.member.update":         {u + "/projects/" + f.project + "/members", `{"user_id":"` + member + `","role":"editor"}`, true},
		"project.member.remove":         {u + "/projects/" + f.project + "/members?user_id=" + member, "", true},
	}
	for k, v := range f.timelineFixtures(t, cmp, ada) {
		m[k] = v
	}
	return m
}

func mustBody(t *testing.T, m map[string]any) string {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Two ops carry a preview of their own (entity.merge and project.compendium.set
// take dry_run in the body). Under the global preview they open a read
// transaction on the preview's view of the database; neither may change a row.
func TestOpsWithTheirOwnPreviewStayPreviews(t *testing.T) {
	s := newAuthzServer(t)
	f := newDryFixture(t, s)
	post := func(path, body string) string {
		t.Helper()
		rec := s.do(f.owner, "POST", path, body)
		if rec.Code >= 300 {
			t.Fatalf("POST %s: %d %s", path, rec.Code, rec.Body)
		}
		return idFromItem(t, rec.Body.Bytes())
	}
	cmp := post("/api/v1/compendiums", `{"name":"Nested Bible"}`)
	a := post("/api/v1/compendiums/"+cmp+"/entities", `{"kind":"character","name":"Ada"}`)
	b := post("/api/v1/compendiums/"+cmp+"/entities", `{"kind":"character","name":"Bea"}`)
	for _, c := range []struct{ method, path, body string }{
		{"POST", "/api/v1/entities/" + a + "/merge", `{"into_id":"` + b + `","dry_run":true}`},
		{"PUT", "/api/v1/projects/" + f.project + "/compendium", `{"compendium_id":"` + cmp + `","dry_run":true}`},
	} {
		before := testutil.Dump(t, s.DB)
		rec := s.doDry(f.owner, c.method, c.path, c.body)
		if rec.Code >= 300 {
			t.Fatalf("%s %s: %d %s", c.method, c.path, rec.Code, rec.Body)
		}
		if m := decodeObj(t, rec); m["dry_run"] != true {
			t.Errorf("%s: %s", c.path, rec.Body)
		}
		if testutil.Dump(t, s.DB) != before {
			t.Errorf("%s %s changed the database", c.method, c.path)
		}
	}
}

func idFromItem(t *testing.T, raw []byte) string {
	t.Helper()
	var out struct{ Item struct{ ID string } }
	if err := json.Unmarshal(raw, &out); err != nil || out.Item.ID == "" {
		t.Fatalf("no id in %s", raw)
	}
	return out.Item.ID
}
