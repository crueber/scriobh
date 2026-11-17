package imports

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/alts"
	"github.com/crueber/scriobh/internal/testutil"
)

// The projects the budgets are measured on are made here, not read from anyone's
// files: ordinary prose with the formatting a manuscript has.

var scaleWords = strings.Fields("the salt road ran north from the sea past the wall and the old mill where she waited for the bus that never came while rain fell on the grey fields and nobody spoke of what had happened at the crossroads in august")

// prose is n words of repeatable text.
func prose(n, seed int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(scaleWords[(i*7+seed)%len(scaleWords)])
	}
	return b.String()
}

// scrivProject makes a Scrivener 3 project of about words words: chapters of ten
// scenes, each scene with bold and italic runs, an annotation, a footnote and a
// link, a synopsis, and a hundred research notes.
func scrivProject(words int) []File {
	const perScene = 500
	scenes := max(1, words/perScene)
	var files []File
	var binder strings.Builder
	binder.WriteString(`<BinderItem UUID="DRAFT" Type="DraftFolder"><Title>Draft</Title><Children>`)
	head := "{\\rtf1\\ansi\\ansicpg1252\\cocoartf2639\n{\\fonttbl\\f0\\fnil Courier;}\n{\\colortbl;\\red255\\green255\\blue255;}\n\\pard\\f0\\fs24 "
	for s := 0; s < scenes; s++ {
		if s%10 == 0 {
			if s > 0 {
				binder.WriteString(`</Children></BinderItem>`)
			}
			fmt.Fprintf(&binder, `<BinderItem UUID="CH%d" Type="Folder"><Title>Chapter %d</Title><Children>`, s/10, s/10+1)
		}
		id := fmt.Sprintf("S%d", s)
		fmt.Fprintf(&binder, `<BinderItem UUID="%s" Type="Text"><Title>Scene %d</Title><MetaData><IncludeInCompile>Yes</IncludeInCompile><StatusID>2</StatusID></MetaData></BinderItem>`, id, s+1)
		var r strings.Builder
		r.WriteString(head)
		for p := 0; p < 5; p++ {
			r.WriteString(prose(perScene/5/2, s+p))
			r.WriteString(" \\b ")
			r.WriteString(prose(8, p))
			r.WriteString("\\b0  \\i ")
			r.WriteString(prose(8, s))
			r.WriteString("\\i0  ")
			r.WriteString(prose(perScene/5/2-16, p+3))
			if p%2 == 0 {
				r.WriteString("{\\*\\Scrv_annot " + prose(12, p) + "}")
			}
			if p == 3 {
				r.WriteString("{\\footnote \\pard\\plain " + prose(10, s) + "}")
			}
			if p == 4 && s > 0 {
				fmt.Fprintf(&r, `{\field{\*\fldinst{HYPERLINK "scrivlnk://S%d"}}{\fldrslt Scene %d}}`, s-1, s)
			}
			r.WriteString("\\\n")
		}
		r.WriteString("}")
		files = append(files, File{Path: fmt.Sprintf("Big.scriv/Files/Data/%s/content.rtf", id), Data: []byte(r.String())})
		files = append(files, File{Path: fmt.Sprintf("Big.scriv/Files/Data/%s/synopsis.txt", id), Data: []byte("A scene about " + prose(6, s))})
	}
	binder.WriteString(`</Children></BinderItem></Children></BinderItem>`)
	binder.WriteString(`<BinderItem UUID="RES" Type="ResearchFolder"><Title>Research</Title><Children>`)
	for i := 0; i < 100; i++ {
		fmt.Fprintf(&binder, `<BinderItem UUID="R%d" Type="Text"><Title>Note %d</Title></BinderItem>`, i, i)
		files = append(files, File{Path: fmt.Sprintf("Big.scriv/Files/Data/R%d/content.rtf", i), Data: []byte(head + prose(120, i) + "}")})
	}
	binder.WriteString(`</Children></BinderItem>`)
	scrivx := `<ScrivenerProject Version="2.0"><Binder>` + binder.String() + `</Binder><StatusSettings><StatusItems><Status ID="2">First Draft</Status></StatusItems></StatusSettings></ScrivenerProject>`
	return append(files, File{Path: "Big.scriv/Big.scrivx", Data: []byte(scrivx)})
}

