package imports_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/imports"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/testutil"
)

// The writer's own breaks (phase 3): join with the chapter above, split a chapter at a paragraph chosen from a list, front and back matter
// for a whole run, a pattern the writer types, and a look inside a long chapter. Each is one change on the draft, with one Undo.

func (e *denv) look(id, key, mode string, after, count int) *imports.LookOut {
	e.t.Helper()
	out := e.must(e.w, "import.draft.get", imports.DraftGetIn{ID: id, LookKey: key, LookMode: mode, LookAfter: after, LookCount: count}).(imports.DraftOut)
	if out.Look == nil {
		e.t.Fatalf("a look at %s answered nothing", key)
	}
	return out.Look
}

func (e *denv) docContent(projectID, title string) string {
	e.t.Helper()
	lo := e.must(e.w, "doc.list", docs.ListIn{ProjectID: projectID}).(docs.ListOut)
	for _, m := range lo.Items {
		if m.Title == title {
			return e.must(e.w, "doc.get", docs.DocIn{ID: m.ID}).(docs.Item).Item.Content
		}
	}
	e.t.Fatalf("no document %q", title)
	return ""
}

func (e *denv) stageParas(paras []cp) (imports.StageOut, imports.DraftOut) {
	e.t.Helper()
	st := e.mustStage(e.w, imports.StageIn{Kind: "docx", Name: "book.docx"}, corpusDoc(paras))
	return st, e.get(e.w, st.StageID, true)
}

func nodeByTitle(t *testing.T, pv *imports.PreviewOut, title string) imports.Node {
	t.Helper()
	for _, n := range pv.Nodes {
		if n.Title == title {
			return n
		}
	}
	t.Fatalf("no %q in %s", title, titles(pv))
	return imports.Node{}
}

func threeChapters() []cp {
	return corpusCat(one(cPh1("Alpha")), corpusBody(6), one(cPh1("Beta")), corpusBody(6), one(cPh1("Gamma")), corpusBody(6))
}

func TestJoinWithPreviousIsOnePressAndPutBackWorks(t *testing.T) {
	e := newDraftEnv(t, imports.StageLimits{})
	st, d := e.stageParas(threeChapters())
	beta := nodeByTitle(t, d.Preview, "Beta")
	yes := true
	u := e.mustUpdate(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: d.Item.Revision, Edit: &imports.DraftEdit{Key: beta.Key, JoinPrevious: &yes}})
	if got := titles(u.Preview); got != "Alpha|Gamma" || u.Note.Code != "joined" || u.Note.Title != "Beta" || u.Note.Other != "Alpha" {
		t.Fatalf("beta joined into alpha: %s %+v", got, u.Note)
	}
	if u.Preview.Nodes[0].Words != d.Preview.Nodes[0].Words+d.Preview.Nodes[1].Words {
		t.Errorf("alpha holds beta's words: %d vs %d + %d", u.Preview.Nodes[0].Words, d.Preview.Nodes[0].Words, d.Preview.Nodes[1].Words)
	}
	g := u.Item.Edits.Gone[beta.Key]
	if g.To == "" || g.ToWords == 0 || g.Words != beta.Words {
		t.Errorf("the join is remembered with the text it was made on and the text it was made onto: %+v", g)
	}
	// the first chapter has nothing before it
	alpha := nodeByTitle(t, d.Preview, "Alpha")
	if _, err := e.update(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: u.Item.Revision, Edit: &imports.DraftEdit{Key: alpha.Key, JoinPrevious: &yes}}); reason(err) != "no_previous" {
		t.Errorf("the first chapter: %v", err)
	}
	// Put back
	no := false
	u = e.mustUpdate(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: u.Item.Revision, Edit: &imports.DraftEdit{Key: beta.Key, Skip: &no}})
	if got := titles(u.Preview); got != "Alpha|Beta|Gamma" || u.Preview.Words != d.Preview.Words {
		t.Errorf("put back: %s", got)
	}
	// both ways at once is refused
	other := alpha.Key
	if _, err := e.update(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: u.Item.Revision, Edit: &imports.DraftEdit{Key: beta.Key, JoinPrevious: &yes, MergeInto: &other}}); code(err) != ops.CodeInvalid {
		t.Errorf("both: %v", err)
	}
}

func TestJoinWithPreviousIsNotForAPartOrIntoOne(t *testing.T) {
	e := newDraftEnv(t, imports.StageLimits{})
	st, d := stageSample(t, e, "02")
	yes := true
	var part, first string
	for _, n := range d.Preview.Nodes {
		if n.Kind == "part" && part == "" {
			part = n.Key
		}
		if n.Kind == "chapter" && first == "" {
			first = n.Key
		}
	}
	for _, k := range []string{part, first} { // a part; the first chapter of a part has no brother before it
		if _, err := e.update(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: d.Item.Revision, Edit: &imports.DraftEdit{Key: k, JoinPrevious: &yes}}); reason(err) != "no_previous" {
			t.Errorf("%s: %v", k, err)
		}
	}
}

