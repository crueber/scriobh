package imports_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/compendiums"
	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/entities"
	"github.com/crueber/scriobh/internal/domain/mentions"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/imports"
)

// Adding files to an existing project that has a bible: a manifest's entity
// references are believed only when the entity is in the project's scope and
// the importer may read it, a point of view must be a character, the mention
// scan is queued, and an importer outside the Compendium links nothing.
func TestAppendIntoAProjectWithABible(t *testing.T) {
	e := newEnv(t)
	cmp := e.must(e.w, "compendium.create", compendiums.CreateIn{Name: "Bible"}).(compendiums.Item).Item.ID
	elara := e.must(e.w, "entity.create", entities.CreateIn{ID: cmp, Kind: "character", Name: "Elara"}).(entities.Item).Item.ID
	port := e.must(e.w, "entity.create", entities.CreateIn{ID: cmp, Kind: "place", Name: "Port Salt"}).(entities.Item).Item.ID
	elsewhere := e.must(e.w, "compendium.create", compendiums.CreateIn{Name: "Elsewhere"}).(compendiums.Item).Item.ID
	alien := e.must(e.w, "entity.create", entities.CreateIn{ID: elsewhere, Kind: "character", Name: "Alien"}).(entities.Item).Item.ID
	pid := e.must(e.w, "project.create", projects.CreateIn{Title: "Existing"}).(projects.Item).Item.ID
	e.must(e.w, "project.compendium.set", compendiums.SetIn{ID: pid, CompendiumID: cmp})

	manifest := `{"schema_version":3,"project":{"title":"P"},"documents":[
		{"path":"manuscript/01-a.md","kind":"scene","title":"In scope","status":"draft","pov":"Elara","pov_entity":{"id":"` + elara + `","name":"Elara"},"location":"Port Salt","location_entity":{"id":"` + port + `"}},
		{"path":"manuscript/02-b.md","kind":"scene","title":"Out of scope","status":"draft","pov":"Alien","pov_entity":{"id":"` + alien + `"}},
		{"path":"manuscript/03-c.md","kind":"scene","title":"Wrong kind","status":"draft","pov":"Port Salt","pov_entity":{"id":"` + port + `"}}]}`
	f := files(map[string]string{
		"project.json":       manifest,
		"manuscript/01-a.md": "Elara came to Port Salt.\n",
		"manuscript/02-b.md": "Alien arrives.\n",
		"manuscript/03-c.md": "Nothing.\n",
	})
	appendWith := func(who, project string) []docs.OutlineItem {
		t.Helper()
		pv := e.must(e.w, "import.append_preview", imports.AppendPreviewIn{ProjectID: project, Files: f}).(imports.PreviewOut)
		e.must(e.w, "import.append", imports.AppendIn{ProjectID: project, Token: pv.Token, Files: f})
		return e.must(e.w, "outline.get", docs.OutlineIn{ProjectID: project}).(docs.OutlineOut).Items
	}
	byTitle := func(items []docs.OutlineItem) map[string]docs.OutlineItem {
		m := map[string]docs.OutlineItem{}
		for _, it := range items {
			m[it.Title] = it
		}
		return m
	}
	got := byTitle(appendWith("writer", pid))
	if a := got["In scope"]; a.PovEntity == nil || a.PovEntity.ID != elara || a.LocationEntity == nil || a.LocationEntity.ID != port || a.Pov != "Elara" {
		t.Errorf("in scope and readable: %+v", a)
	}
	if b := got["Out of scope"]; b.PovEntity != nil || b.Pov != "Alien" {
		t.Errorf("an id outside the project's scope was believed: %+v", b)
	}
	if c := got["Wrong kind"]; c.PovEntity != nil || c.Pov != "Port Salt" {
		t.Errorf("a place as a point of view: %+v", c)
	}
	// The mention scan is queued by the import and finds the entities in the new text.
	var owed int
	if err := e.d.Read().QueryRow(`SELECT count(*) FROM mention_state WHERE project_id = ? AND stamp <> indexed_stamp`, pid).Scan(&owed); err != nil || owed != 1 {
		t.Errorf("owed %d %v", owed, err)
	}
	if _, err := mentions.Settle(context.Background(), e.d); err != nil {
		t.Fatal(err)
	}
	var rows int
	if err := e.d.Read().QueryRow(`SELECT count(*) FROM entity_mentions WHERE project_id = ?`, pid).Scan(&rows); err != nil || rows != 2 {
		t.Errorf("mention rows after the append: %d %v", rows, err)
	}

	// Someone who edits the project but is not on the Compendium links nothing.
	other := e.user("not-on-the-bible", "writer")
	if err := e.d.WithTx(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO project_members(project_id, user_id, role, created_at) VALUES (?,?,?,?)`, pid, other.UserID, "editor", db.Now())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	pv := e.must(other, "import.append_preview", imports.AppendPreviewIn{ProjectID: pid, Files: f}).(imports.PreviewOut)
	e.must(other, "import.append", imports.AppendIn{ProjectID: pid, Token: pv.Token, Files: f})
	linked := 0
	for _, it := range e.must(other, "outline.get", docs.OutlineIn{ProjectID: pid}).(docs.OutlineOut).Items {
		if it.PovEntity != nil || it.LocationEntity != nil {
			linked++
		}
	}
	if linked != 0 {
		t.Errorf("an editor outside the Compendium sees %d linked scenes", linked)
	}
	var rowsLinked int
	if err := e.d.Read().QueryRow(`SELECT count(*) FROM documents WHERE project_id = ? AND title = 'In scope' AND pov_entity_id IS NOT NULL`, pid).Scan(&rowsLinked); err != nil || rowsLinked != 1 {
		t.Errorf("the second append linked scenes of its own: %d %v (only the first, by the person on the bible, should be)", rowsLinked, err)
	}
}
