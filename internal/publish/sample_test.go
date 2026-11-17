package publish

import (
	"strings"
	"time"

	"github.com/crueber/scriobh/internal/text"
)

// sampleBook is a small book with one of everything: a part, chapters, scene
// breaks, emphasis, a quote, lists, a table, code, a footnote, a link, front
// and back matter.
func sampleBook() *Book {
	notes := 0
	doc := func(md string) ([]text.Node, []text.Footnote) {
		d := text.Structure(md, text.StructOptions{NoteStart: notes})
		notes += len(d.Notes)
		return d.Nodes, d.Notes
	}
	sec := func(kind SectionKind, title string, num, level int, md string) Section {
		n, f := doc(md)
		return Section{Kind: kind, Title: title, Number: num, Level: level, Nodes: n, Notes: f}
	}
	b := &Book{
		Title: "The Salt Road", Subtitle: "A novel", Byline: "Máire Ní Bhriain", Surname: "Ní Bhriain", Language: "en",
		Identifier: "11111111-2222-5333-8444-555555555555",
		Modified:   time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC),
		Contact:    Contact{Name: "Máire Ní Bhriain", Address: []string{"12 Quay Street", "Galway"}, Email: "maire@example.com", Phone: "+353 1 555 0100"},
		Words:      84321,
		CoverSVG:   []byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10"><rect width="10" height="10" fill="#2F5D50"/></svg>`),
	}
	b.Sections = []Section{
		sec(SecFront, "Dedication", 0, 1, "For the ones who walked."),
		sec(SecPart, "Part One", 0, 1, "*Epigraph* of part one."),
		sec(SecChapter, "The Arrival", 1, 2, "The boat came in at *dawn*, and **loud**, with ~~nothing~~ everything.[^a]\n\n"+
			"A second paragraph with a [link](https://example.com/a?b=1&c=2) and `code`.\n\n* * *\n\nAfter the break.\n\n"+
			"> A quoted line\n> over two lines.\n\n- one\n- two\n  - nested\n\n3. three\n4. four\n\n"+
			"| a | b |\n|---|:-:|\n| 1 | 2 |\n\n```\nfn main() {}\n```\n\n[^a]: A *footnote* with text.\n"),
		sec(SecChapter, "", 2, 2, "Untitled chapter text & <b>raw</b> html."),
		sec(SecBack, "Afterword", 0, 1, "Thanks."),
	}
	return b
}

func shunnOptions() Options {
	return Options{Format: "docx", Notes: "none", Ghosts: "omit", FrontMatter: True(), BackMatter: True(), TitlePage: True(), PartPages: True(),
		ChapterHeading: "number-title", ChapterLabel: "Chapter", SceneBreak: "hash",
		Font: "Times New Roman", FontSize: 12, LineSpacing: 2, IndentInches: F(0.5), SpaceAfterPt: F(0), PageSize: "letter", MarginInches: 1,
		Header: "surname-title-page", HeaderUpper: True(), PageNumbers: "header", ChapterStart: "new-page", ChapterDrop: F(3), TitleLayout: "manuscript", WordCount: "about"}
}

func contains2(s string, subs ...string) bool {
	for _, x := range subs {
		if !strings.Contains(s, x) {
			return false
		}
	}
	return true
}