func TestSplitAtAParagraphFromTheList(t *testing.T) {
	e := newDraftEnv(t, imports.StageLimits{})
	st, d := e.stageParas(threeChapters())
	beta := nodeByTitle(t, d.Preview, "Beta")
	gamma := nodeByTitle(t, d.Preview, "Gamma")
	// the list is the chapter's paragraphs, a page at a time, and not the first (nothing would be before it)
	l := e.look(st.StageID, beta.Key, "split", 0, 4)
	if l.Total != 5 || len(l.Paragraphs) != 4 || l.Title != "Beta" || l.Mode != "split" {
		t.Fatalf("the list: %+v", l)
	}
	if l.Paragraphs[0].Line == "" || l.Paragraphs[0].Words == 0 || len(l.Paragraphs[0].Line) > 90 {
		t.Errorf("a line and its place: %+v", l.Paragraphs[0])
	}
	more := e.look(st.StageID, beta.Key, "split", 4, 4)
	if len(more.Paragraphs) != 1 || more.After != 4 || more.Paragraphs[0].Ordinal <= l.Paragraphs[3].Ordinal {
		t.Errorf("the next page: %+v", more)
	}
	// rename a chapter, then split another: the rename stays
	nm := "Omega"
	u := e.mustUpdate(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: d.Item.Revision, Edit: &imports.DraftEdit{Key: gamma.Key, Title: &nm}})
	at := l.Paragraphs[2].Ordinal
	u = e.mustUpdate(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: u.Item.Revision, Break: &at, BreakFrom: beta.Key})
	if got := titles(u.Preview); got != "Alpha|Beta|Chapter 3|Omega" {
		t.Fatalf("split: %s", got)
	}
	if u.Note.Code != "split_para" || u.Note.Other != "Beta" || u.Note.Title != "Chapter 3" || u.Note.Ends == "" || u.Note.Starts == "" {
		t.Errorf("the words name where it was split from the key, not the place: %+v", u.Note)
	}
	if u.Note.Kept != 1 {
		t.Errorf("the rename of Gamma stays with Gamma: %+v", u.Note)
	}
	// every other key is the key it was
	for _, k := range []string{nodeByTitle(t, u.Preview, "Alpha").Key, nodeByTitle(t, u.Preview, "Beta").Key, nodeByTitle(t, u.Preview, "Omega").Key} {
		if k != nodeByTitle(t, d.Preview, "Alpha").Key && k != beta.Key && k != gamma.Key {
			t.Errorf("a key changed: %s", k)
		}
	}
	// no text is lost or doubled
	if u.Preview.Words != d.Preview.Words {
		t.Errorf("words: %d, was %d", u.Preview.Words, d.Preview.Words)
	}
	// the same place again, or a place that is already a chapter, is refused and stores nothing
	if _, err := e.update(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: u.Item.Revision, Break: &at, BreakFrom: beta.Key}); reason(err) != "break_noop" {
		t.Errorf("the same place: %v", err)
	}
	if _, err := e.update(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: u.Item.Revision, Break: &beta.Ordinal, BreakFrom: beta.Key}); reason(err) != "break_noop" {
		t.Errorf("where a chapter starts: %v", err)
	}
	// Undo takes the split back
	u = e.mustUpdate(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: u.Item.Revision, Undo: true})
	if got := titles(u.Preview); got != "Alpha|Beta|Omega" {
		t.Errorf("undo: %s", got)
	}
	// a key that is not a chapter is not found
	if _, err := e.call(e.w, "import.draft.get", imports.DraftGetIn{ID: st.StageID, LookKey: "nope.md"}); code(err) != ops.CodeNotFound {
		t.Errorf("a look at nothing: %v", err)
	}
}

// A paragraph that would be the new chapter's title says so: the line leaves the text and becomes the title.
func TestSplitListSaysWhichLineBecomesTheTitle(t *testing.T) {
	e := newDraftEnv(t, imports.StageLimits{})
	paras := corpusCat(one(cPh1("Alpha")), corpusBody(3), one(cP("A Short Line")), corpusBody(3))
	st, d := e.stageParas(paras)
	l := e.look(st.StageID, d.Preview.Nodes[0].Key, "split", 0, 100)
	var at int
	for _, p := range l.Paragraphs {
		if p.Line == "A Short Line" {
			if !p.Title {
				t.Errorf("a short line with no full stop is the title: %+v", p)
			}
			at = p.Ordinal
		} else if p.Title {
			t.Errorf("prose is not a title: %+v", p)
		}
	}
	u := e.mustUpdate(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: d.Item.Revision, Break: &at, BreakFrom: d.Preview.Nodes[0].Key})
	if got := titles(u.Preview); got != "Alpha|A Short Line" {
		t.Errorf("titled by the line: %s", got)
	}
	if u.Preview.Words != d.Preview.Words-3 {
		t.Errorf("the three words of the line are the title's now: %d, was %d", u.Preview.Words, d.Preview.Words)
	}
}

