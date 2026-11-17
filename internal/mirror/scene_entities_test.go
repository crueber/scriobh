package mirror_test

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/domain/compendiums"
	"github.com/crueber/scriobh/internal/domain/entities"
	"github.com/crueber/scriobh/internal/domain/scenes"
)

type refJSON struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// sceneRefs reads the entity references of the document with the given id out of project.json.
func sceneRefs(t *testing.T, root, docID string) (pov, loc *refJSON) {
	t.Helper()
	var m struct {
		Documents []struct {
			ID             string   `json:"id"`
			PovEntity      *refJSON `json:"pov_entity"`
			LocationEntity *refJSON `json:"location_entity"`
		} `json:"documents"`
	}
	if err := json.Unmarshal([]byte(read(t, filepath.Join(root, "project.json"))), &m); err != nil {
		t.Fatal(err)
	}
	for _, d := range m.Documents {
		if d.ID == docID {
			return d.PovEntity, d.LocationEntity
		}
	}
	t.Fatalf("%s is not in the manifest", docID)
	return nil, nil
}

// A scene linked to an entity names it in project.json (the id and the book's
// name for it), the text stays as the fallback, and a rename of the entity, or
// an override of its name for the book, rewrites the manifest.
func TestManifestNamesTheScenesEntities(t *testing.T) {
	e := newEnv(t)
	e.start()
	pid := e.project("Book")
	cid := e.compendium("World")
	maire := e.entity(cid, "character", "Máire Nolan")
	harbour := e.entity(cid, "place", "Port Salt")
	e.call(e.owner, "project.compendium.set", compendiums.SetIn{ID: pid, CompendiumID: cid})
	d := e.outlineDoc(pid)
	e.settleAll()
	if raw := read(t, filepath.Join(e.root(pid), "project.json")); strings.Contains(raw, "pov_entity") {
		t.Fatalf("an unlinked scene names no entity: %s", raw)
	}
	e.call(e.owner, "scene.set_pov", scenes.SetIn{ID: d.ID, Entity: maire})
	e.call(e.owner, "scene.set_location", scenes.SetIn{ID: d.ID, Entity: harbour})
	e.settleAll()
	pov, loc := sceneRefs(t, e.root(pid), d.ID)
	if pov == nil || pov.ID != maire || pov.Name != "Máire Nolan" || loc == nil || loc.ID != harbour || loc.Name != "Port Salt" {
		t.Fatalf("pov %+v location %+v", pov, loc)
	}
	if raw := read(t, filepath.Join(e.root(pid), "project.json")); !strings.Contains(raw, `"pov": "Máire Nolan",`) {
		t.Errorf("the text is the display fallback: %s", raw)
	}
	// Renaming the entity in the Compendium rewrites the manifest of every book that shows it.
	e.call(e.owner, "entity.update", entities.UpdateIn{ID: maire, Name: strptr("Máire Ní Nolan")})
	e.settleAll()
	if pov, _ := sceneRefs(t, e.root(pid), d.ID); pov == nil || pov.Name != "Máire Ní Nolan" {
		t.Errorf("a renamed entity did not reach the manifest: %+v", pov)
	}
	// This book's own name for her (an override) is the one it shows.
	e.call(e.owner, "project.entity.override.set", entities.OverrideSetIn{ID: pid, EntityID: maire, Name: strptr("The Captain")})
	e.settleAll()
	if pov, _ := sceneRefs(t, e.root(pid), d.ID); pov == nil || pov.Name != "The Captain" {
		t.Errorf("the override name is not in the manifest: %+v", pov)
	}
	// Quiet: nothing changed, nothing is written.
	e.fs.reset()
	if _, err := e.m.Rebuild(e.ctx, pid); err != nil {
		t.Fatal(err)
	}
	if n := e.fs.writes(); n != 0 {
		t.Errorf("a rebuild of unchanged data wrote %d files", n)
	}
}