// wxrProject makes a WordPress export of about words words.
func wxrProject(words int) []byte {
	const perPost = 800
	posts := max(1, words/perPost)
	var b strings.Builder
	b.WriteString(`<?xml version="1.0"?><rss xmlns:excerpt="http://wordpress.org/export/1.2/excerpt/" xmlns:content="http://purl.org/rss/1.0/modules/content/" xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:wp="http://wordpress.org/export/1.2/"><channel><title>Big</title><wp:wxr_version>1.2</wp:wxr_version>`)
	for i := 0; i < posts; i++ {
		fmt.Fprintf(&b, `<item><title>Post %d</title><dc:creator>a</dc:creator><content:encoded><![CDATA[`, i)
		for p := 0; p < 8; p++ {
			fmt.Fprintf(&b, `<!-- wp:paragraph --><p>%s <em>%s</em> <a href="https://example.org/%d">%s</a> %s</p><!-- /wp:paragraph -->`, prose(40, i+p), prose(6, p), p, prose(5, i), prose(40, p))
		}
		b.WriteString(`<ul><li>one</li><li>two</li></ul>]]></content:encoded><wp:post_id>`)
		fmt.Fprintf(&b, `%d</wp:post_id><wp:post_date>2024-%02d-%02d 10:00:00</wp:post_date><wp:post_name>post-%d</wp:post_name><wp:status>publish</wp:status><wp:post_type>post</wp:post_type>`, i, i%12+1, i%28+1, i)
		b.WriteString(`<wp:comment><wp:comment_content>c</wp:comment_content></wp:comment></item>`)
	}
	b.WriteString(`</channel></rss>`)
	return []byte(b.String())
}

// ghostProject makes a Ghost export of about words words, in lexical.
func ghostProject(words int) []byte {
	const perPost = 800
	posts := max(1, words/perPost)
	type m = map[string]any
	var ps []m
	for i := 0; i < posts; i++ {
		var kids []any
		for p := 0; p < 8; p++ {
			kids = append(kids, m{"type": "paragraph", "children": []any{
				m{"type": "text", "text": prose(40, i+p), "format": 0},
				m{"type": "text", "text": prose(6, p), "format": 2},
				m{"type": "link", "url": "https://example.org/x", "children": []any{m{"type": "text", "text": prose(5, i), "format": 0}}},
				m{"type": "text", "text": prose(40, p), "format": 1},
			}})
		}
		lex, _ := json.Marshal(m{"root": m{"children": kids, "type": "root"}})
		ps = append(ps, m{"id": fmt.Sprint(i), "title": fmt.Sprint("Post ", i), "slug": fmt.Sprint("post-", i), "lexical": string(lex), "type": "post", "status": "published",
			"published_at": fmt.Sprintf("2024-%02d-%02dT10:00:00.000Z", i%12+1, i%28+1)})
	}
	out, _ := json.Marshal(m{"db": []any{m{"meta": m{"version": "5.0.0"}, "data": m{"posts": ps}}}})
	return out
}

// best runs f four times and keeps the fastest, so a busy machine, or a collection that
// lands in one run of a few milliseconds, is not mistaken for a slow program.
func best(f func()) (time.Duration, uint64) {
	d, a := measure(f)
	for i := 0; i < 3; i++ {
		d2, a2 := measure(f)
		d, a = min(d, d2), min(a, a2)
	}
	return d, a
}

// Converting is linear in the size of the project: four times the words take about
// four times as long and as much memory (the check allows six for memory and nine for time, which
// at a few milliseconds a run is noisy: a quadratic converter would take sixteen), and 150,000 words take well under a second.
func TestConvertersAreLinear(t *testing.T) {
	type tc struct {
		name string
		run  func(words int) func()
	}
	prev := func(files []File, layout string) func() {
		return func() {
			s := source{name: "Big", files: files, layout: layout}
			if _, err := preview(s, nil, nil, alts.DefaultLimits); err != nil {
				t.Fatal(err)
			}
		}
	}
	cases := []tc{
		{"scrivener", func(w int) func() { return prev(scrivProject(w), LayoutScrivener) }},
		{"wordpress", func(w int) func() {
			return prev([]File{{Path: "e.xml", Data: wxrProject(w)}}, LayoutWordPress)
		}},
		{"ghost", func(w int) func() {
			return prev([]File{{Path: "e.json", Data: ghostProject(w)}}, LayoutGhost)
		}},
	}
	for _, c := range cases {
		small, mid, big := c.run(100_000), c.run(150_000), c.run(400_000)
		small() // warm up
		ds, as := best(small)
		dm, am := best(mid)
		db, ab := best(big)
		tr, mr := float64(db)/float64(ds), float64(ab)/float64(max(as, 1))
		t.Logf("%s: 100,000 words %v (%d MB), 150,000 words %v (%d MB), 400,000 words %v (%d MB); times %.1f, memory %.1f for four times the words",
			c.name, ds, as, dm, am, db, ab, tr, mr)
		if am > allocBudget(600) {
			t.Errorf("%s: 150,000 words allocated %d MB", c.name, am)
		}
		if mr > 6 {
			t.Errorf("%s is not linear: four times the words took %.1f times the memory", c.name, mr)
		}
		t.Run(c.name+" time", func(t *testing.T) {
			testutil.Timing(t)
			if dm > 10*time.Second*hostileFactor {
				t.Errorf("%s: 150,000 words took %v", c.name, dm)
			}
			if tr > 9 {
				t.Errorf("%s is not linear: four times the words took %.1f times as long", c.name, tr)
			}
		})
	}
}