// A footnote on the paragraph a chapter starts at is kept: nothing is lost at a cut, and the notes are numbered again in each file.
func TestSplitAtAParagraphThatCarriesAFootnoteKeepsIt(t *testing.T) {
	e := newDraftEnv(t, imports.StageLimits{})
	var sb strings.Builder
	sb.WriteString(para("Heading1", "Alpha"))
	sb.WriteString(`<w:p><w:r><w:t>Early words</w:t></w:r><w:r><w:footnoteReference w:id="2"/></w:r></w:p>`)
	for _, p := range corpusBody(3) {
		sb.WriteString(para("", p.text))
	}
	sb.WriteString(`<w:p><w:r><w:t>A short line</w:t></w:r><w:r><w:footnoteReference w:id="1"/></w:r></w:p>`)
	for _, p := range corpusBody(3) {
		sb.WriteString(para("", p.text))
	}
	sb.WriteString(para("", "The last words"))
	notes := `<?xml version="1.0"?><w:footnotes ` + wns + `><w:footnote w:id="1"><w:p><w:r><w:t>First note.</w:t></w:r></w:p></w:footnote><w:footnote w:id="2"><w:p><w:r><w:t>Second note.</w:t></w:r></w:p></w:footnote></w:footnotes>`
	data := miniDocx(t, map[string]string{"word/document.xml": body(sb.String()), "word/styles.xml": headingStyles, "word/footnotes.xml": notes})
	st := e.mustStage(e.w, imports.StageIn{Kind: "docx", Name: "book.docx"}, data)
	d := e.get(e.w, st.StageID, true)
	l := e.look(st.StageID, d.Preview.Nodes[0].Key, "split", 0, 100)
	var at int
	for _, p := range l.Paragraphs {
		if strings.HasPrefix(p.Line, "A short line") {
			at = p.Ordinal
		}
	}
	if at == 0 {
		t.Fatalf("the paragraph is listed: %+v", l.Paragraphs)
	}
	u := e.mustUpdate(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: d.Item.Revision, Break: &at, BreakFrom: d.Preview.Nodes[0].Key})
	if u.Preview.Documents != 2 {
		t.Fatalf("two chapters: %v", titles(u.Preview))
	}
	ao := e.must(e.w, "import.apply", imports.ApplyIn{StageID: st.StageID, Rev: u.Item.Revision, Token: u.Item.Token}).(imports.ApplyOut)
	all := ""
	for _, n := range u.Preview.Nodes {
		all += e.docContent(ao.ProjectID, n.Title) + "\n"
	}
	for _, want := range []string{"First note.", "Second note.", "A short line", "Early words", "The last words"} {
		if strings.Count(all, want) != 1 {
			t.Errorf("%q is in the chapters %d times:\n%s", want, strings.Count(all, want), all)
		}
	}
	// each chapter's first note is [^1]
	if strings.Count(all, "[^1]:") != 2 {
		t.Errorf("the notes are numbered again in each file:\n%s", all)
	}
}

// A join is for the end of the text it was made onto. Splitting that chapter would leave the join at the wrong half, so it is set aside
// and listed, and Undo brings it back.
func TestSplitInsideAChapterOthersWereJoinedIntoSetsTheJoinAside(t *testing.T) {
	e := newDraftEnv(t, imports.StageLimits{})
	st, d := e.stageParas(threeChapters())
	alpha, beta := nodeByTitle(t, d.Preview, "Alpha"), nodeByTitle(t, d.Preview, "Beta")
	yes := true
	u := e.mustUpdate(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: d.Item.Revision, Edit: &imports.DraftEdit{Key: beta.Key, JoinPrevious: &yes}})
	l := e.look(st.StageID, alpha.Key, "split", 0, 100)
	at := l.Paragraphs[2].Ordinal
	u = e.mustUpdate(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: u.Item.Revision, Break: &at, BreakFrom: alpha.Key})
	if got := titles(u.Preview); got != "Alpha|Chapter 2|Beta|Gamma" {
		t.Fatalf("beta is a chapter again and the split is made: %s", got)
	}
	if len(u.Note.Dropped) != 1 || u.Note.Dropped[0] != "Beta" || len(u.Item.Edits.Stash) != 1 {
		t.Errorf("the join is set aside and listed: %+v", u.Note)
	}
	u = e.mustUpdate(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: u.Item.Revision, Undo: true})
	if got := titles(u.Preview); got != "Alpha|Gamma" {
		t.Errorf("undo: %s", got)
	}
}

func TestSplitIsNotOfferedAtAPictureOrAContentsPageAndAnImageIsNeverTheBoundary(t *testing.T) {
	e := newDraftEnv(t, imports.StageLimits{})
	paras := corpusCat(one(cPh1("Alpha")), corpusBody(2), one(cp{runs: []string{`<w:r><w:drawing/></w:r>`}}), corpusBody(3))
	st, d := e.stageParas(paras)
	l := e.look(st.StageID, d.Preview.Nodes[0].Key, "split", 0, 100)
	for _, p := range l.Paragraphs {
		if p.Line == "" || strings.Contains(p.Line, "image") {
			t.Errorf("a picture is not a place to cut: %+v", p)
		}
	}
	if l.Total != 4 {
		t.Errorf("one picture of six paragraphs, the first not offered: %d", l.Total)
	}
	// by hand, over REST: the ordinal of the picture is refused, as the list never offers it, and nothing is stored
	img := d.Preview.Nodes[0].Ordinal + 3
	if _, err := e.update(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: d.Item.Revision, Break: &img}); reason(err) != "break_noop" {
		t.Errorf("a break at a picture: %v", err)
	}
}

