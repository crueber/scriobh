package imports_test

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/imports"
)

// A Markdown draft staged before a heading inside a comment stopped being a scene holds an edit on the scene that is no longer there
// ("a.md#2"): it must still open, with that edit set aside and listed, and the edit on a scene that is still there kept under the same key.
func TestAnOldMarkdownDraftWithAGoneSceneKeyStillOpens(t *testing.T) {
	e := newDraftEnv(t, imports.StageLimits{})
	md := "Before the first heading.\n\n## One\n\nText of one.\n\n<!--\n## Hidden in a comment\n-->\n\n## Two\n\nText of two.\n"
	st := e.mustStage(e.w, imports.StageIn{Kind: "zip", Name: "book.zip", SplitScenes: "h2"}, zipOf(t, map[string]string{"a.md": md}))
	d := e.get(e.w, st.StageID, true)
	var keys []string
	for _, n := range d.Preview.Nodes {
		keys = append(keys, n.Key)
	}
	if strings.Join(keys, "|") != "a.md|a.md#1|a.md#3" {
		t.Fatalf("keys of the cut now: %v (the heading in the comment keeps its place, #2, and is no scene)", keys)
	}
	// what the draft stored before: edits on the scene that is gone (#2) and on one that is not (#3), and no mark that the keys are current
	err := e.d.WithTx(t.Context(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE import_drafts SET edits_json = '{"edits":{"a.md#2":{"title":"Hidden, renamed"},"a.md#3":{"title":"Two, renamed"}}}' WHERE id = ?`, st.StageID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	g, gerr := e.call(e.w, "import.draft.get", imports.DraftGetIn{ID: st.StageID, Preview: true})
	if gerr != nil {
		t.Fatalf("the draft does not open: %v", gerr)
	}
	out := g.(imports.DraftOut)
	titles := []string{}
	for _, n := range out.Preview.Nodes {
		titles = append(titles, n.Title)
	}
	if strings.Join(titles, "|") != "A|One|Two, renamed" {
		t.Errorf("titles: %v", titles)
	}
	var raw string
	_ = e.d.Read().QueryRow(`SELECT edits_json FROM import_drafts WHERE id = ?`, st.StageID).Scan(&raw)
	// the next change that is saved holds the settled state: the gone scene's edit is set aside, in the draft, by title
	one := "One, renamed"
	e.mustUpdate(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: out.Item.Revision, Edit: &imports.DraftEdit{Key: "a.md#1", Title: &one}})
	_ = e.d.Read().QueryRow(`SELECT edits_json FROM import_drafts WHERE id = ?`, st.StageID).Scan(&raw)
	if !strings.Contains(raw, `"stash"`) || !strings.Contains(raw, "Hidden, renamed") {
		t.Errorf("the edit of the scene that is gone is not set aside: %s", raw)
	}
}
