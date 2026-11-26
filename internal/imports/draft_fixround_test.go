package imports_test

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/imports"
)

// The fix round of phase 3, one case to a test.

// A draft staged before an untitled chapter's key lost its number holds the old key in its edits and in what Undo goes back to. It
// loads, previews and applies; Undo runs; a key that no chapter has is set aside, not an error; and a chapter really titled "Chapter 3"
// keeps its own key.
func TestDraftStoredBeforeTheKeyRenameStillWorks(t *testing.T) {
	e := newDraftEnv(t, imports.StageLimits{})
	paras := corpusCat(corpusBody(2), one(cPbreak()), corpusBody(2), one(cPbreak()), corpusBody(2), one(cPbreak()), corpusBody(2))
	st, d := e.stageParas(paras)
	u := e.mustUpdate(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: d.Item.Revision, Options: map[string]string{"docx_split": "page_breaks"}})
	k2, k3, k4 := u.Preview.Nodes[1].Key, u.Preview.Nodes[2].Key, u.Preview.Nodes[3].Key
	nm, yes := "Renamed", true
	u = e.mustUpdate(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: u.Item.Revision, Edit: &imports.DraftEdit{Key: k2, Title: &nm}})
	u = e.mustUpdate(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: u.Item.Revision, Edit: &imports.DraftEdit{Key: k3, Skip: &yes}})
	u = e.mustUpdate(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: u.Item.Revision, Edit: &imports.DraftEdit{Key: k4, JoinPrevious: &yes}})
	rev := u.Item.Revision
	// make the stored draft what phase 2 would have written: the keys with the place in them
	old := func(k string, n int) string {
		return strings.Replace(k, "-chapter.md", fmt.Sprintf("-chapter-%d.md", n), 1)
	}
	err := e.d.WithTx(context.Background(), func(tx *sql.Tx) error {
		for _, c := range [][2]string{{k2, old(k2, 2)}, {k3, old(k3, 3)}, {k4, old(k4, 4)}} {
			if c[0] == c[1] {
				t.Fatalf("key %s has no -chapter.md", c[0])
			}
			if _, err := tx.Exec(`UPDATE import_drafts SET edits_json = replace(edits_json, ?, ?), prev_json = replace(prev_json, ?, ?)`, c[0], c[1], c[0], c[1]); err != nil {
				return err
			}
		}
		// and without the mark that says the keys are the new ones
		_, err := tx.Exec(`UPDATE import_drafts SET edits_json = replace(edits_json, ',"kv":2', ''), prev_json = replace(prev_json, ',"kv":2', '')`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	got := e.get(e.w, st.StageID, true)
	if gotT := titles(got.Preview); gotT != "Chapter 1|Renamed" {
		t.Fatalf("an old key is the chapter it was: %s", gotT)
	}
	if len(got.Item.Edits.Stash) != 0 || !got.Item.CanUndo {
		t.Errorf("nothing was set aside: %+v", got.Item.Edits.Stash)
	}
	// a change on it saves the new keys, and Undo (which went through the old keys too) runs
	u = e.mustUpdate(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: rev, Options: map[string]string{"docx_split": "page_breaks"}})
	for k := range u.Item.Edits.Edits {
		if strings.Contains(k, "-chapter-") {
			t.Errorf("an old key was saved: %s", k)
		}
	}
	u = e.mustUpdate(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: u.Item.Revision, Undo: true})
	if gotT := titles(u.Preview); gotT != "Chapter 1|Renamed" {
		t.Errorf("undo: %s", gotT)
	}
	ao := e.must(e.w, "import.apply", imports.ApplyIn{StageID: st.StageID, Rev: u.Item.Revision, Token: u.Item.Token}).(imports.ApplyOut)
	if ao.Documents != 2 {
		t.Errorf("applied %d documents", ao.Documents)
	}
}