func TestFrontAndBackMatterForARunIsOneChangeAndOneUndo(t *testing.T) {
	e := newDraftEnv(t, imports.StageLimits{})
	paras := corpusCat(one(cPh1("One")), corpusBody(2), one(cPh1("Two")), corpusBody(2), one(cPh1("Three")), corpusBody(2), one(cPh1("Four")), corpusBody(2), one(cPh1("Five")), corpusBody(2))
	st, d := e.stageParas(paras)
	two, four := nodeByTitle(t, d.Preview, "Two"), nodeByTitle(t, d.Preview, "Four")
	u := e.mustUpdate(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: d.Item.Revision, Matter: &imports.DraftMatter{Key: two.Key, Side: "front"}})
	kinds := func(pv *imports.PreviewOut) string {
		var out []string
		for _, n := range pv.Nodes {
			out = append(out, n.Kind)
		}
		return strings.Join(out, ",")
	}
	if got := kinds(u.Preview); got != "front_matter,front_matter,chapter,chapter,chapter" || u.Note.Code != "matter" || u.Note.Count != 2 || u.Item.Revision != d.Item.Revision+1 {
		t.Fatalf("front: %s %+v rev %d", got, u.Note, u.Item.Revision)
	}
	u = e.mustUpdate(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: u.Item.Revision, Matter: &imports.DraftMatter{Key: four.Key, Side: "back"}})
	if got := kinds(u.Preview); got != "front_matter,front_matter,chapter,back_matter,back_matter" || u.Note.Count != 2 {
		t.Fatalf("back: %s %+v", got, u.Note)
	}
	// nothing to change is said, and stores nothing
	if _, err := e.update(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: u.Item.Revision, Matter: &imports.DraftMatter{Key: four.Key, Side: "back"}}); reason(err) != "matter_none" {
		t.Errorf("again: %v", err)
	}
	// one Undo takes back one change
	u = e.mustUpdate(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: u.Item.Revision, Undo: true})
	if got := kinds(u.Preview); got != "front_matter,front_matter,chapter,chapter,chapter" {
		t.Errorf("undo: %s", got)
	}
	if _, err := e.update(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: u.Item.Revision, Matter: &imports.DraftMatter{Key: four.Key, Side: "middle"}}); code(err) != ops.CodeInvalid {
		t.Errorf("a side that is not one: %v", err)
	}
}

// Parts are left as they are: only the chapters in a run take the kind, and the edits stay valid across a recut.
func TestFrontMatterForARunLeavesPartsAlone(t *testing.T) {
	e := newDraftEnv(t, imports.StageLimits{})
	st, d := stageSample(t, e, "02")
	var last string
	for _, n := range d.Preview.Nodes {
		last = n.Key
	}
	u := e.mustUpdate(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: d.Item.Revision, Matter: &imports.DraftMatter{Key: last, Side: "front"}})
	for _, n := range u.Preview.Nodes {
		if n.Kind == "part" {
			continue
		}
		if n.Kind != "front_matter" {
			t.Errorf("%s is %s", n.Title, n.Kind)
		}
	}
	if u.Note.Count != 4 {
		t.Errorf("the four chapters: %+v", u.Note)
	}
}

func TestAPatternIsTriedBeforeItCutsAndNothingIsTouched(t *testing.T) {
	e := newDraftEnv(t, imports.StageLimits{})
	paras := corpusCat(corpusBody(2), one(cP("Scene: the quay")), corpusBody(3), one(cP("Scene: the mill")), corpusBody(2), one(cP("scene: the road")), corpusBody(2))
	st, d := e.stageParas(paras)
	// a rename first, and the tick: trying a pattern touches neither
	nm := "Mine"
	u := e.mustUpdate(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: d.Item.Revision, Edit: &imports.DraftEdit{Key: d.Preview.Nodes[0].Key, Title: &nm}})
	u = e.mustUpdate(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: u.Item.Revision, Options: map[string]string{"ack_big": "true"}})
	rev := u.Item.Revision
	u = e.mustUpdate(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: rev, Options: map[string]string{"docx_split_try": "scene:"}})
	if u.Preview.Documents != 1 || u.Note.Code != "pattern" || u.Note.Count != 3 || !u.Item.Options.AckBig {
		t.Fatalf("a test cuts nothing, counts, and keeps the tick: %d %+v %+v", u.Preview.Documents, u.Note, u.Item.Options)
	}
	p := u.Preview.Breaks.Try
	if p == nil || p.Count != 3 || len(p.First) != 3 || p.First[0].Line != "Scene: the quay" {
		t.Fatalf("the first matches: %+v", p)
	}
	if got := titles(u.Preview); got != "Mine" {
		t.Errorf("the rename: %s", got)
	}
	// regex mode, case as typed
	u = e.mustUpdate(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: u.Item.Revision, Options: map[string]string{"docx_split_try": `^Scene: the (quay|mill)$`, "docx_split_try_match": "regex"}})
	if u.Preview.Breaks.Try.Count != 2 {
		t.Errorf("regex: %+v", u.Preview.Breaks.Try)
	}
	// choose it: the book is cut at the matches, and what was being tried is no longer
	u = e.mustUpdate(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: u.Item.Revision, Options: map[string]string{"docx_split": "pattern", "docx_split_pattern": `^Scene: the (quay|mill)$`, "docx_split_match": "regex", "docx_split_try": "", "docx_split_try_match": ""}})
	if got := titles(u.Preview); !strings.Contains(got, "Scene: the quay") || u.Preview.Documents != 3 {
		t.Errorf("cut at the pattern: %s (%d)", got, u.Preview.Documents)
	}
	if u.Preview.Breaks.ChosenBy != "explicit" || u.Preview.Breaks.Rule != "pattern" {
		t.Errorf("the way it was cut: %+v", u.Preview.Breaks)
	}
	// Undo, and the word auto never uses a pattern
	u = e.mustUpdate(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: u.Item.Revision, Options: map[string]string{"docx_split": "auto"}})
	if u.Preview.Documents != 1 {
		t.Errorf("auto does not use it: %d", u.Preview.Documents)
	}
}

