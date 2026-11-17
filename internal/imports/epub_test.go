package imports_test

import (
	"testing"

	"github.com/crueber/scriobh/internal/imports"
)

func TestEpub3Preview(t *testing.T) {
	e := newEnv(t)
	pv := e.previewConv(imports.PreviewIn{Name: "salt.epub", Epub: salt3(t)})
	for _, l := range outline(pv.Nodes) {
		t.Log(l)
	}
	ap := e.applyConv(pv, imports.ApplyIn{Name: "salt.epub", Epub: salt3(t)})
	rows, _ := e.d.Read().Query(`SELECT kind, title, content FROM documents WHERE project_id = ? ORDER BY position`, ap.ProjectID)
	defer rows.Close()
	for rows.Next() {
		var k, ti, c string
		rows.Scan(&k, &ti, &c)
		t.Logf("== %s: %s\n%s", k, ti, c)
	}
	t.Logf("%+v", ap)
}
