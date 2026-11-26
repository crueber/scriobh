package imports_test

import (
	"testing"

	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/revisions"
	"github.com/crueber/scriobh/internal/imports"
)

// U8: an import into a project that exists changes no existing document's text, so it writes no
// version of them, and the documents it adds have no earlier text to keep. This is a guard: the
// import's trigger is the documents it changes, if any (docs/plans/UX-FIXES.md U8).
func TestAppendImportLeavesExistingVersionsAlone(t *testing.T) {
	e := newEnv(t)
	base := e.apply(files(map[string]string{"01-first.md": "# First\n\nText of the first chapter.\n"}), "", nil)
	lo := e.must(e.w, "doc.list", docs.ListIn{ProjectID: base.ProjectID}).(docs.ListOut)
	first := lo.Items[0].ID
	item := e.must(e.w, "doc.get", docs.DocIn{ID: first}).(docs.Item)
	edited := "# First\n\nText of the first chapter, edited once more with a few new words in it.\n"
	e.must(e.w, "doc.update", docs.UpdateIn{ID: first, BaseRevision: item.Item.Revision, Content: &edited})
	before := e.must(e.w, "revision.list", revisions.ListIn{ID: first, Limit: 200}).(revisions.ListOut).Items
	if len(before) == 0 {
		t.Fatal("the existing document has no versions to compare")
	}

	z := zipDir(t, "testdata/Salt Road.scriv")
	pv := e.must(e.w, "import.append_preview", imports.AppendPreviewIn{ProjectID: base.ProjectID, Name: "Salt Road.scriv.zip", Zip: z}).(imports.PreviewOut)
	e.must(e.w, "import.append", imports.AppendIn{ProjectID: base.ProjectID, Token: pv.Token, Name: "Salt Road.scriv.zip", Zip: z})

	after := e.must(e.w, "revision.list", revisions.ListIn{ID: first, Limit: 200}).(revisions.ListOut).Items
	if len(after) != len(before) {
		t.Fatalf("an import into the project changed the existing document's versions: %d before, %d after", len(before), len(after))
	}
	for _, lo := range e.must(e.w, "doc.list", docs.ListIn{ProjectID: base.ProjectID}).(docs.ListOut).Items {
		for _, r := range e.must(e.w, "revision.list", revisions.ListIn{ID: lo.ID, Limit: 200}).(revisions.ListOut).Items {
			if r.Reason == "cut" || r.Reason == "split" {
				t.Errorf("a structural version (%s) was written by an import, on %q", r.Reason, lo.Title)
			}
		}
	}
}