func TestAPatternThatCannotBeUsedIsRefusedInWords(t *testing.T) {
	e := newDraftEnv(t, imports.StageLimits{})
	st, d := e.stageParas(threeChapters())
	try := func(opts map[string]string, want string) {
		t.Helper()
		_, err := e.update(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: d.Item.Revision, Options: opts})
		if reason(err) != want {
			t.Errorf("%v: %v (want %s)", opts, err, want)
		}
	}
	try(map[string]string{"docx_split": "pattern"}, "pattern_empty")
	try(map[string]string{"docx_split_pattern": strings.Repeat("a", 201)}, "pattern_long")
	try(map[string]string{"docx_split_pattern": "(abc", "docx_split_match": "regex"}, "pattern_invalid")
	try(map[string]string{"docx_split_pattern": "(?<n>x)(?=y)", "docx_split_match": "regex"}, "pattern_invalid") // look-around is not RE2
	try(map[string]string{"docx_split_pattern": "x{1001}", "docx_split_match": "regex"}, "pattern_invalid")
	try(map[string]string{"docx_split_pattern": "x", "docx_split_match": "glob"}, "")
}

// Nested quantifiers are what makes a backtracking engine run away. RE2 does not, but the compile and the matching are timed, on a book
// with a great deal of short text, so the guarantee is measured and not assumed.
func TestPatternHostile(t *testing.T) {
	e := newDraftEnv(t, imports.StageLimits{})
	var paras []cp
	paras = append(paras, cPh1("Alpha"))
	long := strings.Repeat("a", 190) + "!"
	for i := 0; i < 3000; i++ {
		paras = append(paras, cP(long), cP(fmt.Sprintf("short %d", i)))
	}
	st, d := e.stageParas(paras)
	rev := d.Item.Revision
	for _, pat := range []string{
		`(a+)+$`, `(a*)*b`, `(a|aa)+$`, `((a+)+)+!x`, `^(a+)+$`, `(?i)(x+x+)+y`, `a{0,1000}`, `(a{1,30}){1,30}$`,
		"(" + strings.Repeat("a?", 100) + ")" + strings.Repeat("a", 100),
		strings.Repeat("(a|b)", 40),
	} {
		start := time.Now()
		out, err := e.update(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: rev, Options: map[string]string{"docx_split_try": pat, "docx_split_try_match": "regex"}})
		if err == nil {
			rev = out.Item.Revision
		}
		took := time.Since(start)
		t.Run("time", func(t *testing.T) {
			testutil.Timing(t)
			if took > 4*time.Second {
				t.Errorf("%.40q took %v", pat, took)
			}
		})
		if err != nil && code(err) != ops.CodeInvalid && code(err) != ops.CodeConflict {
			t.Errorf("%.40q: %v", pat, err)
		}
	}
	// a pattern that matches every short paragraph: 3,000 matches is more documents than an import takes, said in words and nothing stored
	_, err := e.update(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: rev, Options: map[string]string{"docx_split": "pattern", "docx_split_pattern": "short", "docx_split_match": "starts"}})
	if err == nil || !strings.Contains(err.Error(), "documents") {
		t.Errorf("3,000 matches: %v", err)
	}
	if again := e.get(e.w, st.StageID, false); again.Item.Revision != rev {
		t.Error("a refused change stored something")
	}
	// the 191-character paragraphs are tried (the line is 200 characters or fewer), and a starts-with matches them
	u := e.mustUpdate(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: rev, Options: map[string]string{"docx_split_try": "aaa", "docx_split_try_match": "starts"}})
	if u.Preview.Breaks.Try.Count != 3000 {
		t.Errorf("the 191-character paragraphs are tried and the starts-with matches: %d", u.Preview.Breaks.Try.Count)
	}
}

