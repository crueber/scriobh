package entities_test

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"sort"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/domain/entities"
	"github.com/crueber/scriobh/internal/ops"
)

func unzip(t *testing.T, b []byte) map[string]string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, f := range zr.File {
		r, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(r)
		r.Close()
		out[f.Name] = string(data)
	}
	return out
}

// A Compendium can be exported with no project open, deterministically, and
// the export is recorded with a manifest of what it held.
func TestCompendiumExport(t *testing.T) {
	x := newH(t)
	cid := x.compendium(x.owner, "The Salt Road")
	maire := x.call(x.owner, "entity.create", entities.CreateIn{ID: cid, Kind: "character", Name: "Máire Nolan", Summary: "Keeps the house.", Body: "Walks the harbour.",
		Aliases: []string{"Maire"}, Fields: map[string]any{"age": 34}}).(entities.Item).Item
	sorcha := x.ent(x.owner, cid, "character", "Sorcha")
	gone := x.ent(x.owner, cid, "item", "Lost key")
	x.call(x.owner, "entity.relation.add", entities.RelationAddIn{ID: maire.ID, ToEntityID: sorcha.ID, Kind: "family", Label: "mother of", InverseLabel: "daughter of"})
	x.call(x.owner, "entity.trash", entities.StateIn{ID: gone.ID})
	if n := x.count(`SELECT count(*) FROM projects`); n != 0 {
		t.Fatal("this test is about a Compendium with no project")
	}

	first := x.call(x.owner, "compendium.export", entities.ExportIn{ID: cid}).(entities.ExportFile)
	second := x.call(x.owner, "compendium.export", entities.ExportIn{ID: cid}).(entities.ExportFile)
	if !bytes.Equal(first.Body, second.Body) || first.SHA256 != second.SHA256 || first.ExportID == second.ExportID {
		t.Fatal("two exports of the same state are not the same bytes (or share an id)")
	}
	if first.Entities != 2 || first.Filename != "the-salt-road-compendium.zip" || first.ContentType != "application/zip" {
		t.Errorf("%+v", first)
	}
	files := unzip(t, first.Body)
	var names []string
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	if len(names) != 5 || names[0] != "README.md" || names[1] != "entities/characters/maire-nolan--"+strings.ToLower(maire.ID[len(maire.ID)-6:])+".md" ||
		names[3] != "scriobh.json" || names[4] != "timeline.md" {
		t.Fatalf("files: %v", names)
	}
	if got := files[names[1]]; !strings.Contains(got, "name: \"Máire Nolan\"") || !strings.Contains(got, "\"mother of: Sorcha\"") || !strings.Contains(got, "age: \"34\"") || !strings.HasSuffix(got, "Walks the harbour.\n") {
		t.Errorf("entity file:\n%s", got)
	}
	var m struct {
		Format     string
		Compendium struct{ Name, State string }
		Entities   []struct{ ID, Path, SHA256 string }
		Fields     []entities.FieldDef `json:"field_definitions"`
	}
	if err := json.Unmarshal([]byte(files["scriobh.json"]), &m); err != nil {
		t.Fatal(err)
	}
	if m.Format != "scriobh-compendium-bundle" || m.Compendium.Name != "The Salt Road" || m.Compendium.State != "active" || len(m.Entities) != 2 || len(m.Fields) != 14 {
		t.Errorf("manifest: %+v", m)
	}
	for _, e := range m.Entities {
		if e.SHA256 == "" || files[e.Path] == "" {
			t.Errorf("manifest entry %+v is not in the bundle", e)
		}
	}
	// What the export recorded.
	var kept, manifest string
	var bytesN int
	if err := x.d.Read().QueryRow(`SELECT sha256, bytes, manifest_json FROM compendium_exports WHERE id = ?`, first.ExportID).Scan(&kept, &bytesN, &manifest); err != nil {
		t.Fatal(err)
	}
	if kept != first.SHA256 || bytesN != first.Bytes || !strings.Contains(manifest, maire.ID) || strings.Contains(manifest, gone.ID) {
		t.Errorf("recorded: %s %d %s", kept, bytesN, manifest)
	}
	// Any change gives different bytes; a reader cannot export; a stranger gets a 404.
	x.call(x.owner, "entity.update", entities.UpdateIn{ID: sorcha.ID, Summary: str("Changed.")})
	if third := x.call(x.owner, "compendium.export", entities.ExportIn{ID: cid}).(entities.ExportFile); bytes.Equal(third.Body, first.Body) {
		t.Error("an export after a change is the same bytes")
	}
	x.member(cid, x.reader, "reader")
	x.expect(x.reader, "compendium.export", entities.ExportIn{ID: cid}, ops.CodeForbidden)
	x.expect(x.stranger, "compendium.export", entities.ExportIn{ID: cid}, ops.CodeNotFound)
}
