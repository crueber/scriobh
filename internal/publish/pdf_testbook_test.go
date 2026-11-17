package publish

import (
	"fmt"
	"strings"
	"time"

	"github.com/crueber/scriobh/internal/text"
)

// print6x9 is the print preset's options as the compile package defines them.
func print6x9() Options {
	return Options{Format: "pdf", Notes: "none", Ghosts: "omit",
		FrontMatter: True(), BackMatter: True(), TitlePage: True(), PartPages: True(),
		TheEnd: False(), SceneTitles: False(), LinkURLs: False(), Justify: True(), Cover: False(),
		ChapterHeading: "number-title", ChapterLabel: "Chapter", SceneBreak: "ornament",
		Font: "Liberation Serif", FontSize: 11, LineSpacing: 1.1, IndentInches: F(0.2), SpaceAfterPt: F(0),
		PageSize: "6x9", MarginTop: F(0.75), MarginBottom: F(0.85), MarginOutside: F(0.6), MarginInside: F(0.75), Gutter: F(0.15),
		PageNumbers: "footer", ChapterStart: "recto", ChapterDrop: F(1.6), TitleLayout: "book",
		RunningHeads: True(), TOC: True(), CopyrightPage: True(), Footnotes: "page", Language: "en"}
}

const loremWords = "the boat came in at dawn and the whole harbour smelled of salt and tar while gulls wheeled over the quay and old men mended nets on the wall nobody spoke of the winter that had passed she carried a small bag a letter folded into four and the name of a man she had never met the road ran east along the shore past the lime kilns and the drowned fields where the sea had taken back what it had lent they walked until the light failed and then slept under an overturned curragh listening to the tide argue with the stones"

// bookOf builds a deterministic book of about the given number of words:
// front matter, two parts, chapters with scene breaks, quotes, lists, a table,
// footnotes and back matter.
func bookOf(words int) *Book {
	vocab := strings.Fields(loremWords)
	pos := 0
	para := func(n int) string {
		var b strings.Builder
		for i := 0; i < n; i++ {
			w := vocab[pos%len(vocab)]
			pos++
			if i == 0 {
				w = strings.ToUpper(w[:1]) + w[1:]
			}
			b.WriteString(w)
			if i%17 == 16 && i < n-1 {
				b.WriteByte(',')
			}
			if i < n-1 {
				b.WriteByte(' ')
			}
		}
		b.WriteString(".")
		return b.String()
	}
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
	b := &Book{Title: "The Salt Road", Subtitle: "A novel", Byline: "Máire Ní Bhriain", Surname: "Ní Bhriain", Language: "en",
		Identifier: "11111111-2222-5333-8444-555555555555", Modified: time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC), InputHash: "test-input-hash"}
	b.Sections = append(b.Sections,
		sec(SecFront, "Dedication", 0, 1, "*For the ones who walked.*"),
		sec(SecFront, "Foreword", 0, 1, para(60)+"\n\n"+para(80)),
	)
	remaining := words
	ch := 0
	footnote := 0
	for part := 1; part <= 2 && remaining > 0; part++ {
		b.Sections = append(b.Sections, sec(SecPart, fmt.Sprintf("Part %s", map[int]string{1: "One", 2: "Two"}[part]), 0, 1, ""))
		per := words / 2 / 6
		if per < 200 {
			per = 200
		}
		for c := 0; c < 6 && remaining > 0; c++ {
			ch++
			var md strings.Builder
			chapterWords := 0
			for chapterWords < per {
				n := 60 + (pos*7)%90
				p := para(n)
				if footnote%7 == 3 {
					p = strings.Replace(p, ",", fmt.Sprintf(".[^n%d]", footnote), 1)
					fmt.Fprintf(&md, "%s\n\n", p)
					fmt.Fprintf(&md, "[^n%d]: A footnote about *something* in chapter %d, with a few words to wrap onto a second line of the note.\n\n", footnote, ch)
				} else {
					fmt.Fprintf(&md, "%s\n\n", p)
				}
				footnote++
				chapterWords += n
				switch footnote % 9 {
				case 4:
					md.WriteString("* * *\n\n")
				case 6:
					fmt.Fprintf(&md, "> %s\n\n", para(30))
				case 8:
					fmt.Fprintf(&md, "- %s\n- %s\n- %s\n\n", para(8), para(9), para(7))
				}
			}
			title := fmt.Sprintf("The %s Road", strings.ToUpper(vocab[ch%len(vocab)][:1])+vocab[ch%len(vocab)][1:])
			level := 2
			b.Sections = append(b.Sections, sec(SecChapter, title, ch, level, md.String()))
			remaining -= chapterWords
		}
	}
	b.Sections = append(b.Sections, sec(SecBack, "Afterword", 0, 1, para(120)))
	b.Words = words
	return b
}
