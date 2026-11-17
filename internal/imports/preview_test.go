package imports_test

import (
	"testing"

	"github.com/crueber/scriobh/internal/imports"
)

func TestPreviewShowsTheEditsApplyWillMake(t *testing.T) {
	e := newEnv(t)
	f := files(map[string]string{
		"01-a.md": "# A\n\nText.\n\n## Scene one\n\nS1.\n\n## Scene two\n\nS2.\n",
		"02-b.md": "B.\n",
		"03-c.md": "C.\n",
	})
	yes, title := true, "Renamed"
	edits := []imports.Edit{{Key: "01-a.md", Split: &yes}, {Key: "01-a.md#2", Title: &title}, {Key: "03-c.md", Skip: true}}
	reorder := []imports.Reorder{{Parent: "01-a.md", Keys: []string{"01-a.md#2"}}}
	pv := e.must(e.w, "import.preview", imports.PreviewIn{Files: f, Edits: edits, Reorder: reorder}).(imports.PreviewOut)
	eq(t, outline(pv.Nodes), []string{"chapter:A", "  scene:Renamed", "  scene:Scene one", "chapter:B"})
	out := e.must(e.w, "import.apply", imports.ApplyIn{Token: pv.Token, Files: f, Edits: edits, Reorder: reorder}).(imports.ApplyOut)
	if out.Documents != pv.Documents || out.Warnings == nil {
		t.Errorf("apply made %d documents, preview showed %d; warnings %v", out.Documents, pv.Documents, out.Warnings)
	}
}

func TestAFolderWithOneSubfolderKeepsThatPart(t *testing.T) {
	e := newEnv(t)
	pv := e.preview(files(map[string]string{"only-part/01-a.md": "A.\n", "only-part/02-b.md": "B.\n"}), "")
	eq(t, outline(pv.Nodes), []string{"part:Only Part", "  chapter:A", "  chapter:B"})
}