// A key of the old shape that no chapter has is set aside and listed, and the rest of the draft works; and a chapter really titled
// "Chapter 3" is not touched.
func TestDraftWithAKeyNoChapterHasSetsItAside(t *testing.T) {
	e := newDraftEnv(t, imports.StageLimits{})
	paras := corpusCat(one(cPh1("Chapter 3")), corpusBody(2), one(cPh1("Chapter 4")), corpusBody(2))
	st, d := e.stageParas(paras)
	real := d.Preview.Nodes[0].Key // "…-chapter-3.md": a real title
	if !strings.Contains(real, "-chapter-3") {
		t.Fatalf("key %s", real)
	}
	nm := "Opening"
	u := e.mustUpdate(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: d.Item.Revision, Edit: &imports.DraftEdit{Key: real, Title: &nm}})
	err := e.d.WithTx(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE import_drafts SET edits_json = replace(replace(edits_json, ',"kv":2', ''), '}}', '},"0000999-chapter-9.md":{"title":"Ghost"}}') WHERE edits_json LIKE '%"edits":{%'`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	got := e.get(e.w, st.StageID, true)
	if gotT := titles(got.Preview); gotT != "Opening|Chapter 4" {
		t.Fatalf("a real Chapter 3 keeps its key and its rename: %s", gotT)
	}
	if st := got.Item.Edits.Stash; len(st) != 1 {
		t.Errorf("the ghost is set aside: %+v (edits %s)", st, fmt.Sprint(u.Item.Edits.Edits))
	}
}

// Splitting a chapter that is cut into scenes keeps every edit that is still true: the scenes that stay in the first half keep their
// renames, the scenes that moved into the new chapter keep theirs (found by their heading and words), and an edit of another chapter is
// not lost for them.
func TestSplitAChapterOfScenesKeepsTheEditsThatStillApply(t *testing.T) {
	e := newDraftEnv(t, imports.StageLimits{})
	var ps []cp
	ps = append(ps, cPh1("Alpha"))
	for i := 1; i <= 4; i++ {
		ps = append(ps, cPh2(fmt.Sprintf("Scene %d", i)), cP(fmt.Sprintf("Body of scene %d one.", i)), cP(fmt.Sprintf("Body of scene %d two.", i)))
	}
	ps = append(ps, cPh1("Beta"), cP("Beta text one."), cP("Beta text two."))
	st := e.mustStage(e.w, imports.StageIn{Kind: "docx", Name: "book.docx", SplitScenes: "h2"}, corpusDoc(ps))
	d := e.get(e.w, st.StageID, true)
	var scenes []imports.Node
	for _, n := range d.Preview.Nodes {
		if strings.Contains(n.Key, "#") {
			scenes = append(scenes, n)
		}
	}
	if len(scenes) != 4 {
		t.Fatalf("four scenes: %s", titles(d.Preview))
	}
	rev := d.Item.Revision
	for i, s := range scenes {
		nm := fmt.Sprintf("Renamed %d", i+1)
		rev = e.mustUpdate(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: rev, Edit: &imports.DraftEdit{Key: s.Key, Title: &nm}}).Item.Revision
	}
	beta, omega := nodeByTitle(t, d.Preview, "Beta"), "Omega"
	rev = e.mustUpdate(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: rev, Edit: &imports.DraftEdit{Key: beta.Key, Title: &omega}}).Item.Revision
	alpha := nodeByTitle(t, d.Preview, "Alpha")
	// split Alpha inside its third scene: scenes 1 and 2, and the start of 3, stay; the end of 3 and scene 4 move to the new chapter, where
	// scene 4 is the first scene and has another key
	l := e.look(st.StageID, alpha.Key, "split", 0, 100)
	var at int
	for _, p := range l.Paragraphs {
		if p.Line == "Body of scene 3 two." {
			at = p.Ordinal
		}
	}
	if at == 0 {
		t.Fatalf("no paragraph for the end of scene 3: %+v", l.Paragraphs)
	}
	u := e.mustUpdate(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: rev, Break: &at, BreakFrom: alpha.Key})
	got := titles(u.Preview)
	for _, want := range []string{"Renamed 1", "Renamed 2", "Renamed 3", "Renamed 4", "Omega"} {
		if !strings.Contains(got, want) {
			t.Errorf("%q was lost: %s (note %+v)", want, got, u.Note)
		}
	}
	if len(u.Note.Dropped) != 0 {
		t.Errorf("nothing true was dropped: %+v", u.Note.Dropped)
	}
	if u.Note.Kept != 5 {
		t.Errorf("five edits stand: %+v", u.Note)
	}
}

// When the split is at a scene's own heading the scene becomes a chapter: only that edit is set aside, and it is listed.
func TestSplitAtASceneHeadingSetsAsideOnlyThatScene(t *testing.T) {
	e := newDraftEnv(t, imports.StageLimits{})
	var ps []cp
	ps = append(ps, cPh1("Alpha"))
	for i := 1; i <= 3; i++ {
		ps = append(ps, cPh2(fmt.Sprintf("Scene %d", i)), cP(fmt.Sprintf("Body of scene %d one.", i)), cP(fmt.Sprintf("Body of scene %d two.", i)))
	}
	ps = append(ps, cPh1("Beta"), cP("Beta text one."), cP("Beta text two."))
	st := e.mustStage(e.w, imports.StageIn{Kind: "docx", Name: "book.docx", SplitScenes: "h2"}, corpusDoc(ps))
	d := e.get(e.w, st.StageID, true)
	rev := d.Item.Revision
	for _, n := range d.Preview.Nodes {
		if n.Title == "Scene 1" || n.Title == "Scene 3" || n.Title == "Beta" {
			nm := "New " + n.Title
			rev = e.mustUpdate(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: rev, Edit: &imports.DraftEdit{Key: n.Key, Title: &nm}}).Item.Revision
		}
	}
	alpha := nodeByTitle(t, d.Preview, "Alpha")
	var at int
	for _, p := range e.look(st.StageID, alpha.Key, "split", 0, 100).Paragraphs {
		if p.Line == "Scene 3" {
			at = p.Ordinal
		}
	}
	u := e.mustUpdate(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: rev, Break: &at, BreakFrom: alpha.Key})
	if got := titles(u.Preview); !strings.Contains(got, "New Scene 1") || !strings.Contains(got, "New Beta") {
		t.Errorf("the edits that still apply stand: %s", got)
	}
	if len(u.Note.Dropped) != 1 || u.Note.Dropped[0] != "New Scene 3" || u.Note.Kept != 2 {
		t.Errorf("only the scene that became a chapter is set aside: %+v", u.Note)
	}
}

// "At most 200 characters" is true of what the pattern is tried on, in any script: a line of Chinese of 135 or 190 characters (three
// bytes each) and a Latin line of 96 words under 200 characters are tried; one of 201 is not.
func TestPatternIsTriedOnEveryLineOf200CharactersOrFewer(t *testing.T) {
	e := newDraftEnv(t, imports.StageLimits{})
	mid := "第二章 " + strings.Repeat("海", 135)
	long := "第三章 " + strings.Repeat("海", 190)
	tooLong := "第四章 " + strings.Repeat("海", 198)
	words := "Chapter " + strings.TrimSpace(strings.Repeat("a ", 95))
	paras := corpusCat(corpusBody(2), one(cP("第一章 "+strings.Repeat("海", 20))), corpusBody(1), one(cP(mid)), corpusBody(1), one(cP(long)), corpusBody(1), one(cP(tooLong)), corpusBody(1), one(cP(words)), corpusBody(1))
	st, d := e.stageParas(paras)
	u := e.mustUpdate(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: d.Item.Revision, Options: map[string]string{"docx_split_try": "第"}})
	if u.Preview.Breaks.Try.Count != 3 {
		t.Errorf("the lines of 24, 138 and 193 characters are tried, the one of 201 is not: %+v", u.Preview.Breaks.Try)
	}
	u = e.mustUpdate(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: u.Item.Revision, Options: map[string]string{"docx_split_try": "chapter"}})
	if u.Preview.Breaks.Try.Count != 1 {
		t.Errorf("a line of 96 words is tried: %+v", u.Preview.Breaks.Try)
	}
	u = e.mustUpdate(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: u.Item.Revision, Options: map[string]string{"docx_split_try": `^第[二三]章 海+$`, "docx_split_try_match": "regex"}})
	if u.Preview.Breaks.Try.Count != 2 {
		t.Errorf("a regular expression on them: %+v", u.Preview.Breaks.Try)
	}
}

// A look reads the book once: the second look, and the next page of a list, are from the book already read, and they say exactly what the
// first would have; a look at one chapter does not disturb a look at another, and a pattern that is only tried does not reach a look.
func TestLookIsFromTheBookAlreadyRead(t *testing.T) {
	e := newDraftEnv(t, imports.StageLimits{})
	st, d := e.stageParas(longChapter())
	long := nodeByTitle(t, d.Preview, "Long")
	short := nodeByTitle(t, d.Preview, "Short")
	first := e.look(st.StageID, long.Key, "split", 0, 40)
	e.look(st.StageID, short.Key, "split", 0, 40)
	e.look(st.StageID, long.Key, "inside", 0, 40)
	again := e.look(st.StageID, long.Key, "split", 0, 40)
	if fmt.Sprint(first) != fmt.Sprint(again) {
		t.Errorf("the same look says other things:\n%+v\n%+v", first, again)
	}
	u := e.mustUpdate(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: d.Item.Revision, Options: map[string]string{"docx_split_try": "word", "docx_split_try_match": "starts"}})
	after := e.look(st.StageID, long.Key, "split", 0, 40)
	if fmt.Sprint(first) != fmt.Sprint(after) || u.Preview.Breaks.Try == nil {
		t.Errorf("a pattern that is only tried reached a look")
	}
	// the cut options reach it: the same chapter looked at after a break is the shorter chapter
	at := first.Paragraphs[5].Ordinal
	u = e.mustUpdate(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: u.Item.Revision, Break: &at, BreakFrom: long.Key})
	cut := e.look(st.StageID, long.Key, "split", 0, 40)
	if cut.Total != 5 || cut.Words >= first.Words {
		t.Errorf("a look after a break is of the chapter as it is now: %d paragraphs, %d words (was %d)", cut.Total, cut.Words, first.Words)
	}
}

// The small things of the round, one to a test.

// A scene break that ends a chapter at a cut the writer chose is not left as its last line; at a heading the source is as it was.
func TestNoDanglingSceneBreakAtTheWritersCut(t *testing.T) {
	e := newDraftEnv(t, imports.StageLimits{})
	ps := corpusCat(one(cP("Marker001 words one two three.")), one(cP("Marker002 words one two three.")), one(cP("* * *")), one(cP("Marker003 words one two three.")), one(cP("Marker004 words one two three.")))
	st, d := e.stageParas(ps)
	key := d.Preview.Nodes[0].Key
	l := e.look(st.StageID, key, "split", 0, 100)
	var at int
	for _, p := range l.Paragraphs {
		if strings.HasPrefix(p.Line, "Marker003") {
			at = p.Ordinal
		}
	}
	u := e.mustUpdate(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: d.Item.Revision, Break: &at, BreakFrom: key})
	ao := e.must(e.w, "import.apply", imports.ApplyIn{StageID: st.StageID, Rev: u.Item.Revision, Token: u.Item.Token}).(imports.ApplyOut)
	for _, n := range u.Preview.Nodes {
		c := strings.TrimSpace(e.docContent(ao.ProjectID, n.Title))
		if strings.HasSuffix(c, "* * *") {
			t.Errorf("%s ends with a scene break:\n%s", n.Title, c)
		}
	}
	if !strings.Contains(e.docContent(ao.ProjectID, u.Preview.Nodes[0].Title)+e.docContent(ao.ProjectID, u.Preview.Nodes[1].Title), "Marker004") {
		t.Error("text was lost")
	}
	// the same break before a heading keeps the source as it was
	ps = corpusCat(one(cPh1("One")), one(cP("Marker001 words one two three.")), one(cP("* * *")), one(cPh1("Two")), one(cP("Marker002 words one two three.")))
	_, d2 := e.stageParas(ps)
	if !strings.Contains(strings.Join(titlesList(d2.Preview), "|"), "One|Two") {
		t.Fatal("two chapters")
	}
}

func titlesList(pv *imports.PreviewOut) []string {
	var out []string
	for _, n := range pv.Nodes {
		out = append(out, n.Title)
	}
	return out
}

// The places inside a long chapter are listed in reading order and the same each time, and bounded after they are ordered.
func TestInsidePlacesAreDeterministic(t *testing.T) {
	e := newDraftEnv(t, imports.StageLimits{})
	var ps []cp
	ps = append(ps, cPh1("Long"))
	for i := 0; i < 160; i++ {
		switch i % 40 {
		case 10:
			ps = append(ps, cPh3x("Third level"))
		case 20:
			ps = append(ps, cPh2("Second level"))
		case 30:
			ps = append(ps, cPbreak())
		}
		ps = append(ps, cP(strings.TrimSpace(strings.Repeat("word ", 100))+fmt.Sprintf(" %d.", i)))
	}
	st, d := e.stageParas(ps)
	key := d.Preview.Nodes[0].Key
	first := fmt.Sprint(e.look(st.StageID, key, "inside", 0, 100).Suggest)
	for i := 0; i < 8; i++ {
		if again := fmt.Sprint(e.look(st.StageID, key, "inside", 0, 100).Suggest); again != first {
			t.Fatalf("the list changed between looks:\n%s\n%s", first, again)
		}
	}
	l := e.look(st.StageID, key, "inside", 0, 100)
	for i := 1; i < len(l.Suggest); i++ {
		if l.Suggest[i].Ordinal <= l.Suggest[i-1].Ordinal {
			t.Fatal("reading order")
		}
	}
}

func cPh3x(text string) cp { return cPs(text, "Heading3") }

// A break at the ordinal of a picture is refused, not moved to the paragraph after it, and break_from must be the chapter the paragraph is in.
func TestBreakAtAPictureAndBreakFromTheWrongChapter(t *testing.T) {
	e := newDraftEnv(t, imports.StageLimits{})
	paras := corpusCat(one(cPh1("Alpha")), corpusBody(3), one(cp{runs: []string{`<w:r><w:drawing/></w:r>`}}), corpusBody(2), one(cPh1("Beta")), corpusBody(4))
	st, d := e.stageParas(paras)
	alpha, beta := d.Preview.Nodes[0], d.Preview.Nodes[1]
	img := alpha.Ordinal + 4
	if _, err := e.update(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: d.Item.Revision, Break: &img, BreakFrom: alpha.Key}); reason(err) != "break_noop" {
		t.Errorf("a picture: %v", err)
	}
	lb := e.look(st.StageID, beta.Key, "split", 0, 100)
	at := lb.Paragraphs[1].Ordinal
	if _, err := e.update(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: d.Item.Revision, Break: &at, BreakFrom: alpha.Key}); reason(err) != "break_noop" {
		t.Errorf("a paragraph of Beta said to be in Alpha: %v", err)
	}
	if again := e.get(e.w, st.StageID, false); again.Item.Revision != d.Item.Revision {
		t.Error("a refused change stored something")
	}
	if _, err := e.update(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: d.Item.Revision, Break: &at, BreakFrom: beta.Key}); err != nil {
		t.Errorf("the right chapter: %v", err)
	}
}

// The refusals that were English only carry a reason the page says in its own words.
func TestCutRefusalsCarryAReason(t *testing.T) {
	e := newDraftEnv(t, imports.StageLimits{})
	var paras []cp
	for i := 0; i < 2100; i++ {
		paras = append(paras, cP(fmt.Sprintf("Line %d of the book, a short one.", i)), cPbreak())
	}
	st, d := e.stageParas(paras)
	_, err := e.update(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: d.Item.Revision, Options: map[string]string{"docx_split": "page_breaks"}})
	if reason(err) != "too_many_docs" {
		t.Errorf("more documents than an import takes: %v", err)
	}
	_, err = e.update(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: d.Item.Revision, Options: map[string]string{"docx_split": "pattern", "docx_split_pattern": "line", "docx_split_match": "starts"}})
	if reason(err) != "too_many_docs" {
		t.Errorf("a pattern that matches too many: %v", err)
	}
}
