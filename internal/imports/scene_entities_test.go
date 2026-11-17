package imports_test

import (
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/imports"
)

// A version 3 manifest names the entity a scene's point of view and location
// were. Importing into a new project has no bible in scope, so the references
// are not believed (an id from a file is untrusted) and the text stays; a
// hostile or malformed reference never fails the import.
func TestImportReadsEntityReferencesButTrustsNone(t *testing.T) {
	e := newEnv(t)
	f := files(map[string]string{
		"project.json": `{"schema_version":3,"project":{"title":"V3"},"documents":[
			{"path":"manuscript/01-a.md","kind":"scene","title":"A","status":"draft","pov":"Máire","pov_entity":{"id":"ent_01ARZ3NDEKTSV4RRFFQ69G5FAV","name":"Máire"},
			 "location":"Port Salt","location_entity":"not an object"},
			{"path":"manuscript/02-b.md","kind":"scene","title":"B","status":"draft","pov":"x","pov_entity":{"id":"` + strings.Repeat("e", 500) + `"}}]}`,
		"manuscript/01-a.md": "Text.\n",
		"manuscript/02-b.md": "More.\n",
	})
	pv := e.must(e.w, "import.preview", imports.PreviewIn{Files: f, Name: "x"}).(imports.PreviewOut)
	for _, w := range pv.Warnings {
		if strings.Contains(w, "not valid JSON") {
			t.Fatalf("the manifest was refused: %v", pv.Warnings)
		}
	}
	out := e.must(e.w, "import.apply", imports.ApplyIn{Token: pv.Token, Files: f, Name: "x"}).(imports.ApplyOut)
	list := e.must(e.w, "doc.list", docs.ListIn{ProjectID: out.ProjectID}).(docs.ListOut)
	got := map[string]docs.Meta{}
	for _, m := range list.Items {
		got[m.Title] = m
	}
	if a := got["A"]; a.Pov != "Máire" || a.Location != "Port Salt" || a.PovEntity != nil || a.LocationEntity != nil {
		t.Errorf("A: %+v", a)
	}
	if b := got["B"]; b.Pov != "x" || b.PovEntity != nil {
		t.Errorf("B: %+v", b)
	}
}