// A big expression on a book with a great deal of short text is refused before it is tried: said in words for a test, an error for a cut.
func TestPatternTooMuchWorkIsRefusedBeforeItIsTried(t *testing.T) {
	e := newDraftEnv(t, imports.StageLimits{})
	var paras []cp
	line := strings.Repeat("ab", 95)
	for i := 0; i < 10000; i++ {
		paras = append(paras, cP(line))
	}
	st, d := e.stageParas(paras)
	heavy := "(?:a|b){1,50}(?:a|b){1,50}(?:a|b){1,50}c"
	start := time.Now()
	u := e.mustUpdate(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: d.Item.Revision, Options: map[string]string{"docx_split_try": heavy, "docx_split_try_match": "regex"}})
	if p := u.Preview.Breaks.Try; p == nil || p.Refused != "too_much" || p.Count != 0 {
		t.Errorf("a test says why it was not tried: %+v", p)
	}
	if _, err := e.update(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: u.Item.Revision, Options: map[string]string{"docx_split": "pattern", "docx_split_pattern": heavy, "docx_split_match": "regex"}}); err == nil || !strings.Contains(err.Error(), "cannot be tried") {
		t.Errorf("a cut by it is refused in words: %v", err)
	}
	took := time.Since(start)
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		if took > 3*time.Second {
			t.Errorf("refusing took %v: it was tried", took)
		}
	})
}

func TestPatternIsNotTriedOnLongParagraphs(t *testing.T) {
	e := newDraftEnv(t, imports.StageLimits{})
	long := "Chapter " + strings.Repeat("word ", 60)
	st, d := e.stageParas(corpusCat(one(cP(long)), corpusBody(2), one(cP("Chapter five")), corpusBody(2)))
	u := e.mustUpdate(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: d.Item.Revision, Options: map[string]string{"docx_split_try": "chapter", "docx_split_try_match": "starts"}})
	if u.Preview.Breaks.Try.Count != 1 {
		t.Errorf("only the short one: %+v", u.Preview.Breaks.Try)
	}
}

// Many boundaries stay inside the document cap, in words and in time, whichever way they are asked for.
func TestFiveThousandBoundariesStayInsideTheCap(t *testing.T) {
	e := newDraftEnv(t, imports.StageLimits{})
	var paras []cp
	for i := 0; i < 5000; i++ {
		paras = append(paras, cP(fmt.Sprintf("Line number %d of the book, and it is a short one.", i)), cPbreak())
	}
	st, d := e.stageParas(paras)
	if d.Preview.Breaks.SuggestionsTotal < 4900 || len(d.Preview.Breaks.Suggestions) > 400 {
		t.Fatalf("the list is counted and bounded: %d, %d", d.Preview.Breaks.SuggestionsTotal, len(d.Preview.Breaks.Suggestions))
	}
	start := time.Now()
	for _, opts := range []map[string]string{{"docx_split": "page_breaks"}, {"docx_split": "pattern", "docx_split_pattern": "line number", "docx_split_match": "starts"}} {
		_, err := e.update(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: d.Item.Revision, Options: opts})
		if err == nil || !strings.Contains(err.Error(), "2,000") {
			t.Errorf("%v: %v", opts, err)
		}
	}
	// a bulk add of every page break inside the one chapter is refused the same way, and stores nothing
	_, err := e.update(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: d.Item.Revision, Within: &imports.DraftWithin{Key: d.Preview.Nodes[0].Key, Rule: "page_breaks"}})
	if err == nil {
		t.Error("5,000 chapters made by a bulk add")
	}
	took := time.Since(start)
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		if took > 20*time.Second {
			t.Errorf("took %v", took)
		}
	})
	if again := e.get(e.w, st.StageID, false); again.Item.Revision != d.Item.Revision {
		t.Error("a refused change stored something")
	}
}

func longChapter() []cp {
	var ps []cp
	ps = append(ps, cPh1("Short"), cP("A few words."), cPh1("Long"))
	for i := 0; i < 40; i++ {
		ps = append(ps, cP(strings.TrimSpace(strings.Repeat("word ", 400))+fmt.Sprintf(" end%d.", i)))
		switch i % 10 {
		case 3:
			ps = append(ps, cPbreak(), cP("Page title"))
		case 6:
			ps = append(ps, cP("* * *"), cP("After the scene break there is more."))
		case 8:
			ps = append(ps, cPh2("A deeper heading"))
		}
	}
	return ps
}

