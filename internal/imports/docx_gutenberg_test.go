package imports_test

import (
	"os"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/imports"
)

// A Project Gutenberg download, as LibreOffice turns it into a .docx: a header, the START line, the book, then the END line and
// the licence text after it. The licence text is set apart as back matter (one document, with Put back); nothing is lost.

const gutStart = "*** START OF THE PROJECT GUTENBERG EBOOK THE SALT ROAD ***"
const gutEnd = "*** END OF THE PROJECT GUTENBERG EBOOK THE SALT ROAD ***"

var gutLicence = []string{
	gutEnd,
	"Produced by volunteers. This eBook is for the use of anyone anywhere at no cost and with almost no restrictions whatsoever.",
	"Project Gutenberg is a registered trademark of the Project Gutenberg Literary Archive Foundation.",
}

func gutenbergParas() []cp {
	var out []cp
	out = append(out, cP("The Project Gutenberg eBook of The Salt Road, by A. Writer"), cP("Release date: 2026-11-01"), cP(gutStart))
	out = append(out, cPh1("The Gate"))
	out = append(out, corpusBody(3)...)
	out = append(out, cPh1("The Well"))
	out = append(out, corpusBody(3)...)
	for _, l := range gutLicence {
		out = append(out, cP(l))
	}
	return out
}

// gutWords counts the words of the paragraphs that are not headings, as the importer counts them: a token of asterisks is
// markup (the *** around a Gutenberg line), not a word.
func gutWords(paras []cp) int {
	n := 0
	for _, p := range paras {
		if p.style == "" && p.text != "" {
			n += len(gutFields(p.text))
		}
	}
	return n
}

func gutFields(s string) []string {
	var out []string
	for _, f := range strings.Fields(s) {
		if strings.Trim(f, "*") != "" {
			out = append(out, f)
		}
	}
	return out
}

func gutNode(t *testing.T, nodes []imports.Node, title string) imports.Node {
	t.Helper()
	for _, n := range nodes {
		if n.Title == title {
			return n
		}
	}
	t.Fatalf("no document named %q in %v", title, gutTitles(nodes))
	return imports.Node{}
}

func gutTitles(nodes []imports.Node) []string {
	var out []string
	for _, n := range nodes {
		out = append(out, n.Kind+":"+n.Title)
	}
	return out
}

func TestGutenbergLicenceIsSetApartAsBackMatter(t *testing.T) {
	e := newEnv(t)
	paras := gutenbergParas()
	pv := e.corpusPreview(corpusSample{name: "gutenberg", paras: paras}, imports.PreviewIn{})

	back := gutNode(t, pv.Nodes, "Project Gutenberg licence")
	if back.Kind != "back_matter" {
		t.Fatalf("the licence is %s, want back_matter", back.Kind)
	}
	// the licence is the last document, and the last chapter ends with the book's own text
	if last := pv.Nodes[len(pv.Nodes)-1]; last.Key != back.Key {
		t.Errorf("the licence is not the last document: %v", gutTitles(pv.Nodes))
	}
	chapter := gutNode(t, pv.Nodes, "The Well")
	if chapter.Kind != "chapter" || !strings.HasSuffix(chapter.LastLine, "turning back.") {
		t.Errorf("the last chapter should end with the book's text, got %q (%s)", chapter.LastLine, chapter.Kind)
	}
	if !strings.HasPrefix(back.FirstLine, "END OF THE PROJECT GUTENBERG EBOOK") {
		t.Errorf("the back matter starts at the END line, got %q", back.FirstLine)
	}
	// nothing is lost: the words of every document are the words of the file
	total := 0
	for _, n := range pv.Nodes {
		total += n.Words
	}
	if want := gutWords(paras); total != want {
		t.Errorf("words in the documents: %d, want %d (the file's words)", total, want)
	}
	if back.Words != len(gutFields(strings.Join(gutLicence, " "))) {
		t.Errorf("licence words: %d", back.Words)
	}
}

func TestGutenbergLicenceCanBePutBackIntoTheBook(t *testing.T) {
	e := newDraftEnv(t, imports.StageLimits{})
	raw := corpusDoc(gutenbergParas())
	s := e.mustStage(e.w, imports.StageIn{Kind: "docx", Name: "The Salt Road"}, raw)
	d := e.get(e.w, s.StageID, true)
	back := gutNode(t, d.Preview.Nodes, "Project Gutenberg licence")
	if back.Kind != "back_matter" {
		t.Fatalf("the licence is %s", back.Kind)
	}
	yes := true
	e.mustUpdate(e.w, imports.DraftUpdateIn{ID: s.StageID, BaseRevision: 1, Edit: &imports.DraftEdit{Key: back.Key, JoinPrevious: &yes}})
	after := e.get(e.w, s.StageID, true).Preview
	for _, n := range after.Nodes {
		if n.Title == "Project Gutenberg licence" {
			t.Fatalf("the licence is still set apart after Put back: %v", gutTitles(after.Nodes))
		}
	}
	well := gutNode(t, after.Nodes, "The Well")
	if want := d.Preview.Nodes[len(d.Preview.Nodes)-2].Words + back.Words; well.Words != want {
		t.Errorf("the chapter after Put back has %d words, want %d (its own and the licence)", well.Words, want)
	}
}

func TestAPlainManuscriptHasNoLicenceAndKeepsItsWords(t *testing.T) {
	e := newEnv(t)
	data, err := os.ReadFile("testdata/docxcorpus/plain-manuscript.docx")
	if err != nil {
		t.Fatal(err)
	}
	pv := e.previewDocx(data, imports.PreviewIn{Name: "plain-manuscript.docx"})
	for _, n := range pv.Nodes {
		if strings.Contains(n.Title, "Gutenberg") || n.Kind == "back_matter" {
			t.Errorf("a plain manuscript gained a back document: %s %q", n.Kind, n.Title)
		}
	}
	var chapters []string
	total := 0
	for _, n := range pv.Nodes {
		total += n.Words
		if n.Kind == "chapter" {
			chapters = append(chapters, n.Title)
		}
	}
	want := []string{"The Road North", "The Miller's Daughter", "Snow on the Pass", "The House Without Knocking", "What the Cupboard Held", "The Woman at the Window", "The Lantern Keeper", "Down to the Mill"}
	if strings.Join(chapters, "|") != strings.Join(want, "|") {
		t.Errorf("chapters: %v", chapters)
	}
	if total != 892 {
		t.Errorf("words: %d, want 892 (the file's body text, dedication and byline included)", total)
	}
}