func TestLookInsideALongChapterListsItsOwnPlacesAndStartsThemAllAtOnce(t *testing.T) {
	e := newDraftEnv(t, imports.StageLimits{})
	st, d := e.stageParas(longChapter())
	long := nodeByTitle(t, d.Preview, "Long")
	if long.Words < 15_000 {
		t.Fatalf("a long chapter: %d", long.Words)
	}
	l := e.look(st.StageID, long.Key, "inside", 0, 100)
	got := map[string]int{}
	for _, f := range l.Found {
		got[f.Rule] = f.Count
	}
	if got["page_breaks"] != 4 || got["headings"] != 4 || got["scenes"] != 4 || got["size"] < 2 || l.Pieces < 2 {
		t.Errorf("found: %+v", l.Found)
	}
	if l.Total < 8 || len(l.Suggest) != min(l.Total, 100) {
		t.Errorf("the places: %d %d", l.Total, len(l.Suggest))
	}
	for i := 1; i < len(l.Suggest); i++ {
		if l.Suggest[i].Ordinal <= l.Suggest[i-1].Ordinal {
			t.Fatal("reading order")
		}
	}
	// the one before it holds no such place
	if short := nodeByTitle(t, d.Preview, "Short"); len(e.look(st.StageID, short.Key, "inside", 0, 100).Found) != 0 {
		t.Error("a short chapter has nothing in it")
	}
	// one of them
	first := l.Suggest[0].Ordinal
	u := e.mustUpdate(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: d.Item.Revision, Break: &first, BreakFrom: long.Key})
	if u.Preview.Documents != 3 {
		t.Fatalf("one: %d", u.Preview.Documents)
	}
	// the chapter the first place made holds the rest of the book's page breaks: all of them as one change
	rest := nodeByTitle(t, u.Preview, u.Preview.Nodes[2].Title)
	u = e.mustUpdate(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: u.Item.Revision, Within: &imports.DraftWithin{Key: rest.Key, Rule: "page_breaks"}})
	if u.Note.Code != "within" || u.Note.Count < 2 || u.Note.Count > 4 {
		t.Errorf("note: %+v", u.Note)
	}
	if lost := d.Preview.Words - u.Preview.Words; lost < 0 || lost > 8 { // the words of the four lines that became titles ("Page title")
		t.Errorf("words: %d, was %d", u.Preview.Words, d.Preview.Words)
	}
	// pieces of the size rule inside the chapter
	last := u.Preview.Nodes[len(u.Preview.Nodes)-1]
	if last.Words >= 15_000 {
		u = e.mustUpdate(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: u.Item.Revision, Within: &imports.DraftWithin{Key: last.Key, Rule: "size"}})
	}
	for _, n := range u.Preview.Nodes {
		if n.Words >= 25_000 {
			t.Errorf("%s is still %d words", n.Title, n.Words)
		}
	}
	// a kind that is not found in a chapter
	if _, err := e.update(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: u.Item.Revision, Within: &imports.DraftWithin{Key: nodeByTitle(t, d.Preview, "Short").Key, Rule: "scenes"}}); reason(err) != "within_none" {
		t.Errorf("none there: %v", err)
	}
	if _, err := e.update(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: u.Item.Revision, Within: &imports.DraftWithin{Key: long.Key, Rule: "everything"}}); code(err) != ops.CodeInvalid {
		t.Errorf("a rule that is not one: %v", err)
	}
}

// Stable keys survive adding and taking away breaks, in any order.
func TestKeysSurviveAddingAndTakingAwayBreaks(t *testing.T) {
	e := newDraftEnv(t, imports.StageLimits{})
	st, d := e.stageParas(threeChapters())
	keys := map[string]bool{}
	for _, n := range d.Preview.Nodes {
		keys[n.Key] = true
	}
	alpha, gamma := nodeByTitle(t, d.Preview, "Alpha"), nodeByTitle(t, d.Preview, "Gamma")
	u := d
	rev := d.Item.Revision
	for _, k := range []string{alpha.Key, gamma.Key, alpha.Key} {
		l := e.look(st.StageID, k, "split", 0, 100)
		at := l.Paragraphs[len(l.Paragraphs)/2].Ordinal
		out, err := e.update(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: rev, Break: &at, BreakFrom: k})
		if err != nil {
			t.Fatal(err)
		}
		rev = out.Item.Revision
		for key := range keys {
			found := false
			for _, n := range out.Preview.Nodes {
				found = found || n.Key == key
			}
			if !found {
				t.Errorf("key %s is gone after a split of %s", key, k)
			}
		}
		_ = u
	}
	// take the splits away by joining with previous: the keys that were there are there
	cur := e.get(e.w, st.StageID, true)
	yes := true
	for len(cur.Preview.Nodes) > 3 {
		var drop string
		for _, n := range cur.Preview.Nodes {
			if !keys[n.Key] {
				drop = n.Key
			}
		}
		out := e.mustUpdate(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: cur.Item.Revision, Edit: &imports.DraftEdit{Key: drop, JoinPrevious: &yes}})
		cur = imports.DraftOut{Item: out.Item, Preview: out.Preview}
	}
	for _, n := range cur.Preview.Nodes {
		if !keys[n.Key] {
			t.Errorf("a key that was not there: %s", n.Key)
		}
	}
}

// A book with nothing to cut by is one document named for the book. The first break makes it "Chapter 1": its key changes, and the
// split is still made, with the words naming the chapter that came before the new one.
func TestSplitAOneDocumentBook(t *testing.T) {
	e := newDraftEnv(t, imports.StageLimits{})
	st, d := e.stageParas(corpusBody(8))
	if d.Preview.Documents != 1 {
		t.Fatalf("one document: %d", d.Preview.Documents)
	}
	key := d.Preview.Nodes[0].Key
	l := e.look(st.StageID, key, "split", 0, 100)
	if l.Total != 7 {
		t.Fatalf("seven places: %+v", l)
	}
	at := l.Paragraphs[3].Ordinal
	u := e.mustUpdate(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: d.Item.Revision, Break: &at, BreakFrom: key})
	if u.Preview.Documents != 2 || u.Note.Code != "split_para" || u.Note.Title == "" || u.Note.Other == "" || u.Note.Ends == "" || u.Note.Starts == "" {
		t.Fatalf("split: %d %+v", u.Preview.Documents, u.Note)
	}
	// the second split, from the first chapter's new key
	l2 := e.look(st.StageID, u.Preview.Nodes[0].Key, "split", 0, 100)
	at2 := l2.Paragraphs[0].Ordinal
	u = e.mustUpdate(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: u.Item.Revision, Break: &at2, BreakFrom: u.Preview.Nodes[0].Key})
	if u.Preview.Documents != 3 {
		t.Errorf("three: %d", u.Preview.Documents)
	}
}

// What the pattern cut previews is what an apply makes, into a new project and into an existing one.
func TestPatternCutIsWhatApplyAndAppendMake(t *testing.T) {
	e := newDraftEnv(t, imports.StageLimits{})
	paras := corpusCat(corpusBody(2), one(cP("Scene: the quay")), corpusBody(3), one(cP("Scene: the mill")), corpusBody(2))
	for _, into := range []bool{false, true} {
		in := imports.StageIn{Kind: "docx", Name: "book.docx"}
		pid := ""
		if into {
			pid = e.newProject(e.w, "Host")
			in.Project = pid
		}
		st := e.mustStage(e.w, in, corpusDoc(paras))
		d := e.get(e.w, st.StageID, true)
		u := e.mustUpdate(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: d.Item.Revision, Options: map[string]string{"docx_split": "pattern", "docx_split_pattern": "scene:", "docx_split_match": "starts"}})
		if u.Preview.Documents != 3 {
			t.Fatalf("preview: %d", u.Preview.Documents)
		}
		var got int
		if into {
			got = e.must(e.w, "import.append", imports.AppendIn{StageID: st.StageID, ProjectID: pid, Rev: u.Item.Revision, Token: u.Item.Token}).(imports.ApplyOut).Documents
		} else {
			got = e.must(e.w, "import.apply", imports.ApplyIn{StageID: st.StageID, Rev: u.Item.Revision, Token: u.Item.Token}).(imports.ApplyOut).Documents
		}
		if got != 3 {
			t.Errorf("into=%v: applied %d, previewed 3", into, got)
		}
	}
}

// Test must never cut: with the pattern already the way the book is cut, trying another changes neither the documents, nor the writer's
// edits, nor the tick, and says what the other matches.
func TestTestWhileThePatternIsInUseCutsNothing(t *testing.T) {
	e := newDraftEnv(t, imports.StageLimits{})
	paras := corpusCat(corpusBody(2), one(cP("Scene: the quay")), corpusBody(3), one(cP("Scene: the mill")), corpusBody(2), one(cP("Other: the road")), corpusBody(2))
	st, d := e.stageParas(paras)
	u := e.mustUpdate(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: d.Item.Revision, Options: map[string]string{"docx_split": "pattern", "docx_split_pattern": "scene:"}})
	if u.Preview.Documents != 3 {
		t.Fatalf("cut by the pattern: %d", u.Preview.Documents)
	}
	nm := "Mine"
	u = e.mustUpdate(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: u.Item.Revision, Edit: &imports.DraftEdit{Key: u.Preview.Nodes[1].Key, Title: &nm}})
	u = e.mustUpdate(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: u.Item.Revision, Options: map[string]string{"ack_big": "true"}})
	before := titles(u.Preview)
	u = e.mustUpdate(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: u.Item.Revision, Options: map[string]string{"docx_split_try": "other:"}})
	if u.Preview.Documents != 3 || titles(u.Preview) != before || !u.Item.Options.AckBig || u.Note.Code != "pattern" || u.Note.Count != 1 || u.Note.Kept != 0 || len(u.Item.Edits.Edits) != 1 {
		t.Fatalf("a test cut something: %d %s (was %s) tick=%v note=%+v", u.Preview.Documents, titles(u.Preview), before, u.Item.Options.AckBig, u.Note)
	}
	if u.Item.Options.DocxPattern != "scene:" || u.Preview.Breaks.Pattern.Count != 2 || u.Preview.Breaks.Try.Count != 1 {
		t.Errorf("the pattern in use is the pattern in use, and the other is only counted: %+v %+v %+v", u.Item.Options, u.Preview.Breaks.Pattern, u.Preview.Breaks.Try)
	}
	if u.Item.CanUndo && false {
		t.Error("unreachable")
	}
}

// A REST stage that says starts is not a change of options later.
func TestStageSaysStartsIsNotAChangeLater(t *testing.T) {
	e := newDraftEnv(t, imports.StageLimits{})
	paras := corpusCat(corpusBody(2), one(cP("Scene: the quay")), corpusBody(2), one(cP("Scenery of the mill")), corpusBody(2))
	st := e.mustStage(e.w, imports.StageIn{Kind: "docx", Name: "book.docx", DocxSplit: "pattern", DocxSplitPattern: "scene", DocxSplitMatch: "starts"}, corpusDoc(paras))
	d := e.get(e.w, st.StageID, true)
	nm := "Mine"
	u := e.mustUpdate(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: d.Item.Revision, Edit: &imports.DraftEdit{Key: d.Preview.Nodes[1].Key, Title: &nm}})
	u = e.mustUpdate(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: u.Item.Revision, Options: map[string]string{"docx_split_pattern": "scene", "docx_split_match": "starts"}})
	if u.Note.Code == "recut" || len(u.Note.Dropped) != 0 || u.Note.Kept != 0 {
		t.Errorf("the same options again are not a recut: %+v", u.Note)
	}
	if titles(u.Preview) != "Chapter 1|Mine|Scenery of the mill" {
		t.Errorf("%s", titles(u.Preview))
	}
}
